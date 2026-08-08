package groupmatrix

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"sort"
	"strings"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/qianye/config"
	"github.com/QuantumNous/new-api/qianye/db"
	"github.com/QuantumNous/new-api/qianye/guard"
	"github.com/QuantumNous/new-api/service"
	"github.com/QuantumNous/new-api/setting/ratio_setting"

	"github.com/gin-gonic/gin"
	"gorm.io/gorm"
)

// preview.go —— 保存前的影响面:这次改动会让哪些令牌/用户失去哪些分组。
//
// ══════════════════════════ 两层证据,缺一不可 ══════════════════════════
//
//	静态计数(本文件 + orphans.go)  回答「理论上谁会被挡」
//	日志聚合(trafficEvidence)      回答「过去 N 天谁真的在用」
//
// 两个都要:「有 107 个令牌挂在 vip 上」和「过去 7 天有 3 个用户在 vip 上真的
// 发过请求」是两个决策 —— 前者决定要不要通知,后者决定敢不敢今晚就切。
// 545 条孤儿里 521 条"启用",但启用不等于在跑。
//
// ══════════════════════════ 块 A 与块 B 绝不合并 ══════════════════════════
//
// A(本次新增的破坏)与 B(改动之前就已经坏了的)必须分开合计:
// 存量的几百条会把新增的 3 条淹掉,而存量是历史欠账、新增是这次的责任。

// tokenActiveDays 是"最近还在用"的判据窗口。
const tokenActiveDays = 30

// maxTrafficRows 是日志聚合一次最多返回的 (分组, 用户) 行数。
// 超出即标记截断 —— 一条没有上界的聚合会在日志库上跑很久。
const maxTrafficRows = 5000

// maxAutoGroupRows 是 auto_groups 候选检查一次最多解析的令牌行数。
// JSON 只能在 Go 里解(三库 JSON 函数不兼容),所以行数必须有界。
const maxAutoGroupRows = 5000

type previewReq struct {
	// UserGroups 限定评估范围。空 = 全部已接管的用户分组。
	UserGroups []string `json:"user_groups"`
	// Cells 是**尚未落库**的草稿动作。纯只读,不会写任何东西。
	Cells []Cell `json:"cells"`
}

// pairImpact 是一个 (用户分组, 模型分组) 组合的受影响面。
//
// JSON 名与 web/src/features/qy/pages/admin-group-matrix/types.ts 的
// QyGmImpactPair **逐字对齐**。这是三路之间唯一的契约声明处,改任一侧都要先改它。
type pairImpact struct {
	UserGroup  string `json:"user_group"`
	ModelGroup string `json:"model_group"`

	Users        int64 `json:"user_count"`
	Tokens       int64 `json:"token_count"`
	TokensActive int64 `json:"token_enabled_count"`
	// TokensRecent 是最近 30 天有过访问的令牌数。它比"启用数"更接近真相:
	// 启用只说明没人去关它。
	TokensRecent int64 `json:"token_active_30d"`

	// Requests 来自日志库(L2)。**指针**:null = 日志库没查成功(不可用/超时/截断),
	// 不是 0。把"没查"下发成 0,运营会以为这条边没人在用而直接切 enforce。
	Requests     *int64 `json:"traffic_requests"`
	RequestUsers *int64 `json:"traffic_users"`

	Samples []tokenSample `json:"samples"`
	// SamplesTruncated 说明这一对的样本只展示了前若干条。
	SamplesTruncated bool `json:"samples_truncated"`
}

type tokenSample struct {
	TokenId   int    `json:"token_id"`
	TokenName string `json:"token_name"`
	// MaskedKey 只留首尾各 4 位。影响面报告是给运营定位用的,不是给它发 key 的。
	MaskedKey  string `json:"key_masked"`
	UserId     int    `json:"user_id"`
	Username   string `json:"username"`
	Group      string `json:"group"`
	Status     int    `json:"status"`
	AccessedAt int64  `json:"accessed_time"`
}

// newlyAllowedPair 是"放开一个组合"的说明。放开同样是金额事件:
// 该组合有没有专属倍率、没有的话会回落到哪个兜底倍率,必须在按保存之前看到。
type newlyAllowedPair struct {
	UserGroup  string `json:"user_group"`
	ModelGroup string `json:"model_group"`
	UserCount  int64  `json:"user_count"`
	// HasOverride 为 true 时 EffectiveRatio 是该组合的专属倍率,否则是兜底倍率。
	HasOverride bool   `json:"has_override"`
	Source      string `json:"source"`
	// EffectiveRatio 是十进制字符串(理由同 modelGroupRow.BaseRatio)。
	EffectiveRatio string `json:"effective_ratio"`
	HasChannels    bool   `json:"has_channels"`
}

// caseNearMissPair 是一对仅大小写不同的分组名。**不折叠、只告警。**
type caseNearMissPair struct {
	Left        string `json:"left"`
	Right       string `json:"right"`
	LeftSource  string `json:"left_source"`
	RightSource string `json:"right_source"`
}

// orphanGroupName 是一个不在 options.GroupRatio 里的分组名 —— 这批正按 1.0 兜底扣费。
type orphanGroupName struct {
	Group  string `json:"group"`
	Source string `json:"source"`
	Count  int64  `json:"count"`
}

type previewResult struct {
	// A:本次改动新造成的破坏。**运营点保存前唯一必须看懂的数字。**
	NewlyBroken []pairImpact `json:"newly_broken"`
	// B:改动之前就已经坏了的。单独一块、单独合计、不与 A 合并。
	AlreadyBroken []pairImpact `json:"already_broken"`
	// C:本次放开的组合。
	NewlyAllowed []newlyAllowedPair `json:"newly_allowed"`
	// D 曾经是「被权威清单接管、因此不再生效的上游 +:/-: 规则」。
	// 上游 GroupSpecialUsableGroup 已整体下线(它从来没有真正生效过,理由见
	// setting/ratio_setting/group_ratio.go),这一块随之删除 —— 一份恒为空的
	// 报表比没有报表更容易让人以为"确实没有规则被接管"。
	// E:权威清单不含用户分组自己。推翻了上游存在多年的不变量,是警告不是拦截。
	SelfExcluded []string `json:"self_excluded"`
	// F:大小写近似的分组名。
	CaseNearMiss []caseNearMissPair `json:"case_near_miss"`
	// G:清单/users.group/tokens.group 里不在分组倍率表的名字。
	OrphanGroupNames []orphanGroupName `json:"orphan_group_names"`

	// LogDays 是 L2 聚合实际用了几天 —— 界面上那句「近 N 天 M 次请求」的 N。
	LogDays int `json:"log_days"`

	// EmptyGroupTokens 是分组为空的令牌数。**单独一栏并显式标注结构性免疫** ——
	// 不写出来的话,运营看到几百个空分组令牌会以为要炸。
	EmptyGroupTokens int64  `json:"empty_group_tokens"`
	EmptyGroupNote   string `json:"empty_group_note"`

	// AutoGroupsShrink 是 auto_groups 里含已失效分组的令牌数。
	// 它的失败形状是"候选列表被静默缩短",没有任何其它信号,必须单列。
	AutoGroupsShrink int64 `json:"auto_groups_shrink"`

	TotalNewlyBrokenTokens   int64 `json:"total_newly_broken_tokens"`
	TotalAlreadyBrokenTokens int64 `json:"total_already_broken_tokens"`

	// Incomplete 为 true 时**禁止切 enforce**:宁可切不了,
	// 也不能在看不见影响面的情况下切。
	Incomplete bool     `json:"preview_incomplete"`
	Notes      []string `json:"notes"`
	// ApproximateUserGroup 说明日志里的 group 是**当时的**使用分组,
	// 而 users.group 是**当前**用户分组,期间改过分组的会归错组。不假装精确。
	ApproximateUserGroup bool `json:"approximate_user_group"`

	DraftHash     string `json:"draft_hash"`
	ImpactHash    string `json:"impact_hash"`
	BaseRatioHash string `json:"base_ratio_hash"`
}

func adminPreview(c *gin.Context) {
	if !guard.RequireAPI(c, guard.FlagGroupMatrix) {
		return
	}
	var req previewReq
	if err := c.ShouldBindJSON(&req); err != nil {
		badRequest(c, "请求体格式错误")
		return
	}
	cells, err := normalizeCells(req.Cells)
	if err != nil {
		badRequest(c, err.Error())
		return
	}
	req.Cells = cells

	res, err := runPreview(req)
	if err != nil {
		internalError(c, err)
		return
	}
	respond(c, res)
}

// previewDigest 重算某个用户分组在**当前已落库清单**下的影响面指纹。
//
// 切 enforce 时服务端拿它与客户端回传的 impact_hash 比对:防的是
// 「预览的是 A、保存的是 B」和「预览到保存之间有人建了新令牌 / 改了倍率表」。
func previewDigest(userGroup string) (*previewResult, error) {
	return runPreview(previewReq{UserGroups: []string{userGroup}})
}

func runPreview(req previewReq) (*previewResult, error) {
	cfgv := config.Get().GroupMatrix
	res := &previewResult{
		NewlyBroken: make([]pairImpact, 0), AlreadyBroken: make([]pairImpact, 0),
		NewlyAllowed: make([]newlyAllowedPair, 0),
		SelfExcluded: make([]string, 0), CaseNearMiss: make([]caseNearMissPair, 0),
		OrphanGroupNames: make([]orphanGroupName, 0), Notes: make([]string, 0),
		LogDays: previewLogDays(cfgv.PreviewLogDays),
		EmptyGroupNote: "这些令牌的分组为空,它们**结构性免疫**本次收紧 —— " +
			"上游 middleware/auth.go 的 `if tokenGroup != \"\"` 让空分组令牌整段绕过可选性检查," +
			"直接用 users.group 当使用分组。把一个用户分组从它自己的清单里删掉," +
			"影响的只是「能不能把令牌显式钉在它上面」,不会让这些用户发不出请求。",
	}

	gdb := db.Get()
	if gdb == nil {
		return nil, db.ErrNotReady
	}
	scopes, err := loadScopes(gdb)
	if err != nil {
		return nil, err
	}
	grants, err := loadGrants(gdb)
	if err != nil {
		return nil, err
	}
	ratios, baseHash, err := loadRatioMatrix()
	if err != nil {
		return nil, err
	}
	res.BaseRatioHash = baseHash
	if res.DraftHash, err = hashCells(req.Cells); err != nil {
		return nil, err
	}

	targets := previewTargets(req.UserGroups, scopes, req.Cells)
	proposed := proposedGrants(targets, scopes, grants, req.Cells)

	// 当前实际生效的可选集合。走 service.GetUserUsableGroups 而不是自己重算 ——
	// 自己重算就是上游那套 +:/-: 逻辑的第二份拷贝,它会自己漂移,
	// 而漂移的方向恰好是"预览说会坏、实际没坏"或者更糟的反过来。
	currentAllowed := make(map[string]map[string]struct{}, len(targets))
	for _, ug := range targets {
		set := map[string]struct{}{}
		for name := range service.GetUserUsableGroups(ug) {
			set[name] = struct{}{}
		}
		currentAllowed[ug] = set
	}

	stats, statErr := tokenPairStats()
	if statErr != nil {
		res.Incomplete = true
		res.Notes = append(res.Notes, "统计主库令牌失败,影响面不完整: "+statErr.Error())
	}
	res.EmptyGroupTokens = stats.emptyGroup
	res.AutoGroupsShrink = stats.autoGroupsShrink

	pairLimit := cfgv.MaxPreviewPairs
	if pairLimit <= 0 {
		pairLimit = 500
	}
	pairs := 0

	brokenPairs := make([]pairImpact, 0)
	for _, ug := range targets {
		cur, prop := currentAllowed[ug], proposed[ug]
		if _, self := prop[ug]; !self {
			res.SelfExcluded = append(res.SelfExcluded, ug)
		}
		// A:现在能用、改完不能用。
		for mg := range cur {
			if mg == autoGroup {
				continue
			}
			if _, ok := prop[mg]; ok {
				continue
			}
			pairs++
			if pairs > pairLimit {
				res.Incomplete = true
				continue
			}
			brokenPairs = append(brokenPairs, stats.impact(ug, mg))
		}
		// C:现在不能用、改完能用。
		for mg := range prop {
			if _, ok := cur[mg]; ok {
				continue
			}
			pairs++
			if pairs > pairLimit {
				res.Incomplete = true
				continue
			}
			res.NewlyAllowed = append(res.NewlyAllowed, describeNewlyAllowed(ug, mg, ratios, stats))
		}
	}
	sortPairs(brokenPairs)
	res.NewlyBroken = brokenPairs

	// B:改动之前就已经坏了的 —— 令牌分组不在**当前**可选集合里。
	// 与本次改动无关,必须单独摆出来,否则第一次预览的巨大数字会让人直接放弃这个功能。
	for _, s := range stats.pairs {
		if s.ModelGroup == "" {
			continue
		}
		cur, tracked := currentAllowed[s.UserGroup]
		if !tracked {
			cur = map[string]struct{}{}
			for name := range service.GetUserUsableGroups(s.UserGroup) {
				cur[name] = struct{}{}
			}
			currentAllowed[s.UserGroup] = cur
		}
		if _, ok := cur[s.ModelGroup]; ok {
			continue
		}
		res.AlreadyBroken = append(res.AlreadyBroken, stats.impact(s.UserGroup, s.ModelGroup))
	}
	sortPairs(res.AlreadyBroken)

	for _, p := range res.NewlyBroken {
		res.TotalNewlyBrokenTokens += p.Tokens
	}
	for _, p := range res.AlreadyBroken {
		res.TotalAlreadyBrokenTokens += p.Tokens
	}

	// 样本与流量证据只对 A 块取:B 块是历史欠账,由 orphans 端点常驻导出。
	if !res.Incomplete {
		attachSamples(res.NewlyBroken, cfgv.PreviewSampleLimit)
	} else {
		// 超限时**只给合计不给样本**:一份不完整的样本比没有样本更容易误导。
		res.Notes = append(res.Notes, fmt.Sprintf(
			"受影响组合超过 group_matrix.max_preview_pairs(%d),已停止展开明细;"+
				"preview_incomplete=true 时禁止切 enforce", pairLimit))
	}
	if trunc := attachTraffic(res.NewlyBroken, cfgv.PreviewLogDays); trunc {
		res.Notes = append(res.Notes, "日志聚合结果被截断,流量证据不完整")
		res.Incomplete = true
	}
	res.ApproximateUserGroup = true

	res.CaseNearMiss = caseNearMiss(targets, proposed)
	res.OrphanGroupNames = orphanGroupNames(proposed, stats)

	sort.Strings(res.SelfExcluded)

	if res.ImpactHash, err = hashImpact(res); err != nil {
		return nil, err
	}
	return res, nil
}

// previewTargets 是本次要评估的用户分组集合。
func previewTargets(explicit []string, scopes map[string]Scope, cells []Cell) []string {
	seen := map[string]struct{}{}
	for _, ug := range explicit {
		if ug = strings.TrimSpace(ug); ug != "" {
			seen[ug] = struct{}{}
		}
	}
	if len(seen) == 0 {
		for ug := range scopes {
			seen[ug] = struct{}{}
		}
	}
	// 草稿里出现过的分组一定要评估:运营正在编辑的那一行不能从报告里漏掉。
	for _, cell := range cells {
		seen[cell.UserGroup] = struct{}{}
	}
	out := make([]string, 0, len(seen))
	for ug := range seen {
		out = append(out, ug)
	}
	sort.Strings(out)
	return out
}

// proposedGrants 把草稿动作叠加在已落库清单上,得出"改完之后"的可选集合。
//
// ══════════════ 尚未接管的用户分组按**预填**算,不是按空集算 ══════════════
//
// adminPutScope 首次接管时会用 service.GetUserUsableGroups 的实际结果预填 grants
// (零行为变更是硬要求)。预览这一侧若按"没有 grant 行 = 什么都不许"算,
// 「首次接管 + 直接 enforce」这条路上就会把该分组**当前能用的每一个模型分组**
// 连同令牌数与真实请求数全部列进块 A —— 那道专门为切 enforce 而设的闸门,
// 在它唯一必需的场景下系统性地报假警。
//
// 两种后果都坏:运营被吓退放弃这个功能;或者他确认一次发现"预览在胡说",
// 从此对块 A 脱敏 —— 而块 A 是撤销操作唯一的护栏。
func proposedGrants(targets []string, scopes map[string]Scope,
	grants map[string]map[string]struct{}, cells []Cell) map[string]map[string]struct{} {
	out := make(map[string]map[string]struct{}, len(targets))
	for _, ug := range targets {
		set := map[string]struct{}{}
		if _, managed := scopes[ug]; !managed {
			for name := range service.GetUserUsableGroups(ug) {
				if name == autoGroup {
					continue
				}
				set[name] = struct{}{}
			}
		}
		for mg := range grants[ug] {
			set[mg] = struct{}{}
		}
		out[ug] = set
	}
	for _, cell := range cells {
		set, ok := out[cell.UserGroup]
		if !ok {
			continue
		}
		switch cell.Action {
		case ActionGrant:
			set[cell.ModelGroup] = struct{}{}
		case ActionRevoke:
			delete(set, cell.ModelGroup)
		}
	}
	return out
}

func describeNewlyAllowed(userGroup, modelGroup string, ratios ratioMatrix, stats *pairStats) newlyAllowedPair {
	p := newlyAllowedPair{
		UserGroup: userGroup, ModelGroup: modelGroup,
		UserCount:      stats.byKey[pairKey{userGroup, modelGroup}].Users,
		Source:         SourceInherit,
		EffectiveRatio: ratioText(ratio_setting.GetGroupRatio(modelGroup)),
	}
	if v, ok := ratios[userGroup][modelGroup]; ok {
		p.HasOverride, p.Source, p.EffectiveRatio = true, SourceOverride, ratioText(v)
	}
	// 判据与 modelGroupRow.HasChannels 同源,而后者**只对列轴成员存在**,
	// 列轴又是 options.GroupRatio 的键派生的。所以这里两条都要判:
	// 一个已从倍率表消失、但 abilities 里仍有启用渠道的模型分组
	// (站上真实存在这一类)只判 abilities 会得到 has_channels=true,
	// 读作"放开它用户就能用" —— 而保存会被 validateCells 以「不在分组倍率表里」
	// 400 掉,请求期也会被上游「分组已被弃用」挡人。
	// 预览与保存对同一份草稿给出相反结论,是这份报告最不该有的形状。
	//
	// 刻意不走 listModelGroups:那个函数现在还要读两张登记表与全局白名单,
	// 而这里只需要一个布尔 —— 为一个布尔重建整条列轴,而且是在一个逐对调用的
	// 循环里,代价与信息量完全不成比例。
	p.HasChannels = ratio_setting.ContainsGroupRatio(modelGroup) &&
		groupsWithEnabledAbilities()[modelGroup]
	return p
}

// caseNearMiss 列出仅大小写不同的分组名对。**不折叠、只告警**,
// 理由见 matrixWarnings:倍率侧是精确 map 查找且在计费路径上,我们无权改。
func caseNearMiss(targets []string, proposed map[string]map[string]struct{}) []caseNearMissPair {
	type named struct{ name, source string }
	byLower := map[string][]named{}
	seen := map[string]struct{}{}
	add := func(name, source string) {
		key := name + "|" + source
		if _, dup := seen[key]; dup {
			return
		}
		seen[key] = struct{}{}
		lower := strings.ToLower(name)
		byLower[lower] = append(byLower[lower], named{name, source})
	}
	for _, ug := range targets {
		add(ug, "用户分组")
		for mg := range proposed[ug] {
			add(mg, "模型分组")
		}
	}

	out := make([]caseNearMissPair, 0)
	for _, group := range byLower {
		sort.Slice(group, func(i, j int) bool {
			if group[i].name != group[j].name {
				return group[i].name < group[j].name
			}
			return group[i].source < group[j].source
		})
		for i := range group {
			for j := i + 1; j < len(group); j++ {
				if group[i].name == group[j].name {
					continue // 同名不同轴不是近似项,是方案 3 已知的命名空间共用
				}
				out = append(out, caseNearMissPair{
					Left: group[i].name, LeftSource: group[i].source,
					Right: group[j].name, RightSource: group[j].source,
				})
			}
		}
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Left != out[j].Left {
			return out[i].Left < out[j].Left
		}
		return out[i].Right < out[j].Right
	})
	return out
}

// orphanGroupNames 列出不在分组倍率表里的分组名 —— 这批正按 1.0 兜底静默扣费。
//
// Count 是"有多少条数据挂在这个名字上"(用户数 / 令牌数),Source 说明它来自哪里。
// 只给名字不给量级,运营无法判断先修哪一个。
func orphanGroupNames(proposed map[string]map[string]struct{}, stats *pairStats) []orphanGroupName {
	seen := map[string]*orphanGroupName{}
	check := func(name, source string, count int64) {
		if name == "" || name == autoGroup {
			return
		}
		if ratio_setting.ContainsGroupRatio(name) {
			return
		}
		row, ok := seen[name]
		if !ok {
			row = &orphanGroupName{Group: name, Source: source}
			seen[name] = row
		}
		if !strings.Contains(row.Source, source) {
			row.Source += " / " + source
		}
		row.Count += count
	}
	for ug, set := range proposed {
		check(ug, "grants", 0)
		for mg := range set {
			check(mg, "grants", 0)
		}
	}
	for _, p := range stats.pairs {
		check(p.UserGroup, "users.group", p.Users)
		check(p.ModelGroup, "tokens.group", p.Tokens)
	}
	out := make([]orphanGroupName, 0, len(seen))
	for _, row := range seen {
		out = append(out, *row)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Group < out[j].Group })
	return out
}

func sortPairs(ps []pairImpact) {
	sort.Slice(ps, func(i, j int) bool {
		if ps[i].Tokens != ps[j].Tokens {
			return ps[i].Tokens > ps[j].Tokens
		}
		if ps[i].UserGroup != ps[j].UserGroup {
			return ps[i].UserGroup < ps[j].UserGroup
		}
		return ps[i].ModelGroup < ps[j].ModelGroup
	})
}

// hashImpact 覆盖统计结果(不含它自己与草稿指纹)。
func hashImpact(res *previewResult) (string, error) {
	shallow := struct {
		NewlyBroken   []pairImpact       `json:"a"`
		AlreadyBroken []pairImpact       `json:"b"`
		NewlyAllowed  []newlyAllowedPair `json:"c"`
		SelfExcluded  []string           `json:"e"`
		Empty         int64              `json:"empty"`
		Incomplete    bool               `json:"inc"`
	}{res.NewlyBroken, res.AlreadyBroken, res.NewlyAllowed, res.SelfExcluded,
		res.EmptyGroupTokens, res.Incomplete}
	b, err := common.Marshal(shallow)
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:]), nil
}

// ─────────────────────── 主库统计(L0 / L1 的数据源)───────────────────────

type pairKey struct{ UserGroup, ModelGroup string }

type pairRow struct {
	UserGroup    string
	ModelGroup   string
	Users        int64
	Tokens       int64
	TokensActive int64
	TokensRecent int64
}

type pairStats struct {
	pairs            []pairRow
	byKey            map[pairKey]pairRow
	emptyGroup       int64
	autoGroupsShrink int64
}

func (s *pairStats) impact(userGroup, modelGroup string) pairImpact {
	r := s.byKey[pairKey{userGroup, modelGroup}]
	return pairImpact{
		UserGroup: userGroup, ModelGroup: modelGroup,
		Users: r.Users, Tokens: r.Tokens,
		TokensActive: r.TokensActive, TokensRecent: r.TokensRecent,
		Samples: make([]tokenSample, 0),
	}
}

// tokenPairStats 一次性把 (users.group, tokens.group) 的全部计数拉回来。
//
// 三条聚合查询而不是每对一条:本站 2684 令牌 / 1260 用户,三条 GROUP BY 是几毫秒,
// 而按对循环会在 (7×15) 的矩阵上打出上百条查询。
//
// **绝不用 SUM(boolean)** —— PostgreSQL 上不可移植。"启用数"用带 status 过滤的
// 同形查询单独跑一遍。`group` 列一律走 model.QyCommonGroupCol()(三库保留字)。
func tokenPairStats() (*pairStats, error) {
	out := &pairStats{pairs: make([]pairRow, 0), byKey: map[pairKey]pairRow{}}
	if model.DB == nil {
		return out, nil
	}
	col := model.QyCommonGroupCol()
	userCol, tokenCol := "users."+col, "tokens."+col

	type agg struct {
		Ug string `gorm:"column:ug"`
		Mg string `gorm:"column:mg"`
		N  int64  `gorm:"column:n"`
		U  int64  `gorm:"column:u"`
	}

	scan := func(extra func(q *gorm.DB) *gorm.DB) ([]agg, error) {
		q := groupByRaw(model.DB.Model(&model.Token{}).
			Joins("JOIN users ON users.id = tokens.user_id").
			Select(userCol+" as ug, "+tokenCol+" as mg, count(*) as n, count(distinct tokens.user_id) as u"),
			userCol+", "+tokenCol)
		if extra != nil {
			q = extra(q)
		}
		var rows []agg
		err := q.Scan(&rows).Error
		return rows, err
	}

	all, err := scan(nil)
	if err != nil {
		return out, err
	}
	active, err := scan(func(q *gorm.DB) *gorm.DB {
		return q.Where("tokens.status = ?", common.TokenStatusEnabled)
	})
	if err != nil {
		return out, err
	}
	recent, err := scan(func(q *gorm.DB) *gorm.DB {
		return q.Where("tokens.accessed_time >= ?", common.GetTimestamp()-tokenActiveDays*86400)
	})
	if err != nil {
		return out, err
	}

	index := map[pairKey]*pairRow{}
	get := func(ug, mg string) *pairRow {
		k := pairKey{ug, mg}
		if r, ok := index[k]; ok {
			return r
		}
		r := &pairRow{UserGroup: ug, ModelGroup: mg}
		index[k] = r
		return r
	}
	for _, a := range all {
		if a.Mg == "" {
			out.emptyGroup += a.N
			continue
		}
		r := get(a.Ug, a.Mg)
		r.Tokens, r.Users = a.N, a.U
	}
	for _, a := range active {
		if a.Mg == "" {
			continue
		}
		get(a.Ug, a.Mg).TokensActive = a.N
	}
	for _, a := range recent {
		if a.Mg == "" {
			continue
		}
		get(a.Ug, a.Mg).TokensRecent = a.N
	}
	for k, r := range index {
		out.byKey[k] = *r
		out.pairs = append(out.pairs, *r)
	}
	sort.Slice(out.pairs, func(i, j int) bool {
		if out.pairs[i].UserGroup != out.pairs[j].UserGroup {
			return out.pairs[i].UserGroup < out.pairs[j].UserGroup
		}
		return out.pairs[i].ModelGroup < out.pairs[j].ModelGroup
	})

	out.autoGroupsShrink = countAutoGroupsShrink()
	return out, nil
}

// countAutoGroupsShrink 数出 auto_groups 候选里**含已失效分组**的令牌。
//
// 判据必须是"含已失效分组",不是"auto_groups 非空"。后者会把一批完全正常的
// 令牌报成风险,而这一栏的界面文案明说了「这条失败形状没有任何其它信号」——
// 运营无法自行证伪,只能要么整栏忽略(那它就白做了),要么为一批没问题的令牌
// 做一次不必要的排查。反方向同样成立:真正会被缩短的和不会被缩短的混在同一个
// 数字里,无法区分。
//
// 只能在 Go 里解 JSON:三种数据库的 JSON 函数互不兼容(AGENTS.md 硬性要求)。
// 因此有硬上界 —— 超出即停止扫描并告警,而不是让一次预览拖着几万行跑。
func countAutoGroupsShrink() int64 {
	if model.DB == nil {
		return 0
	}
	type row struct {
		AutoGroups string `gorm:"column:auto_groups"`
	}
	var rows []row
	err := model.DB.Model(&model.Token{}).
		Select("auto_groups").
		Where("auto_groups IS NOT NULL AND auto_groups <> '' AND auto_groups <> '[]'").
		Limit(maxAutoGroupRows + 1).Scan(&rows).Error
	if err != nil {
		common.SysError("qianye/groupmatrix: 统计 auto_groups 令牌失败: " + err.Error())
		return 0
	}
	if len(rows) > maxAutoGroupRows {
		rows = rows[:maxAutoGroupRows]
		common.SysError(fmt.Sprintf(
			"qianye/groupmatrix: 配了 auto 候选的令牌超过 %d 条,只检查了前 %d 条,"+
				"「候选被静默缩短」这一栏偏小", maxAutoGroupRows, maxAutoGroupRows))
	}
	var n int64
	for _, r := range rows {
		var candidates []string
		if err := common.UnmarshalJsonStr(r.AutoGroups, &candidates); err != nil {
			// 解不开的当成有问题:它同样会在 FilterUserTokenAutoGroups 里被丢掉。
			n++
			continue
		}
		for _, g := range candidates {
			if g != "" && !ratio_setting.ContainsGroupRatio(g) {
				n++
				break
			}
		}
	}
	return n
}

// attachSamples 给每一对补最多 limit 条令牌样本。
//
// 绝不返回全量令牌列表:一次预览拉回几千条带 key 的记录,本身就是一个泄漏面。
func attachSamples(pairs []pairImpact, limit int) {
	if model.DB == nil || len(pairs) == 0 {
		return
	}
	if limit <= 0 {
		limit = 20
	}
	col := model.QyCommonGroupCol()
	for i := range pairs {
		type row struct {
			Id           int
			Name         string
			Key          string
			UserId       int
			Username     string
			Grp          string `gorm:"column:grp"`
			Status       int
			AccessedTime int64
		}
		var rows []row
		// Limit(limit+1) 而不是 Limit(limit):多取一条才能区分「刚好这么多」
		// 与「还有更多」。界面上那句「样本已截断」不能靠猜。
		err := model.DB.Model(&model.Token{}).
			Joins("JOIN users ON users.id = tokens.user_id").
			Where("users."+col+" = ? AND tokens."+col+" = ?", pairs[i].UserGroup, pairs[i].ModelGroup).
			Select("tokens.id, tokens.name, tokens.key, tokens.user_id, tokens.status, tokens.accessed_time, " +
				"users.username as username, users." + col + " as grp").
			Order("tokens.accessed_time desc").Limit(limit + 1).Scan(&rows).Error
		if err != nil {
			common.SysError("qianye/groupmatrix: 取令牌样本失败: " + err.Error())
			continue
		}
		if len(rows) > limit {
			rows = rows[:limit]
			pairs[i].SamplesTruncated = true
		}
		for _, r := range rows {
			pairs[i].Samples = append(pairs[i].Samples, tokenSample{
				TokenId: r.Id, TokenName: r.Name, MaskedKey: maskKey(r.Key),
				UserId: r.UserId, Username: r.Username, Group: r.Grp,
				Status: r.Status, AccessedAt: r.AccessedTime,
			})
		}
	}
}

// previewLogDays 把配置里的日志窗口收进 [1, 31]。
//
// 上界是硬的:日志库是全站最大的一张表,一次没有上界的聚合能跑很久,
// 而这个接口挂在运营点"预览"的同步路径上。
func previewLogDays(days int) int {
	if days <= 0 {
		return 7
	}
	if days > 31 {
		return 31
	}
	return days
}

// maskKey 只留首尾各 4 位。影响面报告是给运营看的,不是给它发 key 的。
func maskKey(k string) string {
	if len(k) <= 8 {
		return "****"
	}
	return k[:4] + "****" + k[len(k)-4:]
}

// attachTraffic 是 L2:过去 N 天真的有人在这些组合上发过请求吗。
//
// 返回 true 表示结果被截断。
//
// ⚠ 已知偏差:logs.group 是**当时的**使用分组,users.group 是**当前**用户分组,
// 期间改过分组的用户会被归到错误的行上。响应里的 approximate_user_group 就是说这件事,
// 绝不假装精确。
func attachTraffic(pairs []pairImpact, days int) bool {
	if len(pairs) == 0 {
		return false
	}
	if model.QyLogDB() == nil {
		// 日志库不可用:每一对的 traffic_requests 保持 **null**(不是 0)。
		// 把"没查"下发成 0,运营会以为这条边没人在用而直接切 enforce。
		common.SysError("qianye/groupmatrix: 日志库不可用,影响面里的真实请求数一律留空(不是 0)")
		return false
	}
	days = previewLogDays(days)
	groups := make([]string, 0, len(pairs))
	seen := map[string]struct{}{}
	for _, p := range pairs {
		if _, ok := seen[p.ModelGroup]; ok {
			continue
		}
		seen[p.ModelGroup] = struct{}{}
		groups = append(groups, p.ModelGroup)
	}

	type row struct {
		Grp    string `gorm:"column:grp"`
		UserId int    `gorm:"column:user_id"`
		N      int64  `gorm:"column:n"`
	}
	col := model.QyLogGroupCol()
	var rows []row
	err := model.QyLogDB().Model(&model.Log{}).
		Select(col+" as grp, user_id, count(*) as n").
		Where(col+" IN ?", groups).
		Where("created_at >= ?", common.GetTimestamp()-int64(days)*86400).
		Group(col + ", user_id").
		Limit(maxTrafficRows + 1).
		Scan(&rows).Error
	if err != nil {
		common.SysError("qianye/groupmatrix: 日志流量聚合失败: " + err.Error())
		return true
	}
	truncated := len(rows) > maxTrafficRows
	if truncated {
		rows = rows[:maxTrafficRows]
	}

	// 把 user_id 映射回**当前**用户分组。日志库与主库可能不是同一个连接,
	// 所以不能 join,只能分别查询后在内存里合并。
	userIds := make([]int, 0, len(rows))
	for _, r := range rows {
		userIds = append(userIds, r.UserId)
	}
	userGroup := map[int]string{}
	if model.DB != nil && len(userIds) > 0 {
		type ur struct {
			Id  int    `gorm:"column:id"`
			Grp string `gorm:"column:grp"`
		}
		var us []ur
		if err := model.DB.Model(&model.User{}).
			Select("id, "+model.QyCommonGroupCol()+" as grp").
			Where("id IN ?", userIds).Scan(&us).Error; err != nil {
			common.SysError("qianye/groupmatrix: 回查用户分组失败: " + err.Error())
			truncated = true
		}
		for _, u := range us {
			userGroup[u.Id] = u.Grp
		}
	}

	// 查询成功 ⇒ 每一对都拿到一个**确定的**数字(可能是 0),因此先把指针填出来。
	// 只有"没查成功"才留 null —— 这个区分是这一栏存在的全部意义。
	agg := map[pairKey]*pairImpact{}
	for i := range pairs {
		zeroReq, zeroUsers := int64(0), int64(0)
		pairs[i].Requests, pairs[i].RequestUsers = &zeroReq, &zeroUsers
		agg[pairKey{pairs[i].UserGroup, pairs[i].ModelGroup}] = &pairs[i]
	}
	counted := map[pairKey]map[int]struct{}{}
	for _, r := range rows {
		k := pairKey{userGroup[r.UserId], r.Grp}
		p, ok := agg[k]
		if !ok {
			continue
		}
		*p.Requests += r.N
		if counted[k] == nil {
			counted[k] = map[int]struct{}{}
		}
		if _, dup := counted[k][r.UserId]; !dup {
			counted[k][r.UserId] = struct{}{}
			*p.RequestUsers++
		}
	}
	return truncated
}
