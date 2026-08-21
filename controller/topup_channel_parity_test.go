package controller

import (
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/setting"
	"github.com/QuantumNous/new-api/setting/operation_setting"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/stripe/stripe-go/v81"
)

// withUnitPrices 把四条通道的单价与下界钉成同一组已知值，让「报价」这一步的
// 期望值可以手算出来。
func withUnitPrices(t *testing.T, unitPrice float64, minTopUp int) {
	t.Helper()
	prevPrice, prevMin := operation_setting.Price, operation_setting.MinTopUp
	prevWaffo, prevWaffoMin := setting.WaffoUnitPrice, setting.WaffoMinTopUp
	prevPancake, prevPancakeMin := setting.WaffoPancakeUnitPrice, setting.WaffoPancakeMinTopUp
	prevStripe, prevStripeMin := setting.StripeUnitPrice, setting.StripeMinTopUp

	operation_setting.Price, operation_setting.MinTopUp = unitPrice, minTopUp
	setting.WaffoUnitPrice, setting.WaffoMinTopUp = unitPrice, minTopUp
	setting.WaffoPancakeUnitPrice, setting.WaffoPancakeMinTopUp = unitPrice, minTopUp
	setting.StripeUnitPrice, setting.StripeMinTopUp = unitPrice, minTopUp

	t.Cleanup(func() {
		operation_setting.Price, operation_setting.MinTopUp = prevPrice, prevMin
		setting.WaffoUnitPrice, setting.WaffoMinTopUp = prevWaffo, prevWaffoMin
		setting.WaffoPancakeUnitPrice, setting.WaffoPancakeMinTopUp = prevPancake, prevPancakeMin
		setting.StripeUnitPrice, setting.StripeMinTopUp = prevStripe, prevStripeMin
	})
}

// TOKENS 展示模式下，四条通道的报价都必须落在**取整之后**的数上。
//
// 缺陷原样（waffo / waffo-pancake / stripe 三条）：报价用精确除法算
// （amount / QuotaPerUnit），落库的 Amount 却截断到整单位，结算再按整单位乘
// 回去。tokens=999999 于是按 1.999998 个单位收钱、按 1 个单位到账，差额被静默
// 吞掉，最坏接近一整个单位价（付 2.00 拿 1.00，100%）。易支付这条路已被
// normalizeTopUpAmount 修过，这里把同一道闸钉在另外三条上。
func TestTokensModeQuotesArePricedOnTheNormalizedAmount(t *testing.T) {
	const quotaPerUnit = 500000

	quotes := []struct {
		name    string
		handler gin.HandlerFunc
		path    string
	}{
		{"易支付询价", RequestAmount, "/api/user/amount"},
		{"waffo 询价", RequestWaffoAmount, "/api/user/waffo/amount"},
		{"pancake 询价", RequestWaffoPancakeAmount, "/api/user/waffo_pancake/amount"},
	}

	cases := []struct {
		name     string
		tokens   int64
		wantData string
	}{
		{"非整倍数按取整后的一个单位报价", 999999, "1.00"},
		{"整倍数不受影响", 1000000, "2.00"},
		{"多个单位", 2999999, "5.00"},
	}

	for _, q := range quotes {
		for _, tc := range cases {
			t.Run(q.name+"/"+tc.name, func(t *testing.T) {
				withTopUpQuotaEnv(t, operation_setting.QuotaDisplayTypeTokens, quotaPerUnit)
				withUnitPrices(t, 1, 1)
				enableWaffoGateways(t)
				insertTopUpQuotaTestUser(t, 51, 0)

				recorder := postTopUpJSON(t, q.handler, 51, q.path,
					fmt.Sprintf(`{"amount":%d}`, tc.tokens))

				require.Equal(t, http.StatusOK, recorder.Code)
				assert.JSONEq(t,
					fmt.Sprintf(`{"message":"success","data":%q}`, tc.wantData),
					recorder.Body.String())
			})
		}
	}
}

// 报价与落库必须出自同一个数：走真实下单 handler，把落库那一行读回来核对。
//
// 用户被收走的是 Money（waffo 把它格式化成 OrderAmount 发给网关），到账额度是
// Amount × QuotaPerUnit。两者必须严格对应，否则差额被静默吞掉。
func TestTokensModeOrdersStoreTheSameAmountTheyPriced(t *testing.T) {
	const quotaPerUnit = 500000

	orders := []struct {
		name    string
		handler gin.HandlerFunc
		path    string
	}{
		{"waffo 下单", RequestWaffoPay, "/api/user/waffo/pay"},
		{"pancake 下单", RequestWaffoPancakePay, "/api/user/waffo_pancake/pay"},
	}

	for _, o := range orders {
		t.Run(o.name, func(t *testing.T) {
			withTopUpQuotaEnv(t, operation_setting.QuotaDisplayTypeTokens, quotaPerUnit)
			withUnitPrices(t, 1, 1)
			enableWaffoGateways(t)
			insertTopUpQuotaTestUser(t, 52, 0)

			// 本机没有网关凭据，拉链接必然失败；但订单在那之前就已落库，
			// 落库的数字正是要发给网关的那一份。
			postTopUpJSON(t, o.handler, 52, o.path, `{"amount":999999}`)

			var topUp model.TopUp
			require.NoError(t, model.DB.Order("id desc").First(&topUp).Error)

			assert.Equal(t, int64(1), topUp.Amount, "999999 tokens 取整后是 1 个单位")
			assert.Equal(t, 1.0, topUp.Money, "按 1 个单位收钱,不是 1.999998 个")

			credited, err := topUp.CreditQuota()
			require.NoError(t, err)
			assert.Equal(t, int(quotaPerUnit*topUp.Money), credited,
				"付的钱对应 %.6f 个单位,到账却是 %d 额度 —— 差额被静默吞掉", topUp.Money, credited)
		})
	}
}

// Stripe Checkout 的 LineItem Quantity 必须是**单位数**，与报价、落库 Money、
// 结算换算同口径。
//
// 缺陷原样：genStripeLink 收到的是未经换算的 req.Amount，而 getStripePayMoney
// 与 stripeChargedMoneyForGroup 都先除以 QuotaPerUnit。一个站点只有一个
// StripePriceId，两种展示类型下同一笔经济上等价的订单发给 Stripe 的 quantity
// 相差正好 QuotaPerUnit 倍：切换展示类型时真实扣款额静默变化 50 万倍，而回调只
// 认 TopUp.Money、从不与 amount_total 对账，两个方向都不会被系统自己发现。
func TestStripeCheckoutQuantityMatchesTheQuotedUnits(t *testing.T) {
	const quotaPerUnit = 500000

	cases := []struct {
		name         string
		displayType  string
		amount       int64
		wantQuantity string
		wantMoney    float64
	}{
		{"USD 模式:1 个单位", operation_setting.QuotaDisplayTypeUSD, 1, "1", 1},
		{"TOKENS 模式:经济上等价的同一笔", operation_setting.QuotaDisplayTypeTokens, quotaPerUnit, "1", 1},
		{"TOKENS 模式:四个单位", operation_setting.QuotaDisplayTypeTokens, 4 * quotaPerUnit, "4", 4},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			withTopUpQuotaEnv(t, tc.displayType, quotaPerUnit)
			withUnitPrices(t, 1, 1)
			insertTopUpQuotaTestUser(t, 53, 0)

			form := captureStripeCheckoutForm(t)

			adaptor := &StripeAdaptor{}
			recorder := postTopUpJSON(t, func(c *gin.Context) {
				adaptor.RequestPay(c, &StripePayRequest{
					Amount:        tc.amount,
					PaymentMethod: model.PaymentMethodStripe,
				})
			}, 53, "/api/user/stripe/pay", `{}`)
			require.Contains(t, recorder.Body.String(), `"pay_link"`)

			assert.Equal(t, tc.wantQuantity, form().Get("line_items[0][quantity]"),
				"发给 Stripe 的 quantity 必须是单位数,不是原始 req.Amount")

			var topUp model.TopUp
			require.NoError(t, model.DB.Order("id desc").First(&topUp).Error)
			assert.Equal(t, tc.wantMoney, topUp.Money,
				"落库 Money 与 quantity 必须是同一个口径的同一个数")
		})
	}
}

// captureStripeCheckoutForm 把 stripe-go 的后端指向本地 httptest，返回一个取回
// 最后一次 /v1/checkout/sessions 表单的闭包。真实扣款额就是这份表单决定的，所
// 以判据必须落在它上面 —— 只断言换算助手函数的话，把调用点改回 req.Amount 仍
// 然全绿。
func captureStripeCheckoutForm(t *testing.T) func() url.Values {
	t.Helper()

	var captured url.Values
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		values, err := url.ParseQuery(string(body))
		require.NoError(t, err)
		captured = values
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"id":"cs_test_qy","object":"checkout.session","url":"https://checkout.example/cs_test_qy"}`))
	}))
	t.Cleanup(server.Close)

	prevKey, prevPriceId := setting.StripeApiSecret, setting.StripePriceId
	setting.StripeApiSecret = "sk_test_qy"
	setting.StripePriceId = "price_qy"
	prevBackend := stripe.GetBackend(stripe.APIBackend)
	stripe.SetBackend(stripe.APIBackend, stripe.GetBackendWithConfig(stripe.APIBackend, &stripe.BackendConfig{
		URL: stripe.String(server.URL),
	}))
	t.Cleanup(func() {
		stripe.SetBackend(stripe.APIBackend, prevBackend)
		setting.StripeApiSecret, setting.StripePriceId = prevKey, prevPriceId
	})

	return func() url.Values { return captured }
}

// 「充值金额过低」这道闸在询价与下单两侧必须是同一个判据。
//
// 缺陷原样：询价侧写 `payMoney <= 0.01`（更严）、下单侧写 `payMoney < 0.01`
// （更松），stripe 的下单侧干脆没有。0.01 是网关允许的最小收款额，放行它才是
// 正确的一侧 —— 于是 payMoney 恰好等于 0.01 的一笔，询价接口告诉用户「充值金额
// 过低」并把报价显示成 0，同一份请求体走下单接口却能建出一张真实订单。
func TestQuoteAndOrderShareTheSameLowAmountGate(t *testing.T) {
	entries := []struct {
		name    string
		handler gin.HandlerFunc
		path    string
	}{
		{"易支付询价", RequestAmount, "/api/user/amount"},
		{"易支付下单", RequestEpay, "/api/user/pay"},
		{"waffo 询价", RequestWaffoAmount, "/api/user/waffo/amount"},
		{"waffo 下单", RequestWaffoPay, "/api/user/waffo/pay"},
		{"pancake 询价", RequestWaffoPancakeAmount, "/api/user/waffo_pancake/amount"},
		{"pancake 下单", RequestWaffoPancakePay, "/api/user/waffo_pancake/pay"},
		{"stripe 询价", RequestStripeAmount, "/api/user/stripe/amount"},
		{"stripe 下单", RequestStripePay, "/api/user/stripe/pay"},
	}

	// 单价 0.001 × 10 = 0.01，恰好落在网关下限那一点上；× 9 = 0.009 在它之下。
	cases := []struct {
		name     string
		amount   int64
		rejected bool
	}{
		{"恰好等于网关下限的一分钱:两侧都必须放行", 10, false},
		{"低于网关下限:两侧都必须拒绝", 9, true},
	}

	for _, e := range entries {
		for _, tc := range cases {
			t.Run(e.name+"/"+tc.name, func(t *testing.T) {
				withTopUpQuotaEnv(t, operation_setting.QuotaDisplayTypeUSD, 500000)
				withUnitPrices(t, 0.001, 1)
				enableWaffoGateways(t)
				insertTopUpQuotaTestUser(t, 54, 0)

				recorder := postTopUpJSON(t, e.handler, 54, e.path,
					fmt.Sprintf(`{"amount":%d,"payment_method":"stripe"}`, tc.amount))

				require.Equal(t, http.StatusOK, recorder.Code)
				if tc.rejected {
					assert.Contains(t, recorder.Body.String(), "充值金额过低")
					return
				}
				// 放行之后各条路会各自撞上「没有网关凭据」之类的下游错误，
				// 判据只看这道闸有没有拦住它。
				assert.NotContains(t, recorder.Body.String(), "充值金额过低")
			})
		}
	}
}

// 单价配得极低时，`payMoney == 0.01` 这个点必须真的可达 —— 否则上面那条守卫
// 测的是一个不存在的边界。
func TestPayMoneyHitsTheGatewayMinimumExactly(t *testing.T) {
	withTopUpQuotaEnv(t, operation_setting.QuotaDisplayTypeUSD, 500000)
	withUnitPrices(t, 0.001, 1)

	require.Equal(t, minPayMoney, getPayMoney(10, "default"))
	require.Equal(t, minPayMoney, getWaffoPayMoney(10, "default"))
	require.Equal(t, minPayMoney, getWaffoPancakePayMoney(10, "default"))
	require.Equal(t, minPayMoney, getStripePayMoney(10, "default"))
	assert.Less(t, getPayMoney(9, "default"), minPayMoney)
	assert.False(t, common.QuotaPerUnit == 0, "夹具必须给出非零单价")
}
