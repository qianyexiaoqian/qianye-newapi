package planentitlement

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/QuantumNous/new-api/model"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// order_test.go —— 「多个套餐按最先到期的先扣」的核实,以及展示顺序与它的一致性。
//
// ═══════════ 为什么这组用例是"核实"而不是"实现" ═══════════
//
// 项目方要的「若用户存在多个套餐,按最先到期的那个套餐余额默认使用」**已经是
// 上游行为**:model.PreConsumeUserSubscription 的候选查询就是
// `Order("end_time asc, id asc")`。本轮不新建任何机制,只做两件事:
//
//	1. 把它钉死,免得哪次上游合并悄悄改掉(改掉之后没有任何报错,只是钱从
//	   另一张套餐里扣,而用户要到某张套餐到期作废时才发现);
//	2. 让**我们自己的**展示接口用同一个顺序 —— 上游那个展示函数按 end_time desc
//	   排,与扣费顺序正好相反。

// getEntitlements 以指定用户身份调一次用户端接口。
func getEntitlements(t *testing.T, userId int, query string) entitlementsResponse {
	t.Helper()
	gin.SetMode(gin.TestMode)
	engine := gin.New()
	engine.GET("/entitlements", func(c *gin.Context) {
		c.Set("id", userId)
		userEntitlements(c)
	})
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/entitlements"+query, nil)
	engine.ServeHTTP(rec, req)
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())

	var envelope struct {
		Success bool                 `json:"success"`
		Data    entitlementsResponse `json:"data"`
	}
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &envelope))
	require.True(t, envelope.Success)
	return envelope.Data
}

// TestPreConsumeChargesEarliestExpiryFirst 是「最先到期优先」的直接核实。
//
// 刻意让**后插入**的那条先到期:只按 id 排序的实现会取到另一条,而两种实现
// 在只有一条订阅时给出完全相同的结果 —— 那正是这类缺陷能长期潜伏的原因。
func TestPreConsumeChargesEarliestExpiryFirst(t *testing.T) {
	mainDB := newMainDB(t)
	seedPlan(t, mainDB, 1, "套餐A")
	// id 大的先到期。
	late := seedSubscription(t, mainDB, 11, 7, 1, 86400*30, 1000, 0)
	early := seedSubscription(t, mainDB, 12, 7, 1, 86400, 1000, 0)

	got, err := model.PreConsumeUserSubscription("req-1", 7, "gpt-4o", 0, 100, "")
	require.NoError(t, err)
	assert.Equal(t, early.Id, got.UserSubscriptionId,
		"必须扣最先到期的那一张 —— 否则先到期的额度会白白作废")

	var lateRow model.UserSubscription
	require.NoError(t, mainDB.Where("id = ?", late.Id).First(&lateRow).Error)
	assert.Zero(t, lateRow.AmountUsed, "晚到期的那张不该被动")
}

// TestPreConsumeSkipsExhaustedAndKeepsOrder 守顺序在"跳过"之后仍然成立。
//
// 最先到期的那张余额不够时上游会顺延到下一张(continue),而不是直接失败。
// 这条与上一条一起,才把"顺序"这件事量完整:只测第一条的话,一个
// "永远取第一个候选"的错误实现照样全绿。
func TestPreConsumeSkipsExhaustedAndKeepsOrder(t *testing.T) {
	mainDB := newMainDB(t)
	seedPlan(t, mainDB, 1, "套餐A")
	// 最先到期的那张只剩 10,不够本次的 100。
	seedSubscription(t, mainDB, 11, 7, 1, 86400, 1000, 990)
	mid := seedSubscription(t, mainDB, 12, 7, 1, 86400*10, 1000, 0)
	seedSubscription(t, mainDB, 13, 7, 1, 86400*30, 1000, 0)

	got, err := model.PreConsumeUserSubscription("req-2", 7, "gpt-4o", 0, 100, "")
	require.NoError(t, err)
	assert.Equal(t, mid.Id, got.UserSubscriptionId,
		"余额不足时顺延到**下一个最早到期**的,而不是跳到最后一个或直接失败")
}

// TestUserViewOrderMatchesConsumeOrder 是这一组的核心:
// **我们展示的第一张,就是上游真的会扣的那一张。**
//
// 断言方式刻意不是"和一份手写的期望顺序比",而是拿同一批数据真的跑一次上游扣费,
// 再比对我们接口标出来的 will_charge_first。手写期望会自己漂移,而漂移之后它
// 仍然与自己一致,测试照常全绿。
//
// 顺带把上游那个展示函数也量一遍:它按 end_time desc 排,与扣费顺序**相反** ——
// 这正是我们必须自己开一个接口的全部理由。哪天上游把它改成 asc,这条会红,
// 那时应该重新评估还要不要维护这个接口,而不是让两份顺序在无人察觉中各走各的。
func TestUserViewOrderMatchesConsumeOrder(t *testing.T) {
	newExtDB(t)
	mainDB := newMainDB(t)
	seedPlan(t, mainDB, 1, "套餐A")
	seedPlan(t, mainDB, 2, "套餐B")
	seedSubscription(t, mainDB, 11, 7, 1, 86400*30, 1000, 0)
	early := seedSubscription(t, mainDB, 12, 7, 2, 86400, 1000, 0)
	require.NoError(t, reload())

	view := getEntitlements(t, 7, "")
	require.Len(t, view.Subscriptions, 2)
	assert.Equal(t, early.Id, view.Subscriptions[0].UserSubscriptionId,
		"列表必须按扣费顺序(最先到期在前)排")
	assert.True(t, view.Subscriptions[0].WillChargeFirst)
	assert.False(t, view.Subscriptions[1].WillChargeFirst)

	got, err := model.PreConsumeUserSubscription("req-3", 7, "gpt-4o", 0, 100, "")
	require.NoError(t, err)
	assert.Equal(t, view.Subscriptions[0].UserSubscriptionId, got.UserSubscriptionId,
		"标着「本次将优先扣除」的那一张,必须就是上游真的扣的那一张")

	upstream, err := model.GetAllActiveUserSubscriptions(7)
	require.NoError(t, err)
	require.Len(t, upstream, 2)
	assert.NotEqual(t, view.Subscriptions[0].UserSubscriptionId, upstream[0].Subscription.Id,
		"上游展示函数按 end_time desc 排,与扣费顺序相反 —— 这条一旦不成立,"+
			"说明上游改了排序,应当重新评估本模块的展示接口是否还有必要")
}

// TestRestrictedPlanIsSkippedNotShownAsInsufficient 守「用不了」与「余额不足」
// 在展示上是两件事。
//
// 用户的投诉只有一种形状:「我明明有余额,为什么扣了我的主额度」。界面必须能在
// **扣费之前**回答这个问题,否则它一定会变成工单 —— 而且是查不清的那种,
// 因为服务端把"没有可用订阅"与"订阅额度不足"映射到了同一个错误码。
func TestRestrictedPlanIsSkippedNotShownAsInsufficient(t *testing.T) {
	gdb := newExtDB(t)
	enforceBalanceScope(t)
	mainDB := newMainDB(t)
	setGroupRatios(t, `{"default":1,"pro":0.8,"lab":0.1}`)
	seedPlan(t, mainDB, 1, "仅限套餐")
	seedPlan(t, mainDB, 2, "通用套餐")
	putGrant(t, gdb, 1, "pro")
	putPolicy(t, gdb, 1, ScopeRestricted)
	require.NoError(t, reload())

	restricted := seedSubscription(t, mainDB, 11, 7, 1, 86400, 1000, 0)
	universal := seedSubscription(t, mainDB, 12, 7, 2, 86400*30, 1000, 0)

	// 请求落在 pro 上:仅限套餐可用,而且它最先到期 → 它优先。
	inScope := getEntitlements(t, 7, "?model_group=pro")
	require.Len(t, inScope.Subscriptions, 2)
	assert.Equal(t, restricted.Id, inScope.Subscriptions[0].UserSubscriptionId)
	assert.True(t, inScope.Subscriptions[0].UsableHere)
	assert.True(t, inScope.Subscriptions[0].WillChargeFirst)

	// 请求落在 lab 上:仅限套餐**用不了**(不是余额不足),优先权让给通用套餐。
	outOfScope := getEntitlements(t, 7, "?model_group=lab")
	require.Len(t, outOfScope.Subscriptions, 2)
	assert.False(t, outOfScope.Subscriptions[0].UsableHere,
		"余额还在,只是不能用于这个分组 —— 这与「余额不足」是两种状态")
	assert.Positive(t, outOfScope.Subscriptions[0].Remaining,
		"「用不了」的那张必须仍然显示它真实的剩余额度,否则用户会以为钱没了")
	assert.False(t, outOfScope.Subscriptions[0].WillChargeFirst)
	assert.Equal(t, universal.Id, outOfScope.Subscriptions[1].UserSubscriptionId)
	assert.True(t, outOfScope.Subscriptions[1].WillChargeFirst)
	assert.True(t, outOfScope.AnyRestricted)
}

// TestWalletFallbackBlockedOnlyCountsEligiblePlans 钉死钱包回退的判定主体。
//
// 规则一句话:allow_wallet_overflow 的判定主体**只包含本次请求真正有资格出资的
// 那批候选**,被范围挡掉的套餐没有发言权。
//
// 反过来的话,一张仅限 G 的套餐就能禁止用户用钱包去买 H —— 用户没有任何操作
// 可以解除这个阻断:他既用不了套餐余额(范围不符),也用不了主额度(被同一张
// 套餐挡住),账户变成活死人。那会变成比原投诉更难解释的投诉。
func TestWalletFallbackBlockedOnlyCountsEligiblePlans(t *testing.T) {
	gdb := newExtDB(t)
	enforceBalanceScope(t)
	mainDB := newMainDB(t)
	setGroupRatios(t, `{"default":1,"pro":0.8,"lab":0.1}`)
	seedPlan(t, mainDB, 1, "仅限且不许用钱包")
	putGrant(t, gdb, 1, "pro")
	putPolicy(t, gdb, 1, ScopeRestricted)
	require.NoError(t, reload())

	sub := seedSubscription(t, mainDB, 11, 7, 1, 86400, 1000, 0)
	sub.AllowWalletOverflow = false
	require.NoError(t, mainDB.Save(sub).Error)

	assert.True(t, getEntitlements(t, 7, "?model_group=pro").WalletFallbackBlocked,
		"在它自己的范围内,这张套餐的「不许用钱包」当然生效")
	assert.False(t, getEntitlements(t, 7, "?model_group=lab").WalletFallbackBlocked,
		"范围之外它没有发言权 —— 否则用户在这个分组上既花不掉套餐余额、又用不了钱包")
}

// TestPendingResetShowsRestoredBalance 守展示与扣费在**周期重置**上的一致性。
//
// 上游在扣费事务里会先跑一次 maybeResetUserSubscriptionWithPlanTx 把 amount_used
// 清零(定时任务只在 master 节点跑,没有 master 的部署里重置**只**发生在请求路径上)。
// 展示不算这一步的话,一张刚跨过重置点的套餐会显示成"已用尽",而它恰恰是下一笔
// 会被扣的那张 —— 显示与扣费给出相反的答案。
func TestPendingResetShowsRestoredBalance(t *testing.T) {
	newExtDB(t)
	mainDB := newMainDB(t)
	seedPlanWithReset(t, mainDB, 1, "月度重置套餐", model.SubscriptionResetMonthly, 0)
	sub := seedSubscription(t, mainDB, 11, 7, 1, 86400*30, 1000, 1000)
	sub.NextResetTime = sub.StartTime + 1 // 早就该重置了
	require.NoError(t, mainDB.Save(sub).Error)
	require.NoError(t, reload())

	view := getEntitlements(t, 7, "")
	require.Len(t, view.Subscriptions, 1)
	assert.True(t, view.Subscriptions[0].PendingReset)
	assert.Equal(t, int64(1000), view.Subscriptions[0].Remaining,
		"重置时间已到时必须按重置后的余额显示,否则页面说「已用尽」而下一笔就从这里扣")
	assert.True(t, view.Subscriptions[0].WillChargeFirst)
}

// TestPendingResetRequiresThePlanToActuallyReset 是上一条的反面,守的是同一句话的
// 另一半:**订阅行上的 next_reset_time 到点了,不代表这个套餐会重置。**
//
// 套餐的重置周期可以在订阅创建之后被管理员改掉(改的时候不回写已有订阅的
// next_reset_time)。只看 next_reset_time 的实现会把一张已经用尽的订阅显示成满额,
// 并标上「本次将优先扣除」;而上游 maybeResetUserSubscriptionWithPlanTx 在
// never / custom_seconds<=0 这两档直接 return,amount_used 一分不减,
// PreConsumeUserSubscription 因余额不足跳过它,钱从钱包出。
// 用户看到的是「套餐还有 1000、本次将优先扣除」,账单却走了钱包。
func TestPendingResetRequiresThePlanToActuallyReset(t *testing.T) {
	newExtDB(t)
	mainDB := newMainDB(t)
	seedPlanWithReset(t, mainDB, 1, "已被改成不重置", model.SubscriptionResetNever, 0)
	seedPlanWithReset(t, mainDB, 2, "custom 但周期是 0", model.SubscriptionResetCustom, 0)
	for _, id := range []int{11, 12} {
		sub := seedSubscription(t, mainDB, id, 7, id-10, 86400*30, 1000, 1000)
		sub.NextResetTime = sub.StartTime + 1 // 订阅行上早就该重置了
		require.NoError(t, mainDB.Save(sub).Error)
	}
	require.NoError(t, reload())

	view := getEntitlements(t, 7, "")
	require.Len(t, view.Subscriptions, 2)
	for _, got := range view.Subscriptions {
		assert.Falsef(t, got.PendingReset,
			"套餐 %d 根本不会重置,next_reset_time 到点了也不该标成待重置", got.PlanId)
		assert.Zerof(t, got.Remaining,
			"套餐 %d 的额度已经用尽且不会重置,余额必须显示 0 —— "+
				"显示成满额会让用户以为下一笔从这里扣,实际走的是钱包", got.PlanId)
		assert.Falsef(t, got.WillChargeFirst,
			"套餐 %d 一分余额都没有,不该被标成「本次将优先扣除」", got.PlanId)
	}

	// 与扣费路径对账:上游确实取不到这两张,整次扣费失败(而不是从它们身上扣)。
	_, err := model.PreConsumeUserSubscription("req-no-reset", 7, "gpt-4o", 0, 100, "")
	require.Error(t, err, "两张套餐都用尽且不会重置,订阅侧必须扣不到 —— 展示与扣费必须同结论")
}
