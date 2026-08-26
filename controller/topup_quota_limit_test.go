package controller

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/setting"
	"github.com/QuantumNous/new-api/setting/operation_setting"

	"github.com/gin-gonic/gin"
	"github.com/glebarez/sqlite"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

// withTopUpQuotaEnv 固定展示类型与单价，并挂一个只有 users 表的内存库。
func withTopUpQuotaEnv(t *testing.T, displayType string, quotaPerUnit float64) {
	t.Helper()
	prevType := operation_setting.GetGeneralSetting().QuotaDisplayType
	prevUnit := common.QuotaPerUnit
	prevDB := model.DB

	operation_setting.GetGeneralSetting().QuotaDisplayType = displayType
	common.QuotaPerUnit = quotaPerUnit

	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	require.NoError(t, err)
	// glebarez/sqlite 的 ":memory:" 是**按连接**隔离的：连接池开第二条连接时
	// 建的是另一个空库。GetUserGroup 会往 gopool 里丢一个 RefreshUserGroupCache
	// 协程（common.RedisEnabled 默认为 true），它抢到第二条连接就会把这条空库
	// 连接留在池里，之后任何查询都可能被它服务 —— users 表凭空消失，断言随机
	// 落到「获取用户分组失败」。锁成单连接是唯一可靠的修法。
	// 同仓 qianye/modules/commission/testdb_test.go:130 的 useMainDB 同因同法。
	sqlDB, err := db.DB()
	require.NoError(t, err)
	sqlDB.SetMaxOpenConns(1)
	require.NoError(t, db.AutoMigrate(&model.User{}, &model.TopUp{}))
	model.DB = db
	// 换了 DB 句柄就必须重新初始化方言列名，否则 GetUserGroup 拼出的
	// `SELECT  FROM users` 会在 SQLite 上语法错误。
	model.InitCol()

	t.Cleanup(func() {
		operation_setting.GetGeneralSetting().QuotaDisplayType = prevType
		common.QuotaPerUnit = prevUnit
		model.DB = prevDB
		_ = sqlDB.Close()
	})
}

// enableWaffoGateways 打开 waffo / waffo-pancake 两条通道，让下单接口能走到
// 金额校验那一步（否则先被「未启用」挡掉，测不到这道闸）。
func enableWaffoGateways(t *testing.T) {
	t.Helper()
	paymentSetting := operation_setting.GetPaymentSetting()
	prev := *paymentSetting
	prevWaffoEnabled := setting.WaffoEnabled
	prevMerchant := setting.WaffoPancakeMerchantID
	prevKey := setting.WaffoPancakePrivateKey
	prevProduct := setting.WaffoPancakeProductID

	paymentSetting.ComplianceConfirmed = true
	paymentSetting.ComplianceTermsVersion = operation_setting.CurrentComplianceTermsVersion
	setting.WaffoEnabled = true
	setting.WaffoPancakeMerchantID = "qy-test-merchant"
	setting.WaffoPancakePrivateKey = "qy-test-key"
	setting.WaffoPancakeProductID = "qy-test-product"

	t.Cleanup(func() {
		*paymentSetting = prev
		setting.WaffoEnabled = prevWaffoEnabled
		setting.WaffoPancakeMerchantID = prevMerchant
		setting.WaffoPancakePrivateKey = prevKey
		setting.WaffoPancakeProductID = prevProduct
	})
}

func insertTopUpQuotaTestUser(t *testing.T, id int, quota int) {
	t.Helper()
	require.NoError(t, model.DB.Create(&model.User{
		Id:       id,
		Username: fmt.Sprintf("qy-topup-cap-%d", id),
		Group:    "default",
		Quota:    quota,
		Status:   common.UserStatusEnabled,
	}).Error)
}

func postTopUpJSON(t *testing.T, handler gin.HandlerFunc, userId int, path string, body string) *httptest.ResponseRecorder {
	t.Helper()
	gin.SetMode(gin.TestMode)
	recorder := httptest.NewRecorder()
	ctx, _ := gin.CreateTestContext(recorder)
	ctx.Set("id", userId)
	ctx.Request = httptest.NewRequest(http.MethodPost, path, strings.NewReader(body))
	ctx.Request.Header.Set("Content-Type", "application/json")
	handler(ctx)
	return recorder
}

// getMaxTopup 的分子必须是 MaxQuota-1，不是 MaxQuota。
//
// common/quota_math.go 的 saturateQuota 在 value >= MaxQuota 时就报错，所以可表示
// 的最大额度是 MaxQuota-1。用 MaxQuota 做分子时 QuotaPerUnit==1 会算出 MaxQuota
// —— 这个数恰好换算失败，也就是上界自己放行了一个必然在结算侧回滚的值：钱进网关、
// 额度零到账、订单永久 pending。这正是这条上界要挡的那个后果。
func TestGetMaxTopupNeverAllowsAnUncreditableAmount(t *testing.T) {
	cases := []struct {
		name         string
		displayType  string
		quotaPerUnit float64
		wantMax      int64
	}{
		// 期望值一律由 common.MaxQuota 算出来。抄一个 4294 进来的代价在这次
		// 抬高上界时刚刚兑现过:上界一动,这条用例断言的就不再是"闸门开在
		// 可换算的最后一格",而是"闸门开在某个远低于它的旧数字上"。
		{"USD 单价 500000", operation_setting.QuotaDisplayTypeUSD, 500000, int64((common.MaxQuota - 1) / 500000)},
		{"TOKENS 单价 500000", operation_setting.QuotaDisplayTypeTokens, 500000, int64((common.MaxQuota-1)/500000) * 500000},
		{"USD 单价 1（旧实现在这里差一）", operation_setting.QuotaDisplayTypeUSD, 1, int64(common.MaxQuota - 1)},
		{"USD 单价 3（不整除）", operation_setting.QuotaDisplayTypeUSD, 3, int64((common.MaxQuota - 1) / 3)},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			withTopUpQuotaEnv(t, tc.displayType, tc.quotaPerUnit)

			maxAmount := getMaxTopup()
			require.Equal(t, tc.wantMax, maxAmount)

			// 闸门上沿这一笔必须真的能换算出额度……
			amount := maxAmount
			if tc.displayType == operation_setting.QuotaDisplayTypeTokens {
				amount = int64(float64(maxAmount) / tc.quotaPerUnit)
			}
			credited, err := (&model.TopUp{PaymentProvider: model.PaymentProviderEpay, Amount: amount}).CreditQuota()
			require.NoError(t, err, "上界自己放行的金额必须能换算成功")
			assert.Greater(t, credited, 0)
			assert.LessOrEqual(t, credited, common.MaxQuota-1)

			// ……而再多一个单位必须换算失败，否则闸门关早了、白拦用户。
			_, err = (&model.TopUp{PaymentProvider: model.PaymentProviderEpay, Amount: amount + 1}).CreditQuota()
			require.Error(t, err, "上界必须正好卡在可表示范围的边沿")
		})
	}
}

// 下单与询价两条路必须共用同一道闸，否则前端先拿到一个报价、付款时才被拒。
func TestTopUpEntryPointsRejectAmountAboveCeiling(t *testing.T) {
	entries := []struct {
		name    string
		path    string
		handler gin.HandlerFunc
	}{
		{"易支付下单", "/api/user/pay", RequestEpay},
		{"易支付询价", "/api/user/amount", RequestAmount},
		{"waffo 下单", "/api/user/waffo/pay", RequestWaffoPay},
		{"waffo 询价", "/api/user/waffo/amount", RequestWaffoAmount},
		{"pancake 下单", "/api/user/waffo_pancake/pay", RequestWaffoPancakePay},
		{"pancake 询价", "/api/user/waffo_pancake/amount", RequestWaffoPancakeAmount},
	}

	for _, e := range entries {
		t.Run(e.name, func(t *testing.T) {
			withTopUpQuotaEnv(t, operation_setting.QuotaDisplayTypeUSD, 500000)
			enableWaffoGateways(t)
			insertTopUpQuotaTestUser(t, 42, 0)

			maxAmount := getMaxTopup() // 4294
			recorder := postTopUpJSON(t, e.handler, 42, e.path,
				fmt.Sprintf(`{"amount":%d,"payment_method":"waffo"}`, maxAmount+1))

			assert.Equal(t, http.StatusOK, recorder.Code)
			assert.JSONEq(t,
				fmt.Sprintf(`{"message":"error","data":"充值数量不能大于 %d"}`, maxAmount),
				recorder.Body.String())
		})
	}
}

// 单笔上界只保证「这一笔自己」能被表示。余额已经很高的账号照样能下出一张结算时
// 必然回滚的订单 —— 钱进网关、额度零到账、订单永久 pending，与完全没有上界时的
// 后果逐字相同。同步自上游 47ba9d2c6 的 ValidateTopUpQuotaCapacity。
func TestTopUpRejectsOrderThatWouldOverflowTheWallet(t *testing.T) {
	const quotaPerUnit = 500000
	const amount = 4294
	credited := amount * quotaPerUnit // 2,147,000,000

	cases := []struct {
		name         string
		currentQuota int
		wantRejected bool
	}{
		{"钱包正好装得下", common.MaxQuota - 1 - credited, false},
		{"钱包多一个单位就装不下", common.MaxQuota - credited, true},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			withTopUpQuotaEnv(t, operation_setting.QuotaDisplayTypeUSD, quotaPerUnit)
			insertTopUpQuotaTestUser(t, 43, tc.currentQuota)

			recorder := postTopUpJSON(t, RequestAmount, 43, "/api/user/amount",
				fmt.Sprintf(`{"amount":%d}`, amount))

			assert.Equal(t, http.StatusOK, recorder.Code)
			if tc.wantRejected {
				assert.JSONEq(t,
					`{"message":"error","data":"充值后余额将超出系统上限，请先消耗部分额度"}`,
					recorder.Body.String())
				return
			}
			assert.Contains(t, recorder.Body.String(), `"message":"success"`)
		})
	}
}

// Stripe 的到账额度按 Money（已乘分组倍率）换算，所以上界必须落在 Money 上。
// 只看 req.Amount 的话，倍率 > 1 的分组在 10000 这道闸之内也能把换算顶穿。
func TestStripeRequestAmountAccountsForTopUpGroupRatio(t *testing.T) {
	// 单价取 1e9 而不是默认的 500000:common.MaxQuota 抬高之后,默认单价下
	// 可换算的下单量远超 Stripe 那 10000 个单位的产品上限,先触发的会是产品
	// 闸门,这条用例就测不到"上界必须落在乘过倍率的 Money 上"了。
	withTopUpQuotaEnv(t, operation_setting.QuotaDisplayTypeUSD, 1e9)
	insertTopUpQuotaTestUser(t, 44, 0)

	prevRatio := common.TopupGroupRatio2JSONString()
	require.NoError(t, common.UpdateTopupGroupRatioByJSONString(`{"default":2}`))
	t.Cleanup(func() { require.NoError(t, common.UpdateTopupGroupRatioByJSONString(prevRatio)) })

	// 闸门那一格由 common.MaxQuota 算出来:倍率 2、单价 500000 时,可换算的最大
	// 下单量是 (MaxQuota-1)/(2*500000)。它 +1 就顶穿。
	const stripeRatioTestDivisor = 2 * 1_000_000_000
	atLimit := int64((common.MaxQuota - 1) / stripeRatioTestDivisor)
	require.Equal(t, float64(atLimit*2), stripeChargedMoneyForGroup(float64(atLimit), "default"))

	adaptor := &StripeAdaptor{}

	ok := postTopUpJSON(t, func(c *gin.Context) {
		adaptor.RequestAmount(c, &StripePayRequest{Amount: atLimit})
	}, 44, "/api/user/stripe/amount", `{}`)
	assert.Contains(t, ok.Body.String(), `"message":"success"`)

	rejected := postTopUpJSON(t, func(c *gin.Context) {
		adaptor.RequestAmount(c, &StripePayRequest{Amount: atLimit + 1})
	}, 44, "/api/user/stripe/amount", `{}`)
	assert.JSONEq(t,
		`{"message":"error","data":"充值额度超出系统可表示范围"}`,
		rejected.Body.String())
}

// Creem 的到账额度整个来自管理端配置的产品表，不经过 Amount 计价那道闸，
// 所以它只能靠下单前那次换算 + 容量校验拦住。走真实 handler，否则测的是助手
// 函数而不是这条路径上真的装了闸。
func TestCreemRequestPayValidatesProductQuotaAgainstWalletCapacity(t *testing.T) {
	const productId = "qy-test-product"

	cases := []struct {
		name         string
		currentQuota int
		productQuota int64
		wantBody     string
	}{
		{
			name:         "产品额度装不进钱包",
			currentQuota: common.MaxQuota - 1000,
			productQuota: 2000,
			wantBody:     `{"message":"error","data":"充值后余额将超出系统上限，请先消耗部分额度"}`,
		},
		{
			name:         "产品额度本身超出可表示范围",
			currentQuota: 0,
			productQuota: int64(common.MaxQuota),
			wantBody:     `{"message":"error","data":"充值额度超出系统可表示范围"}`,
		},
		{
			name:         "产品额度配成 0",
			currentQuota: 0,
			productQuota: 0,
			wantBody:     `{"message":"error","data":"充值额度必须大于 0"}`,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			withTopUpQuotaEnv(t, operation_setting.QuotaDisplayTypeUSD, 500000)
			insertTopUpQuotaTestUser(t, 45, tc.currentQuota)

			prevProducts := setting.CreemProducts
			setting.CreemProducts = fmt.Sprintf(
				`[{"productId":"%s","name":"qy","price":1,"currency":"USD","quota":%d}]`,
				productId, tc.productQuota)
			t.Cleanup(func() { setting.CreemProducts = prevProducts })

			adaptor := &CreemAdaptor{}
			recorder := postTopUpJSON(t, func(c *gin.Context) {
				adaptor.RequestPay(c, &CreemPayRequest{
					PaymentMethod: model.PaymentMethodCreem,
					ProductId:     productId,
				})
			}, 45, "/api/user/creem/pay", `{}`)

			assert.Equal(t, http.StatusOK, recorder.Code)
			assert.JSONEq(t, tc.wantBody, recorder.Body.String())

			// 被拦下的订单一行都不能落库，否则回调侧还会拿到一张永远结算不了的单。
			var count int64
			require.NoError(t, model.DB.Model(&model.TopUp{}).Count(&count).Error)
			assert.Zero(t, count)
		})
	}
}

// TOKENS 展示模式下，校验必须拿**落库的那个 Amount** 去换算到账额度。
//
// 前端传的是 tokens，而三条 Amount 计价渠道落库前都会除以 QuotaPerUnit。校验若直接
// 拿请求里的 tokens 去算，就会用一个放大了 QuotaPerUnit 倍的数去换算 —— 一笔完全
// 正常的充值被误判成「超出可表示范围」，TOKENS 模式下整条通道被自己的上界闸门堵死。
func TestTokensModeTopUpPassesTheStoredAmountToTheCeilingCheck(t *testing.T) {
	const quotaPerUnit = 500000

	// 闸门上沿是"可换算的最后一个单位数",由 common.MaxQuota 算出来。
	unitsAtLimit := int64((common.MaxQuota - 1) / quotaPerUnit)

	cases := []struct {
		name     string
		tokens   int64
		wantPass bool
	}{
		{"正常一笔（4 个单位）", 4 * quotaPerUnit, true},
		{"闸门上沿", unitsAtLimit * quotaPerUnit, true},
		{"闸门上沿再加一个单位", (unitsAtLimit + 1) * quotaPerUnit, false},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			withTopUpQuotaEnv(t, operation_setting.QuotaDisplayTypeTokens, quotaPerUnit)
			insertTopUpQuotaTestUser(t, 46, 0)

			recorder := postTopUpJSON(t, RequestAmount, 46, "/api/user/amount",
				fmt.Sprintf(`{"amount":%d}`, tc.tokens))

			require.Equal(t, http.StatusOK, recorder.Code)
			if tc.wantPass {
				assert.Contains(t, recorder.Body.String(), `"message":"success"`,
					"落库 Amount 是 tokens/QuotaPerUnit，这一笔本该放行")
				return
			}
			assert.JSONEq(t,
				fmt.Sprintf(`{"message":"error","data":"充值数量不能大于 %d"}`, unitsAtLimit*quotaPerUnit),
				recorder.Body.String())
		})
	}
}
