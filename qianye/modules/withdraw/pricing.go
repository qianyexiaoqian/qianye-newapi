package withdraw

import (
	"strings"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/qianye/config"
	"github.com/QuantumNous/new-api/setting/operation_setting"

	"github.com/shopspring/decimal"
)

// fiatScale 是法币金额的存储精度(裁定 C26:decimal(18,6),展示层再 Round(2))。
//
// 不用 2 位:手续费按比率算会出现分以下的位,先按 2 位存会让
// "应付 = 手续费 + 实付"这个恒等式对不上,财务复核时无法解释差额。
const fiatScale = 6

// bpsDenominator 是万分比的分母(裁定 C28:比例一律用整数 bps)。
const bpsDenominator = 10000

// frozenRates 是申请时冻结下来的一组计价参数。
//
// 存在理由:common.QuotaPerUnit 与 operation_setting.USDExchangeRate 都是
// 管理员可以随时热改的全局变量。不冻结的话,一年后没有任何办法复现
// "当时这笔佣金折合多少人民币",历史对账永远对不上。
//
// 两个都要冻:只冻汇率的话,管理员改一次 QuotaPerUnit 就会破坏全部历史换算。
type frozenRates struct {
	QuotaPerUnit decimal.Decimal
	FxRate       decimal.Decimal
}

// freezeRates 读取当前计价参数并冻结。
//
// rate_freeze_mode = fixed 时用配置里的固定汇率,适用于"平台按自己的结算价
// 打款,不跟随充值汇率浮动"的运营策略。
func freezeRates() (frozenRates, error) {
	cfg := config.Get().Withdraw

	perUnit := decimal.NewFromFloat(common.QuotaPerUnit)
	var fx decimal.Decimal
	if cfg.RateFreezeMode == config.RateFreezeFixed {
		parsed, err := decimal.NewFromString(strings.TrimSpace(cfg.RateFreezeFixed))
		if err != nil {
			return frozenRates{}, errRateUnavailable
		}
		fx = parsed
	} else {
		fx = decimal.NewFromFloat(operation_setting.USDExchangeRate)
	}

	// 管理员把汇率或 QuotaPerUnit 改成 0 是完全可能的。放行的话整单金额算成 0,
	// 用户白白损失一笔佣金,所以宁可拒绝建单并告警。
	if perUnit.LessThanOrEqual(decimal.Zero) || fx.LessThanOrEqual(decimal.Zero) {
		common.SysError("qianye/withdraw: 计价参数非法(quota_per_unit=" + perUnit.String() +
			", fx_rate=" + fx.String() + "),已拒绝建单")
		return frozenRates{}, errRateUnavailable
	}
	return frozenRates{QuotaPerUnit: perUnit, FxRate: fx}, nil
}

// fiatAmounts 是一张法币提现单的三段金额。
type fiatAmounts struct {
	Gross decimal.Decimal
	Fee   decimal.Decimal
	Net   decimal.Decimal
}

// computeFiat 把佣金额度换算成法币应付 / 手续费 / 实付。
//
// 全程 decimal,禁止 float64 中间量:float 的舍入分歧会直接变成对不上的账。
// 换算链是单向的(额度 → 法币),绝不反算 —— 让用户输法币金额再反推额度,
// 必然产生"输 ¥100 实扣 ¥100.0001 佣金"这类无法消除的对账噪声。
//
// Gross 与 Fee 各自 Round 到 6 位后,Net = Gross - Fee 仍是精确的 6 位小数,
// 因此恒等式 Gross == Fee + Net 天然成立,无需再做断言。
func computeFiat(quota int64, rates frozenRates, feeBps int) (fiatAmounts, error) {
	if quota <= 0 {
		return fiatAmounts{}, errAmountOutOfRange
	}
	if feeBps < 0 || feeBps > bpsDenominator {
		return fiatAmounts{}, errRateUnavailable
	}

	usd := decimal.NewFromInt(quota).Div(rates.QuotaPerUnit)
	gross := usd.Mul(rates.FxRate).Round(fiatScale)
	fee := gross.
		Mul(decimal.NewFromInt(int64(feeBps))).
		Div(decimal.NewFromInt(bpsDenominator)).
		Round(fiatScale)
	net := gross.Sub(fee)

	if net.LessThanOrEqual(decimal.Zero) {
		return fiatAmounts{}, errFeeEatsAll
	}
	return fiatAmounts{Gross: gross, Fee: fee, Net: net}, nil
}

// minFiatAmount 解析配置里的法币最低提现额。
func minFiatAmount() (decimal.Decimal, error) {
	raw := strings.TrimSpace(config.Get().Withdraw.MinFiatAmount)
	if raw == "" {
		return decimal.Zero, nil
	}
	d, err := decimal.NewFromString(raw)
	if err != nil {
		return decimal.Zero, errRateUnavailable
	}
	return d, nil
}
