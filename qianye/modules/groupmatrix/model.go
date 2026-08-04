package groupmatrix

// 接管模式。行存在即 managed,Mode 决定它是"配好了但先观察"还是"真的生效"。
const (
	// ModeShadow 清单已配置但**不生效**:读侧恒等返回上游,写侧只记录不阻断。
	//
	// 它不只是灰度的一档,还是读写两侧严格性的**统一开关**:没有它,写入侧校验
	// 就只有开和关,无法在观察期先记录不阻断。
	ModeShadow = "shadow"
	// ModeEnforce 清单是权威的:该用户分组能选哪些模型分组完全由 grants 决定。
	ModeEnforce = "enforce"
)

// autoGroup 是上游的伪分组名。它永远不能出现在 grants 里 ——
// service.IsUserSelectableGroup 本来就显式拒绝它,放进清单只会让人
// 误以为它有用。是否把 auto 注回可选 map 由 Scope.AllowAuto 显式控制。
const autoGroup = "auto"

const (
	maxGroupNameLen = 64
	maxNoteLen      = 255
)

// Scope 是"这个用户分组已被扩展接管"的登记行。
//
// ══════════════════ 为什么需要一张单独的表头表 ══════════════════
//
// 它存在的**唯一**理由,是把「清单是空的(一个模型分组都不许用)」与
// 「没配过(走上游)」分开。
//
// 靠 grants 行数推断就是本仓刚修过的"零值与未配置不可区分"缺陷的第二次发作,
// 而这一次的后果是整组用户 403。空清单是**合法且危险**的配置(隔离组、封禁组),
// 必须可表达 —— 不能因为它罕见就让它无法表达。
//
// 行不存在 = inherit = 上游说了算(与改动前逐位一致,这是回退能力的基础)。
type Scope struct {
	// UserGroup 是 users.group 的口径。**精确匹配,不做任何归一化。**
	//
	// 不折叠大小写是刻意的:倍率侧 ratio_setting.GetGroupGroupRatio 是精确 map
	// 查找、在 3 条计费路径里、我们无权改。成员资格折叠而倍率不折叠会造出
	// 「users.group=VIP 命中 vip 的清单拿到访问权,倍率却回落兜底」——
	// 管理端显示 0.3、实际按 1.0 扣费、零告警。那正是本轮要消灭的形状,
	// 不能一边修一边再造一个。大小写近似项在保存校验与 preview 里列成 warning。
	UserGroup string `json:"user_group" gorm:"column:user_group;type:varchar(64);primaryKey"`

	Mode string `json:"mode" gorm:"type:varchar(16);not null"`

	// AllowAuto 决定是否把 auto 伪分组注回可选 map。
	//
	// 刻意不写 gorm:"default:true":MySQL 与 PostgreSQL 对布尔默认值的归一化不同,
	// 会让 AutoMigrate 每次重启都发一条 ALTER TABLE(见 AGENTS.md)。
	// 默认值在 Go 侧的 newScope 里给。
	AllowAuto bool `json:"allow_auto" gorm:"not null"`

	Note string `json:"note" gorm:"type:varchar(255);not null;default:''"`

	OperatorId int   `json:"operator_id" gorm:"not null;default:0"`
	UpdatedAt  int64 `json:"updated_at" gorm:"not null;default:0"`
}

func (Scope) TableName() string { return "qy_group_scopes" }

// Grant 是一条「用户分组 → 可选模型分组」的授权。
//
// **行存在 = 可选。刻意不加 enabled 列**:「删了」与「禁用了」的区分价值
// 不足以抵消再开一个三态的成本,而三态歧义正是本仓反复栽跟头的形状。
// 撤销时间由审计记录回答,不由数据列回答。
//
// **不存倍率**:分组倍率的唯一真相源是上游 options.GroupGroupRatio。
// 任何镜像列都必须有同步机制,而同步失败的表现是"管理端显示 A、热路径乘 B"——
// 与本轮 grouppricing 正在修的缺陷完全同形。
//
// **不存描述文案**:一律走 setting.GetUsableGroupDescription,与上游同源。
type Grant struct {
	Id int64 `json:"id" gorm:"primaryKey;autoIncrement"`

	UserGroup  string `json:"user_group" gorm:"column:user_group;type:varchar(64);not null;uniqueIndex:uk_qy_ggrant_pair,priority:1"`
	ModelGroup string `json:"model_group" gorm:"column:model_group;type:varchar(64);not null;uniqueIndex:uk_qy_ggrant_pair,priority:2"`

	SortOrder int `json:"sort_order" gorm:"not null;default:0"`

	OperatorId int   `json:"operator_id" gorm:"not null;default:0"`
	CreatedAt  int64 `json:"created_at" gorm:"not null;default:0"`
	UpdatedAt  int64 `json:"updated_at" gorm:"not null;default:0"`
}

func (Grant) TableName() string { return "qy_group_grants" }

// WriteDeny 记录影子期**令牌写入侧**本可拒绝的一次分组变更。
//
// 只有这一档来源,所以不设 source 列。
//
// 读侧(middleware/auth.go)的影子拒绝在那个挂载点上**记不出来**:
// 上游的调用形式是 `GetUserUsableGroups(userGroup)[tokenGroup]` —— hook 只拿得到
// userGroup 与整张 map,拿不到被查询的那个 key。硬做出来的"本可拒绝数"是假的,
// 而且列表类调用(模型广场、价格表)会与鉴权调用混进同一个计数器,
// 一个开着模型广场的管理员就能把它刷成几千。
//
// 影子期读侧的证据来源因此是日志库聚合(preview 的 L2 块):精确、可回溯、
// 能区分调用性质。这个不对称是有意的,不是漏了一半。
type WriteDeny struct {
	Id int64 `json:"id" gorm:"primaryKey;autoIncrement"`

	UserGroup  string `json:"user_group" gorm:"column:user_group;type:varchar(64);not null;uniqueIndex:uk_qy_gdeny_pair,priority:1"`
	ModelGroup string `json:"model_group" gorm:"column:model_group;type:varchar(64);not null;uniqueIndex:uk_qy_gdeny_pair,priority:2"`

	Count     int64 `json:"count" gorm:"not null;default:0"`
	FirstSeen int64 `json:"first_seen" gorm:"not null;default:0"`
	LastSeen  int64 `json:"last_seen" gorm:"not null;default:0"`

	SampleUserId int `json:"sample_user_id" gorm:"not null;default:0"`
}

func (WriteDeny) TableName() string { return "qy_group_write_denies" }

// newScope 构造一条接管登记行。布尔默认值在这里给,不走 GORM 的 default tag。
func newScope(userGroup, mode string, allowAuto bool, note string, operatorId int, now int64) *Scope {
	return &Scope{
		UserGroup:  userGroup,
		Mode:       mode,
		AllowAuto:  allowAuto,
		Note:       truncate(note, maxNoteLen),
		OperatorId: operatorId,
		UpdatedAt:  now,
	}
}

func truncate(s string, max int) string {
	if len(s) <= max {
		return s
	}
	return s[:max]
}
