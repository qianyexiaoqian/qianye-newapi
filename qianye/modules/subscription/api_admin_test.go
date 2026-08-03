package subscription

import (
	"bytes"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/QuantumNous/new-api/common"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func callPlanUsage(t *testing.T, planId string) *httptest.ResponseRecorder {
	t.Helper()
	gin.SetMode(gin.TestMode)
	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	c.Request = httptest.NewRequest(http.MethodGet,
		"/api/qy/admin/subscription/plans/"+planId+"/usage", nil)
	c.Params = gin.Params{{Key: "plan_id", Value: planId}}
	c.Set("id", 7)
	c.Set("username", "admin7")

	adminPlanUsage(c)
	return rec
}

func callPutSeat(t *testing.T, planId, body string) *httptest.ResponseRecorder {
	t.Helper()
	gin.SetMode(gin.TestMode)
	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	c.Request = httptest.NewRequest(http.MethodPut,
		"/api/qy/admin/subscription/plans/"+planId+"/seat-limit", bytes.NewBufferString(body))
	c.Request.Header.Set("Content-Type", "application/json")
	c.Params = gin.Params{{Key: "plan_id", Value: planId}}
	c.Set("id", 7)
	c.Set("username", "admin7")

	adminPutSeat(c)
	return rec
}

// usage 里的 used_seats 必须与闸门用的是同一个口径:去重人数、只数 active;
// 而 active_subscriptions 必须是行数 —— 两个数不许互相顶替。
//
// 口径漂移是这类功能最隐蔽的缺陷:页面显示"还剩 1 个名额",用户点下去却被拒,
// 或者反过来。所以这条用例把两侧摆在同一份数据上互相印证。
func TestPlanUsage_UsesTheSameDistinctActiveCountAsTheGate(t *testing.T) {
	ext := newExtDB(t)
	main := newMainDB(t)
	plan := seedPlan(t, main, 1, "限量套餐")
	seedPlan(t, main, 2, "不限量套餐")
	seedSubscription(t, main, 100, 1, "active")
	seedSubscription(t, main, 100, 1, "active") // 同一人两条 → 1 个名额、2 条订阅
	seedSubscription(t, main, 200, 1, "active")
	seedSubscription(t, main, 300, 1, "expired")   // 不算
	seedSubscription(t, main, 400, 1, "cancelled") // 不算
	seedSubscription(t, main, 100, 2, "active")    // 别的套餐,不许串
	seedOrder(t, main, 100, 1, "TRADE-P", common.TopUpStatusPending)
	seedOrder(t, main, 101, 1, "TRADE-S", common.TopUpStatusSuccess)
	putSeat(t, ext, 1, 3)

	rec := callPlanUsage(t, "1")

	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	body := rec.Body.String()
	assert.Contains(t, body, `"plan_id":1`)
	assert.Contains(t, body, `"capacity":3`)
	assert.Contains(t, body, `"used_seats":2`, "名额按去重人数算:同一人的两条 active 只占一个")
	assert.Contains(t, body, `"active_subscriptions":3`, "影响面按行数算:删除会作废 3 条")
	assert.Contains(t, body, `"pending_orders":1`)

	// 与闸门互相印证:usage 说还剩 1 个(3-2),闸门就必须放行第 3 个人、拒绝第 4 个。
	require.NoError(t, gateSeat(main, plan, 500, "balance", nil))
	seedSubscription(t, main, 500, 1, "active")
	resetCache()
	assert.Error(t, gateSeat(main, plan, 600, "balance", nil),
		"满员之后 usage 与闸门必须同时判满")
}

// 没配过名额的套餐要回 capacity=0(不限),而不是报错或 404。
//
// 这是最常见的一种套餐 —— 绝大多数套餐都不限量。这条路径要是报错,
// 编辑弹窗里的「总名额」格子会对着所有正常套餐显示读取失败。
func TestPlanUsage_ReturnsZeroCapacityWhenNeverConfigured(t *testing.T) {
	newExtDB(t)
	main := newMainDB(t)
	seedPlan(t, main, 1, "普通套餐")

	rec := callPlanUsage(t, "1")

	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	assert.Contains(t, rec.Body.String(), `"capacity":0`)
	assert.Contains(t, rec.Body.String(), `"used_seats":0`)
}

func TestPlanUsage_RejectsUnknownPlan(t *testing.T) {
	newExtDB(t)
	newMainDB(t)

	rec := callPlanUsage(t, "404")

	assert.Equal(t, http.StatusBadRequest, rec.Code)
	assert.Contains(t, rec.Body.String(), "qy_subscription_plan_not_found")
}

// 设置名额:合法值落库、审计留痕,并且**立刻**对闸门生效(不等 30 秒缓存)。
func TestPutSeat_PersistsAndTakesEffectImmediately(t *testing.T) {
	ext := newExtDB(t)
	main := newMainDB(t)
	plan := seedPlan(t, main, 1, "限量套餐")
	seedSubscription(t, main, 100, 1, "active")

	require.NoError(t, gateSeat(main, plan, 200, "balance", nil), "前置条件:没配名额时不限")

	rec := callPutSeat(t, "1", `{"capacity":1}`)

	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	var row PlanSeat
	require.NoError(t, ext.Where("plan_id = ?", 1).First(&row).Error)
	assert.Equal(t, 1, row.Capacity)
	assert.Equal(t, 7, row.UpdatedBy, "必须记下是谁改的:名额直接决定还能不能卖")
	assert.Equal(t, []string{"subscription.plan_seat.update:ok"}, auditActions(t, ext))

	assert.Error(t, gateSeat(main, plan, 200, "balance", nil),
		"保存后必须立刻生效,否则运营会以为没保存上而反复点")
}

// capacity=0 是合法的"取消限量",不是"缺参数"。
//
// 用指针接这个字段的全部理由就在这里:0 与"没传"必须能区分开。
func TestPutSeat_ZeroClearsTheLimit(t *testing.T) {
	ext := newExtDB(t)
	main := newMainDB(t)
	plan := seedPlan(t, main, 1, "限量套餐")
	seedSubscription(t, main, 100, 1, "active")
	putSeat(t, ext, 1, 1)
	require.Error(t, gateSeat(main, plan, 200, "balance", nil), "前置条件:名额为 1 时应当满员")

	rec := callPutSeat(t, "1", `{"capacity":0}`)

	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	assert.NoError(t, gateSeat(main, plan, 200, "balance", nil), "0 必须等价于不限")
}

func TestPutSeat_RejectsInvalidInput(t *testing.T) {
	cases := []struct {
		name   string
		planId string
		body   string
		code   string
		why    string
	}{
		{"缺少 capacity", "1", `{}`, "qy_invalid_param",
			"字段没传与传 0 含义相反,不能当成 0 处理"},
		{"负数", "1", `{"capacity":-1}`, "qy_subscription_seat_invalid", ""},
		{"超过上界", "1", `{"capacity":100000001}`, "qy_subscription_seat_invalid",
			"天文数字在页面上看起来像限量,实际等于不限"},
		{"套餐不存在", "404", `{"capacity":10}`, "qy_subscription_plan_not_found",
			"写给不存在的套餐会留下一行永远读不到的配置,页面上完全看不出来"},
		{"套餐 ID 非法", "0", `{"capacity":10}`, "qy_invalid_param", ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			ext := newExtDB(t)
			main := newMainDB(t)
			seedPlan(t, main, 1, "限量套餐")

			rec := callPutSeat(t, tc.planId, tc.body)

			assert.Equal(t, http.StatusBadRequest, rec.Code, tc.why)
			assert.Contains(t, rec.Body.String(), tc.code)
			var rows int64
			require.NoError(t, ext.Model(&PlanSeat{}).Count(&rows).Error)
			assert.EqualValues(t, 0, rows, "校验没过时一行都不许落库")
		})
	}
}

// 名额配置行是 per-plan 的:改一个不许动到另一个。
func TestPutSeat_IsScopedToOnePlan(t *testing.T) {
	ext := newExtDB(t)
	main := newMainDB(t)
	seedPlan(t, main, 1, "套餐一")
	seedPlan(t, main, 2, "套餐二")
	putSeat(t, ext, 2, 9)

	require.Equal(t, http.StatusOK, callPutSeat(t, "1", `{"capacity":5}`).Code)

	var other PlanSeat
	require.NoError(t, ext.Where("plan_id = ?", 2).First(&other).Error)
	assert.Equal(t, 9, other.Capacity)
	assert.Zero(t, other.UpdatedBy)
	// putSeat 种下去的 updated_at 是 1;被 upsert 误伤的话它会变成当前时间戳。
	assert.EqualValues(t, 1, other.UpdatedAt, "没被改到的行,updated_at 必须保持原样")
}

// 值没变时不写库、不写审计。
//
// 编辑弹窗每次保存都会比较后再调这个接口,少了这条短路,运营每保存一次套餐
// (改个标题、调个价)都会多一条"改了名额"的审计,事后查"谁把名额改小了"
// 得从一堆空改动里人工筛 —— 审计被噪音淹没等同于没有审计。
func TestPutSeat_NoOpWriteIsSkipped(t *testing.T) {
	ext := newExtDB(t)
	main := newMainDB(t)
	seedPlan(t, main, 1, "限量套餐")
	putSeat(t, ext, 1, 5) // updated_by=0, updated_at=1

	rec := callPutSeat(t, "1", `{"capacity":5}`)

	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	assert.Contains(t, rec.Body.String(), `"changed":false`)
	assert.Empty(t, auditActions(t, ext), "没有实际改动就不该留审计")

	var row PlanSeat
	require.NoError(t, ext.Where("plan_id = ?", 1).First(&row).Error)
	assert.EqualValues(t, 1, row.UpdatedAt, "没改动就不该动 updated_at")
	assert.Zero(t, row.UpdatedBy)
}

// 扩展库不可用时,三个管理端接口必须在**做任何事之前**停住。
//
// 这条锁的是 guard.RequireAPI 那一行本身。它没有返回值、没有调用者依赖它,
// 是重构里最容易被"顺手清掉的死代码" —— 而它一旦消失,adminDeletePlan 会照常
// 级联作废用户订阅、删掉套餐,同时 audit.Write 因为扩展库不可用而静默丢弃:
// 一次破坏力最大的操作,零审计记录。
func TestAdminEndpointsStopWhenExtensionDBIsUnavailable(t *testing.T) {
	ext := newExtDB(t)
	main := newMainDB(t)
	seedPlan(t, main, 1, "月度套餐")
	putSeat(t, ext, 1, 5)

	qyDBHealthy.Store(false)
	t.Cleanup(func() { qyDBHealthy.Store(true) })

	assert.Equal(t, http.StatusServiceUnavailable, callPlanUsage(t, "1").Code)
	assert.Equal(t, http.StatusServiceUnavailable, callPutSeat(t, "1", `{"capacity":9}`).Code)
	assert.Equal(t, http.StatusServiceUnavailable,
		callDelete(t, "1", `{"force":true,"reason":"应当在闸门处停住"}`).Code)

	// 闸门后面的副作用一个都不许发生。
	assert.True(t, planExists(t, main, 1), "套餐必须原封不动")
	var row PlanSeat
	require.NoError(t, ext.Where("plan_id = ?", 1).First(&row).Error)
	assert.Equal(t, 5, row.Capacity, "名额必须原封不动")
}

// 一次回源失败之后,负缓存必须挡住下一次回源。
//
// 这不是缓存洁癖:闸门跑在**已经打开的主库事务**内部(订阅创建事务,其中一条是
// 支付回调)。没有负缓存的话,"扩展库可达但慢"这一窄带里每一笔购买都会各自再打
// 一次超时 SELECT,促销洪峰会逐个撞上同一条慢查询,把主库连接池一起吃掉。
//
// 用"回源了就会看到新值"来观测:失败之后往扩展库直接插一行(绕过 putSeat,
// 它会重置缓存),若负缓存失效,第二次调用就会读到 5 并开始拦人。
func TestSeatCacheBacksOffAfterAFailedLoad(t *testing.T) {
	ext := newExtDB(t)
	main := newMainDB(t)
	plan := seedPlan(t, main, 1, "限量套餐")
	seedSubscription(t, main, 100, 1, "active")

	resetCache()
	qyDBHealthy.Store(false) // 回源失败(熔断打开)
	require.NoError(t, gateSeat(main, plan, 200, "balance", nil),
		"前置条件:读不到名额配置时必须放行")

	qyDBHealthy.Store(true)
	require.NoError(t, ext.Exec(
		"INSERT INTO qy_subscription_plan_seats (plan_id, capacity, updated_by, created_at, updated_at) "+
			"VALUES (1, 1, 0, 1, 1)").Error)

	assert.NoError(t, gateSeat(main, plan, 200, "balance", nil),
		"负缓存窗口内不许回源:每笔购买都重试一次慢查询会把主库连接池吃掉")
}
