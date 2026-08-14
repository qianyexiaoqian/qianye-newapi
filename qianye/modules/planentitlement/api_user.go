package planentitlement

import (
	"context"
	"sort"
	"strings"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/qianye/db"
	"github.com/QuantumNous/new-api/qianye/guard"
	"github.com/QuantumNous/new-api/setting/ratio_setting"

	"github.com/gin-gonic/gin"
)

// api_user.go —— 用户看得见的那一半:「我的哪一张套餐会先被扣、它能用在什么上」。
//
// ═══════════ 为什么必须新开一个接口,而不是改上游那个 ═══════════
//
// 上游 model.GetAllActiveUserSubscriptions 按 `end_time desc` 排序(展示用),
// 而扣费 model.PreConsumeUserSubscription 按 `end_time asc, id asc` 遍历候选
// (最先到期的先扣)。**运营和用户看到的顺序,与实际扣钱的顺序正好相反。**
//
// 但那一行 ORDER BY 不能改:
//
//	(a) 它是展示函数,上游自己的订阅页也在消费它,改了会改变我们没有审计过的
//	    上游 UI 行为;
//	(b) 铁律要求每一行上游改动都无法避免,而这一行可以避免;
//	(c) 最根本的一条 —— 引入「仅限某些模型分组」的余额之后,「下一张会被扣的是
//	    哪一个」本身就**依赖本次请求用的模型分组**。一个与分组无关的全局 asc
//	    排序**同样是在骗人**,那一行上游改动买不到真话。
//
// 所以真话只能在我们自己的界面里说:本接口按扣费顺序排列,并按**当前所选模型
// 分组**计算「本次将优先扣除」。
//
// ═══════════ 这里给出的是判据,不是承诺 ═══════════
//
// 真正的扣费发生在一个持锁的事务里,而本接口是一次无锁的只读快照。两者之间
// 用户可能又发了十次请求。因此 will_charge_first 是"按此刻的余额,下一笔会先动
// 这一张",不是"这一笔一定从这里扣"。界面文案必须是"将优先扣除"而不是"将扣除"。

// subscriptionView 是一条活跃订阅在用户端的样子。
type subscriptionView struct {
	UserSubscriptionId int    `json:"user_subscription_id"`
	PlanId             int    `json:"plan_id"`
	PlanTitle          string `json:"plan_title"`

	// AmountTotal 为 0 表示**不限额**(上游语义),此时 Remaining 无意义。
	AmountTotal int64 `json:"amount_total"`
	AmountUsed  int64 `json:"amount_used"`
	Unlimited   bool  `json:"unlimited"`
	// Remaining 已经把"本次消费前会先跑一次周期重置"算进去了(见 PendingReset)。
	// 不算的话,一张刚跨过重置点的套餐会显示成"已用尽",而用户下一笔请求恰恰
	// 会从它这里扣 —— 显示与扣费给出相反的答案,是最难解释的一类工单。
	Remaining int64 `json:"remaining"`
	// PendingReset 表示这条订阅的周期重置时间已经到了,但还没有人触发它。
	// 重置发生在**下一次扣费的事务内**(上游 maybeResetUserSubscriptionWithPlanTx),
	// 而定时任务只在 master 节点跑 —— 没有 master 的部署里它就只在请求路径上发生。
	PendingReset  bool  `json:"pending_reset"`
	NextResetTime int64 `json:"next_reset_time"`

	StartTime int64 `json:"start_time"`
	EndTime   int64 `json:"end_time"`

	// AllowWalletOverflow 是这条订阅的"额度用尽后允许使用钱包余额"快照。
	AllowWalletOverflow bool `json:"allow_wallet_overflow"`

	// BalanceScope ∈ {universal, restricted}。
	BalanceScope string `json:"balance_scope"`
	// BoundGroups 是该套餐解锁 / 绑定的模型分组。restricted 时它同时是
	// "这笔余额能用在什么上"的**权威清单**;universal 时它只是解锁清单。
	BoundGroups []string `json:"bound_groups"`

	// UsableHere 只在请求带了 model_group 时有意义:这笔余额能不能用于该分组。
	// restricted 且分组不在绑定里时为 false —— 那不是"余额不足",是"用不了"。
	UsableHere bool `json:"usable_here"`
	// WillChargeFirst 标出按当前余额、当前所选分组,下一笔会先动哪一张。
	WillChargeFirst bool `json:"will_charge_first"`
}

// planGrantView 是**一个套餐**的权益披露,与当前用户是否持有它无关。
//
// 为什么必须与 subscriptionView 分开:subscriptionView 只覆盖用户**已持有**的
// 活跃订阅,拿它当买前披露的数据源,等于「买了才告诉你这笔余额只能花在哪」——
// 而「余额仅限某些模型分组」恰恰是付款**前**必须知道、付款后才发现等于误导的
// 那一条。买家在钱包页套餐卡与购买确认弹窗上读的就是这一段。
//
// 只列快照里配置过权益的套餐:没配过的套餐行为与上游逐位一致,没有额外的话要说,
// 硬给它编一行「不解锁任何分组」反而是替后端编造事实。
type planGrantView struct {
	PlanId       int      `json:"plan_id"`
	BalanceScope string   `json:"balance_scope"`
	BoundGroups  []string `json:"bound_groups"`
}

// entitlementsResponse 是接口的完整下发形状。
type entitlementsResponse struct {
	// Plans 是买**前**的披露:全站配置过权益的每个套餐各一条。
	Plans []planGrantView `json:"plans"`
	// ModelGroup 是本次判定用的模型分组(回显)。为空表示没指定,
	// 此时 UsableHere 一律为 true、WillChargeFirst 按纯余额判定。
	ModelGroup string `json:"model_group"`
	// Subscriptions 已按**扣费顺序**(end_time asc, id asc)排好。
	Subscriptions []subscriptionView `json:"subscriptions"`
	// UnlockedGroups 是这些套餐一共给用户解锁了哪些模型分组(并集)。
	UnlockedGroups []string `json:"unlocked_groups"`
	// AnyRestricted 为真表示用户至少有一张"仅限"套餐 —— 前端据此决定要不要
	// 在令牌页/聊天页提示「换个分组就不会用套餐余额了」。
	AnyRestricted bool `json:"any_restricted"`
	// BalanceScopeEnforced 表示「仅限」此刻真的在扣费路径上生效。为 false 时
	// usable_here 一律为 true(展示与扣费同一个判据,两边同时说真话)。
	BalanceScopeEnforced bool `json:"balance_scope_enforced"`
	// WalletFallbackBlocked 为真表示**在本次所选分组下有资格出资**的那批套餐里,
	// 至少有一张不允许用钱包余额兜底。判定主体只包含有资格的候选:
	// 一张仅限 G 的套餐没有资格禁止用户用钱包去买 H(见 CandidateUsable)。
	WalletFallbackBlocked bool `json:"wallet_fallback_blocked"`
}

// userEntitlements 返回当前用户的活跃套餐,按扣费顺序排列。
//
// 查询判据与 model.PreConsumeUserSubscription 的候选查询逐字相同
// (status='active' AND end_time > now,ORDER BY end_time asc, id asc)——
// 顺序与集合一旦分叉,这个页面就在骗人,而它存在的**全部**理由就是别骗人。
func userEntitlements(c *gin.Context) {
	if !guard.RequireAPI(c, guard.FlagCore) {
		return
	}
	userId := c.GetInt("id")
	if userId <= 0 {
		badRequest(c, "qy_invalid_param", "登录状态异常")
		return
	}
	if model.DB == nil {
		internalError(c, db.ErrNotReady)
		return
	}
	modelGroup := strings.TrimSpace(c.Query("model_group"))
	if modelGroup != "" && !ratio_setting.ContainsGroupRatio(modelGroup) {
		// 不是错误:令牌可能指着一个已经被删掉的分组。按"不指定"处理并回显空串,
		// 让前端知道这次判定没有用上分组条件。
		modelGroup = ""
	}

	ctx, cancel := guard.ColdContext(c.Request.Context())
	defer cancel()

	now := common.GetTimestamp()
	subs := make([]model.UserSubscription, 0, 8)
	if err := model.DB.WithContext(ctx).
		Where("user_id = ? AND status = ? AND "+model.SubscriptionActiveEndTimeSQL, userId, statusActive, now).
		Order("end_time asc, id asc").
		Find(&subs).Error; err != nil {
		internalError(c, err)
		return
	}

	// 一次请求只 Load 一次快照:同一份判据必须贯穿本次响应的每一行,
	// 否则列表里前后两条可能读到跨了一次刷新的两份配置。
	snap := Current()

	plans := planFacts(ctx, subs)
	out := entitlementsResponse{
		ModelGroup:           modelGroup,
		BalanceScopeEnforced: BalanceScopeEnforced(),
		Subscriptions:        make([]subscriptionView, 0, len(subs)),
		UnlockedGroups:       make([]string, 0, 4),
	}
	unlocked := make(map[string]struct{}, 8)
	chosen := false
	for i := range subs {
		sub := &subs[i]
		bound := snap.BoundGroups(sub.PlanId)
		for _, g := range bound {
			unlocked[g] = struct{}{}
		}
		plan := plans[sub.PlanId]
		view := subscriptionView{
			UserSubscriptionId: sub.Id,
			PlanId:             sub.PlanId,
			PlanTitle:          plan.Title,
			AmountTotal:        sub.AmountTotal,
			AmountUsed:         sub.AmountUsed,
			Unlimited:          sub.AmountTotal <= 0,
			// 到点了**且套餐真的会重置**。少后半句的话,一张已经用尽、而套餐的
			// 重置周期后来被改成「不重置」的订阅会显示成满额并被标上「本次将优先
			// 扣除」,而上游 maybeResetUserSubscriptionWithPlanTx 在 never 这一档
			// 直接 return、amount_used 一分不减,钱最终从钱包出。
			// 那正是设计要消灭的「明明有余额却从主额度扣了」这类投诉。
			PendingReset:        plan.ResetsQuota && sub.NextResetTime > 0 && sub.NextResetTime <= now,
			NextResetTime:       sub.NextResetTime,
			StartTime:           sub.StartTime,
			EndTime:             sub.EndTime,
			AllowWalletOverflow: sub.AllowWalletOverflow,
			BalanceScope:        snap.BalanceScope(sub.PlanId),
			BoundGroups:         bound,
		}
		view.Remaining = remainingOf(sub, view.PendingReset)
		view.UsableHere = modelGroup == "" || snap.CandidateUsable(sub.PlanId, modelGroup)
		if view.BalanceScope == ScopeRestricted {
			out.AnyRestricted = true
		}
		// 「本次将优先扣除」= 扣费顺序里第一条**有资格出资且还有余额**的。
		// 余额是否够本次请求这里算不出来(请求还没发生),因此这是判据不是承诺。
		if !chosen && view.UsableHere && (view.Unlimited || view.Remaining > 0) {
			view.WillChargeFirst = true
			chosen = true
		}
		if view.UsableHere && !sub.AllowWalletOverflow {
			// 判定主体只包含**有资格出资**的候选。被范围挡掉的那些没有发言权 ——
			// 让一张仅限 G 的套餐去禁止用户用钱包买 H,用户没有任何操作能解除
			// 这个阻断:既用不了套餐余额(范围不符),又用不了主额度(被同一张
			// 套餐挡住),账户变成活死人。
			out.WalletFallbackBlocked = true
		}
		out.Subscriptions = append(out.Subscriptions, view)
	}
	for g := range unlocked {
		out.UnlockedGroups = append(out.UnlockedGroups, g)
	}
	sort.Strings(out.UnlockedGroups)
	out.Plans = configuredPlanGrants(snap)
	respond(c, out)
}

// configuredPlanGrants 把快照里配置过权益的套餐摊成买前披露。
//
// 纯内存:快照本身就是 planId → 绑定 / 范围 的两张 map,不需要读库,也不需要
// 知道用户是谁 —— 一个套餐解锁哪些模型分组是它的售卖条款,不是谁的隐私。
// 键取两张 map 的并集:「仅限 + 零绑定」的死钱套餐只有 Scope 一张有键,而那
// 恰恰是最需要在付款前说出口的一种。
func configuredPlanGrants(s *Snapshot) []planGrantView {
	if s == nil {
		return make([]planGrantView, 0)
	}
	ids := make(map[int]struct{}, len(s.Bind)+len(s.Scope))
	for id := range s.Bind {
		ids[id] = struct{}{}
	}
	for id := range s.Scope {
		ids[id] = struct{}{}
	}
	out := make([]planGrantView, 0, len(ids))
	for id := range ids {
		out = append(out, planGrantView{
			PlanId:       id,
			BalanceScope: s.BalanceScope(id),
			BoundGroups:  s.BoundGroups(id),
		})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].PlanId < out[j].PlanId })
	return out
}

// remainingOf 算一条订阅还剩多少额度。
//
// pendingReset 为真时按**重置后**的余额算:上游会在下一次扣费的事务里先跑一次
// maybeResetUserSubscriptionWithPlanTx 把 amount_used 清零。不这么算的话,
// 一张刚跨过重置点的套餐在页面上显示"已用尽",而它恰恰是下一笔会被扣的那张。
func remainingOf(sub *model.UserSubscription, pendingReset bool) int64 {
	if sub.AmountTotal <= 0 {
		return 0 // 不限额,Remaining 无意义(由 Unlimited 表达)
	}
	used := sub.AmountUsed
	if pendingReset {
		used = 0
	}
	remain := sub.AmountTotal - used
	if remain < 0 {
		remain = 0
	}
	return remain
}

// planFact 是展示一条订阅需要从**套餐**(而不是订阅行)读到的事实。
type planFact struct {
	Title string
	// ResetsQuota 表示这个套餐真的会重置额度。
	//
	// 判据必须与上游 maybeResetUserSubscriptionWithPlanTx 逐条对齐:
	// never 直接 return;custom 但 custom_seconds<=0 时 calcNextResetTime 返回 0,
	// 循环一次都不进,同样不重置。订阅行上的 next_reset_time 是**创建时**算出来的,
	// 套餐的重置周期后来被改掉时它不会被回写 —— 只看它就会给出与扣费相反的结论。
	ResetsQuota bool
}

// planFacts 批量取套餐事实。取不到时留零值,不算失败:这一页的行数由订阅决定,
// 查不到套餐不该让整页失败。零值方向也是安全的:ResetsQuota=false 让余额按
// **未重置**显示(偏保守),不会把一张用尽的套餐说成满额。
func planFacts(ctx context.Context, subs []model.UserSubscription) map[int]planFact {
	out := make(map[int]planFact, len(subs))
	if len(subs) == 0 || model.DB == nil {
		return out
	}
	ids := make([]int, 0, len(subs))
	for i := range subs {
		if _, ok := out[subs[i].PlanId]; ok {
			continue
		}
		out[subs[i].PlanId] = planFact{}
		ids = append(ids, subs[i].PlanId)
	}
	plans := make([]model.SubscriptionPlan, 0, len(ids))
	if err := model.DB.WithContext(ctx).Where("id IN ?", ids).Find(&plans).Error; err != nil {
		common.SysError("qianye/planentitlement: 读取套餐信息失败(列表照常下发,额度按未重置显示): " + err.Error())
		return out
	}
	for i := range plans {
		p := &plans[i]
		period := model.NormalizeResetPeriod(p.QuotaResetPeriod)
		out[p.Id] = planFact{
			Title: p.Title,
			ResetsQuota: period != model.SubscriptionResetNever &&
				(period != model.SubscriptionResetCustom || p.QuotaResetCustomSeconds > 0),
		}
	}
	return out
}
