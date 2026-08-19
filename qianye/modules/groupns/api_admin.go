package groupns

// api_admin.go —— 两张登记表的管理端接口。
//
// 它同时是**路 C(前端)的契约冻结点**:三块界面(A 用户分组 / B 矩阵 / C 模型分组)
// 的候选下拉与 A 页的默认模型分组编辑全部只走这里。
//
// ══════════════════ 编辑器唯一性(硬约束,前后端共同遵守)══════════════════
//
//	default_mode + default_model_group   只在 A 页编辑(本文件)
//	grants + GroupGroupRatio             只在 B 页编辑(groupmatrix/api_admin.go)
//	GroupRatio / AutoGroups / MaxToken…  只在 C 页编辑(上游 options)
//
// 同一份数据出现在第二个页面上时必须是只读。两个编辑器编同一份数据 =
// 两份草稿 + 两道闸门,而写入跨两个库不原子。

import (
	"fmt"
	"net/http"
	"strings"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/qianye/db"
	"github.com/QuantumNous/new-api/qianye/guard"
	qymodel "github.com/QuantumNous/new-api/qianye/model"
	"github.com/QuantumNous/new-api/qianye/service/audit"
	"github.com/QuantumNous/new-api/service"
	"github.com/QuantumNous/new-api/setting"
	"github.com/QuantumNous/new-api/setting/ratio_setting"

	"github.com/gin-gonic/gin"
)

func respond(c *gin.Context, data any) {
	c.JSON(http.StatusOK, gin.H{"success": true, "message": "", "data": data})
}

func badRequest(c *gin.Context, code, msg string) {
	c.JSON(http.StatusBadRequest, gin.H{"success": false, "code": code, "message": msg})
}

// requireDefaultResolve 是**写接口专用**的第二道准入:模块本身开着吗?
//
// guard.RequireAPI(FlagCore) 判的是"整个扩展开不开",与 group_namespace 这一段
// 毫无关系。少了这一道的后果非常具体:data/qianye-prod.yaml 里**根本没有**
// group_namespace 段(GroupNamespace.Enabled 是普通 bool、零值 false),于是运营
// 为了救那些 503 的空分组令牌去配一个默认模型分组 —— 接口返回 200、写审计、
// 界面显示配好了,而 hook.go 的 `if !defaultResolveOn() { return userGroup, inherit }`
// 让它一个字节都不生效。运营的下一步必然是去查渠道和 abilities,而不是去查一个
// 他没听说过的 YAML 段。
//
// 只作用于写接口:只读的列表/报表在模块关闭时仍然应该能打开(它们正是用来
// 判断"要不要开这个模块"的)。
func requireDefaultResolve(c *gin.Context) bool {
	if defaultResolveOn() {
		return true
	}
	badRequest(c, "qy_groupns_disabled",
		"「用户分组的默认模型分组」当前未启用,保存了也不会生效。"+
			"请先在 qianye 配置文件里把 group_namespace.enabled 设为 true"+
			"(并确认 group_namespace.default_model_group_enabled 未被显式关掉)")
	return false
}

func internalError(c *gin.Context, err error) {
	common.SysError("qianye/groupns: 接口处理失败: " + err.Error())
	c.JSON(http.StatusInternalServerError, gin.H{
		"success": false, "code": "qy_internal_error", "message": "处理失败,请稍后重试",
	})
}

// UserGroupRow 是 A 页的一行。
type UserGroupRow struct {
	UserGroup
	// Users 是该用户分组下的在册用户数。
	Users int64 `json:"users"`
	// EmptyGroupTokens 是该用户分组下的**空分组令牌**数 —— 也就是配一个默认模型
	// 分组会立刻改变行为的那些令牌。它必须与配置项摆在同一行:粒度是用户分组,
	// 而每一行背后是几十个令牌,不摆出来运营就是在盲配。
	EmptyGroupTokens int64 `json:"empty_group_tokens"`
	// LegacyDual 表示这个名字**同时**是一个模型分组。存量重名,只减不增。
	LegacyDual bool `json:"legacy_dual"`
	// DefaultHasRoute 只在 pin 时有意义:配的那个模型分组此刻还有没有渠道。
	// false = 该用户分组的全部空分组令牌正在 503。
	DefaultHasRoute bool `json:"default_has_route"`
}

// ModelGroupRow 是 C 页的一行。
type ModelGroupRow struct {
	ModelGroup
	// BaseRatio 是兜底倍率,来自 options.GroupRatio(**唯一真相源**,登记表不存)。
	BaseRatio float64 `json:"base_ratio"`
	// RatioMissing 为 true 表示这个模型分组不在 GroupRatio 里 ——
	// 有路由的话,走到它的请求正在按 1.0 静默计费。
	RatioMissing bool `json:"ratio_missing"`
	// HasRoute 是 abilities 口径:存在 enabled=true 的行。
	HasRoute bool `json:"has_route"`
	// ChannelCount 是 channels 口径的 enabled 渠道数。
	//
	// **两个口径都必须给前端**,并对不一致的行标红:缓存模式下真实路由由 channels
	// 决定,而 has_route 由 abilities 决定,两者背离正是「修复数据库一致性」
	// (FixAbility)存在的理由。只给一个口径会让那种背离永远看不见。
	ChannelCount int64 `json:"channel_count"`
	// LegacyDual 表示这个名字同时是一个用户分组。
	LegacyDual bool `json:"legacy_dual"`

	// ─────────── 下面四项回答项目方那句「模型分组当前预设有点混乱」 ───────────
	//
	// 混乱的具体形状是:回填出来的名单里混着 default / group_1 这类历史残留,
	// 而界面上没有任何一处说得清「这一行是从哪来的、它现在还能不能路由」。
	// 光有 base_ratio 与 has_route 两个布尔是不够的 —— 运营看到的是两个绿点,
	// 看不到"这个名字只是被回填进来的一个空壳"。

	// Sources 是这个名字的来源集合(group_ratio / abilities / registry_only)。
	// 与 ProbeModelGroupImpact 同源同口径,见 modelgroup_store.go。
	Sources []string `json:"sources"`
	// InUsableGroups 是项目方点名的「全局(用户可选分组)」——
	// options.UserUsableGroups 里有没有这个键。
	InUsableGroups bool `json:"in_usable_groups"`
	// UsableDescription 是白名单里那段**原文**。它与 Note 同屏显示,
	// 运营才看得出哪一段被覆盖了(优先级见 setting/qy_groupnote_export.go)。
	UsableDescription string `json:"usable_description"`
	// AutoPosition 是它在 options.AutoGroups 里的位次(从 1 起),0 表示不在。
	AutoPosition int `json:"auto_position"`
}

func adminListUserGroups(c *gin.Context) {
	if !guard.RequireAPI(c, guard.FlagCore) {
		return
	}
	gdb := db.Get()
	if gdb == nil {
		internalError(c, db.ErrNotReady)
		return
	}
	var rows []UserGroup
	if err := gdb.WithContext(c).Order("sort_order asc, name asc").Find(&rows).Error; err != nil {
		internalError(c, err)
		return
	}
	userCounts := countByGroup(c, "users")
	emptyTokens, _ := EmptyGroupTokenCounts(c, model.DB, true)
	routed := routedModelGroupNames(c)

	out := make([]UserGroupRow, 0, len(rows))
	for _, row := range rows {
		item := UserGroupRow{
			UserGroup:        row,
			Users:            userCounts[row.Name],
			EmptyGroupTokens: emptyTokens[row.Name],
			LegacyDual:       routed[row.Name],
		}
		if row.DefaultMode == DefaultModePin {
			item.DefaultHasRoute, _ = HasRoute(c, model.DB, row.DefaultModelGroup)
		}
		out = append(out, item)
	}
	respond(c, gin.H{"items": out})
}

func adminListModelGroups(c *gin.Context) {
	if !guard.RequireAPI(c, guard.FlagCore) {
		return
	}
	gdb := db.Get()
	if gdb == nil {
		internalError(c, db.ErrNotReady)
		return
	}
	var rows []ModelGroup
	if err := gdb.WithContext(c).Order("sort_order asc, name asc").Find(&rows).Error; err != nil {
		internalError(c, err)
		return
	}
	ratios := ratio_setting.GetGroupRatioCopy()
	userNames := registeredUserGroupNames(c)
	channelCounts := enabledChannelCountsByGroup(c)
	// **原文**而不是经备注覆盖之后的值:这一列存在的全部理由就是让运营看见
	// "白名单里本来写着什么、现在被备注盖成了什么"。拿覆盖后的值填它,
	// 两列会显示成一模一样,而那正是"哪一份生效"这个问题的原样重现。
	usable := setting.RawUserUsableGroupsCopy()
	autoPos := map[string]int{}
	for i, g := range setting.GetAutoGroups() {
		if _, dup := autoPos[g]; !dup {
			autoPos[g] = i + 1
		}
	}

	out := make([]ModelGroupRow, 0, len(rows))
	for _, row := range rows {
		ratio, ok := ratios[row.Name]
		hasRoute, _ := HasRoute(c, model.DB, row.Name)
		desc, inUsable := usable[row.Name]
		sources := make([]string, 0, 2)
		if ok {
			sources = append(sources, SourceRatio)
		}
		if hasRoute {
			sources = append(sources, SourceRoute)
		}
		if len(sources) == 0 {
			sources = append(sources, SourceRegistryOnly)
		}
		out = append(out, ModelGroupRow{
			ModelGroup:        row,
			BaseRatio:         ratio,
			RatioMissing:      !ok,
			HasRoute:          hasRoute,
			ChannelCount:      channelCounts[row.Name],
			LegacyDual:        userNames[row.Name],
			Sources:           sources,
			InUsableGroups:    inUsable,
			UsableDescription: desc,
			AutoPosition:      autoPos[row.Name],
		})
	}
	respond(c, gin.H{"items": out})
}

// adminReport 是 S0 的三份体检报表:重名、两个方向的差集、空分组令牌分布。
//
// 它**不写任何东西**,只观测。项目方要的「先把现状看清楚」就是这个接口。
func adminReport(c *gin.Context) {
	if !guard.RequireAPI(c, guard.FlagCore) {
		return
	}
	_, _, res, err := observeGroups(c, model.DB)
	if err != nil {
		internalError(c, err)
		return
	}
	emptyTokens, _ := EmptyGroupTokenCounts(c, model.DB, true)
	respond(c, gin.H{
		"observed":                res,
		"empty_group_tokens":      emptyTokens,
		"missing_ratio_policy":    cfg().MissingRatioPolicy,
		"funding_gate_mode":       cfg().FundingGateMode,
		"shadow_funding_denies":   ShadowFundingDenies(),
		"enforced_funding_denies": EnforcedFundingDenies(),
		// 核销额度与拒绝次数分开报:前者是"请求跑完了、平台自己吃下这一段",
		// 后者是"请求根本没跑"。合成一个数字既看不出杀伤面也看不出免单量。
		"shortfall_write_offs":   ShortfallWriteOffs(),
		"default_model_group_on": defaultResolveOn(),
	})
}

func adminBackfill(c *gin.Context) {
	if !guard.RequireAPI(c, guard.FlagCore) {
		return
	}
	res, err := Backfill(c, model.DB, db.Get(), c.GetInt("id"))
	if err != nil {
		audit.WriteConfigUpdate(c, audit.ConfigChange{
			Action: "groupns.backfill", Result: qymodel.ResultFail,
			Reason: "登记回填失败: " + err.Error(),
		})
		internalError(c, err)
		return
	}
	if err := InvalidateAndReload(); err != nil {
		common.SysError("qianye/groupns: 回填后重载快照失败: " + err.Error())
	}
	audit.WriteConfigUpdate(c, audit.ConfigChange{
		Action: "groupns.backfill", Result: qymodel.ResultOK,
		Reason: "登记回填(只新增、default_mode=inherit,零行为变化)",
		After:  res,
	})
	respond(c, res)
}

type setDefaultRequest struct {
	// Mode ∈ {inherit, pin, deny}。三态必须由调用方显式给出,
	// 不接受"空 = 清除"这种约定 —— 那正是本模块在防的零值歧义。
	Mode string `json:"mode"`
	// ModelGroup 只在 Mode == pin 时使用。
	ModelGroup string `json:"model_group"`
	// ForceNoRoute 是 has_route 硬闸门的显式覆盖。它进审计。
	ForceNoRoute bool `json:"force_no_route"`
	// ForceZeroRatio 是「目标模型分组的实际倍率为 0(整档人白嫖)」这道闸门的显式覆盖。
	//
	// 它必须与 ForceNoRoute 分开:两者的后果方向完全相反 —— 一个是全组 503,
	// 另一个是全组免费,而后者不会有任何人来报障。
	ForceZeroRatio bool `json:"force_zero_ratio"`
}

// SetDefaultResult 是保存默认模型分组的返回值。
//
// 它比"保存后的那一行"多出**影响面与价格**:粒度是用户分组,而每一行背后是几十个
// 令牌;三道闸门里没有任何一道看倍率的**值**(ContainsGroupRatio 对 ratio=0 返回
// true,而 0 正是最危险的取值)。请求体、响应、审计正文里都没有金额,运营就没有
// 任何一处能看到"改完之后这批令牌按几倍计费"。
type SetDefaultResult struct {
	UserGroup
	// EffectiveRatio 是保存后这一档人走默认模型分组时**真正会被乘进账单**的倍率,
	// 即 GroupGroupRatio[用户分组][模型分组],未配则回落 GroupRatio[模型分组]。
	EffectiveRatio float64 `json:"effective_ratio"`
	// RatioSource ∈ {override, inherit},回答"这个数字是交叉格配的还是兜底来的"。
	RatioSource string `json:"ratio_source"`
	// AffectedTokens 是这一档人名下会被立刻改变行为的空分组令牌数。
	AffectedTokens int64 `json:"affected_tokens"`
	// AffectedUsers 是这一档人的在册用户数。
	AffectedUsers int64 `json:"affected_users"`
}

// adminSetDefaultModelGroup 设置一个用户分组的默认模型分组。
//
// ══════════════════ 三道保存闸门,一道都不能省 ══════════════════
//
//  1. **has_route == false 一律拒绝**(除非显式勾选覆盖,勾选进审计)。
//     配一个没有 enabled 渠道的模型分组,该用户分组的全部空分组令牌会在下一次请求
//     同时 503 —— 那正是这个功能要修的东西。
//  2. **必须在该用户分组的可选清单里**。否则默认值就是绕过权威可选清单的后门:
//     运营能通过"设默认"把一个本来不许选的分组塞给整组用户。
//  3. **必须在分组倍率表里**。不在的话计费会走 fail-open 的 1.0,
//     等于把整组用户的价静默改成原价。
//
// 三道闸门与 middleware/auth.go 的 pin 运行时校验**是同一组判据**,顺序也一致。
// 保存时拦住是主要防线,运行时那道是"有人绕过接口改了库"的兜底。
func adminSetDefaultModelGroup(c *gin.Context) {
	if !guard.RequireAPI(c, guard.FlagCore) || !requireDefaultResolve(c) {
		return
	}
	name := strings.TrimSpace(c.Param("name"))
	if name == "" {
		badRequest(c, "qy_invalid_param", "缺少用户分组名")
		return
	}
	var req setDefaultRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		badRequest(c, "qy_invalid_param", "请求格式错误")
		return
	}
	if !ValidDefaultMode(req.Mode) {
		badRequest(c, "qy_invalid_param", "mode 必须是 inherit / pin / deny 之一")
		return
	}
	target := strings.TrimSpace(req.ModelGroup)
	if req.Mode != DefaultModePin {
		// pin 之外的两档不携带值。清成空串而不是留着上一次的值:
		// 留着会让"改回 inherit 之后又改成 pin"静默复活一个早已作废的目标。
		target = ""
	} else if target == "" {
		badRequest(c, "qy_invalid_param", "mode=pin 时必须给出默认模型分组")
		return
	}

	if req.Mode == DefaultModePin {
		if !service.GroupInUserUsableGroups(name, target) {
			badRequest(c, "qy_groupns_not_usable",
				"模型分组 "+target+" 不在用户分组 "+name+" 的可选清单里 —— "+
					"默认值不能绕过权威可选清单。请先在「用户分组 × 模型分组」矩阵里授权")
			return
		}
		if !ratio_setting.ContainsGroupRatio(target) {
			badRequest(c, "qy_groupns_no_ratio",
				"模型分组 "+target+" 不在分组倍率表里 —— "+
					"配上去会让这一整组用户按 fail-open 的 1.0 倍计费")
			return
		}
		hasRoute, err := HasRoute(c, model.DB, target)
		if err != nil {
			internalError(c, err)
			return
		}
		if !hasRoute && !req.ForceNoRoute {
			badRequest(c, "qy_groupns_no_route",
				"模型分组 "+target+" 当前没有任何 enabled 渠道(abilities 口径)—— "+
					"配上去之后该用户分组的全部空分组令牌会立刻 503。"+
					"确实要这么做请显式勾选强制覆盖")
			return
		}
		// 第四道闸门:**倍率的值**。
		//
		// 上面三道都只判"在不在",而 ContainsGroupRatio 对 ratio=0 返回 true ——
		// 0 恰恰是最危险的取值。在真实数据上,三道闸门对用户分组 default
		// (707 人 / 47 个空分组令牌)放行的候选里就有一个名字写着"免费"、倍率是 0 的
		// 分组;随手选中它,从这一秒起这 707 个人的全部空分组令牌请求永久免费,
		// 而审计里只留下一个分组名。这一档不会有任何人来报障。
		if res := ratio_setting.InspectGroupRatio(name, target); res.Ratio == 0 && !req.ForceZeroRatio {
			badRequest(c, "qy_groupns_zero_ratio",
				"模型分组 "+target+" 对用户分组 "+name+" 的实际倍率是 **0**(调用完全免费)。"+
					"这一档人的全部空分组令牌会从保存的那一刻起免费调用,而且不会有人来报障。"+
					"确认这是有意的请显式勾选强制覆盖")
			return
		}
	}

	gdb := db.Get()
	if gdb == nil {
		internalError(c, db.ErrNotReady)
		return
	}
	var before UserGroup
	if err := gdb.WithContext(c).Where("name = ?", name).First(&before).Error; err != nil {
		badRequest(c, "qy_groupns_unknown",
			"用户分组 "+name+" 还没有登记。请先执行一次回填,或确认这个名字确实存在于 users.group")
		return
	}

	err := gdb.WithContext(c).Model(&UserGroup{}).Where("name = ?", name).
		Updates(map[string]any{
			"default_mode":        req.Mode,
			"default_model_group": target,
			"operator_id":         c.GetInt("id"),
			"updated_at":          now(),
		}).Error
	after := before
	after.DefaultMode = req.Mode
	after.DefaultModelGroup = target
	if err != nil {
		audit.WriteConfigUpdate(c, audit.ConfigChange{
			Action: "groupns.user_group.default.update", Result: qymodel.ResultFail,
			Reason: "设置默认模型分组失败: " + err.Error(),
			Before: before, After: after,
		})
		internalError(c, err)
		return
	}
	if reloadErr := InvalidateAndReload(); reloadErr != nil {
		common.SysError("qianye/groupns: 保存后重载快照失败: " + reloadErr.Error())
	}
	// 影响面与价格必须进返回值**和**审计正文。事故复盘时的第一个问题永远是
	// "改完之后这批令牌按几倍计费、影响了多少人",而那两个数字此前一个都没有。
	out := SetDefaultResult{UserGroup: after}
	if req.Mode == DefaultModePin {
		res := ratio_setting.InspectGroupRatio(name, target)
		out.EffectiveRatio, out.RatioSource = res.Ratio, res.Source
	}
	if counts, err := EmptyGroupTokenCounts(c, model.DB, true); err == nil {
		out.AffectedTokens = counts[name]
	}
	out.AffectedUsers = countByGroup(c, "users")[name]

	reason := fmt.Sprintf(
		"设置用户分组的默认模型分组(影响 %d 个空分组令牌 / %d 名在册用户)",
		out.AffectedTokens, out.AffectedUsers)
	if req.Mode == DefaultModePin {
		reason += fmt.Sprintf(";生效倍率 %g(来源 %s)", out.EffectiveRatio, out.RatioSource)
	}
	if req.ForceNoRoute && req.Mode == DefaultModePin {
		// 强制覆盖必须落进审计正文,不是一个布尔字段:事故复盘时第一个问题就是
		// "有没有人点过那个强制勾选"。
		reason += "(**强制覆盖 has_route 闸门**:目标分组当前没有 enabled 渠道)"
	}
	if req.ForceZeroRatio && req.Mode == DefaultModePin {
		reason += "(**强制覆盖零倍率闸门**:这一档人的空分组令牌调用完全免费)"
	}
	audit.WriteConfigUpdate(c, audit.ConfigChange{
		Action: "groupns.user_group.default.update", Result: qymodel.ResultOK,
		Reason: reason, Before: before, After: out,
	})
	respond(c, out)
}

// ─────────────────────────── 只读辅助 ───────────────────────────

func registeredUserGroupNames(c *gin.Context) map[string]bool {
	out := map[string]bool{}
	gdb := db.Get()
	if gdb == nil {
		return out
	}
	var rows []UserGroup
	if err := gdb.WithContext(c).Select("name").Find(&rows).Error; err != nil {
		return out
	}
	for _, row := range rows {
		out[row.Name] = true
	}
	return out
}

// routedModelGroupNames 是 legacy_dual 两侧共用的判据:**abilities 里有 enabled 行**
// 的分组名。
//
// 刻意不是"登记表里有这个模型分组"。上游设计要求每个用户分组在 options.GroupRatio 里
// 都有一个兜底倍率(否则那一档人的请求按 fail-open 的 1.0 计费),而回填把 GroupRatio
// 的键全部登记成模型分组 —— 拿登记表当判据的话 legacy_dual 恒等于"全部用户分组",
// 一个恒真的徽标既指不出真正的重名,也让"只减不增"这条守卫失去全部分辨力。
//
// 真正的重名是「这个名字既是一档人、又是一个真的能路由的渠道池子」,也就是
// "改一次名字会同时动到人和路由"的那个集合。与 store.go 的 observeGroups 同源。
func routedModelGroupNames(c *gin.Context) map[string]bool {
	out := map[string]bool{}
	if model.DB == nil {
		return out
	}
	col := model.QyCommonGroupCol()
	var rows []struct {
		Grp string `gorm:"column:grp"`
	}
	if err := model.DB.WithContext(c).
		Raw("SELECT DISTINCT "+col+" AS grp FROM abilities WHERE enabled = ?", true).
		Scan(&rows).Error; err != nil {
		return out
	}
	for _, row := range rows {
		if row.Grp != "" {
			out[row.Grp] = true
		}
	}
	return out
}

func countByGroup(c *gin.Context, table string) map[string]int64 {
	out := map[string]int64{}
	if model.DB == nil {
		return out
	}
	col := model.QyCommonGroupCol()
	var rows []struct {
		Grp string `gorm:"column:grp"`
		Cnt int64  `gorm:"column:cnt"`
	}
	sql := "SELECT " + col + " AS grp, COUNT(*) AS cnt FROM " + table +
		" WHERE deleted_at IS NULL GROUP BY " + col
	if err := model.DB.WithContext(c).Raw(sql).Scan(&rows).Error; err != nil {
		return out
	}
	for _, row := range rows {
		out[row.Grp] = row.Cnt
	}
	return out
}

// enabledChannelCountsByGroup 是 channels 口径的 enabled 渠道数。
//
// channels.group 是**逗号串**,所以只能拉出来在 Go 侧展开 —— 没有一种在三种方言
// 上都成立的 SQL 拆分。这是冷路径(管理端列表),渠道数是百量级,可以接受。
func enabledChannelCountsByGroup(c *gin.Context) map[string]int64 {
	out := map[string]int64{}
	if model.DB == nil {
		return out
	}
	col := model.QyCommonGroupCol()
	var rows []struct {
		Grp string `gorm:"column:grp"`
	}
	sql := "SELECT " + col + " AS grp FROM channels WHERE status = ?"
	if err := model.DB.WithContext(c).Raw(sql, common.ChannelStatusEnabled).Scan(&rows).Error; err != nil {
		return out
	}
	for _, row := range rows {
		// **刻意不 TrimSpace。** 真正的路由索引不 trim:model/channel_cache.go 的
		// buildGroup2Model2Channels 与 model/ability.go 的 AddAbilities 都是
		// `strings.Split(channel.Group, ",")` 原样落键。这里 trim 掉的话,一个写成
		// `"池A, 池B"` 的渠道会让 C 页显示「池B 有 1 个 enabled 渠道」判定健康,
		// 而 abilities 里落的键是 `" 池B"`,任何按「池B」发起的请求都 503 ——
		// 运营看着一个绿色徽标去排查一个 100% 失败的分组。
		// 这一列存在的全部理由就是暴露口径背离,替它把空格抹平等于把它关掉。
		for _, g := range strings.Split(row.Grp, ",") {
			if g != "" {
				out[g]++
			}
		}
	}
	return out
}
