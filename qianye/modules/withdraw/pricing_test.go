package withdraw

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/qianye/config"
	"github.com/QuantumNous/new-api/setting/operation_setting"

	"github.com/shopspring/decimal"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func loadWithdrawYAML(t *testing.T, body string) {
	t.Helper()
	yaml := "enabled: true\ndatabase:\n  dsn: \"u:p@tcp(127.0.0.1:3306)/qy\"\n" + body
	p := filepath.Join(t.TempDir(), "qianye.yaml")
	require.NoError(t, os.WriteFile(p, []byte(yaml), 0o600))
	t.Setenv(config.EnvConfigPath, p)
	require.NoError(t, config.Load())
}

func TestFreezeRates_FixedMode(t *testing.T) {
	loadWithdrawYAML(t, `withdraw:
  enabled: true
  methods: ["quota"]
  rate_freeze_mode: "fixed"
  rate_freeze_fixed: "6.85"
`)
	rates, err := freezeRates()
	require.NoError(t, err)
	assert.True(t, rates.FxRate.Equal(decimal.RequireFromString("6.85")))
	assert.True(t, rates.QuotaPerUnit.Equal(decimal.NewFromFloat(common.QuotaPerUnit)))
}

// operation_setting.USDExchangeRate 是管理员可以随时热改的全局变量。
// 冻结的意义就在这里:改了之后,新单跟着变,已经建好的单一分不动。
func TestFreezeRates_TracksOperationSetting(t *testing.T) {
	loadWithdrawYAML(t, `withdraw:
  enabled: true
  methods: ["quota"]
`)
	original := operation_setting.USDExchangeRate
	t.Cleanup(func() { operation_setting.USDExchangeRate = original })

	operation_setting.USDExchangeRate = 7.3
	first, err := freezeRates()
	require.NoError(t, err)

	operation_setting.USDExchangeRate = 9.9
	second, err := freezeRates()
	require.NoError(t, err)

	assert.True(t, first.FxRate.Equal(decimal.NewFromFloat(7.3)))
	assert.True(t, second.FxRate.Equal(decimal.NewFromFloat(9.9)))
}

// 管理员把汇率改成 0 是完全可能的。放行的话整单金额算成 0,
// 用户白白损失一笔佣金,所以必须拒绝建单而不是安静地算出 0。
func TestFreezeRates_RejectsNonPositiveRate(t *testing.T) {
	loadWithdrawYAML(t, `withdraw:
  enabled: true
  methods: ["quota"]
`)
	original := operation_setting.USDExchangeRate
	t.Cleanup(func() { operation_setting.USDExchangeRate = original })

	operation_setting.USDExchangeRate = 0
	_, err := freezeRates()
	assert.ErrorIs(t, err, errRateUnavailable)
}

// 已冻结的参数一旦传进来,换算结果就不该再受全局变量影响。
// 这是"历史对账永远对得上"的技术前提 —— 谁把 computeFiat 改成直接读全局,
// 这条测试就会红。
func TestComputeFiat_IgnoresLiveGlobalRate(t *testing.T) {
	original := operation_setting.USDExchangeRate
	t.Cleanup(func() { operation_setting.USDExchangeRate = original })

	frozen := frozenRates{
		QuotaPerUnit: decimal.NewFromInt(500000),
		FxRate:       decimal.RequireFromString("7.3"),
	}
	before, err := computeFiat(500000, frozen, 0)
	require.NoError(t, err)

	operation_setting.USDExchangeRate = 99
	after, err := computeFiat(500000, frozen, 0)
	require.NoError(t, err)

	assert.True(t, before.Gross.Equal(after.Gross))
	assert.True(t, before.Gross.Equal(decimal.RequireFromString("7.3")))
}

func TestComputeFiat(t *testing.T) {
	frozen := frozenRates{
		QuotaPerUnit: decimal.NewFromInt(500000),
		FxRate:       decimal.RequireFromString("7.3"),
	}
	cases := []struct {
		name            string
		quota           int64
		feeBps          int
		gross, fee, net string
	}{
		{"零手续费", 5000000, 0, "73", "0", "73"},
		{"2% 手续费", 5000000, 200, "73", "1.46", "71.54"},
		{"不足一分的手续费按 6 位保留", 500000, 1, "7.3", "0.00073", "7.29927"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := computeFiat(tc.quota, frozen, tc.feeBps)
			require.NoError(t, err)
			assert.True(t, got.Gross.Equal(decimal.RequireFromString(tc.gross)), "gross=%s", got.Gross)
			assert.True(t, got.Fee.Equal(decimal.RequireFromString(tc.fee)), "fee=%s", got.Fee)
			assert.True(t, got.Net.Equal(decimal.RequireFromString(tc.net)), "net=%s", got.Net)
			// 恒等式:三个字段必须永远加得上,否则财务无法复核。
			assert.True(t, got.Gross.Equal(got.Fee.Add(got.Net)))
		})
	}
}

func TestComputeFiat_RejectsFullFee(t *testing.T) {
	frozen := frozenRates{
		QuotaPerUnit: decimal.NewFromInt(500000),
		FxRate:       decimal.RequireFromString("7.3"),
	}
	// 手续费吃掉全部金额时必须拒绝建单,而不是落一张实付 0 的单据。
	_, err := computeFiat(500000, frozen, 10000)
	assert.ErrorIs(t, err, errFeeEatsAll)
}

func TestComputeFiat_RejectsInvalidInput(t *testing.T) {
	frozen := frozenRates{
		QuotaPerUnit: decimal.NewFromInt(500000),
		FxRate:       decimal.RequireFromString("7.3"),
	}
	_, err := computeFiat(0, frozen, 0)
	assert.ErrorIs(t, err, errAmountOutOfRange)

	_, err = computeFiat(500000, frozen, 10001)
	assert.ErrorIs(t, err, errRateUnavailable)
}
