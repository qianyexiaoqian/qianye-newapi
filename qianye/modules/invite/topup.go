package invite

import (
	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/model"

	"github.com/shopspring/decimal"
)

// topup.go —— 一笔充值单"该不该返、按多少返"的两条口径。
//
// 扫描 top_ups 的任务住在星屑那一侧(stardust/topup_scan.go);这里只留判定,
// 因为它们绑死在支付渠道的字段语义上,而支付渠道是上游的事,与哪个模块在发钱无关。

// ExcludedTopUp 判断一笔充值是否不该给邀请人返任何东西。
//
// 用余额支付产生的订单无条件排除:那笔余额在充值进来时已经返过一次,
// 再返一次等于同一笔钱付两遍。管理员补单是否排除由调用方自己的开关决定
// (stardust.exclude_manual_topup);判据认 complete_source 而不是
// payment_method == "manual" —— 全仓没有任何一条路径会把后者写成这个值。
func ExcludedTopUp(t *model.TopUp, excludeManual bool) bool {
	if t.PaymentProvider == model.PaymentProviderBalance || t.PaymentMethod == model.PaymentMethodBalance {
		return true
	}
	return excludeManual && t.CompleteSource == model.TopUpCompleteSourceAdmin
}

// TopUpBaseQuota 按支付渠道推算这笔充值的到账额度与法币金额。
//
// 各条充值路径的换算方式并不一致,统一按 Amount × QuotaPerUnit 会算错:
//   - creem:Amount 本身就是额度,不再乘;
//   - stripe 与订阅付费单(payment_provider 为空、Amount=0):按 Money 换算;
//   - 其余(epay/waffo/...):Amount × QuotaPerUnit。
func TopUpBaseQuota(t *model.TopUp) (int64, decimal.Decimal) {
	money := decimal.NewFromFloat(t.Money)
	qpu := decimal.NewFromFloat(common.QuotaPerUnit)
	switch t.PaymentProvider {
	case model.PaymentProviderCreem:
		return t.Amount, money
	case model.PaymentProviderStripe:
		return quotaFromDecimal(money.Mul(qpu)), money
	case "":
		// 空 provider 有两种来源,判据必须落在 **Amount** 上而不是 provider:
		//   - 订阅付费单(upsertSubscriptionTopUpTx 硬编码 Amount=0)→ 按 Money;
		//   - **payment_provider 列存在之前的历史 epay 订单**(Amount>0)→ 与
		//     model.TopUp.CreditQuota 的 default 分支一样按 Amount × QuotaPerUnit。
		if t.Amount > 0 {
			return quotaFromDecimal(decimal.NewFromInt(t.Amount).Mul(qpu)), money
		}
		return quotaFromDecimal(money.Mul(qpu)), money
	default:
		return quotaFromDecimal(decimal.NewFromInt(t.Amount).Mul(qpu)), money
	}
}

// quotaFromDecimal 把换算结果转成整数额度。
//
// 走 common 的饱和转换而不是裸 IntPart():额度受 common.MaxQuota 约束,
// 一个被篡改的订单金额不能变成负数额度,更不能成为负数返还。
func quotaFromDecimal(d decimal.Decimal) int64 {
	v, clamp := common.QuotaFromDecimalChecked(d.Floor())
	if clamp != nil {
		warnf("充值基数换算触顶: %s", clamp.Error())
	}
	if v < 0 {
		return 0
	}
	return int64(v)
}
