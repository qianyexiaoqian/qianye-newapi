package config

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestRatePercentUnits 锁定"对外百分比 ↔ 内部整数"的换算口径。
//
// 这是本次改动的地基:百分比是给人看的,整数是给钱算的。换算一旦有偏差,
// 偏差会原样乘进每一笔佣金,而且因为费率被冻结进 accrual 行,错的费率
// 会连着历史数据一起固化下来。
func TestRatePercentUnits(t *testing.T) {
	cases := []struct {
		raw   string
		units int
	}{
		{"0", 0},         // 边界:关掉返佣是合法配置
		{"100", 10000},   // 边界:全额返佣
		{"10", 1000},     // 整数百分比
		{"10.5", 1050},   // 一位小数
		{"10.25", 1025},  // 两位小数,本次需求的核心场景
		{"0.01", 1},      // 最小可表达的非零费率
		{"10.250", 1025}, // 补零不改变取值
		{" 7.5 ", 750},   // 两侧空白容忍
		{"99.99", 9999},
	}
	for _, tc := range cases {
		got, err := RatePercentUnits(tc.raw)
		require.NoError(t, err, "raw=%q", tc.raw)
		assert.Equal(t, tc.units, got, "raw=%q", tc.raw)
	}
}

// 非法输入必须报错,不能悄悄猜一个值 —— 猜错的方向可能是多发钱。
func TestRatePercentUnitsRejectsBadInput(t *testing.T) {
	cases := []struct {
		name    string
		raw     string
		wantErr string
	}{
		{"负数", "-1", "不得为负数"},
		{"负的小数", "-0.01", "不得为负数"},
		{"超过 100%", "100.01", "不得超过"},
		{"远超上限", "20000", "不得超过"},
		// 三位小数不四舍五入而是拒绝:静默把 10.005 变成 10.01 是一次
		// 没有人签字的加薪,而账面上看不出任何异常。
		{"超过两位小数", "10.005", "最多两位小数"},
		{"更细的小数", "0.001", "最多两位小数"},
		{"空串", "", "不能为空"},
		{"只有空白", "   ", "不能为空"},
		{"不是数字", "abc", "不是合法数值"},
		{"带百分号", "10%", "不是合法数值"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := RatePercentUnits(tc.raw)
			require.Error(t, err)
			assert.Contains(t, err.Error(), tc.wantErr)
		})
	}
}

// 往返转换必须无损:管理端回显的百分比再存一次,不能漂移。
func TestRatePercentRoundTrip(t *testing.T) {
	for units := 0; units <= MaxRateUnits; units++ {
		got, err := RatePercentUnits(FormatRatePercent(units))
		require.NoError(t, err, "units=%d 格式化后应当能解析回来", units)
		require.Equal(t, units, got, "units=%d 往返后漂移", units)
	}
	assert.Equal(t, "10.25", FormatRatePercent(1025))
	assert.Equal(t, "10.5", FormatRatePercent(1050))
	assert.Equal(t, "10", FormatRatePercent(1000))
	assert.Equal(t, "0", FormatRatePercent(0))
	assert.Equal(t, "100", FormatRatePercent(MaxRateUnits))
}

// 旧的万分比字段必须能无损换算成百分比字符串,否则兼容期反而制造资损。
func TestBpsToPercentIsLossless(t *testing.T) {
	for bps := 0; bps <= maxBps; bps++ {
		units, err := RatePercentUnits(bpsToPercent(bps))
		require.NoError(t, err, "bps=%d", bps)
		require.Equal(t, bps, units, "bps=%d 换算成 %q 后取值变了", bps, bpsToPercent(bps))
	}
	assert.Equal(t, "10", bpsToPercent(1000))
	assert.Equal(t, "5", bpsToPercent(500))
	assert.Equal(t, "10.25", bpsToPercent(1025))
	assert.Equal(t, "10.5", bpsToPercent(1050))
}

// 只写旧字段时必须照常生效(带告警),否则升级即掉费率。
func TestParseFile_DeprecatedBpsStillWorks(t *testing.T) {
	c, _, err := parseFile(writeTemp(t, `
enabled: true
database:
  dsn: "u:p@tcp(h:3306)/d"
commission:
  enabled: true
  topup_rate_bps: 1025
  consume_rate_bps: 250
`))
	require.NoError(t, err)
	assert.Equal(t, "10.25", c.Commission.TopupRatePercent)
	assert.Equal(t, "2.5", c.Commission.ConsumeRatePercent)
}

// 新旧字段同时存在且矛盾时必须拒绝启动:替运维挑一个生效值,
// 正是本项目反复吃过亏的"以为改了其实没改"。
func TestParseFile_DeprecatedBpsConflictIsFatal(t *testing.T) {
	_, _, err := parseFile(writeTemp(t, `
enabled: true
database:
  dsn: "u:p@tcp(h:3306)/d"
commission:
  enabled: true
  topup_rate_percent: "10"
  topup_rate_bps: 500
`))
	require.Error(t, err)
	assert.Contains(t, err.Error(), "互相矛盾")

	// 一致时放行 —— 迁移过程中两处都写着同一个值是正常的中间态。
	_, _, err = parseFile(writeTemp(t, `
enabled: true
database:
  dsn: "u:p@tcp(h:3306)/d"
commission:
  enabled: true
  topup_rate_percent: "5"
  topup_rate_bps: 500
`))
	require.NoError(t, err)
}

// 百分比字段自身的非法取值必须点名新字段。
func TestParseFile_RatePercentRange(t *testing.T) {
	_, _, err := parseFile(writeTemp(t, `
enabled: true
database:
  dsn: "u:p@tcp(h:3306)/d"
commission:
  enabled: true
  consume_rate_percent: "150"
`))
	require.Error(t, err)
	assert.Contains(t, err.Error(), "commission.consume_rate_percent")
}

// 未配置时补默认值,且默认值本身必须可解析。
func TestParseFile_RatePercentDefaults(t *testing.T) {
	c, _, err := parseFile(writeTemp(t, minimalValid))
	require.NoError(t, err)
	assert.Equal(t, defaultTopupRatePercent, c.Commission.TopupRatePercent)
	assert.Equal(t, defaultConsumeRatePercent, c.Commission.ConsumeRatePercent)

	units, err := RatePercentUnits(c.Commission.TopupRatePercent)
	require.NoError(t, err)
	assert.Equal(t, 1000, units)
}
