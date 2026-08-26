package commission

// api_admin_adjust.go —— 管理员手工增减某个人的佣金。
//
// # 为什么它落成一条账目行,而不是把余额列改掉
//
// qy_commission_balance 上的每一个数字都是**派生量**,由三张表咬合出来:
//
//	Σ(accrual.gross_amount)                = Σ(accrual.settled_amount) + 尚未被吸收的增量
//	Σ(accrual.settled_amount)              = Σ(settlement.granted − settlement.reclaimed) + balance.unsettled_amount
//	balance.total_earned − total_clawback  = Σ(settlement.granted − settlement.reclaimed)
//	                                       = balance.available + frozen + withdrawn
//
// 直接 UPDATE 余额列(或者反过来,直接 DELETE 一条计佣行)会当场打破其中一条,
// 而没有任何一行流水能解释差额 —— 上一轮实测里"直接删计佣行导致 Σaccrual 与
// Σsettlement 对不上"就是这么来的。同样的错不再犯第二次。
//
// 所以手工调整走的是账本本来就有的那条路:**写一条 source_type = manual 的
// accrual**,金额可正可负,mature_at 为 0(立即成熟),然后让既有的结算流程
// (settleUser)把它吸收进余额。恒等式在每一步都成立,而且这一笔在佣金流水页上
// 有单号、有理由、有操作人、有时间,和其它任何一笔佣金一样可追溯。
//
// 这一路没有下线也没有费率:invitee_id 落 0、rate_bps 落 0。它既不来自某一笔
// 消费/充值,也不按任何比例算出来。invitee_id 落 0 还有一个实际好处 ——
// clawback 的可冲正上限按 invitee_id 汇总,手工调整不会污染任何一个下线的额度。
//
// # 下界:减到负数必须被拒,而不是悄悄变成欠账
//
// computeSettlement 本身是安全的:回收额被夹在可提现余额内,超出部分记成负余数
// (欠账)。也就是说即使不做任何校验,余额也不会变成负数。但"运营点了减 5000,
// 系统悄悄给这个人记了一笔 3000 的欠账"不是可接受的结果 —— 欠账会冻结提现,
// 而运营完全不知道自己制造了它。所以这里显式算出**可回收上限**并在越界时 400:
//
//	可回收上限 = 可提现额度 + 未结算余数 + 尚未被结算吸收的应发佣金
//
// 这三项恰好是"这个人最终会拿到手的钱";已冻结(提现审核中)与已提现的部分
// 不在其中 —— 那些钱要收回必须走提现模块或冲正,不是这个接口能碰的。

import (
	"context"
	"errors"
	"net/http"
	"strings"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/qianye/db"
	"github.com/QuantumNous/new-api/qianye/guard"
	qymodel "github.com/QuantumNous/new-api/qianye/model"
	"github.com/QuantumNous/new-api/qianye/service/audit"

	"github.com/gin-gonic/gin"
	"github.com/shopspring/decimal"
	"gorm.io/gorm"
)

var (
	// errAdjustOverReclaimable 是最常撞到的一条:要减的比这个人最终会拿到的还多。
	errAdjustOverReclaimable = errors.New("扣减额度超过可回收上限(可提现 + 未结算余数 + 待结算佣金)")
	// errAdjustOverflow 守另一个方向:加完之后的可提现会超过单账户额度上限,
	// 而这些额度最终要流向主库的额度列,受 common.MaxQuota 约束。
	errAdjustOverflow = errors.New("增加后的可提现佣金会超过单账户额度上限")
	// errAdjustUserMissing 挡住手滑打错 user_id 凭空建出一行余额。
	errAdjustUserMissing = errors.New("该用户不存在(或已被删除)")
	// errAdjustSelfDealing 挡住"给自己记一笔佣金"。见 guard/fund_actor.go:
	// 这是自铸 → 自提 → 自批那条链的第一环,也是最便宜的一环。
	errAdjustSelfDealing = errors.New("不能调整自己的佣金,请由另一位管理员操作")
	// errAdjustTargetPeer 挡住"给同级或更高级的管理员记一笔佣金",
	// 与上游 canManageTargetRole 同一条判据。
	errAdjustTargetPeer = errors.New("无权调整同级或更高权限账号的佣金")
	// errAdjustIdemConflict 表示同一个 client_request_id 换了参数重放。
	errAdjustIdemConflict = errors.New("该请求标识已被另一次调整占用,请刷新后重新发起")
)

var adjustErrCodes = map[error]string{
	errAdjustOverReclaimable: "qy_adj_over_reclaimable",
	errAdjustOverflow:        "qy_adj_overflow",
	errAdjustUserMissing:     "qy_adj_user_not_found",
	errAdjustSelfDealing:     "qy_self_dealing",
	errAdjustTargetPeer:      "qy_target_not_manageable",
	errAdjustIdemConflict:    "qy_idem_key_conflict",
}

// manualAdjustInput 是一次手工调整的全部参数。
type manualAdjustInput struct {
	UserId     int
	Delta      int64
	Reason     string
	OperatorId int
	// OperatorRole 是操作人在**本次请求**里的角色,由 middleware.AdminAuth()
	// 写进上下文。它与 OperatorId 一起决定这次调整有没有资格发生,见
	// guard.SelfDealing / guard.ManageableTarget。
	OperatorRole int
	ClientId     string
}

// adjustOutcome 是一次手工调整的结局。
type adjustOutcome struct {
	AccrualNo string
	// Created 为假表示这是同一个 client_request_id 的幂等重放,账本没有再变动。
	Created bool
	Before  Balance
	// Ceiling 是当刻算出的可回收上限,回显给前端(它决定了减号方向能填多大)。
	Ceiling int64
}

// adminAdjustCommission 手工增加/减少某个用户的佣金。
func adminAdjustCommission(c *gin.Context) {
	if !guard.RequireAPI(c, guard.FlagCommission) {
		return
	}
	var req struct {
		UserId int `json:"user_id"`
		// 指针:0 必须被当成"填了个没有意义的值"而不是"根本没填",
		// 两者要给出不同的提示。
		DeltaQuota      *int64 `json:"delta_quota"`
		Reason          string `json:"reason"`
		ClientRequestId string `json:"client_request_id"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		badRequest(c, "qy_invalid_param", "请求格式错误")
		return
	}
	if req.UserId <= 0 {
		badRequest(c, "qy_invalid_param", "必须指定 user_id")
		return
	}
	if req.DeltaQuota == nil {
		badRequest(c, "qy_invalid_param", "必须给出调整额度")
		return
	}
	delta := *req.DeltaQuota
	if delta == 0 {
		badRequest(c, "qy_invalid_param", "调整额度不能为 0")
		return
	}
	if delta > int64(common.MaxQuota) || delta < -int64(common.MaxQuota) {
		badRequest(c, "qy_invalid_param", "单次调整额度必须在正负额度上限之内")
		return
	}
	reason, ok := requireReason(c, req.Reason)
	if !ok {
		return
	}
	clientId := strings.TrimSpace(req.ClientRequestId)
	if clientId == "" {
		// 幂等键必填:加钱/减钱的接口没有幂等键时,一次网络超时重试就是第二笔。
		badRequest(c, "qy_invalid_param", "缺少 client_request_id")
		return
	}

	ctx := c.Request.Context()
	out, err := applyManualAdjust(ctx, manualAdjustInput{
		UserId:       req.UserId,
		Delta:        delta,
		Reason:       reason,
		OperatorId:   c.GetInt("id"),
		OperatorRole: c.GetInt("role"),
		ClientId:     clientId,
	})
	if err != nil {
		// 审计落在事务之外,否则它会跟着一起回滚 —— 而"有人在这一刻试图给某个人
		// 加/减佣金、失败了"正是最需要留痕的事实。AmountQuota 记 0:
		// 这一路账本上什么都没发生,写上意图金额会让审计看起来像是真动了钱。
		snap := currentBalance(ctx, req.UserId)
		writeAdjustAudit(c, req.UserId, 0, qymodel.ResultFail,
			"手工调整佣金失败(事务已回滚): "+err.Error()+" | 事由: "+reason, snap, snap, "")
		respondAdjustError(c, err)
		return
	}

	// 落账之后立刻结算一次,让余额马上反映这笔调整。失败不影响正确性:
	// 这条 accrual 的 mature_at 是 0,下一轮定时结算(pendingInviters 的第一路)
	// 一定会把它捞出来。所以这里只告警,不回滚、也不改结局。
	if out.Created {
		if err := settleOne(req.UserId); err != nil {
			warnf("手工调整佣金后立即结算失败(下一轮定时结算会补上): 用户 %d: %v", req.UserId, err)
		}
	}

	applied := int64(0)
	if out.Created {
		applied = delta
	}
	after := currentBalance(ctx, req.UserId)
	// 金额取**实际落账**的那个数,绝不用请求里的 delta:幂等重放时账本上
	// 什么都没有再发生,而审计表是这套资金系统事后仲裁的唯一凭据。
	writeAdjustAudit(c, req.UserId, applied, qymodel.ResultOK,
		adjustAuditReason(out.Created, delta, reason), out.Before, after, out.AccrualNo)

	respond(c, gin.H{
		"user_id":             req.UserId,
		"delta_quota":         applied,
		"created":             out.Created,
		"accrual_no":          out.AccrualNo,
		"reclaimable_ceiling": out.Ceiling,
		"before":              resolvedBalanceView(c.Request.Context(), out.Before),
		"after":               resolvedBalanceView(c.Request.Context(), after),
	})
}

// adjustAuditReason 拼审计正文。幂等重放要写清楚"这次什么都没发生",
// 否则事后看到两条成功记录会以为发了两次。
func adjustAuditReason(created bool, delta int64, reason string) string {
	if !created {
		return "手工调整佣金(幂等重放,账本未再变动): " + reason
	}
	verb := "增加"
	if delta < 0 {
		verb = "减少"
	}
	return "手工" + verb + "佣金(已落一条 manual 计佣行,由结算流程吸收): " + reason
}

// applyManualAdjust 在一个事务里完成:取余额行锁 → 幂等判定 → 上下界校验 → 落账目行。
//
// 四件事必须在同一把锁下:锁外做校验时,结算任务或提现冻结可以在校验与写入之间
// 把可提现搬走,那时这条负额行落下去就会变成一笔谁都没批准的欠账。
func applyManualAdjust(ctx context.Context, in manualAdjustInput) (*adjustOutcome, error) {
	if err := requireAdjustableTarget(ctx, in); err != nil {
		return nil, err
	}
	gdb := db.Get()
	if gdb == nil {
		return nil, db.ErrNotReady
	}

	// 幂等键把操作人也算进去:两个管理员各自的前端生成的 client_request_id
	// 是各自的随机串,撞上的概率不高但没有任何理由留着这个可能。
	idemKey := SourceManual + ":" + itoa(in.OperatorId) + ":" + in.ClientId
	out := &adjustOutcome{}

	// 法币折算比例按**被调整的这个人**(他就是这条 manual 行的 inviter)的
	// 分组解析,与自动计佣同口径 —— 走同一个入口 resolveInviterPricing,
	// 不在这里另写一次"取谁的分组"。不解析的话 writeAccrualTx 会取全站充值
	// 汇率,于是同一个人的自动佣金按分组档折算、手工补的那一笔按另一个比例
	// 折算,available_fiat 里从此混着两种价 —— 而手工调整正是事后最需要说得
	// 清的一笔。
	//
	// 这条路只取 Fiat:manual 行不写 rate_units/rate_group(金额由运营直接
	// 给出,不是 base × rate 算出来的),所以 source 传什么都不影响结果,
	// 如实传 SourceManual。
	//
	// 刻意在开事务之前解析:它要读主库 users.group,而事务里握着
	// qy_commission_balance 的行锁,跨库读绝不能发生在锁内。
	fiat := resolveInviterPricing(ctx, in.UserId, SourceManual, effectiveCtx(ctx)).Fiat

	err := gdb.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		bal, err := lockBalance(tx, in.UserId)
		if err != nil {
			return err
		}
		out.Before = *bal

		// 先查幂等命中,再做上下界校验。顺序不能反:重放时账本上早就有那一行了,
		// 拿此刻的余额重新校验会因为"上一次已经扣过"而失败,运营会以为自己
		// 从来没成功过,于是换个数再发一次 —— 那才是真的扣两遍。
		var existing []Accrual
		if err := tx.Where("idem_scope = ? AND idem_key = ?",
			SourceManual, normalizeIdemKey(idemKey)).Limit(1).Find(&existing).Error; err != nil {
			return err
		}
		if len(existing) > 0 {
			if existing[0].InviterId != in.UserId || existing[0].BaseQuota != in.Delta {
				return errAdjustIdemConflict
			}
			out.AccrualNo = existing[0].AccrualNo
			out.Created = false
			out.Ceiling = 0
			return nil
		}

		ceiling, err := reclaimableCeiling(tx, bal)
		if err != nil {
			return err
		}
		out.Ceiling = ceiling
		// 一条式子同时守住两个方向:调整之后"这个人最终会拿到的钱"
		// 必须落在 [0, 单账户额度上限] 之内。
		eventual := ceiling + in.Delta
		if eventual < 0 {
			return errAdjustOverReclaimable
		}
		if eventual > int64(common.MaxQuota) {
			return errAdjustOverflow
		}

		inserted, err := writeAccrualTx(tx, accrualInput{
			SourceType: SourceManual,
			IdemKey:    idemKey,
			SourceRef:  "OP" + itoa(in.OperatorId),
			InviterId:  in.UserId,
			// 没有下线:这笔钱不是从谁的消费里分出来的。
			InviteeId: 0,
			// BaseQuota 冻结"管理员这一次填了多少",充当幂等指纹的金额分量,
			// 与 manualClawback 同一手法。
			BaseQuota: in.Delta,
			Gross:     decimal.NewFromInt(in.Delta),
			UsdRate:   fiat.Rate,
			// 立即成熟:手工调整不是从下线消费里分出来的钱,没有任何理由陪着
			// 成熟期等 —— 那只会让运营以为接口没生效。
			MatureAt: 0,
			Status:   StatusAccrued,
			Remark:   truncate("手工调整: "+in.Reason, 255),
		})
		if err != nil {
			return err
		}
		if !inserted {
			// 上面刚查过不存在,这里却冲突了 = 同一个幂等键的并发重放。
			// 回 409 而不是假装成功:资金侧执行的是另一次请求的参数。
			return errAdjustIdemConflict
		}
		var created []Accrual
		if err := tx.Where("idem_scope = ? AND idem_key = ?",
			SourceManual, normalizeIdemKey(idemKey)).Limit(1).Find(&created).Error; err != nil {
			return err
		}
		if len(created) == 0 {
			return errors.New("commission: 手工调整已写入但回读不到")
		}
		out.AccrualNo = created[0].AccrualNo
		out.Created = true
		return nil
	})
	if err != nil {
		return nil, err
	}
	return out, nil
}

// reclaimableCeiling 返回"这次最多能扣掉多少而不制造欠账",即手工扣减的上限。
//
//	可提现额度 + 未结算余数 + **已成熟**且尚未被结算吸收的应发佣金
//
// 三项刻意都是"紧接着这一次结算就能被抵掉的钱":手工调整落账后立刻会跑一次
// settleUser,而那一批捞的正是 mature_at <= now 的行 —— 我们这条负额行(mature_at=0)
// 与那些已成熟的正额行会在**同一批**里相加,所以它们能互相抵消。
//
// **未成熟**的应发佣金不能算进来,哪怕它金额很大:它进不了这一批,
// computeSettlement 只能把回收额夹在可提现内、把超出部分记成负余数 ——
// 也就是一笔运营根本不知道自己制造了的欠账,而欠账会冻结这个人的提现。
// 已冻结(提现审核中)与已提现的部分同理不算:那些钱要收回必须走提现模块或冲正。
//
// 已知的窄缝:settleUser 单轮最多吸收 settleAccrualBatch 行,某个邀请人同时挂着
// 上千条已成熟未结算的行时,我们这条(id 最大)可能落在批外,于是本轮抵不掉。
// 结果仍然是安全的(余额不会为负),只是那笔抵扣要等下一轮。
func reclaimableCeiling(tx *gorm.DB, bal *Balance) (int64, error) {
	// 两次查询而不是新写一个"只算已成熟"的谓词:全部 − 未成熟 = 已成熟,
	// 复用的是 sumOutstanding 里那个已经被结算路径验证过的 WHERE。
	all, err := sumOutstanding(tx, bal.UserId, false)
	if err != nil {
		return 0, err
	}
	unmatured, err := sumOutstanding(tx, bal.UserId, true)
	if err != nil {
		return 0, err
	}
	total := decimal.NewFromInt(bal.AvailableQuota).
		Add(bal.UnsettledAmount).
		Add(all.Sub(unmatured)).
		Floor()
	ceiling := int64(common.QuotaFromDecimal(total))
	if ceiling < 0 {
		// 已有欠账。此时任何扣减都该被拒 —— 再扣只会把欠账做得更深。
		return 0, nil
	}
	return ceiling, nil
}

// requireAdjustableTarget 回答"这个管理员现在有资格给这个账号记一笔佣金吗"。
//
// 三问合一,共用同一次主库查询:
//
//  1. 目标账号真的存在吗 —— 不问的话,把 user_id 打错一位就会在
//     qy_commission_balance 里凭空建出一行余额(lockBalance 不存在即建),
//     而那一行永远不会有人来认领;
//  2. 操作人是不是就是受益人 —— 这是"自铸佣金 → 自己发起提现 → 自己批准"
//     那条链的起点(见 guard/fund_actor.go);
//  3. 受益人是不是同级或更高权限的账号 —— 与上游 canManageTargetRole 对齐,
//     堵掉两个管理员互相记账的闭环。
//
// 自营与越级都在**开事务之前**判掉:它们与余额、幂等键都无关,越早拒绝,
// qy_commission_balance 上那把行锁被握住的时间越短。
func requireAdjustableTarget(ctx context.Context, in manualAdjustInput) error {
	// 判据与角色回查都在 guard.ActorMayActOn —— 本模块另外四条动钱接口
	// (已提现迁移、绑定/换绑邀请关系、手动结算)走的是同一个函数。这里只
	// 把哨兵错误翻回本模块的错误码表,因为 adjustErrCodes 已经是这三种情形
	// 对外的 code 契约,换成 guard 的哨兵会改掉前端认的字符串。
	switch err := guard.ActorMayActOn(ctx, in.OperatorId, in.OperatorRole, in.UserId); {
	case err == nil:
		return nil
	case errors.Is(err, guard.ErrActorIsTarget):
		return errAdjustSelfDealing
	case errors.Is(err, guard.ErrTargetMissing):
		return errAdjustUserMissing
	case errors.Is(err, guard.ErrTargetNotLower):
		return errAdjustTargetPeer
	default:
		return err
	}
}

// writeAdjustAudit 落一条手工调整审计,成功与失败共用。
//
// 快照走 balanceView:它带着派生可提现与漂移量,那正是判断"这次调整有没有
// 把账本改坏"要看的两个数。TraceNo 挂账目单号,审计与账本行互相指得回去。
func writeAdjustAudit(c *gin.Context, userId int, delta int64, result, reason string,
	before, after Balance, accrualNo string) {
	beforeSnap, _ := common.Marshal(resolvedBalanceView(c.Request.Context(), before))
	afterSnap, _ := common.Marshal(resolvedBalanceView(c.Request.Context(), after))
	audit.Write(c, audit.Entry{
		TraceNo:      accrualNo,
		Category:     qymodel.AuditCategoryCommission,
		Action:       "commission.balance.manual_adjust",
		ActorType:    qymodel.ActorAdmin,
		ActorUserId:  c.GetInt("id"),
		ActorName:    c.GetString("username"),
		TargetUserId: userId,
		AmountQuota:  delta,
		Result:       result,
		Reason:       reason,
		BeforeSnap:   string(beforeSnap),
		AfterSnap:    string(afterSnap),
	})
}

func respondAdjustError(c *gin.Context, err error) {
	if code, ok := adjustErrCodes[err]; ok {
		status := http.StatusBadRequest
		switch {
		case errors.Is(err, errAdjustIdemConflict):
			status = http.StatusConflict
		case errors.Is(err, errAdjustSelfDealing), errors.Is(err, errAdjustTargetPeer):
			// 403 而不是 400:参数没问题,是**这个人**不该做这件事。
			// 前端据此提示"换一位管理员操作",而不是让他改数字重试。
			status = http.StatusForbidden
		}
		respondFail(c, status, code, err.Error())
		return
	}
	db.MarkFailure(err)
	internalError(c, err)
}

// registerAdjustRoutes 挂载手工增减佣金。
//
// 只有一条路由,仍然单独放一个注册函数:module.go 那份清单与处理器分两处
// 维护时,"加了处理器忘了挂"是本仓反复出现的形状。
// 直接改钱,必须挂关键操作限流。
func registerAdjustRoutes(g *gin.RouterGroup, crit gin.HandlerFunc) {
	g.POST("/commission/balances/adjust", crit, adminAdjustCommission)
}
