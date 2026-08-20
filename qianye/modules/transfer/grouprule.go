package transfer

import (
	"fmt"
	"sort"
	"strings"

	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/qianye/db"
	"github.com/QuantumNous/new-api/qianye/groupname"
	"github.com/QuantumNous/new-api/qianye/modules/groupns"
	"github.com/QuantumNous/new-api/setting"
	"github.com/QuantumNous/new-api/setting/ratio_setting"
)

// grouprule.go —— "哪个用户组可以转给哪些用户组"的运营规则。
//
// # 为什么存扩展库而不是 YAML
//
// 这是运营会反复调整的策略(拉一个分组进白名单、临时冻结某个分组的转出),
// 每次都改 YAML + 重启不可接受。而且本扩展已经四次栽在"配置项定义了却没有
// 消费方"上,新增 YAML 键的风险收益比很差 —— 本文件因此**不引入任何 YAML 配置**。
//
// # 为什么自建表而不是塞进 qy_settings
//
// qy_settings 是 (scope, k) → 字符串的 KV,适合"一个数字一个开关"。分组规则是
// 一张有结构的清单:增删改要能定位到行、并发编辑不能互相覆盖、每一次改动都要
// 能在审计里看到"改的是哪一条、从什么变成什么"。把整张清单编码成一个 JSON 值
// 塞进一个 KV,两个管理员同时点保存就会丢掉其中一个人的修改。
//
// # 判定口径
//
//	规则表为空                → 完全不限制(向后兼容:升级不改变任何既有行为)
//	from_group 精确命中       → 用这条
//	否则命中 "*" 兜底规则      → 用兜底
//	都没有                    → 不限制
//
// 一个 from_group 至多一条规则(唯一索引),因此没有优先级、没有"先匹配到哪条"
// 的顺序依赖 —— "谁能转给谁"必须是看一眼就能确定的,不能取决于扫描顺序。

// 规则策略。
const (
	// GroupPolicyAllowAll 该分组可转给任意分组,包括将来新增的分组。
	GroupPolicyAllowAll = "allow_all"
	// GroupPolicyDenyAll 该分组禁止发起划转。
	GroupPolicyDenyAll = "deny_all"
	// GroupPolicyAllowList 白名单:只能转给 ToGroups 里列出的分组。
	GroupPolicyAllowList = "allow_list"
	// GroupPolicyDenyList 黑名单:除 ToGroups 里列出的分组之外都可以转。
	GroupPolicyDenyList = "deny_list"
)

const (
	// groupWildcard 是兜底规则的 from_group,匹配所有没有专属规则的分组。
	groupWildcard = "*"
	// groupSelfToken 在 to_groups 里代表"与发起方相同的分组"。
	//
	// 有了它,"只能转给同组"就是 allow_list + [@self],"禁止组内互转"就是
	// deny_list + [@self] —— 两种最常见的形态不需要各加一个布尔开关,
	// 也不需要运营为每个分组各写一条只有自己名字的规则。
	groupSelfToken = "@self"
	// defaultGroupName 与主库 users.group 的列默认值一致。
	defaultGroupName = "default"
)

// 写入侧的硬上限。规则表每次划转都会被全量读一遍,不设上界的话一次误操作
// (脚本批量插入)会把冷路径拖成慢查询,而分组本身就是个位数量级的东西。
const (
	maxGroupNameLen    = 64
	maxGroupRuleCount  = 200
	maxToGroupEntries  = 64
	maxToGroupsLen     = 1024
	maxGroupRuleRemark = 255
)

// GroupRule 是一条分组划转规则:from_group 的用户按 Policy + ToGroups 决定能转给谁。
type GroupRule struct {
	Id int64 `json:"id" gorm:"primaryKey;autoIncrement"`

	// FromGroup 是发起方分组,**一律以归一化后的形式存储**
	// (groupname.Effective:去空白 + 小写);groupWildcard 表示兜底规则。
	// 唯一索引是本模块可读性的支点:一个分组只能有一条规则。
	//
	// 列上刻意没有写 COLLATE:扩展库是 MySQL,建表继承的库默认排序规则
	// (5.7 general_ci / 8.0 0900_ai_ci)大小写不敏感,而 byGroup 是 Go map
	// 的精确匹配。这个错位在这里比费率表严重 —— 一条 deny_all 配在 VIP 上、
	// 用户的 users.group 是 vip,map 查不到就落到兜底规则,没有兜底就是完全
	// 不设防。消掉它的办法是让写入与判定都只产生小写键(理由与残留边界见
	// groupname 包),而不是加一条 auto_migrate=false 的部署不会执行的 ALTER。
	FromGroup string `json:"from_group" gorm:"type:varchar(64);not null;uniqueIndex:uk_qy_tr_grp_from"`

	Policy string `json:"policy" gorm:"type:varchar(16);not null;default:'allow_list'"`

	// ToGroups 是逗号分隔的目标分组名单,可含 groupSelfToken。
	// 存字符串而不是关联表:名单是"一条规则的一部分",拆表之后编辑一条规则
	// 就变成跨两张表的事务,而它至多几十个短字符串。
	ToGroups string `json:"to_groups" gorm:"type:varchar(1024);not null;default:''"`

	// Enabled 为 false 时视同该规则不存在,因此会落到兜底规则(若有)上。
	Enabled bool `json:"enabled" gorm:"not null"`

	Remark string `json:"remark" gorm:"type:varchar(255);not null;default:''"`

	CreatedAt int64 `json:"created_at" gorm:"not null;default:0"`
	UpdatedAt int64 `json:"updated_at" gorm:"not null;default:0"`
	UpdatedBy int   `json:"updated_by" gorm:"not null;default:0"`
}

func (GroupRule) TableName() string { return "qy_transfer_group_rules" }

// groupRuleSet 是一次请求内冻结的规则视图。
//
// 冻结是刻意的:一笔划转要在受理阶段与主库锁内各判一次,两处若各读各的,
// 运营在这中间改一次规则就会出现"受理放行、锁内拒绝"—— 用户白吃一次冷却
// 与风控预占,而他提交时看到的规则确实是允许的。
type groupRuleSet struct {
	byGroup  map[string]*GroupRule
	fallback *GroupRule
}

// buildGroupRuleSet 把库里的行整理成可判定的视图。停用的规则直接丢弃。
func buildGroupRuleSet(rows []GroupRule) groupRuleSet {
	set := groupRuleSet{byGroup: make(map[string]*GroupRule, len(rows))}
	for i := range rows {
		r := &rows[i]
		if !r.Enabled {
			continue
		}
		if strings.TrimSpace(r.FromGroup) == groupWildcard {
			set.fallback = r
			continue
		}
		set.byGroup[normalizeGroupName(r.FromGroup)] = r
	}
	return set
}

// ruleFor 返回作用于 fromGroup 的规则,nil 表示不限制。
func (s groupRuleSet) ruleFor(fromGroup string) *GroupRule {
	if r, ok := s.byGroup[normalizeGroupName(fromGroup)]; ok {
		return r
	}
	return s.fallback
}

// allowsGroup 判断 fromGroup 的用户能否转给 toGroup 的用户。
//
// 没有任何规则命中时返回 nil —— 这是向后兼容的落点:升级到本版本、
// 规则表还是空的部署,行为与升级前逐字节相同。
func (s groupRuleSet) allowsGroup(fromGroup, toGroup string) error {
	rule := s.ruleFor(fromGroup)
	if rule == nil {
		return nil
	}
	from := normalizeGroupName(fromGroup)
	to := normalizeGroupName(toGroup)

	switch rule.Policy {
	case GroupPolicyAllowAll:
		return nil
	case GroupPolicyDenyAll:
		return errGroupSendBlocked
	case GroupPolicyAllowList:
		if matchGroupList(rule.ToGroups, from, to) {
			return nil
		}
		return errGroupTargetDenied
	case GroupPolicyDenyList:
		if matchGroupList(rule.ToGroups, from, to) {
			return errGroupTargetDenied
		}
		return nil
	default:
		// 未知策略一律拒绝。能走到这里只有一种可能:有人绕过接口直接改库把
		// policy 列写坏了。静默放行会让一条本意是"限制"的规则变成完全不设防,
		// 而这条规则的存在本身就说明运营需要限制。
		return errGroupTargetDenied
	}
}

// matchGroupList 判断 to 是否落在名单里。groupSelfToken 代表"与发起方同组"。
func matchGroupList(raw, from, to string) bool {
	for _, entry := range parseGroupList(raw) {
		if entry == groupSelfToken {
			if to == from {
				return true
			}
			continue
		}
		if entry == to {
			return true
		}
	}
	return false
}

// enforceGroupPolicy 是两个判定点共用的唯一入口。
//
// 参数刻意是主库的 *model.User 而不是两个分组字符串:调用点必须以"这一刻从
// users 行读到的 group"为准,把取值这一步留在函数外面,迟早会有一处传进
// 缓存里的旧分组 —— 而分组恰恰是管理员随时会改的字段。
func enforceGroupPolicy(sender, receiver *model.User, rules groupRuleSet) error {
	if sender == nil || receiver == nil {
		return errInvalidParam
	}
	return rules.allowsGroup(sender.Group, receiver.Group)
}

// loadGroupRules 从扩展库读出全部规则并冻结成一份快照。
//
// 刻意不做进程内缓存:
//   - 划转创建是冷路径(已挂 CriticalRateLimit),一次小表全量 SELECT 可忽略;
//   - 缓存会引入"运营刚收紧规则、另一个节点还按旧规则放行"的窗口,而这是一道
//     策略闸门,多节点口径不一致比多一次查库严重得多;
//   - 缓存 + 失效是本扩展反复出问题的形状(改了配置没生效),不值得为此引入。
//
// 读失败一律向上抛,绝不回落成"不限制":空表是合法的"不限制",查库失败不是。
// 两者混同会让扩展库抖动的那一瞬间变成全部分组限制同时失效。
func loadGroupRules() (groupRuleSet, error) {
	gdb := db.Get()
	if gdb == nil {
		return groupRuleSet{}, db.ErrNotReady
	}
	var rows []GroupRule
	// 不加 Limit:截断会静默丢掉几条规则,那等于悄悄放开一部分限制。
	// 数量上界由写入侧的 maxGroupRuleCount 保证。
	if err := gdb.Order("from_group asc").Find(&rows).Error; err != nil {
		db.MarkFailure(err)
		return groupRuleSet{}, err
	}
	return buildGroupRuleSet(rows), nil
}

// ─────────────────────────── 对外可读的结论 ───────────────────────────

// 用户视角的策略取值。allow_all 与"没有规则"对用户完全等价,统一收敛成
// unrestricted —— 让用户区分这两者没有意义,只会多一种要翻译的文案。
const (
	groupViewUnrestricted = "unrestricted"
	groupViewBlocked      = "blocked"
)

// groupPolicyView 是下发给用户的"我能转给谁"。
type groupPolicyView struct {
	// Policy ∈ unrestricted | allow_list | deny_list | blocked。
	Policy  string `json:"policy"`
	MyGroup string `json:"my_group"`
	// AllowedGroups 仅在 policy=allow_list 时有意义,groupSelfToken 已解析成真实分组名。
	AllowedGroups []string `json:"allowed_groups"`
	// DeniedGroups 仅在 policy=deny_list 时有意义。
	DeniedGroups []string `json:"denied_groups"`
}

// describeGroupPolicy 把规则翻译成用户视角的结论,供划转页在提交之前就告诉用户
// "你能转给谁"。切片一律非 nil:前端拿到 null 还要各自兜一次底。
func describeGroupPolicy(rules groupRuleSet, fromGroup string) groupPolicyView {
	view := groupPolicyView{
		Policy:        groupViewUnrestricted,
		MyGroup:       normalizeGroupName(fromGroup),
		AllowedGroups: []string{},
		DeniedGroups:  []string{},
	}
	rule := rules.ruleFor(fromGroup)
	if rule == nil {
		return view
	}
	switch rule.Policy {
	case GroupPolicyDenyAll:
		view.Policy = groupViewBlocked
	case GroupPolicyAllowList:
		view.Policy = GroupPolicyAllowList
		view.AllowedGroups = resolveGroupList(rule.ToGroups, view.MyGroup)
	case GroupPolicyDenyList:
		view.Policy = GroupPolicyDenyList
		view.DeniedGroups = resolveGroupList(rule.ToGroups, view.MyGroup)
	}
	return view
}

// groupMatrixRow 是管理端"谁能转给谁"的一行。
type groupMatrixRow struct {
	FromGroup string `json:"from_group"`
	// RuleId 为 0 表示这个分组没有被任何规则覆盖(完全不限制)。
	RuleId int64 `json:"rule_id"`
	// Policy 是规则原样的策略,外加 unrestricted 表示"没有规则覆盖"。
	Policy string `json:"policy"`
	// ToGroups 是在已知分组范围内**实际**可以转入的分组。
	ToGroups []string `json:"to_groups"`
}

// buildGroupMatrix 逐个已知分组算出它实际能转给谁。
//
// 存在理由:规则的形式是"策略 + 名单",而运营真正要回答的问题是"A 组现在到底
// 能转给谁"。兜底规则、@self、黑名单三者叠加之后,靠肉眼从规则列表推这个结论
// 极易出错,而配错的直接后果是钱转到了不该去的地方(或该转的转不了)。
//
// 每一格都复用 allowsGroup 而不是另写一套推导:矩阵一旦与真正的判定分家,
// 管理端看到的"能转"就会与用户实际遭遇的拒绝对不上,那比没有矩阵更危险。
func buildGroupMatrix(rules groupRuleSet, known []string) []groupMatrixRow {
	rows := make([]groupMatrixRow, 0, len(known))
	for _, from := range known {
		row := groupMatrixRow{FromGroup: from, Policy: groupViewUnrestricted, ToGroups: []string{}}
		if rule := rules.ruleFor(from); rule != nil {
			row.RuleId = rule.Id
			row.Policy = rule.Policy
		}
		for _, to := range known {
			if rules.allowsGroup(from, to) == nil {
				row.ToGroups = append(row.ToGroups, to)
			}
		}
		rows = append(rows, row)
	}
	return rows
}

// definedUserGroups 返回"站点正式登记过"的**用户分组**集合(已归一)。
//
// ══════════════ 缺陷回归:这里一度是模型分组的命名空间 ══════════════
//
// 本函数原名 definedGroups,来源是 ratio_setting.GetGroupRatioCopy()(分组倍率
// 表的键)与 setting.GetUserUsableGroupsCopy()(「用户可选分组」白名单)。
// 用户分组与模型分组分家之后(见 qianye/modules/groupns 的包注释),**那两份
// 都是模型分组**:前者是 channels/abilities 那一侧的兜底倍率键,后者是令牌能
// 挑哪个渠道池子的白名单。
//
// 而本模块的判定端从第一天起就是用户分组:enforceGroupPolicy 收的是
// model.User.Group(users.group),qy_transfer_group_rules 的残留处置也注册在
// groupns.RegisterResidue(用户分组那一侧)上。
//
// 错位的后果全在**看得见的那一层**,判定一个字节都没受影响:
//
//	下拉候选   列出来的是模型分组。运营从下拉里挑一个"分组"配上限制,
//	           配出来的是一条永不命中的规则 —— 没有任何用户的 users.group
//	           等于那个名字。
//	矩阵取值域 行与列都是模型分组,于是"谁能转给谁"这张图与真正的判定
//	           对不上号,而矩阵是那一页存在的全部理由。
//	软告警     真正的用户分组反而会被标成"站点没定义过"。
//
// 因此取值域必须换成用户分组登记表(qy_user_groups)。default 恒在:
// 它是主库 users.group 的列默认值。
//
// 规则表刻意**不在**这里:规则是分组的**引用方**。把引用方也算成定义方,
// unknownRuleGroups 就永远返回空,那条软告警会变成一句永真的废话。
//
// 刻意不做 SELECT DISTINCT `group` FROM users:那是主库最大的表,而且该列没有
// 索引。少列一个分组不影响任何判定,判定读的始终是 users 行上的真实值。
//
// 第二个返回值 false 表示登记表**读不到**(登记模块关着且扩展库也没读成)。
// 此时清单只有 default 一项,调用方必须把"未登记分组"的软告警整块收起来 ——
// 否则每一个名字都会被标成未登记,那是一片假警报,比没有警报更糟。
func definedUserGroups() (map[string]bool, bool) {
	seen := map[string]bool{defaultGroupName: true}
	if snap, ok := groupns.SnapshotView(); ok {
		for name := range snap.UserGroups {
			seen[normalizeGroupName(name)] = true
		}
		return seen, true
	}
	// 快照没加载(group_namespace 段没开、或本节点刚起来还没刷到)时直接读一次
	// 登记表。这是管理端冷路径上的一张几十行的表,而"下拉里一个分组都没有"
	// 对运营来说与页面坏掉没有区别。
	gdb := db.Get()
	if gdb == nil {
		return seen, false
	}
	names := make([]string, 0, 16)
	if err := gdb.Model(&groupns.UserGroup{}).Pluck("name", &names).Error; err != nil {
		db.MarkFailure(err)
		return seen, false
	}
	for _, name := range names {
		seen[normalizeGroupName(name)] = true
	}
	return seen, true
}

// ruleReferencedGroups 收集规则表自己引用到的分组名(已归一)。
//
// 通配符与 @self 都不是分组名,不收:前者是"所有没有专属规则的分组",
// 后者要到判定时才知道指的是谁,把它们当分组会在矩阵里多出两行永远没人能命中
// 的伪分组。
//
// 停用的规则同样收:运营打开一条停用规则、想在矩阵里确认"启用之后会变成什么样",
// 前提是那一行存在。丢掉它们等于让人在看不见的情况下按下开关。
func ruleReferencedGroups(rows []GroupRule, into map[string]bool) {
	for i := range rows {
		from := normalizeGroupName(rows[i].FromGroup)
		if from != groupWildcard {
			into[from] = true
		}
		for _, to := range parseGroupList(rows[i].ToGroups) {
			if to == groupSelfToken || to == groupWildcard {
				continue
			}
			into[to] = true
		}
	}
}

// knownGroups 汇总管理端矩阵的取值域(行与列取的是同一份),也用作表单的候选清单。
//
// # 缺陷回归(96-audit-r3 transfer #1)
//
// 本函数一度只有 definedGroups 那三个来源,规则表自身完全不参与。于是运营给一个
// 不在分组倍率表里的分组(历史分组、或刚建出来还没配倍率的分组)配规则时:
// 规则确实落库了、判定也确实生效了,但矩阵既不给它一行也不给它一列 ——
// 管理员看到的是"我刚配的分组根本不存在"。矩阵是这一页存在的理由,取值域漏掉
// 规则自己引用的分组,等于让人照着一张看不见自己刚配那条规则的图做决定。
//
// 取值域变完整**不改变任何判定**:矩阵的每一格仍旧是 allowsGroup 逐格算出来的,
// 这里只决定"算哪些格"。
func knownGroups(rows []GroupRule) []string {
	seen, _ := definedUserGroups()
	ruleReferencedGroups(rows, seen)
	out := make([]string, 0, len(seen))
	for name := range seen {
		out = append(out, name)
	}
	sort.Strings(out)
	return out
}

// unknownRuleGroups 返回规则引用了、但站点没有定义过的分组名。
//
// **这是软告警,不是闸门。** 历史分组(倍率表里已经删掉、users 里还有人挂着)
// 恰恰是最需要限制转出的一批账号,拦下来会让运营在最需要配置的时刻配不进去。
// usergroup 的 groupExists 是硬闸门,因为它决定"新用户默认落到哪个分组",
// 配错等于新用户一个模型都调不通;而这里配错的后果只是一条永不命中的规则。
// 两处口径不同是刻意的,不是遗漏。
//
// 登记表读不到时一律返回空:那时每一个名字都会被算成"未登记",那是一片假警报,
// 而假警报比没有警报更糟(见 definedUserGroups 的第二个返回值)。
//
// 返回值一律非 nil:前端拿到 null 还要各自兜一次底。
func unknownRuleGroups(rows []GroupRule) []string {
	out := []string{}
	defined, ok := definedUserGroups()
	if !ok {
		return out
	}
	referenced := map[string]bool{}
	ruleReferencedGroups(rows, referenced)

	for name := range referenced {
		if !defined[name] {
			out = append(out, name)
		}
	}
	sort.Strings(out)
	return out
}

// userGroupCandidate 是管理端**用户分组**下拉的一项。
//
// 划转的两页(分组限制、门槛分档)都只该消费它 —— 两者的键都是 users.group。
// 元数据刻意只有登记表上那三样:显示名、启停、备注。倍率与渠道数是模型分组的
// 属性,把它们摆在用户分组的下拉里正是这一轮在消灭的那种混淆。
type userGroupCandidate struct {
	Name        string `json:"name"`
	DisplayName string `json:"display_name"`
	// Enabled 来自登记表,只影响可见性,**不遮断任何判定** ——
	// 一个被停用的用户分组底下仍然可能挂着用户,给它配规则/门槛是合法的。
	Enabled bool   `json:"enabled"`
	Note    string `json:"note"`
}

// listUserGroupCandidates 汇总用户分组下拉。
//
// 第二个返回值为 false 表示登记表读不到 —— 前端据此必须收起"未登记分组"的
// 软告警并保留自由输入,理由见 definedUserGroups。
//
// default 恒在:它是主库 users.group 的列默认值,即使没人给它建过登记行,
// 也一定有用户挂在上面。
func listUserGroupCandidates() ([]userGroupCandidate, bool) {
	byName := map[string]userGroupCandidate{}
	ok := false

	if snap, view := groupns.SnapshotView(); view {
		for name, ug := range snap.UserGroups {
			key := normalizeGroupName(name)
			byName[key] = userGroupCandidate{
				Name: key, DisplayName: ug.DisplayName, Enabled: ug.Enabled, Note: ug.Note,
			}
		}
		ok = true
	} else if gdb := db.Get(); gdb != nil {
		rows := make([]groupns.UserGroup, 0, 16)
		if err := gdb.Order("sort_order asc, name asc").Find(&rows).Error; err != nil {
			db.MarkFailure(err)
		} else {
			for _, ug := range rows {
				key := normalizeGroupName(ug.Name)
				byName[key] = userGroupCandidate{
					Name: key, DisplayName: ug.DisplayName, Enabled: ug.Enabled, Note: ug.Note,
				}
			}
			ok = true
		}
	}

	if _, has := byName[defaultGroupName]; !has {
		byName[defaultGroupName] = userGroupCandidate{Name: defaultGroupName, Enabled: true}
	}
	out := make([]userGroupCandidate, 0, len(byName))
	for _, item := range byName {
		out = append(out, item)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out, ok
}

// groupCandidate 是管理端**模型分组**下拉的一项。
//
// ⚠️ 它与本模块的判定毫无关系:划转的键是用户分组(见 definedUserGroups 的
// 缺陷回归)。留着它、并继续从 /admin/transfer/group-rules 下发,只有一个理由 ——
// 前端 `web/src/features/qy/lib/group-options.ts` 把这份清单抬成了共享层,
// 而它现在唯一的消费方是**违规规则的分组作用域**,那一处比的是
// relayInfo.UsingGroup,也就是模型分组,口径本来就是对的。
//
// 把它挪去一个模型分组自己的端点是正确的归宿,但那要连带改动 violation 那一页,
// 不在这一轮的射程内。在挪走之前,任何人都不要拿它去填划转的两个下拉。
//
// 四个 JSON 字段与 usergroup 的 groupOption 逐字一致(name / ratio /
// has_channels / public_usable):同一个"分组下拉"概念在两个管理页出现,字段名
// 一旦分叉,前端就会为同一件事写两套渲染。两个模块各自持有实现而不共享一个包,
// 是因为它们的候选范围口径不同(那边排除 auto 伪分组、这边不排除),
// TestGroupCandidateShapeMatchesUserGroupModule 负责在字段名分叉时报警。
type groupCandidate struct {
	Name string `json:"name"`
	// Ratio 是该名字在 GroupRatio 里的值,**可以是 null**,与 usergroup 的
	// groupOption.Ratio 同口径。
	//
	// 指针而不是裸 float64:此前只在"用户可选分组"白名单里、不在 GroupRatio 里的
	// 名字会被硬塞一个 ratio=1,而 1 恰好与上游 GetGroupRatio 的 fail-open 数值
	// 巧合 —— 界面上「配过原价」与「压根没配、扣费时按 1.0 兜底」长得一模一样。
	// null 是「后端确实回答了:这个名字没有兜底倍率」。
	Ratio *float64 `json:"ratio"`
	// HasChannels 表示 abilities 里至少有一条启用的行落在该分组。
	HasChannels bool `json:"has_channels"`
	// PublicUsable 表示该分组在"用户可选分组"白名单里。
	PublicUsable bool `json:"public_usable"`
}

// listGroupCandidates 汇总下拉选项。第二个返回值表示 abilities 探测是否成功 ——
// 探测失败时全部 has_channels 都是 false,前端必须知道那是"不确定"而不是
// "确实没有渠道",否则运营会被一片警告吓住。
//
// 只列模型分组命名空间里的名字,并且带上元数据。
func listGroupCandidates() ([]groupCandidate, bool) {
	withChannels, probeOK := groupsWithEnabledAbilities()

	// 三份来源的键都按判定端的口径归一后再合并:名字必须与"保存后落库的那个值"
	// 逐字相同,否则运营从下拉里选一项、保存、再回来看,会发现自己选的那项
	// 已经不在下拉里了(倍率表写 "VIP"、规则表存 vip)。
	usable := map[string]bool{}
	for name := range setting.GetUserUsableGroupsCopy() {
		usable[normalizeGroupName(name)] = true
	}
	// ratios 的键是归一化后的名字,而**倍率侧 GetGroupRatio 是精确 map 查找**。
	// 归一化把 "VIPpool" 折成 "vippool" 之后,这一项上挂的倍率是 GroupRatio["VIPpool"]
	// 的值,而真正请求走 UsingGroup="vippool" 时那次精确查找会落空、fail-open 按 1.0
	// 扣费 —— 界面 0.1、实扣 1.0。因此只有「归一化之后与原名逐字相同」的键才带倍率,
	// 其余一律 null:说不出准确的价,就不要编一个。
	ratios := map[string]*float64{defaultGroupName: nil}
	for name, ratio := range ratio_setting.GetGroupRatioCopy() {
		normalized := normalizeGroupName(name)
		if normalized != name {
			if _, seen := ratios[normalized]; !seen {
				ratios[normalized] = nil
			}
			continue
		}
		value := ratio
		ratios[normalized] = &value
	}
	// 只在可选清单里、不在 GroupRatio 里的名字同样是 null:给它填 1 会与
	// fail-open 的那个 1.0 长得一模一样,而那正是这份清单要消灭的骗人数字。
	for name := range usable {
		if _, ok := ratios[name]; !ok {
			ratios[name] = nil
		}
	}

	out := make([]groupCandidate, 0, len(ratios))
	for name, ratio := range ratios {
		out = append(out, groupCandidate{
			Name:         name,
			Ratio:        ratio,
			HasChannels:  withChannels[name],
			PublicUsable: usable[name],
		})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out, probeOK
}

// groupsWithEnabledAbilities 取 abilities 里所有仍有启用渠道的分组。
//
// 走 GORM 的 Model + Pluck 而不是拼 SQL:group 是三种数据库里的保留字,只有让
// GORM 拿着 model.Ability 的 schema 去渲染,引号规则才会自动跟着方言走
// (MySQL/SQLite 反引号、PostgreSQL 双引号)。手写 "DISTINCT group" 在任何一种
// 数据库上都是语法错误。
//
// 探测失败一律返回 (nil, false) 而不是向上抛:这是一条纯提示信息,主库抖一下
// 不该让"谁能转给谁"这张矩阵整页打不开。
func groupsWithEnabledAbilities() (map[string]bool, bool) {
	if model.DB == nil {
		return nil, false
	}
	var names []string
	err := model.DB.Model(&model.Ability{}).
		Where("enabled = ?", true).
		Distinct().
		Pluck("group", &names).Error
	if err != nil {
		return nil, false
	}
	set := make(map[string]bool, len(names))
	for _, name := range names {
		set[normalizeGroupName(name)] = true
	}
	return set, true
}

// ─────────────────────────── 归一化与校验 ───────────────────────────

// normalizeGroupName 归一化分组名:去空白、折叠大小写、空串归一成 default。
//
// 折叠大小写的理由见 groupname 包:存储层的排序规则大小写不敏感,而这里是
// Go map 的精确匹配,不折叠就会出现"规则写在 VIP 上、用户是 vip、闸门形同虚设"。
// 对一道限制类闸门,折叠是 fail-closed 的那一侧 —— 宁可多盖一个大小写变体,
// 不能漏一个。
//
// 空串归一成 default:主库 users.group 的列默认值就是 'default',但历史行、
// 以及被直接改过库的账号可能留着空串。不归一的话,一条"default 只能转给 vip"
// 的白名单对这些账号完全不生效 —— 而它们恰恰是最可疑的那批账号。写入侧不受
// 影响:validateGroupRule 在归一之前就拒了空的 from_group。
func normalizeGroupName(g string) string { return groupname.Effective(g) }

// parseGroupList 按逗号/分号/换行拆分名单,丢弃空项,并把每一项归一成
// 与判定端同一个口径的分组名。
//
// 归一化放在拆分这一步而不是留给三个调用方(matchGroupList / resolveGroupList /
// validateGroupRule)各自处理:名单里的项最终都要和 normalizeGroupName 过的
// from/to 比较,漏一处的后果是静默的 —— 白名单里的 "VIP" 永远匹配不上 vip
// (名单静默变窄),黑名单里的 "VIP" 则永远拦不住 vip(名单静默变宽,这一侧
// 直接放走本该被拦的资金)。
func parseGroupList(raw string) []string {
	parts := strings.FieldsFunc(raw, func(r rune) bool {
		return r == ',' || r == ';' || r == '\n' || r == '\r'
	})
	out := make([]string, 0, len(parts))
	for _, p := range parts {
		if strings.TrimSpace(p) == "" {
			continue
		}
		out = append(out, normalizeGroupName(p))
	}
	return out
}

// resolveGroupList 把名单里的 groupSelfToken 换成真实分组名并去重,供展示使用。
func resolveGroupList(raw, selfGroup string) []string {
	seen := make(map[string]bool)
	out := []string{}
	for _, entry := range parseGroupList(raw) {
		if entry == groupSelfToken {
			entry = selfGroup
		}
		if seen[entry] {
			continue
		}
		seen[entry] = true
		out = append(out, entry)
	}
	return out
}

// validateGroupRule 校验并就地归一化一条规则,写库前必须过这一关。
//
// 返回的 error 是给管理员看的修正依据(与 violation.ValidateRule 同口径),
// 因此是自然语言而不是 code —— 这些消息只有管理员看得到。
func validateGroupRule(r *GroupRule) error {
	r.FromGroup = strings.TrimSpace(r.FromGroup)
	if r.FromGroup == "" {
		return fmt.Errorf("发起分组不能为空,兜底规则请填 %q", groupWildcard)
	}
	if r.FromGroup != groupWildcard {
		// 先归一再校验,不能反过来:落库的必须就是被校验过的那个值,
		// 否则"校验通过的字符串"与"实际存进去的字符串"是两个东西。
		r.FromGroup = normalizeGroupName(r.FromGroup)
		if err := checkGroupToken(r.FromGroup); err != nil {
			return err
		}
	}

	entries := parseGroupList(r.ToGroups)
	switch r.Policy {
	case GroupPolicyAllowAll, GroupPolicyDenyAll:
		// 名单在这两种策略下没有任何含义。留着它只会让"谁能转给谁"必须先想清楚
		// "这个名单现在算不算数",而那正是配错的来源。
		entries = nil
	case GroupPolicyAllowList:
		if len(entries) == 0 {
			return fmt.Errorf("白名单为空等同于禁止发起划转,请直接选择 %s", GroupPolicyDenyAll)
		}
	case GroupPolicyDenyList:
		if len(entries) == 0 {
			return fmt.Errorf("黑名单为空等同于不做限制,请直接选择 %s", GroupPolicyAllowAll)
		}
	default:
		return fmt.Errorf("policy 取值非法: %q", r.Policy)
	}
	if len(entries) > maxToGroupEntries {
		return fmt.Errorf("目标分组最多 %d 个,当前 %d 个", maxToGroupEntries, len(entries))
	}

	seen := make(map[string]bool, len(entries))
	norm := make([]string, 0, len(entries))
	for _, e := range entries {
		if e == groupWildcard {
			// 名单里放 * 与 allow_all / deny_all 语义重叠。同一件事两种写法,
			// 就意味着读规则的人必须两处对照才敢下结论。
			return fmt.Errorf("目标分组不支持通配符 %q,请改用 %s 或 %s",
				groupWildcard, GroupPolicyAllowAll, GroupPolicyDenyAll)
		}
		if e != groupSelfToken {
			if err := checkGroupToken(e); err != nil {
				return err
			}
		}
		if seen[e] {
			continue
		}
		seen[e] = true
		norm = append(norm, e)
	}
	r.ToGroups = strings.Join(norm, ",")
	if len(r.ToGroups) > maxToGroupsLen {
		return fmt.Errorf("目标分组名单过长(%d 字节,上限 %d)", len(r.ToGroups), maxToGroupsLen)
	}
	r.Remark = truncate(strings.TrimSpace(r.Remark), maxGroupRuleRemark)
	return nil
}

// checkGroupToken 校验单个分组名。
//
// 收紧到"不含空白与分隔符"是硬要求:名单以逗号拼成一列存储,分组名里混进逗号
// 会让一条规则在读回来时裂成两条,而裂出来的那半条谁也不认识 —— 白名单因此
// 静默变宽,黑名单静默变窄。
func checkGroupToken(s string) error {
	if s == "" || len(s) > maxGroupNameLen {
		return fmt.Errorf("分组名长度必须在 1..%d 之间: %q", maxGroupNameLen, s)
	}
	if strings.ContainsAny(s, ",;\r\n\t ") {
		return fmt.Errorf("分组名不能包含空白或分隔符: %q", s)
	}
	return nil
}
