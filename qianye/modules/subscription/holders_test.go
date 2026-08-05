package subscription

import (
	"net/http"
	"net/http/httptest"
	"strconv"
	"testing"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/model"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// callPlanHolders 打「当前人数」的下钻接口。query 形如 "p=2&page_size=1"。
func callPlanHolders(t *testing.T, planId, query string) *httptest.ResponseRecorder {
	t.Helper()
	gin.SetMode(gin.TestMode)
	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	url := "/api/qy/admin/subscription/plans/" + planId + "/holders"
	if query != "" {
		url += "?" + query
	}
	c.Request = httptest.NewRequest(http.MethodGet, url, nil)
	c.Params = gin.Params{{Key: "plan_id", Value: planId}}
	c.Set("id", 7)
	c.Set("username", "admin7")

	adminPlanHolders(c)
	return rec
}

// holdersBody 是下钻接口的响应形状,前端按同一份字段名解包。
type holdersBody struct {
	Success bool `json:"success"`
	Data    struct {
		PlanId int `json:"plan_id"`
		Items  []struct {
			UserId        int    `json:"user_id"`
			Username      string `json:"username"`
			UserDeleted   bool   `json:"user_deleted"`
			Status        string `json:"status"`
			Subscriptions int64  `json:"subscriptions"`
			StartTime     int64  `json:"start_time"`
			EndTime       int64  `json:"end_time"`
		} `json:"items"`
		Total    int64 `json:"total"`
		Page     int   `json:"p"`
		PageSize int   `json:"page_size"`
	} `json:"data"`
}

func decodeHolders(t *testing.T, rec *httptest.ResponseRecorder) holdersBody {
	t.Helper()
	var body holdersBody
	require.NoError(t, common.Unmarshal(rec.Body.Bytes(), &body), rec.Body.String())
	return body
}

// 下钻列表与「当前人数」那个数字必须是同一个集合 —— 这是本功能的全部意义。
//
// 项目方的原话是「只能看见人数,无法查看具体是哪些用户」。补上列表之后,新的
// 失败形状不是"打不开",而是**两个数字对不上**:列表页那一列说 3 个人,点开
// 只列出 2 行。这类缺陷没有报错、没有日志,而且两侧都言之凿凿,运营无从判断
// 该信哪个。所以这条用例把两侧摆在同一份数据上互证:total、行数、以及列表页
// 那个 used_seats,三者必须完全相等。
//
// 数据刻意铺满全部边界:同一人多条(去重)、别的套餐(不许串)、
// expired/cancelled(不占名额)、以及已到期但没被清扫的 active(同样不占)。
func TestPlanHolders_ListAndCountAreTheSameSet(t *testing.T) {
	ext := newExtDB(t)
	main := newMainDB(t)
	seedPlan(t, main, 1, "限量套餐")
	seedPlan(t, main, 2, "别的套餐")
	seedUser(t, main, 100, "alice")
	seedUser(t, main, 200, "bob")
	seedUser(t, main, 300, "carol")
	seedUser(t, main, 400, "dave")
	seedUser(t, main, 500, "erin")
	seedSubscription(t, main, 100, 1, "active")
	seedSubscription(t, main, 100, 1, "active") // 同一人两条 → 1 行、条数 2
	seedSubscription(t, main, 200, 1, "active")
	seedSubscription(t, main, 300, 1, "expired")   // 不占名额,不许出现在列表里
	seedSubscription(t, main, 400, 1, "cancelled") // 同上
	seedLapsedSubscription(t, main, 500, 1)        // 到期但没被清扫,同样不占
	seedSubscription(t, main, 300, 2, "active")    // 别的套餐,不许串进来
	putSeat(t, ext, 1, 5)

	rec := callPlanHolders(t, "1", "")
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	body := decodeHolders(t, rec)

	require.Len(t, body.Data.Items, 2, "只有 alice 与 bob 正占着名额")
	assert.EqualValues(t, 2, body.Data.Total)
	assert.Equal(t, 1, body.Data.PlanId)

	byUser := make(map[int]int64, len(body.Data.Items))
	for _, it := range body.Data.Items {
		byUser[it.UserId] = it.Subscriptions
		assert.Equal(t, "active", it.Status, "列表按定义只列当前生效的人")
		assert.False(t, it.UserDeleted)
	}
	assert.EqualValues(t, 2, byUser[100], "同一人的两条订阅是一行、条数 2 —— 名额仍只占 1 个")
	assert.EqualValues(t, 1, byUser[200])

	// 与列表页那一列互证:used_seats、total、行数三者必须同时相等。
	// 差一个就意味着页面上的数字与点开看到的人对不上。
	usage := decodePlansUsage(t, callPlansUsage(t))
	var usedSeats int64
	for _, p := range usage.Data.Plans {
		if p.PlanId == 1 {
			usedSeats = p.UsedSeats
		}
	}
	assert.EqualValues(t, usedSeats, body.Data.Total,
		"列表页显示的人数与下钻的 total 必须是同一个数")
	assert.EqualValues(t, usedSeats, int64(len(body.Data.Items)),
		"点开之后列出的行数必须与那个数字相等,否则运营无从判断该信哪个")
}

// 用户名是从主库另查一次贴上去的,而不是 JOIN —— 已被删除的用户不许让整行消失。
//
// 上游删除用户是软删除,而且**不动 user_subscriptions**:一条 active 且未到期的
// 订阅会继续占着名额,它的主人却在用户管理里查不到。用 JOIN 写成一条 SQL 的话
// 这一行会直接从列表里消失,于是"当前人数 2"点开只有 1 行 —— 而缺的那一个
// 恰恰是最该被看见的异常:名额被一个已删除账号永久占着。
func TestPlanHolders_DeletedUserStillOccupiesASeatAndStaysVisible(t *testing.T) {
	newExtDB(t)
	main := newMainDB(t)
	seedPlan(t, main, 1, "限量套餐")
	seedUser(t, main, 100, "alice")
	seedUser(t, main, 200, "ghost")
	seedSubscription(t, main, 100, 1, "active")
	seedSubscription(t, main, 200, 1, "active")
	require.NoError(t, main.Delete(&model.User{}, 200).Error, "软删除,users 行还在但带 deleted_at")

	body := decodeHolders(t, callPlanHolders(t, "1", ""))

	require.Len(t, body.Data.Items, 2, "被删除的用户仍然占着名额,不许从列表里消失")
	assert.EqualValues(t, 2, body.Data.Total)
	got := make(map[int]bool, 2)
	for _, it := range body.Data.Items {
		got[it.UserId] = it.UserDeleted
		if it.UserId == 200 {
			assert.Equal(t, "ghost", it.Username, "软删除的用户名仍要显示,否则这一行无法追查")
		}
	}
	assert.False(t, got[100])
	assert.True(t, got[200], "必须标出「这个人已被删除」,否则运营只会觉得人数对不上")
}

// 分页:每一页都不重不漏,合起来正好是 total。
//
// 排序键必须唯一,否则同一秒到期的多个人在两页之间的相对顺序由数据库自由决定,
// 翻页时会出现"有人重复出现、有人从没出现过"。这里三个人的 end_time 全部相同,
// 正是那个条件 —— 只按 end_time 排的实现在这条用例上会漏人。
func TestPlanHolders_PagesCoverEveryHolderExactlyOnce(t *testing.T) {
	newExtDB(t)
	main := newMainDB(t)
	seedPlan(t, main, 1, "限量套餐")
	for _, u := range []struct {
		id   int
		name string
	}{{100, "alice"}, {200, "bob"}, {300, "carol"}} {
		seedUser(t, main, u.id, u.name)
		seedSubscription(t, main, u.id, 1, "active") // end_time 全部相同
	}

	seen := make(map[int]int, 3)
	var total int64
	for page := 1; page <= 3; page++ {
		body := decodeHolders(t, callPlanHolders(t, "1", "p="+strconv.Itoa(page)+"&page_size=1"))
		require.Len(t, body.Data.Items, 1, "第 %d 页应当正好一行", page)
		seen[body.Data.Items[0].UserId]++
		total = body.Data.Total
		assert.Equal(t, page, body.Data.Page)
		assert.Equal(t, 1, body.Data.PageSize)
	}

	assert.EqualValues(t, 3, total, "total 是全量人数,不随页长变化")
	assert.Equal(t, map[int]int{100: 1, 200: 1, 300: 1}, seen,
		"三页合起来必须不重不漏地覆盖全部占用者")

	// 越界页返回空列表而不是报错,且 total 照常回 —— 前端据此显示"没有更多"。
	over := decodeHolders(t, callPlanHolders(t, "1", "p=9&page_size=1"))
	assert.Empty(t, over.Data.Items)
	assert.EqualValues(t, 3, over.Data.Total)
}

// 一个人都没有时必须回 `"items":[]` 而不是 `"items":null`。
//
// nil 切片序列化成 JSON null,前端 `items.map(...)` 直接崩,整页白屏 ——
// 判据与触发条件见 qianye/json_array_guard_test.go(那条 AST 锁看不见跨函数
// 返回的切片,所以这里用空库行为把它守住)。
func TestPlanHolders_EmptyPlanReturnsAnEmptyArrayNotNull(t *testing.T) {
	newExtDB(t)
	main := newMainDB(t)
	seedPlan(t, main, 1, "没人买的套餐")

	rec := callPlanHolders(t, "1", "")

	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	assert.Contains(t, rec.Body.String(), `"items":[]`,
		"nil 切片会序列化成 null,前端 items.map 直接崩成白屏")
	assert.Contains(t, rec.Body.String(), `"total":0`)
}

func TestPlanHolders_RejectsUnknownPlan(t *testing.T) {
	newExtDB(t)
	newMainDB(t)

	rec := callPlanHolders(t, "404", "")

	assert.Equal(t, http.StatusBadRequest, rec.Code)
	assert.Contains(t, rec.Body.String(), "qy_subscription_plan_not_found")
}

// 扩展库不可用时必须在做任何事之前停住,与同组另外四个管理端接口同一档。
func TestPlanHolders_StopsWhenExtensionDBIsUnavailable(t *testing.T) {
	newExtDB(t)
	main := newMainDB(t)
	seedPlan(t, main, 1, "限量套餐")

	qyDBHealthy.Store(false)
	t.Cleanup(func() { qyDBHealthy.Store(true) })

	assert.Equal(t, http.StatusServiceUnavailable, callPlanHolders(t, "1", "").Code)
}
