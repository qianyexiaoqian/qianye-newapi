package transfer

import (
	"fmt"
	"sort"
	"strings"

	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/qianye/db"
	"github.com/QuantumNous/new-api/qianye/groupname"
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
	Enabled bool `json:"enabled" gorm:"not null;default:false"`

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

// knownGroups 汇总运营配置过的分组名,供管理端下拉与矩阵使用。
//
// 刻意不做 SELECT DISTINCT `group` FROM users:那是主库最大的表,而且该列没有
// 索引。这里只是给管理端一份候选清单 —— 表单允许自由填写,少列一个分组不影响
// 任何判定,判定读的始终是 users 行上的真实值。
func knownGroups() []string {
	seen := map[string]bool{defaultGroupName: true}
	for name := range ratio_setting.GetGroupRatioCopy() {
		seen[normalizeGroupName(name)] = true
	}
	for name := range setting.GetUserUsableGroupsCopy() {
		seen[normalizeGroupName(name)] = true
	}
	out := make([]string, 0, len(seen))
	for name := range seen {
		out = append(out, name)
	}
	sort.Strings(out)
	return out
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
