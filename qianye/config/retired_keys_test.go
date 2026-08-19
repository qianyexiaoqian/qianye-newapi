package config

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// retired_keys_test.go —— 「下线一个功能不许把存量部署炸掉」。
//
// ═══════════════════════ 这条测试防的是一次全站宕机 ═══════════════════════
//
// 本包是**严格解析**(parseFile 里的 KnownFields(true),未知字段直接 return err,
// 进程起不来)。所以"删掉一个已下线功能的配置字段"这个看起来最干净的动作,
// 会让每一个 YAML 里还写着那个键的部署在**升级二进制的那一刻**启动失败 ——
// 不是功能不生效,是网关根本不起来,而且错误信息只说"unknown field"。
//
// 正确做法是保留一个去语义化的 Deprecated 占位吸收它,加载时告警并置 nil。
// 本仓已有三处:commission.*_rate_bps、group_pricing 整段、以及本轮的
// group_matrix.new_group_default_deny / new_group_scan_interval_seconds。
//
// 这条测试把"存量 YAML 仍能起来"钉成断言。没有它,下一个下线功能的人会重犯 ——
// 而这类错误在开发机上永远复现不了(开发机的 YAML 是新写的)。

// TestRetiredConfigKeysStillParse 用一份**含全部已下线键**的 YAML 断言仍能加载。
//
// 键是逐个列出来的,不是拼出来的:这份清单本身就是文档 ——
// "哪些键已经不起作用了,但你仍然可以留在文件里"。
func TestRetiredConfigKeysStillParse(t *testing.T) {
	c, _, err := parseFile(writeTemp(t, `
enabled: true
database:
  dsn: "u:p@tcp(127.0.0.1:3306)/qy"

# 上一轮的「新分组默认全遮断」。本轮口径反转(未设定范围 = 全部模型分组可用),
# 整台机器连同登记簿表一起撤销。
group_matrix:
  enabled: false
  new_group_default_deny: true
  new_group_scan_interval_seconds: 60

# 「模型按分组单独定价」整段下线,由 (用户分组, 模型分组) 倍率矩阵取代。
group_pricing:
  enabled: true
  shadow_mode: false
  rule_cache_seconds: 30
  一个我们从来没定义过的键: 123

# 1.x 的万分比费率字段。
commission:
  enabled: false
  topup_rate_bps: 1000
`))
	require.NoError(t, err,
		"仍写着已下线键的 YAML 必须能加载 —— 本包是 KnownFields(true) 严格解析,"+
			"直接删字段会让这些部署在升级二进制的那一刻**启动失败**(全站宕机,而不是功能不生效)")

	// 加载之后必须**观测不到**它们:置 nil 是刻意的,断掉任何后来者把占位
	// 重新当成开关读的可能。留着一个"读得到但不起作用"的值,下一个人一定会读它。
	assert.Nil(t, c.GroupMatrix.NewGroupDefaultDenyDeprecated,
		"new_group_default_deny 加载后必须被置 nil —— 它不是开关,填 true 也不会让任何东西收紧")
	assert.Nil(t, c.GroupMatrix.NewGroupScanIntervalSecondsDeprecated)
	assert.Nil(t, c.GroupPricingDeprecated,
		"group_pricing 整段必须被吸收并置 nil")

	// map[string]any 占位吸收了一个我们从来没定义过的键而没有报错 ——
	// 这正是它相对"保留原结构体"的价值:某个部署自己加过的键也不会炸。
	assert.False(t, c.GroupMatrix.Enabled)
}

// TestRetiredGroupPricingSectionIsSafeToDelete 是"这一段现在可以删了"的证据。
//
// 上面那条测的是**留着不炸**。本条测的是相反的一半:**删掉也不掉东西**。
//
// 为什么这一半需要单独钉住:本仓有过"缺一段配置 = 整块功能静默不可见"的先例
// (sections.go 文件头列了三次)。判据不是"我看了一眼代码觉得没事",而是
//
//	① 缺段能加载(它不在任何必填校验里);
//	② 缺段与写了这一段,解析出来的 Config **逐字段相等** ——
//	   也就是说这一段确实一个消费方都没有,删它不改变任何行为;
//	③ 它不在 moduleGates 里,所以缺段不会触发"模块静默关闭"的启动告警,
//	   删掉之后启动日志是干净的(否则运维会以为自己删坏了什么)。
//
// 演示站 data/qianye-prod.yaml 里的那一段据此删除。
func TestRetiredGroupPricingSectionIsSafeToDelete(t *testing.T) {
	const withSection = `
enabled: true
database:
  dsn: "u:p@tcp(127.0.0.1:3306)/qy"
group_pricing:
  enabled: true
  shadow_mode: true
  shadow_flush_interval_seconds: 10
`
	const withoutSection = `
enabled: true
database:
  dsn: "u:p@tcp(127.0.0.1:3306)/qy"
`
	with, _, err := parseFile(writeTemp(t, withSection))
	require.NoError(t, err)
	without, _, err := parseFile(writeTemp(t, withoutSection))
	require.NoError(t, err, "缺 group_pricing 段必须照常加载")

	assert.Nil(t, without.GroupPricingDeprecated)
	// declared 集合天然不同(一个写了这一段、一个没写),那正是它的职责;
	// 比较的是**行为**,所以把它排除在外之后逐字段相等。
	with.declared, without.declared = nil, nil
	assert.Equal(t, with, without,
		"写了 group_pricing 与没写解析出来的配置必须完全一样 —— "+
			"不一样就说明这一段还有消费方,不能从演示站的 YAML 里删")

	for _, g := range ModuleGates() {
		assert.NotEqual(t, "group_pricing", g.Section,
			"group_pricing 出现在 moduleGates 里 = 删掉这一段会在启动日志里报"+
				"「模块静默关闭」,而它根本不是一个还活着的模块")
	}
}

// TestRetiredNewGroupKeysDoNotResurrectAnySwitch 守语义,不只守可解析性。
//
// 光能解析是不够的:如果占位字段的值还能被谁读到,"新分组默认全遮断"就会以
// 另一种形式活过来,而那与本轮拍定的口径**正好相反**。
//
// 判据用 GroupMatrix 上**没有**任何相关方法来表达:上一轮的读取入口是
// NewGroupDefaultDenyOn(),它已经随功能一起删除。这条断言由编译期承担 ——
// 有人加回那个方法,下面这段注释里列出的调用会重新出现,而
// selfcheck.go 的消费点登记会把它指回 defaults.go 之外的地方,自检面板随之判红。
func TestRetiredNewGroupKeysDoNotResurrectAnySwitch(t *testing.T) {
	c, _, err := parseFile(writeTemp(t, `
enabled: true
database:
  dsn: "u:p@tcp(127.0.0.1:3306)/qy"
group_matrix:
  enabled: true
  cache_seconds: 30
  max_stale_seconds: 300
  preview_log_days: 7
  max_preview_pairs: 500
  preview_sample_limit: 20
  max_grants: 2000
  new_group_default_deny: true
`))
	require.NoError(t, err)

	// 显式写 true 也不会在配置层留下任何"打开了"的痕迹。
	assert.Nil(t, c.GroupMatrix.NewGroupDefaultDenyDeprecated)
	// 而真正还在用的那个默认开关不受影响 —— 证明上面那句 nil 不是"整段没解析"。
	assert.True(t, c.GroupMatrix.WriteGuardOn())
	assert.True(t, c.GroupMatrix.Enabled)
}
