package mall

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"net/http"
	"sort"
	"strconv"
	"strings"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/qianye/config"
	"github.com/QuantumNous/new-api/qianye/db"
	"github.com/QuantumNous/new-api/qianye/guard"
	qymodel "github.com/QuantumNous/new-api/qianye/model"
	"github.com/QuantumNous/new-api/qianye/modules/planentitlement"
	"github.com/QuantumNous/new-api/qianye/modules/stardust"
	"github.com/QuantumNous/new-api/qianye/service/audit"
	"github.com/QuantumNous/new-api/qianye/service/twophase"

	"gorm.io/gorm"
)

// plan.go —— kind=plan 的下单:跨库两阶段(design §6.3)。
//
// 三种商品里只有套餐要动主库(发订阅),它是本模块唯一一条 twophase 路径:
//
//	扩展库事务①(LocalDetail)  锁商品行 + 限购 + 扣星屑 + 订单 paid
//	主库事务②(MainApply)      复核套餐可售 / 用户可买 / 顶替集合 → 发订阅 → 写 subscription_orders
//	提交后(AfterCommit)       刷分组缓存 + 套餐解锁缓存失效 + 主库 RecordLog
//	扩展库事务③(LocalCommit)  订单 paid → done,回填 user_subscription_id / sub_renewed
//
// 失败与对账的全部判据只有一条:**Failed 且探针说 MainNotApplied 才退星屑**,其余一律
// 挂 held 交对账任务 / root 裁决 —— 与 transfer/service.go 的 releaseOnFailure 同一口径。

const (
	// tradeNoPrefix 拼进主库 subscription_orders.trade_no:SUBSD + 商城订单号。
	// Resolver 按它回读 provider_payload 回填订阅 id,所以它在 LocalDetail 阶段就定死。
	tradeNoPrefix = "SUBSD"
	// subscriptionSource 是 CreateUserSubscriptionFromPlanTx 的 source。它**不在**
	// isPaidSubscriptionSource 里:限购 / 名额 / 同组永久三条会在主库事务内拒绝 →
	// 回滚 → Failed → 退星屑。商城是第一条能在事务内安全拒绝的购买路径。
	subscriptionSource = "stardust"
	// subscriptionSourceLottery 是抽奖中奖生成的套餐订单发订阅时的 source。它同样不在
	// isPaidSubscriptionSource 里,而且**不在** stardust 套餐返的来源闭集里
	// (stardust.PlanReward.SourceAllowed 只认 order / balance / admin / redemption):
	// 项目方原话"这个套餐抽中的激活不要触发星屑返还"。
	subscriptionSourceLottery = "lottery"
	// refTypeMallOrder 是资金单与流水上的关联类型。
	refTypeMallOrder = "mall_order"
)

// errUserUnavailable:用户行在主库里查不到或不是 enabled。纵深防御 —— 能走到下单的
// 会话本来就是 enabled 的,这里挡的是"预检与主库事务之间被禁用"那一小段窗口。
var errUserUnavailable = newBizError(http.StatusForbidden, "qy_ml_user_unavailable",
	"账号当前状态不允许购买套餐")

// planOrderInput 是一次套餐下单的全部输入(已通过 handler 校验)。
type planOrderInput struct {
	UserId int
	// ClientRequestId 已经过 NormalizeIdemClientKey 折叠。
	ClientRequestId string
	Product         *Product
	// ExpectAction / ExpectSuperseded 是用户在商品页确认过的顶替后果(契约 §4)。
	ExpectAction     string
	ExpectSuperseded []string
}

// planPreview 是商品详情里的 preview(契约 §4):这一单会新开 / 续期 / 顶替 / 拒绝,
// 以及全站名额还有没有位置。
type planPreview struct {
	Action           string   `json:"action"`
	SupersededGroups []string `json:"superseded_groups"`
	SeatAvailable    bool     `json:"seat_available"`
	Reason           string   `json:"reason"`
}

// planGrant 是主库事务②交给扩展库事务③的结果:发了哪条订阅、是不是续期;
// 奖品单还带回锁内算出的顶替结论(Action / Superseded),写进订单供事后解释。
type planGrant struct {
	SubId      int
	Renewed    bool
	Action     string
	Superseded string
}

// previewPlan 算出商品详情里的 preview。它只读、不锁,判据与 precheckPlan 同源。
//
// 套餐不存在 / 已停用 / 不在窗时 action 一律 reject 并把原因写进 reason:
// 用户看到的是"为什么不能买",而不是一个灰掉的按钮。
func previewPlan(userId int, p *Product) *planPreview {
	out := &planPreview{Action: "reject", SupersededGroups: make([]string, 0), SeatAvailable: true}
	if !config.Get().TwoPhase.OutboxEnabled() {
		out.Reason = errPlanNeedsOutbox.Message()
		return out
	}
	plan, err := model.GetSubscriptionPlanById(p.PlanId)
	if err != nil {
		out.Reason = "套餐不存在"
		return out
	}
	if !plan.Enabled {
		out.Reason = "套餐已下架"
		return out
	}
	if err := model.PlanSaleWindowError(plan, common.GetTimestamp()); err != nil {
		out.Reason = err.Error()
		return out
	}
	if err := model.QyGateSubscriptionSeat(nil, plan, userId, "precheck", nil); err != nil {
		out.SeatAvailable = false
		out.Reason = err.Error()
	}
	if plan.MaxPurchasePerUser > 0 {
		n, err := model.CountUserSubscriptionsByPlan(userId, plan.Id)
		if err == nil && n >= int64(plan.MaxPurchasePerUser) {
			out.Reason = "已达到该套餐购买上限"
			return out
		}
	}
	pv, err := model.PreviewUserGroupPurchase(userId, plan)
	if err != nil {
		out.Reason = "无法预览购买后果,请稍后重试"
		return out
	}
	out.Action = pv.Action
	out.SupersededGroups = sortedGroups(pv.SupersededGroups)
	if out.Reason == "" {
		out.Reason = pv.Message
	}
	return out
}

// precheckPlan 是锁外预检(体验层):让用户在扣星屑之前就被拦下。
//
// 权威判定在主库事务②里(applyPlanOnMain):预检与事务之间名额被别人占掉、套餐被
// 下架、顶替集合变了,都由那一侧拒绝 → 回滚 → 退星屑。这里只负责"尽早报错"。
func precheckPlan(userId int, p *Product, expectAction string, expectSuperseded []string) (*model.SubscriptionPlan, *planPreview, error) {
	if !config.Get().TwoPhase.OutboxEnabled() {
		return nil, nil, errPlanNeedsOutbox
	}
	plan, err := model.GetSubscriptionPlanById(p.PlanId)
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, nil, errOffSale
		}
		return nil, nil, wrapInternal("读取套餐", err)
	}
	if !plan.Enabled {
		return nil, nil, errOffSale
	}
	if err := model.PlanSaleWindowError(plan, common.GetTimestamp()); err != nil {
		return nil, nil, newBizError(http.StatusConflict, codeOffSale, err.Error())
	}
	if err := model.QyGateSubscriptionSeat(nil, plan, userId, "precheck", nil); err != nil {
		return nil, nil, newBizError(http.StatusConflict, codeSoldOut, err.Error())
	}
	if plan.MaxPurchasePerUser > 0 {
		n, err := model.CountUserSubscriptionsByPlan(userId, plan.Id)
		if err != nil {
			return nil, nil, wrapInternal("统计已购套餐", err)
		}
		if n >= int64(plan.MaxPurchasePerUser) {
			return nil, nil, newBizError(http.StatusConflict, codeLimit, "已达到该套餐购买上限")
		}
	}
	pv, err := model.PreviewUserGroupPurchase(userId, plan)
	if err != nil {
		return nil, nil, wrapInternal("预览购买后果", err)
	}
	if pv.Action == model.UserGroupPurchaseActionReject {
		return nil, nil, newBizError(http.StatusConflict, codeLimit, pv.Message)
	}
	preview := &planPreview{
		Action: pv.Action, SupersededGroups: sortedGroups(pv.SupersededGroups),
		SeatAvailable: true, Reason: pv.Message,
	}
	// 顶替是不可逆的(旧组剩余时间直接作废),必须由用户确认过才能下单;
	// 其余动作只要客户端带了 expect_action,就得与此刻的判定一致。
	if !expectMatches(preview, expectAction, expectSuperseded) {
		return nil, nil, errPlanStateChanged
	}
	return plan, preview, nil
}

// expectMatches 回答"用户确认过的后果与此刻算出来的一致吗"。
func expectMatches(pv *planPreview, expectAction string, expectSuperseded []string) bool {
	expectAction = strings.TrimSpace(expectAction)
	if pv.Action == model.UserGroupPurchaseActionSupersede {
		if expectAction != model.UserGroupPurchaseActionSupersede {
			return false
		}
		want := sortedGroups(pv.SupersededGroups)
		got := sortedGroups(expectSuperseded)
		if len(want) != len(got) {
			return false
		}
		for i := range want {
			if want[i] != got[i] {
				return false
			}
		}
		return true
	}
	return expectAction == "" || expectAction == pv.Action
}

// sortedGroups 去空白、排序,返回非 nil 切片(它会被下发)。
func sortedGroups(in []string) []string {
	out := make([]string, 0, len(in))
	for _, g := range in {
		if g = strings.TrimSpace(g); g != "" {
			out = append(out, g)
		}
	}
	sort.Strings(out)
	return out
}

// foldSupersededSet 把顶替集合折成落库的一列(varchar(255))。超长时改用哈希:
// 截断会让两个不同的集合撞成同一个值,复核就永远通不过或永远通得过。
func foldSupersededSet(groups []string) string {
	joined := strings.Join(sortedGroups(groups), ",")
	if len(joined) <= 255 {
		return joined
	}
	sum := sha256.Sum256([]byte(joined))
	return "h:" + hex.EncodeToString(sum[:])
}

// supersedeVerdict 在主库行锁内重算"这一单会发生什么",判据与
// model.PreviewUserGroupPurchase 逐字相同(同组永久 → reject、同组 → extend、
// 有别的升组订阅 → supersede、否则 new)。
func supersedeVerdict(actives []model.UserSubscription, target string) (string, []string) {
	for i := range actives {
		if strings.TrimSpace(actives[i].UpgradeGroup) != target {
			continue
		}
		if actives[i].EndTime == 0 {
			return model.UserGroupPurchaseActionReject, make([]string, 0)
		}
		return model.UserGroupPurchaseActionExtend, make([]string, 0)
	}
	groups := make([]string, 0, len(actives))
	for i := range actives {
		groups = append(groups, strings.TrimSpace(actives[i].UpgradeGroup))
	}
	if len(groups) > 0 {
		return model.UserGroupPurchaseActionSupersede, sortedGroups(groups)
	}
	return model.UserGroupPurchaseActionNew, groups
}

// placePlanOrder 完成一次套餐下单。第二个返回值为 true 表示幂等重放。
//
// 返回 (order, false, err) 且 order 非 nil 时,订单已经落库并停在 failed(已退星屑):
// 调用方把 err 回给用户,同时能在审计里挂上单号。返回 (order, false, nil) 且状态是
// held 时,星屑已扣、结局待对账 —— 用户看到的是"待核对",不是失败。
func placePlanOrder(ctx context.Context, in planOrderInput) (*Order, bool, error) {
	handle := db.Get()
	if handle == nil {
		return nil, false, db.ErrNotReady
	}
	gdb := handle.WithContext(ctx)

	p := in.Product
	if p.Kind != KindPlan {
		return nil, false, errBadRequest("该商品不是套餐")
	}
	plan, preview, err := precheckPlan(in.UserId, p, in.ExpectAction, in.ExpectSuperseded)
	if err != nil {
		return nil, false, err
	}

	now := common.GetTimestamp()
	o := &Order{
		OrderNo:   newOrderNo(),
		UserId:    in.UserId,
		ProductId: p.Id,
		ProductNo: p.ProductNo,
		Kind:      KindPlan,
		Title:     p.Title,
		Price:     p.Price,
		Status:    StatusPaid,
		Source:    SourceMall,
		IdemKey:   idemKeyOf(in.UserId, in.ClientRequestId),
		// 存的是预检**算出来**的后果而不是用户的原始输入:主库事务里复核的就是这一份。
		ExpectAction:     preview.Action,
		ExpectSuperseded: foldSupersededSet(preview.SupersededGroups),
		CreatedAt:        now,
		UpdatedAt:        now,
	}
	o.TradeNo = tradeNoPrefix + o.OrderNo

	grant := &planGrant{}
	req := twophase.Request{
		Kind:      qymodel.KindMallPlan,
		IdemScope: idemScope,
		// 与 qy_ml_order.idem_key 同源:LocalDetail 撞订单唯一键与资金单撞 uk 指向同一张原单。
		IdemKey:     o.IdemKey,
		UserId:      in.UserId,
		AmountQuota: p.Price,
		RefType:     refTypeMallOrder,
		RefId:       o.OrderNo,
		LocalDetail: func(tx *gorm.DB, fo *qymodel.FundOrder) error {
			if err := reserveProductTx(tx, p.Id, in.UserId); err != nil {
				return err
			}
			if err := debitForOrderTx(tx, o); err != nil {
				return err
			}
			o.FundOrderNo = fo.OrderNo
			if err := tx.Create(o).Error; err != nil {
				return err
			}
			return writeEvent(tx, o.Id, ActionPay,
				"已支付 "+strconv.FormatInt(o.Price, 10)+" "+stardust.UnitName()+",等待发放订阅", in.UserId)
		},
		MainApply: func(tx *gorm.DB, _ *qymodel.FundOrder) error {
			return applyPlanOnMain(tx, o, plan.Id, grant)
		},
		AfterCommit: postMallPlanFromOrder,
		LocalCommit: func(tx *gorm.DB, _ *qymodel.FundOrder) error {
			_, err := finalizeOrderTx(tx, o, grant.SubId, grant.Renewed)
			return err
		},
	}
	// 指纹只收用户这次请求说了什么:order_no 每次现生成,不得进指纹(否则同一个
	// client_request_id 的两次提交永远算出两个指纹,幂等键在结构上就命中不了)。
	fp := req
	fp.RefId = ""
	req.Fingerprint = fp.Digest(p.ProductNo)

	fo, err := twophase.Execute(ctx, req)
	if err != nil {
		return settlePlanExecuteError(ctx, gdb, o, p, fo, err)
	}
	if o.Id == 0 {
		// Execute 幂等命中了已成功的资金单(并发的另一路刚落定):本次什么都没做,
		// 回读那一路留下的订单当重放。
		existing, err := findOrderByIdemKey(gdb, o.IdemKey)
		if err != nil {
			return nil, false, err
		}
		if existing == nil {
			return nil, false, wrapInternal("资金单幂等命中但订单不存在", errors.New(o.IdemKey))
		}
		if existing.ProductNo != p.ProductNo {
			return nil, false, errIdemConflict
		}
		return existing, true, nil
	}
	// settleGuard:Execute 返回 nil ≠ 订单已落定(回写扩展库失败、CAS 被别人抢走
	// 都会返回 nil)。回读一次,还停在 paid 的挂 held 交对账任务。
	cur, err := loadOrderByNo(gdb, o.OrderNo)
	if err != nil {
		return nil, false, err
	}
	if cur.Status == StatusPaid {
		if err := holdOrder(gdb, cur, "资金单已提交但订单未落定,等待对账收敛"); err != nil {
			common.SysError("qianye/mall: 套餐订单 " + cur.OrderNo + " 挂起失败: " + err.Error())
		}
	}
	return cur, false, nil
}

// settlePlanExecuteError 把 twophase.Execute 的错误落成订单的终局。
func settlePlanExecuteError(ctx context.Context, gdb *gorm.DB, o *Order, p *Product, fo *qymodel.FundOrder, cause error) (*Order, bool, error) {
	switch {
	case errors.Is(cause, twophase.ErrIdemConflict):
		return nil, false, errIdemConflict
	case errors.Is(cause, twophase.ErrInProgress), errors.Is(cause, twophase.ErrOrderFailed):
		// 幂等命中了另一路并发请求落下的资金单:回读它的订单当重放。
		existing, err := findOrderByIdemKey(gdb, o.IdemKey)
		if err != nil {
			return nil, false, err
		}
		if existing == nil {
			return nil, false, errInProgress
		}
		if existing.ProductNo != p.ProductNo {
			return nil, false, errIdemConflict
		}
		return existing, true, nil
	}
	if fo == nil || o.Id == 0 {
		// 扩展库事务①没落下:锁商品 / 扣星屑 / 落单任一步失败,什么都没发生。
		if _, ok := AsBizError(BizOf(cause)); ok {
			return nil, false, BizOf(cause)
		}
		db.MarkFailure(cause)
		return nil, false, wrapInternal("套餐下单", cause)
	}
	if !settlePlanFailure(ctx, o, fo, cause) {
		// 挂 held:星屑已扣、结局待对账。对用户这不是失败。
		return o, false, nil
	}
	if _, ok := AsBizError(BizOf(cause)); ok {
		return o, false, BizOf(cause)
	}
	db.MarkFailure(cause)
	return o, false, wrapInternal("发放订阅", cause)
}

// settlePlanFailure 在主库那一侧失败之后决定退不退。返回 true 表示已退星屑并置 failed。
//
// 只在 Failed 且探针明确说 MainNotApplied 时退(transfer/service.go 的 releaseOnFailure
// 判据):Failed + MainApplied/MainUnknown、InDoubt、Uncertain 都意味着"订阅可能已经
// 发了",退了就是白送一份订阅,一律挂 held。
//
// 收尾一律走 guard.ColdContext(context.WithoutCancel(ctx)):Failed 常常正是调用方
// 预算耗尽触发的,沿用那个 ctx 去退款,第一条语句就 DeadlineExceeded,订单永远停在 paid。
func settlePlanFailure(ctx context.Context, o *Order, fo *qymodel.FundOrder, cause error) bool {
	sctx, cancel := guard.ColdContext(context.WithoutCancel(ctx))
	defer cancel()
	handle := db.Get()
	if handle == nil {
		common.SysError("qianye/mall: 套餐订单 " + o.OrderNo + " 主库失败后扩展库不可用,交由对账任务处理")
		return false
	}
	gdb := handle.WithContext(sctx)

	if fo.Status == qymodel.StatusFailed && twophase.ProbeMainSide(fo) == twophase.MainNotApplied {
		reason := audit.Truncate("发放订阅失败,"+stardust.UnitName()+"已退回: "+auditReason(cause), 255)
		if err := refundPlanOrder(gdb, o, []string{StatusPaid}, reason, 0); err != nil {
			common.SysError("qianye/mall: 套餐订单 " + o.OrderNo + " 退款失败,交由对账任务处理: " + err.Error())
			return false
		}
		return true
	}
	note := "资金单未确定失败(" + qymodel.StatusName(fo.Status) + "),等待对账 / 人工核对"
	if err := holdOrder(gdb, o, note); err != nil {
		common.SysError("qianye/mall: 套餐订单 " + o.OrderNo + " 挂起失败: " + err.Error())
	}
	return false
}

// refundPlanOrder 把一张套餐订单从 from 推到 failed 并退星屑。CAS 落空(别的路径已处理)
// 时回读真实状态、不报错 —— 三条退款路径(请求线程、对账、裁决)任一条赢了就够了。
func refundPlanOrder(gdb *gorm.DB, o *Order, from []string, reason string, actorId int) error {
	return gdb.Transaction(func(tx *gorm.DB) error {
		lo, err := lockOrderByNo(tx, o.OrderNo)
		if err != nil {
			return err
		}
		if statusIn(lo.Status, from) {
			if err := refundAndTransition(tx, lo, reason, transition{
				From: from, To: StatusFailed, Action: ActionFail, Note: reason, ActorId: actorId,
				Updates: map[string]any{"fail_reason": audit.Truncate(reason, 255)},
			}); err != nil {
				return err
			}
		}
		*o = *lo
		return nil
	})
}

// holdOrder 把一张 paid 的套餐订单挂成 held(展示态,不动钱)。已不是 paid 的原样回读。
func holdOrder(gdb *gorm.DB, o *Order, note string) error {
	return gdb.Transaction(func(tx *gorm.DB) error {
		lo, err := lockOrderByNo(tx, o.OrderNo)
		if err != nil {
			return err
		}
		if lo.Status == StatusPaid {
			if err := applyTransition(tx, lo, transition{
				From: []string{StatusPaid}, To: StatusHeld, Action: ActionHold, Note: note,
			}); err != nil {
				return err
			}
		}
		*o = *lo
		return nil
	})
}

// applyPlanOnMain 是主库事务②:CreateUserSubscriptionFromPlanTx **不检查** Enabled 与
// 发售窗(它只有限购 / 名额 / 顶替规则),所以这里先自查,再在用户行锁内复核顶替集合。
func applyPlanOnMain(tx *gorm.DB, o *Order, planId int, grant *planGrant) error {
	// 直读主库,不走 getSubscriptionPlanByIdTx:它先查 300 秒的缓存,预检刚刚看过的
	// 那一份可能已经被运营下架。
	var plan model.SubscriptionPlan
	if err := tx.Where("id = ?", planId).First(&plan).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return errOffSale
		}
		return err
	}
	plan.NormalizeDefaults()
	now := common.GetTimestamp()
	if !plan.Enabled {
		return errOffSale
	}
	if err := model.PlanSaleWindowError(&plan, now); err != nil {
		return newBizError(http.StatusConflict, codeOffSale, err.Error())
	}

	// 锁用户行;status ≠ enabled 拒(纵深防御;软删由 First 的 DeletedAt 过滤覆盖)。
	var u model.User
	if err := model.QyLockForUpdate(tx).Select("id", "status").Where("id = ?", o.UserId).First(&u).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return errUserUnavailable
		}
		return err
	}
	if u.Status != common.UserStatusEnabled {
		return errUserUnavailable
	}

	// 复核顶替集合:只对纯商品 + 会改组的套餐(applyUserGroupPurchaseRulesTx 的适用范围)。
	// 用户在商品页确认的是 A 组会被顶掉,此刻若变成 B 组也会被顶掉,必须拒绝而不是照做 ——
	// 顶替是不可逆的,剩余时间直接作废。
	//
	// 奖品单(SourceLottery)**不问用户**:项目方原话"套餐立即生效",中奖者没有一个
	// 确认页可以站;算出来的后果原样记进订单的 expect_* 两列与完结事件,供事后解释
	// "为什么我的 X 组没了"。
	if target := strings.TrimSpace(plan.UpgradeGroup); plan.NoQuota && target != "" {
		actives := make([]model.UserSubscription, 0, 4)
		if err := model.QyLockForUpdate(tx).
			Where("user_id = ? AND status = ? AND upgrade_group <> '' AND "+model.SubscriptionActiveEndTimeSQL,
				o.UserId, "active", now).
			Order("id asc").Find(&actives).Error; err != nil {
			return err
		}
		action, groups := supersedeVerdict(actives, target)
		if o.Source == SourceLottery {
			grant.Action, grant.Superseded = action, foldSupersededSet(groups)
		} else if action != o.ExpectAction || foldSupersededSet(groups) != o.ExpectSuperseded {
			return errPlanStateChanged
		}
	}

	// 续期判定:发放前用户名下订阅的最大 id。发放返回的行 id 不大于它,就是同组续期
	// (applyUserGroupPurchaseRulesTx 只改 end_time、不新建行)。
	var maxId int
	if err := tx.Model(&model.UserSubscription{}).Where("user_id = ?", o.UserId).
		Select("COALESCE(MAX(id), 0)").Scan(&maxId).Error; err != nil {
		return err
	}
	source := subscriptionSource
	if o.Source == SourceLottery {
		source = subscriptionSourceLottery
	}
	sub, err := model.CreateUserSubscriptionFromPlanTx(tx, o.UserId, &plan, source)
	if err != nil {
		return grantRejection(err)
	}
	renewed := sub.Id <= maxId

	// 写全 subscription_orders:本仓没有任何列出它的接口或页面,这一行只供对账,
	// 用户可见性靠订单页的 user_subscription_id。
	renewedFlag := 0
	if renewed {
		renewedFlag = 1
	}
	so := &model.SubscriptionOrder{
		UserId:          o.UserId,
		PlanId:          plan.Id,
		Money:           0,
		TradeNo:         o.TradeNo,
		PaymentMethod:   source,
		PaymentProvider: source,
		Status:          "success",
		CreateTime:      now,
		CompleteTime:    now,
		ProviderPayload: fmt.Sprintf("mall_order_no=%s;stardust=%d;user_subscription_id=%d;renewed=%d;source=%s",
			o.OrderNo, o.Price, sub.Id, renewedFlag, o.Source),
		PlanSnapshot: model.SubscriptionPlanSnapshot(&plan),
	}
	if err := tx.Create(so).Error; err != nil {
		return err
	}
	grant.SubId, grant.Renewed = sub.Id, renewed
	return nil
}

// grantRejection 把 CreateUserSubscriptionFromPlanTx 的业务拒绝翻成带 code 的业务错误。
//
// 上游用 errors.New 的中文句子表达拒绝(限购 / 名额 / 同组永久),没有哨兵可比,
// 只能按句子归类;认不出来的原样返回,按内部错误处理(500 + 退星屑)。
func grantRejection(err error) error {
	if errors.Is(err, model.ErrPlanNotOnSaleYet) || errors.Is(err, model.ErrPlanSaleEnded) {
		return newBizError(http.StatusConflict, codeOffSale, err.Error())
	}
	msg := err.Error()
	switch {
	case strings.Contains(msg, "购买上限"), strings.Contains(msg, "永久拥有"):
		return newBizError(http.StatusConflict, codeLimit, msg)
	case strings.Contains(msg, "名额"):
		return newBizError(http.StatusConflict, codeSoldOut, msg)
	}
	return err
}

// postMallPlanFromOrder 是提交后收尾:AfterCommit(业务线程)与 PostCommit(补偿任务)
// 共用同一个函数,只用 order.UserId —— 补偿路径拿不到任何进程内快照。
//
// **无条件**刷分组缓存:补偿路径拿不到 PrevUserGroup,分组没变时刷新是语义空操作。
// QyRefreshSubscriptionUserGroupCache 内部已先调 QyOnUserGroupChanged 再刷缓存,
// 不要再并列写钩子。
func postMallPlanFromOrder(order *qymodel.FundOrder) {
	uid := order.UserId
	model.QyRefreshSubscriptionUserGroupCache(uid, "mall plan purchase")
	planentitlement.InvalidateUser(uid)
	model.RecordLog(uid, model.LogTypeTopup, fmt.Sprintf(
		"%s兑换订阅成功:商城订单 %s,支付 %d %s",
		stardust.UnitName(), order.RefId, order.AmountQuota, stardust.UnitName()))
}

// finalizePlanOrder 是注册进 twophase 的 Resolver:补偿任务 / 对账 / 裁决确认主库已生效后,
// 把订单推到 done 并回填订阅 id。必须幂等 —— 同一单可能被推进多次。
func finalizePlanOrder(ctx context.Context, fo *qymodel.FundOrder) error {
	handle := db.Get()
	if handle == nil {
		return db.ErrNotReady
	}
	gdb := handle.WithContext(ctx)

	var o Order
	err := gdb.Where("fund_order_no = ?", fo.OrderNo).Take(&o).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		// 订单与资金单是同一个事务插入的,查不到只能是数据被人为删过。
		common.SysError("qianye/mall: 资金单 " + fo.OrderNo + " 找不到对应的商城订单,需人工核对")
		return nil
	}
	if err != nil {
		db.MarkFailure(err)
		return err
	}
	switch o.Status {
	case StatusDone:
		return nil
	case StatusPaid, StatusHeld:
	default:
		// 资金单成功而订单已经退过款:订阅发了、星屑也退了。这不是本函数能修的,
		// 但必须被人看见。
		common.SysError(fmt.Sprintf(
			"qianye/mall: 资金单 %s 已成功,但商城订单 %s 处于 %s(星屑可能已退回),需人工核对",
			fo.OrderNo, o.OrderNo, o.Status))
		return nil
	}
	grant, found, err := readGrantByTradeNo(ctx, o.TradeNo)
	if err != nil {
		return err
	}
	if !found {
		common.SysError("qianye/mall: 商城订单 " + o.OrderNo + " 的主库 subscription_orders 行不存在,订阅 id 无法回填")
	}
	return gdb.Transaction(func(tx *gorm.DB) error {
		_, err := finalizeOrderTx(tx, &o, grant.SubId, grant.Renewed)
		return err
	})
}

// readGrantByTradeNo 按 trade_no 读主库 subscription_orders.provider_payload,取回订阅 id 与续期标记。
func readGrantByTradeNo(ctx context.Context, tradeNo string) (planGrant, bool, error) {
	if model.DB == nil {
		return planGrant{}, false, db.ErrNotReady
	}
	var so model.SubscriptionOrder
	err := model.DB.WithContext(ctx).Select("id", "provider_payload").
		Where("trade_no = ?", tradeNo).Take(&so).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return planGrant{}, false, nil
	}
	if err != nil {
		return planGrant{}, false, wrapInternal("读取主库订阅订单", err)
	}
	g := planGrant{}
	for _, kv := range strings.Split(so.ProviderPayload, ";") {
		k, v, ok := strings.Cut(kv, "=")
		if !ok {
			continue
		}
		switch k {
		case "user_subscription_id":
			g.SubId, _ = strconv.Atoi(v)
		case "renewed":
			g.Renewed = v == "1"
		}
	}
	return g, true, nil
}

// InstallResolvers 把套餐订单的收尾接进 twophase:Resolver 收扩展库明细,PostCommit
// 收主库那一侧的可见结果。缺了 Resolver 的后果在本仓的 violation 上真实发生过:
// 补偿任务把资金单推成 success,业务侧永远停在中间态。
func InstallResolvers() {
	twophase.RegisterResolver(qymodel.KindMallPlan, finalizePlanOrder)
	twophase.RegisterPostCommit(qymodel.KindMallPlan, func(_ context.Context, order *qymodel.FundOrder) error {
		postMallPlanFromOrder(order)
		return nil
	})
}
