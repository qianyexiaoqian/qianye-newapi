package withdraw

import (
	"strings"

	"github.com/QuantumNous/new-api/common"

	"github.com/shopspring/decimal"
)

// pricing.go —— 一张法币提现单上的金额是怎么来的。
//
// # 唯一来源:佣金账本
//
// 应付金额(gross)== 本次冻结从 qy_commission_balance.available_fiat 里削走的
// 那个绝对值,由 commission.QuoteWithdrawFiat 给出。本文件只负责在这个数上
// 拆手续费、判最低额,以及把它反解成一个可解释的冻结汇率。
//
// 这里曾经有一套**独立的**计价:quota / QuotaPerUnit × 充值页汇率
// (operation_setting.USDExchangeRate,或 withdraw.rate_freeze_fixed)。
// 它与账本那一侧毫无关系 —— 账本按计佣当刻的三层折算比例(分组档 → 兜底档 →
// 全站汇率)一笔笔攒,单据按提交当刻的充值汇率现算。默认配置下两者恰好相等,
// 所以装完不错;运营只要配一个分组结汇档、或改一次充值汇率,全站每一笔法币
// 提现就开始按错的价打款(实测:账本冻走 850 CNY,单据让运营付 100 CNY)。
// 详见 commission.QuoteWithdrawFiat 的说明。
//
// 因此 withdraw.rate_freeze_mode / rate_freeze_fixed 这两个配置项已经删除:
// 一个"平台按自己的结算价打款"的旋钮,正确的落点是佣金侧的法币折算档
// (commission 的分组档 / 兜底档),它在计佣当刻就冻进账本,提现只是照着付。
// 留一个第二汇率源在提现侧,等于把刚修掉的这个缺陷原样留一个入口。

// fiatScale 是法币金额的存储精度(裁定 C26:decimal(18,6),展示层再 Round(2))。
//
// 不用 2 位:手续费按比率算会出现分以下的位,先按 2 位存会让
// "应付 = 手续费 + 实付"这个恒等式对不上,财务复核时无法解释差额。
const fiatScale = 6

// fxRateScale 是冻结汇率的存储精度,与 qy_withdrawals.frozen_fx_rate 的
// decimal(18,8) 一致。
const fxRateScale = 8

// bpsDenominator 是万分比的分母(裁定 C28:比例一律用整数 bps)。
const bpsDenominator = 10000

// frozenQuotaPerUnit 冻结当前的额度单位。
//
// common.QuotaPerUnit 是管理员可以随时热改的全局变量。不冻结的话,一年后没有
// 任何办法复现"当时这笔佣金折合多少法币",历史对账永远对不上。
//
// 它被改成 0 或负数时拒绝建单:整单的 usd 口径会变成 0 或负,而 frozen_fx_rate
// 是拿它反解出来的,除以 0 只能编一个数字出来。宁可拒绝并告警。
func frozenQuotaPerUnit() (decimal.Decimal, error) {
	perUnit := decimal.NewFromFloat(common.QuotaPerUnit)
	if perUnit.LessThanOrEqual(decimal.Zero) {
		common.SysError("qianye/withdraw: 计价参数非法(quota_per_unit=" +
			perUnit.String() + "),已拒绝建单")
		return decimal.Zero, errRateUnavailable
	}
	return perUnit, nil
}

// fiatAmounts 是一张法币提现单的三段金额。
type fiatAmounts struct {
	Gross decimal.Decimal
	Fee   decimal.Decimal
	Net   decimal.Decimal
}

// splitFiat 把账本给出的应付金额拆成 应付 / 手续费 / 实付。
//
// 全程 decimal,禁止 float64 中间量:float 的舍入分歧会直接变成对不上的账。
// 换算链是单向的(账本法币 → 单据三段),绝不反算 —— 让用户输法币金额再反推
// 额度,必然产生"输 ¥100 实扣 ¥100.0001 佣金"这类无法消除的对账噪声。
//
// Gross 与 Fee 各自 Round 到 6 位后,Net = Gross - Fee 仍是精确的 6 位小数,
// 因此恒等式 Gross == Fee + Net 天然成立,无需再做断言。
func splitFiat(gross decimal.Decimal, feeBps int) (fiatAmounts, error) {
	if gross.LessThanOrEqual(decimal.Zero) {
		return fiatAmounts{}, errAmountOutOfRange
	}
	if feeBps < 0 || feeBps > bpsDenominator {
		return fiatAmounts{}, errRateUnavailable
	}

	g := gross.Round(fiatScale)
	fee := g.
		Mul(decimal.NewFromInt(int64(feeBps))).
		Div(decimal.NewFromInt(bpsDenominator)).
		Round(fiatScale)
	net := g.Sub(fee)

	if net.LessThanOrEqual(decimal.Zero) {
		return fiatAmounts{}, errFeeEatsAll
	}
	return fiatAmounts{Gross: g, Fee: fee, Net: net}, nil
}

// impliedFxRate 反解本单实际适用的汇率:gross ÷ (quota ÷ quotaPerUnit)。
//
// 它是一个**派生的展示/对账值**,不参与算钱 —— 单据金额只由账本给出。
// 但它必须留在单据上:没有它,一年后拿着一张 ¥850 的单据无法回答
// "当时是按几比几结的汇",而那正是 frozen_fx_rate 这一列存在的全部理由。
// 舍入到 8 位是存储列的精度,因此 gross 不一定精确等于 usd × 本值。
func impliedFxRate(gross decimal.Decimal, quota int64, quotaPerUnit decimal.Decimal) decimal.Decimal {
	if quota <= 0 || quotaPerUnit.LessThanOrEqual(decimal.Zero) {
		return decimal.Zero
	}
	usd := decimal.NewFromInt(quota).Div(quotaPerUnit)
	if usd.LessThanOrEqual(decimal.Zero) {
		return decimal.Zero
	}
	return gross.Div(usd).Round(fxRateScale)
}

// minFiatAmount 解析配置里的法币最低提现额。
func minFiatAmount(raw string) (decimal.Decimal, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return decimal.Zero, nil
	}
	d, err := decimal.NewFromString(raw)
	if err != nil {
		return decimal.Zero, errRateUnavailable
	}
	return d, nil
}
