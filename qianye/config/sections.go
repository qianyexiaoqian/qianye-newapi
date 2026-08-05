package config

// sections.go —— "模块编译进来了,配置文件里却没有它那一段" 的启动期防线。
//
// # 为什么需要这个文件
//
// 同一个失败形状在这个仓库里已经出现三次,每次都是几个小时的排障:
//
//	group_pricing 段缺失   5 个计价/结算挂载点全部空转,而账目看起来完全正常
//	violation 内置规则包    破限检测在生产零命中,单体测试却 25/31 全绿
//	lottery 段缺失         引导端点下发 features.lottery=false,前端整个入口不渲染
//
// 共同的根不是"默认值选错了",而是**无信号**:模块级的 Enabled 是普通 bool,
// 零值就是 false。于是"配置文件里根本没有这一段"与"运维读过文档、想清楚了、
// 显式写了 enabled: false"这两件事,在进程内是**同一个字节**。运维看不出来、
// 评审看不出来、测试也看不出来 —— 因为被测的那份配置是测试自己造的。
//
// # 为什么不是把默认值改成 true
//
// 那会让一个刚装好的站点在管理员还没读过文档时就把抽奖、工单、违规扣费全部
// 打开。默认打开一个会动用户余额的功能,比默认关闭严重得多。要修的是无信号,
// 不是默认值。
//
// # 怎么区分"没写"和"写了 false"
//
// 与 defaults.go 的 markNumbersUnset 同一个思路,只是把粒度从字段抬到段:
// 数值字段靠解析前打哨兵、解析后看是否还是哨兵;布尔开关没法打哨兵
// (true/false 之外没有第三个值),但 YAML 文本本身知道答案 —— 解析成功之后
// 再把同一份字节按 yaml.Node 走一遍,就能得到"这个文件到底写出了哪些键"。
// 这份集合存在 Config.declared 里,随快照一起被热载替换。
//
// 得到判据之后,处置分成三档,由下面的 moduleGates 逐模块登记:
//
//	段缺失 / 开关键缺失,且缺失即静默失效  →  SysError 显式告警(不阻断启动)
//	开关键缺失,但该开关默认打开            →  一个字都不报
//	显式写了(true 或 false 都算)          →  一个字都不报
//
// 最后一档是本文件存在的**全部意义**:运维显式写下 enabled: false 之后,
// 启动日志必须干干净净,否则这条告警会在第二周变成背景噪声。
//
// # 新增一个模块时要做什么
//
// 在 moduleGates 里加一行。忘了加会被 qianye/module_section_guard_test.go
// 直接判红 —— 那条测试拿 module.All() 与本表逐个对账,理由与
// qianye/modules_test.go 守 modules.go 的 blank import 完全一样。

import (
	"fmt"
	"reflect"
	"sort"
	"strings"

	"github.com/QuantumNous/new-api/common"

	"gopkg.in/yaml.v3"
)

// 段状态。同时是 /admin/health 下发给前端的取值,改动即接口变更。
const (
	// SectionStateDeclared 开关键被显式写出来了(true 或 false 都算)。
	// 这是运维做过决定的证据,不告警。
	SectionStateDeclared = "declared"
	// SectionStateMissingSection 顶层压根没有这一段。模块静默关闭。
	SectionStateMissingSection = "missing_section"
	// SectionStateMissingKey 段写了,但段内没有总开关那一行。同样是静默关闭 ——
	// 而且更隐蔽:运维明明能在文件里看到这一段,只是它整段不生效。
	SectionStateMissingKey = "missing_key"
	// SectionStateDefaultOn 开关键没写,但它是 *bool 且默认打开,缺失不会
	// 造成静默失效。不告警。
	SectionStateDefaultOn = "default_on"
	// SectionStateUngated 该模块没有配置开关,随扩展总开关一起生效。不告警。
	SectionStateUngated = "ungated"
)

// ModuleGate 登记"某个模块在配置文件里的开关长什么样"。
type ModuleGate struct {
	// Module 必须与该模块 module.Module.Name() 的返回值逐字一致。
	Module string
	// Section 是顶层 yaml 段名。空串表示该模块没有配置段。
	Section string
	// Key 是段内决定模块是否生效的开关键。空串表示该段没有总开关。
	Key string
	// DefaultOn 是"这一行没写时"开关的实际取值。
	//
	// 它不是随便填的一句描述,而是与 Go 类型绑定的事实:普通 bool 的零值只能是
	// false,只有 *bool 才可能默认为 true。config 包的
	// TestModuleGateDefaultOnMatchesFieldType 会去 Config 上核对这一点。
	DefaultOn bool
	// Effect 是"这一段缺失时运维会看到什么现象"。它是告警正文里最有用的一句,
	// 直接写用户可观察到的症状,不要写"模块未启用"这种同义反复。
	Effect string
	// Extra 登记同一段内**还必须打开、否则挂载点照样空转**的二级开关。
	//
	// 存在的理由与整个文件一样,只是下沉了一层:violation 打开 enabled 之后,
	// 真正决定"抓不抓"的是 precheck_enabled / post_charge_enabled,而它们同样是
	// 零值 false 的普通 bool。只覆盖模块级总开关的话,一个两个挂载点全空转的
	// violation 会在健康面板上显示成「已显式配置 + 当前生效=是」—— 与它要消灭的
	// 那个缺陷是同一种形状,只是骗过了更多人。
	//
	// 每个二级开关在 /admin/health 与启动告警里各占**独立的一行**,状态与总开关
	// 各算各的:段里写了 enabled 却漏了 precheck_enabled,前者 declared、后者
	// missing_key,后者照样告警。
	Extra []GateSwitch
}

// GateSwitch 是段内的二级开关。字段语义与 ModuleGate 的同名字段逐字一致。
type GateSwitch struct {
	Key       string
	DefaultOn bool
	Effect    string
}

// moduleGates 是模块 → 配置开关的登记表。
//
// 每一个注册进 qianye/module 的模块都必须在这里出现一行,包括那些压根没有
// 配置段的模块 —— "这个模块没有开关"是一条需要被写下来并被测试守住的事实,
// 而不是一个可以靠"表里查不到"推断出来的空白。两者的区别在于:前者是决定,
// 后者是遗漏,而本文件存在的理由正是这两者长得一样。
var moduleGates = []ModuleGate{
	// ── 缺这一段 = 模块静默关闭,启动时必须告警 ──────────────────────────
	{
		Module: "transfer", Section: "transfer", Key: "enabled",
		Effect: "全部划转接口返回 qy_feature_off,钱包页不渲染划转入口,用户只会觉得「功能没上线」",
	},
	{
		Module: "commission", Section: "commission", Key: "enabled",
		Effect: "消费与充值都不再计提佣金,邀请页恒为 0 —— 这段时间产生的佣金没有任何补算路径",
	},
	{
		Module: "withdraw", Section: "withdraw", Key: "enabled",
		Effect: "提现接口一律 qy_feature_off,已冻结的佣金取不出来,而佣金仍在继续冻结",
	},
	{
		Module: "ticket", Section: "ticket", Key: "enabled",
		Effect: "工单入口不渲染,用户端与管理端接口一律 qy_feature_off,用户没有任何反馈通道",
	},
	{
		Module: "availability", Section: "availability", Key: "enabled",
		Effect: "可用率采样一条都不写,监控面板恒空,而空曲线与「全都正常」在图上分不出来",
	},
	{
		Module: "violation", Section: "violation", Key: "enabled",
		Effect: "违规检测的两个挂载点完全空转,规则表里配多少条都是零命中",
		// 这两行不是补充说明,它们与 enabled 一样是"抓不抓"的必要条件:
		// guard.go 的两个挂载点各自判 `!cfg.Enabled || !cfg.XxxEnabled` 就直接
		// return。只写 enabled: true 的站点,规则表配满了也是零命中。
		Extra: []GateSwitch{
			{
				Key: "precheck_enabled",
				Effect: "转发前的提示词检查一次都不跑:enforce 规则不拦截、影子规则也不记录命中," +
					"规则列表和试跑都完全正常,唯一的表现是线上零命中",
			},
			{
				Key: "post_charge_enabled",
				Effect: "计费后扫描一次都不跑:违规不扣费、不计违规次数、不累计封号," +
					"接口照常 200,唯一的表现是线上零命中",
			},
		},
	},
	{
		Module: "grouppricing", Section: "group_pricing", Key: "enabled",
		Effect: "5 个计价/结算挂载点恒等返回,分组级价格规则形同虚设,全部请求按全局价扣费",
	},
	{
		Module: "groupmatrix", Section: "group_matrix", Key: "enabled",
		Effect: "权威可选清单完全不生效,所有用户分组仍按上游「全局白名单 + 特殊规则 + 无条件补自己」" +
			"一视同仁,令牌写入校验也不生效 —— 而管理端矩阵看起来配得好好的、列表页完全正常",
		Extra: []GateSwitch{
			{
				Key: "write_guard_enabled", DefaultOn: true,
				Effect: "令牌写入侧不再校验分组可选性。*bool 且默认打开,不写不会静默失效",
			},
			{
				Key: "new_group_default_deny", DefaultOn: true,
				Effect: "新出现的用户分组不再被自动全遮断,与上游行为一致。*bool 且默认打开," +
					"不写不会静默失效 —— 但它默认打开的方向是**收紧**,所以矩阵页常驻提示它当前的状态",
			},
		},
	},
	{
		Module: "lottery", Section: "lottery", Key: "enabled",
		Effect: "引导端点下发 features.lottery=false,前端整个娱乐入口不渲染,用户端与创建接口 404",
	},

	// ── 有配置段,但缺失不会造成静默失效 ────────────────────────────────
	{
		Module: "groupvis", Section: "group_visibility", Key: "enabled", DefaultOn: true,
		Effect: "无权分组裁剪。开关是 *bool 且默认打开:不写这一段时裁剪照常生效,不需要告警",
	},
	{
		Module: "logmetrics", Section: "log_metrics",
		Effect: "日志增强列。段内没有总开关,两个展示开关都是 *bool 且默认打开,缺段不会静默失效",
		// 登记这两行不是为了告警(它们默认打开,永远不会 needs_attention),
		// 而是为了让"当前生效"这一列说得出真话:本模块的实际取值是这两个开关的
		// 逻辑或(与 guard.featureOn(FlagLogMetrics) 同一个判据),两个都被显式
		// 关掉时模块确实不生效,面板必须跟着变。
		Extra: []GateSwitch{
			{Key: "show_reasoning_effort", DefaultOn: true, Effect: "日志表不再展示推理强度列。*bool 且默认打开,不写不会静默失效"},
			{Key: "show_cache_ratio", DefaultOn: true, Effect: "日志表不再展示缓存命中率列。*bool 且默认打开,不写不会静默失效"},
		},
	},

	// ── 没有配置开关:随扩展总开关一起生效 ──────────────────────────────
	//
	// 这四个模块只有自己的表与路由(或一个未配置时原样返回入参的 hook),
	// 不存在"配置里少一段就静默失效"的形态,因此不需要开关,也就不需要告警。
	{Module: "apiaddr", Effect: "API 地址簿:只有自己的表与只读路由,无 hook、无后台任务"},
	{Module: "channelops", Effect: "渠道批量操作:没有自己的表、没有 hook,只有三个管理端路由;" +
		"关掉扩展总开关时渠道列表页回落到上游自带的批量按钮,不会缺功能"},
	{Module: "paypass", Effect: "支付密码:只有自己的表与路由,启用与否由用户是否设置密码决定"},
	{Module: "subscription", Effect: "订阅名额闸门:未配置名额时 hook 原样返回入参,不需要开关"},
	{Module: "usergroup", Effect: "新用户默认分组:未配置时 hook 原样返回入参,与上游行为逐字一致"},
}

// ModuleGates 返回登记表的副本,供 qianye 包的结构性守卫与文档工具使用。
func ModuleGates() []ModuleGate {
	out := make([]ModuleGate, 0, len(moduleGates))
	out = append(out, moduleGates...)
	return out
}

// ModuleSection 是单个模块的配置段现状,同时是 /admin/health 的下发结构。
type ModuleSection struct {
	Module  string `json:"module"`
	Section string `json:"section"`
	Key     string `json:"key"`
	State   string `json:"state"`
	// Enabled 是该模块此刻的实际取值。State 说"运维有没有做决定",
	// Enabled 说"决定的结果是什么" —— 排障时两个都要看:一个 enabled=false
	// 且 state=declared 的模块是正常的,而 enabled=false 且 state=missing_section
	// 的模块八成不是任何人想要的结果。
	Enabled bool   `json:"enabled"`
	Effect  string `json:"effect"`
	// Fix 是需要告警的两种状态下的最小修法,**两种状态给的东西不一样**:
	//
	//	missing_section  一整段(`lottery:` + 缩进的开关行),追加到文件末尾即可
	//	missing_key      只有那一行(`  enabled: true`),要补进已经存在的那一段里
	//
	// 这个区分是必须的:missing_key 的前提正是"那一段已经在文件里了",再给一份
	// 带顶层键的完整段,照着粘贴就会产生重复的顶层 YAML 键,而 parseFile 的
	// KnownFields(true) 会把它判成解析错误 —— 一条"不阻断启动"的告警,其修复
	// 指引反而让整台网关起不来。展示端要按 state 说清楚往哪儿粘。
	Fix string `json:"fix,omitempty"`
}

// NeedsAttention 表示这一行对应的是"没人做过决定"的静默关闭。
func (m ModuleSection) NeedsAttention() bool {
	return m.State == SectionStateMissingSection || m.State == SectionStateMissingKey
}

// ModuleSectionStatus 返回当前配置快照下每个模块的段现状,按模块名排序。
func ModuleSectionStatus() []ModuleSection { return moduleSections(Get()) }

// moduleSections 把登记表摊平成"一个开关一行":模块总开关一行,它的每个二级
// 开关各再占一行。二级开关不能折进总开关那一行 —— 折进去就得在一行里表达两个
// 互相独立的状态,而"enabled 写了、precheck_enabled 没写"恰恰是最需要被看见的
// 那一种组合。同一个模块因此可能出现多行,展示端的行 key 必须带上 Key。
func moduleSections(c *Config) []ModuleSection {
	out := make([]ModuleSection, 0, len(moduleGates))
	for _, g := range moduleGates {
		out = append(out, describeGate(c, g))
		for _, s := range g.Extra {
			out = append(out, describeGate(c, ModuleGate{
				Module:    g.Module,
				Section:   g.Section,
				Key:       s.Key,
				DefaultOn: s.DefaultOn,
				Effect:    s.Effect,
			}))
		}
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Module != out[j].Module {
			return out[i].Module < out[j].Module
		}
		return out[i].Key < out[j].Key
	})
	return out
}

func describeGate(c *Config, g ModuleGate) ModuleSection {
	m := ModuleSection{
		Module:  g.Module,
		Section: g.Section,
		Key:     g.Key,
		Effect:  g.Effect,
		State:   SectionStateUngated,
	}
	if g.Section == "" || g.Key == "" {
		// 没有总开关的模块随扩展总开关一起生效,这里必须把它**算出来**。
		//
		// 留在零值 false 的后果不是少一个信息,而是这张表在说反话:apiaddr /
		// paypass / subscription / usergroup / logmetrics 五个正在工作的模块会被
		// 显示成「当前生效:否」,排障的人据此去配置文件里找一个根本不存在的开关。
		// 这张表存在的唯一理由是"界面上写的状态可以被相信"。
		m.Enabled = c.Enabled && anyExtraOn(c, g)
		return m
	}
	m.Enabled = gateValue(c, g)
	switch {
	case c.Declared(g.Section + "." + g.Key):
		m.State = SectionStateDeclared
	case g.DefaultOn:
		m.State = SectionStateDefaultOn
	case c.Declared(g.Section):
		// 段已经在文件里了,缺的只是这一行。这里**只能**给出那一行:
		// 再给一份带顶层键的完整段,粘贴之后文件里就有两个同名顶层键,
		// 下次启动 parseFile 直接报错,整台网关起不来。
		m.State = SectionStateMissingKey
		m.Fix = fmt.Sprintf("  %s: true", g.Key)
	default:
		m.State = SectionStateMissingSection
		m.Fix = sectionFixSnippet(g)
	}
	return m
}

// sectionFixSnippet 是"整段都不在文件里"时的最小可粘贴片段。
//
// 段内还有二级开关时,片段末尾附一条 YAML 注释点名它们,但**不替运维选值** ——
// precheck_enabled 打开就是真的开始拦截用户请求,那是运维的决定,不是修复指引
// 该替他做的。注释是合法 YAML,粘回去不影响解析(有测试钉住)。
func sectionFixSnippet(g ModuleGate) string {
	var b strings.Builder
	fmt.Fprintf(&b, "%s:\n  %s: true", g.Section, g.Key)
	names := make([]string, 0, len(g.Extra))
	for _, s := range g.Extra {
		if !s.DefaultOn {
			names = append(names, s.Key)
		}
	}
	if len(names) > 0 {
		fmt.Fprintf(&b, "\n  # 本段还有 %d 个同样是零值 false 的开关:%s",
			len(names), strings.Join(names, " / "))
		b.WriteString("\n  # 不显式写出来的话,它们对应的挂载点照样空转,而 " +
			g.Key + ": true 会让人以为已经生效")
	}
	return b.String()
}

// anyExtraOn 用于"段里没有总开关、但有若干二级开关"的模块(log_metrics):
// 任一二级开关打开,这个模块就有实际效果 —— 与 guard.featureOn 对 FlagLogMetrics
// 的判据(show_reasoning_effort || show_cache_ratio)同向。
// 压根没有二级开关的模块恒为 true,它们只随扩展总开关生效。
func anyExtraOn(c *Config, g ModuleGate) bool {
	if len(g.Extra) == 0 {
		return true
	}
	for _, s := range g.Extra {
		if gateValue(c, ModuleGate{Section: g.Section, Key: s.Key, DefaultOn: s.DefaultOn}) {
			return true
		}
	}
	return false
}

// gateValue 按 yaml 路径在 Config 上取开关当前的实际取值。
//
// 走反射而不是给每个模块写一个 func(*Config) bool:后者是 featureOn 的第二份
// 拷贝,两份东西会各自漂移,而漂移的方向恰好是"健康面板说开着,实际是关的"。
// 反射版本认的是 yaml tag,与运维在文件里写的路径是同一个东西。
func gateValue(c *Config, g ModuleGate) bool {
	f, ok := gateField(reflect.ValueOf(c).Elem(), g.Section, g.Key)
	if !ok {
		return false
	}
	switch {
	case f.Kind() == reflect.Bool:
		return f.Bool()
	case f.Kind() == reflect.Ptr && f.Type().Elem().Kind() == reflect.Bool:
		if f.IsNil() {
			return g.DefaultOn
		}
		return f.Elem().Bool()
	}
	return false
}

// gateField 定位 section.key 对应的字段值。第二个返回值为 false 表示
// 登记表里的路径在 Config 上不存在,或者它不是一个布尔开关。
func gateField(cfg reflect.Value, section, key string) (reflect.Value, bool) {
	sec, ok := fieldByYAMLName(cfg, section)
	if !ok || sec.Kind() != reflect.Struct {
		return reflect.Value{}, false
	}
	f, ok := fieldByYAMLName(sec, key)
	if !ok {
		return reflect.Value{}, false
	}
	if f.Kind() == reflect.Bool ||
		(f.Kind() == reflect.Ptr && f.Type().Elem().Kind() == reflect.Bool) {
		return f, true
	}
	return reflect.Value{}, false
}

func fieldByYAMLName(v reflect.Value, name string) (reflect.Value, bool) {
	t := v.Type()
	for i := 0; i < t.NumField(); i++ {
		if strings.Split(t.Field(i).Tag.Get("yaml"), ",")[0] == name {
			return v.Field(i), true
		}
	}
	return reflect.Value{}, false
}

// LogModuleSectionCheck 把"没人做过决定的静默关闭"打进启动日志。
//
// 调用点有两个:qianye.Init()(进程启动)与 config.Reload()(热载生效之后)。
// 热载那一处不是可有可无的 —— 一次误编辑把整段删掉,与启动时少一段是同一个
// 缺陷,而热载不会重启进程,启动日志里那一行永远不会再出现。
func LogModuleSectionCheck() { logModuleSections(Get()) }

// 一律用 SysError 而不是 SysLog:这条信息的全部价值在于"被人看见"。
// SysLog 会和每分钟几十行的常规信息混在一起,写了等于没写 —— 这正是前三次
// 事故里"日志一切正常"的由来。但绝不阻断启动:一个模块没开不造成资损,
// 让整台网关起不来才会。
func logModuleSections(c *Config) {
	if !c.Enabled {
		return
	}
	path := Path()
	if path == "" {
		path = "配置文件"
	}
	for _, g := range moduleGates {
		main := describeGate(c, g)
		logSection(main, path)
		for _, s := range g.Extra {
			// 整段都不在文件里时,上面那条已经把"这一段没写"说清楚了,而且它的
			// 片段末尾已经点名了这些二级开关。再逐个开关重复一遍,只会让一个
			// 全新站点的启动日志一次性刷出十几行 —— 告警刷屏与告警缺席是同一种
			// 失败:没人再读它。段写了、只缺这一行时必须报,那才是真正隐蔽的一种。
			if main.State == SectionStateMissingSection {
				continue
			}
			logSection(describeGate(c, ModuleGate{
				Module: g.Module, Section: g.Section, Key: s.Key,
				DefaultOn: s.DefaultOn, Effect: s.Effect,
			}), path)
		}
	}
}

func logSection(s ModuleSection, path string) {
	if !s.NeedsAttention() {
		return
	}
	cause := fmt.Sprintf("配置文件里没有 `%s:` 这一段", s.Section)
	// 修法必须跟着状态走。段已经存在时给出一整段,是在教运维写出重复的顶层
	// YAML 键 —— 粘完下次重启 parseFile 直接报解析错误,整台网关起不来。
	how := fmt.Sprintf("要启用:在 %s 末尾追加这一段", path)
	if s.State == SectionStateMissingKey {
		cause = fmt.Sprintf(" `%s:` 这一段里没有 `%s:` 这一行", s.Section, s.Key)
		how = fmt.Sprintf(
			"要启用:在 %s 里【已有的】 `%s:` 段内补上这一行(不要再写一个顶层 `%s:`,"+
				"重复的顶层键会让配置文件解析失败,整台网关起不来)", path, s.Section, s.Section)
	}
	common.SysError(fmt.Sprintf(
		"qianye: 模块 %s 的开关 %s 已编译进本二进制,但%s —— 它现在是关闭的。\n"+
			"  「没写」与「显式写了 %s: false」在进程内是同一个字节,因此这条告警是它们唯一的区别。\n"+
			"  现在的现象:%s\n"+
			"  %s\n%s\n"+
			"  确实要关闭:把 `%s: false` 显式写进 `%s:` 段,本行告警随即消失,一个字都不再报。",
		s.Module, s.Key, cause, s.Key, s.Effect, how, indentBlock(s.Fix, "    "), s.Key, s.Section))
}

func indentBlock(s, pad string) string {
	lines := strings.Split(s, "\n")
	for i, l := range lines {
		lines[i] = pad + l
	}
	return strings.Join(lines, "\n")
}

// ─────────────────── "这个键到底写没写" 的判定(段级) ───────────────────

// declaredPaths 收集 YAML 文本里实际写出来的全部键路径(点分,与 yaml tag 同名)。
//
// 只在 parseFile 里、且只在严格解析成功之后调用:那时文件语法必定合法,
// 未知字段也已经被 KnownFields(true) 拦掉,这里再走一遍纯粹是为了拿到
// "写了哪些键"这个 Go 结构体表达不出来的信息。
//
// 递归而不是只看两层:与 markNumbersUnset 一样,新增一层嵌套段不需要改这里。
func declaredPaths(raw []byte) map[string]bool {
	out := make(map[string]bool, 128)
	var doc yaml.Node
	if err := yaml.Unmarshal(raw, &doc); err != nil || len(doc.Content) == 0 {
		return out
	}
	collectDeclared(doc.Content[0], "", out)
	return out
}

func collectDeclared(n *yaml.Node, prefix string, out map[string]bool) {
	if n == nil || n.Kind != yaml.MappingNode {
		return
	}
	for i := 0; i+1 < len(n.Content); i += 2 {
		key := n.Content[i].Value
		if key == "" {
			continue
		}
		path := key
		if prefix != "" {
			path = prefix + "." + key
		}
		out[path] = true
		collectDeclared(n.Content[i+1], path, out)
	}
}

// Declared 表示这个 yaml 路径在配置文件里被显式写出来过。
//
// 注意它与取值无关:`enabled: false` 与 `enabled: true` 一样是"写过"。
// 这正是本文件要的判据 —— 我们分不出的从来不是 true 和 false,
// 而是 false 和"没人写过"。
func (c *Config) Declared(path string) bool { return c.declared[path] }
