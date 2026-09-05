package stardust

import (
	"context"
	"strconv"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/qianye/config"
	"github.com/QuantumNous/new-api/qianye/db"
	"github.com/QuantumNous/new-api/qianye/groupname"
	"github.com/QuantumNous/new-api/qianye/guard"
	"github.com/QuantumNous/new-api/qianye/modules/invite"
	"github.com/QuantumNous/new-api/setting/operation_setting"

	"github.com/shopspring/decimal"
	"gorm.io/gorm"
)

// hooks.go —— 三个事件型获得途径:兑换码返(§4.3)、注册奖(D-G)、套餐返(§4.4 / D-F),
// 以及一次性来源共用的档位解析与换算。
//
// hook 体跑在用户请求线程上,必须 O(1) 无 I/O:只读 config 快照与已缓存的运营参数,
// 所有查库都在 guard.HotAsync 的 worker 里,ctx 透传到每个 GORM 调用。
// 依赖方向只有 stardust → invite(邀请关系判定 InviteeEligible);invite
// 只暴露 AfterRedeemSuccess 等 func 变量给这里赋值(qianye/module_import_guard_test.go)。

const (
	redeemIdemScope   = "sd_redeem"
	registerIdemScope = "sd_register"
	planIdemScope     = "sd_plan"
)

func init() {
	hookInstallers = append(hookInstallers, func() {
		// 兑换码:model.QyOnRedeemSuccess 单槽已被 invite 占,它不设任何早退、
		// 无条件转发到这个变量(由 qianye/stardust_hookpoint_guard_test.go 钉住)。
		invite.AfterRedeemSuccess = onRedeemSuccess
		model.QyOnUserRegistered = onUserRegistered
		model.QyOnSubscriptionGranted = onSubscriptionGranted
	})
}

// cachedSettings 只读进程内的运营参数快照,绝不回库:给热路径 hook 做预过滤用。
// 快照不在(冷启动 / 刚失效)时返回 false,由 worker 里的 effective() 再判一次。
func cachedSettings() (opSettings, bool) {
	settingsMu.Lock()
	defer settingsMu.Unlock()
	if settingsCache == nil {
		return opSettings{}, false
	}
	return complianceGate(*settingsCache), true
}

// inviteRateKind 区分一次性来源用分组表的哪一档。
type inviteRateKind int

const (
	inviteTopupRate inviteRateKind = iota
	inviteRedeemRate
)

// inviteRateFor 解析邀请返的档位:按**上线**分组(D-02)查 qy_sd_group_rate,
// 没配(nil)或整行未启用就回落全站 stardust.invite_*_bps。
//
// 合规门套在两层之外:effective() 已按 D-G 把三个邀请类正值归零,分组表那一档
// 不经 effective(),这里再判一次 —— 否则"分组表配了 10%"就是一条绕过合规门的路。
// 第二个返回值是归一化后的分组名,冻结进流水的 rate_group。
func inviteRateFor(ctx context.Context, inviterGroup string, kind inviteRateKind) (int, string) {
	group := groupname.Effective(inviterGroup)
	if !operation_setting.IsPaymentComplianceConfirmed() {
		return 0, group
	}
	s := effective()
	fallback := s.InviteTopupBps
	if kind == inviteRedeemRate {
		fallback = s.InviteRedeemBps
	}
	if r, ok := groupRateFor(ctx, inviterGroup); ok {
		v := r.InviteTopupBps
		if kind == inviteRedeemRate {
			v = r.InviteRedeemBps
		}
		if v != nil {
			return *v, group
		}
	}
	return fallback, group
}

// stardustFromQuota 是一次性来源的换算:floor(base × bps / 10000 / quota_per_unit),
// 一单一行、余数不结转(§4.3)。QuoRem 取精确整数商,再经饱和转换钉在 MaxQuota 之内。
func stardustFromQuota(base int64, bps int, qpu int64) int64 {
	if base <= 0 || bps <= 0 || qpu <= 0 {
		return 0
	}
	num := decimal.NewFromInt(base).Mul(decimal.NewFromInt(int64(bps)))
	den := decimal.NewFromInt(10_000).Mul(decimal.NewFromInt(qpu))
	q, _ := num.QuoRem(den, 0)
	v, clamp := common.QuotaFromDecimalChecked(q)
	if clamp != nil {
		warnf("星屑换算触顶(base=%d bps=%d): %s", base, bps, clamp.Error())
	}
	if v < 0 {
		return 0
	}
	return int64(v)
}

// stardustFromMoney 是套餐返的换算:1 星屑 = $1 等值。price 是美元;
// 先按 common.QuotaPerUnit 折成额度,再按本模块的刻度折成星屑 —— 两个刻度默认相等,
// 运营改了 stardust.quota_per_unit 时按比例折算(D-F)。
func stardustFromMoney(price decimal.Decimal, bps int) int64 {
	if !price.IsPositive() || bps <= 0 {
		return 0
	}
	num := price.Mul(decimal.NewFromFloat(common.QuotaPerUnit)).Mul(decimal.NewFromInt(int64(bps)))
	den := decimal.NewFromInt(10_000).Mul(decimal.NewFromInt(QuotaPerUnit()))
	q, _ := num.QuoRem(den, 0)
	v, clamp := common.QuotaFromDecimalChecked(q)
	if clamp != nil {
		warnf("套餐返换算触顶(price=%s bps=%d): %s", price.String(), bps, clamp.Error())
	}
	if v < 0 {
		return 0
	}
	return int64(v)
}

// quotaOfMoney 把美元售价折成额度(冻结进流水的 base_quota)。
func quotaOfMoney(price decimal.Decimal) int64 {
	v, _ := common.QuotaFromDecimalChecked(price.Mul(decimal.NewFromFloat(common.QuotaPerUnit)).Floor())
	if v < 0 {
		return 0
	}
	return int64(v)
}

// postCredit 在一个扩展库事务里记一笔入账。Credit 的错误分支只能 return(让事务回滚),
// 幂等重放(Inserted=false)对一次性来源不是错误 —— 同一事件被投递两次只会到此为止。
func postCredit(ctx context.Context, p Posting) (Result, error) {
	gdb := db.Get()
	if gdb == nil {
		return Result{}, db.ErrNotReady
	}
	var out Result
	err := gdb.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		res, err := Credit(tx, p)
		if err != nil {
			return err
		}
		out = res
		return nil
	})
	if err != nil {
		db.MarkFailure(err)
		return Result{}, err
	}
	return out, nil
}

// ───────────────────────────── ① 兑换码返 ─────────────────────────────

// onRedeemSuccess 是余额兑换码兑换成功之后的第二级转发槽(invite.AfterRedeemSuccess)。
// 只对余额码触发(套餐码走 onSubscriptionGranted);基数 = 码面额。
// 自判 stardust.enabled / invite_redeem_bps > 0 / quota > 0;invite.enabled 由 InviteeEligible 判。
func onRedeemSuccess(userId int, redemptionId int, quota int) {
	if !config.Get().Stardust.Enabled || userId <= 0 || redemptionId <= 0 || quota <= 0 {
		return
	}
	if s, ok := cachedSettings(); ok && s.InviteRedeemBps <= 0 {
		return
	}
	base := int64(quota)
	guard.HotAsync("stardust.redeem", func(ctx context.Context) error {
		return accrueRedeem(ctx, userId, redemptionId, base)
	})
}

// accrueRedeem 给兑换人的上线记一笔 invite_redeem。幂等键 redeem:<redemption_id>。
func accrueRedeem(ctx context.Context, userId, redemptionId int, base int64) error {
	if base <= 0 {
		return nil
	}
	match, err := invite.InviteeEligible(ctx, userId)
	if err != nil {
		return err
	}
	if !match.Eligible() {
		return nil
	}
	bps, group := inviteRateFor(ctx, match.InviterGroup, inviteRedeemRate)
	amount := stardustFromQuota(base, bps, QuotaPerUnit())
	if amount <= 0 {
		return nil
	}
	_, err = postCredit(ctx, Posting{
		UserId: match.InviterId, Kind: KindInviteRedeem, Amount: amount,
		IdemScope: redeemIdemScope, IdemKey: "redeem:" + strconv.Itoa(redemptionId),
		RefType: "redemption", RefNo: strconv.Itoa(redemptionId),
		PeerUserId: userId, RateBps: bps, RateGroup: group, BaseQuota: base,
	})
	return err
}

// ───────────────────────────── ② 注册奖 ─────────────────────────────

// onUserRegistered 在建号完成之后触发(model.QyOnUserRegistered)。入参的 inviterId
// 只用来做"有没有邀请人"的预过滤;真正记账给谁以 InviteeEligible 解析到的上线为准
// (它会补建关系快照并判自邀 / 拉黑),两者不一致时以后者为准。
func onUserRegistered(userId int, inviterId int) {
	if inviterId <= 0 || userId <= 0 || !config.Get().Stardust.Enabled {
		return
	}
	if s, ok := cachedSettings(); ok && s.InviteRegisterStardust <= 0 {
		return
	}
	guard.HotAsync("stardust.register", func(ctx context.Context) error {
		return accrueRegister(ctx, userId)
	})
}

// accrueRegister 给新账号的上线记一笔固定额 invite_register。幂等键 register:<invitee_id>。
// 金额是 effective() 里过了合规门的 InviteRegisterStardust,不按分组分档。
func accrueRegister(ctx context.Context, userId int) error {
	amount := effective().InviteRegisterStardust
	if amount <= 0 {
		return nil
	}
	match, err := invite.InviteeEligible(ctx, userId)
	if err != nil {
		return err
	}
	if !match.Eligible() {
		return nil
	}
	_, err = postCredit(ctx, Posting{
		UserId: match.InviterId, Kind: KindInviteRegister, Amount: amount,
		IdemScope: registerIdemScope, IdemKey: "register:" + strconv.Itoa(userId),
		RefType: "register", RefNo: strconv.Itoa(userId),
		PeerUserId: userId, RateGroup: groupname.Effective(match.InviterGroup),
	})
	return err
}

// ───────────────────────────── ③ 套餐返 ─────────────────────────────

// onSubscriptionGranted 在四条购买路径的事务提交之后触发(model.QyOnSubscriptionGranted)。
// 购买是低频事件,丢弃面可接受;续期(Renewed=true)照返 —— 钱已收。
// 商城自购(source=stardust)不经此 hook,且它永远不在来源闭集里(D-F)。
func onSubscriptionGranted(g model.QySubscriptionGrant) {
	if !config.Get().Stardust.Enabled || g.UserId <= 0 || g.PlanId <= 0 {
		return
	}
	grantedAt := common.GetTimestamp()
	guard.HotAsync("stardust.plan", func(ctx context.Context) error {
		return accruePlanGrant(ctx, g, grantedAt)
	})
}

// accruePlanGrant 按套餐的返还定义给买家(plan_buyer)与上线(plan_inviter)各记一笔。
//
// 基数:order 来源优先用实付 Money(快照),其余用套餐售价 PriceAmount(美元)。
// 两笔各自一个事务:上线那笔失败不该回滚买家已经到手的那笔;各自幂等,重投无害。
func accruePlanGrant(ctx context.Context, g model.QySubscriptionGrant, grantedAt int64) error {
	reward, _ := planRewardFor(ctx, g.PlanId)
	if !reward.SourceAllowed(g.Source) {
		return nil
	}
	price := decimal.NewFromFloat(g.PriceAmount)
	if g.Source == PlanSourceOrder && g.Money > 0 {
		price = decimal.NewFromFloat(g.Money)
	}
	if !price.IsPositive() {
		return nil
	}
	key := planIdemKey(g, grantedAt)
	refNo := strconv.Itoa(g.SubscriptionId)
	baseQuota := quotaOfMoney(price)

	if buyer := stardustFromMoney(price, reward.BuyerBps); buyer > 0 {
		if _, err := postCredit(ctx, Posting{
			UserId: g.UserId, Kind: KindPlanBuyer, Amount: buyer,
			IdemScope: planIdemScope, IdemKey: key,
			RefType: "subscription", RefNo: refNo,
			RateBps: reward.BuyerBps, BaseQuota: baseQuota,
			Remark: "套餐 " + strconv.Itoa(g.PlanId) + " / " + g.Source,
		}); err != nil {
			return err
		}
	}
	if reward.InviterBps <= 0 {
		return nil
	}
	match, err := invite.InviteeEligible(ctx, g.UserId)
	if err != nil {
		return err
	}
	if !match.Eligible() {
		return nil
	}
	inviter := stardustFromMoney(price, reward.InviterBps)
	if inviter <= 0 {
		return nil
	}
	_, err = postCredit(ctx, Posting{
		UserId: match.InviterId, Kind: KindPlanInviter, Amount: inviter,
		IdemScope: planIdemScope, IdemKey: key + ":inviter",
		RefType: "subscription", RefNo: refNo,
		PeerUserId: g.UserId, RateBps: reward.InviterBps,
		RateGroup: groupname.Effective(match.InviterGroup), BaseQuota: baseQuota,
		Remark: "套餐 " + strconv.Itoa(g.PlanId) + " / " + g.Source,
	})
	return err
}

// planIdemKey 按来源取唯一量(D-F):order/balance → sub:<trade_no>;redemption →
// sub:rd<redemption_id>;admin → sub:admin<subscription_id>:<granted_at>。
// 上游没给出该来源应有的唯一量时退回"订阅 id + 时刻"并告警:漏发比多发难查。
func planIdemKey(g model.QySubscriptionGrant, grantedAt int64) string {
	switch g.Source {
	case PlanSourceOrder, PlanSourceBalance:
		if g.TradeNo != "" {
			return "sub:" + g.TradeNo
		}
	case PlanSourceRedemption:
		if g.RedemptionId > 0 {
			return "sub:rd" + strconv.Itoa(g.RedemptionId)
		}
	case PlanSourceAdmin:
		return "sub:admin" + strconv.Itoa(g.SubscriptionId) + ":" + strconv.FormatInt(grantedAt, 10)
	}
	warnf("套餐授予事件缺少来源应有的唯一量(source=%s trade_no=%q redemption_id=%d),按订阅 id + 时刻建幂等键",
		g.Source, g.TradeNo, g.RedemptionId)
	return "sub:" + g.Source + strconv.Itoa(g.SubscriptionId) + ":" + strconv.FormatInt(grantedAt, 10)
}
