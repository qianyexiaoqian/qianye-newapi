package groupmatrix

import "strings"

// 接管模式。**行存在即生效**,不再有第二档。
const (
	// ModeEnforce 清单是权威的:该用户分组能选哪些模型分组完全由 grants 决定。
	//
	// 它是 mode 列现在唯一的合法取值。列本身保留(而不是 DROP)有两个理由:
	// 跨三种数据库删列各有各的坑,而且留着它,存量行迁移前后都能被解释。
	ModeEnforce = "enforce"

	// ModeShadow 是**已下线**的第三档:清单已配、但一个字节都不生效。
	//
	// Deprecated: 仅用于把存量行迁移成 enforce(见 migrateShadowScopesToEnforce)。
	// 任何读取路径都不得再判它 —— 判了就等于把"配了不生效"又放回来。
	//
	// 下线理由:它在本站是空转的。全局白名单 options.UserUsableGroups 是空的,
	// 影子期用户本来就几乎什么都选不到,「先观察、现状不变」这个前提根本不成立;
	// 保护的是一个不存在的风险,代价却是运营勾完模型分组、保存成功、刷新之后
	// 什么都没发生。项目方拍板:编辑用户分组的模型分组立即生效,
	// 令牌被挡就挡,提醒一下即可。
	ModeShadow = "shadow"
)

// autoGroup 是上游的伪分组名。它永远不能出现在 grants 里 ——
// service.IsUserSelectableGroup 本来就显式拒绝它,放进清单只会让人
// 误以为它有用。是否把 auto 注回可选 map 由 Scope.AllowAuto 显式控制。
const autoGroup = "auto"

const (
	maxGroupNameLen = 64
	maxNoteLen      = 255
)

// Scope 是"这个用户分组**已经设定过范围**"的登记行。
//
// ══════════════════ 三态,以及为什么需要一张单独的表头表 ══════════════════
//
//	行不存在            → 未设定范围 → 全部模型分组可用,各按自己的兜底倍率。
//	                      实现上是**逐位返回上游**(hook 返回入参那一张 map 的指针)。
//	行存在 + 非空 grants → 权威清单:只可用清单里的模型分组,不叠加全局白名单、
//	                      不应用 +:/-: 差分、不无条件补自己。
//	行存在 + 空 grants   → 一个模型分组都不可用(隔离组 / 封禁组 / 待配置组)。
//
// 靠 len(grants)==0 推断只能区分前两种,第一种与第三种会塌成同一个字节 ——
// 那是本仓已经修过三次的"零值与未配置不可区分",而这一次两个错误方向分别是
// **整组用户 403** 与 **整组用户全放行**,都静默。所以表头表不能省。
//
// ══════════════════ 行**只能由管理端写出来** ══════════════════
//
// 上一轮有一个「新分组默认全遮断」的后台任务会自动建 scope 行,本轮已整体撤销
// (口径反转:未设定范围现在意味着全部可用)。除 api_admin.go 的管理端 handler
// 之外,任何路径都不得创建 scope 行 —— 由 TestNoAutoScopeCreation 的 AST 守卫钉死。
// 没有那条守卫,半年后有人"顺手"加回一个自动接管,这次撤销就白做了。
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

	// AutoOrder 是这一档人**默认**的 auto 分组尝试顺序,逗号分隔的模型分组名。
	//
	// ── 它替代的是什么 ──
	//
	// 上游只有一份**全站共用**的 options.AutoGroups。于是"auto 先试哪个池子"
	// 对所有用户分组一视同仁 —— 而这恰恰是最该按档区分的东西:付费档该先试
	// 独占池,免费档该先试免费池。项目方拍板改成按用户分组单独配。
	//
	// ── 空 = 回落全局清单,不是"一个都不试" ──
	//
	// 空串必须表示"这一档没单独配,沿用全站那份"。反过来(空 = 空清单)会让
	// 每一档新建的范围都自动把 auto 变成一个必然失败的选项,而运营从没做过
	// 这个决定 —— 与 allow_auto 当初那个 artifact 是同一个形状。
	//
	// ── 它只是**默认值** ──
	//
	// 令牌自己存了有序清单时走令牌那一份(service.GetRequestAutoGroups),
	// 这里这份只在令牌选择"继承"时生效。两者都会再被当前权限过滤一遍。
	//
	// 存逗号分隔而不是 JSON:成员是模型分组名,而模型分组名的合法字符集里
	// 没有逗号(saveModelGroup 校验),因此不存在转义问题;而一个纯文本列
	// 在三种数据库上的行为完全一致,不必操心 JSON 列的方言差异。
	AutoOrder string `json:"auto_order" gorm:"type:varchar(1024);not null;default:''"`

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
// **存的是「这一格」的描述文案,不是那个模型分组的通用文案**:见 Note。
type Grant struct {
	Id int64 `json:"id" gorm:"primaryKey;autoIncrement"`

	UserGroup  string `json:"user_group" gorm:"column:user_group;type:varchar(64);not null;uniqueIndex:uk_qy_ggrant_pair,priority:1"`
	ModelGroup string `json:"model_group" gorm:"column:model_group;type:varchar(64);not null;uniqueIndex:uk_qy_ggrant_pair,priority:2"`

	// Note 是「**这个用户分组**看**这个模型分组**时」的说明文案。
	//
	// ══════════════ 它是说明文案阶梯的最上面一级,不是第四份真相 ══════════════
	//
	// 站内此前有两处文案来源,而"哪一份生效"从来没人说得清:
	//
	//	options.UserUsableGroups[模型分组]  全局白名单的 value(上游原生)
	//	qy_model_groups.note               模型分组的默认备注(覆盖上一条)
	//
	// 本列是第三处,而且是**唯一带用户分组维度**的一处。三者合并成一条阶梯:
	//
	//	grant.note(本列,若非空)
	//	  > qy_model_groups.note
	//	    > options.UserUsableGroups[模型分组]
	//	      > 模型分组名
	//
	// 下面两级已经由 setting.QyDescribeGroup 统一持有(见 setting/qy_groupnote_export.go),
	// 本列只在**权威清单生效**的那一档(scope.mode == enforce)由 Resolve 叠在最上面。
	// 阶梯而不是拼接:拼接出来的文案没有唯一作者,而且长度不可控 ——
	// 它要显示在用户建 key 的分组下拉里。
	//
	// **为什么不做成"必须显式覆盖"的三态**:空串在这里没有歧义。
	// 「这一格不写备注」与「这一格要显示成空白」在用户界面上是同一件事
	// (下拉里那一行总要有字,空白就退回下一级),所以空串 = 未填 = 回落,
	// 不需要 *string。这与倍率的 null/0 区分不同:倍率的 0 是一个**能改变账单**的值。
	Note string `json:"note" gorm:"type:varchar(255);not null;default:''"`

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
func newScope(userGroup, mode string, allowAuto bool, autoOrder []string, note string, operatorId int, now int64) *Scope {
	return &Scope{
		UserGroup:  userGroup,
		Mode:       mode,
		AllowAuto:  allowAuto,
		AutoOrder:  joinAutoOrder(autoOrder),
		Note:       truncate(note, maxNoteLen),
		OperatorId: operatorId,
		UpdatedAt:  now,
	}
}

// joinAutoOrder / splitAutoOrder 是 AutoOrder 列的唯一编解码入口。
//
// 去重并丢弃空串:重复项会让 auto 在同一个池子上试两次(第二次必然同样失败),
// 而空串会变成一个查不到任何渠道的分组名。两者都只会在故障转移时白白多烧一次
// 上游超时。
func joinAutoOrder(groups []string) string {
	seen := make(map[string]struct{}, len(groups))
	out := make([]string, 0, len(groups))
	for _, g := range groups {
		g = strings.TrimSpace(g)
		if g == "" {
			continue
		}
		if _, dup := seen[g]; dup {
			continue
		}
		seen[g] = struct{}{}
		out = append(out, g)
	}
	return strings.Join(out, ",")
}

func splitAutoOrder(raw string) []string {
	if strings.TrimSpace(raw) == "" {
		return nil
	}
	parts := strings.Split(raw, ",")
	out := make([]string, 0, len(parts))
	for _, p := range parts {
		if p = strings.TrimSpace(p); p != "" {
			out = append(out, p)
		}
	}
	return out
}

func truncate(s string, max int) string {
	if len(s) <= max {
		return s
	}
	return s[:max]
}
