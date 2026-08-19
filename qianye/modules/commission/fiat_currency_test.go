package commission

import (
	"testing"

	"github.com/QuantumNous/new-api/qianye/config"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// fiat_currency_test.go —— 法币余额的**币种**必须与生成它的汇率来自同一时刻。
//
// 深测查出来的缺口:qy_commission_balance.fiat_currency 全站零处赋值,5 行余额
// 该列全是空串,用户端只好拿**当前**的 withdraw.fiat_currency 去顶替。于是
// 「逐笔冻结汇率」这条设计在币种维度上根本不存在:运营改一次配置,历史金额
// 一个数字没动、标签全变了,而线下是照着单据上的币种打款的。

// commissionConfigWithCurrency 在最小配置上补一个法币币种。
func commissionConfigWithCurrency(currency string) *config.Config {
	c := commissionConfig(0)
	c.Withdraw.FiatCurrency = currency
	return c
}

func TestFrozenFiatCurrency(t *testing.T) {
	useConfig(t, commissionConfigWithCurrency("CNY"))
	for _, tc := range []struct {
		name   string
		frozen string
		want   string
	}{
		{"余额行上冻结过币种就用它", "USD", "USD"},
		{"冻结的币种与当前配置不同也用冻结的那个", "JPY", "JPY"},
		{"空串是补这一列之前的存量行,回落到当前配置", "", "CNY"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			assert.Equal(t, tc.want, frozenFiatCurrency(tc.frozen))
		})
	}
}

// 结算把币种与金额一起写回余额行。
//
// 判据打在**库行**上而不是返回值上:漏写的那一版里 available_fiat 一直是对的,
// 少的正是它旁边那一列。
func TestSettleFreezesFiatCurrencyOnBalance(t *testing.T) {
	t.Run("首次结算把当前币种冻结进余额行", func(t *testing.T) {
		gdb := newTestDB(t)
		useConfig(t, commissionConfigWithCurrency("CNY"))
		useMoneyGlobals(t, 7.3, 500000)

		seedBalance(t, gdb, 42, "4000")
		settleUserOnce(t, 42)

		bal := balanceOf(t, gdb, 42)
		require.NotNil(t, bal)
		assert.True(t, bal.AvailableFiat.IsPositive(), "法币金额本身仍要算出来")
		assert.Equal(t, "CNY", bal.FiatCurrency,
			"金额与币种必须一起冻结,否则 available_fiat 是一个不知道自己是什么钱的数")
	})

	t.Run("存量空串行在下一次结算时被补上", func(t *testing.T) {
		gdb := newTestDB(t)
		useConfig(t, commissionConfigWithCurrency("CNY"))
		useMoneyGlobals(t, 7.3, 500000)

		bal := seedBalance(t, gdb, 43, "4000")
		require.Empty(t, bal.FiatCurrency, "夹具必须真的是一行存量空串,否则这条永真")

		settleUserOnce(t, 43)
		assert.Equal(t, "CNY", balanceOf(t, gdb, 43).FiatCurrency)
	})
}

// 用户端下发的币种取自余额行,不是当前配置。
//
// 这一条是给运营看的那句「折合法币 0.68 CNY」的唯一来源:改一次全局配置就
// 让历史金额换个标签,等于把一个 USD 量级的数标成 CNY —— 按 7.3 的汇率是七倍。
func TestSummaryReportsFrozenFiatCurrencyNotTheCurrentConfig(t *testing.T) {
	gdb := newTestDB(t)
	useHealthyExtDB(t)
	useMoneyGlobals(t, 7.3, 500000)

	bal := seedBalance(t, gdb, 1, "0")
	bal.FiatCurrency = "USD"
	require.NoError(t, gdb.Save(bal).Error)

	// 配置此刻是 CNY:下发的必须仍是余额行上冻结的 USD。
	useConfig(t, commissionConfigWithCurrency("CNY"))
	data := readCommissionData(t, "/api/qy/commission/summary", getSummary)
	raw, ok := data["fiat_currency"]
	require.True(t, ok, "响应里没有 fiat_currency 字段,字段名改过了?")
	assert.JSONEq(t, `"USD"`, string(raw))
}
