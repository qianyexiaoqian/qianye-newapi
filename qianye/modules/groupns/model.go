package groupns

// model.go —— 两张**登记表**的定义。
//
// ══════════════════════ 登记表是什么、更重要的是不是什么 ══════════════════════
//
// 是:管理端写校验的判据、UI 候选的来源、「用户分组的默认模型分组」的存放处。
// 不是:路由准入、鉴权准入、倍率真相源。
//
// **fail-open 是硬约束。** abilities / users 里出现、而登记表里没有的名字叫
// 「未登记」,它在管理端以告警行出现,**不遮断任何请求**。理由很直接:
// 漏登记一个名字 = 那一整组用户 503。一张给人看的表不该有让站点下线的能力。
//
// ══════════════════════ 一个字节的倍率都不存 ══════════════════════
//
// 真相源恒为 options.GroupRatio / options.GroupGroupRatio。这条与
// qianye/modules/groupmatrix/store.go、qianye/modules/planentitlement 的包注释
// 逐字同源:任何镜像列都需要同步机制,而同步失败的表现是
// **「管理端显示 A、热路径乘 B」** —— 那正是本轮在消灭的形状。
//
// ══════════════════════ 名字仍然是唯一连接键 ══════════════════════
//
// 刻意**不引入数字主键**。abilities 是 (group, model, channel_id) 的三列联合主键
// 物化路由表,channels.group 是逗号串,logs.group 是历史对账的冻结值 ——
// 换主键要一次性改写五张表 + 全部 GORM 查询 + 缓存 + distributor,不可回退、
// 不可灰度,而且历史账单口径会断裂。代价必须明说而不是假装解决了:
// **同名冲突仍然可以被制造出来**,只能靠登记表 + 分列 UI + 巡检压住。

import (
	"unicode/utf8"

	"github.com/QuantumNous/new-api/common"
)

const (
	// maxGroupNameLen 是登记表 name 主键的宽度,单位是**字符**,不是字节。
	//
	// 两张登记表的 name 是 varchar(64),而 MySQL/PostgreSQL 的 varchar(N) 计的是
	// 字符;上游 users.group / abilities.group / channels.group 也都是 varchar(64)。
	// 按字节裁会把一个 22 个汉字的合法分组名(66 字节)砍成 21 个字 + 半个 UTF-8
	// 序列,MySQL 在 STRICT_TRANS_TABLES 下以 Error 1366 拒绝**整条** INSERT ——
	// 而全部新行在同一条 CreateInBatches 里,于是一个坏名字让整次回填失败并 return,
	// 两张登记表永远是空的,而唯一的症状是一行 SysError。
	maxGroupNameLen = 64
)

// 默认模型分组的三态。**用显式枚举承载,而不是用行存在性或空串。**
//
// ── 为什么必须是三态,以及为什么不能用空串 ──
//
// 需要表达的是三件不同的事:
//
//	inherit  还没配 → UsingGroup = users.group,**逐位等于今天**
//	pin      配了一个具体的模型分组
//	deny     显式拒绝兜底(隔离组 / 封禁组):没有令牌分组就别想发请求
//
// 登记表对每个观测到的用户分组**自动回填一行**,于是「行存不存在」不再携带信息;
// 而空串一旦兼任"还没配"与"就是不给兜底",就复现了本仓已经栽过三次的
// 「零值与未配置不可区分」—— 那三次的错误方向分别是整组用户 403 与整组用户全放行,
// 而且都静默。枚举同时满足两边。
const (
	DefaultModeInherit = "inherit"
	DefaultModePin     = "pin"
	DefaultModeDeny    = "deny"
)

// ValidDefaultMode 报告 mode 是不是三个合法取值之一。
// 非法值一律按 inherit 处理(见 buildSnapshot):未知配置必须退化成"今天的行为"。
func ValidDefaultMode(mode string) bool {
	return mode == DefaultModeInherit || mode == DefaultModePin || mode == DefaultModeDeny
}

// ModelGroup 是一条模型分组登记。
//
// 口径:channels.group / abilities.group / relayInfo.UsingGroup 说的都是它。
// 它回答的是「这一次请求去哪个渠道池子」。
type ModelGroup struct {
	// Name 精确匹配,**不做任何归一化**(不折叠大小写、不 trim 内部空白)。
	//
	// 倍率侧 ratio_setting 是精确 map 查找且在三条计费路径上。成员资格折叠而倍率
	// 不折叠,就会造出「登记表命中拿到访问权、倍率却回落兜底」——
	// 管理端显示 0.3、实际按 1.0 扣费、零告警。近似名一律列 warning,不替人折叠。
	Name string `json:"name" gorm:"column:name;type:varchar(64);primaryKey"`

	DisplayName string `json:"display_name" gorm:"type:varchar(64);not null;default:''"`
	Note        string `json:"note" gorm:"type:varchar(255);not null;default:''"`

	// Enabled=false **只在可见性层遮断**(下拉、可选清单、新建校验),
	// 绝不触碰 abilities、绝不影响路由。路由层遮断意味着一次误点即全站 503。
	//
	// 刻意不写 gorm:"default:true":MySQL 与 PostgreSQL 对布尔默认值的归一化不同,
	// 会让 AutoMigrate 每次重启都发一条 ALTER TABLE(见 AGENTS.md)。默认值在 Go 侧给。
	Enabled bool `json:"enabled" gorm:"not null"`

	SortOrder int `json:"sort_order" gorm:"not null;default:0"`

	OperatorId int   `json:"operator_id" gorm:"not null;default:0"`
	CreatedAt  int64 `json:"created_at" gorm:"not null;default:0"`
	UpdatedAt  int64 `json:"updated_at" gorm:"not null;default:0"`
}

func (ModelGroup) TableName() string { return "qy_model_groups" }

// UserGroup 是一条用户分组登记。
//
// 口径:users.group 说的是它,而且**只**说它。它回答的是「这个人是谁」。
type UserGroup struct {
	// Name 精确匹配,理由同 ModelGroup.Name。
	Name string `json:"name" gorm:"column:name;type:varchar(64);primaryKey"`

	DisplayName string `json:"display_name" gorm:"type:varchar(64);not null;default:''"`
	Note        string `json:"note" gorm:"type:varchar(255);not null;default:''"`

	Enabled   bool `json:"enabled" gorm:"not null"`
	SortOrder int  `json:"sort_order" gorm:"not null;default:0"`

	// DefaultMode ∈ {inherit, pin, deny}。回填后的初始态恒为 inherit。
	//
	// **这一列是「空分组令牌 503」的解法。** 令牌没有指定分组时,上游
	// middleware/auth.go 让 UsingGroup = users.group,再拿它去 abilities 选渠道;
	// 用户分组一旦不再兼作模型分组就没有渠道,表现是
	// 「503 No available channel for model X under group <用户分组>」。
	DefaultMode string `json:"default_mode" gorm:"type:varchar(16);not null;default:'inherit'"`

	// DefaultModelGroup 只在 DefaultMode == pin 时有意义。
	DefaultModelGroup string `json:"default_model_group" gorm:"type:varchar(64);not null;default:''"`

	OperatorId int   `json:"operator_id" gorm:"not null;default:0"`
	CreatedAt  int64 `json:"created_at" gorm:"not null;default:0"`
	UpdatedAt  int64 `json:"updated_at" gorm:"not null;default:0"`
}

func (UserGroup) TableName() string { return "qy_user_groups" }

// newModelGroup / newUserGroup 在 Go 侧给布尔默认值,不走 GORM 的 default tag。
//
// **名字一律原样存,永不裁剪。** 裁剪出来的行是一条"幽灵登记":它的主键与源名字
// 永远不相等,于是 haveModel[全名] 恒 false ⇒ 每次回填都重新尝试插入 ⇒ 回填
// 不幂等、Added 计数永远不为零;而 snapshot.UserGroups[全名] 与
// `Where("name = ?", 全名)` 又永远 miss ⇒ 那一行既配不上也读不到。
// 过长的名字由 registrableGroupName 在调用点直接**跳过并上报**(见 store.go)。
func newModelGroup(name string, operatorId int, now int64) *ModelGroup {
	return &ModelGroup{
		Name: name, Enabled: true,
		OperatorId: operatorId, CreatedAt: now, UpdatedAt: now,
	}
}

func newUserGroup(name string, operatorId int, now int64) *UserGroup {
	return &UserGroup{
		Name: name, Enabled: true,
		// 回填出来的行**必须**是 inherit:S0 上线当天零行为变化的唯一来源。
		DefaultMode: DefaultModeInherit,
		OperatorId:  operatorId, CreatedAt: now, UpdatedAt: now,
	}
}

// registrableGroupName 报告一个观测到的名字能不能安全地写进登记表。
//
// 判据是**字符数**(见 maxGroupNameLen)。超限时返回 false —— 调用方跳过它并把它
// 列进 BackfillResult.SkippedNames,而不是裁一刀塞进去。理由见 newModelGroup:
// 裁剪产出的行不可用、不幂等,而且在 MySQL 严格模式下多数情况直接让整批插入失败。
//
// 空串同样返回 false:它不是一个分组。
func registrableGroupName(name string) bool {
	return name != "" && utf8.RuneCountInString(name) <= maxGroupNameLen
}

func now() int64 { return common.GetTimestamp() }
