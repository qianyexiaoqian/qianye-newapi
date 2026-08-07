package groupns

// store.go —— 登记表的回填与写入。
//
// ══════════════════════ 回填的两条硬规则 ══════════════════════
//
//  1. **以 users 表为准。** 用户分组的来源集合恒含 `SELECT DISTINCT users.group`,
//     所以结构上不可能漏掉一个"正在被使用的用户分组"。漏一个的表现是:
//     S4 翻写侧权威之后,管理员改不动那一整组用户。
//
//  2. **回填永不因重名而失败。** 本站存量就有 5 个名字同时是用户分组和模型分组
//     (最典型的是「浅夜の梦专属号池」:539 个用户的用户分组,同时是 76 行 abilities
//     的模型分组)。跨 roster 唯一性只对**管理端新建**生效,对回填生效会让 S0
//     整个失败,而 S0 的全部价值就是"先把现状看清楚"。

import (
	"context"
	"fmt"
	"sort"
	"strings"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/setting/ratio_setting"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// BackfillResult 是一次回填的结果,直接给管理端与启动日志用。
type BackfillResult struct {
	UserGroupsSeen    int      `json:"user_groups_seen"`
	ModelGroupsSeen   int      `json:"model_groups_seen"`
	UserGroupsAdded   int      `json:"user_groups_added"`
	ModelGroupsAdded  int      `json:"model_groups_added"`
	LegacyDualNames   []string `json:"legacy_dual_names"`
	RatioWithoutRoute []string `json:"ratio_without_route"`
	RouteWithoutRatio []string `json:"route_without_ratio"`
	// SkippedNames 是**超过 64 个字符、无法登记**的名字。
	//
	// 它必须是一个显式的返回字段而不是一行日志:这些名字在管理端两页上不会出现,
	// 而"一个分组在列表里没有"与"这个分组不存在"在界面上长得一模一样。
	// 它们不影响路由与计费(登记表 fail-open),但对它们配不了默认模型分组。
	SkippedNames []string `json:"skipped_names"`
}

// observeGroups 采集全站正在被使用的分组名。
//
// 三个来源分别回答三个不同的问题,缺一个都会漏掉一整类名字:
//
//	users.group             谁是用户分组(权威,以它为准)
//	abilities.group         哪些模型分组**真的有渠道**(has_route 的判据)
//	options.GroupRatio 的键 哪些模型分组**配了倍率**
//
// 后两者的差集本身就是报表:
//
//	有路由没倍率 → 正在静默按 1.0 扣费(这才是真正的资金泄漏集合)
//	有倍率没路由 → 用户选得到,一发请求必然 503
func observeGroups(ctx context.Context, mainDB *gorm.DB) (userGroups, modelGroups map[string]bool, res BackfillResult, err error) {
	userGroups = map[string]bool{}
	modelGroups = map[string]bool{}
	if mainDB == nil {
		return nil, nil, res, fmt.Errorf("qianye/groupns: 主库未初始化")
	}
	mainDB = mainDB.WithContext(ctx)
	col := model.QyCommonGroupCol()

	var userRows []struct {
		Grp string `gorm:"column:grp"`
	}
	// deleted_at IS NULL:users 是软删除,漏掉这个条件会把早已注销的账号的分组
	// 也登记进来,让一个 0 影响的历史分组看起来还在用。
	if err = mainDB.Raw("SELECT DISTINCT " + col + " AS grp FROM users WHERE deleted_at IS NULL").
		Scan(&userRows).Error; err != nil {
		return nil, nil, res, err
	}
	for _, row := range userRows {
		if row.Grp != "" {
			userGroups[row.Grp] = true
		}
	}

	var abilityRows []struct {
		Grp string `gorm:"column:grp"`
	}
	// enabled 的行才算"有路由":AddAbilities 令 Enabled = (channel.Status == enabled),
	// InitChannelCache 也跳过 disabled 渠道,两种缓存模式下这个判据都成立。
	if err = mainDB.Raw("SELECT DISTINCT "+col+" AS grp FROM abilities WHERE enabled = ?", true).
		Scan(&abilityRows).Error; err != nil {
		return nil, nil, res, err
	}
	routed := map[string]bool{}
	for _, row := range abilityRows {
		if row.Grp != "" {
			modelGroups[row.Grp] = true
			routed[row.Grp] = true
		}
	}

	rated := map[string]bool{}
	for name := range ratio_setting.GetGroupRatioCopy() {
		if name == "" {
			continue
		}
		modelGroups[name] = true
		rated[name] = true
	}

	// 超长名字在这里就摘出来,早于一切统计:它们既进不了登记表
	// (见 registrableGroupName),也不该被计进 seen —— 否则 seen 与 added 永远
	// 对不上,而对不上的原因不可见。
	res.SkippedNames = dropUnregistrable(userGroups, modelGroups, routed, rated)

	res.RouteWithoutRatio = diffNames(routed, rated)
	res.RatioWithoutRoute = diffNames(rated, routed)
	res.UserGroupsSeen = len(userGroups)
	res.ModelGroupsSeen = len(modelGroups)

	// ── legacy_dual 的判据是 routed,**不是** modelGroups ──
	//
	// modelGroups 是 abilities ∪ GroupRatio 键,而上游设计要求每个用户分组在
	// GroupRatio 里都有一个兜底倍率(否则那一档人的请求按 fail-open 的 1.0 计费)。
	// 拿它当判据的话 legacy_dual 恒等于"全部用户分组",7/7 —— 一个恒真的徽标既
	// 指不出真正的重名,也让"只减不增"这条守卫失去全部分辨力。
	//
	// 真正的重名是「这个名字既是一档人、又是一个**真的能路由**的渠道池子」,
	// 也就是 users.group ∩ (abilities 里有 enabled 行的分组)。它才是"改一次名字
	// 会同时动到人和路由"的那个集合。
	for name := range userGroups {
		if routed[name] {
			res.LegacyDualNames = append(res.LegacyDualNames, name)
		}
	}
	sort.Strings(res.LegacyDualNames)
	return userGroups, modelGroups, res, nil
}

// dropUnregistrable 把超过 64 个字符的名字从全部观测集合里剔除并返回它们。
//
// 剔除而不是裁剪:裁剪产出的主键与源名字永远不相等(见 model.go 的
// newModelGroup 注释),而且中文名被按字节裁一刀之后会带上半个 UTF-8 序列,
// MySQL 在 STRICT_TRANS_TABLES 下以 Error 1366 拒绝**整批** INSERT。
func dropUnregistrable(sets ...map[string]bool) []string {
	skipped := map[string]bool{}
	for _, set := range sets {
		for name := range set {
			if !registrableGroupName(name) {
				skipped[name] = true
				delete(set, name)
			}
		}
	}
	if len(skipped) == 0 {
		return nil
	}
	out := make([]string, 0, len(skipped))
	for name := range skipped {
		out = append(out, name)
	}
	sort.Strings(out)
	common.SysError(fmt.Sprintf(
		"qianye/groupns: %d 个分组名超过 %d 个字符,**未登记**(路由与计费不受影响,"+
			"但无法为它们配置默认模型分组):%s",
		len(out), maxGroupNameLen, strings.Join(out, "、")))
	return out
}

func diffNames(a, b map[string]bool) []string {
	out := make([]string, 0, len(a))
	for name := range a {
		if !b[name] {
			out = append(out, name)
		}
	}
	sort.Strings(out)
	return out
}

// Backfill 把观测到的分组名登记进两张表。
//
// **只新增,永不修改已有行**:回填是描述性的,而已有行可能已经被运营配过
// default_mode / display_name。一次回填覆盖掉运营配置,表现是"配好的默认模型分组
// 第二天自己变回 inherit",而且没有任何日志能解释它。
//
// 新增行的 default_mode 恒为 inherit ⇒ 回填本身**零行为变化**。
func Backfill(ctx context.Context, mainDB, extDB *gorm.DB, operatorId int) (BackfillResult, error) {
	userGroups, modelGroups, res, err := observeGroups(ctx, mainDB)
	if err != nil {
		return res, err
	}
	if extDB == nil {
		return res, fmt.Errorf("qianye/groupns: 扩展库未初始化")
	}
	extDB = extDB.WithContext(ctx)
	ts := now()

	var existingUsers []UserGroup
	if err := extDB.Select("name").Find(&existingUsers).Error; err != nil {
		return res, err
	}
	haveUser := make(map[string]bool, len(existingUsers))
	for _, row := range existingUsers {
		haveUser[row.Name] = true
	}
	newUsers := make([]*UserGroup, 0, len(userGroups))
	for name := range userGroups {
		if !haveUser[name] {
			newUsers = append(newUsers, newUserGroup(name, operatorId, ts))
		}
	}

	var existingModels []ModelGroup
	if err := extDB.Select("name").Find(&existingModels).Error; err != nil {
		return res, err
	}
	haveModel := make(map[string]bool, len(existingModels))
	for _, row := range existingModels {
		haveModel[row.Name] = true
	}
	newModels := make([]*ModelGroup, 0, len(modelGroups))
	for name := range modelGroups {
		if !haveModel[name] {
			newModels = append(newModels, newModelGroup(name, operatorId, ts))
		}
	}

	// DoNothing 而不是 Upsert:并发的两个节点同时回填时,后到的那个必须什么都不做,
	// 而不是把先到者(可能已经被运营改过)覆盖掉。
	//
	// Added 取 **RowsAffected 而不是 len(new…)**:后者是"尝试插入数"。两个节点
	// 同时回填时后到的那个会被 DoNothing 全部吞掉,却仍然报"新增 N 个"并触发一次
	// 无谓的快照重载 —— 而"每次启动都新增同样的 N 个"正是回填不幂等的表现形状,
	// 把它和真实新增混在同一个数字里,幂等性回归就没有任何地方能被看见。
	if len(newUsers) > 0 {
		tx := extDB.Clauses(clause.OnConflict{DoNothing: true}).CreateInBatches(newUsers, 100)
		if tx.Error != nil {
			return res, tx.Error
		}
		res.UserGroupsAdded = int(tx.RowsAffected)
	}
	if len(newModels) > 0 {
		tx := extDB.Clauses(clause.OnConflict{DoNothing: true}).CreateInBatches(newModels, 100)
		if tx.Error != nil {
			return res, tx.Error
		}
		res.ModelGroupsAdded = int(tx.RowsAffected)
	}

	if len(res.RouteWithoutRatio) > 0 {
		// 这一条是**资金泄漏集合的主角**,不是随便一条 info。
		// 有 enabled 渠道、没有倍率 ⇒ 走到这些分组的请求正在被按 1.0 静默计费。
		common.SysError(fmt.Sprintf(
			"qianye/groupns: %d 个模型分组有 enabled 渠道但**不在分组倍率表里**,"+
				"走到它们的请求正在按 fail-open 的 1.0 倍静默计费:%s",
			len(res.RouteWithoutRatio), strings.Join(res.RouteWithoutRatio, "、")))
	}
	if len(res.RatioWithoutRoute) > 0 {
		common.SysError(fmt.Sprintf(
			"qianye/groupns: %d 个模型分组配了倍率但**没有任何 enabled 渠道**,"+
				"用户选得到、一发请求必然 503:%s",
			len(res.RatioWithoutRoute), strings.Join(res.RatioWithoutRoute, "、")))
	}
	return res, nil
}

// HasRoute 报告一个模型分组此刻**是否真的有渠道**。
//
// 判据冻结为「abilities 里存在 group=M AND enabled=true 的行」,前后端共用。
// 它是保存默认模型分组时的硬闸门:配一个没有渠道的值,该用户分组的**全部**
// 空分组令牌会在下一次请求同时 503。
//
// 冷路径查询,只在管理端保存时调用一次。
func HasRoute(ctx context.Context, mainDB *gorm.DB, modelGroup string) (bool, error) {
	if mainDB == nil {
		return false, fmt.Errorf("qianye/groupns: 主库未初始化")
	}
	var count int64
	col := model.QyCommonGroupCol()
	err := mainDB.WithContext(ctx).
		Raw("SELECT COUNT(*) FROM abilities WHERE "+col+" = ? AND enabled = ?", modelGroup, true).
		Scan(&count).Error
	return count > 0, err
}

// EmptyGroupTokenCounts 统计每个用户分组下的**空分组令牌**数量。
//
// 这是「配一个默认模型分组会影响多少人」的直接答案,也是 96/47/47/1 那份分布的
// 来源。它必须出现在保存前的 preview 里:粒度是 7 个用户分组,而每一行背后是
// 几十个令牌,不摆出来运营就是在盲配。
//
// ── enabledOnly 为什么必须由调用方给,而不是写死 ──
//
// 两个问题的正确答案不同:
//
//	「配一个默认模型分组之后,多少个令牌的路由会当场变化」 —— 只数启用的。
//	                                                       停用的令牌发不出请求。
//	「删掉这个用户分组会波及多少个令牌」                   —— 启用与停用都要数,
//	                                                       与 QyUserGroupTokenCount 同口径。
//
// 写死成"只数启用"曾经让删除弹窗里的「N 个(其中空分组令牌 M 个)」两个数字
// 出自两个口径:M 不是 N 的子集,运营据此以为只有 M 个令牌的行为会变,
// 而实际上那些停用的空分组令牌一旦被重新启用,同样按新分组解析清单与倍率。
func EmptyGroupTokenCounts(ctx context.Context, mainDB *gorm.DB, enabledOnly bool) (map[string]int64, error) {
	out := map[string]int64{}
	if mainDB == nil {
		return out, fmt.Errorf("qianye/groupns: 主库未初始化")
	}
	col := model.QyCommonGroupCol()
	var rows []struct {
		Grp string `gorm:"column:grp"`
		Cnt int64  `gorm:"column:cnt"`
	}
	// 令牌的 group 为空 ⇒ UsingGroup 回落 users.group ⇒ 这些正是会被默认解析改写的。
	sql := "SELECT u." + col + " AS grp, COUNT(*) AS cnt FROM tokens t " +
		"JOIN users u ON u.id = t.user_id " +
		"WHERE t.deleted_at IS NULL AND u.deleted_at IS NULL AND t." + col + " = '' "
	args := make([]any, 0, 1)
	if enabledOnly {
		sql += "AND t.status = ? "
		args = append(args, common.TokenStatusEnabled)
	}
	sql += "GROUP BY u." + col
	if err := mainDB.WithContext(ctx).Raw(sql, args...).Scan(&rows).Error; err != nil {
		return out, err
	}
	for _, row := range rows {
		out[row.Grp] = row.Cnt
	}
	return out, nil
}
