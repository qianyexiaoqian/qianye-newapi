package controller

import (
	"go/ast"
	"go/parser"
	"go/token"
	"testing"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/setting/operation_setting"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func withTokensDisplay(t *testing.T, quotaPerUnit float64) {
	t.Helper()
	prevType := operation_setting.GetGeneralSetting().QuotaDisplayType
	prevUnit := common.QuotaPerUnit
	operation_setting.GetGeneralSetting().QuotaDisplayType = operation_setting.QuotaDisplayTypeTokens
	common.QuotaPerUnit = quotaPerUnit
	t.Cleanup(func() {
		operation_setting.GetGeneralSetting().QuotaDisplayType = prevType
		common.QuotaPerUnit = prevUnit
	})
}

// TOKENS 模式下「按多少收钱」与「按多少到账」必须出自同一个数。
//
// 缺陷原样：金额用精确除法算（getPayMoney 的 dAmount.Div(dQuotaPerUnit)），
// 落库的 Amount 却用 IntPart() 截断，结算再乘回 QuotaPerUnit。tokens=999999
// 于是按 1.999998 个单位收钱、按 1 个单位到账，差额被静默吞掉且全链路无提示。
func TestTokensModeTopUpAmountIsNormalizedBeforePricing(t *testing.T) {
	withTokensDisplay(t, 500000)

	cases := []struct {
		name string
		in   int64
		want int64
	}{
		{"非整倍数向下取整", 999999, 500000},
		{"整倍数不变", 1000000, 1000000},
		{"不足一个单位归零(随后会被下界拒掉)", 499999, 0},
		{"多个单位", 2999999, 2500000},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := normalizeTopUpAmount(tc.in)
			assert.Equal(t, tc.want, got)

			// 取整之后，收的钱与到账的额度必须严格对应。
			if got > 0 {
				payMoney := getPayMoney(got, "default")
				credited, err := (&model.TopUp{
					PaymentProvider: model.PaymentProviderEpay,
					Amount:          int64(float64(got) / common.QuotaPerUnit),
					Money:           payMoney,
				}).CreditQuota()
				require.NoError(t, err)
				assert.Equal(t, int(got), credited,
					"付的钱对应 %d tokens,到账却是 %d —— 差额被静默吞掉", got, credited)
			}
		})
	}
}

// USD 模式下这条取整不许生效：那里的 amount 本来就是金额单位。
func TestNormalizeTopUpAmountIsANoOpOutsideTokensMode(t *testing.T) {
	prev := operation_setting.GetGeneralSetting().QuotaDisplayType
	operation_setting.GetGeneralSetting().QuotaDisplayType = operation_setting.QuotaDisplayTypeUSD
	t.Cleanup(func() { operation_setting.GetGeneralSetting().QuotaDisplayType = prev })

	assert.Equal(t, int64(37), normalizeTopUpAmount(37))
}

// Stripe 的上下界必须同口径，否则整条通道在 TOKENS 模式下不可用。
//
// 下界在 TOKENS 模式下乘了 QuotaPerUnit，上界原先硬编码 10000 —— 于是
// min(500000) > max(10000)，任何金额都过不去，而两条报错互相矛盾
// （小于 500000 报"不能小于 500000"，大于等于 500000 报"不能大于 10000"）。
func TestStripeBoundsStayOrderedInTokensMode(t *testing.T) {
	withTokensDisplay(t, 500000)
	min, max := getStripeMinTopup(), getStripeMaxTopup()
	require.Less(t, min, max,
		"下界必须小于上界,否则 Stripe 充值在 TOKENS 模式下对任何金额都返回错误")
	// 上界是两道闸的**较小者**:10000 个单位这条产品策略,与结算侧能表示的额度。
	// 断言取小本身,而不是断言当下哪一道更紧 —— 后者随 common.MaxQuota 而变,
	// 而"取小"是这段代码要守的不变量。
	creditable := getMaxTopup()
	productCap := int64(10000 * int(common.QuotaPerUnit))
	want := productCap
	if creditable < want {
		want = creditable
	}
	assert.Equal(t, want, max, "上界必须是产品策略与结算侧容量的较小者")
}

// Stripe 报的上界必须与另外三条通道是同一个物理约束、同一个数。
//
// 上界原先是硬编码的 10000 个单位，与 getMaxTopup()（结算侧 CreditQuota 能表示
// 的真实上界）互不知情：落在两者之间的输入被第二道闸以「充值额度超出系统可表示
// 范围」拒掉 —— 这句话不指向任何用户能做的动作，而 epay/waffo/pancake 三条路对
// 同一个约束报的是「充值数量不能大于 N」。
//
// 两个方向都要覆盖:common.MaxQuota 抬高之后,默认单价下更紧的那一道换成了
// 产品策略;把 QuotaPerUnit 调大才能重新让结算侧更紧。用例按**哪一道更紧**
// 分组,期望值一律由 common.MaxQuota 算出来。
func TestStripeMaxTopupNeverExceedsTheCreditableCeiling(t *testing.T) {
	cases := []struct {
		name         string
		displayType  string
		quotaPerUnit float64
		wantMax      int64
	}{
		{"USD 单价 1e9:结算侧更紧", operation_setting.QuotaDisplayTypeUSD, 1e9,
			int64(common.MaxQuota-1) / 1_000_000_000},
		{"TOKENS 单价 1e9:结算侧更紧", operation_setting.QuotaDisplayTypeTokens, 1e9,
			int64(common.MaxQuota-1) / 1_000_000_000 * 1_000_000_000},
		{"USD 单价 500000:10000 这条产品策略更紧", operation_setting.QuotaDisplayTypeUSD, 500000, 10000},
		{"USD 单价 1:10000 这条产品策略更紧", operation_setting.QuotaDisplayTypeUSD, 1, 10000},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			withTopUpQuotaEnv(t, tc.displayType, tc.quotaPerUnit)
			require.Equal(t, tc.wantMax, getStripeMaxTopup())

			// 上界自己放行的那一笔必须真的能换算出额度（倍率 1 的分组）。
			credited, err := (&model.TopUp{
				PaymentProvider: model.PaymentProviderStripe,
				Money:           stripeChargedMoneyForGroup(float64(getStripeMaxTopup()), "default"),
			}).CreditQuota()
			require.NoError(t, err, "上界自己放行的金额必须能换算成功")
			assert.Greater(t, credited, 0)
			assert.LessOrEqual(t, credited, common.MaxQuota-1)
		})
	}
}

// TOKENS 模式下报价端与落库端必须同口径。
//
// getStripePayMoney 除了 QuotaPerUnit，GetChargedAmount 原先没除，而落库的
// Money 取自后者、CreditQuota 的 stripe 分支又按 Money × QuotaPerUnit 换额度：
// 两端差 QuotaPerUnit 倍。
func TestStripeChargedAmountFollowsTokensSemantics(t *testing.T) {
	withTokensDisplay(t, 500000)
	user := model.User{Group: "default"}
	assert.Equal(t, 2.0, GetChargedAmount(1000000, user),
		"1000000 tokens = 2 个单位;不除就会按 1000000 个单位落库")
}

// RequestEpay 必须**真的**调用 normalizeTopUpAmount。
//
// 单测只证明这个函数算得对，证明不了下单路径用了它 —— 而缺陷恰恰是“两侧口径
// 不一致”，删掉调用点单测仍然全绿。gin 注册之后拿不回处理链、运行时又要先过
// 会话鉴权与真实网关客户端，判据只能落在源码上（与
// router/redeem_route_rate_limit_test.go 同形）。
func TestRequestEpayNormalizesBeforePricing(t *testing.T) {
	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, "topup.go", nil, 0)
	require.NoError(t, err)

	var body *ast.FuncDecl
	for _, d := range file.Decls {
		if fn, ok := d.(*ast.FuncDecl); ok && fn.Name.Name == "RequestEpay" {
			body = fn
		}
	}
	require.NotNil(t, body, "RequestEpay 不见了,这份守卫已经过期")

	found := false
	ast.Inspect(body, func(n ast.Node) bool {
		call, ok := n.(*ast.CallExpr)
		if !ok {
			return true
		}
		if id, ok := call.Fun.(*ast.Ident); ok && id.Name == "normalizeTopUpAmount" {
			found = true
		}
		return true
	})
	assert.True(t, found,
		"RequestEpay 必须先把 amount 取整再计价与落库;少了它,收的钱按精确除法算、"+
			"到账按截断算,差额被静默吞掉")
}
