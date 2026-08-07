package ratio_setting

// qy_ratio_export.go —— 分组倍率解析的**唯一**入口,以及上游 GetGroupRatio
// fail-open 缺口的观测挂载点。
//
// ═══════════════════════ 为什么解析器落在这个包 ═══════════════════════
//
// 三条计费路径分属三个包:
//
//	relay/helper/price.go        预扣 / 主计价
//	service/quota.go             WSS / 音频结算
//	service/task_billing.go      Task 差额结算
//
// 它们此前各自抄了一遍「交叉格命中就用它,否则兜底」的两句 if,而三份复制品
// 迟早会漂移 —— 上一轮 task_billing 就漂成了 `GetGroupGroupRatio(group, group)`
// (对角格),预扣打折、结算原价,差额以追扣落到用户头上,而每份复制品都与自己
// 一致所以全绿。
//
// 合一的落点**只能是本包**:qianye/groupratio 里已经有一份合并形态,但它 import
// service 与 model,而 service/quota.go 本身就在 service 包里 —— 让计费路径去调它
// 是一个包级循环依赖(service → qianye/groupratio → service)。ratio_setting 是
// 两张倍率表的宿主,且不依赖 relay/* 与 service/*,是全仓唯一能被三处同时 import
// 的位置。
//
// ═══════════════════════ 两个入口的区别只有一个:billing ═══════════════════════
//
//	ResolveGroupRatio  计费调用。兜底缺失时按 billing=true 上报 —— 那一笔钱**已经**
//	                   按凭空的 1.0 扣掉了,必须逐笔可归因。
//	InspectGroupRatio  展示调用(管理端矩阵页、价格表、健康面板)。同样上报,但
//	                   billing=false:一个列 12 个模型分组的管理页每打开一次就会
//	                   撞到全部缺失项,把它们当成告警会立刻变成背景噪声,而背景
//	                   噪声与告警缺席是同一种失败。
//
// 两者共用同一个函数体,差别只有那个布尔 —— 展示与账单在结构上不可能分叉。

import "github.com/QuantumNous/new-api/common"

// 倍率来源。与上游三条计费路径原有的两个分支一一对应。
const (
	// GroupRatioSourceOverride:命中 GroupGroupRatio[用户分组][模型分组],
	// 整体替换兜底倍率。判据是 **key 是否存在**,不是值 —— 显式配的 0 是
	// 「我配的免费」,与「兜底本来就是 0」是两句话。
	GroupRatioSourceOverride = "override"
	// GroupRatioSourceInherit:未命中交叉格,回落 GroupRatio[模型分组]。
	GroupRatioSourceInherit = "inherit"
)

// GroupRatioResolution 是一次 (用户分组, 模型分组) 倍率解析的完整结果。
type GroupRatioResolution struct {
	// Ratio 是**真正会被乘进账单**的那个倍率。
	Ratio float64
	// Source 是 Ratio 的来历:GroupRatioSourceOverride / GroupRatioSourceInherit。
	Source string
	// Base 是模型分组的兜底倍率(GroupRatio)。Source==Inherit 时 Base==Ratio。
	Base float64
	// BaseMissing 为 true 表示模型分组**不在** GroupRatio 里。此时 Base 是 1,
	// 但那个 1 不是任何人配出来的 —— 它是上游 GetGroupRatio 的 fail-open。
	BaseMissing bool
}

// SilentFallback 表示这一次解析**真的**用上了那个凭空的 1.0。
//
// BaseMissing 单独为真还不足以说明资损:交叉格命中时倍率是运营配出来的,
// 兜底缺不缺都不影响这一笔的金额(那只是一处需要在管理端清理的登记不一致)。
// 只有「没有交叉格 + 兜底也查不到」这一档才是静默按原价扣费。
func (r GroupRatioResolution) SilentFallback() bool {
	return r.BaseMissing && r.Source == GroupRatioSourceInherit
}

// GroupRatioMiss 是一次静默 fail-open 的逐笔标记。
//
// 它挂在 RelayInfo 上(与既有的 QuotaClamp 同形),由 service/log_info_generate.go
// 嵌进消费日志的 other.admin_info。嵌在 admin_info 下天然只有管理员可见
// (model.formatUserLogs 会为非管理员剥掉整个 admin_info),不需要另做权限。
//
// **这是把不可逆损失变成可逆损失的关键**:今天日志里 group 是对的、quota 是按
// 1.0 算出来的,事后重算不出来"本该是多少";打上标记之后每一笔都能定位并补差。
type GroupRatioMiss struct {
	UserGroup    string  `json:"user_group"`
	ModelGroup   string  `json:"model_group"`
	AppliedRatio float64 `json:"applied_ratio"`
}

// AuditMap 是写进 other.admin_info 的形状。与 common.QuotaClamp.AuditMap 同风格。
func (m *GroupRatioMiss) AuditMap() map[string]any {
	if m == nil {
		return nil
	}
	return map[string]any{
		"user_group":    m.UserGroup,
		"model_group":   m.ModelGroup,
		"applied_ratio": m.AppliedRatio,
		"reason":        "model group is not present in GroupRatio; billed at fail-open ratio 1.0",
	}
}

// QyNoteGroupRatioMiss 是「模型分组不在分组倍率表里」的唯一上报口。
//
// ─────────────────────── 为什么它同时挂在源和汇 ───────────────────────
//
// 汇(GetGroupRatio 的 miss 分支)负责**检出完备性**:GetGroupRatio 的调用点
// 不止三条计费路径,今后新增的第四处不接任何 hook 也会被它捞到。
// 源(ResolveGroupRatio)负责**逐请求可归因**:只有它知道这一次是不是真的用上了
// 那个凭空的 1.0,以及对应的用户分组是谁。
//
// billing=false 的调用**只计数、不告警、不进 admin_info**(见文件头)。
//
// 默认空实现:扩展未启用(甚至整个 qianye 目录被删掉)时行为与上游逐位一致。
// 赋值只在 qianye.Init() 发生一次,早于 HTTP 监听与后台协程。
var QyNoteGroupRatioMiss = func(modelGroup string, billing bool) {}

// ResolveGroupRatio 是**三条计费路径唯一允许使用**的倍率解析入口。
func ResolveGroupRatio(userGroup, modelGroup string) GroupRatioResolution {
	return resolveGroupRatio(userGroup, modelGroup, true)
}

// InspectGroupRatio 是展示 / 对账 / 管理端用的解析入口,结论与计费逐位相同。
func InspectGroupRatio(userGroup, modelGroup string) GroupRatioResolution {
	return resolveGroupRatio(userGroup, modelGroup, false)
}

// resolveGroupRatio 是那个唯一的函数体。
//
// 与上游三条路径原有的写法逐位同结论:
//
//	命中交叉格 → 用交叉格的值(哪怕它是 0)
//	未命中     → GroupRatio[模型分组];查不到时 fail-open 返回 1
//
// 刻意走 lookupGroupRatio 而不是 GetGroupRatio:后者会自己上报一次
// billing=false,而这里必须按 billing 参数上报,两处一起发就成了重复计数。
// 上游那行 "group ratio not found" 的 SysLog 由 lookupGroupRatio 保留。
func resolveGroupRatio(userGroup, modelGroup string, billing bool) GroupRatioResolution {
	base, ok := lookupGroupRatio(modelGroup)
	res := GroupRatioResolution{
		Ratio:       base,
		Source:      GroupRatioSourceInherit,
		Base:        base,
		BaseMissing: !ok,
	}
	if override, hit := GetGroupGroupRatio(userGroup, modelGroup); hit {
		res.Source = GroupRatioSourceOverride
		res.Ratio = override
	}
	if res.BaseMissing {
		QyNoteGroupRatioMiss(modelGroup, billing && res.SilentFallback())
	}
	return res
}

// lookupGroupRatio 是 GroupRatio 表的单点查找,含上游那条 fail-open 与它的 SysLog。
//
// 第二个返回值是「这个 1 是配出来的还是编出来的」。上游 GetGroupRatio 把这两者
// 压成同一个 float64,于是"分组被删掉"与"分组配的就是 1"在调用方眼里完全一样 ——
// 本包全部的可观测性都建立在把它们分开这一件事上。
func lookupGroupRatio(name string) (float64, bool) {
	ratio, ok := groupRatioMap.Get(name)
	if !ok {
		common.SysLog("group ratio not found: " + name)
		return 1, false
	}
	return ratio, true
}
