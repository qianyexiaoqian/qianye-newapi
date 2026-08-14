package planentitlement

import (
	"context"
	"fmt"
	"math"
	"net/http"
	"sort"
	"strings"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/qianye/db"
	"github.com/QuantumNous/new-api/qianye/guard"
	"github.com/QuantumNous/new-api/qianye/httpq"
	qymodel "github.com/QuantumNous/new-api/qianye/model"
	"github.com/QuantumNous/new-api/qianye/service/audit"
	"github.com/QuantumNous/new-api/service"
	"github.com/QuantumNous/new-api/setting/ratio_setting"

	"github.com/gin-gonic/gin"
)

// ratioCell 是「某个用户分组用这个模型分组时,实际按几倍扣费」。
//
// ═══════════ 为什么这张表必须常驻在套餐详情页上 ═══════════
//
// 套餐解锁的是**成员资格**,倍率仍由 (用户分组, 模型分组) 决定。于是会出现这样
// 一条链:用户分组 A 的权威清单里没有 G,但 A 组的某个用户买了解锁 G 的套餐 ——
// 他现在可以用 G,按 GroupRatio[G] 计费,而矩阵页的 A 行根本不渲染 (A,G) 这一格。
// 运营看那张页面得到的结论是「A 组的人永远碰不到 G」,几周后他为了别的目的把
// GroupRatio[G] 从 1.0 调到 3.0,A 组所有持有该套餐的用户当场按 3 倍扣费,
// 而站内没有任何一个页面显示过 A 组的人可以到达 G。
//
// 这不是倍率数值的漂移,是**可达性认知的漂移**,后果直接是钱。这张表是它的
// 一半解药(另一半在矩阵页:可达性要按 scope-grants ∪ plan-unlocks 计算)。
type ratioCell struct {
	UserGroup  string  `json:"user_group"`
	ModelGroup string  `json:"model_group"`
	Ratio      float64 `json:"ratio"`
	// Source 取 cross_cell(用户分组单独配过这一格)或 group_ratio(继承兜底)。
	// 两者的数值可能相同,但含义完全不同:前者改一格只影响一个用户分组,
	// 后者改一下会影响所有继承它的分组。界面必须分开显示,不能只给一个数字。
	Source string `json:"source"`
}

// entitlementView 是套餐解锁配置的完整下发形状。
type entitlementView struct {
	PlanId    int    `json:"plan_id"`
	PlanTitle string `json:"plan_title"`
	// UnlockGroups 是这个套餐解锁的模型分组。空数组 = 没解锁任何分组(合法)。
	UnlockGroups []string `json:"unlock_groups"`
	// BalanceScope ∈ {universal, restricted}。
	BalanceScope string `json:"balance_scope"`
	Note         string `json:"note"`
	// MissingGroups 是已经不在分组倍率表里的绑定:运营删掉了一个仍被引用的
	// 模型分组。**这是 error 级信号**:这些绑定在快照里已被剔除,用户拿不到该
	// 分组;而 restricted 套餐撞上它时,余额会变成花不掉的死钱直到到期作废。
	MissingGroups []string `json:"missing_groups"`
	// ActiveSubscriptions 是当前生效中的订阅条数 —— 改这份配置会立刻影响多少人。
	ActiveSubscriptions int64 `json:"active_subscriptions"`
	// RatioTable 见 ratioCell 的说明。
	RatioTable []ratioCell `json:"ratio_table"`
	// BalanceScopeEnforced 表示「余额使用范围」此刻真的在扣费路径上生效。
	// 为 false 时 balance_scope 只是一份存下来的配置 —— 界面必须说明这一点,
	// 否则运营会以为已经限制住了(见 seams.go 的 balanceScopeEnforced)。
	BalanceScopeEnforced bool `json:"balance_scope_enforced"`
	// Enabled 是本模块的 kill switch(plan_entitlement.enabled)此刻的状态。
	//
	// 为 false 时**这整个弹窗配的东西一样都不生效**:已购用户拿不到套餐解锁的
	// 模型分组,余额范围也不过滤。而管理端仍然能配、能保存成功、审计也记成功 ——
	// 界面必须自己说出这件事,否则运营会对着一个已经被拉闸的功能反复调参。
	Enabled bool `json:"enabled"`
	// ModelGroupCandidates 是多选框的候选集合:**模型分组轴**的事实清单
	// (options.GroupRatio 的键)。前端不要自己去别处凑这份清单 ——
	// 用户分组与模型分组共用一个字符串命名空间,凑错轴会让运营解锁出一个
	// 根本没有渠道的"分组"。
	ModelGroupCandidates []string `json:"model_group_candidates"`
}

// adminGetEntitlement 读一个套餐的解锁配置。纯读,不写审计。
func adminGetEntitlement(c *gin.Context) {
	if !guard.RequireAPI(c, guard.FlagCore) {
		return
	}
	planId, ok := planIdParam(c)
	if !ok {
		return
	}
	if model.DB == nil {
		internalError(c, db.ErrNotReady)
		return
	}
	ctx, cancel := guard.ColdContext(c.Request.Context())
	defer cancel()

	var plan model.SubscriptionPlan
	if err := model.DB.WithContext(ctx).Where("id = ?", planId).First(&plan).Error; err != nil {
		badRequest(c, "qy_subscription_plan_not_found", "套餐不存在")
		return
	}
	view, err := readPlanView(ctx, &plan)
	if err != nil {
		internalError(c, err)
		return
	}
	respond(c, view)
}

// readPlanView 读一个套餐的完整下发形状(配置 + 影响面)。
//
// GET 与 PUT 共用它:写接口必须回**同一个结构**,前端才能无条件地用响应替换本地
// 状态。早先在别处踩过的坑是写接口各回各的小对象,前端把它塞进同一个缓存键 ——
// 下一次渲染 unlock_groups 是 undefined,整页崩到错误边界,而改动其实已经落库了。
func readPlanView(ctx context.Context, plan *model.SubscriptionPlan) (entitlementView, error) {
	ent, err := readPlanEntitlement(ctx, plan.Id)
	if err != nil {
		return entitlementView{}, err
	}
	var active int64
	if err := model.DB.WithContext(ctx).Model(&model.UserSubscription{}).
		Where("user_id > 0 AND plan_id = ? AND status = ? AND "+model.SubscriptionActiveEndTimeSQL,
			plan.Id, statusActive, common.GetTimestamp()).
		Count(&active).Error; err != nil {
		return entitlementView{}, err
	}
	return buildView(plan, ent, active), nil
}

// buildView 把库里的配置渲染成下发形状。activeSubs < 0 表示本次不统计。
func buildView(plan *model.SubscriptionPlan, ent planEntitlement, activeSubs int64) entitlementView {
	groups := ent.Groups
	if groups == nil {
		groups = make([]string, 0)
	}
	out := entitlementView{
		PlanId:               plan.Id,
		PlanTitle:            plan.Title,
		UnlockGroups:         groups,
		BalanceScope:         ent.Scope,
		Note:                 ent.Note,
		MissingGroups:        make([]string, 0),
		ActiveSubscriptions:  activeSubs,
		RatioTable:           make([]ratioCell, 0, len(groups)*4),
		BalanceScopeEnforced: BalanceScopeEnforced(),
		Enabled:              enabled(),
		ModelGroupCandidates: modelGroupCandidates(),
	}
	if out.BalanceScope != ScopeRestricted {
		out.BalanceScope = ScopeUniversal
	}
	userGroups := ratioTableUserGroups()
	for _, mg := range groups {
		if !ratio_setting.ContainsGroupRatio(mg) {
			out.MissingGroups = append(out.MissingGroups, mg)
			continue
		}
		for _, ug := range userGroups {
			source := "group_ratio"
			if _, ok := ratio_setting.GetGroupGroupRatio(ug, mg); ok {
				source = "cross_cell"
			}
			out.RatioTable = append(out.RatioTable, ratioCell{
				UserGroup:  ug,
				ModelGroup: mg,
				// **现算**,不读任何扩展侧缓存:这个数必须与热路径乘进账单的那个
				// 数逐位相同,而 service.GetUserGroupRatio 就是热路径三条计费
				// 路径用的同一份判据(交叉格命中就用它,否则回落兜底)。
				Ratio:  service.GetUserGroupRatio(ug, mg),
				Source: source,
			})
		}
	}
	return out
}

// ratioTableUserGroups 是「实际会按多少扣」那张表的**行轴**:用户分组。
//
// ═══════════ 为什么不能只用 options.GroupRatio 的键 ═══════════
//
// 两个轴共用一个字符串命名空间,于是最省事的写法是拿 GroupRatio 的键当用户分组
// 清单 —— 而那恰好漏掉最容易出事的一档:一个早已从 GroupRatio 里删掉、却仍有几
// 百个 users.group 指着它、并且在 GroupGroupRatio 里留着交叉格的旧分组
// (本站的 qianye/groupratio 孤儿扫描就是为这批人建的)。它不出现在表里,运营
// 看到的最低倍率就是错的,而那批用户正按那个看不见的交叉格扣费。
//
// 所以行轴 = GroupRatio 的键 ∪ GroupGroupRatio 的一级键。后者正是"配过交叉格
// 的用户分组"的事实清单,把上述那一档带了回来,且零数据库查询。
//
// 覆盖不到的残余:一个既不在 GroupRatio、也没有任何交叉格的 users.group 孤儿。
// 它按兜底倍率计费,与表里其它继承行的数字一致,不会让运营读出错误的价格。
func ratioTableUserGroups() []string {
	set := make(map[string]struct{}, 16)
	for name := range ratio_setting.GetGroupRatioCopy() {
		if name != autoGroup {
			set[name] = struct{}{}
		}
	}
	for name := range ratio_setting.GetGroupRatioSetting().GroupGroupRatio.ReadAll() {
		if name != autoGroup {
			set[name] = struct{}{}
		}
	}
	out := make([]string, 0, len(set))
	for name := range set {
		out = append(out, name)
	}
	sort.Strings(out)
	return out
}

// modelGroupCandidates 是模型分组的事实清单:options.GroupRatio 的键集合。
//
// 它同时也是上游拿来当"用户分组清单"的那一份(controller/group.go 直接把这些键
// 当分组列表下发)。两个轴共用一个字符串命名空间是既定代价,因此管理端必须
// **始终分列显示,绝不合并成一个下拉**。
func modelGroupCandidates() []string {
	ratios := ratio_setting.GetGroupRatioCopy()
	out := make([]string, 0, len(ratios))
	for name := range ratios {
		if name == autoGroup {
			continue
		}
		out = append(out, name)
	}
	sort.Strings(out)
	return out
}

type putEntitlementRequest struct {
	// UnlockGroups 是**最终状态**(整体替换),不是增量。
	// 用指针区分"没传这个字段"与"传了空数组(= 清空解锁)"——
	// 后者是合法且必须可表达的操作。
	UnlockGroups *[]string `json:"unlock_groups"`
	BalanceScope *string   `json:"balance_scope"`
	Note         string    `json:"note"`
}

// adminPutEntitlement 设置一个套餐解锁哪些模型分组、以及它的余额使用范围。
//
// 必写审计(成功与失败各一条):这份配置直接决定「谁能用哪些模型分组」与
// 「一笔钱从哪个池子里扣」,与改分组倍率是同一档的操作。写失败时库里到底变没变
// 是不确定的(连接被掐、部分提交),而"有人在这一刻试图把某个套餐改成仅限"
// 这个事实本身与成功的那次同等重要。
func adminPutEntitlement(c *gin.Context) {
	if !guard.RequireAPI(c, guard.FlagCore) {
		return
	}
	planId, ok := planIdParam(c)
	if !ok {
		return
	}
	var req putEntitlementRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		badRequest(c, "qy_invalid_param", "请求格式错误")
		return
	}
	if req.UnlockGroups == nil {
		badRequest(c, "qy_invalid_param", "缺少 unlock_groups(要清空请传空数组)")
		return
	}
	scope := ScopeUniversal
	if req.BalanceScope != nil {
		scope = strings.TrimSpace(*req.BalanceScope)
	}
	if scope != ScopeUniversal && scope != ScopeRestricted {
		badRequest(c, "qy_plan_balance_scope_invalid",
			"余额使用范围只能是 universal(通用)或 restricted(仅限绑定的模型分组)")
		return
	}
	if model.DB == nil {
		internalError(c, db.ErrNotReady)
		return
	}

	ctx, cancel := guard.ColdContext(c.Request.Context())
	defer cancel()

	// 套餐必须真实存在。不校验的话,一个手滑打错的 plan_id 会写出一行永远不会被
	// 任何人读到的配置,而页面上完全看不出来。
	var plan model.SubscriptionPlan
	if err := model.DB.WithContext(ctx).Where("id = ?", planId).First(&plan).Error; err != nil {
		badRequest(c, "qy_subscription_plan_not_found", "套餐不存在")
		return
	}

	// 旧值必须在校验**之前**读到:已经存在的绑定要被放行(见 validateGroups 的
	// 「存量豁免」),否则一个已从倍率表消失的分组会让这个套餐永久存不下去。
	before, err := readPlanEntitlement(ctx, planId)
	if err != nil {
		// 连旧值都没读到就失败了 —— 库里此刻什么都没变,但"有人在这一刻试图动
		// 这个套餐"这个事实照样要留痕。少了这一条,一次扩展库抖动期间的反复尝试
		// 在事后完全查不到,而那正是最需要知道"谁在动它"的时刻。
		// After 写不出具体值:校验依赖旧值(存量豁免),此刻还没跑。
		audit.WriteConfigUpdate(c, audit.ConfigChange{
			Action: "subscription.plan_entitlement.update",
			Result: qymodel.ResultFail,
			Reason: fmt.Sprintf("读取套餐 %d(%s)的现有解锁配置失败,本次未做任何改动: %v", planId, plan.Title, err),
			Before: "(读取失败,旧值未知)",
			After:  "(校验依赖旧值,尚未进行)",
		})
		internalError(c, err)
		return
	}

	groups, msg := validateGroups(*req.UnlockGroups, before.Groups)
	if msg != "" {
		badRequest(c, "qy_plan_unlock_group_invalid", msg)
		return
	}
	if scope == ScopeRestricted {
		// 一份任何请求都用不上的额度池 = 纯粹的死钱。在写入侧堵死,
		// 而不是等用户来投诉"我有 10 美元却花不出去"。
		//
		// ═══════ 判据是**仍然有效**的绑定数,不是 len(groups) ═══════
		//
		// validateGroups 对存量绑定有豁免(见它的注释:分组被删之后不能让这个
		// 套餐从此存不下去),所以 groups 里完全可能只剩一个已经从倍率表里删掉的
		// 名字。那种绑定会被快照编译期原样剔除(不变量 I4),于是 Snapshot.Binds
		// 恒 false → CandidateUsable 恒 false → 这个套餐对**任何**模型分组都不是
		// 出资候选:用户看得见余额、一分也花不掉,直到到期作废。
		//
		// 只判 len(groups)==0 的话,这条路径能直接 PUT 进库,而"服务端在写入侧
		// 堵死了死钱"这句话就只剩前端那道 restrictedWithoutBinding 还成立 ——
		// 任何脚本、任何没实现那道校验的客户端都能绕过去。
		live := 0
		for _, g := range groups {
			if ratio_setting.ContainsGroupRatio(g) {
				live++
			}
		}
		if live == 0 {
			badRequest(c, "qy_plan_balance_scope_need_binding",
				"余额使用范围设为「仅限」时必须至少绑定一个仍然存在于分组倍率表的模型分组,"+
					"否则这个套餐的额度任何请求都用不上。当前提交的绑定要么为空,要么全部指向已被删除的分组"+
					"(那些名字只是为了不让这个套餐存不下去才被放行的,它们在扣费路径上一律不生效)")
			return
		}
	}

	next := planEntitlement{PlanId: planId, Groups: groups, Scope: scope, Note: req.Note}
	if sameEntitlement(before, next) {
		// 值没变就不写库、不写审计。少了这条短路,运营每保存一次套餐表单都会
		// 留下一条没有实际改动的审计,事后查"谁把这个套餐改成仅限了"要从一堆
		// 空改动里人工筛。仍然回完整视图:前端拿响应替换本地状态这件事,
		// 不该因为"这次没改动"而变成另一条分支。
		respondView(c, ctx, &plan)
		return
	}

	if err := writePlanEntitlement(ctx, next, c.GetInt("id")); err != nil {
		audit.WriteConfigUpdate(c, audit.ConfigChange{
			Action: "subscription.plan_entitlement.update",
			Result: qymodel.ResultFail,
			Reason: fmt.Sprintf("设置套餐 %d(%s)的解锁模型分组与余额使用范围失败: %v", planId, plan.Title, err),
			Before: describeEntitlement(before),
			After:  describeEntitlement(next),
		})
		internalError(c, err)
		return
	}

	audit.WriteConfigUpdate(c, audit.ConfigChange{
		Action: "subscription.plan_entitlement.update",
		Result: qymodel.ResultOK,
		Reason: fmt.Sprintf("设置套餐 %d(%s)的解锁模型分组与余额使用范围", planId, plan.Title),
		Before: describeEntitlement(before),
		After:  describeEntitlement(next),
	})

	// 本节点立刻用上新配置;其它节点最多滞后一个 cache_seconds。
	if err := InvalidateAndReload(); err != nil {
		common.SysError("qianye/planentitlement: 保存后刷新快照失败(最多滞后一个刷新周期): " + err.Error())
	}
	// 解锁集合变了,持有该套餐的人的 per-user 缓存里那份结论也就过期了。
	// 这里只能整体清空:反查"谁持有这个套餐"要扫主库,而这是一次极低频的
	// 管理端写操作,清空的代价是每个活跃用户重新回源一次。
	InvalidateUser(0)

	respondView(c, ctx, &plan)
}

// respondView 回读一次并下发完整视图。回读失败时退回一个最小对象而不是报错:
// 改动**已经落库了**,这时候回 500 会让运营以为没保存成功并再点一次。
func respondView(c *gin.Context, ctx context.Context, plan *model.SubscriptionPlan) {
	view, err := readPlanView(ctx, plan)
	if err != nil {
		common.SysError("qianye/planentitlement: 保存成功但回读失败: " + err.Error())
		respond(c, gin.H{"plan_id": plan.Id})
		return
	}
	respond(c, view)
}

// validateGroups 校验解锁清单。返回排序去重后的结果;第二个返回值非空即拒绝。
//
// existing 是这个套餐**当前已经存下来的**解锁清单,用于存量豁免(见下)。
//
// 三条硬拒绝(不是警告):
//
//	不在分组倍率表里  → 它一旦被解锁,必然撞上上游 GetGroupRatio 的 fail-open:
//	                    找不到分组时**静默返回 1 并只写一条 SysLog**,按原价扣费、
//	                    零告警。同时 middleware/auth.go 紧随其后的
//	                    ContainsGroupRatio 会用「分组已被弃用」把用户挡掉 ——
//	                    也就是说这种配置一定不生效,却在页面上看起来是通的。
//	auto              → auto 不是模型分组,是"自动选一个"的开关。
//	超过上限          → 别让一次脚本误操作把整张倍率表灌进一个套餐。
//
// ═══════════ 存量豁免:已经存在的名字不能让这个套餐存不下去 ═══════════
//
// 上游删除模型分组时不做任何引用检查。于是「保存时校验通过 → 分组被删 → 这个
// 套餐从此永远存不下去」是一条必然会走到的路:管理端把已存的绑定原样回显、
// 用户点保存,校验拿掉它 → 400「模型分组不可解锁」。连"把余额范围改回通用"
// 这个界面自己给的补救按钮也一起 400,因为它照样要提交整份清单。
//
// 所以:**已经在库里的名字一律放行**,新加的名字照旧硬拒。放行不等于纵容 ——
// 快照编译期仍会把它剔除(不变量 I4)、管理端仍会红色横幅列出 missing_groups、
// /admin/health 仍会标 needs_attention。区别只在于运营现在有路可走:
// 他可以在界面上把那个名字取消勾选,而不是被困在一个自己修不好的错误里。
func validateGroups(in []string, existing []string) ([]string, string) {
	grandfathered := make(map[string]struct{}, len(existing))
	for _, g := range existing {
		grandfathered[g] = struct{}{}
	}
	cleaned := make([]string, 0, len(in))
	for _, raw := range in {
		g := strings.TrimSpace(raw)
		if g == "" {
			continue
		}
		if len(g) > maxGroupNameLen {
			return nil, fmt.Sprintf("模型分组名过长(上限 %d 字节):%s", maxGroupNameLen, g)
		}
		if g == autoGroup {
			return nil, "auto 不是模型分组,不能被套餐解锁(它是「自动选一个分组」的开关)"
		}
		if _, kept := grandfathered[g]; !kept && !ratio_setting.ContainsGroupRatio(g) {
			return nil, fmt.Sprintf("模型分组 %q 不在系统的分组倍率表里%s —— "+
				"解锁一个不存在的分组不会有任何效果,用户拿到它之后会被「分组已被弃用」挡掉",
				g, nearestHint(g))
		}
		cleaned = append(cleaned, g)
	}
	out := sortedUnique(cleaned)
	if len(out) > maxGroupsPerPlan {
		return nil, fmt.Sprintf("一个套餐最多解锁 %d 个模型分组", maxGroupsPerPlan)
	}
	return out, ""
}

// nearestHint 给出大小写近似项的提示。
//
// 大小写**不折叠**是刻意的(见 PlanGrant.ModelGroup 的注释),但把 "VIP 与 vip
// 只差大小写"这件事直接说出来,能把最常见的一次手误从"保存成功却永远不生效"
// 变成"当场看懂"。
func nearestHint(g string) string {
	lower := strings.ToLower(g)
	for name := range ratio_setting.GetGroupRatioCopy() {
		if name != g && strings.ToLower(name) == lower {
			return fmt.Sprintf("(是不是想选 %q?分组名区分大小写)", name)
		}
	}
	return ""
}

func sameEntitlement(a, b planEntitlement) bool {
	if a.Scope != b.Scope || a.Note != b.Note || len(a.Groups) != len(b.Groups) {
		return false
	}
	for i := range a.Groups {
		if a.Groups[i] != b.Groups[i] {
			return false
		}
	}
	return true
}

// describeEntitlement 把配置渲染成审计里读得懂的一句话。
//
// 直接写空数组的话,审计详情里"没解锁任何分组"与"没配过"长得一模一样,
// 而这两件事在复盘时的含义完全不同。
func describeEntitlement(e planEntitlement) string {
	groups := "(未解锁任何模型分组)"
	if len(e.Groups) > 0 {
		groups = strings.Join(e.Groups, "、")
	}
	scope := "通用(余额可用于任何模型分组)"
	if e.Scope == ScopeRestricted {
		scope = "仅限(余额只能用于上面这些模型分组)"
	}
	return fmt.Sprintf("解锁分组: %s;余额使用范围: %s", groups, scope)
}

// planIdParam 解析并校验路径上的 plan_id,失败时已经写好响应。
func planIdParam(c *gin.Context) (int, bool) {
	planId64, ok := httpq.PathInt64(c, "plan_id")
	if !ok || planId64 <= 0 || planId64 > math.MaxInt32 {
		badRequest(c, "qy_invalid_param", "套餐 ID 非法")
		return 0, false
	}
	return int(planId64), true
}

// ───────────────────────────── 响应辅助 ─────────────────────────────
//
// 与扩展其余部分保持同一个信封:{success, message, data},失败时额外带 code
// 供前端做 i18n 映射,不把中文写死在前端。

func respond(c *gin.Context, data any) {
	c.JSON(http.StatusOK, gin.H{"success": true, "message": "", "data": data})
}

func badRequest(c *gin.Context, code, msg string) {
	c.JSON(http.StatusBadRequest, gin.H{"success": false, "code": code, "message": msg})
}

func internalError(c *gin.Context, err error) {
	common.SysError("qianye/planentitlement: 接口处理失败: " + err.Error())
	c.JSON(http.StatusInternalServerError, gin.H{
		"success": false, "code": "qy_internal_error", "message": "处理失败,请稍后重试",
	})
}
