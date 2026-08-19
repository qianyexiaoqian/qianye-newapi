package config

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// fiat_currency_consistency_test.go —— 提现侧那套独立汇率下线之后,
// 「金额从哪来」与「币种标什么」这两件事各自还剩下什么约束。
//
// 曾经的缺陷:提现单的金额由 withdraw.rate_freeze_mode 选出的汇率现算
// (充值页汇率或一个写死的固定值),而佣金账本 available_fiat 是按计佣当刻的
// 三层折算比例攒的绝对值 —— 两个数各算各的。实测:账本冻走 850 CNY,
// 单据只让运营付 100 CNY。金额口径已统一到账本(commission.QuoteWithdrawFiat),
// 那两个键随之下线。
//
// 于是这个文件要钉的只剩两条:
//
//	① 存量 YAML 里还写着那两个键时,站点仍然起得来(严格解析 + Deprecated 占位),
//	   而且加载之后观测不到它们 —— 断掉"读得到就还能当汇率源用"的可能;
//	② 币种不再是启动期硬闸(配没配折算档写在扩展库里,配置期读不到),
//	   非 CNY 只告警不拒绝,而合法配置一律不许因此起不来。
func TestRetiredRateFreezeKeysStillParseAndAreInvisible(t *testing.T) {
	c, _, err := parseFile(writeTemp(t, `
enabled: true
database:
  dsn: "u:p@tcp(h:3306)/d"
withdraw:
  enabled: true
  methods: ["quota"]
  rate_freeze_mode: "fixed"
  rate_freeze_fixed: "7.3"
`))
	require.NoError(t, err,
		"仍写着 rate_freeze_* 的 YAML 必须能加载 —— 本包是 KnownFields(true) 严格解析,"+
			"直接删字段会让这些部署在升级二进制的那一刻启动失败(全站宕机,而不是功能不生效)")

	assert.Nil(t, c.Withdraw.RateFreezeModeDeprecated,
		"加载后必须置 nil:留着一个读得到的值,下一个人一定会把它重新当成汇率源")
	assert.Nil(t, c.Withdraw.RateFreezeFixedDeprecated)
}

// 币种:非 CNY 只是告警,不再拒绝启动。
//
// 理由写在 validateWithdraw 里 —— 站点有没有配佣金法币折算档(分组档 / 兜底档)
// 记在扩展库的 qy_commission_fiat_rate 与 qy_settings 上,运营在管理端随时可增删,
// 配置加载这一刻根本读不到。硬拒绝会把"配了 8.5 结汇比例、币种标 USD"这种
// 完全合法的部署挡在启动之外。运行期的判定在 commission.resolveFiatRate:
// 真回落到全站充值汇率且币种不是 CNY 时,按降级上报。
func TestValidate_NonCnyFiatCurrencyIsWarnedNotFatal(t *testing.T) {
	base := "enabled: true\ndatabase:\n  dsn: \"u:p@tcp(h:3306)/d\"\nwithdraw:\n"

	for _, tc := range []struct {
		name string
		yaml string
	}{
		{"默认(CNY)", "  enabled: true\n  methods: [\"quota\"]\n"},
		{"大小写与空白不构成新币种", "  enabled: true\n  methods: [\"quota\"]\n  fiat_currency: \" cny \"\n"},
		{"配了折算档的站点可以标 USD", "  enabled: true\n  methods: [\"quota\"]\n  fiat_currency: \"USD\"\n"},
		{"提现关闭时同样不拒绝", "  enabled: false\n  fiat_currency: \"EUR\"\n"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, _, err := parseFile(writeTemp(t, base+tc.yaml))
			require.NoError(t, err, "币种标签不该成为站点起不来的理由")
		})
	}
}
