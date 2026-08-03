package subscription

import (
	"bytes"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/model"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

// callDelete 打一次真实的删除 handler。
//
// 走 handler 而不是直接调事务体:必填事由、force 解析、409 与 200 的分流、
// 审计埋点全都住在 handler 里。校验逻辑写得再对,只要 handler 忘了调它,
// 缺事由的删除照样能落地 —— 本仓反复出现的正是这种"纯函数对了、调度层没接上"。
func callDelete(t *testing.T, planId, body string) *httptest.ResponseRecorder {
	t.Helper()
	gin.SetMode(gin.TestMode)
	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	c.Request = httptest.NewRequest(http.MethodPost,
		"/api/qy/admin/subscription/plans/"+planId+"/delete", bytes.NewBufferString(body))
	c.Request.Header.Set("Content-Type", "application/json")
	c.Params = gin.Params{{Key: "plan_id", Value: planId}}
	c.Set("id", 7)
	c.Set("username", "admin7")

	adminDeletePlan(c)
	return rec
}

func planExists(t *testing.T, gdb *gorm.DB, planId int) bool {
	t.Helper()
	var n int64
	require.NoError(t, gdb.Model(&model.SubscriptionPlan{}).Where("id = ?", planId).Count(&n).Error)
	return n > 0
}

func subStatuses(t *testing.T, gdb *gorm.DB, planId int) []string {
	t.Helper()
	var out []string
	require.NoError(t, gdb.Model(&model.UserSubscription{}).
		Where("plan_id = ?", planId).Order("id asc").Pluck("status", &out).Error)
	return out
}

func orderStatuses(t *testing.T, gdb *gorm.DB, planId int) []string {
	t.Helper()
	var out []string
	require.NoError(t, gdb.Model(&model.SubscriptionOrder{}).
		Where("plan_id = ?", planId).Order("id asc").Pluck("status", &out).Error)
	return out
}

// 强制删除必须填事由:级联作废别人已付款的订阅,事后复盘的第一个问题就是"为什么"。
func TestDeletePlan_RequiresReasonOnlyWhenForced(t *testing.T) {
	t.Run("force 时空事由被拒", func(t *testing.T) {
		newExtDB(t)
		main := newMainDB(t)
		seedPlan(t, main, 1, "月度套餐")

		rec := callDelete(t, "1", `{"force":true,"reason":"   "}`)

		assert.Equal(t, http.StatusBadRequest, rec.Code)
		assert.Contains(t, rec.Body.String(), "qy_subscription_delete_reason_required")
		assert.True(t, planExists(t, main, 1), "校验没过时套餐必须原封不动")
	})

	// 默认路径不强制。这一档后端已经确认过"没有任何人在用",最常见的场景是删掉
	// 一个刚建错的空套餐 —— 前端在无占用时不会弹事由框,后端若在这里也强制,
	// 这条最常见的删除路径会 100% 走不通,而界面上没有任何地方能填。
	t.Run("默认路径允许空事由", func(t *testing.T) {
		newExtDB(t)
		main := newMainDB(t)
		seedPlan(t, main, 1, "刚建错的套餐")

		rec := callDelete(t, "1", `{}`)

		require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
		assert.False(t, planExists(t, main, 1))
	})
}

// 默认路径:有活跃订阅就拒绝,并把具体数字告诉管理员。
//
// 数字是这条拒绝的全部价值 —— 只回一句"还在使用中"的话,管理员既不知道要处理
// 多少人,也无从判断该不该强制删。
func TestDeletePlan_RefusesWhenActiveSubscriptionsExist(t *testing.T) {
	ext := newExtDB(t)
	main := newMainDB(t)
	seedPlan(t, main, 1, "月度套餐")
	seedSubscription(t, main, 100, 1, "active")
	seedSubscription(t, main, 100, 1, "active") // 同一个人两条,考察去重人数
	seedSubscription(t, main, 200, 1, "active")
	seedSubscription(t, main, 300, 1, "expired") // 不该计入

	rec := callDelete(t, "1", `{"reason":"下架旧套餐"}`)

	assert.Equal(t, http.StatusConflict, rec.Code)
	body := rec.Body.String()
	assert.Contains(t, body, "qy_subscription_plan_in_use")
	assert.Contains(t, body, `"active_subscriptions":3`)
	assert.Contains(t, body, `"active_users":2`, "受影响人数必须去重,与名额口径同源")
	assert.True(t, planExists(t, main, 1))
	assert.Equal(t, []string{"active", "active", "active", "expired"}, subStatuses(t, main, 1),
		"被拒绝的删除不许留下任何副作用")
	assert.Equal(t, []string{"subscription.plan.delete:fail"}, auditActions(t, ext),
		"被拒绝的删除同样要留痕:反复尝试删一个在用套餐正是最该查的东西")
}

// 默认路径:待处理订单同样构成拒绝理由。
//
// 它比活跃订阅更危险 —— pending 意味着钱可能已经在路上,套餐一旦没了,
// 回调会在写 success 之前就 return err,订单永久卡死。
func TestDeletePlan_RefusesWhenPendingOrdersExist(t *testing.T) {
	newExtDB(t)
	main := newMainDB(t)
	seedPlan(t, main, 1, "月度套餐")
	seedOrder(t, main, 100, 1, "TRADE-PENDING", common.TopUpStatusPending)
	seedOrder(t, main, 101, 1, "TRADE-DONE", common.TopUpStatusSuccess)

	rec := callDelete(t, "1", `{"reason":"下架旧套餐"}`)

	assert.Equal(t, http.StatusConflict, rec.Code)
	assert.Contains(t, rec.Body.String(), `"pending_orders":1`)
	assert.True(t, planExists(t, main, 1))
}

// 没有任何在用记录时,默认路径可以直接删,不需要 force。
func TestDeletePlan_AllowsWhenNothingIsInUse(t *testing.T) {
	ext := newExtDB(t)
	main := newMainDB(t)
	seedPlan(t, main, 1, "月度套餐")
	seedSubscription(t, main, 100, 1, "expired")
	seedOrder(t, main, 100, 1, "TRADE-DONE", common.TopUpStatusSuccess)
	putSeat(t, ext, 1, 50)

	rec := callDelete(t, "1", `{"reason":"下架旧套餐"}`)

	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	assert.False(t, planExists(t, main, 1))
	assert.Equal(t, []string{"expired"}, subStatuses(t, main, 1), "已过期的订阅不该被改写")

	var seatRows int64
	require.NoError(t, ext.Model(&PlanSeat{}).Where("plan_id = ?", 1).Count(&seatRows).Error)
	assert.EqualValues(t, 0, seatRows, "扩展库的名额配置必须一并清掉,否则留孤儿")
	assert.Equal(t, []string{"subscription.plan.delete:ok"}, auditActions(t, ext))
}

// force 路径:级联失效 + 删除,并且必须真的把两个「严重后果」堵住。
//
// 断言刻意落在**状态值**上而不是"接口返回 200":
//   - active → cancelled 之后,PreConsumeUserSubscription 那条
//     `WHERE status = 'active'` 的遍历就再也扫不到它们,用户其余套餐的预扣费
//     不会被一个查不到的套餐带崩;
//   - pending → failed 之后,CompleteSubscriptionOrder 命中的是
//     `order.Status != pending` 这个明确终态,而不是在查套餐时 return err 卡死。
//
// 换句话说,这两个状态值就是防护本身,不是实现细节。
func TestDeletePlan_ForceCascadesAndDeletes(t *testing.T) {
	ext := newExtDB(t)
	main := newMainDB(t)
	seedPlan(t, main, 1, "月度套餐")
	seedPlan(t, main, 2, "年度套餐")
	seedSubscription(t, main, 100, 1, "active")
	seedSubscription(t, main, 200, 1, "active")
	seedSubscription(t, main, 300, 1, "expired")
	seedSubscription(t, main, 100, 2, "active") // 别的套餐,不许被殃及
	seedOrder(t, main, 100, 1, "TRADE-PENDING", common.TopUpStatusPending)
	seedOrder(t, main, 101, 1, "TRADE-DONE", common.TopUpStatusSuccess)
	seedOrder(t, main, 102, 2, "TRADE-OTHER", common.TopUpStatusPending)
	putSeat(t, ext, 1, 50)
	putSeat(t, ext, 2, 60)

	rec := callDelete(t, "1", `{"force":true,"reason":"活动结束,强制下架"}`)

	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	assert.False(t, planExists(t, main, 1))
	assert.Equal(t, []string{"cancelled", "cancelled", "expired"}, subStatuses(t, main, 1))
	assert.Equal(t, []string{common.TopUpStatusFailed, common.TopUpStatusSuccess},
		orderStatuses(t, main, 1))

	// 响应里的两个数字是管理员判断"要人工退多少钱"的唯一依据,必须是实际影响行数。
	body := rec.Body.String()
	assert.Contains(t, body, `"cancelled_subscriptions":2`)
	assert.Contains(t, body, `"failed_orders":1`)

	assert.True(t, planExists(t, main, 2), "别的套餐不许被殃及")
	assert.Equal(t, []string{"active"}, subStatuses(t, main, 2))
	assert.Equal(t, []string{common.TopUpStatusPending}, orderStatuses(t, main, 2))

	var seatPlans []int
	require.NoError(t, ext.Model(&PlanSeat{}).Order("plan_id").Pluck("plan_id", &seatPlans).Error)
	assert.Equal(t, []int{2}, seatPlans, "只该清掉被删套餐那一行名额配置")
	assert.Equal(t, []string{"subscription.plan.delete:ok"}, auditActions(t, ext))
}

// 级联之后,被取消的订阅确实不再参与上游的预扣费遍历。
//
// 上一条用例断言的是状态值,这一条把它接到真实行为上:直接调上游的
// PreConsumeUserSubscription,证明"套餐被删 + 订阅已 cancelled"的用户不会再
// 因为查不到套餐而让整个事务失败。这两条合起来才算真的堵住了后果 1。
func TestDeletePlan_ForceKeepsPreConsumeWorkingForOtherPlans(t *testing.T) {
	newExtDB(t)
	main := newMainDB(t)
	seedUser(t, main, 100, "buyer")
	kept := seedPlan(t, main, 2, "年度套餐")
	seedPlan(t, main, 1, "月度套餐")
	seedSubscription(t, main, 100, 1, "active")
	// 留一条别的套餐的订阅,并给足额度,它必须在删除之后继续可用。
	require.NoError(t, main.Create(&model.UserSubscription{
		UserId: 100, PlanId: kept.Id, Status: "active",
		AmountTotal: 1000, StartTime: 1, EndTime: 1 << 40,
	}).Error)

	rec := callDelete(t, "1", `{"force":true,"reason":"活动结束"}`)
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())

	got, err := model.PreConsumeUserSubscription("req-after-delete", 100, "gpt-4", 0, 10)
	require.NoError(t, err,
		"套餐被删之后,该用户其余套餐的预扣费必须照常工作 —— 这正是级联要堵住的后果")
	assert.EqualValues(t, 10, got.PreConsumed)
	assert.Equal(t, kept.Id, subscriptionPlanOf(t, main, got.UserSubscriptionId))
}

// force 级联必须把用户分组降回去,并把 end_time 推到当下。
//
// 只改 status 是这条路径最容易漏、后果最久的缺陷:被删套餐升过组的用户会
// **永久**留在高级分组里 —— 上游的到期扫描只看 status='active',回落目标只从
// status='expired' 的行里找,cancelled 两边都不命中,系统此后没有任何路径能把
// 他们降回来,只能人工进库改数据。而且套餐行已经删了,再也补不回来。
func TestDeletePlan_ForceDowngradesUpgradedUsers(t *testing.T) {
	newExtDB(t)
	main := newMainDB(t)
	seedUser(t, main, 100, "vip-buyer")
	seedPlan(t, main, 1, "VIP 月卡")
	require.NoError(t, main.Model(&model.User{}).Where("id = ?", 100).
		Update("group", "vip").Error)
	require.NoError(t, main.Create(&model.UserSubscription{
		UserId: 100, PlanId: 1, Status: "active",
		UpgradeGroup: "vip", PrevUserGroup: "default",
		StartTime: 1, EndTime: 1 << 40,
	}).Error)

	rec := callDelete(t, "1", `{"force":true,"reason":"活动结束,强制下架"}`)
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())

	var user model.User
	require.NoError(t, main.Where("id = ?", 100).First(&user).Error)
	assert.Equal(t, "default", user.Group,
		"升过组的用户必须降回去,否则他会永久白嫖 vip 的价格与模型权限")

	var sub model.UserSubscription
	require.NoError(t, main.Where("plan_id = ?", 1).First(&sub).Error)
	assert.Equal(t, "cancelled", sub.Status)
	assert.Less(t, sub.EndTime, int64(1<<40),
		"end_time 必须推到当下,否则库里这条被作废的订阅仍显示未到期")
}

// 删除与"正在进行的购买"之间那道竞态窗口:提交之后再扫一遍,把漏进来的行收掉。
//
// 用例直接构造窗口的**结果**(套餐已不在、却还有一条 active 订阅指向它),
// 因为真正的时序无法在单进程里稳定复现。这个结果就是 FACTS 里的"后果 1":
// 该用户此后每一次模型调用的预扣费都会整事务失败,连他别的套餐一起用不了。
// 重试一次删除必须能把它收干净 —— 那是管理员唯一会做的动作。
func TestDeletePlan_SweepsSubscriptionsThatRacedThePlanDeletion(t *testing.T) {
	ext := newExtDB(t)
	main := newMainDB(t)
	// 主库里没有套餐 1:上一次删除已经提交,而一笔在途购买在那之后才 INSERT。
	seedSubscription(t, main, 100, 1, "active")
	seedOrder(t, main, 100, 1, "TRADE-RACED", common.TopUpStatusPending)
	putSeat(t, ext, 1, 50)

	rec := callDelete(t, "1", `{"reason":"重试上次未完成的删除"}`)

	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	assert.Equal(t, []string{"cancelled"}, subStatuses(t, main, 1),
		"竞态写进来的订阅必须被收掉,否则这个用户此后全部套餐的预扣费一起失败")
	assert.Equal(t, []string{common.TopUpStatusFailed}, orderStatuses(t, main, 1))
	assert.Contains(t, rec.Body.String(), `"cancelled_subscriptions":1`)
}

func subscriptionPlanOf(t *testing.T, gdb *gorm.DB, subId int) int {
	t.Helper()
	var sub model.UserSubscription
	require.NoError(t, gdb.Where("id = ?", subId).First(&sub).Error)
	return sub.PlanId
}

// 幂等:套餐已经不在了,重试删除仍然成功,并顺手清掉扩展库的孤儿名额行。
//
// 这是"先主库、后扩展库"这条顺序在退化时的自愈路径。若这里返回 404,
// 管理员看到的是一个错误,他就不会再点第二次,孤儿行会永远留着。
func TestDeletePlan_IsIdempotentAndPurgesOrphanSeatRow(t *testing.T) {
	ext := newExtDB(t)
	newMainDB(t)
	putSeat(t, ext, 1, 50) // 主库里根本没有这个套餐:上一次删除只完成了一半

	rec := callDelete(t, "1", `{"reason":"重试上次未完成的删除"}`)

	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	assert.Contains(t, rec.Body.String(), `"already_gone":true`)

	var seatRows int64
	require.NoError(t, ext.Model(&PlanSeat{}).Count(&seatRows).Error)
	assert.EqualValues(t, 0, seatRows, "重试删除必须能把孤儿名额行清掉")
}
