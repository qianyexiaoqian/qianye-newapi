package subscription

import (
	"errors"
	"fmt"
	"net/http"
	"strings"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/qianye/db"
	"github.com/QuantumNous/new-api/qianye/guard"
	qymodel "github.com/QuantumNous/new-api/qianye/model"
	"github.com/QuantumNous/new-api/qianye/service/audit"

	"github.com/gin-gonic/gin"
	"gorm.io/gorm"
)

// deletePlanRequest 是删除套餐的请求体。
type deletePlanRequest struct {
	// Force 为 true 时级联失效活跃订阅与待处理订单;false(默认)时只要还有
	// 任何一条就拒绝删除。
	Force bool `json:"force"`
	// Reason 在 Force 为 true 时必填(项目方口径)。级联作废别人已付款的订阅
	// 是本模块破坏力最大的动作,事后复盘的第一个问题永远是"为什么删",
	// 空事由的审计等于没有审计。
	//
	// 默认路径不强制:那一档已经由后端确认过"没有任何人在用",最常见的场景是
	// 删掉一个刚建错的空套餐。在那里也强制填事由,只会逼出"1"、"删除"这类
	// 敷衍字符,并不会让审计更可读 —— 何况前端在无占用时压根不该弹出事由框,
	// 两侧口径一旦不一致,这条最常见的删除路径就 100% 走不通。
	Reason string `json:"reason"`
}

// planImpact 是一次删除会波及到的范围,同时用于拒绝时的报错与审计快照。
type planImpact struct {
	ActiveSubscriptions int64 `json:"active_subscriptions"`
	ActiveUsers         int64 `json:"active_users"`
	PendingOrders       int64 `json:"pending_orders"`
}

func (i planImpact) blocking() bool {
	return i.ActiveSubscriptions > 0 || i.PendingOrders > 0
}

// errPlanInUse 是默认路径(force=false)的拒绝信号,带上具体数字。
var errPlanInUse = errors.New("qy: 套餐仍在使用中")

// adminDeletePlan 删除一个订阅套餐。
//
// ═══════════════════ 一、为什么这件事非做不可 ═══════════════════
//
// 上游没有任何删除套餐的能力 —— 连未挂路由的删除函数都没有。运营手上只有
// "停用",于是废弃套餐会永久堆在列表里。但直接 DELETE 一行会踩两个已知的
// 严重后果,本函数的全部复杂度都用来防它们:
//
//	后果 1(model/subscription.go PreConsumeUserSubscription):
//	  预扣费会遍历用户**全部** active 订阅,对每一条查一次套餐,查不到就
//	  `return err` —— 返回的是整个事务的错误。套餐被删之后,持有它的用户
//	  连同其他套餐的订阅一起用不了,表现为"充了钱也调不通模型"。
//	  → 防法:force 路径把该套餐所有 active 订阅完整作废(状态 + 到期时间 +
//	    分组回落,见 cascadePlan),它们从此不再落进那条 `status = 'active'`
//	    的遍历里;删除提交之后还会再扫一遍,收掉竞态窗口里漏进来的行。
//
//	后果 2(model/subscription.go CompleteSubscriptionOrder):
//	  支付回调在把订单写成 success **之前**查套餐,查不到直接 return err。
//	  该套餐所有未回调的 pending 订单会永久卡死:钱收了,订阅发不出,
//	  而且订单状态一直是 pending,任何重试都走同一条死路。
//	  → 防法:force 路径把 pending 订单一并置为 failed。之后回调命中的是
//	    "订单状态非法"这个明确的终态错误,不再是无限重试的 pending。
//
// 默认路径(force=false)则干脆不让删:只要还有 active 订阅或 pending 订单,
// 就带着具体数字拒绝,让管理员先去处理,而不是替他做决定。
//
// ═══════════════ 二、跨库:为什么不上 twophase ═══════════════
//
// 这一步要动两个库:主库删套餐行,扩展库删名额配置行。本仓有现成的
// qianye/twophase(主库 outbox + 补偿任务),但**这里不值得上**:
//
//	twophase 存在的理由是"钱动了却没有记录"。它的每一个部件 —— outbox 探针、
//	幂等键、补偿扫描、不确定态 —— 都在回答同一个问题:主库的**资金**副作用
//	到底生效没有。而这里跨库的那一半是一行**配置**(plan_id → capacity),
//	它的孤儿态没有任何后果:capacity 只会被 gateSeat 按 plan_id 查询,
//	而一个已经不存在的 plan_id 永远不会被查。孤儿行是死数据,不是不一致。
//
// 因此用最朴素的顺序保证:**先主库、后扩展库**。这个顺序不能反 ——
//
//	主库先删:失败模式是"套餐没了、名额配置还在" → 死数据,零影响。
//	扩展库先删:失败模式是"名额配置没了、套餐还在" → 该套餐**静默变成不限量**,
//	            限量活动当场穿仓。
//
// 退化时的自愈路径有两条,不需要人工进库:
//
//  1. 本接口幂等:套餐已经不在了,再调一次仍然会清扩展库的孤儿行并返回成功。
//     "重试一次删除"就是标准修复动作,而且它顺带把竞态扫尾也再跑一遍。
//  2. 即便一直没人清,孤儿行也只是占几十字节,不参与任何判定 ——
//     capacity 只会被 gateSeat 按 plan_id 查,而一个已经不存在的 plan_id
//     永远不会被查到。
//
// ═══════════════════════ 三、审计 ═══════════════════════
//
// 成功与失败各写一条。只写成功那一条是本仓反复出现的缺陷形状:
// "有人在这一刻试图删掉这个套餐、失败了"与成功同等重要,而攻击者/误操作者
// 最可能反复尝试的恰恰是失败的那一段。before 快照带完整套餐内容 + 受影响范围,
// 因为套餐行删掉之后,这份快照是唯一还能回答"删的是什么"的东西。
func adminDeletePlan(c *gin.Context) {
	if !guard.RequireAPI(c, guard.FlagCore) {
		return
	}
	planId, ok := planIdParam(c)
	if !ok {
		return
	}

	var req deletePlanRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		badRequest(c, "qy_invalid_param", "请求格式错误")
		return
	}
	reason := strings.TrimSpace(req.Reason)
	if req.Force && reason == "" {
		badRequest(c, "qy_subscription_delete_reason_required", "强制删除必须填写事由")
		return
	}
	reason = audit.Truncate(reason, 256)
	if reason == "" {
		reason = "未填写"
	}

	if model.DB == nil {
		internalError(c, db.ErrNotReady)
		return
	}

	var (
		before    string
		impact    planImpact
		deleted   bool
		cascade   cascadeResult
		sweep     cascadeResult
		sweptBack bool
	)
	err := model.DB.Transaction(func(tx *gorm.DB) error {
		var plan model.SubscriptionPlan
		// 行锁:同一秒钟里另一个管理员的编辑、以及正在进行的购买(它会读这一行)
		// 都必须与删除排开。锁的是主键行,不产生区间锁。
		err := model.QyLockForUpdate(tx).Where("id = ?", planId).First(&plan).Error
		if errors.Is(err, gorm.ErrRecordNotFound) {
			// 幂等:套餐已经不在了(上一次删除只完成了主库这一半,或者另一个
			// 管理员刚删过)。不报 404 —— 报 404 会让"重试删除"这条自愈路径
			// 在管理员眼里变成一个错误,他就不会重试了。继续往下走去清扩展库。
			return nil
		}
		if err != nil {
			return err
		}
		impact, err = measurePlanImpact(tx, planId)
		if err != nil {
			return err
		}
		before = snapshotPlan(&plan, impact)
		if !req.Force && impact.blocking() {
			return errPlanInUse
		}
		cascade, err = cascadePlan(tx, planId)
		if err != nil {
			return err
		}
		res := tx.Where("id = ?", planId).Delete(&model.SubscriptionPlan{})
		if res.Error != nil {
			return res.Error
		}
		if res.RowsAffected != 1 {
			return fmt.Errorf("删除套餐 %d 影响了 %d 行,已回滚", planId, res.RowsAffected)
		}
		deleted = true
		return nil
	})

	if err != nil {
		writeDeleteAudit(c, planId, qymodel.ResultFail, "删除订阅套餐失败: "+reason+" / "+err.Error(), before)
		if errors.Is(err, errPlanInUse) {
			c.JSON(http.StatusConflict, gin.H{
				"success": false,
				"code":    "qy_subscription_plan_in_use",
				"message": fmt.Sprintf("该套餐仍有 %d 个活跃订阅(%d 人)、%d 个待处理订单,已拒绝删除;"+
					"确认要一并作废请勾选强制删除",
					impact.ActiveSubscriptions, impact.ActiveUsers, impact.PendingOrders),
				"data": impact,
			})
			return
		}
		internalError(c, err)
		return
	}

	// 缓存失效必须在提交之后、扫尾之前。GetSubscriptionPlanById 有 5 分钟的
	// 混合缓存(启用 Redis 时全局共享,否则单节点内存),不清的话支付回调仍能
	// 从缓存里拿到这个已被删除的套餐,继续给人开订阅 —— 那正好把下面这条扫尾
	// 要收拾的窗口从毫秒级拉长到 5 分钟。
	model.InvalidateSubscriptionPlanCache(planId)

	// 分组回落已经写进库了,缓存还停在升级后的高级组上,不刷的话这些人会继续
	// 按高级组的价格与模型权限跑。必须在事务提交之后刷,提交前刷会把旧值再读回来。
	for _, userId := range cascade.DowngradedUsers {
		model.QyRefreshSubscriptionUserGroupCache(userId, "qianye subscription plan delete")
	}

	// ─────────── 竞态扫尾:删除与"正在进行的购买"之间那道窗口 ───────────
	//
	// 删除事务对套餐行加了 FOR UPDATE,但那**挡不住正在进行的购买** —— 购买侧
	// 读套餐走的是 getSubscriptionPlanByIdTx / GetSubscriptionPlanById,先命中
	// 5 分钟缓存,退化时也只是普通非锁定 SELECT,两者都不与我们的行锁冲突。
	// 于是存在这样一条时序:购买事务在删除提交之前读到了套餐,在删除提交之后
	// 才 INSERT —— 留下一条 status='active'、plan_id 指向已删套餐的订阅。
	//
	// 那条孤儿订阅的后果就是函数头 §1 的"后果 1":该用户此后**每一次**模型调用
	// 的预扣费都会因为查不到套餐而整事务失败,连他别的套餐一起用不了。
	//
	// 所以在缓存失效之后再数一次并补一次级联。这不是加锁,不能给出强保证 ——
	// 缓存已清,新的购买读不到套餐了,剩下的只有"缓存清除那一刻已经在途"的
	// 极窄一批,扫尾把它们收掉。真正的强保证要在闸门里对套餐行加锁定读,
	// 而那会与余额购买的 users→plans 加锁顺序构成死锁环(见 gate.go R1(b)),
	// 被回滚的若是支付回调就是"钱收了、订阅发不出",比这条窗口严重得多。
	// 幂等重试(套餐早就不在了)那一档同样要扫:那正是上一次删除留下竞态行时,
	// 管理员唯一会做的动作。此处不看 force —— 套餐都没了,这些行指向一个不存在的
	// 套餐,留着只会让这些用户的每一次模型调用都失败,作废它们是修复而不是破坏。
	sweep, sweptBack = sweepPlanRace(planId)

	// 扩展库收尾。失败不回滚主库(见函数头 §2:孤儿行零影响),但必须留下痕迹,
	// 否则"为什么这条名额配置还在"事后没人说得清。
	purged := int64(0)
	ctx, cancel := guard.ColdContext(c.Request.Context())
	defer cancel()
	if n, perr := deleteCapacities(ctx, []int{planId}); perr != nil {
		common.SysError(fmt.Sprintf(
			"qianye/subscription: 套餐 %d 已从主库删除,但清理扩展库名额配置失败(孤儿行不影响判定,"+
				"重试一次删除即可自愈): %v", planId, perr))
	} else {
		purged = n
	}

	action := "删除订阅套餐"
	if !deleted {
		action = "订阅套餐已不存在,仅清理扩展库名额配置"
	}
	if sweptBack {
		action += fmt.Sprintf("(删除后扫尾又作废了 %d 条竞态写入的订阅、%d 个订单)",
			sweep.CancelledSubscriptions, sweep.FailedOrders)
	}
	writeDeleteAudit(c, planId, qymodel.ResultOK, action+": "+reason, before)

	respond(c, gin.H{
		"plan_id": planId,
		"deleted": deleted,
		"forced":  req.Force,
		"impact":  impact,
		// 级联的两个数字取的是**实际影响行数**而不是删除前测得的 impact:
		// 两者在竞态下会不一致,而管理员要拿来判断"要人工退多少钱"的是前者。
		"cancelled_subscriptions": cascade.CancelledSubscriptions + sweep.CancelledSubscriptions,
		"failed_orders":           cascade.FailedOrders + sweep.FailedOrders,
		"seat_rows":               purged,
		"already_gone":            !deleted,
		"cache_cleared":           true,
	})
}

// cascadeResult 是一次级联作废的实际影响面。
type cascadeResult struct {
	CancelledSubscriptions int64
	FailedOrders           int64
	// DowngradedUsers 是分组真的被回落了的用户,提交之后要逐个刷缓存。
	DowngradedUsers []int
}

// cascadePlan 把一个套餐的活跃订阅与待处理订单一并作废。调用方保证已在事务内。
//
// 订阅逐条处理而不是一条批量 UPDATE:作废一条订阅从来不只是改 status。上游的
// AdminInvalidateUserSubscription 同时做三件事,缺一不可 ——
//
//	status  → cancelled  置为 cancelled 而不是 expired:expired 会被上游的到期
//	                     降级逻辑当成"自然到期",而这里是运营强行下架。
//	end_time → now       不推的话库里这条被作废的订阅仍显示"未到期",
//	                     管理端与用户端都会把它当成还有效。
//	分组回落             最容易漏、后果最久:被删套餐升过组的用户不回落的话会
//	                     **永久**留在高级分组里 —— 到期扫描只看 status='active',
//	                     回落目标只从 status='expired' 里找,cancelled 两边都不
//	                     命中,系统此后没有任何路径能把他们降回来,只能人工进库。
//
// 订单仍走批量 UPDATE:它没有跨行的派生状态,一条 SQL 就是完整语义。
func cascadePlan(tx *gorm.DB, planId int) (cascadeResult, error) {
	var out cascadeResult
	now := common.GetTimestamp()

	var subs []model.UserSubscription
	if err := tx.Where("plan_id = ? AND status = ?", planId, statusActive).
		Order("id asc").Find(&subs).Error; err != nil {
		return out, err
	}
	for i := range subs {
		sub := &subs[i]
		// 必须显式写 updated_at:GORM 的 BeforeUpdate 钩子在 map 形态的
		// Updates 上改的是那个空结构体,改不到实际发出的 SQL。
		res := tx.Model(&model.UserSubscription{}).Where("id = ?", sub.Id).
			Updates(map[string]any{
				"status":     statusCancelled,
				"end_time":   now,
				"updated_at": now,
			})
		if res.Error != nil {
			return out, res.Error
		}
		out.CancelledSubscriptions += res.RowsAffected
		// 分组回落必须在这一行已经变成 cancelled **之后**做:回落逻辑会去看
		// "这个人还有没有别的 active 升级订阅",这一行还挂着 active 的话
		// 它会认为人家还在升级中而跳过回落。
		target, err := model.QyDowngradeUserGroupForSubscriptionTx(tx, sub, now)
		if err != nil {
			return out, err
		}
		if target != "" {
			out.DowngradedUsers = append(out.DowngradedUsers, sub.UserId)
		}
	}

	res := tx.Model(&model.SubscriptionOrder{}).
		Where("plan_id = ? AND status = ?", planId, common.TopUpStatusPending).
		Updates(map[string]any{
			"status":        common.TopUpStatusFailed,
			"complete_time": now,
		})
	if res.Error != nil {
		return out, res.Error
	}
	out.FailedOrders = res.RowsAffected
	return out, nil
}

// sweepPlanRace 在套餐已删、缓存已清之后再补一次级联,收掉竞态窗口里写进来的行。
//
// 第二个返回值表示"确实收到了东西",供审计与日志区分"扫了一遍没事"和
// "扫出来了" —— 后者意味着刚刚真的发生过一次竞态,值得运营看一眼。
//
// 失败不回滚、不改接口结果:套餐已经删掉了,这一步只是把窗口里漏进来的行收干净。
// 它失败的表现与"没做扫尾"完全一致(留下孤儿订阅),而把已经成功的删除改报失败
// 只会让管理员重试一次删除 —— 那条路径本来就是幂等的,自会再扫一遍。
func sweepPlanRace(planId int) (cascadeResult, bool) {
	var out cascadeResult
	err := model.DB.Transaction(func(tx *gorm.DB) error {
		var err error
		out, err = cascadePlan(tx, planId)
		return err
	})
	if err != nil {
		common.SysError(fmt.Sprintf(
			"qianye/subscription: 套餐 %d 删除后的竞态扫尾失败(可能残留指向已删套餐的订阅,"+
				"重试一次删除即可自愈): %v", planId, err))
		return cascadeResult{}, false
	}
	if out.CancelledSubscriptions == 0 && out.FailedOrders == 0 {
		return out, false
	}
	common.SysError(fmt.Sprintf(
		"qianye/subscription: 套餐 %d 删除后扫尾又作废了 %d 条订阅、%d 个订单 —— "+
			"说明删除与购买发生了竞态,请确认这些用户是否需要退款",
		planId, out.CancelledSubscriptions, out.FailedOrders))
	for _, userId := range out.DowngradedUsers {
		model.QyRefreshSubscriptionUserGroupCache(userId, "qianye subscription plan delete sweep")
	}
	return out, true
}

// measurePlanImpact 数出一个套餐当前波及的范围。
//
// 三个数字各有用途,缺一不可:
//   - ActiveSubscriptions 决定要不要级联,也是拒绝时最直观的那个数;
//   - ActiveUsers 是去重人数,与名额口径同源,让管理员看到"影响多少人"而不只是
//     "多少条记录"(一个人可能持有同一套餐的多条订阅);
//   - PendingOrders 是"钱可能已经在路上"的那一批,它才是默认拒绝的真正理由。
func measurePlanImpact(tx *gorm.DB, planId int) (planImpact, error) {
	var out planImpact
	if err := tx.Model(&model.UserSubscription{}).
		Where("plan_id = ? AND status = ?", planId, statusActive).
		Count(&out.ActiveSubscriptions).Error; err != nil {
		return out, err
	}
	if err := tx.Model(&model.UserSubscription{}).
		Where("plan_id = ? AND status = ?", planId, statusActive).
		Distinct("user_id").
		Count(&out.ActiveUsers).Error; err != nil {
		return out, err
	}
	if err := tx.Model(&model.SubscriptionOrder{}).
		Where("plan_id = ? AND status = ?", planId, common.TopUpStatusPending).
		Count(&out.PendingOrders).Error; err != nil {
		return out, err
	}
	return out, nil
}

// snapshotPlan 把套餐内容与波及范围压成审计快照。
//
// 套餐行删掉之后这份快照是唯一还能回答"删的是什么、当时有多少人在用"的东西,
// 因此它带的是**完整**套餐而不是几个挑出来的字段。
func snapshotPlan(plan *model.SubscriptionPlan, impact planImpact) string {
	b, err := common.Marshal(map[string]any{
		"plan":   plan,
		"impact": impact,
	})
	if err != nil {
		return "<snapshot marshal failed: " + err.Error() + ">"
	}
	return string(b)
}

// writeDeleteAudit 是删除路径的审计出口,成功与失败共用。
func writeDeleteAudit(c *gin.Context, planId int, result, reason, before string) {
	audit.Write(c, audit.Entry{
		Category:    qymodel.AuditCategoryConfig,
		Action:      "subscription.plan.delete",
		ActorType:   qymodel.ActorAdmin,
		ActorUserId: c.GetInt("id"),
		ActorName:   c.GetString("username"),
		Result:      result,
		Reason:      reason,
		BeforeSnap:  before,
		// 删除没有 after。刻意留空而不是写 "{}":审计详情里 after 为空正是
		// "这个对象不再存在"的表达。
		AfterSnap: "",
	})
}
