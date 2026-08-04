package grouppricing

import (
	"fmt"
	"sort"
	"strings"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/qianye/groupratio"
	"github.com/QuantumNous/new-api/setting/billing_setting"
	"github.com/QuantumNous/new-api/setting/ratio_setting"

	"github.com/shopspring/decimal"
)

// isPattern 判断模型名是不是通配形态("*" 或 "前缀*")。
func isPattern(modelName string) bool {
	return strings.HasSuffix(strings.TrimSpace(modelName), "*")
}

// effective.go —— 折算后的**最终生效价**。
//
// 用户拍板选了"分组级价 × 分组倍率"的相乘方案。我在设计阶段提醒过:相乘意味着
// 运营在输入框里填的那个数不是用户最终付的价,心算很容易错一位。因此
// **管理端必须直接显示折算后的最终生效价**,这是这个方案的必要配套,不是可选装饰。
//
// 本文件是那个折算的唯一实现,列表接口与试算接口共用 —— 两处各写一套折算,
// 迟早会有一处漏乘分组倍率,而那一处正好是运营天天看的那个。
//
// ═════════════════ 「最终生效价」不再是一个标量(本轮的行为变更)═════════════════
//
// 改动前这里用的是 ratio_setting.GetGroupRatio(模型分组) —— 一个只认模型分组的
// 单参数查询。而热路径 relay/helper/price.go:58-68 的 HandleGroupRatio 在
// GetGroupGroupRatio(用户分组, 使用分组) 命中时会**整体替换**分组倍率。
// 本站已经配了若干条 GroupGroupRatio,所以管理端显示的"最终生效价"与真实扣费
// **早就对不上了**;而本轮把 GroupGroupRatio 从"偶尔配的例外"提升为主要机制之后,
// 这个不一致会从边缘 bug 变成每天都在骗运营的数字。
//
// 结构性事实:grouppricing 的规则键是 (模型分组, 模型),而真实倍率的键是
// (用户分组, 模型分组)。**因此一条规则的"最终生效价"是一组值,不是一个数。**
// 硬凑一个标量就是继续骗人。本文件的做法:
//
//	Effective                 头条数字,口径由 UserGroup 字段显式声明。
//	                          UserGroup == "" 时它是**兜底口径**(没有配专属倍率的
//	                          那些用户分组看到的价),前端必须原样标注,
//	                          不许再让它冒充"最终值"。
//	Effective.Uniform         全部可达用户分组的倍率是否一致。true 时那个头条
//	                          数字对所有人成立(今天绝大多数情况),false 时不成立。
//	[]UserGroupEffective      逐个用户分组的展开。只对**真的配了** GroupGroupRatio
//	                          的用户分组渲染明细行,再加一行 user_group="*" 的兜底 ——
//	                          不给全站每个用户分组都铺一行。
//
// 倍率一律走 qianye/groupratio.Resolve,它内部调 service.GetUserGroupRatio
// (service/group.go:127-133),与 HandleGroupRatio 的分支逐字同构。
// **禁止在这里再写第四份 if** —— 全仓已经有三份复制的倍率解析
// (price.go:59 / quota.go:119 / task_billing.go:308),第四份必然自己漂移。

// wildcardUserGroup 是兜底口径那一行的 user_group 取值。
//
// 用 "*" 而不是空串:空串在 JSON 里与"字段没给"长得一样,而这一行恰恰是
// 覆盖人数最多的那一行,不能让它在前端退化成一个可有可无的缺省值。
const wildcardUserGroup = "*"

// Effective 是一条规则在某个 (用户分组, 模型分组) 下的最终生效价。
//
// 全部金额/倍率字段都是十进制字符串:这些数字会被运营直接拿去和对外报价单
// 核对,经过 float64 打印出来的 0.30000000000000004 会让人对整套系统失去信任。
type Effective struct {
	// UserGroup 声明这一份折算是**站在谁的角度**算的。
	//
	// 空串 = 兜底口径(未配置专属倍率的用户分组)。这个字段不是装饰:
	// 同一条规则在不同用户分组下是不同的价,不写清楚口径的数字没有意义。
	UserGroup string `json:"user_group"`

	// GroupRatio 是本次折算实际用的分组倍率(= 真正会被乘进账单的那个)。
	GroupRatio string `json:"group_ratio"`
	// RatioSource 是 GroupRatio 的来历:
	//   override 命中 GroupGroupRatio[用户分组][模型分组],整体替换兜底倍率
	//   inherit  未命中,回落 GroupRatio[模型分组]
	RatioSource string `json:"ratio_source"`
	// BaseGroupRatio 是模型分组的兜底倍率(GroupRatio[模型分组])。
	// RatioSource==inherit 时它等于 GroupRatio;不等时,差额就是专属倍率的作用。
	BaseGroupRatio string `json:"base_group_ratio"`

	// GroupRatioMin / GroupRatioMax 是该模型分组在**全部可达用户分组**上的倍率跨度
	// (兜底值 ∪ 全部专属倍率)。Uniform 为 true 表示两者相等 ——
	// 此时头条数字对所有人成立,前端可以照旧只显示一个数。
	GroupRatioMin string `json:"group_ratio_min"`
	GroupRatioMax string `json:"group_ratio_max"`
	Uniform       bool   `json:"uniform"`

	// GlobalValue 是该模型当前的全局价/倍率(未配置时为空)。
	GlobalValue string `json:"global_value,omitempty"`
	// GlobalEffective = 全局价 × 分组倍率,也就是**改动前**这个分组实际付的价。
	GlobalEffective string `json:"global_effective,omitempty"`

	// RuleValue 是规则里填的分组级价/倍率。
	RuleValue string `json:"rule_value"`
	// RuleEffective = 分组级价 × 分组倍率,也就是**切换后**这个分组实际付的价。
	// 这一个字段就是这套 UI 存在的全部理由。
	RuleEffective string `json:"rule_effective"`

	// DeltaPercent 是相对改动前的涨跌幅(百分比字符串,负数表示变便宜)。
	// 全局价缺失时为空 —— 没有基准就没有涨跌幅,不能拿 0 去凑一个数。
	//
	// 注意它**与分组倍率无关**:涨跌幅 = 新值/旧值 - 1,倍率在比值里约掉了。
	// 同一条规则在所有用户分组下的涨跌幅完全相同。
	DeltaPercent string `json:"delta_percent,omitempty"`

	// Unit 说明 RuleEffective 的单位,直接显示在数字后面。
	Unit string `json:"unit"`

	// QuotaPerCall 只在 price 口径下有值:一次调用实际扣多少 quota。
	// 运营看的是余额数字,美元是他们心里的换算,quota 才是账面上真正减少的量。
	QuotaPerCall int64 `json:"quota_per_call,omitempty"`

	// Warning 是"这条规则配了也不会生效"的显式提示(口径侧)。
	//
	// 本扩展已经四次栽在"配置定义了却没有消费方"上。规则口径与模型当前的
	// 全局计费口径不匹配(给按 token 计费的模型配 tiered 乘数、给阶梯计价的
	// 模型配 price)时,它会安静地一直不生效,而运营从列表上完全看不出来。
	Warning string `json:"warning,omitempty"`

	// RatioWarning 是**倍率侧**的告警,与 Warning 刻意分成两个字段:
	// 一个说"这条规则会不会生效",另一个说"这个倍率数字可不可信",
	// 混在一个字符串里会让运营分不清该去改规则还是去改分组倍率表。
	RatioWarning string `json:"ratio_warning,omitempty"`
}

// UserGroupEffective 是一条规则在某个**用户分组**下的最终生效价。
//
// 只对真的配了 GroupGroupRatio 的用户分组生成明细行,外加一行
// UserGroup == "*" 的兜底 —— 给全站每个用户分组都铺一行会让列表爆炸,
// 而那些行的数字与兜底行逐位相同,没有信息量。
type UserGroupEffective struct {
	// UserGroup 为 "*" 表示"未配置专属倍率的用户分组",即兜底口径。
	UserGroup string `json:"user_group"`

	GroupRatio string `json:"group_ratio"`
	// Source: override | inherit。"*" 那一行恒为 inherit。
	Source string `json:"source"`

	RuleEffective   string `json:"rule_effective"`
	GlobalEffective string `json:"global_effective,omitempty"`
	QuotaPerCall    int64  `json:"quota_per_call,omitempty"`
	// DeltaPercent 每一行都相同(倍率在比值里约掉了)。保留是为了让这一行
	// 可以被独立渲染,不必回头去拼头条对象。
	DeltaPercent string `json:"delta_percent,omitempty"`

	// Warning 是这一格独有的告警(目前只有一种:Task 类模型的交叉格差额缺陷)。
	Warning string `json:"warning,omitempty"`
}

// priceBasis 是与分组倍率**无关**的那一半折算:单位、全局基准值、涨跌幅。
//
// 拆出来的理由不是复用,是防误读:涨跌幅在所有用户分组下完全相同(倍率约掉了),
// 而展开成"每用户分组一行"之后,最容易发生的误解就是以为那一列也随倍率变。
// 让它只算一次,这件事在代码里就是自明的。
type priceBasis struct {
	unit         string
	globalValue  decimal.Decimal
	hasGlobal    bool
	deltaPercent string
	// isPrice 为 true 时才有 quota_per_call(按次口径才谈得上"一次调用扣多少")。
	isPrice bool
}

func newPriceBasis(modelName, mode string, value decimal.Decimal) priceBasis {
	// 通配规则没有唯一的"全局价"可比 —— 它覆盖的是一批模型。
	// 硬取一个值来当基准会让涨跌幅指向一个根本不存在的模型。
	pattern := isPattern(modelName)

	b := priceBasis{}
	switch mode {
	case ModePrice:
		b.unit = "美元/次"
		b.isPrice = true
		if p, ok := ratio_setting.GetModelPrice(modelName, false); ok && !pattern {
			b.globalValue, b.hasGlobal = decimal.NewFromFloat(p), true
		}
	case ModeRatio:
		b.unit = "模型倍率"
		if r, ok, _ := ratio_setting.GetModelRatio(modelName); ok && !pattern {
			b.globalValue, b.hasGlobal = decimal.NewFromFloat(r), true
		}
	case ModeTiered:
		b.unit = "阶梯乘数"
		b.globalValue, b.hasGlobal = decimal.NewFromInt(1), true
	}

	if b.hasGlobal && !b.globalValue.IsZero() {
		delta := value.DivRound(b.globalValue, 12).Sub(decimal.NewFromInt(1)).
			Mul(decimal.NewFromInt(100))
		b.deltaPercent = delta.StringFixed(2)
	}
	return b
}

// computeEffective 折算一条 (用户分组, 模型分组, 模型, 口径, 值) 的最终生效价。
//
// userGroup 传空串 = 兜底口径。**不要**把空串理解成"全站通用":它只对没有配
// 专属倍率的那些用户分组成立,配了的用户走的是另一个数(见 Uniform 与
// effectiveByUserGroup)。
func computeEffective(userGroup, group, modelName, mode string, value decimal.Decimal) Effective {
	group = normalizeGroup(group)
	// 用户分组只去空白,**不折叠大小写**:倍率侧 GetGroupGroupRatio 是精确
	// map 查找且在 3 条计价路径上,我们无权改;这边折叠、那边不折叠,
	// 就会造出"管理端显示 0.3、实际按兜底扣费"的新骗人数字。
	userGroup = strings.TrimSpace(userGroup)

	// 规则表的键是折叠过的(rules.go 的 normalizeGroup,MySQL 列排序规则大小写不敏感),
	// 而倍率表是精确查找。两边直接用同一个折叠名,含大写的分组会在倍率侧整个落空:
	// 头条倍率退化成 fail-open 的 1.0,overriddenUserGroups 也永远查不到那个分组,
	// 于是 Uniform 算成 true —— 界面断言"这个价对所有用户分组成立",
	// 而真正在打折的那一档根本不在展开行里。ratioGroupName 负责把折叠名映射回
	// 倍率表里那个**真实存在**的键。
	ratioGroup, aliased := ratioGroupName(group)

	res := groupratio.Resolve(userGroup, ratioGroup)
	basis := newPriceBasis(modelName, mode, value)
	overrides := overriddenUserGroups(ratioGroup)
	lo, hi := ratioSpread(res.Base, overrides)

	e := Effective{
		UserGroup:      userGroup,
		GroupRatio:     normalizeDecimal(decimal.NewFromFloat(res.Ratio)),
		RatioSource:    res.Source,
		BaseGroupRatio: normalizeDecimal(decimal.NewFromFloat(res.Base)),
		GroupRatioMin:  normalizeDecimal(decimal.NewFromFloat(lo)),
		GroupRatioMax:  normalizeDecimal(decimal.NewFromFloat(hi)),
		Uniform:        lo == hi,
		RuleValue:      normalizeDecimal(value),
		Unit:           basis.unit,
		DeltaPercent:   basis.deltaPercent,
	}

	ratio := decimal.NewFromFloat(res.Ratio)
	ruleEff := value.Mul(ratio)
	e.RuleEffective = normalizeDecimal(ruleEff)
	if basis.isPrice {
		e.QuotaPerCall = quotaPerCall(ruleEff)
	}
	if basis.hasGlobal {
		e.GlobalValue = normalizeDecimal(basis.globalValue)
		e.GlobalEffective = normalizeDecimal(basis.globalValue.Mul(ratio))
	}

	e.Warning = modeMismatchWarning(modelName, mode)
	e.RatioWarning = ratioWarning(ratioGroup, res, aliased, group)
	return e
}

// ratioGroupName 把规则表里那个**折叠过**的模型分组名映射回分组倍率表里
// 真实存在的键。
//
// 三种结果:
//
//	精确命中          → 原样返回(绝大多数情况)
//	只差大小写且唯一  → 返回倍率表里那个真名,并置 aliased —— 热路径的规则查找
//	                    也是折叠匹配的(rules.go:141),所以这条规则确实会命中,
//	                    只是倍率必须按真名查,否则管理端算出来的是 fail-open 的 1.0
//	                    而热路径乘的是真值。本轮修的就是这个「界面骗人」形状。
//	多个大小写变体    → 原样返回并置 aliased:选哪一个都是猜,交给告警去说。
func ratioGroupName(folded string) (string, bool) {
	if ratio_setting.ContainsGroupRatio(folded) {
		return folded, false
	}
	match, count := "", 0
	for name := range ratio_setting.GetGroupRatioCopy() {
		if strings.EqualFold(name, folded) {
			match, count = name, count+1
		}
	}
	if count == 1 {
		return match, true
	}
	return folded, count > 1
}

// effectiveByUserGroup 展开一条规则在各用户分组下的最终生效价。
//
// 第一行永远是 UserGroup=="*" 的兜底口径(覆盖人数最多的那一档),
// 其后是真的配了 GroupGroupRatio 的用户分组,按分组名排序 ——
// 顺序必须稳定,否则同一条规则每次刷新明细行都在跳。
func effectiveByUserGroup(group, modelName, mode string, value decimal.Decimal) []UserGroupEffective {
	// 与 computeEffective 同一条映射:折叠名只用来查规则,倍率必须按倍率表里的真名查。
	ratioGroup, _ := ratioGroupName(normalizeGroup(group))
	basis := newPriceBasis(modelName, mode, value)
	overrides := overriddenUserGroups(ratioGroup)

	rows := make([]UserGroupEffective, 0, len(overrides)+1)
	// 兜底行走 Resolve("") 而不是直接 GetGroupRatio:数值完全相同,
	// 但前者会把"这个模型分组根本不在倍率表里"登记进失配登记簿并限频告警。
	rows = append(rows, userGroupRow(wildcardUserGroup, groupratio.SourceInherit,
		groupratio.Resolve("", ratioGroup).Ratio, basis, value, ""))
	for _, ov := range overrides {
		rows = append(rows, userGroupRow(ov.UserGroup, groupratio.SourceOverride,
			ov.Ratio, basis, value, ""))
	}
	return rows
}

func userGroupRow(userGroup, source string, ratioFloat float64,
	basis priceBasis, value decimal.Decimal, warning string) UserGroupEffective {
	ratio := decimal.NewFromFloat(ratioFloat)
	ruleEff := value.Mul(ratio)

	row := UserGroupEffective{
		UserGroup:     userGroup,
		GroupRatio:    normalizeDecimal(ratio),
		Source:        source,
		RuleEffective: normalizeDecimal(ruleEff),
		DeltaPercent:  basis.deltaPercent,
		Warning:       warning,
	}
	if basis.isPrice {
		row.QuotaPerCall = quotaPerCall(ruleEff)
	}
	if basis.hasGlobal {
		row.GlobalEffective = normalizeDecimal(basis.globalValue.Mul(ratio))
	}
	return row
}

// quotaPerCall 把"美元/次"换算成账面上真正减少的额度。
func quotaPerCall(effective decimal.Decimal) int64 {
	return effective.Mul(decimal.NewFromFloat(common.QuotaPerUnit)).IntPart()
}

// userGroupRatio 是一条 (用户分组 → 该模型分组的专属倍率)。
type userGroupRatio struct {
	UserGroup string
	Ratio     float64
}

// overriddenUserGroups 列出为该模型分组**显式配了**专属倍率的用户分组。
//
// 数据源是上游 options.GroupGroupRatio(倍率的唯一真相源,扩展库不存镜像)。
// 每次调用都实时读:存一份镜像就要有同步机制,而同步失败的表现正是
// "管理端显示 A、热路径乘 B" —— 与本文件正在修的缺陷完全同形。
func overriddenUserGroups(modelGroup string) []userGroupRatio {
	all := ratio_setting.GetGroupRatioSetting().GroupGroupRatio.ReadAll()
	out := make([]userGroupRatio, 0, len(all))
	for userGroup, inner := range all {
		ratio, ok := inner[modelGroup]
		if !ok {
			continue
		}
		out = append(out, userGroupRatio{UserGroup: userGroup, Ratio: ratio})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].UserGroup < out[j].UserGroup })
	return out
}

// userGroupCandidates 是试算接口"站在哪个用户分组的角度"这个下拉的取值域。
//
// 取 GroupGroupRatio 的键(真的配了专属倍率的用户分组)∪ GroupRatio 的键。
// 后者是必要的:运营也需要能选一个**没有**专属倍率的分组,亲眼确认它落在兜底口径上。
//
// 方案 3 的已知代价在这里露头:users.group 与 channels.group 共用同一个字符串
// 命名空间,所以这份清单里既有真正的用户分组也有模型分组,分不开。
// 它只是输入辅助,不是闸门 —— 试算接口不校验这个名字存不存在,
// 因为"这个用户分组现在什么倍率都没配"恰恰是最需要试算一次的情形。
func userGroupCandidates() []string {
	seen := map[string]struct{}{}
	for userGroup := range ratio_setting.GetGroupRatioSetting().GroupGroupRatio.ReadAll() {
		seen[userGroup] = struct{}{}
	}
	for group := range ratio_setting.GetGroupRatioCopy() {
		seen[group] = struct{}{}
	}
	out := make([]string, 0, len(seen))
	for name := range seen {
		out = append(out, name)
	}
	sort.Strings(out)
	return out
}

// ratioSpread 给出兜底值与全部专属倍率合起来的取值跨度。
//
// 两端相等 = 这条规则的最终生效价对所有人是同一个数,列表可以照旧显示一个数字;
// 不等 = 那个头条数字对一部分用户不成立,必须显示区间并标注。
func ratioSpread(base float64, overrides []userGroupRatio) (lo, hi float64) {
	lo, hi = base, base
	for _, ov := range overrides {
		if ov.Ratio < lo {
			lo = ov.Ratio
		}
		if ov.Ratio > hi {
			hi = ov.Ratio
		}
	}
	return lo, hi
}

// ratioWarning 给出**倍率侧**的告警:界面上那个数字与真实扣费不是一回事。
//
// ratioGroup 是倍率表里真正被查的那个键,ruleGroup 是规则表里存的(折叠过的)名字。
// 两者不同时必须点名 —— 否则运营看到一个正确的数字却不知道它来自另一个名字,
// 而这两个名字在别处(倍率表编辑页、矩阵页)是分开显示的。
//
// Task 类模型的交叉格告警已随上游 service/task_billing.go 的修复一并删除:
// 那里的差额结算现在按 (users.group, task.Group) 查交叉格,与预扣同口径。
// 留着一条不再成立的告警,和显示一个错误的数字一样糟。
func ratioWarning(ratioGroup string, res groupratio.Resolution, aliased bool, ruleGroup string) string {
	parts := make([]string, 0, 2)
	if res.BaseMissing {
		msg := fmt.Sprintf("模型分组 %q 不在分组倍率表里,上游 GetGroupRatio 会 fail-open "+
			"按 1.0 倍计费(不报错、不拒绝,只写一行会被滚走的日志)", ratioGroup)
		if res.BaseNearMiss != "" {
			msg += fmt.Sprintf(";倍率表里存在仅大小写不同的 %q —— 分组倍率按精确匹配,"+
				"二者被视为两个分组", res.BaseNearMiss)
		}
		parts = append(parts, msg)
	}
	if aliased && ratioGroup != ruleGroup {
		parts = append(parts, fmt.Sprintf("本规则存的模型分组名是 %q,而分组倍率表里的是 %q ——"+
			"规则匹配大小写不敏感,倍率查找大小写敏感,本页的倍率按 %q 取。"+
			"建议把两处改成同一个写法,避免以后再加一个仅大小写不同的分组时无法判断该用哪个",
			ruleGroup, ratioGroup, ratioGroup))
	} else if aliased {
		parts = append(parts, fmt.Sprintf("分组倍率表里存在多个仅大小写不同、都能匹配 %q 的分组名 ——"+
			"倍率按精确匹配,本页无法确定该用哪一个,显示的是 fail-open 的兜底值。请先把重名清理掉", ruleGroup))
	}
	return strings.Join(parts, " | ")
}

// modeMismatchWarning 判断规则口径与模型当前的全局计费口径是否对得上。
//
// 返回的是告警而不是错误:全局计费口径随时可能被改(管理员在「分组与模型定价设置」
// 里加一条按次价,这个模型就从按 token 变成按次了),把它做成硬校验会让一条
// 昨天还合法的规则今天改不动。但它必须在列表里持续可见 —— 一条静默不生效的
// 价格规则,和一个定义了却没有消费方的配置项是同一种缺陷。
func modeMismatchWarning(modelName, mode string) string {
	if isPattern(modelName) {
		// 通配规则覆盖的是一批模型,它们的全局计费口径可能各不相同,
		// 判不出一个确定的结论。给一个不确定的结论比不给更糟。
		return "通配规则:覆盖范围内各模型的全局计费口径可能不同,请用试算接口逐个确认是否生效"
	}
	expr, hasExpr := billing_setting.GetBillingExpr(modelName)
	tiered := billing_setting.GetBillingMode(modelName) == billing_setting.BillingModeTieredExpr &&
		hasExpr && strings.TrimSpace(expr) != ""

	switch mode {
	case ModeTiered:
		if !tiered {
			return "该模型当前不是阶梯表达式计价,tiered 乘数不会生效"
		}
	case ModePrice:
		if tiered {
			return "该模型当前是阶梯表达式计价,price 覆盖不会生效(请改用 tiered 口径)"
		}
		if _, ok := ratio_setting.GetModelPrice(modelName, false); !ok {
			return "该模型当前按 token 计费,配 price 覆盖会把它在本分组下切换成按次计费 —— " +
				"计价口径发生变化,影子差额无法按比例折算,请确认这是本意"
		}
	case ModeRatio:
		if tiered {
			return "该模型当前是阶梯表达式计价,ratio 覆盖不会生效(请改用 tiered 口径)"
		}
		if _, ok := ratio_setting.GetModelPrice(modelName, false); ok {
			return "该模型当前按次固定价计费,ratio 覆盖不会生效(请改用 price 口径)"
		}
	}
	return ""
}
