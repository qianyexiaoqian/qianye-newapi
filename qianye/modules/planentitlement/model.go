package planentitlement

// 余额使用范围。**行不存在 ≡ ScopeUniversal**(= 上游今天的行为),
// 因此升级零变化、回退只需删行。默认值在 newPolicy 里给,不走 GORM default tag。
const (
	// ScopeUniversal 套餐自带的额度池可以用在**任何**模型分组上(上游现行行为)。
	ScopeUniversal = "universal"
	// ScopeRestricted 套餐自带的额度池**只能**用在它绑定的模型分组上。
	//
	// 本次请求的模型分组与绑定集合不符时,这条订阅要从**出资候选**里跳过 ——
	// 不是「余额不足」。余额明明还在,只是用不了,这是另一种状态;混在一起
	// 会让排障与对账同时失去依据。跳过的落点见 CandidateUsable。
	ScopeRestricted = "restricted"
)

// autoGroup 是上游的伪分组名。它永远不能被套餐解锁:
// auto 是"自动选一个分组"的开关而不是一个真的模型分组,而且
// service/group.go 的 auto 候选是 group-keyed 的(见包注释「不参与 auto」)。
const autoGroup = "auto"

const (
	maxGroupNameLen = 64
	maxNoteLen      = 255
	// maxGroupsPerPlan 是一个套餐最多能解锁多少个模型分组。
	//
	// 上界的意义不是"谁会解锁一百个",而是别让一次脚本误操作把整张倍率表
	// 灌进一个套餐里 —— 那种配置在页面上看起来只是列表长了一点。
	maxGroupsPerPlan = 64
)

// PlanGrant 是一条「套餐 → 解锁的模型分组」的授权。
//
// ═══════════════ 与 groupmatrix.Grant 刻意同构 ═══════════════
//
// 同一个概念(成员资格授权),不同的授权主体:一个按用户分组授权,一个按套餐
// 授权。两张表的列、约束、以及下面三条「不存什么」的理由逐字相同 ——
// 长得一样是有意的,读过一张就读懂了另一张。
//
// **行存在 = 解锁,不加 enabled 列**:「删了」与「禁用了」的区分价值不足以抵消
// 再开一个三态的成本,而三态歧义正是本仓反复栽跟头的形状。撤销时间由审计回答。
//
// **不存倍率**:分组倍率的唯一真相源是上游 options.GroupGroupRatio /
// options.GroupRatio。套餐**不携带倍率** —— 允许之后,倍率就从
// (用户分组, 模型分组) 的纯函数变成 (userId, 模型分组, 此刻持有的套餐集合) 的
// 函数,而最要命的一条是:**套餐到期的那一秒倍率会跳变,预扣与结算跨越那一秒时
// 会用两个不同的倍率结算同一笔请求**。这条没有便宜的修法。
//
// **不存描述文案**:一律走 setting.GetUsableGroupDescription,与上游同源。
//
// PlanId 是主库 subscription_plans.id 的**软引用**:扩展库是另一个实例,
// 不能建外键。套餐被删时由删除路径清理(见 DeleteByPlans)。
type PlanGrant struct {
	Id int64 `json:"id" gorm:"primaryKey;autoIncrement"`

	PlanId int `json:"plan_id" gorm:"column:plan_id;not null;uniqueIndex:uk_qy_pgrant_pair,priority:1"`
	// ModelGroup 精确匹配,**不折叠大小写**。理由与 groupmatrix.Grant 相同:
	// 倍率侧 ratio_setting.GetGroupGroupRatio 是精确 map 查找、在 3 条计费路径里、
	// 我们无权改。成员资格折叠而倍率不折叠会造出「拿到访问权、倍率却回落兜底」——
	// 管理端显示 0.3、实际按 1.0 扣费、零告警。
	ModelGroup string `json:"model_group" gorm:"column:model_group;type:varchar(64);not null;uniqueIndex:uk_qy_pgrant_pair,priority:2"`

	SortOrder int `json:"sort_order" gorm:"not null;default:0"`

	OperatorId int   `json:"operator_id" gorm:"not null;default:0"`
	CreatedAt  int64 `json:"created_at" gorm:"not null;default:0"`
	UpdatedAt  int64 `json:"updated_at" gorm:"not null;default:0"`
}

func (PlanGrant) TableName() string { return "qy_plan_group_grants" }

// PlanBalancePolicy 是套餐额度池的「使用范围」。
//
// ═══════════ 为什么是单独一张表,而不是塞进 PlanGrant 的行里 ═══════════
//
// scope 是**套餐级**属性。放进 grants 行会出现「三行说了三个不同的 scope」
// 这种无法解释的状态 —— 而它直接决定一笔钱从哪个池子里扣,不允许有无法解释的
// 中间态。单独一张表让「一个套餐一个 scope」成为主键约束,而不是一条约定。
//
// **行不存在 ≡ universal**:空表起步 = 现有套餐全部通用 = 上游今天的行为。
type PlanBalancePolicy struct {
	PlanId int `json:"plan_id" gorm:"column:plan_id;primaryKey"`
	// BalanceScope ∈ {universal, restricted}。非法值一律按 universal 处理并告警
	// (见 buildSnapshot):脏值的方向必须倒向「与上游一致」,而不是倒向
	// 「用户的余额突然用不了了」。
	BalanceScope string `json:"balance_scope" gorm:"type:varchar(16);not null"`

	Note string `json:"note" gorm:"type:varchar(255);not null;default:''"`

	OperatorId int   `json:"operator_id" gorm:"not null;default:0"`
	UpdatedAt  int64 `json:"updated_at" gorm:"not null;default:0"`
}

func (PlanBalancePolicy) TableName() string { return "qy_plan_balance_policy" }

// newPolicy 构造一条使用范围行。默认值在这里给,不走 GORM 的 default tag
// (MySQL 与 PostgreSQL 对默认值的归一化不同,会让 AutoMigrate 每次重启都
// 发一条 ALTER TABLE,见 AGENTS.md)。
func newPolicy(planId int, scope, note string, operatorId int, now int64) *PlanBalancePolicy {
	if scope != ScopeRestricted {
		scope = ScopeUniversal
	}
	return &PlanBalancePolicy{
		PlanId:       planId,
		BalanceScope: scope,
		Note:         truncate(note, maxNoteLen),
		OperatorId:   operatorId,
		UpdatedAt:    now,
	}
}

func truncate(s string, max int) string {
	if len(s) <= max {
		return s
	}
	return s[:max]
}
