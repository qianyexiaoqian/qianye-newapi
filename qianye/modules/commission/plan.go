package commission

import (
	"context"
	"strings"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/qianye/config"
	"github.com/QuantumNous/new-api/qianye/guard"
	"github.com/QuantumNous/new-api/qianye/modules/stardust"

	"github.com/shopspring/decimal"
)

// plan.go —— 下线买套餐的计佣。
//
// 套餐只在**购买时**按三条渠道各自的档计一次佣(运营在管理端分别配):
//   - order      在线支付:基数是实付金额;档位 plan_order_rate_percent,未配时跟随充值档;
//   - balance    余额购买:基数是实际扣的余额购买价;档位 plan_balance_rate_percent;
//   - redemption 套餐兑换码:基数是套餐售价;档位 plan_redemption_rate_percent。
//
// admin(管理员绑定)不计佣 —— 没有人付钱。之后用套餐额度的消费是否计佣由
// exclude_subscription_consume 决定(consume.go),默认运营应当打开它,否则同一份
// 套餐会在购买与使用两头各计一次。
//
// 在线支付的套餐单同时会在 top_ups 里留一行,充值扫描按 trade_no 跳过它(topup_scan.go)。

// planChannel 把套餐授予事件映射成计佣来源、幂等键与基数。ok=false 表示这条来源不计佣。
func planChannel(g model.QySubscriptionGrant) (source, idemKey, ref string, money decimal.Decimal, cash bool, ok bool) {
	tradeNo := strings.TrimSpace(g.TradeNo)
	switch g.Source {
	case stardust.PlanSourceOrder:
		if tradeNo == "" {
			return "", "", "", decimal.Zero, false, false
		}
		amount := g.Money
		if amount <= 0 {
			amount = g.PriceAmount
		}
		return SourcePlanOrder, SourcePlanOrder + ":" + tradeNo, tradeNo, decimal.NewFromFloat(amount), true, true
	case stardust.PlanSourceBalance:
		if tradeNo == "" {
			return "", "", "", decimal.Zero, false, false
		}
		// 余额购买价可以与在线价分开配(含 0 = 免费),按实际扣的那个价计。
		return SourcePlanBalance, SourcePlanBalance + ":" + tradeNo, tradeNo, decimal.NewFromFloat(g.Money), false, true
	case stardust.PlanSourceRedemption:
		if g.RedemptionId <= 0 {
			return "", "", "", decimal.Zero, false, false
		}
		id := itoa(g.RedemptionId)
		return SourcePlanRedemption, SourcePlanRedemption + ":" + id, "RD" + id, decimal.NewFromFloat(g.PriceAmount), false, true
	}
	return "", "", "", decimal.Zero, false, false
}

// onSubscriptionGranted 是 invite.AfterSubscriptionGrantedCommission 的实现,
// 跑在用户的购买请求 / 支付回调线程上:只读 config 快照,查库全在 worker 里。
func onSubscriptionGranted(g model.QySubscriptionGrant) {
	if !config.Get().Commission.Enabled || g.UserId <= 0 {
		return
	}
	if _, _, _, money, _, ok := planChannel(g); !ok || !money.IsPositive() {
		return
	}
	guard.HotAsync("commission.plan", func(ctx context.Context) error {
		return accruePlanGrant(ctx, g)
	})
}

// accruePlanGrant 是 worker 里的那一半:按渠道取档、写一条计佣行。幂等键是订单号 /
// 兑换码 id,同一次购买重投只会落一行。
func accruePlanGrant(ctx context.Context, g model.QySubscriptionGrant) error {
	source, key, ref, money, cash, ok := planChannel(g)
	if !ok || !money.IsPositive() {
		return nil
	}
	// 支付合规门在 writeAccrual 这个漏斗上,三条渠道一并受它管,这里不再各挂一份。
	// 金额域只给真付了钱的在线单;余额与兑换码不是这一刻的进账。
	baseMoney := decimal.Zero
	if cash {
		baseMoney = money
	}
	return accrueOneShot(ctx, g.UserId, quotaOfPlanMoney(money), baseMoney, source, key, ref)
}

// quotaOfPlanMoney 把美元金额折成额度(计佣基数的刻度),饱和转换防篡改金额变负数。
func quotaOfPlanMoney(money decimal.Decimal) int64 {
	v, clamp := common.QuotaFromDecimalChecked(money.Mul(decimal.NewFromFloat(common.QuotaPerUnit)).Floor())
	if clamp != nil {
		warnf("套餐计佣基数换算触顶: %s", clamp.Error())
	}
	if v < 0 {
		return 0
	}
	return int64(v)
}
