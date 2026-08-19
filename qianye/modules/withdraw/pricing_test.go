package withdraw

import (
	"testing"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/setting/operation_setting"

	"github.com/shopspring/decimal"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// splitFiat 拿到的已经是账本给出的应付金额,它只负责拆手续费。
// 三段必须永远加得上,否则财务无法复核。
func TestSplitFiat(t *testing.T) {
	cases := []struct {
		name            string
		gross           string
		feeBps          int
		wantG, wantF    string
		wantN           string
		wantErrContains error
	}{
		{name: "零手续费", gross: "73", feeBps: 0, wantG: "73", wantF: "0", wantN: "73"},
		{name: "2% 手续费", gross: "73", feeBps: 200, wantG: "73", wantF: "1.46", wantN: "71.54"},
		{name: "不足一分的手续费按 6 位保留", gross: "7.3", feeBps: 1,
			wantG: "7.3", wantF: "0.00073", wantN: "7.29927"},
		{name: "账本给出的位数超过 6 位时先按存储精度收敛", gross: "10.0000004", feeBps: 0,
			wantG: "10", wantF: "0", wantN: "10"},
		{name: "手续费吃光全部金额时拒绝建单", gross: "73", feeBps: 10000,
			wantErrContains: errFeeEatsAll},
		{name: "账本折不出正金额", gross: "0", feeBps: 0, wantErrContains: errAmountOutOfRange},
		{name: "费率越界", gross: "73", feeBps: 10001, wantErrContains: errRateUnavailable},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := splitFiat(decimal.RequireFromString(tc.gross), tc.feeBps)
			if tc.wantErrContains != nil {
				assert.ErrorIs(t, err, tc.wantErrContains)
				return
			}
			require.NoError(t, err)
			assert.True(t, got.Gross.Equal(decimal.RequireFromString(tc.wantG)), "gross=%s", got.Gross)
			assert.True(t, got.Fee.Equal(decimal.RequireFromString(tc.wantF)), "fee=%s", got.Fee)
			assert.True(t, got.Net.Equal(decimal.RequireFromString(tc.wantN)), "net=%s", got.Net)
			assert.True(t, got.Gross.Equal(got.Fee.Add(got.Net)), "应付 == 手续费 + 实付")
		})
	}
}

// 冻结汇率是**反解出来的展示值**,不参与算钱:它必须由账本给出的金额反推,
// 而不是反过来拿一个汇率去算金额 —— 后者正是"账面 850、单据 100"的成因。
func TestImpliedFxRate(t *testing.T) {
	perUnit := decimal.NewFromInt(500000)

	cases := []struct {
		name  string
		gross string
		quota int64
		want  string
	}{
		{"分组档 8.5:50,000,000 额度折 850", "850", 50000000, "8.5"},
		{"全站汇率 1", "100", 50000000, "1"},
		{"部分提现的比例不变", "425", 25000000, "8.5"},
		{"额度为 0 时给不出汇率", "850", 0, "0"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := impliedFxRate(decimal.RequireFromString(tc.gross), tc.quota, perUnit)
			assert.True(t, got.Equal(decimal.RequireFromString(tc.want)), "rate=%s", got)
		})
	}

	t.Run("额度单位非法时给不出汇率", func(t *testing.T) {
		assert.True(t, impliedFxRate(decimal.NewFromInt(850), 50000000, decimal.Zero).IsZero())
	})
}

// 提现侧不再有自己的汇率:充值页汇率怎么改,都不该影响单据金额。
//
// 这条与 TestPriceFiatInTx_AmountComesFromLedgerNotRechargeRate 是一对 ——
// 这里守的是"计价函数根本不认识那个全局变量",那里守的是端到端的落库金额。
func TestSplitFiat_IgnoresLiveGlobalRate(t *testing.T) {
	original := operation_setting.USDExchangeRate
	t.Cleanup(func() { operation_setting.USDExchangeRate = original })

	operation_setting.USDExchangeRate = 1
	before, err := splitFiat(decimal.RequireFromString("850"), 0)
	require.NoError(t, err)

	operation_setting.USDExchangeRate = 99
	after, err := splitFiat(decimal.RequireFromString("850"), 0)
	require.NoError(t, err)

	assert.True(t, before.Gross.Equal(after.Gross))
	assert.True(t, before.Gross.Equal(decimal.RequireFromString("850")))
}

// 管理员把 QuotaPerUnit 改成 0 是完全可能的。放行的话冻结汇率会被 0 除,
// 只能编一个数字出来,所以必须拒绝建单。
func TestFrozenQuotaPerUnit_RejectsNonPositive(t *testing.T) {
	original := common.QuotaPerUnit
	t.Cleanup(func() { common.QuotaPerUnit = original })

	common.QuotaPerUnit = 500000
	perUnit, err := frozenQuotaPerUnit()
	require.NoError(t, err)
	assert.True(t, perUnit.Equal(decimal.NewFromInt(500000)))

	common.QuotaPerUnit = 0
	_, err = frozenQuotaPerUnit()
	assert.ErrorIs(t, err, errRateUnavailable)
}

func TestMinFiatAmount(t *testing.T) {
	cases := []struct {
		name, raw, want string
		wantErr         bool
	}{
		{name: "空串表示不设下限", raw: "  ", want: "0"},
		{name: "正常取值", raw: "100", want: "100"},
		{name: "非法取值拒绝建单", raw: "十块", wantErr: true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := minFiatAmount(tc.raw)
			if tc.wantErr {
				assert.ErrorIs(t, err, errRateUnavailable)
				return
			}
			require.NoError(t, err)
			assert.True(t, got.Equal(decimal.RequireFromString(tc.want)), "got=%s", got)
		})
	}
}
