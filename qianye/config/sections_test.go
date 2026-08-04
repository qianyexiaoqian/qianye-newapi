package config

import (
	"os"
	"reflect"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// sectionState 取出某个模块在这份配置下的段状态,找不到就让测试直接失败。
func sectionState(t *testing.T, c *Config, module string) ModuleSection {
	t.Helper()
	for _, s := range moduleSections(c) {
		if s.Module == module {
			return s
		}
	}
	require.FailNowf(t, "模块未登记", "moduleGates 里没有 %q", module)
	return ModuleSection{}
}

// 缺整段必须被判成 missing_section —— 这是 lottery 那次事故的原始形状:
// 代码全都编译进去了,引导端点却下发 features.lottery=false,前端整个入口不渲染。
func TestMissingSectionIsReported(t *testing.T) {
	c, _, err := parseFile(writeTemp(t, minimalValid))
	require.NoError(t, err)

	s := sectionState(t, c, "lottery")
	assert.Equal(t, SectionStateMissingSection, s.State)
	assert.False(t, s.Enabled)
	assert.True(t, s.NeedsAttention())
	assert.Contains(t, s.Fix, "lottery:")
	assert.Contains(t, s.Fix, "enabled: true")
}

// 显式写 false 是运维的明确选择,一个字都不该报。
//
// 这条是整套机制的**成立条件**:告警若连"我知道,我就是要关掉它"都要念一遍,
// 第二周它就会变成没人再读的背景噪声,而下一次真正的静默失效照样看不见。
func TestExplicitFalseIsSilent(t *testing.T) {
	c, _, err := parseFile(writeTemp(t, `
enabled: true
database:
  dsn: "u:p@tcp(127.0.0.1:3306)/qy"
lottery:
  enabled: false
`))
	require.NoError(t, err)

	s := sectionState(t, c, "lottery")
	assert.Equal(t, SectionStateDeclared, s.State)
	assert.False(t, s.Enabled)
	assert.False(t, s.NeedsAttention())
	assert.Empty(t, s.Fix)
}

// 显式写 true 同样是 declared,且 Enabled 必须跟着走。
func TestExplicitTrueIsDeclaredAndEnabled(t *testing.T) {
	c, _, err := parseFile(writeTemp(t, `
enabled: true
database:
  dsn: "u:p@tcp(127.0.0.1:3306)/qy"
ticket:
  enabled: true
`))
	require.NoError(t, err)

	s := sectionState(t, c, "ticket")
	assert.Equal(t, SectionStateDeclared, s.State)
	assert.True(t, s.Enabled)
	assert.False(t, s.NeedsAttention())
}

// 段在、总开关那一行不在,是比整段缺失更隐蔽的一种:运维能在文件里看到
// `ticket:` 和一堆限额,于是理所当然地认为工单开着,而它整段不生效。
func TestSectionPresentButGateKeyMissing(t *testing.T) {
	c, _, err := parseFile(writeTemp(t, `
enabled: true
database:
  dsn: "u:p@tcp(127.0.0.1:3306)/qy"
ticket:
  daily_max_count: 10
`))
	require.NoError(t, err)

	s := sectionState(t, c, "ticket")
	assert.Equal(t, SectionStateMissingKey, s.State)
	assert.False(t, s.Enabled)
	assert.True(t, s.NeedsAttention())
}

// 告警给出的修复片段必须**粘回去还能解析**。
//
// 修复前两种 missing 状态给的是同一句"完整的顶层段",而 missing_key 的前提
// 恰恰是那一段已经在文件里了 —— 照着粘会产生重复的顶层键,parseFile 的
// KnownFields(true) 判成解析错误,Load 返回 error,整台网关起不来。
// 一条"不阻断启动"的告警,其修复指引反而把网关关停了。
func TestFixSnippetsPasteBackIntoAParsableFile(t *testing.T) {
	base := minimalValid

	// missing_section:片段是一整段,追加到文件末尾。
	c, _, err := parseFile(writeTemp(t, base))
	require.NoError(t, err)
	miss := sectionState(t, c, "lottery")
	require.Equal(t, SectionStateMissingSection, miss.State)

	patched, _, err := parseFile(writeTemp(t, base+"\n"+miss.Fix+"\n"))
	require.NoError(t, err, "追加 missing_section 的修复片段之后必须仍然能解析")
	after := sectionState(t, patched, "lottery")
	assert.Equal(t, SectionStateDeclared, after.State)
	assert.True(t, after.Enabled)

	// missing_key:段已经存在,片段只能是段内那一行。
	withSection := base + "ticket:\n  daily_max_count: 10\n"
	c2, _, err := parseFile(writeTemp(t, withSection))
	require.NoError(t, err)
	key := sectionState(t, c2, "ticket")
	require.Equal(t, SectionStateMissingKey, key.State)
	assert.NotContains(t, key.Fix, key.Section+":",
		"段已经存在时还给出带顶层键的整段,等于在教运维写出重复的顶层 YAML 键")

	patched2, _, err := parseFile(writeTemp(t, withSection+key.Fix+"\n"))
	require.NoError(t, err, "把片段补进已有的那一段里之后必须仍然能解析")
	after2 := sectionState(t, patched2, "ticket")
	assert.Equal(t, SectionStateDeclared, after2.State)
	assert.True(t, after2.Enabled)

	// 反向:把整段当成新的顶层段追加(修复前的告警教的正是这一手),
	// 配置文件从此解析失败。这一条钉住"不能再退回去"。
	_, _, err = parseFile(writeTemp(t, withSection+"\nticket:\n  enabled: true\n"))
	require.Error(t, err,
		"重复的顶层键必须是解析错误 —— 这正是 missing_key 的片段不能带顶层键的原因")
}

// 段内有二级开关时,整段的修复片段必须点名它们,而且粘回去仍然能解析。
//
// 只给 `violation:\n  enabled: true` 的话,运维粘完重启会看到违规检测"已启用"
// 却一条都抓不到 —— 那正是本轮事故 #2 的形状。片段里刻意只用 YAML 注释点名,
// 不替运维把 precheck_enabled 选成 true:打开它就是真的开始拦截线上请求。
func TestSectionFixSnippetNamesTheSecondarySwitches(t *testing.T) {
	c, _, err := parseFile(writeTemp(t, minimalValid))
	require.NoError(t, err)

	main := sectionState(t, c, "violation")
	require.Equal(t, SectionStateMissingSection, main.State)
	assert.Contains(t, main.Fix, "precheck_enabled")
	assert.Contains(t, main.Fix, "post_charge_enabled")

	patched, _, err := parseFile(writeTemp(t, minimalValid+"\n"+main.Fix+"\n"))
	require.NoError(t, err, "带 YAML 注释的修复片段粘回去必须仍然能解析")
	assert.Equal(t, SectionStateDeclared, sectionState(t, patched, "violation").State)

	// 粘完之后两个二级开关仍然是"没写",这时它们各自的告警才登场 ——
	// 分两步是刻意的:第一步没人替运维决定要不要真的开始拦截线上请求。
	for _, s := range moduleSections(patched) {
		if s.Module == "violation" && s.Key != "enabled" {
			assert.Equal(t, SectionStateMissingKey, s.State, "%s", s.Key)
			assert.True(t, s.NeedsAttention(), "%s", s.Key)
		}
	}
}

// 默认打开的开关(*bool)缺失不是缺陷,不该混进告警队列。
func TestDefaultOnGateIsNeverReported(t *testing.T) {
	c, _, err := parseFile(writeTemp(t, minimalValid))
	require.NoError(t, err)

	s := sectionState(t, c, "groupvis")
	assert.Equal(t, SectionStateDefaultOn, s.State)
	assert.True(t, s.Enabled, "group_visibility.enabled 是 *bool,未写时应为 true")
	assert.False(t, s.NeedsAttention())
}

// 没有配置开关的模块永远是 ungated,不因配置文件内容变化。
//
// Enabled 必须跟着扩展总开关走,而不是留在零值 false:这五行(含 logmetrics)
// 全都是扩展一开就在跑的模块,把它们显示成「当前生效:否」会让排障的人去找一个
// 根本不存在的开关 —— 面板说反话比面板少一行更糟。
func TestUngatedModulesAreNeverReported(t *testing.T) {
	c, _, err := parseFile(writeTemp(t, minimalValid))
	require.NoError(t, err)

	for _, name := range []string{"apiaddr", "paypass", "subscription", "usergroup"} {
		s := sectionState(t, c, name)
		assert.Equal(t, SectionStateUngated, s.State, "%s", name)
		assert.False(t, s.NeedsAttention(), "%s", name)
		assert.True(t, s.Enabled,
			"%s 随扩展总开关生效,扩展已启用时不能显示成「当前生效:否」", name)
	}
}

// 扩展整体关掉时,ungated 行必须跟着变成"不生效" —— 否则这一列就不是在
// 报告状态,只是在报告"这一行有没有开关"。
func TestUngatedModulesFollowTheExtensionSwitch(t *testing.T) {
	c, _, err := parseFile(writeTemp(t, `
enabled: false
database:
  dsn: "u:p@tcp(127.0.0.1:3306)/qy"
`))
	require.NoError(t, err)

	s := sectionState(t, c, "apiaddr")
	assert.Equal(t, SectionStateUngated, s.State)
	assert.False(t, s.Enabled)
}

// logmetrics 没有总开关,它的实际取值是段内两个展示开关的逻辑或 ——
// 与 guard.featureOn(FlagLogMetrics) 是同一个判据。
//
// 两个都显式关掉时模块确实什么都不做,面板必须跟着说"否";默认(两行都不写)
// 时它是开着的,面板必须说"是"。这一行曾经恒为"否",与真实取值直接相反。
func TestLogMetricsEnabledMatchesItsDisplaySwitches(t *testing.T) {
	on, _, err := parseFile(writeTemp(t, minimalValid))
	require.NoError(t, err)
	assert.True(t, sectionState(t, on, "logmetrics").Enabled,
		"两个展示开关都是 *bool 默认打开,不写这一段时模块是生效的")

	off, _, err := parseFile(writeTemp(t, `
enabled: true
database:
  dsn: "u:p@tcp(127.0.0.1:3306)/qy"
log_metrics:
  show_reasoning_effort: false
  show_cache_ratio: false
`))
	require.NoError(t, err)
	assert.False(t, sectionState(t, off, "logmetrics").Enabled,
		"两个展示开关都被显式关掉时,模块不再有任何效果")
}

// 二级开关各占一行,状态各算各的。
//
// 这是 violation 的真实形态:运维照着告警补了 `enabled: true`,两个决定
// "抓不抓"的开关一个字没写 —— 修复前健康面板只有 violation 一行,显示成
// 「已显式配置 + 当前生效=是」,而两个挂载点全都直接 return。
func TestSecondarySwitchesAreReportedOnTheirOwnRow(t *testing.T) {
	c, _, err := parseFile(writeTemp(t, `
enabled: true
database:
  dsn: "u:p@tcp(127.0.0.1:3306)/qy"
violation:
  enabled: true
`))
	require.NoError(t, err)

	rows := make(map[string]ModuleSection, 3)
	for _, s := range moduleSections(c) {
		if s.Module == "violation" {
			rows[s.Key] = s
		}
	}
	require.Len(t, rows, 3, "violation 应有总开关 + 两个二级开关共三行")

	assert.Equal(t, SectionStateDeclared, rows["enabled"].State)
	assert.True(t, rows["enabled"].Enabled)

	for _, key := range []string{"precheck_enabled", "post_charge_enabled"} {
		s := rows[key]
		assert.Equal(t, SectionStateMissingKey, s.State, "%s", key)
		assert.False(t, s.Enabled, "%s", key)
		assert.True(t, s.NeedsAttention(),
			"%s 没写就是两个挂载点空转,必须和总开关缺失一样告警", key)
	}
}

// Declared 认的是"写没写",不是"写的是什么"。这是本机制唯一的判据,
// 一旦它开始受取值影响,`enabled: false` 就会重新变得无法与"没写"区分。
func TestDeclaredIgnoresValue(t *testing.T) {
	c, _, err := parseFile(writeTemp(t, `
enabled: true
database:
  dsn: "u:p@tcp(127.0.0.1:3306)/qy"
transfer:
  enabled: false
  fee_bps: 0
`))
	require.NoError(t, err)

	assert.True(t, c.Declared("transfer"))
	assert.True(t, c.Declared("transfer.enabled"))
	assert.True(t, c.Declared("transfer.fee_bps"), "显式写的 0 也是「写过」")
	assert.False(t, c.Declared("transfer.daily_max_count"))
	assert.False(t, c.Declared("lottery"))
	assert.False(t, c.Declared("lottery.enabled"))
}

// 零值 Config(还没加载过配置)不能因为 declared 是 nil 就 panic ——
// 健康端点在扩展未启用时也可能走到这里。
func TestDeclaredOnZeroConfig(t *testing.T) {
	var c Config
	assert.False(t, c.Declared("lottery.enabled"))
	assert.Len(t, moduleSections(&c), len(gatedSwitches()))
}

// ─────────────────────── 登记表与 Config 的结构性对账 ───────────────────────

// gatedSwitches 把登记表摊平成"一个开关一条",总开关与二级开关一视同仁。
// 下面每一条结构性对账都必须走这里,否则二级开关会整批逃过校验 ——
// 而"逃过校验的那一档最后就是静默失效的那一档"是本文件的全部主题。
func gatedSwitches() []ModuleGate {
	out := make([]ModuleGate, 0, len(moduleGates)*2)
	for _, g := range moduleGates {
		out = append(out, g)
		for _, s := range g.Extra {
			out = append(out, ModuleGate{
				Module: g.Module, Section: g.Section, Key: s.Key,
				DefaultOn: s.DefaultOn, Effect: s.Effect,
			})
		}
	}
	return out
}

// 登记表里写的 section.key 必须在 Config 上真的存在,而且真的是一个布尔开关。
//
// 没有这一条,把 `group_pricing` 手滑写成 `grouppricing` 的后果是该模块
// 永远处于 declared(因为那个路径永远查不到,也就永远"没写")—— 告警静默失效,
// 与它要防的缺陷是同一种形状。
func TestModuleGatePathsExistOnConfig(t *testing.T) {
	cfg := reflect.ValueOf(&Config{}).Elem()
	for _, g := range gatedSwitches() {
		if g.Section == "" || g.Key == "" {
			assert.Empty(t, g.Key,
				"%s 登记了 key=%q 却没有 section,这条登记不完整", g.Module, g.Key)
			continue
		}
		_, ok := gateField(cfg, g.Section, g.Key)
		assert.True(t, ok,
			"moduleGates 里 %s 的开关路径 %s.%s 在 Config 上不存在(或不是 bool/*bool)—— "+
				"这条登记正在白白放行一个模块", g.Module, g.Section, g.Key)
	}
}

// DefaultOn 必须与 Go 类型一致:普通 bool 的零值只能是 false,
// 只有 *bool 才可能"没写时为 true"。
//
// 这条把登记表钉在类型系统上。有人哪天把 Lottery.Enabled 从 bool 改成 *bool
// 并让它默认打开,却忘了改 DefaultOn,这里会红;反过来,有人给某个普通 bool
// 标上 DefaultOn: true(等于宣称"缺这一段没关系"),这里同样会红 ——
// 而那正是把告警关掉的最省事的方式。
func TestModuleGateDefaultOnMatchesFieldType(t *testing.T) {
	cfg := reflect.ValueOf(&Config{}).Elem()
	for _, g := range gatedSwitches() {
		if !g.DefaultOn {
			continue
		}
		f, ok := gateField(cfg, g.Section, g.Key)
		require.True(t, ok, "%s: %s.%s 不存在", g.Module, g.Section, g.Key)
		assert.Equal(t, reflect.Ptr, f.Kind(),
			"%s 声称 %s.%s 默认打开,但它是普通 bool —— 普通 bool 的零值只能是 false,"+
				"这条 DefaultOn 是在把一个真实的静默失效标成「安全」",
			g.Module, g.Section, g.Key)
	}
}

// 登记表里不许有重名模块:重名时 sectionState 之类的按名查找会随机命中一条,
// 而另一条的告警从此永远不会出现。
func TestModuleGatesHaveUniqueModuleNames(t *testing.T) {
	seen := make(map[string]bool, len(moduleGates))
	for _, g := range moduleGates {
		assert.False(t, seen[g.Module], "模块 %s 在 moduleGates 里出现了两次", g.Module)
		seen[g.Module] = true
	}
}

// 每条登记都必须写清楚"缺了会怎样"。Effect 是告警正文里唯一能让人直接行动的
// 一句,空着的话告警就退化成"某模块未启用",与它要取代的沉默没有区别。
func TestModuleGatesAllExplainTheEffect(t *testing.T) {
	for _, g := range gatedSwitches() {
		assert.NotEmpty(t, g.Effect, "模块 %s(开关 %q)的登记没有写 Effect", g.Module, g.Key)
	}
}

// TestEveryPlainBoolSwitchIsGated 是这套机制的**反向对账**,也是唯一能挡住
// "把一个真有开关的模块登记成没有开关"的一条。
//
// 在它之前,所有结构性守卫问的都是"模块名在不在登记表里",而
// TestModuleGatePathsExistOnConfig 与 TestExampleYAMLDeclaresEveryGatedSection
// 都以 `Section == "" || Key == ""` 为跳过条件 —— 于是把 lottery 那一行的
// Section 清空(含义:"这个模块没有开关"),告警立刻消失、面板显示 ungated、
// 全套测试照样全绿。那正是看到测试报错之后最省事的一行"修法",也正是本轮
// 要消灭的那个形状。
//
// 这条从 Config 这一侧问:凡是**普通 bool**(零值只能是 false,缺失即静默失效)
// 且键名带 enabled 的段内开关,都必须被某条登记覆盖。*bool 不在此列 ——
// 它们能表达"没写",缺失时走各自的默认值,不构成本文件要防的歧义。
func TestEveryPlainBoolSwitchIsGated(t *testing.T) {
	covered := make(map[string]bool, len(moduleGates)*2)
	for _, g := range gatedSwitches() {
		if g.Section != "" && g.Key != "" {
			covered[g.Section+"."+g.Key] = true
		}
	}

	cfgType := reflect.TypeOf(Config{})
	var ungoverned []string
	for i := 0; i < cfgType.NumField(); i++ {
		sec := cfgType.Field(i)
		name := strings.Split(sec.Tag.Get("yaml"), ",")[0]
		if name == "" || name == "-" || sec.Type.Kind() != reflect.Struct {
			continue
		}
		for j := 0; j < sec.Type.NumField(); j++ {
			f := sec.Type.Field(j)
			key := strings.Split(f.Tag.Get("yaml"), ",")[0]
			if f.Type.Kind() != reflect.Bool || !strings.Contains(key, "enabled") {
				continue
			}
			if !covered[name+"."+key] {
				ungoverned = append(ungoverned, name+"."+key)
			}
		}
	}

	assert.Empty(t, ungoverned,
		"以下开关是普通 bool(零值 false,配置里少这一行 = 静默关闭且无信号),"+
			"但没有出现在 moduleGates 的任何一条 Section+Key 上:%v。"+
			"它们既不会触发启动告警,也不会出现在健康面板上 —— 请把它们登记进去"+
			"(模块总开关用 Key,段内的二级开关用 Extra),而不是把登记降级成「没有开关」",
		ungoverned)
}

// 示例配置必须把每个需要告警的模块段都写全。
//
// 它是运维手里的样板文件:样板自己少一段,等于把这次要修的缺陷预装进每一个
// 照着抄的部署,而且抄的人不会怀疑样板。
func TestExampleYAMLDeclaresEveryGatedSection(t *testing.T) {
	raw, err := os.ReadFile("qianye.example.yaml")
	require.NoError(t, err)
	declared := declaredPaths(raw)
	require.NotEmpty(t, declared, "示例配置解析不出任何键,declaredPaths 必然写错了")

	for _, g := range gatedSwitches() {
		if g.Section == "" || g.Key == "" || g.DefaultOn {
			continue
		}
		assert.True(t, declared[g.Section+"."+g.Key],
			"qianye.example.yaml 里没有 %s.%s —— 照着示例改配置的部署会带着一个"+
				"静默关闭的 %s 模块上线", g.Section, g.Key, g.Module)
	}
}
