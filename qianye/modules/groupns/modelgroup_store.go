package groupns

// modelgroup_store.go —— 模型分组的**影响面探测**与**删除联动**。
//
// ════════════════════ 一个模型分组的名字住在几个地方 ════════════════════
//
//	主库 options   GroupRatio 的键            兜底倍率 = "这个站点有哪些模型分组"的事实源
//	主库 options   GroupGroupRatio 的**内层**键  (用户分组, 模型分组) 的交叉倍率
//	主库 options   UserUsableGroups 的键       全局「用户可选分组」白名单(项目方点名的那一处)
//	主库 options   AutoGroups 的元素           auto 伪分组的尝试顺序
//	主库 tokens    tokens.group                令牌显式钉住的分组
//	主库 channels  channels.group(逗号串)      渠道绑定
//	主库 abilities abilities.group             物化路由表(由 channels 生成)
//	扩展 qy_model_groups                        登记行(备注 / 显示名 / 启停)
//	扩展 qy_user_groups.default_model_group     用户分组的默认模型分组(pin)
//	扩展 qy_group_grants.model_group            用户分组 × 模型分组的权威授权
//	扩展 qy_plan_group_grants.model_group       套餐解锁
//
// 上游"删除一个模型分组"的全部实现是:在模型分组页把一行从 GroupRatio 里去掉。
// 其余十处一个都不动。表现分三档,从轻到重:
//
//	轻   白名单里还留着这个键 → 用户在令牌下拉里照样看得到它
//	中   授权 / 套餐解锁 / auto 顺序里还留着 → 一批永远不会命中的配置
//	重   有 pin 指着它 → 那一整档人的空分组令牌下一次请求全部 500(配置自相矛盾);
//	     有 abilities 行 → 渠道还在路由表里,而它已经没有倍率也没有人能选到它,
//	     GetGroupRatio 对它 fail-open 返回 1.0 —— 「有渠道但选不到」+ 静默按原价扣费
//
// ════════════════════ channels / abilities 为什么**一行都不动** ════════════════════
//
//  1. channels.group 是**逗号串**,一个渠道可以同时属于多个池子。从串里摘掉一段
//     是在改渠道配置;把某个渠道的最后一段摘掉会让它变成 `group=""`,
//     而 model/channel.go 的 AddAbilities 会照样为空串写出 ability 行 ——
//     一条谁都路由不到、也没有任何界面能删掉的僵尸路由。
//  2. abilities 是**由 channels 物化出来的**。单独删 abilities 而不动 channels,
//     下一次渠道保存或一次「修复数据库一致性」就把它整块重建回来。
//     一个"删了又自己回来"的删除,比不删更难排查。
//  3. 删渠道绑定是不可逆的运维动作,而且它的正确做法与"删一个分组名"无关
//     (要么把渠道改挂到别的池子、要么停用渠道)。把它做成删分组的**副作用**,
//     等于让一次命名整理顺手改掉线上的路由拓扑。
//
// 所以这里选的是**拦住**而不是级联:只要 abilities 里还有 enabled 行,删除默认
// 被拒绝,理由正文直接告诉运营去渠道页处理;确实要留着这些渠道再删名字的话,
// 必须显式勾选覆盖,而那次勾选进审计。这样"有渠道但选不到"就不可能被静默造出来。
//
// ════════════════════ 两库之间的顺序:先扩展、后 options ════════════════════
//
// 跨库事务不存在。中断只可能落在两者之间,所以顺序要挑**代价更小的那一半**:
//
//	先 options 后扩展   中断后:名字已从 GroupRatio 消失,而 abilities/grants 还在
//	                    → GetGroupRatio fail-open 返回 1.0,走到它的请求静默按原价扣费
//	先扩展后 options    中断后:授权已清,而名字还在 GroupRatio 里
//	                    → 一批用户少一个可选分组(403),没有任何一笔钱算错
//
// 「资金安全优先于可用性」在这里的直接推论就是后者。中断的表现会原样进
// partial 与审计,并且**再按一次同一个按钮**即可收敛(两半都是幂等的)。

import (
	"context"
	"fmt"
	"sort"
	"strings"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/setting"
	"github.com/QuantumNous/new-api/setting/ratio_setting"

	"gorm.io/gorm"
)

// 模型分组名的**来源**。项目方原话:「模型分组当前预设有点混乱」——
// 混乱的具体形状是回填出来的名单里混着 default / group_1 这类历史残留,
// 而界面上没有任何一处说得清"这一行是从哪来的、它现在还能不能路由"。
//
// 三个来源刻意分开而不是合成一个"是否有效":它们对应三种完全不同的处置。
const (
	// SourceRatio 出现在 options.GroupRatio 里 —— 有兜底倍率,是"这个站点有哪些
	// 模型分组"的事实源。只有它没有路由 = 用户选得到、一发请求必然 503。
	SourceRatio = "group_ratio"
	// SourceRoute abilities 里有 enabled 行 —— 真的能路由。
	// 只有它没有倍率 = 走到它的请求正在按 fail-open 的 1.0 静默计费(资金泄漏)。
	SourceRoute = "abilities"
	// SourceRegistryOnly 两者都没有,只在扩展登记表里 —— 一个纯粹的名字。
	// 它是最安全的删除对象,也是"预设混乱"里占比最大的那一档。
	SourceRegistryOnly = "registry_only"
)

// ModelGroupImpact 是删除一个模型分组之前必须摆在运营眼前的全部东西。
//
// **它不是一份"看看就好"的报表。** 每一个字段都对应一条删除闸门或一句必须读到的
// 后果,字段少一个,对应的那种失败就只能在线上被发现。
type ModelGroupImpact struct {
	Name string `json:"name"`

	// Registered 表示扩展登记表里有这一行。false 时删除只做联动、不删登记行。
	Registered bool `json:"registered"`
	// Sources 是这个名字的来源集合(见 SourceRatio / SourceRoute / SourceRegistryOnly)。
	Sources []string `json:"sources"`

	InGroupRatio bool    `json:"in_group_ratio"`
	BaseRatio    float64 `json:"base_ratio"`

	// InUsableGroups 是项目方点名的「全局(用户可选分组)」。
	InUsableGroups bool `json:"in_usable_groups"`
	// UsableDescription 是白名单里那段**原文**(未经模型分组备注覆盖)。
	UsableDescription string `json:"usable_description"`
	// Note 是 qy_model_groups.note。两者的优先级见 setting/qy_groupnote_export.go。
	Note string `json:"note"`

	// AutoPosition 是它在 options.AutoGroups 里的位次(从 1 起),0 表示不在。
	AutoPosition int `json:"auto_position"`

	// HasRoute / AbilityRows 是 abilities 口径,EnabledChannels 是 channels 口径。
	// **两个口径都要给**:缓存模式下真实路由由 channels 决定,而 has_route 由
	// abilities 决定,两者背离正是「修复数据库一致性」存在的理由。
	HasRoute        bool  `json:"has_route"`
	AbilityRows     int64 `json:"ability_rows"`
	EnabledChannels int64 `json:"enabled_channels"`

	// Tokens 是**启用状态**下 tokens.group 正指着它的令牌数。删掉之后这些令牌
	// 每一次请求都撞 403,而且用户端看到的是「分组已被弃用」。
	Tokens int64 `json:"tokens"`
	// TokenOwners 是这些令牌背后有多少个不同的人。行数决定工作量,人数决定影响面。
	TokenOwners int64 `json:"token_owners"`

	// CrossRatioUserGroups 是 options.GroupGroupRatio 里配过 (X, 本分组) 交叉倍率的
	// 用户分组。删除会把这些内层键一并摘掉 —— 它们是**价格**,必须点名。
	CrossRatioUserGroups []string `json:"cross_ratio_user_groups"`

	// PinnedByUserGroups 是把它设成默认模型分组(default_mode=pin)的用户分组。
	// **这一条是全部影响面里最危险的**:删掉之后那些档的空分组令牌下一次请求
	// 全部 500(middleware/auth.go 的 pin 校验失败),而它们此前是好的。
	PinnedByUserGroups []string `json:"pinned_by_user_groups"`
	// PinnedEmptyGroupTokens 是上面那些用户分组名下的空分组令牌总数 ——
	// 「多少令牌会当场挂掉」的那个数字。
	PinnedEmptyGroupTokens int64 `json:"pinned_empty_group_tokens"`

	// Residues 是各模块声明的残留(授权、套餐解锁、影子计数……),见 modelgroup_residue.go。
	Residues []Residue `json:"residues"`

	// Blockers 是**不可覆盖**的拒绝理由。非空即删不了,而且正文里必须写明下一步。
	Blockers []string `json:"blockers"`
	// NeedsForceHasRoute / NeedsForceOrphanTokens 是两道**可覆盖**的闸门。
	// 分开而不是合成一个布尔:两者的后果方向完全不同(一个是留下僵尸路由 +
	// 静默按 1.0 计费,另一个是一批令牌当场 403),运营需要分别确认。
	NeedsForceHasRoute     bool `json:"needs_force_has_route"`
	NeedsForceOrphanTokens bool `json:"needs_force_orphan_tokens"`
}

// DeleteModelGroupOptions 是两道可覆盖闸门的显式勾选。它们进审计正文。
type DeleteModelGroupOptions struct {
	ForceHasRoute     bool `json:"force_has_route"`
	ForceOrphanTokens bool `json:"force_orphan_tokens"`
}

// ModelGroupDeleteResult 是一次删除的结果。
type ModelGroupDeleteResult struct {
	Name   string           `json:"name"`
	Impact ModelGroupImpact `json:"impact"`
	// RemovedFrom 是真正被改动过的 options 键,按字典序。空表示 options 侧无事可做。
	RemovedFrom []string `json:"removed_from"`
	// Partial 非空表示扩展库那一半已经提交、而 options 那一半没写成。
	// **必须原样进界面**:两库不原子的最坏失败方式是运营看到一句绿色的「已删除」
	// 然后走人,而线上停在中间态。
	Partial *ModelGroupDeletePartial `json:"partial,omitempty"`
}

// ModelGroupDeletePartial 是删除的半成状态。
type ModelGroupDeletePartial struct {
	Stage string `json:"stage"`
	// Message 必须包含**下一步怎么做**,不能只说哪里错了。
	Message string `json:"message"`
}

const (
	// StageModelSweep 是扩展库那一半(授权 / 套餐解锁 / 登记行)。
	StageModelSweep = "sweep"
	// StageModelOptions 是主库 options 那一半(倍率 / 白名单 / auto 顺序)。
	StageModelOptions = "options"
)

// ProbeModelGroupImpact 采集一个模型分组的全部影响面。
//
// **只读,一个字节都不写。** 任何一处采集失败都整体返回错误,而不是把那一项留空:
// 一份缺了一块的影响面清单和一份完整的在界面上长得一模一样,而运营会照着它按下删除。
func ProbeModelGroupImpact(ctx context.Context, mainDB, extDB *gorm.DB, name string) (ModelGroupImpact, error) {
	out := ModelGroupImpact{Name: name, Sources: make([]string, 0, 2)}
	if strings.TrimSpace(name) == "" {
		return out, fmt.Errorf("qianye/groupns: 模型分组名不能为空")
	}
	if mainDB == nil {
		return out, fmt.Errorf("qianye/groupns: 主库未初始化")
	}
	if extDB == nil {
		return out, fmt.Errorf("qianye/groupns: 扩展库未初始化")
	}
	mainDB = mainDB.WithContext(ctx)
	extDB = extDB.WithContext(ctx)
	col := model.QyCommonGroupCol()

	// ── 扩展登记行 ──────────────────────────────────────────────
	var registered ModelGroup
	switch err := extDB.Where("name = ?", name).First(&registered).Error; {
	case err == nil:
		out.Registered, out.Note = true, registered.Note
	case err == gorm.ErrRecordNotFound:
		// 未登记不是错误:登记表是 fail-open 的,一个只出现在 GroupRatio 里的
		// 名字照样要能删。
	default:
		return out, err
	}

	// ── options 四处 ────────────────────────────────────────────
	ratios := ratio_setting.GetGroupRatioCopy()
	out.BaseRatio, out.InGroupRatio = ratios[name]

	usable := setting.RawUserUsableGroupsCopy()
	out.UsableDescription, out.InUsableGroups = usable[name]

	for i, g := range setting.GetAutoGroups() {
		if g == name {
			out.AutoPosition = i + 1
			break
		}
	}

	cross, err := crossRatioUserGroups(name)
	if err != nil {
		return out, err
	}
	out.CrossRatioUserGroups = cross

	// ── 路由两个口径 ────────────────────────────────────────────
	if err := mainDB.Raw(
		"SELECT COUNT(*) FROM abilities WHERE "+col+" = ? AND enabled = ?", name, true,
	).Scan(&out.AbilityRows).Error; err != nil {
		return out, err
	}
	out.HasRoute = out.AbilityRows > 0

	channels, err := enabledChannelsBoundTo(mainDB, col, name)
	if err != nil {
		return out, err
	}
	out.EnabledChannels = channels

	// ── 令牌 ────────────────────────────────────────────────────
	//
	// 只数**启用**的令牌:已禁用的令牌本来就发不出请求,把它们算进"会当场 403 的
	// 数量"会让影响面虚高,而虚高的影响面和虚假的警报一样会被忽略。
	if err := mainDB.Raw(
		"SELECT COUNT(*) FROM tokens WHERE deleted_at IS NULL AND "+col+" = ? AND status = ?",
		name, common.TokenStatusEnabled,
	).Scan(&out.Tokens).Error; err != nil {
		return out, err
	}
	if err := mainDB.Raw(
		"SELECT COUNT(DISTINCT user_id) FROM tokens WHERE deleted_at IS NULL AND "+col+" = ? AND status = ?",
		name, common.TokenStatusEnabled,
	).Scan(&out.TokenOwners).Error; err != nil {
		return out, err
	}

	// ── pin ─────────────────────────────────────────────────────
	var pinned []UserGroup
	if err := extDB.Where("default_mode = ? AND default_model_group = ?", DefaultModePin, name).
		Find(&pinned).Error; err != nil {
		return out, err
	}
	emptyTokens, err := EmptyGroupTokenCounts(ctx, mainDB, true)
	if err != nil {
		return out, err
	}
	out.PinnedByUserGroups = make([]string, 0, len(pinned))
	for _, ug := range pinned {
		out.PinnedByUserGroups = append(out.PinnedByUserGroups, ug.Name)
		out.PinnedEmptyGroupTokens += emptyTokens[ug.Name]
	}
	sort.Strings(out.PinnedByUserGroups)

	// ── 各模块残留 ──────────────────────────────────────────────
	residues, err := ProbeModelResidues(extDB, name)
	if err != nil {
		return out, err
	}
	out.Residues = residues

	// ── 来源与闸门 ──────────────────────────────────────────────
	if out.InGroupRatio {
		out.Sources = append(out.Sources, SourceRatio)
	}
	if out.HasRoute {
		out.Sources = append(out.Sources, SourceRoute)
	}
	if len(out.Sources) == 0 {
		out.Sources = append(out.Sources, SourceRegistryOnly)
	}

	out.Blockers = modelGroupBlockers(out)
	out.NeedsForceHasRoute = out.HasRoute || out.EnabledChannels > 0
	out.NeedsForceOrphanTokens = out.Tokens > 0
	return out, nil
}

// modelGroupBlockers 是**不可覆盖**的拒绝理由。
//
// 判据只有一条:正确的下一步动作**只有运营知道**,而任何一个自动处置都等于替他
// 做一次改价或改可用性的决定。
//
//	pin        重置成 inherit 看起来最"安全",但那一档人的空分组令牌会直接回到
//	           503(那正是 pin 当初要修的东西),而正确的新默认值只有运营知道。
//	block 残留 由各模块自己声明,典型是「余额范围=仅限、且这是唯一解锁项」的套餐:
//	           那笔钱已经收过了,替他改出资来源不可接受。
func modelGroupBlockers(impact ModelGroupImpact) []string {
	out := make([]string, 0, 2)
	if len(impact.PinnedByUserGroups) > 0 {
		out = append(out, fmt.Sprintf(
			"%d 个用户分组正把它设为**默认模型分组**(%s),名下共 %d 个空分组令牌。"+
				"删掉之后这些令牌下一次请求全部失败,而正确的新默认值只有你知道 —— "+
				"请先在「用户分组」页把它们改成 inherit 或换一个仍然有渠道的模型分组",
			len(impact.PinnedByUserGroups), strings.Join(impact.PinnedByUserGroups, "、"),
			impact.PinnedEmptyGroupTokens))
	}
	for _, r := range BlockingResidues(impact.Residues) {
		out = append(out, fmt.Sprintf("%s(%s):%d 行。%s", r.Label, r.Table, r.Rows, r.Detail))
	}
	return out
}

// crossRatioUserGroups 列出在 options.GroupGroupRatio 里给这个模型分组配过
// 交叉倍率的用户分组。
//
// 读 ratio_setting 的内存快照而不是查 options 表:热路径乘的就是这一份,
// 管理端必须与它同源,否则又会出现"界面显示 A、实际乘 B"。
func crossRatioUserGroups(modelGroup string) ([]string, error) {
	raw := ratio_setting.GroupGroupRatio2JSONString()
	m := map[string]map[string]float64{}
	if raw != "" {
		if err := common.UnmarshalJsonStr(raw, &m); err != nil {
			return nil, fmt.Errorf("上游 GroupGroupRatio 不是合法 JSON: %w", err)
		}
	}
	out := make([]string, 0, len(m))
	for userGroup, row := range m {
		if _, ok := row[modelGroup]; ok {
			out = append(out, userGroup)
		}
	}
	sort.Strings(out)
	return out, nil
}

// enabledChannelsBoundTo 数有多少个 enabled 渠道把这个名字写进了 channels.group。
//
// channels.group 是逗号串,所以只能拉出来在 Go 侧展开 —— 没有一种在三种方言上都
// 成立的 SQL 拆分。这是冷路径(管理端影响面),渠道数是百量级。
//
// **刻意不 TrimSpace**,与 api_admin.go 的 enabledChannelCountsByGroup 同口径:
// 真正的路由索引(model/channel_cache.go 与 model/ability.go)都是
// `strings.Split(channel.Group, ",")` 原样落键。替它把空格抹平会让
// `"池A, 池B"` 这种写法在影响面里显示成绑定了「池B」,而 abilities 里的键是 `" 池B"`。
func enabledChannelsBoundTo(mainDB *gorm.DB, col, name string) (int64, error) {
	var rows []struct {
		Grp string `gorm:"column:grp"`
	}
	if err := mainDB.Raw(
		"SELECT "+col+" AS grp FROM channels WHERE status = ?", common.ChannelStatusEnabled,
	).Scan(&rows).Error; err != nil {
		return 0, err
	}
	var count int64
	for _, row := range rows {
		for _, g := range strings.Split(row.Grp, ",") {
			if g == name {
				count++
				break
			}
		}
	}
	return count, nil
}

// DeleteModelGroup 删除一个模型分组并清掉它在扩展库与 options 里的全部引用。
//
// 闸门与顺序见文件头。返回的 Impact 是**执行前**那一份 —— 它进审计正文,
// 事后复盘"删的时候站上是什么样"只能靠它。
func DeleteModelGroup(ctx context.Context, mainDB, extDB *gorm.DB, name string,
	opts DeleteModelGroupOptions, operatorId int) (ModelGroupDeleteResult, error) {
	res := ModelGroupDeleteResult{Name: name, RemovedFrom: make([]string, 0, 4)}

	impact, err := ProbeModelGroupImpact(ctx, mainDB, extDB, name)
	if err != nil {
		return res, err
	}
	res.Impact = impact

	// 这个名字在任何一处都不存在 ⇒ 404,而不是 200 + 一条 result=ok 的审计。
	//
	// 此前这里对一个根本不存在的名字照常返回成功,而**兄弟动词的口径是反的**:
	// DELETE .../user-groups/<不存在> 回 400 qy_groupns_unknown(lookupUserGroup
	// 有这道判据),DELETE .../model-groups/<不存在> 回 200。两层代价:
	// 界面上管理员打错一个名字得到的是"删除成功",他会以为删掉了;台账上
	// 留下的是一条与真实删除在结果字段上完全一致的记录,而事故复盘读的就是它。
	//
	// 判据取「登记行 + 四处 options + 路由」的并集:只要它在任何一处露过面,
	// 这次删除就有可清理的东西,照常往下走(那正是"清理残留"这个用途)。
	if !impact.Registered && !impact.InGroupRatio && !impact.InUsableGroups &&
		impact.AutoPosition == 0 && !impact.HasRoute && impact.AbilityRows == 0 &&
		impact.EnabledChannels == 0 && impact.Tokens == 0 &&
		len(impact.CrossRatioUserGroups) == 0 && len(impact.PinnedByUserGroups) == 0 {
		return res, fmt.Errorf(
			"qy_modelgroup_unknown: 模型分组 %q 既没有登记行,分组倍率表 / 全局可选清单 / "+
				"auto 顺序 / 路由里也都没有它 —— 名字可能拼错了,或者它已经被删掉了。"+
				"这一次什么都没有删", name)
	}

	if len(impact.Blockers) > 0 {
		return res, fmt.Errorf("qy_modelgroup_blocked: %s", strings.Join(impact.Blockers, "\n"))
	}
	if impact.NeedsForceHasRoute && !opts.ForceHasRoute {
		return res, fmt.Errorf(
			"qy_modelgroup_has_route: 这个模型分组还绑着 %d 个 enabled 渠道(channels 口径)、"+
				"abilities 里还有 %d 行 enabled 路由。删掉名字**不会**动这些渠道 —— "+
				"结果是「有渠道但谁都选不到」,而且它一旦从分组倍率表里消失,"+
				"任何仍然走到它的请求都会按 fail-open 的 1.0 倍静默计费。"+
				"请先在渠道页把这些渠道改挂到别的模型分组或停用它们;"+
				"确实要保留渠道绑定再删名字,请显式勾选强制覆盖",
			impact.EnabledChannels, impact.AbilityRows)
	}
	if impact.NeedsForceOrphanTokens && !opts.ForceOrphanTokens {
		return res, fmt.Errorf(
			"qy_modelgroup_orphan_tokens: %d 个启用中的令牌(属于 %d 个用户)正显式指向它。"+
				"删掉之后它们每一次请求都会被拒(「分组已被弃用」)。"+
				"本接口**不会**替这些令牌改分组 —— 那等于替用户改了计费分组。"+
				"请先让用户自行改分组、或在「用户分组」页的孤儿令牌面板逐条置空;"+
				"确实要把它们变成孤儿,请显式勾选强制覆盖",
			impact.Tokens, impact.TokenOwners)
	}

	// ── 第一半:扩展库(原子) ───────────────────────────────────
	err = extDB.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if err := SweepModelResidues(tx, name); err != nil {
			return err
		}
		return tx.Where("name = ?", name).Delete(&ModelGroup{}).Error
	})
	if err != nil {
		// 扩展库那一半失败 ⇒ 一个字节都没改(事务回滚),options 也还没碰。
		// 这是最干净的失败,直接把错误抛回去。
		return res, err
	}
	if reloadErr := InvalidateAndReload(); reloadErr != nil {
		common.SysError("qianye/groupns: 删除模型分组后重载登记快照失败: " + reloadErr.Error())
	}

	// ── 第二半:主库 options(原子) ─────────────────────────────
	removed, err := removeModelGroupFromOptions(name)
	res.RemovedFrom = removed
	if err != nil {
		res.Partial = &ModelGroupDeletePartial{
			Stage: StageModelOptions,
			Message: fmt.Sprintf(
				"扩展库那一半已经提交(授权 / 套餐解锁 / 登记行已清),但主库 options 没写成:%s。"+
					"当前线上状态:分组名仍然在分组倍率表 / 全局可选清单 / auto 顺序里,"+
					"而它的授权已经没了 —— 表现是一批用户少一个可选分组,**没有任何一笔钱会算错**。"+
					"请**再按一次删除**(两半都是幂等的);"+
					"若反复失败,可以在「模型分组」页手工把这一行从分组倍率表里去掉",
				err.Error()),
		}
		common.SysError("qianye/groupns: 删除模型分组 " + name + " 的 options 半程失败: " + err.Error())
		return res, nil
	}

	common.SysLog(fmt.Sprintf(
		"qianye/groupns: 操作者 %d 删除模型分组 %q,已清理 options:%s",
		operatorId, name, strings.Join(removed, "、")))
	return res, nil
}

// removeModelGroupFromOptions 把这个名字从四处 options 里摘掉,**一次事务写完**。
//
// 走 model.UpdateOptionsBulk 而不是四次 UpdateOption:后者的第三次失败会留下
// 「倍率删了、白名单没删」这种半成状态,而这四项互相解释(白名单里有、倍率表里
// 没有 = 正在按 1.0 静默计费),拆开写就是在制造那种状态。
// UpdateOptionsBulk 逐个检查 DB 错误并整体回滚,再统一更新内存快照。
func removeModelGroupFromOptions(name string) ([]string, error) {
	values := map[string]string{}
	removed := make([]string, 0, 4)

	ratios := ratio_setting.GetGroupRatioCopy()
	if _, ok := ratios[name]; ok {
		delete(ratios, name)
		payload, err := common.Marshal(ratios)
		if err != nil {
			return removed, err
		}
		values["GroupRatio"] = string(payload)
		removed = append(removed, "GroupRatio")
	}

	usable := setting.RawUserUsableGroupsCopy()
	if _, ok := usable[name]; ok {
		delete(usable, name)
		payload, err := common.Marshal(usable)
		if err != nil {
			return removed, err
		}
		values["UserUsableGroups"] = string(payload)
		removed = append(removed, "UserUsableGroups")
	}

	autoGroups := setting.GetAutoGroups()
	kept := make([]string, 0, len(autoGroups))
	for _, g := range autoGroups {
		if g != name {
			kept = append(kept, g)
		}
	}
	if len(kept) != len(autoGroups) {
		payload, err := common.Marshal(kept)
		if err != nil {
			return removed, err
		}
		values["AutoGroups"] = string(payload)
		removed = append(removed, "AutoGroups")
	}

	cross := map[string]map[string]float64{}
	if raw := ratio_setting.GroupGroupRatio2JSONString(); raw != "" {
		if err := common.UnmarshalJsonStr(raw, &cross); err != nil {
			return removed, fmt.Errorf("上游 GroupGroupRatio 不是合法 JSON: %w", err)
		}
	}
	changed := false
	cleaned := make(map[string]map[string]float64, len(cross))
	for userGroup, row := range cross {
		if _, ok := row[name]; ok {
			delete(row, name)
			changed = true
		}
		// 整行为空时连外层键都不写:留一个空对象只会让 options 里堆满永远不会被
		// 读到的空壳(与 groupmatrix.marshalRatioMatrix 同口径)。
		if len(row) > 0 {
			cleaned[userGroup] = row
		}
	}
	if changed {
		payload, err := common.Marshal(cleaned)
		if err != nil {
			return removed, err
		}
		values["GroupGroupRatio"] = string(payload)
		removed = append(removed, "GroupGroupRatio")
	}

	sort.Strings(removed)
	if len(values) == 0 {
		return removed, nil
	}
	if err := model.UpdateOptionsBulk(values); err != nil {
		return removed, err
	}
	return removed, nil
}
