package config

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// 法币金额与法币币种由两个互不校验的旋钮决定,这条闸门把它们绑在一起。
//
// 缺陷长相:rate_freeze_mode = operation_setting 时汇率取自站点的
// USDExchangeRate,而它在上游的定义是 USD → CNY(controller/billing.go 与
// logger/logger.go 都按 cny := usd * rate 用它)。把 fiat_currency 填成别的
// 币种,提现单上的数字与币种就不是同一件事 —— 数字看起来完全正常,只有线下
// 按单据币种打款的人会发现付错了(按 7.3 的汇率是七倍)。
//
// fixed 档不在闸门内:那一档的汇率是运维自己写死的一个数,折算成哪种钱由他决定。
func TestValidate_FiatCurrencyMustMatchRateSource(t *testing.T) {
	base := "enabled: true\ndatabase:\n  dsn: \"u:p@tcp(h:3306)/d\"\nwithdraw:\n"

	for _, tc := range []struct {
		name    string
		yaml    string
		wantErr bool
	}{
		{"默认值(operation_setting + CNY)自洽", "  enabled: true\n  methods: [\"quota\"]\n", false},
		{"operation_setting 配成 USD:金额是 CNY、标签是 USD", "  enabled: true\n  methods: [\"quota\"]\n" +
			"  rate_freeze_mode: \"operation_setting\"\n  fiat_currency: \"USD\"\n", true},
		{"operation_setting 配成 EUR 同理", "  enabled: true\n  methods: [\"quota\"]\n" +
			"  rate_freeze_mode: \"operation_setting\"\n  fiat_currency: \"EUR\"\n", true},
		{"大小写与空白不构成新币种", "  enabled: true\n  methods: [\"quota\"]\n" +
			"  rate_freeze_mode: \"operation_setting\"\n  fiat_currency: \" cny \"\n", false},
		{"fixed 档自带汇率,币种由运维决定", "  enabled: true\n  methods: [\"quota\"]\n" +
			"  rate_freeze_mode: \"fixed\"\n  rate_freeze_fixed: \"1\"\n  fiat_currency: \"USD\"\n", false},
		// 提现整个关掉也要校验:佣金页那句「折合法币」不看 withdraw.enabled。
		{"提现关闭时也要校验", "  enabled: false\n" +
			"  rate_freeze_mode: \"operation_setting\"\n  fiat_currency: \"USD\"\n", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, _, err := parseFile(writeTemp(t, base+tc.yaml))
			if !tc.wantErr {
				require.NoError(t, err)
				return
			}
			require.Error(t, err)
			assert.Contains(t, err.Error(), "fiat_currency")
			assert.Contains(t, err.Error(), "rate_freeze_mode",
				"报错必须给出补救动作,只说「对不上」运维猜不到该改哪一项")
		})
	}
}
