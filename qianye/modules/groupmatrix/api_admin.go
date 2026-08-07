package groupmatrix

import (
	"errors"
	"fmt"
	"net/http"
	"sort"
	"strconv"
	"strings"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/qianye/config"
	"github.com/QuantumNous/new-api/qianye/db"
	"github.com/QuantumNous/new-api/qianye/groupratio"
	"github.com/QuantumNous/new-api/qianye/guard"
	qymodel "github.com/QuantumNous/new-api/qianye/model"
	"github.com/QuantumNous/new-api/qianye/service/audit"
	"github.com/QuantumNous/new-api/service"
	"github.com/QuantumNous/new-api/setting/ratio_setting"

	"github.com/gin-gonic/gin"
	"gorm.io/gorm"
)

// api_admin.go —— 矩阵读写、接管开关、单条孤儿令牌修复。
//
// 每一次写操作都留下 before/after 审计:这份清单决定谁能发出请求、按什么倍率
// 扣钱,事后「为什么这批用户突然全 403」只能靠它来回答。成功与失败**各写一条** ——
// 被 409 挡回去的那次同样是重要信号(说明有人在拿一份过期的预览做决定)。

const auditCategoryGroupMatrix = "group_matrix"

const (
	auditActionScopeUpdate  = "groupmatrix.scope.update"
	auditActionModeUpdate   = "groupmatrix.mode.update"
	auditActionGrantUpdate  = "groupmatrix.grant.update"
	auditActionRatioPublish = "groupmatrix.ratio.publish"
	auditActionTokenRepair  = "groupmatrix.token.repair"
)

// 倍率格子的来源。回显每格都必须带它:运营看到 0 时要能一眼分清
// 「我配的 0(免费)」和「兜底倍率本来就是 0」。
const (
	SourceOverride = "override"
	SourceInherit  = "inherit"
)

// ─────────────────────────── GET /group-matrix ───────────────────────────

type userGroupRow struct {
	Name      string `json:"name"`
	UserCount int64  `json:"user_count"`
	// ActiveTokenCount 是「启用且近 30 天有访问」的令牌数 ——
	// 「撤销这一行会打断谁」的分母。它必须长在行头上而不是藏进预览弹窗:
	// 运营在动格子的那一刻就要看见,点开才看得到就已经晚了。
	ActiveTokenCount int64  `json:"active_token_count"`
	Managed          bool   `json:"managed"`
	Mode             string `json:"mode"`
	AllowAuto        bool   `json:"allow_auto"`
	Note             string `json:"note"`
	// SelfExcluded 表示权威清单不含这个用户分组自己。
	//
	// 它推翻了上游存在多年的不变量,所以必须显式下发让界面能醒目提示 ——
	// 但它是**警告不是拦截**:项目方明确要求这条不变量能被推翻。
	SelfExcluded bool `json:"self_excluded"`

	// ScopeState 是行头的**三态**,前端必须按它渲染,不得自己用 managed+len(grants) 推。
	//
	// 三态与 Scope 的注释一一对应:
	//
	//	ScopeStateUnset → 未设定范围:全部模型分组可用,各按自己的兜底倍率。
	//	ScopeStateSet   → 已设定范围:只可用清单里的那些。
	//	ScopeStateEmpty → 已设定范围但清单为空:一个模型分组都不可用(红色)。
	//
	// **不要在界面上用「已接管 / 未接管」那套词。** 新口径下"未接管"意味着
	// 全部可用,而"接管"这个词会让人读成"不接管就用不了",方向正好相反。
	ScopeState string `json:"scope_state"`
}

// 行头三态。前端按它渲染,不自己推。
const (
	ScopeStateUnset = "unset"
	ScopeStateSet   = "set"
	ScopeStateEmpty = "empty"
)

type modelGroupRow struct {
	Name string `json:"name"`
	// BaseRatio 是模型分组的初始/兜底倍率(options.GroupRatio)。
	// 用户分组没有为它单独配倍率时,回落到这个值。
	//
	// **十进制字符串而不是 float64**:它会被展示在列头与继承格的占位符里,
	// 也会被前端拿去和格子上的覆盖值比较。走一遍 JSON float64 往返会把
	// 0.1 印成 0.10000000000000001,而这一页上每一个数字都是报价。
	BaseRatio string `json:"base_ratio"`
	// HasChannels 为 false 时该分组下没有任何可用渠道,授权它等于什么都没给。
	HasChannels bool `json:"has_channels"`
}

type cellView struct {
	UserGroup  string `json:"user_group"`
	ModelGroup string `json:"model_group"`
	// Granted 是**当前真实的可选性**,不是"扩展库里有没有这一行"。
	//
	// 未接管的用户分组一律走上游 service.GetUserUsableGroups 的实际结果 ——
	// 回 false 会把「现在是敞开的」画成「现在是锁死的」,而这一页的全部价值
	// 就是"先让人看清现状再改"。首次上线时一条 scope 行都没有,那一屏正是
	// 最需要看准的一屏。
	Granted bool `json:"granted"`
	// Ratio 是 *float64:null = 未配置(继承),0 = 显式免费。
	// **禁止改成 float64 + omitempty** —— 运营填的 0 会在序列化时消失。
	Ratio  *float64 `json:"ratio"`
	Source string   `json:"source"`
	// InheritedFrom 是继承时实际生效的兜底倍率(十进制字符串,理由同 BaseRatio),
	// 让界面能把继承值以灰字显示而**不预填进输入框**
	// (预填会诱导运营把继承变成一次显式覆盖)。
	InheritedFrom string `json:"inherited_from"`

	// EffectiveRatio 是**这一格此刻真的会被乘进账单**的那个数(十进制字符串)。
	//
	// 它由 service.GetUserGroupRatio 现算,与三条计费路径同源;Source 只说明它
	// 来自交叉格还是兜底。下发它是为了让格子**永远不出现空白态** ——
	// 空白是 0 还是继承,人永远猜不对,而这一页上每个数字都是报价。
	EffectiveRatio string `json:"effective_ratio"`
	// BaseMissing 为 true 表示该模型分组**不在** options.GroupRatio 里。
	//
	// 此时上游 GetGroupRatio 已经 fail-open 静默返回 1 —— 那个 1 不是任何人配出来的。
	// 界面必须把这一格标红并写「兜底缺失·按 1.0 计费」,不能显示成一个正常的 1。
	BaseMissing bool `json:"base_missing"`

	// ReachableVia 回答「这一格此刻可达吗、是谁给的」:
	// scope(范围清单/上游默认) / plan(套餐解锁) / both / none。
	//
	// **必须按 scope ∪ plan 计算,不能只按 scope。** 否则会出现这样一种漂移:
	// 用户分组 A 设了范围且不含 G,但 A 组的某个用户买了解锁 G 的套餐 —— 他能用 G、
	// 按 GroupRatio[G] 计费,而矩阵页的 A 行根本不渲染这一格。运营几周后为了别的
	// 目的把 GroupRatio[G] 从 1.0 调到 3.0,那批人当场按 3 倍扣费,
	// **而站内没有任何一个页面显示过 A 组的人可以到达 G**。
	// 这不是倍率数值的漂移,是可达性认知的漂移,而它的后果直接是钱。
	ReachableVia string `json:"reachable_via"`
	// ReachableByPlans 是让这一格可达的套餐名(去重后排序)。空表示没有套餐参与。
	ReachableByPlans []string `json:"plan_titles"`

	// RatioSource 说明 EffectiveRatio **来自哪一层**:cross_cell(交叉格)
	// 或 group_ratio(该模型分组的兜底倍率)。
	//
	// 与 Source 的区别是刻意的:Source 描述"这一格配没配"(界面上要不要把输入框
	// 显示成已填),RatioSource 描述"扣钱时乘的是哪个数"。今天两者结论相同,
	// 但它们回答的不是同一个问题,合并成一个字段会让将来任何一层回落变得无法表达。
	RatioSource string `json:"ratio_source"`
}

// 生效倍率的来源层。与 service.GetUserGroupRatio 的两个分支一一对应。
const (
	RatioSourceCrossCell  = "cross_cell"
	RatioSourceGroupRatio = "group_ratio"
)

// 格子的可达来源。
const (
	ReachableNone  = "none"
	ReachableScope = "scope"
	ReachablePlan  = "plan"
	ReachableBoth  = "both"
)

// PlanUnlockedModelGroups 由订阅侧注入:返回「被某个套餐解锁的模型分组 → 套餐名」。
//
// ══════════════ 为什么这个接缝必须存在于矩阵页而不是套餐页 ══════════════
//
// 运营改倍率时看的是矩阵页,不是套餐详情页。可达性只按 scope 算的话,
// 通过套餐可达的那些格子在矩阵页上根本不存在,而它们照样在花钱(见 cellView.ReachableVia)。
//
// 默认返回 nil = 没有任何套餐解锁 = 与本轮之前完全一致,所以在订阅侧接上之前
// 这个接缝是**行为中性**的。
//
// 契约(实现方必须满足):
//
//	冷路径专用      —— 只被管理端 GET /group-matrix 调用,允许查库,但要有超时;
//	                   任何内部失败一律 return nil(可达性少标一点,不影响任何扣费)。
//	返回值只读      —— 调用方不修改它;实现方也不得返回内部快照持有的那张 map。
//	键必须是模型分组 —— 且必须已在 options.GroupRatio 里(编译期已丢弃的不要给回来)。
var PlanUnlockedModelGroups = func() map[string][]string { return nil }

// PlanUnlockEnabled 回答「套餐解锁此刻整体是开着的吗」。
//
// 它与 PlanUnlockedModelGroups 分开,因为两者回答的不是同一个问题:
// 后者为空可能是"关掉了",也可能是"开着但一个绑定都没配"。这两种状态在矩阵页上
// 必须显示成不同的话 —— 前者要提示"已购用户当前拿不到他买的分组",后者什么都不用说。
//
// 开关本身在 plan_entitlement 段,不在 group_matrix 段:同一份事实不设两个配置源。
// 默认 false = 订阅侧尚未接入 = 与本轮之前完全一致。
var PlanUnlockEnabled = func() bool { return false }

// savePartial 是两库写入的半成状态。**只在部分失败时出现。**
//
// 它必须在界面上被看见:倍率落上游 options、清单落扩展库,两库不原子。
// 运营按完保存看到一句绿色的「已保存」然后走人,是这套设计最坏的失败方式。
type savePartial struct {
	RatioApplied      bool   `json:"ratio_applied"`
	MembershipApplied bool   `json:"membership_applied"`
	Message           string `json:"message"`
}

// matrixView 是 GET 的响应,**也是每一次写成功后强制回读的同一个形状**。
//
// 三个写接口(矩阵 / 接管开关 / 修复)全部回这个结构,前端因此可以无条件地
// 用响应替换本地状态。早期版本让写接口各回各的小对象,前端把它 setQueryData
// 进同一个缓存键 —— 下一次渲染 model_groups 是 undefined,整页崩到错误边界,
// 而改动其实已经落库了。
type matrixView struct {
	UserGroups        []userGroupRow  `json:"user_groups"`
	ModelGroups       []modelGroupRow `json:"model_groups"`
	Cells             []cellView      `json:"cells"`
	BaseRatioHash     string          `json:"base_ratio_hash"`
	Snapshot          gin.H           `json:"snapshot"`
	Warnings          []string        `json:"warnings"`
	ShadowWriteDenies []WriteDeny     `json:"shadow_write_denies"`
	Partial           *savePartial    `json:"partial,omitempty"`

	// ScopePolicy 回答「一个用户分组没有出现在这份范围配置里,会发生什么」。
	//
	// 它必须随每一次矩阵回读一起下发,而不是让前端写死一句话:这条口径本轮刚刚
	// **反转过**(上一轮是"新分组自动全遮断"),而前端写死的文案不会跟着后端一起回退。
	ScopePolicy scopePolicy `json:"scope_policy"`
}

// scopePolicy 是「未设定范围」这一档的常驻说明与计数。
type scopePolicy struct {
	// UnsetMeansAll 恒为 true。它不是开关,是把口径本身下发给界面 ——
	// 界面据此渲染那句常驻说明,而不是把它硬编码在前端。
	//
	// ⚠ 它的准确含义是「**本页不限制**未设定范围的用户分组」,不是「它们能用到
	// options.GroupRatio 里的每一个模型分组」。未设定范围时 Resolve 逐位返回上游
	// service.GetUserUsableGroups 的结果 —— 也就是全局「用户可选分组」清单
	// (setting.userUsableGroups),外加「用户分组自己恰好也是一个配了倍率的模型分组」
	// 时的那一条补入。这两件事在「全局清单没有列出全部模型分组」的站点上结论不同,
	// 而本站正是这种站点。界面文案必须按前一种说法写;要让它真的等于"全部模型分组",
	// 需要项目方拍板改变 Resolve 的恒等契约(那会同时推翻"未配置 = 上游指针恒等")。
	//
	// 刻意**不**提供一个 unscoped_sees_all 配置项:那种开关只会被一次性打开然后
	// 没人再看,并且它会让「未设范围」重新有两种含义,正是本轮在消灭的形状。
	UnsetMeansAll bool `json:"unset_means_all"`
	// UnsetGroups 是当前未设定范围的用户分组数。它必须常驻显示、不能只在
	// 有变化时通知 —— 一次性通知会被忽略,常驻的一栏不会。
	UnsetGroups int `json:"unset_groups"`
	// ScopedGroups / EmptyScopedGroups 分别是"设了非空范围"与"设了空范围"的分组数。
	// 后者单独计数是因为它是唯一会让整组用户一个模型分组都选不到的配置。
	ScopedGroups      int `json:"scoped_groups"`
	EmptyScopedGroups int `json:"empty_scoped_groups"`
	// SubscriptionUnlockOn 的真相源是 **plan_entitlement.enabled**,经
	// groupmatrix.PlanUnlockEnabled 这个接缝注入。
	//
	// **本段(group_matrix)里没有、也不会有第二个开关。** 两个开关方向相反:
	// group_matrix.enabled 拉下是让「收紧」失效,套餐解锁拉下是让已付款用户当场
	// 少掉一批分组 —— 共用一个开关的人一定会误伤一侧;而同一件事两个配置源,
	// 同步失败的表现正是「一个页面说开着、另一个说关着」。
	//
	// 关掉时通过套餐可达的格子全部失效,而套餐照常收钱 —— 这一栏是它唯一的形状。
	SubscriptionUnlockOn bool `json:"subscription_unlock_on"`
}

// ratioText 把倍率渲染成十进制字符串。
//
// 'f' + 精度 -1 给出能**原样往返**的最短表示:0.1 就是 "0.1",不会变成
// 0.10000000000000001。这一页上每一个数字都会被运营当成报价读。
func ratioText(v float64) string { return strconv.FormatFloat(v, 'f', -1, 64) }

func adminGetMatrix(c *gin.Context) {
	if !guard.RequireAPI(c, guard.FlagGroupMatrix) {
		return
	}
	view, err := buildMatrixView(db.Get())
	if err != nil {
		internalError(c, err)
		return
	}
	respond(c, view)
}

// buildMatrixView 组装矩阵的完整真实状态。
func buildMatrixView(gdb *gorm.DB) (*matrixView, error) {
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

	userGroups, userCounts := listUserGroups(scopes)
	activeTokens := activeTokenCounts()
	modelGroups := listModelGroups()
	// 套餐解锁的可达性。关掉开关时不查:那一档里通过套餐可达的格子确实不可达,
	// 界面必须跟着变,否则它会显示一批"看起来通、实际不通"的格子。
	planUnlocked := map[string][]string{}
	if PlanUnlockEnabled() {
		for mg, plans := range PlanUnlockedModelGroups() {
			names := append(make([]string, 0, len(plans)), plans...)
			sort.Strings(names)
			planUnlocked[mg] = names
		}
	}

	// 未设定范围的行必须显示**上游此刻的实际可选集合**(见 cellView.Granted)。
	// 走 service.GetUserUsableGroups 是安全的:该分组没有 scope 行时
	// Resolve 恒等返回上游,读不到我们自己写的东西。每个分组只算一次。
	upstreamUsable := make(map[string]map[string]string, len(userGroups))
	for _, ug := range userGroups {
		if _, managed := scopes[ug]; !managed {
			upstreamUsable[ug] = service.GetUserUsableGroups(ug)
		}
	}

	policy := scopePolicy{
		UnsetMeansAll:        true,
		SubscriptionUnlockOn: PlanUnlockEnabled(),
	}
	rows := make([]userGroupRow, 0, len(userGroups))
	for _, ug := range userGroups {
		sc, managed := scopes[ug]
		row := userGroupRow{
			Name: ug, UserCount: userCounts[ug],
			ActiveTokenCount: activeTokens[ug], Managed: managed,
			// 未设定范围时下发 shadow 而不是空串:前端的 mode 是一个二值枚举,
			// 空串会让它在 managed 被打开的那一刻落进"两个选项都没选中"的状态。
			Mode: ModeShadow, ScopeState: ScopeStateUnset,
		}
		switch {
		case !managed:
			// 未设定范围:allow_auto 的预填值 = 上游此刻是否已经把 auto 放行。
			// 设定范围的开关打开时这就是零行为变更的那一份初值。
			_, row.AllowAuto = upstreamUsable[ug][autoGroup]
			policy.UnsetGroups++
		default:
			row.Mode, row.AllowAuto, row.Note = sc.Mode, sc.AllowAuto, sc.Note
			_, self := grants[ug][ug]
			row.SelfExcluded = !self
			if len(grants[ug]) == 0 {
				row.ScopeState = ScopeStateEmpty
				policy.EmptyScopedGroups++
			} else {
				row.ScopeState = ScopeStateSet
			}
			policy.ScopedGroups++
		}
		rows = append(rows, row)
	}

	cells := make([]cellView, 0, len(userGroups)*len(modelGroups))
	for _, ug := range userGroups {
		upstream, unmanaged := upstreamUsable[ug]
		for _, mg := range modelGroups {
			granted := false
			if unmanaged {
				_, granted = upstream[mg.Name]
			} else {
				_, granted = grants[ug][mg.Name]
			}
			cv := cellView{
				UserGroup: ug, ModelGroup: mg.Name, Granted: granted,
				Source: SourceInherit, InheritedFrom: mg.BaseRatio,
				ReachableByPlans: make([]string, 0),
			}
			cv.RatioSource = RatioSourceGroupRatio
			if v, ok := ratios[ug][mg.Name]; ok {
				val := v
				cv.Ratio, cv.Source = &val, SourceOverride
				cv.RatioSource = RatioSourceCrossCell
			}
			// 生效倍率由 qianye/groupratio 现算,它内部走 service.GetUserGroupRatio ——
			// 全仓唯一没有被复制的那一份解析,与三条计费路径同结论。
			// 绝不在这里再写一遍 if:第四份复制品迟早自己漂移,而漂移的表现正是
			// "管理端显示 A、热路径乘 B"。
			res := groupratio.Resolve(ug, mg.Name)
			cv.EffectiveRatio, cv.BaseMissing = ratioText(res.Ratio), res.BaseMissing
			cv.ReachableVia = ReachableNone
			if granted {
				cv.ReachableVia = ReachableScope
			}
			if plans, ok := planUnlocked[mg.Name]; ok && len(plans) > 0 {
				cv.ReachableByPlans = plans
				if granted {
					cv.ReachableVia = ReachableBoth
				} else {
					cv.ReachableVia = ReachablePlan
				}
			}
			cells = append(cells, cv)
		}
	}

	snap, loaded := SnapshotView()
	snapInfo := gin.H{
		"loaded": loaded, "age_seconds": int64(0), "version": int64(0),
		"stale": false, "dropped_grants": []string{},
	}
	if loaded {
		age := common.GetTimestamp() - loadedAt.Load()
		snapInfo["age_seconds"] = age
		snapInfo["version"] = snap.Version
		snapInfo["dropped_grants"] = snap.DroppedGrants
		// 陈旧刻意不丢弃快照(见 snapshot.go 的 activeSnapshot),所以「陈旧」是
		// 唯一一个"清单在生效、但内容可能是旧的"状态。不下发这个布尔,
		// 界面上那条横幅就永远不会出现,而它是这条设计选择的唯一补偿。
		snapInfo["stale"] = age > maxStaleSeconds()
	}

	return &matrixView{
		UserGroups: rows, ModelGroups: modelGroups, Cells: cells,
		BaseRatioHash: baseHash, Snapshot: snapInfo,
		Warnings: matrixWarnings(userGroups, modelGroups, grants),
		// 影子期的写入拒绝。它是**唯一可归因**的影子证据来源:
		// 读侧那个挂载点拿不到被查询的 key(理由见 WriteDeny 的注释),
		// 所以那一半的证据来自 preview 的日志聚合。
		//
		// 不下发它的话,这张表就是一张只写不读的表 —— 而只写不读的观测数据
		// 与没有观测是同一回事,只是更容易让人以为自己有证据。
		ShadowWriteDenies: listWriteDenies(gdb),
		ScopePolicy:       policy,
	}, nil
}

// respondMatrix 把写接口的响应统一成"回读后的真实状态"。
func respondMatrix(c *gin.Context, gdb *gorm.DB, partial *savePartial) {
	view, err := buildMatrixView(gdb)
	if err != nil {
		internalError(c, err)
		return
	}
	view.Partial = partial
	if partial == nil {
		respond(c, view)
		return
	}
	c.JSON(http.StatusOK, gin.H{
		"success": true, "message": partial.Message, "data": view,
	})
}

// activeTokenCounts 统计每个用户分组下「启用且近 30 天有访问」的令牌数。
//
// 单独一条聚合而不是复用 tokenPairStats:那份统计按 (用户分组, 模型分组) 分行,
// 而且刻意丢掉了 model_group 为空的行 —— 空分组令牌恰恰是这一档里数量最大的一批,
// 漏掉它们会让行头的分母显著偏小,而运营会据此低估撤销的影响。
func activeTokenCounts() map[string]int64 {
	out := map[string]int64{}
	if model.DB == nil {
		return out
	}
	type row struct {
		Grp string `gorm:"column:grp"`
		N   int64  `gorm:"column:n"`
	}
	col := model.QyCommonGroupCol()
	var rows []row
	q := model.DB.Model(&model.Token{}).
		Joins("JOIN users ON users.id = tokens.user_id").
		Select("users."+col+" as grp, count(*) as n").
		Where("tokens.status = ?", common.TokenStatusEnabled).
		Where("tokens.accessed_time >= ?", common.GetTimestamp()-tokenActiveDays*86400)
	err := groupByRaw(q, "users."+col).Scan(&rows).Error
	if err != nil {
		common.SysError("qianye/groupmatrix: 统计活跃令牌失败(行头的活跃令牌数会显示为 0): " + err.Error())
		return out
	}
	for _, r := range rows {
		if r.Grp == "" {
			continue
		}
		out[r.Grp] = r.N
	}
	return out
}

// listUserGroups 汇总应当出现在矩阵行轴上的用户分组。
//
// 三个来源的并集:users 表里在用的、已经被接管的、分组倍率表里登记的。
// 少任何一个都会让某一类用户在界面上"不存在":在用但没被接管的会看不见,
// 被接管但已经没人用的会在删掉最后一个用户后从界面消失(而 scope 行还在生效)。
func listUserGroups(scopes map[string]Scope) ([]string, map[string]int64) {
	seen := map[string]struct{}{}
	counts := map[string]int64{}

	if model.DB != nil {
		type row struct {
			Grp string `gorm:"column:grp"`
			N   int64  `gorm:"column:n"`
		}
		var rows []row
		// group 是三种数据库里的保留字,列名一律走 model.QyCommonGroupCol()。
		col := model.QyCommonGroupCol()
		err := groupByRaw(model.DB.Model(&model.User{}).
			Select(col+" as grp, count(*) as n"), col).Scan(&rows).Error
		if err != nil {
			common.SysError("qianye/groupmatrix: 统计用户分组失败(行轴仍会展示已接管与倍率表里的分组): " + err.Error())
		}
		for _, r := range rows {
			if r.Grp == "" {
				continue
			}
			seen[r.Grp] = struct{}{}
			counts[r.Grp] = r.N
		}
	}
	for name := range scopes {
		seen[name] = struct{}{}
	}
	for name := range ratio_setting.GetGroupRatioCopy() {
		if name == autoGroup {
			continue
		}
		seen[name] = struct{}{}
	}

	out := make([]string, 0, len(seen))
	for name := range seen {
		out = append(out, name)
	}
	sort.Strings(out)
	return out, counts
}

// listModelGroups 汇总列轴。判据是分组倍率表 —— 与上游
// middleware/auth.go 的 ContainsGroupRatio 同源,不在这张表里的分组
// 即使授权了也会被上游用「分组已被弃用」挡掉。
func listModelGroups() []modelGroupRow {
	ratios := ratio_setting.GetGroupRatioCopy()
	withChannels := groupsWithEnabledAbilities()

	out := make([]modelGroupRow, 0, len(ratios))
	for name, ratio := range ratios {
		if name == autoGroup {
			continue
		}
		out = append(out, modelGroupRow{
			Name: name, BaseRatio: ratioText(ratio), HasChannels: withChannels[name],
		})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}

// groupsWithEnabledAbilities 取 abilities 里所有仍有启用渠道的模型分组。
//
// 走 GORM 的 Model + Pluck 而不是拼 SQL:group 是三种数据库里的保留字,
// 只有让 GORM 拿着 model.Ability 的 schema 去渲染,引号规则才会自动跟着方言走。
func groupsWithEnabledAbilities() map[string]bool {
	out := map[string]bool{}
	if model.DB == nil {
		return out
	}
	var names []string
	if err := model.DB.Model(&model.Ability{}).
		Where("enabled = ?", true).Distinct().Pluck("group", &names).Error; err != nil {
		common.SysError("qianye/groupmatrix: 探测渠道分组失败(has_channels 一律按 false 显示): " + err.Error())
		return out
	}
	for _, n := range names {
		out[n] = true
	}
	return out
}

// matrixWarnings 列出保存前应当被看见、但**不拦截**的问题。
func matrixWarnings(userGroups []string, modelGroups []modelGroupRow, grants map[string]map[string]struct{}) []string {
	warns := make([]string, 0)

	// 大小写近似项。**刻意不折叠**:倍率侧 GetGroupGroupRatio 是精确 map 查找、
	// 在 3 条计费路径里、我们无权改。折叠成员资格而不折叠倍率会造出
	// 「users.group=VIP 命中 vip 的清单拿到访问权,倍率却回落兜底」——
	// 管理端显示 0.3、实际按 1.0 扣、零告警。让人看见并自己决定,不替他折叠。
	byLower := map[string][]string{}
	for _, ug := range userGroups {
		byLower[strings.ToLower(ug)] = append(byLower[strings.ToLower(ug)], "用户分组 "+ug)
	}
	for _, mg := range modelGroups {
		byLower[strings.ToLower(mg.Name)] = append(byLower[strings.ToLower(mg.Name)], "模型分组 "+mg.Name)
	}
	for _, names := range byLower {
		if len(names) < 2 {
			continue
		}
		distinct := map[string]struct{}{}
		for _, n := range names {
			distinct[n] = struct{}{}
		}
		if len(distinct) < 2 {
			continue
		}
		sorted := make([]string, 0, len(distinct))
		for n := range distinct {
			sorted = append(sorted, n)
		}
		sort.Strings(sorted)
		warns = append(warns, fmt.Sprintf(
			"存在仅大小写不同的分组名:%s。系统按**精确匹配**处理,它们是不同的分组 —— "+
				"不折叠是刻意的:倍率侧是精确查找且不可改,折叠成员资格会造出「界面 0.3、实际 1.0」",
			strings.Join(sorted, " / ")))
	}

	// 授权了一个没有任何启用渠道的模型分组:选它等于什么都没给。
	hasChannels := map[string]bool{}
	for _, mg := range modelGroups {
		hasChannels[mg.Name] = mg.HasChannels
	}
	for ug, set := range grants {
		for mg := range set {
			// 引用了一个**已经不在分组倍率表里**的模型分组。
			//
			// 这一条比"没有渠道"严重一档,而且是本页唯一能发现「运营删了一个仍被
			// 引用的模型分组」的机制 —— 上游删除时不做任何引用检查。
			// 后果不是少给权限:快照编译期会把它剔除,而矩阵页上那一格看起来是通的,
			// 用户实际会被上游用「分组已被弃用」挡掉。写在前面单独成句,不与渠道警告合并。
			if !ratio_setting.ContainsGroupRatio(mg) {
				hint := ""
				if near := groupratio.NearMiss(mg); near != "" {
					hint = fmt.Sprintf(",倍率表里有一个仅大小写不同的 %q(分组倍率按精确匹配,二者是两个分组)", near)
				}
				warns = append(warns, fmt.Sprintf(
					"【需要处理】用户分组 %q 的清单里有一个已从分组倍率表消失的模型分组 %q%s —— "+
						"该项已被快照剔除,「用户分组」页上那一格看起来是通的,用户实际会被上游的"+
						"「分组已被弃用」挡掉。请把它从清单里撤掉,或把这个模型分组加回「模型分组定价」的分组表",
					ug, mg, hint))
				continue
			}
			if !hasChannels[mg] {
				warns = append(warns, fmt.Sprintf(
					"用户分组 %q 被授权的模型分组 %q 当前没有任何启用中的渠道,选它等于寸步难行", ug, mg))
			}
		}
	}
	sort.Strings(warns)
	return warns
}

// ─────────────────────────── PUT /group-matrix ───────────────────────────

type putMatrixReq struct {
	Cells []Cell `json:"cells"`
	// DraftHash / ImpactHash 只在把某一行切到 enforce 时必需(见 adminPutScope)。
	// 这里带上是为了让"预览的是 A、保存的是 B"这种情形也能被挡住。
	DraftHash  string `json:"draft_hash"`
	ImpactHash string `json:"impact_hash"`
	// BaseRatioHash 是打开页面时那一刻的上游倍率指纹。
	//
	// 运营仍可在上游「系统设置-分组倍率」页改同一份数据 —— 那个入口**刻意不锁死**,
	// 扩展关掉之后运营必须还能在原地改倍率,这是回退能力的一部分。
	// 代价是可能有人在你编辑期间改过它,所以这里必须比对。
	BaseRatioHash string `json:"base_ratio_hash"`
}

// adminPutMatrix 落一次矩阵改动。
//
// ══════════════ 两库写入顺序:先倍率,后成员资格 ══════════════
//
// 这个顺序是算出来的,不是随手定的:
//
//	倍率成功 / 清单失败 → 新的可达组合还没打开,没有任何流量按新倍率走,
//	                      **零金额影响**,表现为"配了没生效"。
//	清单成功 / 倍率失败 → 用户立刻能选中该分组,倍率回落到 GroupRatio[模型分组],
//	                      可能比运营意图贵得多或便宜得多,**钱立刻按错误的价走**。
//
// 「先清单后倍率」只考虑了"没人被多扣",漏了"按兜底价放行"同样是错价。
//
// 成员资格内部先 revoke 后 grant:revoke 是唯一会打断线上流量的操作,
// 先做能让部分失败停在「收紧了但还没放开」这个更保守的状态。
func adminPutMatrix(c *gin.Context) {
	if !guard.RequireAPI(c, guard.FlagGroupMatrix) {
		return
	}
	var req putMatrixReq
	if err := c.ShouldBindJSON(&req); err != nil {
		badRequest(c, "请求体格式错误")
		return
	}
	cells, err := normalizeCells(req.Cells)
	if err != nil {
		badRequest(c, err.Error())
		return
	}
	if len(cells) == 0 {
		badRequest(c, "cells 为空,没有任何要保存的改动")
		return
	}

	ratioMu.Lock()
	defer ratioMu.Unlock()

	ratios, baseHash, err := loadRatioMatrix()
	if err != nil {
		internalError(c, err)
		return
	}
	// base_ratio_hash **必填**。早期版本写的是 `req.BaseRatioHash != "" && ...`,
	// 于是一个省略该字段的请求(脚本、curl、缓存了旧页面的前端)就能整条跳过这道闸门,
	// 静默覆盖另一个管理员刚在上游分组倍率页写下的值,而审计里看不出发生过冲突。
	if req.BaseRatioHash == "" {
		badRequest(c, "缺少 base_ratio_hash —— 它证明你手上这份倍率是最新的。"+
			"请先 GET /group-matrix 拿到它再保存")
		return
	}
	if req.BaseRatioHash != baseHash {
		writeMatrixFailure(c, auditActionRatioPublish, cells, ratios,
			errors.New("base_ratio_hash 不匹配:分组倍率在上游页面被改动过"))
		conflict(c, "分组倍率在上游「系统设置 → 计费与支付 → 模型分组定价」页被改动过,请重新载入「用户分组」页再保存")
		return
	}

	gdb := db.Get()
	scopes, err := loadScopes(gdb)
	if err != nil {
		internalError(c, err)
		return
	}
	beforeGrants, err := loadGrants(gdb)
	if err != nil {
		internalError(c, err)
		return
	}
	if err := validateCells(cells, scopes); err != nil {
		badRequest(c, err.Error())
		return
	}
	if err := checkGrantBudget(gdb, cells, beforeGrants); err != nil {
		badRequest(c, err.Error())
		return
	}
	// ── 撤销闸门:必须先看过影响面 ──────────────────────────────────
	//
	// 撤销是唯一会让一批正在跑的令牌当场变成 403 的动作。这道闸门早先只存在于
	// 前端(diff-bar 的 needsPreview),而 putMatrixReq 虽然**声明**了两个哈希、
	// 注释里也写明它们是干什么的,服务端却一次都没读过 —— 一条 curl 就能绕过。
	// 更糟的是:一个确实回传了哈希的客户端也不会因为哈希过期而被拦,
	// 字段被静默忽略比不接收它更危险。
	if err := requirePreviewedRevoke(req, cells); err != nil {
		writeMatrixFailure(c, auditActionGrantUpdate, cells, grantsToLists(beforeGrants), err)
		conflict(c, err.Error())
		return
	}

	// ── 第一步:倍率(上游 options)──────────────────────────────────
	//
	// before 快照必须在 applyRatioCells 之前拷出来:那个函数就地改 ratios,
	// 之后再取就只剩 after,而"改之前是什么"正是这条审计一半的价值。
	beforeRatios := cloneRatioMatrix(ratios)
	ratioChanged := applyRatioCells(ratios, cells)
	if ratioChanged {
		if err := publishRatioMatrix(ratios); err != nil {
			writeMatrixFailure(c, auditActionRatioPublish, cells, beforeRatios, err)
			internalError(c, err)
			return
		}
		audit.Write(c, matrixAuditEntry(c, auditActionRatioPublish, cells, beforeRatios, nil))
	}

	// ── 第二步:成员资格(扩展库),先 revoke 后 grant ────────────────
	listErr := applyGrantCells(gdb, cells, c.GetInt("id"))
	before := grantsToLists(beforeGrants)
	if listErr != nil {
		writeMatrixFailure(c, auditActionGrantUpdate, cells, before, listErr)
	} else {
		audit.Write(c, matrixAuditEntry(c, auditActionGrantUpdate, cells, before, nil))
	}

	if err := InvalidateAndReload(); err != nil {
		common.SysError("qianye/groupmatrix: 写入后刷新快照失败(其它节点仍会按周期刷新): " + err.Error())
	}

	// ── 强制回读两侧真实状态 ────────────────────────────────────────
	//
	// 部分失败时运营必须**立刻在界面上**看到「倍率已生效、清单未生效」,
	// 而不是靠一个乐观的本地状态继续编辑。前端禁止用提交前的草稿渲染。
	// 回的是与 GET 完全相同的结构,前端因此可以无条件整体替换。
	var partial *savePartial
	if listErr != nil {
		// 倍率已经落了、清单没落。
		//
		// ⚠ 早先这里写的是「新的可达组合尚未打开,因此没有任何流量按新倍率走」——
		// **那句话只对 grant 出来的新组合成立**。对已经可达的组合(已 granted 的格子、
		// 以及全部未接管/影子用户分组的任意格子)改价,倍率写入成功的那一刻就在扣钱。
		// 运营据那句话判断"本次无资金影响、不必回滚",是一个由文案直接造成的事故。
		partial = &savePartial{
			RatioApplied: ratioChanged, MembershipApplied: false,
			Message: "倍率已生效,可选清单未生效。**已经可达的组合(已放开的格子、" +
				"以及未接管/影子模式下的任意格子)从这一刻起就按新倍率扣费**;" +
				"只有本次新放开的组合还没打开。请立刻确认改价是否符合预期,再决定重试还是回滚。",
		}
	}
	respondMatrix(c, gdb, partial)
}

// requirePreviewedRevoke 是撤销动作的服务端闸门。
//
// 只有含 revoke 的草稿才要求带 draft_hash / impact_hash:放开与改价不会让任何
// 一个正在跑的请求变成 403,为它们也强制走一遍预览只会让运营学会无脑点过。
// (改价的金额风险由前端的差异条与本函数之外的 base_ratio_hash 负责。)
func requirePreviewedRevoke(req putMatrixReq, cells []Cell) error {
	revoke := false
	for _, cell := range cells {
		if cell.Action == ActionRevoke {
			revoke = true
			break
		}
	}
	if !revoke {
		return nil
	}
	if req.DraftHash == "" || req.ImpactHash == "" {
		return errors.New("本次保存含撤销操作,必须先调 POST /group-matrix/preview 看过影响面," +
			"并把返回的 draft_hash 与 impact_hash 一起回传")
	}
	fresh, err := runPreview(previewReq{Cells: cells})
	if err != nil {
		return err
	}
	if fresh.DraftHash != req.DraftHash {
		return errors.New("预览的草稿与本次要保存的草稿不是同一份,请重新预览")
	}
	if fresh.ImpactHash != req.ImpactHash {
		return errors.New("影响面已经变化(预览之后有人建了新令牌、改了倍率表或改了清单),请重新预览")
	}
	return nil
}

// validateCells 是写入侧的第一道校验。快照编译时还会再跑一次同样的判据 ——
// 分组可能在保存之后被从倍率表删掉,只在保存时把关拦不住那种漂移。
func validateCells(cells []Cell, scopes map[string]Scope) error {
	for _, cell := range cells {
		if cell.Action != ActionGrant && cell.Action != ActionRevoke {
			continue
		}
		if _, managed := scopes[cell.UserGroup]; !managed {
			return fmt.Errorf("用户分组 %q 尚未接管,不能编辑它的可选清单 —— "+
				"请先用 PUT /group-matrix/scope/%s 建立 scope 行(建议先用 shadow 模式)",
				cell.UserGroup, cell.UserGroup)
		}
		if cell.Action == ActionGrant && !ratio_setting.ContainsGroupRatio(cell.ModelGroup) {
			return fmt.Errorf("模型分组 %q 不在分组倍率表里,授权它没有意义 —— "+
				"上游会在请求时用「分组 %s 已被弃用」把用户挡掉", cell.ModelGroup, cell.ModelGroup)
		}
	}
	return nil
}

// checkGrantBudget 挡住"一次脚本误操作让每个节点定期拉一张大表"。
func checkGrantBudget(gdb *gorm.DB, cells []Cell, before map[string]map[string]struct{}) error {
	limit := int64(config.Get().GroupMatrix.MaxGrants)
	if limit <= 0 {
		limit = 2000
	}
	cur, err := countGrants(gdb)
	if err != nil {
		return err
	}
	delta := int64(0)
	for _, cell := range cells {
		_, exists := before[cell.UserGroup][cell.ModelGroup]
		switch {
		case cell.Action == ActionGrant && !exists:
			delta++
		case cell.Action == ActionRevoke && exists:
			delta--
		}
	}
	if cur+delta > limit {
		return fmt.Errorf("清单行数将达到 %d,超过 group_matrix.max_grants(%d)", cur+delta, limit)
	}
	return nil
}

// applyRatioCells 把倍率动作作用在矩阵上,返回是否真的有改动。
//
// inherit 的格子**不写 key**(而不是写 0 或 null):上游 GetGroupGroupRatio 靠
// key 存在与否返回 (ratio, false),"没有这个 key" = 继承兜底倍率。
// 写 0 是**显式免费**,与继承是两件完全不同的事,而且 0 会命中
// relay/helper/price.go 的整体替换分支。
func applyRatioCells(m ratioMatrix, cells []Cell) bool {
	changed := false
	for _, cell := range cells {
		switch cell.Action {
		case ActionSetRatio:
			row, ok := m[cell.UserGroup]
			if !ok {
				row = map[string]float64{}
				m[cell.UserGroup] = row
			}
			if old, had := row[cell.ModelGroup]; had && old == *cell.Ratio {
				continue
			}
			row[cell.ModelGroup] = *cell.Ratio
			changed = true
		case ActionClearRatio:
			row, ok := m[cell.UserGroup]
			if !ok {
				continue
			}
			if _, had := row[cell.ModelGroup]; !had {
				continue
			}
			delete(row, cell.ModelGroup)
			changed = true
		}
	}
	return changed
}

// applyGrantCells 落成员资格。**先 revoke 后 grant**:revoke 是唯一会打断
// 线上流量的操作,先做能让部分失败停在更保守的状态。
func applyGrantCells(gdb *gorm.DB, cells []Cell, operatorId int) error {
	now := common.GetTimestamp()
	return gdb.Transaction(func(tx *gorm.DB) error {
		for _, cell := range cells {
			if cell.Action != ActionRevoke {
				continue
			}
			if err := tx.Where("user_group = ? AND model_group = ?", cell.UserGroup, cell.ModelGroup).
				Delete(&Grant{}).Error; err != nil {
				return err
			}
		}
		for _, cell := range cells {
			if cell.Action != ActionGrant {
				continue
			}
			var existing Grant
			err := tx.Where("user_group = ? AND model_group = ?", cell.UserGroup, cell.ModelGroup).
				Take(&existing).Error
			if err == nil {
				continue
			}
			if !errors.Is(err, gorm.ErrRecordNotFound) {
				return err
			}
			row := &Grant{
				UserGroup: cell.UserGroup, ModelGroup: cell.ModelGroup,
				OperatorId: operatorId, CreatedAt: now, UpdatedAt: now,
			}
			if err := tx.Create(row).Error; err != nil {
				return err
			}
		}
		return nil
	})
}

func grantsToLists(grants map[string]map[string]struct{}) map[string][]string {
	out := make(map[string][]string, len(grants))
	for ug, set := range grants {
		names := make([]string, 0, len(set))
		for mg := range set {
			names = append(names, mg)
		}
		sort.Strings(names)
		out[ug] = names
	}
	return out
}

// ───────────────── PUT /group-matrix/scope/:user_group ─────────────────

type putScopeReq struct {
	Managed   bool   `json:"managed"`
	Mode      string `json:"mode"`
	AllowAuto *bool  `json:"allow_auto"`
	Note      string `json:"note"`
	// 切到 **enforce** 时必填:服务端会重算并比对,任一不符返回 409。
	// 切到 shadow 不需要(shadow 不产生任何影响)。
	DraftHash  string `json:"draft_hash"`
	ImpactHash string `json:"impact_hash"`
}

// adminPutScope 建立/撤销接管,或切换 shadow ↔ enforce。
//
// 首次接管时 grants 预填 = 切换**之前**调 service.GetUserUsableGroups(该分组)
// 的实际结果 —— 也就是上游差分算完之后的样子。运营看到的第一屏与现状完全一致,
// 因此不需要任何迁移工具,而且开箱零行为变更。
//
// 这一点很容易做错:预填值必须来自 **hook 未生效** 的那条路径。
// unmanaged 分支本来就返回上游原值,所以"切之前读一次"即可 —— 顺序不能反。
func adminPutScope(c *gin.Context) {
	if !guard.RequireAPI(c, guard.FlagGroupMatrix) {
		return
	}
	userGroup := strings.TrimSpace(c.Param("user_group"))
	if userGroup == "" || len(userGroup) > maxGroupNameLen {
		badRequest(c, "user_group 非法")
		return
	}
	if userGroup == autoGroup {
		badRequest(c, "auto 是伪分组,不能作为用户分组接管")
		return
	}
	var req putScopeReq
	if err := c.ShouldBindJSON(&req); err != nil {
		badRequest(c, "请求体格式错误")
		return
	}
	if req.Managed && req.Mode != ModeShadow && req.Mode != ModeEnforce {
		badRequest(c, "mode 必须是 shadow 或 enforce")
		return
	}

	gdb := db.Get()
	var before Scope
	err := gdb.Where("user_group = ?", userGroup).Take(&before).Error
	existed := err == nil
	if err != nil && !errors.Is(err, gorm.ErrRecordNotFound) {
		internalError(c, err)
		return
	}

	action := auditActionScopeUpdate
	if existed && req.Managed {
		action = auditActionModeUpdate
	}

	// 切 enforce 是唯一会真的把用户挡在门外的动作,必须先看过影响面。
	if req.Managed && req.Mode == ModeEnforce {
		if req.DraftHash == "" || req.ImpactHash == "" {
			writeScopeFailure(c, action, &before, nil, errors.New("切 enforce 缺少 draft_hash / impact_hash"))
			badRequest(c, "切换到 enforce 之前必须先调 POST /group-matrix/preview 看过影响面,"+
				"并把返回的 draft_hash 与 impact_hash 一起回传")
			return
		}
		fresh, err := previewDigest(userGroup)
		if err != nil {
			internalError(c, err)
			return
		}
		if fresh.Incomplete {
			writeScopeFailure(c, action, &before, nil, errors.New("影响面统计不完整,禁止切 enforce"))
			conflict(c, "影响面统计不完整(超时或超过 max_preview_pairs),"+
				"在看不见影响面的情况下禁止切 enforce —— 宁可切不了")
			return
		}
		if fresh.DraftHash != req.DraftHash {
			// 切 enforce 用的是**已落库**的清单,所以服务端重算出来的草稿指纹
			// 必然是"空动作列表"的那一个。对不上说明客户端预览的是一份还没保存的
			// 草稿 —— 那正是「预览的是 A、保存的是 B」。
			writeScopeFailure(c, action, &before, nil, errors.New("draft_hash 不匹配"))
			conflict(c, "预览用的是一份尚未保存的草稿。请先保存清单改动,再重新预览,最后才切 enforce")
			return
		}
		if fresh.ImpactHash != req.ImpactHash {
			writeScopeFailure(c, action, &before, nil, errors.New("impact_hash 不匹配"))
			conflict(c, "影响面已经变化(预览之后有人建了新令牌、改了倍率表或改了清单),请重新预览")
			return
		}
	}

	now := common.GetTimestamp()
	var after *Scope
	if req.Managed {
		allowAuto := defaultAllowAuto(userGroup)
		if req.AllowAuto != nil {
			allowAuto = *req.AllowAuto
		} else if existed {
			allowAuto = before.AllowAuto
		}
		after = newScope(userGroup, req.Mode, allowAuto, req.Note, c.GetInt("id"), now)
	}

	// 首次接管的预填必须在写 scope 行**之前**算好:写完之后再算,
	// 拿到的就是 hook 已经生效的结果(空清单),预填会变成一次静默的全量撤销。
	// make 而不是 var:nil 切片会被序列化成 JSON null,前端对着 null 调 .map 会白屏。
	prefill := make([]string, 0)
	if !existed && req.Managed {
		prefill = currentUsableGroups(userGroup)
	}

	err = gdb.Transaction(func(tx *gorm.DB) error {
		if after == nil {
			// 撤销接管 = 删 scope 行 → 该用户分组回到 inherit(上游行为逐位一致)。
			// grants 刻意**保留**:重新接管时它就是现成的清单,而且删掉之后
			// 「上一次配的是什么」就只剩审计能回答了。
			return tx.Where("user_group = ?", userGroup).Delete(&Scope{}).Error
		}
		if err := tx.Save(after).Error; err != nil {
			return err
		}
		return ensureGrants(tx, userGroup, prefill, c.GetInt("id"), now)
	})
	if err != nil {
		writeScopeFailure(c, action, scopeOrNil(existed, before), after, err)
		internalError(c, err)
		return
	}
	audit.Write(c, scopeAuditEntry(c, action, scopeOrNil(existed, before), after, prefill))

	if err := InvalidateAndReload(); err != nil {
		common.SysError("qianye/groupmatrix: 写入后刷新快照失败(其它节点仍会按周期刷新): " + err.Error())
	}

	// 与 PUT /group-matrix 回同一个结构:前端把响应 setQueryData 进同一个缓存键,
	// 回一个 {user_group, managed, scope, ...} 的小对象会让下一次渲染
	// model_groups 为 undefined,整页崩到错误边界 —— 而改动其实已经落库了。
	respondMatrix(c, gdb, nil)
}

// ensureGrants 幂等地把一批授权写进扩展库。
//
// ══════════════ 为什么必须先查再建 ══════════════
//
// 撤销接管刻意只删 scope 行、**保留 grants**(重新接管时它就是现成的清单,
// 而且删掉之后「上一次配的是什么」就只剩审计能回答)。于是
// 「接管 → 撤销接管 → 再接管」这条路上,预填会对已经存在的行再 Create 一次,
// 撞上 uk_qy_ggrant_pair 唯一键 → 整个事务回滚 → 接口 500 →
// **该用户分组从此再也接管不了**,除非有人手工去库里删行。
//
// 撤销接管是本方案宣称的核心回退能力。回退一次就再也走不回来,那不叫回退。
func ensureGrants(tx *gorm.DB, userGroup string, modelGroups []string, operatorId int, now int64) error {
	for _, mg := range modelGroups {
		if mg == autoGroup || mg == "" {
			// auto 是伪分组,它的可选性由 scope.AllowAuto 控制,不进 grants。
			continue
		}
		var existing Grant
		err := tx.Where("user_group = ? AND model_group = ?", userGroup, mg).Take(&existing).Error
		if err == nil {
			continue
		}
		if !errors.Is(err, gorm.ErrRecordNotFound) {
			return err
		}
		if err := tx.Create(&Grant{
			UserGroup: userGroup, ModelGroup: mg,
			OperatorId: operatorId, CreatedAt: now, UpdatedAt: now,
		}).Error; err != nil {
			return err
		}
	}
	return nil
}

func scopeOrNil(existed bool, s Scope) *Scope {
	if !existed {
		return nil
	}
	return &s
}

// currentUsableGroups 取该用户分组**此刻**的可选清单(经上游全部规则算完之后)。
//
// 它就是 service.GetUserUsableGroups —— 调用是安全的:此刻该分组还没有 scope 行,
// Resolve 走 unmanaged 分支恒等返回上游,不会读到我们自己写的东西。
func currentUsableGroups(userGroup string) []string {
	got := service.GetUserUsableGroups(userGroup)
	out := make([]string, 0, len(got))
	for name := range got {
		out = append(out, name)
	}
	sort.Strings(out)
	return out
}

// defaultAllowAuto 首次接管时 auto 的预填值 = 「auto 当前是否在可选清单里」。
// 不借收紧之机顺手关掉一个功能:零行为变更的开箱状态是硬要求。
func defaultAllowAuto(userGroup string) bool {
	_, ok := service.GetUserUsableGroups(userGroup)[autoGroup]
	return ok
}

// ───────────────── POST /group-matrix/repair-token ─────────────────

type repairTokenReq struct {
	TokenId int `json:"token_id"`
}

// adminRepairToken 把一条孤儿令牌的分组置空。
//
// **只提供单条,不提供批量。** 批量改写 545 行 tokens.group 不可逆,
// 而"用户当初选这个分组是有原因的"这件事系统不知道。
//
// 「置空」是唯一既安全又不猜用户意图的修复:令牌分组为空时 UsingGroup 恒等于
// users.group(middleware/auth.go 保证),立即恢复可用且不改变该用户的可用范围。
// 「改成某个指定分组」需要替用户做选择,风险更高,不做。
func adminRepairToken(c *gin.Context) {
	if !guard.RequireAPI(c, guard.FlagGroupMatrix) {
		return
	}
	var req repairTokenReq
	if err := c.ShouldBindJSON(&req); err != nil || req.TokenId <= 0 {
		badRequest(c, "token_id 非法")
		return
	}
	if model.DB == nil {
		internalError(c, errors.New("主库未就绪"))
		return
	}
	var tk model.Token
	if err := model.DB.Where("id = ?", req.TokenId).Take(&tk).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			notFound(c, "令牌不存在")
			return
		}
		internalError(c, err)
		return
	}
	if tk.Group == "" {
		badRequest(c, "该令牌的分组本来就是空的,无需修复")
		return
	}
	oldGroup := tk.Group
	// 列名必须是**裸名**:model.QyCommonGroupCol() 返回的是已经带引号的串,
	// 交给 GORM 的列位参数会被再引一次(SQLite 上生成 SET ``group``= 直接语法错误,
	// 于是 SQLite 部署上这个唯一的孤儿修复出口一条都修不了)。
	// 裸名交给 GORM,引号规则自动跟着方言走。
	err := model.DB.Model(&model.Token{}).Where("id = ?", tk.Id).
		Update("group", "").Error
	entry := audit.Entry{
		Category: auditCategoryGroupMatrix, Action: auditActionTokenRepair,
		ActorType: qymodel.ActorAdmin, ActorUserId: c.GetInt("id"), ActorName: c.GetString("username"),
		TargetUserId: tk.UserId,
		Reason: fmt.Sprintf("把令牌 %d(用户 %d)的分组由 %q 置空,使其回落到用户分组",
			tk.Id, tk.UserId, oldGroup),
		BeforeSnap: fmt.Sprintf(`{"token_id":%d,"group":%q}`, tk.Id, oldGroup),
		AfterSnap:  fmt.Sprintf(`{"token_id":%d,"group":""}`, tk.Id),
	}
	if err != nil {
		entry.Result = qymodel.ResultFail
		entry.Reason = "孤儿令牌修复失败: " + err.Error() + " | " + entry.Reason
		audit.Write(c, entry)
		internalError(c, err)
		return
	}
	audit.Write(c, entry)
	// 令牌缓存里还留着旧分组,不清掉的话修复要等缓存自然过期才生效 ——
	// 而"修复了但用户还在 403"是最容易被当成修复无效的表现。
	if cErr := model.InvalidateUserTokensCache(tk.UserId); cErr != nil {
		common.SysError("qianye/groupmatrix: 清理令牌缓存失败(修复已落库,最迟随缓存过期生效): " + cErr.Error())
	}
	respond(c, gin.H{"token_id": tk.Id, "old_group": oldGroup, "group": ""})
}

// ─────────────────────────── 审计 ───────────────────────────

func matrixAuditEntry(c *gin.Context, action string, cells []Cell, before any, err error) audit.Entry {
	e := audit.Entry{
		Category: auditCategoryGroupMatrix, Action: action,
		ActorType: qymodel.ActorAdmin, ActorUserId: c.GetInt("id"), ActorName: c.GetString("username"),
		BeforeSnap: snapshotJSON(before),
		AfterSnap:  snapshotJSON(cells),
	}
	grant, revoke, reprice := 0, 0, 0
	for _, cell := range cells {
		switch cell.Action {
		case ActionGrant:
			grant++
		case ActionRevoke:
			revoke++
		default:
			reprice++
		}
	}
	// **撤销单独计数**:它是唯一会打断线上流量的操作类型,不能和改价混进一个数字。
	e.Reason = fmt.Sprintf("矩阵改动:放开 %d 项 / 撤销 %d 项 / 改价 %d 项", grant, revoke, reprice)
	if err != nil {
		e.Result = qymodel.ResultFail
		e.Reason = "失败(已回滚): " + err.Error() + " | " + e.Reason
	}
	return e
}

func writeMatrixFailure(c *gin.Context, action string, cells []Cell, before any, err error) {
	audit.Write(c, matrixAuditEntry(c, action, cells, before, err))
}

func scopeAuditEntry(c *gin.Context, action string, before, after *Scope, prefill []string) audit.Entry {
	e := audit.Entry{
		Category: auditCategoryGroupMatrix, Action: action,
		ActorType: qymodel.ActorAdmin, ActorUserId: c.GetInt("id"), ActorName: c.GetString("username"),
		BeforeSnap: snapshotJSON(before),
		AfterSnap:  snapshotJSON(after),
	}
	switch {
	case before == nil && after != nil:
		e.Reason = fmt.Sprintf("接管用户分组 %s(mode=%s,allow_auto=%v),预填清单 %d 项:%v",
			after.UserGroup, after.Mode, after.AllowAuto, len(prefill), prefill)
	case before != nil && after == nil:
		e.Reason = fmt.Sprintf("撤销接管用户分组 %s —— 它回到上游行为(全局白名单 + 特殊规则 + 补自己)",
			before.UserGroup)
	case before != nil && after != nil:
		e.Reason = fmt.Sprintf("用户分组 %s 的接管模式 %s → %s(allow_auto %v → %v)",
			after.UserGroup, before.Mode, after.Mode, before.AllowAuto, after.AllowAuto)
	}
	return e
}

func writeScopeFailure(c *gin.Context, action string, before, after *Scope, err error) {
	e := scopeAuditEntry(c, action, before, after, nil)
	e.Result = qymodel.ResultFail
	e.Reason = "接管变更失败: " + err.Error() + " | " + e.Reason
	audit.Write(c, e)
}

func snapshotJSON(v any) string {
	if v == nil {
		return ""
	}
	b, err := common.Marshal(v)
	if err != nil {
		return ""
	}
	return string(b)
}

// ─────────────────────────── HTTP 信封 ───────────────────────────

func respond(c *gin.Context, data any) {
	c.JSON(http.StatusOK, gin.H{"success": true, "message": "", "data": data})
}

func respondFail(c *gin.Context, status int, code, msg string) {
	c.JSON(status, gin.H{"success": false, "code": code, "message": msg})
}

func badRequest(c *gin.Context, msg string) {
	respondFail(c, http.StatusBadRequest, "qy_gm_bad_request", msg)
}

func notFound(c *gin.Context, msg string) {
	respondFail(c, http.StatusNotFound, "qy_gm_not_found", msg)
}

// conflict 是"你看的和现在的不是同一份数据"。它与 400 分开:
// 400 是"你写错了",409 是"你没写错,但世界变了,请重新看一眼"。
func conflict(c *gin.Context, msg string) {
	respondFail(c, http.StatusConflict, "qy_gm_conflict", msg)
}

func internalError(c *gin.Context, err error) {
	common.SysError("qianye/groupmatrix: 接口处理失败: " + err.Error())
	respondFail(c, http.StatusInternalServerError, "qy_internal_error", "处理失败,请稍后重试")
}
