package groupmatrix

import (
	"errors"
	"fmt"
	"net/http"
	"sort"
	"strconv"
	"strings"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/qianye/config"
	"github.com/QuantumNous/new-api/qianye/db"
	"github.com/QuantumNous/new-api/qianye/groupratio"
	"github.com/QuantumNous/new-api/qianye/guard"
	qymodel "github.com/QuantumNous/new-api/qianye/model"
	"github.com/QuantumNous/new-api/qianye/modules/groupns"
	"github.com/QuantumNous/new-api/qianye/service/audit"
	"github.com/QuantumNous/new-api/service"
	"github.com/QuantumNous/new-api/setting"
	"github.com/QuantumNous/new-api/setting/ratio_setting"

	"github.com/gin-gonic/gin"
	"gorm.io/gorm"
)

// api_admin.go —— 矩阵读写、接管开关、单条孤儿令牌修复。
//
// 每一次写操作都留下 before/after 审计:这份清单决定谁能发出请求、按什么倍率
// 扣钱,事后「为什么这批用户突然全 403」只能靠它来回答。成功与失败**各写一条** ——
// 被 409 挡回去的那次同样是重要信号(说明有人在拿一份过期的预览做决定)。

const auditCategoryGroupMatrix = "group_matrix"

const (
	auditActionScopeUpdate  = "groupmatrix.scope.update"
	auditActionModeUpdate   = "groupmatrix.mode.update"
	auditActionGrantUpdate  = "groupmatrix.grant.update"
	auditActionRatioPublish = "groupmatrix.ratio.publish"
	auditActionTokenRepair  = "groupmatrix.token.repair"
)

// 倍率格子的来源。回显每格都必须带它:运营看到 0 时要能一眼分清
// 「我配的 0(免费)」和「兜底倍率本来就是 0」。
const (
	SourceOverride = "override"
	SourceInherit  = "inherit"
)

// ─────────────────────────── GET /group-matrix ───────────────────────────

// userGroupRow 是「用户分组」这一张表的一行 —— **整张表只有这一份数据源**。
//
// ══════════════ 为什么它要把登记表的列一起带上来 ══════════════
//
// 上一轮管理端有两张并排的表:「用户分组」(从 users.group 观测出来的一档档人)
// 与「用户分组登记」(运营登记出来的清单)。这个区分是内部数据模型的事
// (刚建出来还没有人的分组只存在于登记表;历史遗留的 users.group 值可能不在登记表里),
// 而它被原样搬到了界面上 —— 两张表并排、名字几乎一样、内容互相重叠。
//
// 本轮把差异收进两个布尔(Registered / Observed)并**只出一张表**:
// 清单 = 观测值 ∪ 登记表。差异不再是一张表,而是一行上的一个徽标,
// 并且"补登记"变成编辑时的自动动作(见 groupns.adminUpdateUserGroup)。
type userGroupRow struct {
	Name string `json:"name"`
	// DisplayName / Note / Enabled / SortOrder 来自登记表 qy_user_groups。
	// Registered 为 false 时它们全是零值 —— 那不是"没填",是"还没有登记行"。
	DisplayName string `json:"display_name"`
	// Note 是项目方点名的「用户分组备注」列。
	//
	// ⚠ 它**不是**上一轮那个 note:上一轮这个字段装的是 scope.note(接管说明),
	// 现在那一份改叫 scope_note。两者的主语完全不同 —— 一个说"这一档人是谁",
	// 一个说"为什么给这一档设了范围"。合用一个键会让前端拿到一段答非所问的文字。
	Note      string `json:"note"`
	Enabled   bool   `json:"enabled"`
	SortOrder int    `json:"sort_order"`
	// Registered 表示 qy_user_groups 里有这一行。
	// Observed 表示 users.group 里此刻真的有人挂着这个名字。
	//
	// 两者都为 false 的行也可能出现:它的名字只存在于 options 的
	// GroupGroupRatio 外层键 / TopupGroupRatio 键里(一份读不到任何人的死配置)。
	// 这种行**必须显示**,否则那份配置永远没有入口可以清掉。
	Registered bool `json:"registered"`
	Observed   bool `json:"observed"`

	// TopupRatio 是充值倍率 options.TopupGroupRatio[分组名] 的**库里原值**。
	//
	// ***float64:null = 没配过(上游 GetTopupGroupRatio 回落 1 并写一条 SysError)。**
	// 用 float64 + omitempty 会让任何显式写下的值在序列化时消失,而"没配过"与
	// "配过"在这一页上要显示成两种话。
	//
	// ⚠ **0 在充值倍率上不是「免费」。** 四条支付路径(易支付 / Stripe / Waffo /
	// Pancake)在读到 0 之后一律 `if ratio == 0 { ratio = 1 }`,而且
	// controller/topup.go 还有一道 `payMoney < 0.01 → 拒单`。所以库里的 0 既不免费、
	// 也不是运营以为的那个意思 —— 本轮起写侧直接拒绝 0(见 groupns.validateTopupRatio),
	// 存量 0 由 TopupRatioEffective 如实显示成 1 并出一条 warning。
	TopupRatio *float64 `json:"topup_ratio"`
	// TopupRatioEffective 是此刻**真正会被乘进充值金额**的那个数(十进制字符串)。
	//
	// 它逐字复刻支付路径的判据(缺键 → 1、0 → 1),而不是把 TopupRatio 原样印出来:
	// 这一列是报价,印一个不会被收款用到的数就是骗人。没配过时它是 "1",
	// 并且 TopupRatio 为 null —— 两个字段合起来才说得清
	// 「按 1 收钱,但那个 1 不是任何人配出来的」。
	TopupRatioEffective string `json:"topup_ratio_effective"`

	// ModelGroups 是项目方点名的「可用模型分组」列,**名称清单而不是个数**。
	//
	// 取值口径:设了范围时 = 那份清单(qy_group_grants),没设范围时 = 上游
	// service.GetUserUsableGroups 的实际结果。前端直接渲染它,不必再从 cells 里
	// 过滤一遍 —— 过滤条件是后端才知道的事,让前端重推一遍必然漂移。
	//
	// ⚠ 它是**配置值**,不一定是"此刻真的在生效的值":mode=shadow 的清单一个
	// 字节都不生效(读侧逐位返回上游)。所以这一列必须与 ScopeEnforced 同屏,
	// 前端在 shadow 上出「尚未生效」徽章。
	//
	// ⚠ 它**不受列轴约束**。列轴(modelGroups)是从 options.GroupRatio 的键派生的,
	// 而清单里完全可能引用一个已从倍率表消失的模型分组。早先这一列是在列轴循环
	// 内侧回填的,于是那种行被画成「一个都没有」—— 运营读到的是"这一档人一个池子
	// 都用不了",真实原因却是"授权指向了已消失的模型分组",两者的处置完全相反。
	// 现在直接从来源 map 算,并由 matrixWarnings 给出那条【需要处理】的告警。
	ModelGroups []string `json:"model_groups"`

	UserCount int64 `json:"user_count"`
	// ActiveTokenCount 是「启用且近 30 天有访问」的令牌数 ——
	// 「撤销这一行会打断谁」的分母。它必须长在行头上而不是藏进预览弹窗:
	// 运营在动格子的那一刻就要看见,点开才看得到就已经晚了。
	ActiveTokenCount int64  `json:"active_token_count"`
	Managed          bool   `json:"managed"`
	Mode             string `json:"mode"`
	AllowAuto        bool   `json:"allow_auto"`
	// ScopeNote 是 scope 行上的说明(「为什么给这一档设了范围」),
	// 与 Note(用户分组备注)是两件事,见 Note 的注释。
	ScopeNote string `json:"scope_note"`
	// SelfExcluded 表示权威清单不含这个用户分组自己。
	//
	// 它推翻了上游存在多年的不变量,所以必须显式下发让界面能醒目提示 ——
	// 但它是**警告不是拦截**:项目方明确要求这条不变量能被推翻。
	SelfExcluded bool `json:"self_excluded"`

	// ScopeState 是行头的**三态**,前端必须按它渲染,不得自己用 managed+len(grants) 推。
	//
	// 三态与 Scope 的注释一一对应:
	//
	//	ScopeStateUnset → 未设定范围:全部模型分组可用,各按自己的兜底倍率。
	//	ScopeStateSet   → 已设定范围:只可用清单里的那些。
	//	ScopeStateEmpty → 已设定范围但清单为空:一个模型分组都不可用(红色)。
	//
	// **不要在界面上用「已接管 / 未接管」那套词。** 新口径下"未接管"意味着
	// 全部可用,而"接管"这个词会让人读成"不接管就用不了",方向正好相反。
	ScopeState string `json:"scope_state"`

	// ScopeEnforced 回答「这份清单**此刻真的在限制人**吗」。
	//
	// ScopeState=set 只说明"配过清单",而 mode=shadow 的清单一个字节都不生效
	// (读侧 Resolve 逐位返回上游)。两者摆在一起会让界面显示成"已设范围"
	// 而实际谁都拦不住 —— 项目方的口径是「设了可用模型分组则用户只能选这些」,
	// 那句话对应的**只有 enforce**。所以这个布尔必须单独下发,
	// 而不是让前端从 mode 字符串里推(推错的方向是把 shadow 画成已生效)。
	ScopeEnforced bool `json:"scope_enforced"`

	// SelfInserted 表示这一档人能选到**与自己同名的模型分组**,而这一条
	// 既不来自可用清单、也不来自「用户可选」开关,来自上游 GetUserUsableGroups
	// 最后那一步的自我补入(判据:名字在 options.GroupRatio 里)。
	//
	// 它是「没设清单 = 按模型分组自己的用户可选开关」这条规则**唯一的例外**,
	// 而且不能删:存量有 5 个名字同时是用户分组和真的能路由的模型分组
	// (legacy_dual,最典型的是 539 个用户 + 76 行 abilities 的那一个),
	// 它们一直靠这一步隐式获得可选性。删掉的表现是这几档人**已经存在的、
	// 显式指向自己分组的令牌**在下一次请求同时 403。
	//
	// 所以不删、但必须**可见**:一条隐式规则不写在界面上,就等于没有规则。
	// 要让某一档人连自己都不能选,唯一受支持的做法是给它设一份不含自己的可用清单。
	SelfInserted bool `json:"self_inserted"`
}

// 行头三态。前端按它渲染,不自己推。
const (
	ScopeStateUnset = "unset"
	ScopeStateSet   = "set"
	ScopeStateEmpty = "empty"
)

// modelGroupRow 同时是矩阵的列头**和**「模型分组」那一张表的一行:
// 分组名称 / 兜底倍率 / 用户可选 / 分组备注。
type modelGroupRow struct {
	Name string `json:"name"`
	// DisplayName / Note 来自登记表 qy_model_groups。
	DisplayName string `json:"display_name"`
	// Note 是这个模型分组的**默认备注**,也就是说明文案阶梯的第 2 级
	// (按格备注没填时它生效)。见 Grant.Note。
	Note string `json:"note"`
	// BaseRatio 是模型分组的初始/兜底倍率(options.GroupRatio)。
	// 用户分组没有为它单独配倍率时,回落到这个值。
	//
	// **十进制字符串而不是 float64**:它会被展示在列头与继承格的占位符里,
	// 也会被前端拿去和格子上的覆盖值比较。走一遍 JSON float64 往返会把
	// 0.1 印成 0.10000000000000001,而这一页上每一个数字都是报价。
	BaseRatio string `json:"base_ratio"`
	// UserSelectable 是项目方点名的「用户可选」开关:
	// 这个模型分组在不在全局白名单 options.UserUsableGroups 的**键**里。
	//
	// 它是「用户分组没设可用清单时」唯一的判据(见 Resolve 的 unmanaged 分支
	// 与本文件 scopePolicy.UnsetMeansAll 的注释)。下发它是为了让
	// 「没设清单的那些分组到底能选到什么」在界面上可解释,而不是一句空话。
	UserSelectable bool `json:"user_selectable"`
	// UsableDescription 是全局白名单里那一行的**原文**(阶梯第 3 级)。
	// 与 Note 同屏显示,运营才看得出哪一段被覆盖了。
	UsableDescription string `json:"usable_description"`
	// HasChannels 为 false 时该分组下没有任何可用渠道,授权它等于什么都没给。
	HasChannels bool `json:"has_channels"`
}

type cellView struct {
	UserGroup  string `json:"user_group"`
	ModelGroup string `json:"model_group"`
	// Granted 是**当前真实的可选性**,不是"扩展库里有没有这一行"。
	//
	// 未接管的用户分组一律走上游 service.GetUserUsableGroups 的实际结果 ——
	// 回 false 会把「现在是敞开的」画成「现在是锁死的」,而这一页的全部价值
	// 就是"先让人看清现状再改"。首次上线时一条 scope 行都没有,那一屏正是
	// 最需要看准的一屏。
	Granted bool `json:"granted"`
	// Ratio 是 *float64:null = 未配置(继承),0 = 显式免费。
	// **禁止改成 float64 + omitempty** —— 运营填的 0 会在序列化时消失。
	Ratio  *float64 `json:"ratio"`
	Source string   `json:"source"`
	// InheritedFrom 是继承时实际生效的兜底倍率(十进制字符串,理由同 BaseRatio),
	// 让界面能把继承值以灰字显示而**不预填进输入框**
	// (预填会诱导运营把继承变成一次显式覆盖)。
	InheritedFrom string `json:"inherited_from"`

	// EffectiveRatio 是**这一格此刻真的会被乘进账单**的那个数(十进制字符串)。
	//
	// 它由 service.GetUserGroupRatio 现算,与三条计费路径同源;Source 只说明它
	// 来自交叉格还是兜底。下发它是为了让格子**永远不出现空白态** ——
	// 空白是 0 还是继承,人永远猜不对,而这一页上每个数字都是报价。
	EffectiveRatio string `json:"effective_ratio"`
	// BaseMissing 为 true 表示该模型分组**不在** options.GroupRatio 里。
	//
	// 此时上游 GetGroupRatio 已经 fail-open 静默返回 1 —— 那个 1 不是任何人配出来的。
	// 界面必须把这一格标红并写「兜底缺失·按 1.0 计费」,不能显示成一个正常的 1。
	BaseMissing bool `json:"base_missing"`

	// ReachableVia 回答「这一格此刻可达吗、是谁给的」:
	// scope(范围清单/上游默认) / plan(套餐解锁) / both / none。
	//
	// **必须按 scope ∪ plan 计算,不能只按 scope。** 否则会出现这样一种漂移:
	// 用户分组 A 设了范围且不含 G,但 A 组的某个用户买了解锁 G 的套餐 —— 他能用 G、
	// 按 GroupRatio[G] 计费,而矩阵页的 A 行根本不渲染这一格。运营几周后为了别的
	// 目的把 GroupRatio[G] 从 1.0 调到 3.0,那批人当场按 3 倍扣费,
	// **而站内没有任何一个页面显示过 A 组的人可以到达 G**。
	// 这不是倍率数值的漂移,是可达性认知的漂移,而它的后果直接是钱。
	ReachableVia string `json:"reachable_via"`
	// ReachableByPlans 是让这一格可达的套餐名(去重后排序)。空表示没有套餐参与。
	ReachableByPlans []string `json:"plan_titles"`

	// RatioSource 说明 EffectiveRatio **来自哪一层**:cross_cell(交叉格)
	// 或 group_ratio(该模型分组的兜底倍率)。
	//
	// 与 Source 的区别是刻意的:Source 描述"这一格配没配"(界面上要不要把输入框
	// 显示成已填),RatioSource 描述"扣钱时乘的是哪个数"。今天两者结论相同,
	// 但它们回答的不是同一个问题,合并成一个字段会让将来任何一层回落变得无法表达。
	RatioSource string `json:"ratio_source"`

	// Note 是**这一格自己写的**备注(qy_group_grants.note),空串 = 没写。
	// 它是输入框里那个值,不是用户最终看到的那一段 —— 后者是 EffectiveNote。
	Note string `json:"note"`
	// EffectiveNote 是用户在**建 key 选分组**时真正会看到的那一段文案,
	// 由后端按阶梯解析后下发(按格备注 > 模型分组备注 > 白名单原文 > 分组名)。
	//
	// 必须由后端算:阶梯有四级、其中两级在上游 setting 包里,让前端再实现一遍
	// 的表现是"管理端预览的文案与用户看到的不是同一段",而这正是本轮在收敛的东西。
	//
	// ⚠ 它必须与 Resolve 的结论**逐字相同**,包括生效条件:按格备注只在
	// mode=enforce 那一档被读到(shadow 与未设范围逐位返回上游)。所以这一格所属的
	// 用户分组不是 enforce 时,解析**跳过第 1 级** —— 否则管理端把一段用户永远
	// 看不到的文字显示成"已生效",而运营验收看的正是这个字段。
	EffectiveNote string `json:"effective_note"`
	// NoteSource 说明 EffectiveNote 来自阶梯的哪一级 ——
	// 界面据此把"这一格自己写的"与"继承下来的"渲染成两种样子
	// (继承值以灰字显示而**不预填进输入框**:预填会把一次继承变成一次显式覆盖,
	// 此后模型分组改了默认备注,这一格再也不跟着变)。
	NoteSource string `json:"note_source"`
	// NotePending 为 true 表示**这一格写了备注、但它此刻对用户不生效**
	// (Note != "" 而 NoteSource != grant)。今天唯一的成因是 mode != enforce。
	//
	// 单独一个布尔而不是让前端拿 note 与 note_source 自己比:那个比法是一条
	// 隐式规则,而这条规则一旦漏在某个外壳上,那个外壳就会把 shadow 期写下的备注
	// 画成已生效 —— 正是本字段要消灭的形状。
	NotePending bool `json:"note_pending"`
}

// 说明文案阶梯的四级。与 Grant.Note 的注释一一对应。
const (
	NoteSourceGrant      = "grant"
	NoteSourceModelGroup = "model_group"
	NoteSourceWhitelist  = "usable_groups"
	NoteSourceGroupName  = "group_name"
)

// 生效倍率的来源层。与 service.GetUserGroupRatio 的两个分支一一对应。
const (
	RatioSourceCrossCell  = "cross_cell"
	RatioSourceGroupRatio = "group_ratio"
)

// 格子的可达来源。
const (
	ReachableNone  = "none"
	ReachableScope = "scope"
	ReachablePlan  = "plan"
	ReachableBoth  = "both"
)

// PlanUnlockedModelGroups 由订阅侧注入:返回「被某个套餐解锁的模型分组 → 套餐名」。
//
// ══════════════ 为什么这个接缝必须存在于矩阵页而不是套餐页 ══════════════
//
// 运营改倍率时看的是矩阵页,不是套餐详情页。可达性只按 scope 算的话,
// 通过套餐可达的那些格子在矩阵页上根本不存在,而它们照样在花钱(见 cellView.ReachableVia)。
//
// 默认返回 nil = 没有任何套餐解锁 = 与本轮之前完全一致,所以在订阅侧接上之前
// 这个接缝是**行为中性**的。
//
// 契约(实现方必须满足):
//
//	冷路径专用      —— 只被管理端 GET /group-matrix 调用,允许查库,但要有超时;
//	                   任何内部失败一律 return nil(可达性少标一点,不影响任何扣费)。
//	返回值只读      —— 调用方不修改它;实现方也不得返回内部快照持有的那张 map。
//	键必须是模型分组 —— 且必须已在 options.GroupRatio 里(编译期已丢弃的不要给回来)。
var PlanUnlockedModelGroups = func() map[string][]string { return nil }

// PlanUnlockEnabled 回答「套餐解锁此刻整体是开着的吗」。
//
// 它与 PlanUnlockedModelGroups 分开,因为两者回答的不是同一个问题:
// 后者为空可能是"关掉了",也可能是"开着但一个绑定都没配"。这两种状态在矩阵页上
// 必须显示成不同的话 —— 前者要提示"已购用户当前拿不到他买的分组",后者什么都不用说。
//
// 开关本身在 plan_entitlement 段,不在 group_matrix 段:同一份事实不设两个配置源。
// 默认 false = 订阅侧尚未接入 = 与本轮之前完全一致。
var PlanUnlockEnabled = func() bool { return false }

// savePartial 是两库写入的半成状态。**只在部分失败时出现。**
//
// 它必须在界面上被看见:倍率落上游 options、清单落扩展库,两库不原子。
// 运营按完保存看到一句绿色的「已保存」然后走人,是这套设计最坏的失败方式。
type savePartial struct {
	RatioApplied      bool   `json:"ratio_applied"`
	MembershipApplied bool   `json:"membership_applied"`
	Message           string `json:"message"`
}

// matrixView 是 GET 的响应,**也是每一次写成功后强制回读的同一个形状**。
//
// 三个写接口(矩阵 / 接管开关 / 修复)全部回这个结构,前端因此可以无条件地
// 用响应替换本地状态。早期版本让写接口各回各的小对象,前端把它 setQueryData
// 进同一个缓存键 —— 下一次渲染 model_groups 是 undefined,整页崩到错误边界,
// 而改动其实已经落库了。
type matrixView struct {
	UserGroups        []userGroupRow  `json:"user_groups"`
	ModelGroups       []modelGroupRow `json:"model_groups"`
	Cells             []cellView      `json:"cells"`
	BaseRatioHash     string          `json:"base_ratio_hash"`
	Snapshot          gin.H           `json:"snapshot"`
	Warnings          []string        `json:"warnings"`
	ShadowWriteDenies []WriteDeny     `json:"shadow_write_denies"`
	Partial           *savePartial    `json:"partial,omitempty"`

	// SupportsGrantNote 恒为 true:本版本认 set_note / clear_note 两个动作。
	//
	// ══════════ 为什么要一个显式的能力位,而不是让前端嗅探 ══════════
	//
	// 嗅探("看看有没有哪一格带 note")在一个真实输入上给出错误答案:后端支持
	// 按格备注、但站里此刻一条备注都没写过 —— 每一格都是空串,嗅探判定"不支持",
	// 那一列永远不出现,功能上线即隐身。
	//
	// 反方向更贵:老后端不认这两个动作,新前端却画出输入框,运营敲进去的备注会和
	// **同一次提交里的倍率与授权动作一起**被整体拒绝(这是一次两库写入,不是逐条
	// 尽力)—— 表现是「改了备注之后连改价都保存不了」,而错误正文说的是一个他
	// 没听说过的动作名。
	//
	// 一个常量布尔换掉这两种失败,值。
	SupportsGrantNote bool `json:"supports_grant_note"`

	// ScopePolicy 回答「一个用户分组没有出现在这份范围配置里,会发生什么」。
	//
	// 它必须随每一次矩阵回读一起下发,而不是让前端写死一句话:这条口径本轮刚刚
	// **反转过**(上一轮是"新分组自动全遮断"),而前端写死的文案不会跟着后端一起回退。
	ScopePolicy scopePolicy `json:"scope_policy"`
}

// scopePolicy 是「未设定范围」这一档的常驻说明与计数。
type scopePolicy struct {
	// UnsetMeansAll 恒为 true。它不是开关,是把口径本身下发给界面 ——
	// 界面据此渲染那句常驻说明,而不是把它硬编码在前端。
	//
	// ⚠ 它的准确含义是「**本页不限制**未设定范围的用户分组」,不是「它们能用到
	// options.GroupRatio 里的每一个模型分组」。未设定范围时 Resolve 逐位返回上游
	// service.GetUserUsableGroups 的结果 —— 也就是全局「用户可选分组」清单
	// (setting.userUsableGroups),外加「用户分组自己恰好也是一个配了倍率的模型分组」
	// 时的那一条补入。这两件事在「全局清单没有列出全部模型分组」的站点上结论不同,
	// 而本站正是这种站点。界面文案必须按前一种说法写;要让它真的等于"全部模型分组",
	// 需要项目方拍板改变 Resolve 的恒等契约(那会同时推翻"未配置 = 上游指针恒等")。
	//
	// 刻意**不**提供一个 unscoped_sees_all 配置项:那种开关只会被一次性打开然后
	// 没人再看,并且它会让「未设范围」重新有两种含义,正是本轮在消灭的形状。
	UnsetMeansAll bool `json:"unset_means_all"`
	// UnsetGroups 是当前未设定范围的用户分组数。它必须常驻显示、不能只在
	// 有变化时通知 —— 一次性通知会被忽略,常驻的一栏不会。
	UnsetGroups int `json:"unset_groups"`
	// ScopedGroups / EmptyScopedGroups 分别是"设了非空范围"与"设了空范围"的分组数。
	// 后者单独计数是因为它是唯一会让整组用户一个模型分组都选不到的配置。
	ScopedGroups      int `json:"scoped_groups"`
	EmptyScopedGroups int `json:"empty_scoped_groups"`
	// SubscriptionUnlockOn 的真相源是 **plan_entitlement.enabled**,经
	// groupmatrix.PlanUnlockEnabled 这个接缝注入。
	//
	// **本段(group_matrix)里没有、也不会有第二个开关。** 两个开关方向相反:
	// group_matrix.enabled 拉下是让「收紧」失效,套餐解锁拉下是让已付款用户当场
	// 少掉一批分组 —— 共用一个开关的人一定会误伤一侧;而同一件事两个配置源,
	// 同步失败的表现正是「一个页面说开着、另一个说关着」。
	//
	// 关掉时通过套餐可达的格子全部失效,而套餐照常收钱 —— 这一栏是它唯一的形状。
	SubscriptionUnlockOn bool `json:"subscription_unlock_on"`
}

// ratioText 把倍率渲染成十进制字符串。
//
// 'f' + 精度 -1 给出能**原样往返**的最短表示:0.1 就是 "0.1",不会变成
// 0.10000000000000001。这一页上每一个数字都会被运营当成报价读。
func ratioText(v float64) string { return strconv.FormatFloat(v, 'f', -1, 64) }

// effectiveTopupRatio 复刻**支付路径**对充值倍率的处置,一个 if 都不能少。
//
// controller/topup.go:158、topup_stripe.go:389 与 :403、topup_waffo.go:94、
// topup_waffo_pancake.go:59 五处逐字相同:
//
//	ratio := common.GetTopupGroupRatio(group)   // 缺键 → 1 + 一条 SysError
//	if ratio == 0 { ratio = 1 }                 // ← 显式 0 在这里被抬回 1
//
// 所以库里的 0 **不是免费**。管理端把它原样印成 0 的表现是:界面写着 0、
// 收款按 1,而这个偏差不出现在任何告警里。本轮写侧已经拒绝 0
// (groupns.validateTopupRatio),这个函数负责把**存量** 0 如实显示成 1,
// 并由 matrixWarnings 单独出一句话。
func effectiveTopupRatio(v float64) float64 {
	if v == 0 {
		return 1
	}
	return v
}

func adminGetMatrix(c *gin.Context) {
	if !guard.RequireAPI(c, guard.FlagGroupMatrix) {
		return
	}
	view, err := buildMatrixView(db.Get())
	if err != nil {
		internalError(c, err)
		return
	}
	respond(c, view)
}

// buildMatrixView 组装「用户分组」这一张表的完整真实状态。
//
// ══════════════ 它是这一页**唯一**的读接口 ══════════════
//
// 项目方的原话是「一个列表框即可」。上一轮为了同一张表要打三个请求
// (矩阵 / 用户分组登记 / 模型分组登记),而三份数据各有各的刷新时机 ——
// 前端拼出来的那一行,人数来自 A 时刻、可用清单来自 B 时刻、备注来自 C 时刻。
// 运营照着它做的每一个决定都建立在一份从未同时存在过的状态上。
//
// 所以这里一次给全:分组名称 / 注册用户数 / 充值倍率 / 可用模型分组(名称清单)/
// 分组备注 + 编辑弹窗要的每一格(勾选、倍率、备注)。前端**禁止**再去拼第二个接口。
func buildMatrixView(gdb *gorm.DB) (*matrixView, error) {
	scopes, err := loadScopes(gdb)
	if err != nil {
		return nil, err
	}
	grantRows, err := loadGrantRows(gdb)
	if err != nil {
		return nil, err
	}
	grants := make(map[string]map[string]struct{}, len(grantRows))
	grantNotes := make(map[string]map[string]string)
	for _, r := range grantRows {
		set, ok := grants[r.UserGroup]
		if !ok {
			set = map[string]struct{}{}
			grants[r.UserGroup] = set
		}
		set[r.ModelGroup] = struct{}{}
		if r.Note == "" {
			continue
		}
		notes, ok := grantNotes[r.UserGroup]
		if !ok {
			notes = map[string]string{}
			grantNotes[r.UserGroup] = notes
		}
		notes[r.ModelGroup] = r.Note
	}
	ratios, baseHash, err := loadRatioMatrix()
	if err != nil {
		return nil, err
	}

	userRegistry := loadUserGroupRegistry(gdb)
	modelRegistry := loadModelGroupRegistry(gdb)
	topupRatios := loadTopupRatios()
	// **原文**而不是经备注覆盖之后的值:这一页要同屏显示"白名单里本来写着什么、
	// 现在被哪一级盖成了什么"。拿覆盖后的值当第 3 级,阶梯会自己吃掉自己
	// (GetUserUsableGroupsCopy 已经应用过第 2 级),于是 note_source 永远算不出
	// model_group 这一档。
	whitelist := setting.RawUserUsableGroupsCopy()

	userGroups, userCounts, observed := listUserGroups(scopes, userRegistry, ratios, topupRatios)
	activeTokens := activeTokenCounts()
	modelGroups := listModelGroups(modelRegistry, whitelist)
	// 套餐解锁的可达性。关掉开关时不查:那一档里通过套餐可达的格子确实不可达,
	// 界面必须跟着变,否则它会显示一批"看起来通、实际不通"的格子。
	planUnlocked := map[string][]string{}
	if PlanUnlockEnabled() {
		for mg, plans := range PlanUnlockedModelGroups() {
			names := append(make([]string, 0, len(plans)), plans...)
			sort.Strings(names)
			planUnlocked[mg] = names
		}
	}

	// 未设定范围的行必须显示**上游此刻的实际可选集合**(见 cellView.Granted)。
	// 走 service.GetUserUsableGroups 是安全的:该分组没有 scope 行时
	// Resolve 恒等返回上游,读不到我们自己写的东西。每个分组只算一次。
	upstreamUsable := make(map[string]map[string]string, len(userGroups))
	for _, ug := range userGroups {
		if _, managed := scopes[ug]; !managed {
			upstreamUsable[ug] = service.GetUserUsableGroups(ug)
		}
	}

	policy := scopePolicy{
		UnsetMeansAll:        true,
		SubscriptionUnlockOn: PlanUnlockEnabled(),
	}
	rows := make([]userGroupRow, 0, len(userGroups))
	for _, ug := range userGroups {
		sc, managed := scopes[ug]
		reg, registered := userRegistry[ug]
		row := userGroupRow{
			Name: ug, UserCount: userCounts[ug],
			ActiveTokenCount: activeTokens[ug], Managed: managed,
			Registered: registered, Observed: observed[ug],
			DisplayName: reg.DisplayName, Note: reg.Note,
			Enabled: reg.Enabled, SortOrder: reg.SortOrder,
			// 未设定范围时 Mode 仍下发一个非空值:前端的 mode 曾是一个二值枚举,
			// 空串会让它在 managed 被打开的那一刻落进"两个选项都没选中"的状态。
			Mode: ModeEnforce, ScopeState: ScopeStateUnset,
			// nil 切片会被序列化成 JSON null,前端对着 null 调 .map 会白屏。
			ModelGroups: make([]string, 0),
			// 没配过充值倍率时 Ratio 留 null,而**生效值仍然要给**:
			// 上游 GetTopupGroupRatio 在缺键时返回 1 并写一条 SysError,
			// 那个 1 不是任何人配出来的。两个字段合起来才说得清这件事。
			TopupRatioEffective: "1",
		}
		if v, ok := topupRatios[ug]; ok {
			val := v
			row.TopupRatio = &val
			row.TopupRatioEffective = ratioText(effectiveTopupRatio(v))
		}
		switch {
		case !managed:
			// 未设定范围:allow_auto 的预填值 = 上游此刻是否已经把 auto 放行。
			// 设定范围的开关打开时这就是零行为变更的那一份初值。
			_, row.AllowAuto = upstreamUsable[ug][autoGroup]
			// 自我补入只在"没设清单"这一档才可能发生:设了清单之后
			// Resolve 完全由 grants 决定,上游那一步够不到。
			// 判据与 service.GetUserUsableGroups 收窄后的那一条逐字同源。
			if _, self := upstreamUsable[ug][ug]; self && ratio_setting.ContainsGroupRatio(ug) {
				row.SelfInserted = true
			}
			policy.UnsetGroups++
		default:
			row.Mode, row.AllowAuto, row.ScopeNote = sc.Mode, sc.AllowAuto, sc.Note
			row.ScopeEnforced = sc.Mode == ModeEnforce
			_, self := grants[ug][ug]
			row.SelfExcluded = !self
			if len(grants[ug]) == 0 {
				row.ScopeState = ScopeStateEmpty
				policy.EmptyScopedGroups++
			} else {
				row.ScopeState = ScopeStateSet
			}
			policy.ScopedGroups++
		}
		rows = append(rows, row)
	}

	// ── 「可用模型分组」这一列直接从来源 map 算,**不经列轴** ──────────────
	//
	// 列轴是 options.GroupRatio 的键派生的,而一份清单完全可能引用一个已经从
	// 倍率表里消失的模型分组。早先这一列在列轴循环内侧回填,于是那种行被画成
	// 「一个都没有」—— 见 userGroupRow.ModelGroups 的注释。
	for i, row := range rows {
		names := make([]string, 0, len(grants[row.Name]))
		if upstream, unmanaged := upstreamUsable[row.Name]; unmanaged {
			for mg := range upstream {
				names = append(names, mg)
			}
		} else {
			for mg := range grants[row.Name] {
				names = append(names, mg)
			}
		}
		// auto 是伪分组,由 AllowAuto 单独表达。混进这一列会让运营把它当成一个池子。
		filtered := names[:0]
		for _, mg := range names {
			if mg != autoGroup {
				filtered = append(filtered, mg)
			}
		}
		sort.Strings(filtered)
		rows[i].ModelGroups = filtered
	}

	cells := make([]cellView, 0, len(userGroups)*len(modelGroups))
	for _, ug := range userGroups {
		upstream, unmanaged := upstreamUsable[ug]
		// 按格备注只在 enforce 那一档被 Resolve 读到,管理端的解析必须用同一个判据。
		enforced := !unmanaged && scopes[ug].Mode == ModeEnforce
		for _, mg := range modelGroups {
			granted := false
			if unmanaged {
				_, granted = upstream[mg.Name]
			} else {
				_, granted = grants[ug][mg.Name]
			}
			cv := cellView{
				UserGroup: ug, ModelGroup: mg.Name, Granted: granted,
				Source: SourceInherit, InheritedFrom: mg.BaseRatio,
				ReachableByPlans: make([]string, 0),
				Note:             grantNotes[ug][mg.Name],
			}
			cv.EffectiveNote, cv.NoteSource = resolveCellNote(cv.Note, mg.Note, whitelist, mg.Name, enforced)
			cv.NotePending = cv.Note != "" && cv.NoteSource != NoteSourceGrant
			cv.RatioSource = RatioSourceGroupRatio
			if v, ok := ratios[ug][mg.Name]; ok {
				val := v
				cv.Ratio, cv.Source = &val, SourceOverride
				cv.RatioSource = RatioSourceCrossCell
			}
			// 生效倍率由 qianye/groupratio 现算,它内部走 service.GetUserGroupRatio ——
			// 全仓唯一没有被复制的那一份解析,与三条计费路径同结论。
			// 绝不在这里再写一遍 if:第四份复制品迟早自己漂移,而漂移的表现正是
			// "管理端显示 A、热路径乘 B"。
			res := groupratio.Resolve(ug, mg.Name)
			cv.EffectiveRatio, cv.BaseMissing = ratioText(res.Ratio), res.BaseMissing
			cv.ReachableVia = ReachableNone
			if granted {
				cv.ReachableVia = ReachableScope
			}
			if plans, ok := planUnlocked[mg.Name]; ok && len(plans) > 0 {
				cv.ReachableByPlans = plans
				if granted {
					cv.ReachableVia = ReachableBoth
				} else {
					cv.ReachableVia = ReachablePlan
				}
			}
			cells = append(cells, cv)
		}
	}

	snap, loaded := SnapshotView()
	snapInfo := gin.H{
		"loaded": loaded, "age_seconds": int64(0), "version": int64(0),
		"stale": false, "dropped_grants": []string{},
	}
	if loaded {
		age := common.GetTimestamp() - loadedAt.Load()
		snapInfo["age_seconds"] = age
		snapInfo["version"] = snap.Version
		snapInfo["dropped_grants"] = snap.DroppedGrants
		// 陈旧刻意不丢弃快照(见 snapshot.go 的 activeSnapshot),所以「陈旧」是
		// 唯一一个"清单在生效、但内容可能是旧的"状态。不下发这个布尔,
		// 界面上那条横幅就永远不会出现,而它是这条设计选择的唯一补偿。
		snapInfo["stale"] = age > maxStaleSeconds()
	}

	return &matrixView{
		UserGroups: rows, ModelGroups: modelGroups, Cells: cells,
		BaseRatioHash: baseHash, Snapshot: snapInfo,
		Warnings: matrixWarnings(userGroups, modelGroups, grants, topupRatios),
		// 影子期的写入拒绝。它是**唯一可归因**的影子证据来源:
		// 读侧那个挂载点拿不到被查询的 key(理由见 WriteDeny 的注释),
		// 所以那一半的证据来自 preview 的日志聚合。
		//
		// 不下发它的话,这张表就是一张只写不读的表 —— 而只写不读的观测数据
		// 与没有观测是同一回事,只是更容易让人以为自己有证据。
		ShadowWriteDenies: listWriteDenies(gdb),
		ScopePolicy:       policy,
		SupportsGrantNote: true,
	}, nil
}

// respondMatrix 把写接口的响应统一成"回读后的真实状态"。
func respondMatrix(c *gin.Context, gdb *gorm.DB, partial *savePartial) {
	view, err := buildMatrixView(gdb)
	if err != nil {
		internalError(c, err)
		return
	}
	view.Partial = partial
	if partial == nil {
		respond(c, view)
		return
	}
	c.JSON(http.StatusOK, gin.H{
		"success": true, "message": partial.Message, "data": view,
	})
}

// activeTokenCounts 统计每个用户分组下「启用且近 30 天有访问」的令牌数。
//
// 单独一条聚合而不是复用 tokenPairStats:那份统计按 (用户分组, 模型分组) 分行,
// 而且刻意丢掉了 model_group 为空的行 —— 空分组令牌恰恰是这一档里数量最大的一批,
// 漏掉它们会让行头的分母显著偏小,而运营会据此低估撤销的影响。
func activeTokenCounts() map[string]int64 {
	out := map[string]int64{}
	if model.DB == nil {
		return out
	}
	type row struct {
		Grp string `gorm:"column:grp"`
		N   int64  `gorm:"column:n"`
	}
	col := model.QyCommonGroupCol()
	var rows []row
	q := model.DB.Model(&model.Token{}).
		Joins("JOIN users ON users.id = tokens.user_id").
		Select("users."+col+" as grp, count(*) as n").
		Where("tokens.status = ?", common.TokenStatusEnabled).
		Where("tokens.accessed_time >= ?", common.GetTimestamp()-tokenActiveDays*86400)
	err := groupByRaw(q, "users."+col).Scan(&rows).Error
	if err != nil {
		common.SysError("qianye/groupmatrix: 统计活跃令牌失败(行头的活跃令牌数会显示为 0): " + err.Error())
		return out
	}
	for _, r := range rows {
		if r.Grp == "" {
			continue
		}
		out[r.Grp] = r.N
	}
	return out
}

// listUserGroups 汇总应当出现在「用户分组」这一张表上的名字。
//
// ══════════════ 并集口径:观测 ∪ 登记 ∪ 以用户分组为键的配置 ══════════════
//
//	users.group                       观测值:此刻真的有人挂着的名字
//	qy_user_groups                    登记表:运营登记出来的清单(含 0 人的新分组)
//	qy_group_scopes                   设过范围的名字(它还在生效,不能从界面消失)
//	options.GroupGroupRatio 的外层键   为这一档配过交叉倍率
//	options.TopupGroupRatio 的键       为这一档配过充值倍率
//
// 后三者不是"多余的保险",它们各自都是**只能从这张表进入的配置**:一个 0 人、
// 未登记但设过范围的名字若不显示,那条 scope 行会永远生效且无入口可改。
//
// ══════════════ options.GroupRatio 的键**刻意不在**这个并集里 ══════════════
//
// 那张表的键是**模型分组**(渠道池子)。上一轮把它并进行轴,于是「用户分组」页上
// 列满了模型分组的名字 —— 一张本该回答"站上有哪几档人"的表,一半的行不是人。
// 这正是项目方说的"搞得一团糟"里最直接的一条。
//
// 代价说明白:一个**只**在 GroupRatio 里出现、既没有用户也没有登记行、也没配过
// 交叉/充值倍率的名字,从此不在这张表上。它本来就不是一个用户分组;真要把它变成
// 一档人,走「新建用户分组」。
func listUserGroups(scopes map[string]Scope, registry map[string]groupns.UserGroup,
	crossRatios ratioMatrix, topup map[string]float64) (names []string, counts map[string]int64, observed map[string]bool) {
	seen := map[string]struct{}{}
	counts = map[string]int64{}
	observed = map[string]bool{}

	if model.DB != nil {
		type row struct {
			Grp string `gorm:"column:grp"`
			N   int64  `gorm:"column:n"`
		}
		var rows []row
		// group 是三种数据库里的保留字,列名一律走 model.QyCommonGroupCol()。
		col := model.QyCommonGroupCol()
		err := groupByRaw(model.DB.Model(&model.User{}).
			Select(col+" as grp, count(*) as n"), col).Scan(&rows).Error
		if err != nil {
			common.SysError("qianye/groupmatrix: 统计用户分组失败" +
				"(表上仍会展示登记表、已设范围与配过倍率的分组,但**在册用户数会全部显示为 0**): " + err.Error())
		}
		for _, r := range rows {
			if r.Grp == "" {
				continue
			}
			seen[r.Grp] = struct{}{}
			observed[r.Grp] = true
			counts[r.Grp] = r.N
		}
	}
	for name := range registry {
		seen[name] = struct{}{}
	}
	for name := range scopes {
		seen[name] = struct{}{}
	}
	for name, row := range crossRatios {
		if len(row) == 0 {
			continue
		}
		seen[name] = struct{}{}
	}
	for name := range topup {
		seen[name] = struct{}{}
	}
	delete(seen, "")
	delete(seen, autoGroup)

	names = make([]string, 0, len(seen))
	for name := range seen {
		names = append(names, name)
	}
	sort.Strings(names)
	return names, counts, observed
}

// loadUserGroupRegistry 读出登记表 qy_user_groups(备注 / 显示名 / 启停 / 排序)。
//
// ══════════════ 读失败为什么只降级不报错 ══════════════
//
// 登记表是 fail-open 的(见 groupns/model.go):它不参与路由、不参与鉴权、不参与
// 计费,只提供给人看的属性。读不到它的时候,这张表仍然应该能打开并显示人数、
// 可用清单与倍率 —— 那些才是运营在出事时要看的。整体 500 掉的表现是
// 「扩展库抖一下,用户分组页就打不开」,而打不开的时刻正是最需要它的时刻。
//
// 代价是 registered 会全部显示成 false。这不是无声的:失败写 SysError,
// 而且"整站一条登记都没有"本身就是一个显眼到不可能被忽略的界面状态。
func loadUserGroupRegistry(gdb *gorm.DB) map[string]groupns.UserGroup {
	out := map[string]groupns.UserGroup{}
	if gdb == nil {
		return out
	}
	var rows []groupns.UserGroup
	if err := gdb.Order("sort_order asc, name asc").Find(&rows).Error; err != nil {
		common.SysError("qianye/groupmatrix: 读取用户分组登记表失败" +
			"(备注/显示名/排序会显示为空,registered 一律 false;人数与可用清单不受影响): " + err.Error())
		return out
	}
	for _, row := range rows {
		if row.Name == "" {
			continue
		}
		out[row.Name] = row
	}
	return out
}

// loadModelGroupRegistry 读出登记表 qy_model_groups(备注 = 说明文案阶梯的第 2 级)。
// 失败方向与 loadUserGroupRegistry 同源。
func loadModelGroupRegistry(gdb *gorm.DB) map[string]groupns.ModelGroup {
	out := map[string]groupns.ModelGroup{}
	if gdb == nil {
		return out
	}
	var rows []groupns.ModelGroup
	if err := gdb.Order("sort_order asc, name asc").Find(&rows).Error; err != nil {
		common.SysError("qianye/groupmatrix: 读取模型分组登记表失败" +
			"(模型分组备注会显示为空;倍率与可选清单不受影响): " + err.Error())
		return out
	}
	for _, row := range rows {
		if row.Name == "" {
			continue
		}
		out[row.Name] = row
	}
	return out
}

// loadTopupRatios 解出 options.TopupGroupRatio(用户分组 → 充值倍率)。
//
// 解析失败返回空 map 而不是错误:充值倍率与本页其余列毫无关系,
// 让一段坏 JSON 把整张表打不开是把一处配置错误放大成一次页面故障。
// 缺键与 0 的区分交给调用方(见 userGroupRow.TopupRatio 的 *float64)。
func loadTopupRatios() map[string]float64 {
	out := map[string]float64{}
	raw := common.TopupGroupRatio2JSONString()
	if raw == "" {
		return out
	}
	if err := common.UnmarshalJsonStr(raw, &out); err != nil {
		common.SysError("qianye/groupmatrix: 上游 TopupGroupRatio 不是合法 JSON," +
			"充值倍率一律显示为「未配置」: " + err.Error())
		return map[string]float64{}
	}
	return out
}

// listModelGroups 汇总列轴。判据是分组倍率表 —— 与上游
// middleware/auth.go 的 ContainsGroupRatio 同源,不在这张表里的分组
// 即使授权了也会被上游用「分组已被弃用」挡掉。
func listModelGroups(registry map[string]groupns.ModelGroup, whitelist map[string]string) []modelGroupRow {
	ratios := ratio_setting.GetGroupRatioCopy()
	withChannels := groupsWithEnabledAbilities()

	out := make([]modelGroupRow, 0, len(ratios))
	for name, ratio := range ratios {
		if name == autoGroup {
			continue
		}
		reg := registry[name]
		desc, selectable := whitelist[name]
		out = append(out, modelGroupRow{
			Name: name, DisplayName: reg.DisplayName, Note: reg.Note,
			BaseRatio: ratioText(ratio), HasChannels: withChannels[name],
			// 「用户可选」的判据是**键存不存在**,不是 value 有没有内容:
			// 一个 value 为空串的键仍然是"放行",而按 value 判会把它读成"没放行",
			// 于是运营删掉一段说明文案就顺手把一个分组从全站下拉里撤了。
			UserSelectable: selectable, UsableDescription: desc,
		})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}

// resolveCellNote 走一遍说明文案阶梯,返回最终文案与它来自哪一级。
//
// **全站只有这一处实现管理端口径**,而它必须与 Resolve(用户口径)结论相同:
// 第 1 级在 Resolve 里叠加,第 2~4 级由 setting.QyDescribeGroup 持有,
// 这里只是把同样两步按显示需要拆开命名。写第二份 if 链的表现是
// 「管理端预览的文案与用户看到的不是同一段」。
//
// enforced 为 false 时**跳过第 1 级**:那一档的 Resolve 逐位返回上游,
// 按格备注一个字节都不生效。把它算进来的表现是管理端把一段用户永远看不到的
// 文字显示成"已生效",而运营在 shadow 期做的正是"先配好再切 enforce"、
// 核对文案用的正是这个字段。
func resolveCellNote(grantNote, modelGroupNote string, whitelist map[string]string, modelGroup string, enforced bool) (string, string) {
	if enforced && grantNote != "" {
		return grantNote, NoteSourceGrant
	}
	if modelGroupNote != "" {
		return modelGroupNote, NoteSourceModelGroup
	}
	if desc, ok := whitelist[modelGroup]; ok && desc != "" {
		return desc, NoteSourceWhitelist
	}
	// 最后一级与 setting.GetUsableGroupDescription 的兜底逐字一致:分组名本身。
	return modelGroup, NoteSourceGroupName
}

// groupsWithEnabledAbilities 取 abilities 里所有仍有启用渠道的模型分组。
//
// 走 GORM 的 Model + Pluck 而不是拼 SQL:group 是三种数据库里的保留字,
// 只有让 GORM 拿着 model.Ability 的 schema 去渲染,引号规则才会自动跟着方言走。
func groupsWithEnabledAbilities() map[string]bool {
	out := map[string]bool{}
	if model.DB == nil {
		return out
	}
	var names []string
	if err := model.DB.Model(&model.Ability{}).
		Where("enabled = ?", true).Distinct().Pluck("group", &names).Error; err != nil {
		common.SysError("qianye/groupmatrix: 探测渠道分组失败(has_channels 一律按 false 显示): " + err.Error())
		return out
	}
	for _, n := range names {
		out[n] = true
	}
	return out
}

// matrixWarnings 列出保存前应当被看见、但**不拦截**的问题。
func matrixWarnings(userGroups []string, modelGroups []modelGroupRow,
	grants map[string]map[string]struct{}, topupRatios map[string]float64) []string {
	warns := make([]string, 0)

	// 存量充值倍率 0。写侧本轮起拒绝它,但库里可能已经有 ——
	// 它既不免费也不按 0 收,支付路径会把它抬回 1(见 effectiveTopupRatio)。
	// 不说出来的话,那一行显示「配置值 0 / 生效值 1」而没有任何解释。
	for _, ug := range userGroups {
		v, ok := topupRatios[ug]
		if !ok || v > 0 {
			continue
		}
		warns = append(warns, fmt.Sprintf(
			"【需要处理】用户分组 %q 的充值倍率是 %s,而支付路径会把非正的充值倍率抬回 1 收款 —— "+
				"它既不是免费也不是打折。请在这一档的编辑弹窗里改成一个大于 0 的数,或清空它(清空 = 按 1 收款)",
			ug, ratioText(v)))
	}

	// 大小写近似项。**刻意不折叠**:倍率侧 GetGroupGroupRatio 是精确 map 查找、
	// 在 3 条计费路径里、我们无权改。折叠成员资格而不折叠倍率会造出
	// 「users.group=VIP 命中 vip 的清单拿到访问权,倍率却回落兜底」——
	// 管理端显示 0.3、实际按 1.0 扣、零告警。让人看见并自己决定,不替他折叠。
	byLower := map[string][]string{}
	for _, ug := range userGroups {
		byLower[strings.ToLower(ug)] = append(byLower[strings.ToLower(ug)], "用户分组 "+ug)
	}
	for _, mg := range modelGroups {
		byLower[strings.ToLower(mg.Name)] = append(byLower[strings.ToLower(mg.Name)], "模型分组 "+mg.Name)
	}
	for _, names := range byLower {
		if len(names) < 2 {
			continue
		}
		distinct := map[string]struct{}{}
		for _, n := range names {
			distinct[n] = struct{}{}
		}
		if len(distinct) < 2 {
			continue
		}
		sorted := make([]string, 0, len(distinct))
		for n := range distinct {
			sorted = append(sorted, n)
		}
		sort.Strings(sorted)
		warns = append(warns, fmt.Sprintf(
			"存在仅大小写不同的分组名:%s。系统按**精确匹配**处理,它们是不同的分组 —— "+
				"不折叠是刻意的:倍率侧是精确查找且不可改,折叠成员资格会造出「界面 0.3、实际 1.0」",
			strings.Join(sorted, " / ")))
	}

	// 授权了一个没有任何启用渠道的模型分组:选它等于什么都没给。
	hasChannels := map[string]bool{}
	for _, mg := range modelGroups {
		hasChannels[mg.Name] = mg.HasChannels
	}
	for ug, set := range grants {
		for mg := range set {
			// 引用了一个**已经不在分组倍率表里**的模型分组。
			//
			// 这一条比"没有渠道"严重一档,而且是本页唯一能发现「运营删了一个仍被
			// 引用的模型分组」的机制 —— 上游删除时不做任何引用检查。
			// 后果不是少给权限:快照编译期会把它剔除,而矩阵页上那一格看起来是通的,
			// 用户实际会被上游用「分组已被弃用」挡掉。写在前面单独成句,不与渠道警告合并。
			if !ratio_setting.ContainsGroupRatio(mg) {
				hint := ""
				if near := groupratio.NearMiss(mg); near != "" {
					hint = fmt.Sprintf(",倍率表里有一个仅大小写不同的 %q(分组倍率按精确匹配,二者是两个分组)", near)
				}
				warns = append(warns, fmt.Sprintf(
					"【需要处理】用户分组 %q 的清单里有一个已从分组倍率表消失的模型分组 %q%s —— "+
						"该项已被快照剔除,「用户分组」页上那一格看起来是通的,用户实际会被上游的"+
						"「分组已被弃用」挡掉。请把它从清单里撤掉,或把这个模型分组加回「模型分组定价」的分组表",
					ug, mg, hint))
				continue
			}
			if !hasChannels[mg] {
				warns = append(warns, fmt.Sprintf(
					"用户分组 %q 被授权的模型分组 %q 当前没有任何启用中的渠道,选它等于寸步难行", ug, mg))
			}
		}
	}
	sort.Strings(warns)
	return warns
}

// ─────────────────────────── PUT /group-matrix ───────────────────────────

type putMatrixReq struct {
	Cells []Cell `json:"cells"`
	// DraftHash / ImpactHash 只在把某一行切到 enforce 时必需(见 adminPutScope)。
	// 这里带上是为了让"预览的是 A、保存的是 B"这种情形也能被挡住。
	DraftHash  string `json:"draft_hash"`
	ImpactHash string `json:"impact_hash"`
	// BaseRatioHash 是打开页面时那一刻的上游倍率指纹。
	//
	// 运营仍可在上游「系统设置-分组倍率」页改同一份数据 —— 那个入口**刻意不锁死**,
	// 扩展关掉之后运营必须还能在原地改倍率,这是回退能力的一部分。
	// 代价是可能有人在你编辑期间改过它,所以这里必须比对。
	BaseRatioHash string `json:"base_ratio_hash"`
}

// adminPutMatrix 落一次矩阵改动。
//
// ══════════════ 两库写入顺序:先倍率,后成员资格 ══════════════
//
// 这个顺序是算出来的,不是随手定的:
//
//	倍率成功 / 清单失败 → 新的可达组合还没打开,没有任何流量按新倍率走,
//	                      **零金额影响**,表现为"配了没生效"。
//	清单成功 / 倍率失败 → 用户立刻能选中该分组,倍率回落到 GroupRatio[模型分组],
//	                      可能比运营意图贵得多或便宜得多,**钱立刻按错误的价走**。
//
// 「先清单后倍率」只考虑了"没人被多扣",漏了"按兜底价放行"同样是错价。
//
// 成员资格内部先 revoke 后 grant:revoke 是唯一会打断线上流量的操作,
// 先做能让部分失败停在「收紧了但还没放开」这个更保守的状态。
func adminPutMatrix(c *gin.Context) {
	if !guard.RequireAPI(c, guard.FlagGroupMatrix) {
		return
	}
	var req putMatrixReq
	if err := c.ShouldBindJSON(&req); err != nil {
		badRequest(c, "请求体格式错误")
		return
	}
	cells, err := normalizeCells(req.Cells)
	if err != nil {
		badRequest(c, err.Error())
		return
	}
	if len(cells) == 0 {
		badRequest(c, "cells 为空,没有任何要保存的改动")
		return
	}

	ratioMu.Lock()
	defer ratioMu.Unlock()

	ratios, baseHash, err := loadRatioMatrix()
	if err != nil {
		internalError(c, err)
		return
	}
	// base_ratio_hash **必填**。早期版本写的是 `req.BaseRatioHash != "" && ...`,
	// 于是一个省略该字段的请求(脚本、curl、缓存了旧页面的前端)就能整条跳过这道闸门,
	// 静默覆盖另一个管理员刚在上游分组倍率页写下的值,而审计里看不出发生过冲突。
	if req.BaseRatioHash == "" {
		badRequest(c, "缺少 base_ratio_hash —— 它证明你手上这份倍率是最新的。"+
			"请先 GET /group-matrix 拿到它再保存")
		return
	}
	if req.BaseRatioHash != baseHash {
		writeMatrixFailure(c, auditActionRatioPublish, cells, ratios,
			errors.New("base_ratio_hash 不匹配:分组倍率在上游页面被改动过"))
		conflict(c, "分组倍率在上游「系统设置 → 计费与支付 → 模型分组定价」页被改动过,请重新载入「用户分组」页再保存")
		return
	}

	gdb := db.Get()
	scopes, err := loadScopes(gdb)
	if err != nil {
		internalError(c, err)
		return
	}
	beforeGrants, err := loadGrants(gdb)
	if err != nil {
		internalError(c, err)
		return
	}
	if err := validateCells(cells, scopes, beforeGrants); err != nil {
		badRequest(c, err.Error())
		return
	}
	if err := checkGrantBudget(gdb, cells, beforeGrants); err != nil {
		badRequest(c, err.Error())
		return
	}
	// ── 撤销闸门:必须先看过影响面 ──────────────────────────────────
	//
	// 撤销是唯一会让一批正在跑的令牌当场变成 403 的动作。这道闸门早先只存在于
	// 前端(diff-bar 的 needsPreview),而 putMatrixReq 虽然**声明**了两个哈希、
	// 注释里也写明它们是干什么的,服务端却一次都没读过 —— 一条 curl 就能绕过。
	// 更糟的是:一个确实回传了哈希的客户端也不会因为哈希过期而被拦,
	// 字段被静默忽略比不接收它更危险。
	if err := requirePreviewedRevoke(req, cells); err != nil {
		writeMatrixFailure(c, auditActionGrantUpdate, cells, grantsToLists(beforeGrants), err)
		conflict(c, err.Error())
		return
	}

	// ── 第一步:倍率(上游 options)──────────────────────────────────
	//
	// before 快照必须在 applyRatioCells 之前拷出来:那个函数就地改 ratios,
	// 之后再取就只剩 after,而"改之前是什么"正是这条审计一半的价值。
	beforeRatios := cloneRatioMatrix(ratios)
	ratioChanged := applyRatioCells(ratios, cells)
	if ratioChanged {
		if err := publishRatioMatrix(ratios); err != nil {
			writeMatrixFailure(c, auditActionRatioPublish, cells, beforeRatios, err)
			internalError(c, err)
			return
		}
		audit.Write(c, matrixAuditEntry(c, auditActionRatioPublish, cells, beforeRatios, nil))
	}

	// ── 第二步:成员资格(扩展库),先 revoke 后 grant ────────────────
	listErr := applyGrantCells(gdb, cells, c.GetInt("id"))
	before := grantsToLists(beforeGrants)
	if listErr != nil {
		writeMatrixFailure(c, auditActionGrantUpdate, cells, before, listErr)
	} else {
		audit.Write(c, matrixAuditEntry(c, auditActionGrantUpdate, cells, before, nil))
	}

	if err := InvalidateAndReload(); err != nil {
		common.SysError("qianye/groupmatrix: 写入后刷新快照失败(其它节点仍会按周期刷新): " + err.Error())
	}

	// ── 强制回读两侧真实状态 ────────────────────────────────────────
	//
	// 部分失败时运营必须**立刻在界面上**看到「倍率已生效、清单未生效」,
	// 而不是靠一个乐观的本地状态继续编辑。前端禁止用提交前的草稿渲染。
	// 回的是与 GET 完全相同的结构,前端因此可以无条件整体替换。
	var partial *savePartial
	if listErr != nil {
		// 倍率已经落了、清单没落。
		//
		// ⚠ 早先这里写的是「新的可达组合尚未打开,因此没有任何流量按新倍率走」——
		// **那句话只对 grant 出来的新组合成立**。对已经可达的组合(已 granted 的格子、
		// 以及全部未接管/影子用户分组的任意格子)改价,倍率写入成功的那一刻就在扣钱。
		// 运营据那句话判断"本次无资金影响、不必回滚",是一个由文案直接造成的事故。
		partial = &savePartial{
			RatioApplied: ratioChanged, MembershipApplied: false,
			Message: "倍率已生效,可选清单未生效。**已经可达的组合(已放开的格子、" +
				"以及未接管/影子模式下的任意格子)从这一刻起就按新倍率扣费**;" +
				"只有本次新放开的组合还没打开。请立刻确认改价是否符合预期,再决定重试还是回滚。",
		}
	}
	respondMatrix(c, gdb, partial)
}

// requirePreviewedRevoke 是撤销动作的服务端闸门。
//
// 只有含 revoke 的草稿才要求带 draft_hash / impact_hash:放开与改价不会让任何
// 一个正在跑的请求变成 403,为它们也强制走一遍预览只会让运营学会无脑点过。
// (改价的金额风险由前端的差异条与本函数之外的 base_ratio_hash 负责。)
func requirePreviewedRevoke(req putMatrixReq, cells []Cell) error {
	revoke := false
	for _, cell := range cells {
		if cell.Action == ActionRevoke {
			revoke = true
			break
		}
	}
	if !revoke {
		return nil
	}
	if req.DraftHash == "" || req.ImpactHash == "" {
		return errors.New("本次保存含撤销操作,必须先调 POST /group-matrix/preview 看过影响面," +
			"并把返回的 draft_hash 与 impact_hash 一起回传")
	}
	fresh, err := runPreview(previewReq{Cells: cells})
	if err != nil {
		return err
	}
	if fresh.DraftHash != req.DraftHash {
		return errors.New("预览的草稿与本次要保存的草稿不是同一份,请重新预览")
	}
	if fresh.ImpactHash != req.ImpactHash {
		return errors.New("影响面已经变化(预览之后有人建了新令牌、改了倍率表或改了清单),请重新预览")
	}
	return nil
}

// validateCells 是写入侧的第一道校验。快照编译时还会再跑一次同样的判据 ——
// 分组可能在保存之后被从倍率表删掉,只在保存时把关拦不住那种漂移。
//
// 报错正文是给**运营**看的,不是给写 curl 的人看的:早先这里写的是
// 「请先用 PUT /group-matrix/scope/:ug 建立 scope 行」,而收到它的人手上只有一个
// 弹窗。现在一律指向界面上真实存在的那个控件。
func validateCells(cells []Cell, scopes map[string]Scope, before map[string]map[string]struct{}) error {
	// 本次保存**之后**每一格的成员资格。按格备注只 UPDATE 不 INSERT
	// (见 applyGrantCells),所以给一个保存后仍然没有 grant 行的格子写备注,
	// 落库时会命中 0 行、事务照常提交、接口 200、审计写「改备注 1 项」,
	// 而库里一个字节都没变 —— 运营多半会以为是自己没点上,于是重复操作。
	// 静默丢弃在这里被换成一句能照着做的话。
	after := make(map[string]map[string]struct{}, len(before))
	for ug, set := range before {
		row := make(map[string]struct{}, len(set))
		for mg := range set {
			row[mg] = struct{}{}
		}
		after[ug] = row
	}
	for _, cell := range cells {
		switch cell.Action {
		case ActionGrant:
			if after[cell.UserGroup] == nil {
				after[cell.UserGroup] = map[string]struct{}{}
			}
			after[cell.UserGroup][cell.ModelGroup] = struct{}{}
		case ActionRevoke:
			delete(after[cell.UserGroup], cell.ModelGroup)
		}
	}

	for _, cell := range cells {
		if cell.Action != ActionGrant && cell.Action != ActionRevoke &&
			cell.Action != ActionSetNote && cell.Action != ActionClearNote {
			continue
		}
		if _, managed := scopes[cell.UserGroup]; !managed {
			// set_note 也走这一道:按格备注只在权威清单生效的那一档被读到
			// (见 Resolve),给一个没设范围的用户分组写备注等于写一条死配置,
			// 而它会在管理端回读时显示出来 —— 运营会以为已经配好了。
			return fmt.Errorf("用户分组 %q 还没有自己的可用模型分组清单,所以现在还不能勾选模型分组、"+
				"也不能给某一格单独写备注。请先在这一档的编辑弹窗里把「可用范围」打开,"+
				"再回到下面的清单里勾选与写备注。注意:范围一旦打开就**立即生效**,"+
				"该用户分组能选的模型分组从此完全由这份清单决定。",
				cell.UserGroup)
		}
		if cell.Action == ActionGrant && !ratio_setting.ContainsGroupRatio(cell.ModelGroup) {
			return fmt.Errorf("模型分组 %q 不在分组倍率表里,授权它没有意义 —— "+
				"上游会在请求时用「分组 %s 已被弃用」把用户挡掉。请先到「模型分组」页把它加回去",
				cell.ModelGroup, cell.ModelGroup)
		}
		if cell.Action != ActionSetNote && cell.Action != ActionClearNote {
			continue
		}
		if _, granted := after[cell.UserGroup][cell.ModelGroup]; !granted {
			return fmt.Errorf("模型分组 %q 不在用户分组 %q 的可用清单里,给它写的备注保存不下来 —— "+
				"备注是「这一档人看这个池子」的说明,那一格得先勾上。请在同一次保存里把它勾上,或者先撤掉这条备注。",
				cell.ModelGroup, cell.UserGroup)
		}
		// shadow 期的备注写得进库,但读侧逐位返回上游 —— 用户一个字都看不到。
		// 拒绝它是错的(运营正当地"先配好再切 enforce"),静默接受也是错的。
		// 这里不拦,由回读时的 note_pending 在界面上说明,见 cellView.NotePending。
	}
	return nil
}

// checkGrantBudget 挡住"一次脚本误操作让每个节点定期拉一张大表"。
func checkGrantBudget(gdb *gorm.DB, cells []Cell, before map[string]map[string]struct{}) error {
	limit := int64(config.Get().GroupMatrix.MaxGrants)
	if limit <= 0 {
		limit = 2000
	}
	cur, err := countGrants(gdb)
	if err != nil {
		return err
	}
	delta := int64(0)
	for _, cell := range cells {
		_, exists := before[cell.UserGroup][cell.ModelGroup]
		switch {
		case cell.Action == ActionGrant && !exists:
			delta++
		case cell.Action == ActionRevoke && exists:
			delta--
		}
	}
	if cur+delta > limit {
		return fmt.Errorf("清单行数将达到 %d,超过 group_matrix.max_grants(%d)", cur+delta, limit)
	}
	return nil
}

// applyRatioCells 把倍率动作作用在矩阵上,返回是否真的有改动。
//
// inherit 的格子**不写 key**(而不是写 0 或 null):上游 GetGroupGroupRatio 靠
// key 存在与否返回 (ratio, false),"没有这个 key" = 继承兜底倍率。
// 写 0 是**显式免费**,与继承是两件完全不同的事,而且 0 会命中
// relay/helper/price.go 的整体替换分支。
func applyRatioCells(m ratioMatrix, cells []Cell) bool {
	changed := false
	for _, cell := range cells {
		switch cell.Action {
		case ActionSetRatio:
			row, ok := m[cell.UserGroup]
			if !ok {
				row = map[string]float64{}
				m[cell.UserGroup] = row
			}
			if old, had := row[cell.ModelGroup]; had && old == *cell.Ratio {
				continue
			}
			row[cell.ModelGroup] = *cell.Ratio
			changed = true
		case ActionClearRatio:
			row, ok := m[cell.UserGroup]
			if !ok {
				continue
			}
			if _, had := row[cell.ModelGroup]; !had {
				continue
			}
			delete(row, cell.ModelGroup)
			changed = true
		}
	}
	return changed
}

// applyGrantCells 落成员资格。**先 revoke 后 grant**:revoke 是唯一会打断
// 线上流量的操作,先做能让部分失败停在更保守的状态。
func applyGrantCells(gdb *gorm.DB, cells []Cell, operatorId int) error {
	now := common.GetTimestamp()
	return gdb.Transaction(func(tx *gorm.DB) error {
		for _, cell := range cells {
			if cell.Action != ActionRevoke {
				continue
			}
			if err := tx.Where("user_group = ? AND model_group = ?", cell.UserGroup, cell.ModelGroup).
				Delete(&Grant{}).Error; err != nil {
				return err
			}
			// 取消授权时,把它从这一档的默认 auto 顺序里一并摘掉。
			//
			// 不摘的话运行期确实也安全(GetUserAutoGroup 会按当前权限过滤),
			// 但管理端仍然会把它显示在顺序里 —— 运营看到的是「A → 已撤销的 B → C」,
			// 实际执行的是「A → C」。界面与行为不一致比少一个功能更难排查,
			// 而这里的修法只是一次逐行读改写。
			if err := dropFromAutoOrder(tx, cell.UserGroup, cell.ModelGroup); err != nil {
				return err
			}
		}
		for _, cell := range cells {
			if cell.Action != ActionGrant {
				continue
			}
			var existing Grant
			err := tx.Where("user_group = ? AND model_group = ?", cell.UserGroup, cell.ModelGroup).
				Take(&existing).Error
			if err == nil {
				continue
			}
			if !errors.Is(err, gorm.ErrRecordNotFound) {
				return err
			}
			row := &Grant{
				UserGroup: cell.UserGroup, ModelGroup: cell.ModelGroup,
				OperatorId: operatorId, CreatedAt: now, UpdatedAt: now,
			}
			if err := tx.Create(row).Error; err != nil {
				return err
			}
		}
		// ── 按格备注放在最后 ────────────────────────────────────────────
		//
		// 排在 grant 之后是必需的:同一次保存里「勾上这个模型分组 + 给它写一句备注」
		// 是一次点击的两个动作,备注先跑会撞上"这一格还没有行"而被静默丢掉,
		// 于是运营看到分组勾上了、备注没了。
		//
		// **只 UPDATE,不 INSERT。** 没有 grant 行的格子不可选,给它写备注等于
		// 造一条 Resolve 永远遍历不到的死配置;而它会在管理端回读时显示出来,
		// 让运营以为那一格已经配好了。RowsAffected == 0 是正常结局(备注没变),
		// 不作为错误 —— 但也正因如此,这里不能靠它判断"格子存不存在"。
		for _, cell := range cells {
			if cell.Action != ActionSetNote && cell.Action != ActionClearNote {
				continue
			}
			if cell.Note == nil {
				continue
			}
			err := tx.Model(&Grant{}).
				Where("user_group = ? AND model_group = ?", cell.UserGroup, cell.ModelGroup).
				Updates(map[string]any{
					"note": *cell.Note, "operator_id": operatorId, "updated_at": now,
				}).Error
			if err != nil {
				return err
			}
		}
		return nil
	})
}

func grantsToLists(grants map[string]map[string]struct{}) map[string][]string {
	out := make(map[string][]string, len(grants))
	for ug, set := range grants {
		names := make([]string, 0, len(set))
		for mg := range set {
			names = append(names, mg)
		}
		sort.Strings(names)
		out[ug] = names
	}
	return out
}

// ───────────────── PUT /group-matrix/scope/:user_group ─────────────────

type putScopeReq struct {
	// Managed = 这一档要不要有自己的可用范围。true 建行(**立即生效**),false 删行。
	//
	// 刻意**没有** Mode 字段:shadow 已下线,行存在即生效。老客户端仍可能带
	// 一个 mode 字符串上来,ShouldBindJSON 会直接忽略它 —— 忽略比报错好,
	// 那个字段现在无论取什么值结果都一样。
	Managed   bool   `json:"managed"`
	AllowAuto *bool  `json:"allow_auto"`
	// AutoOrder 是这一档人默认的 auto 尝试顺序。
	//
	// nil = 这次不动它(沿用库里那份);空数组 = 清空成"回落全局清单"。
	// 指针语义在这里是必需的:两者在 JSON 里都长得像"没给",而它们的效果相反。
	AutoOrder *[]string `json:"auto_order"`
	Note      string    `json:"note"`
}

// adminPutScope 建立/撤销接管,或切换 shadow ↔ enforce。
//
// 首次接管时 grants 预填 = 切换**之前**调 service.GetUserUsableGroups(该分组)
// 的实际结果 —— 也就是上游差分算完之后的样子。运营看到的第一屏与现状完全一致,
// 因此不需要任何迁移工具,而且开箱零行为变更。
//
// 这一点很容易做错:预填值必须来自 **hook 未生效** 的那条路径。
// unmanaged 分支本来就返回上游原值,所以"切之前读一次"即可 —— 顺序不能反。
func adminPutScope(c *gin.Context) {
	if !guard.RequireAPI(c, guard.FlagGroupMatrix) {
		return
	}
	userGroup := strings.TrimSpace(c.Param("user_group"))
	if userGroup == "" || len(userGroup) > maxGroupNameLen {
		badRequest(c, "user_group 非法")
		return
	}
	if userGroup == autoGroup {
		badRequest(c, "auto 是伪分组,不能作为用户分组接管")
		return
	}
	var req putScopeReq
	if err := c.ShouldBindJSON(&req); err != nil {
		badRequest(c, "请求体格式错误")
		return
	}

	gdb := db.Get()
	var before Scope
	err := gdb.Where("user_group = ?", userGroup).Take(&before).Error
	existed := err == nil
	if err != nil && !errors.Is(err, gorm.ErrRecordNotFound) {
		internalError(c, err)
		return
	}

	action := auditActionScopeUpdate
	if existed && req.Managed {
		action = auditActionModeUpdate
	}

	// ── 这里曾经有一道「切 enforce 前必须先看影响面」的闸门,已整体拆除 ──
	//
	// 它要求客户端先调 preview、把 draft_hash 与 impact_hash 一起回传,对不上
	// 就 409。拆掉的理由是项目方拍板的口径变了:编辑用户分组的模型分组**立即生效**,
	// 令牌被挡就挡,提醒一下即可。闸门在新口径下只剩摩擦 —— 运营勾完清单还要
	// 多走两步才能让它生效,而那两步正是上一版里全站没人走完的那两步。
	//
	// 影响面本身没有消失:adminPutMatrix 保存清单之后会把受影响的令牌数
	// 一起返回,由前端直接提示。preview 端点也保留,供想先看一眼的人主动调用。

	now := common.GetTimestamp()
	var after *Scope
	if req.Managed {
		allowAuto := defaultAllowAuto(userGroup)
		if req.AllowAuto != nil {
			allowAuto = *req.AllowAuto
		} else if existed {
			allowAuto = before.AllowAuto
		}
		// mode 恒为 enforce:行存在即生效,不再有第二档。
		autoOrder := splitAutoOrder(before.AutoOrder)
		if req.AutoOrder != nil {
			autoOrder = *req.AutoOrder
		}
		after = newScope(userGroup, ModeEnforce, allowAuto, autoOrder, req.Note, c.GetInt("id"), now)
	}

	// 首次接管的预填必须在写 scope 行**之前**算好:写完之后再算,
	// 拿到的就是 hook 已经生效的结果(空清单),预填会变成一次静默的全量撤销。
	// make 而不是 var:nil 切片会被序列化成 JSON null,前端对着 null 调 .map 会白屏。
	prefill := make([]string, 0)
	if !existed && req.Managed {
		prefill = currentUsableGroups(userGroup)
	}

	// 首次建清单时被清掉的残留授权 —— 进审计。它是这次写入里唯一一处
	// **减少**了什么的地方,不说出来就只能靠事后比对库里的行才能发现。
	var dropped []string
	err = gdb.Transaction(func(tx *gorm.DB) error {
		if after == nil {
			// 撤销接管 = 删 scope 行 → 该用户分组回到 inherit(上游行为逐位一致)。
			// grants 刻意**保留**:那一份是"上一次配的是什么"的现场,而下一次
			// 建清单会把它对齐到预填(见 resetGrantsToPrefill),不会被悄悄复活。
			return tx.Where("user_group = ?", userGroup).Delete(&Scope{}).Error
		}
		if err := tx.Save(after).Error; err != nil {
			return err
		}
		if existed {
			// 已有 scope 行时清单由格子接口维护,这里一个字节都不该动。
			return nil
		}
		removed, err := resetGrantsToPrefill(tx, userGroup, prefill, c.GetInt("id"), now)
		dropped = removed
		return err
	})
	if err != nil {
		writeScopeFailure(c, action, scopeOrNil(existed, before), after, err)
		internalError(c, err)
		return
	}
	audit.Write(c, scopeAuditEntry(c, action, scopeOrNil(existed, before), after, prefill, dropped))

	if err := InvalidateAndReload(); err != nil {
		common.SysError("qianye/groupmatrix: 写入后刷新快照失败(其它节点仍会按周期刷新): " + err.Error())
	}

	// 与 PUT /group-matrix 回同一个结构:前端把响应 setQueryData 进同一个缓存键,
	// 回一个 {user_group, managed, scope, ...} 的小对象会让下一次渲染
	// model_groups 为 undefined,整页崩到错误边界 —— 而改动其实已经落库了。
	respondMatrix(c, gdb, nil)
}

// resetGrantsToPrefill 把一个**刚建立的**清单对齐成 modelGroups 这一份,
// 并返回因此被清掉的残留授权名。
//
// ══════════════ 为什么必须先查再建 ══════════════
//
// 撤销接管刻意只删 scope 行、**保留 grants**。于是「接管 → 撤销接管 → 再接管」
// 这条路上,预填会对已经存在的行再 Create 一次,撞上 uk_qy_ggrant_pair 唯一键 →
// 整个事务回滚 → 接口 500 → **该用户分组从此再也接管不了**,除非有人手工去库里
// 删行。撤销接管是本方案宣称的核心回退能力,回退一次就再也走不回来,那不叫回退。
//
// ══════════════ 为什么必须把预填之外的残留**删掉**,而不是留着 ══════════════
//
// 预填 = currentUsableGroups(该分组此刻真的能用的那些),而残留的 grant 行完全
// 可能包含一个**当前用不到**的模型分组:上一次配过清单、后来撤销接管,那一行就
// 留在库里。只增不删的话,新清单 = 预填 ∪ 残留 —— 多出来的那些既没有出现在建
// 清单的确认弹窗里(那里数的是预填),也没有出现在建立前的列表里(未接管档按
// 上游实际可用清单画),更没有任何人决定过要放开它们。等这一档日后切到 enforce,
// 它们连同各自那一格的倍率一起当场生效。
//
// 「这一次点击不改变任何人能用什么」是建清单这条路径的全部承诺,而残留复活
// 恰好是对它的违反,且**静默**:界面上的项数、后端的预填计数、审计里的预填清单
// 三处都不含那几行。
func resetGrantsToPrefill(tx *gorm.DB, userGroup string, modelGroups []string,
	operatorId int, now int64) ([]string, error) {
	keep := make(map[string]bool, len(modelGroups))
	for _, mg := range modelGroups {
		if mg == autoGroup || mg == "" {
			// auto 是伪分组,它的可选性由 scope.AllowAuto 控制,不进 grants。
			continue
		}
		keep[mg] = true
	}

	var existing []Grant
	if err := tx.Where("user_group = ?", userGroup).Find(&existing).Error; err != nil {
		return nil, err
	}
	have := make(map[string]bool, len(existing))
	dropped := make([]string, 0)
	for _, row := range existing {
		if keep[row.ModelGroup] {
			have[row.ModelGroup] = true
			continue
		}
		dropped = append(dropped, row.ModelGroup)
	}
	if len(dropped) > 0 {
		// 显式列名而不是 NOT IN:预填为空时 `NOT IN ()` 在三种方言上的行为
		// 各不相同(GORM 对空切片渲染出的 SQL 会把整条 WHERE 变成恒假或语法错)。
		sort.Strings(dropped)
		if err := tx.Where("user_group = ? AND model_group IN ?", userGroup, dropped).
			Delete(&Grant{}).Error; err != nil {
			return nil, err
		}
	}

	// 按预填的原顺序建行(currentUsableGroups 已排过序):按 map 迭代顺序建
	// 会让每次运行的自增主键顺序都不一样,而那是"同一份配置两次导出不一致"的来源。
	for _, mg := range modelGroups {
		if !keep[mg] || have[mg] {
			continue
		}
		have[mg] = true
		if err := tx.Create(&Grant{
			UserGroup: userGroup, ModelGroup: mg,
			OperatorId: operatorId, CreatedAt: now, UpdatedAt: now,
		}).Error; err != nil {
			return nil, err
		}
	}
	return dropped, nil
}

func scopeOrNil(existed bool, s Scope) *Scope {
	if !existed {
		return nil
	}
	return &s
}

// currentUsableGroups 取该用户分组**此刻**的可选清单(经上游全部规则算完之后)。
//
// 它就是 service.GetUserUsableGroups —— 调用是安全的:此刻该分组还没有 scope 行,
// Resolve 走 unmanaged 分支恒等返回上游,不会读到我们自己写的东西。
func currentUsableGroups(userGroup string) []string {
	got := service.GetUserUsableGroups(userGroup)
	out := make([]string, 0, len(got))
	for name := range got {
		out = append(out, name)
	}
	sort.Strings(out)
	return out
}

// defaultAllowAuto 首次接管时 auto 的预填值 = 「auto 当前是否在可选清单里」。
// 不借收紧之机顺手关掉一个功能:零行为变更的开箱状态是硬要求。
// defaultAllowAuto 是首次给一档人建范围时「允许 auto」的初值。
//
// ── 为什么恒为 true,而不是沿用上游此刻的可选清单 ──
//
// 老实现是 `_, ok := service.GetUserUsableGroups(userGroup)[autoGroup]`,也就是
// 「上游全局白名单里有没有 auto 键」。那个推导在本站恒为 false —— 白名单是空的 ——
// 于是每一档新建的范围都自动禁掉了 auto,而运营从来没做过这个决定。
//
// auto 本身不放宽任何权限:令牌的 auto 候选会被 FilterUserTokenAutoGroups 按
// 这一档的可选清单再过滤一遍,用户不可能借它用到清单外的模型分组。它决定的只是
// 「这一档的人能不能让令牌在自己已有的几个分组之间按序故障转移」,
// 而项目方拍板要的正是这个能力。要关的那一档,在编辑弹窗里显式关掉即可。
func defaultAllowAuto(string) bool { return true }

// ───────────────── POST /group-matrix/repair-token ─────────────────

type repairTokenReq struct {
	TokenId int `json:"token_id"`
}

// adminRepairToken 把一条孤儿令牌的分组置空。
//
// **只提供单条,不提供批量。** 批量改写 545 行 tokens.group 不可逆,
// 而"用户当初选这个分组是有原因的"这件事系统不知道。
//
// 「置空」是唯一既安全又不猜用户意图的修复:令牌分组为空时 UsingGroup 恒等于
// users.group(middleware/auth.go 保证),立即恢复可用且不改变该用户的可用范围。
// 「改成某个指定分组」需要替用户做选择,风险更高,不做。
func adminRepairToken(c *gin.Context) {
	if !guard.RequireAPI(c, guard.FlagGroupMatrix) {
		return
	}
	var req repairTokenReq
	if err := c.ShouldBindJSON(&req); err != nil || req.TokenId <= 0 {
		badRequest(c, "token_id 非法")
		return
	}
	if model.DB == nil {
		internalError(c, errors.New("主库未就绪"))
		return
	}
	var tk model.Token
	if err := model.DB.Where("id = ?", req.TokenId).Take(&tk).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			notFound(c, "令牌不存在")
			return
		}
		internalError(c, err)
		return
	}
	if tk.Group == "" {
		badRequest(c, "该令牌的分组本来就是空的,无需修复")
		return
	}
	oldGroup := tk.Group
	// 列名必须是**裸名**:model.QyCommonGroupCol() 返回的是已经带引号的串,
	// 交给 GORM 的列位参数会被再引一次(SQLite 上生成 SET ``group``= 直接语法错误,
	// 于是 SQLite 部署上这个唯一的孤儿修复出口一条都修不了)。
	// 裸名交给 GORM,引号规则自动跟着方言走。
	err := model.DB.Model(&model.Token{}).Where("id = ?", tk.Id).
		Update("group", "").Error
	entry := audit.Entry{
		Category: auditCategoryGroupMatrix, Action: auditActionTokenRepair,
		ActorType: qymodel.ActorAdmin, ActorUserId: c.GetInt("id"), ActorName: c.GetString("username"),
		TargetUserId: tk.UserId,
		Reason: fmt.Sprintf("把令牌 %d(用户 %d)的分组由 %q 置空,使其回落到用户分组",
			tk.Id, tk.UserId, oldGroup),
		BeforeSnap: fmt.Sprintf(`{"token_id":%d,"group":%q}`, tk.Id, oldGroup),
		AfterSnap:  fmt.Sprintf(`{"token_id":%d,"group":""}`, tk.Id),
	}
	if err != nil {
		entry.Result = qymodel.ResultFail
		entry.Reason = "孤儿令牌修复失败: " + err.Error() + " | " + entry.Reason
		audit.Write(c, entry)
		internalError(c, err)
		return
	}
	audit.Write(c, entry)
	// 令牌缓存里还留着旧分组,不清掉的话修复要等缓存自然过期才生效 ——
	// 而"修复了但用户还在 403"是最容易被当成修复无效的表现。
	if cErr := model.InvalidateUserTokensCache(tk.UserId); cErr != nil {
		common.SysError("qianye/groupmatrix: 清理令牌缓存失败(修复已落库,最迟随缓存过期生效): " + cErr.Error())
	}
	respond(c, gin.H{"token_id": tk.Id, "old_group": oldGroup, "group": ""})
}

// ─────────────────────────── 审计 ───────────────────────────

func matrixAuditEntry(c *gin.Context, action string, cells []Cell, before any, err error) audit.Entry {
	e := audit.Entry{
		Category: auditCategoryGroupMatrix, Action: action,
		ActorType: qymodel.ActorAdmin, ActorUserId: c.GetInt("id"), ActorName: c.GetString("username"),
		BeforeSnap: snapshotJSON(before),
		AfterSnap:  snapshotJSON(cells),
	}
	grant, revoke, reprice, renote := 0, 0, 0, 0
	for _, cell := range cells {
		switch cell.Action {
		case ActionGrant:
			grant++
		case ActionRevoke:
			revoke++
		case ActionSetNote, ActionClearNote:
			renote++
		default:
			reprice++
		}
	}
	// **撤销单独计数**:它是唯一会打断线上流量的操作类型,不能和改价混进一个数字。
	// 备注同样单独计数:它是一段**面向用户**的文案(显示在建 key 的分组下拉里),
	// 与改价混在一起会让"这次到底改了钱还是改了字"事后分不出来。
	e.Reason = fmt.Sprintf("矩阵改动:放开 %d 项 / 撤销 %d 项 / 改价 %d 项 / 改备注 %d 项",
		grant, revoke, reprice, renote)
	if err != nil {
		e.Result = qymodel.ResultFail
		e.Reason = "失败(已回滚): " + err.Error() + " | " + e.Reason
	}
	return e
}

func writeMatrixFailure(c *gin.Context, action string, cells []Cell, before any, err error) {
	audit.Write(c, matrixAuditEntry(c, action, cells, before, err))
}

func scopeAuditEntry(c *gin.Context, action string, before, after *Scope, prefill, dropped []string) audit.Entry {
	e := audit.Entry{
		Category: auditCategoryGroupMatrix, Action: action,
		ActorType: qymodel.ActorAdmin, ActorUserId: c.GetInt("id"), ActorName: c.GetString("username"),
		BeforeSnap: snapshotJSON(before),
		AfterSnap:  snapshotJSON(after),
	}
	switch {
	case before == nil && after != nil:
		e.Reason = fmt.Sprintf("接管用户分组 %s(mode=%s,allow_auto=%v),预填清单 %d 项:%v",
			after.UserGroup, after.Mode, after.AllowAuto, len(prefill), prefill)
		if len(dropped) > 0 {
			// 上一次撤销接管留下的行。它们不在预填里 = 这一档此刻用不到它们,
			// 留着会让新清单悄悄多出几项,而没有人决定过要放开它们。
			e.Reason += fmt.Sprintf(";同时清掉 %d 条上一次撤销接管留下的残留授权:%v",
				len(dropped), dropped)
		}
	case before != nil && after == nil:
		e.Reason = fmt.Sprintf("撤销接管用户分组 %s —— 它回到上游行为(全局白名单 + 特殊规则 + 补自己)",
			before.UserGroup)
	case before != nil && after != nil:
		e.Reason = fmt.Sprintf("用户分组 %s 的接管模式 %s → %s(allow_auto %v → %v)",
			after.UserGroup, before.Mode, after.Mode, before.AllowAuto, after.AllowAuto)
	}
	return e
}

func writeScopeFailure(c *gin.Context, action string, before, after *Scope, err error) {
	e := scopeAuditEntry(c, action, before, after, nil, nil)
	e.Result = qymodel.ResultFail
	e.Reason = "接管变更失败: " + err.Error() + " | " + e.Reason
	audit.Write(c, e)
}

func snapshotJSON(v any) string {
	if v == nil {
		return ""
	}
	b, err := common.Marshal(v)
	if err != nil {
		return ""
	}
	return string(b)
}

// ─────────────────────────── HTTP 信封 ───────────────────────────

func respond(c *gin.Context, data any) {
	c.JSON(http.StatusOK, gin.H{"success": true, "message": "", "data": data})
}

func respondFail(c *gin.Context, status int, code, msg string) {
	c.JSON(status, gin.H{"success": false, "code": code, "message": msg})
}

func badRequest(c *gin.Context, msg string) {
	respondFail(c, http.StatusBadRequest, "qy_gm_bad_request", msg)
}

func notFound(c *gin.Context, msg string) {
	respondFail(c, http.StatusNotFound, "qy_gm_not_found", msg)
}

// conflict 是"你看的和现在的不是同一份数据"。它与 400 分开:
// 400 是"你写错了",409 是"你没写错,但世界变了,请重新看一眼"。
func conflict(c *gin.Context, msg string) {
	respondFail(c, http.StatusConflict, "qy_gm_conflict", msg)
}

func internalError(c *gin.Context, err error) {
	common.SysError("qianye/groupmatrix: 接口处理失败: " + err.Error())
	respondFail(c, http.StatusInternalServerError, "qy_internal_error", "处理失败,请稍后重试")
}


// dropFromAutoOrder 把一个模型分组从**某一档**的默认 auto 顺序里摘掉。
//
// 与 sweepAutoOrder(删模型分组时扫全表)的区别只是作用域:这里是"这一档
// 不再被授权用它",那里是"它整个不存在了"。两处刻意各写各的 WHERE ——
// 合成一个带可选参数的函数会让调用点读起来像"可能扫全表也可能扫一行"。
func dropFromAutoOrder(tx *gorm.DB, userGroup, modelGroup string) error {
	var scope Scope
	err := tx.Where("user_group = ?", userGroup).Take(&scope).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil
	}
	if err != nil {
		return err
	}
	current := splitAutoOrder(scope.AutoOrder)
	kept := make([]string, 0, len(current))
	for _, g := range current {
		if g != modelGroup {
			kept = append(kept, g)
		}
	}
	if len(kept) == len(current) {
		return nil
	}
	return tx.Model(&Scope{}).Where("user_group = ?", userGroup).
		Update("auto_order", joinAutoOrder(kept)).Error
}
