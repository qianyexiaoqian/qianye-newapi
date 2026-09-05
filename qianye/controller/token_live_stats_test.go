package controller

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/middleware"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// token_live_stats_test.go —— 密钥页「当前并发 / 近 1 分钟」那条端点。
//
// 窗口算术、回收、时钟回拨由 middleware/qy_token_live_export_test.go 守。
// 这里守的是这条端点自己负责的两件事:**下发的形状**(前端逐字依赖它)
// 与**取谁的数**(它是一条批量读接口,漏了归属就是横向越权)。

func callTokenLiveStats(t *testing.T, userId int) (int, map[string]any) {
	t.Helper()
	gin.SetMode(gin.TestMode)
	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	c.Request = httptest.NewRequest(http.MethodGet, "/api/qy/token-usage/live", nil)
	if userId != 0 {
		c.Set("id", userId)
	}
	UserTokenLiveStats(c)

	var body struct {
		Success bool           `json:"success"`
		Data    map[string]any `json:"data"`
	}
	require.NoError(t, common.Unmarshal(rec.Body.Bytes(), &body), rec.Body.String())
	return rec.Code, body.Data
}

// recordOneRelayRequest 让一次请求真的走一遍中间件,而不是往计数器里塞假数据。
// 这条端点与中间件之间那个"进程内单例"的接缝,正是它唯一可能接错的地方。
func recordOneRelayRequest(t *testing.T, tokenId, userId int) {
	t.Helper()
	gin.SetMode(gin.TestMode)
	engine := gin.New()
	engine.Use(func(c *gin.Context) {
		c.Set("token_id", tokenId)
		c.Set("id", userId)
		c.Next()
	})
	engine.Use(middleware.QyTokenLiveStats())
	engine.GET("/relay", func(c *gin.Context) { c.Status(http.StatusOK) })
	engine.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "/relay", nil))
}

// TestUserTokenLiveStatsShapeMatchesWhatTheColumnReads 钉下发的形状。
//
// 前端按 `data.stats[<令牌 id 的十进制字符串>].{in_flight,requests}` 取数,
// 并用 `data.window_seconds` 渲染"近 N 秒"那句文案。这三个键名任何一个漂了,
// 表现都是那一列**静默显示 0** —— 没有报错、没有红,只是永远不动。
func TestUserTokenLiveStatsShapeMatchesWhatTheColumnReads(t *testing.T) {
	const tokenId, userId = 771_001, 771_002
	recordOneRelayRequest(t, tokenId, userId)

	code, data := callTokenLiveStats(t, userId)
	require.Equal(t, http.StatusOK, code)

	assert.Equal(t, float64(middleware.QyTokenLiveWindowSeconds), data["window_seconds"],
		"窗口长度必须由后端下发,否则界面上那句'近 60 秒'迟早会跟实现对不上")

	stats, ok := data["stats"].(map[string]any)
	require.True(t, ok, "stats 必须是对象: %v", data["stats"])
	row, ok := stats["771001"].(map[string]any)
	require.True(t, ok, "键必须是令牌 id 的十进制字符串: %v", stats)

	assert.Equal(t, float64(0), row["in_flight"], "请求已经结束,在途位必须还回去了")
	assert.Equal(t, float64(1), row["requests"])
}

// TestUserTokenLiveStatsOnlyReturnsTheCallersOwnTokens 守横向越权。
//
// 这条接口不接受前端传令牌 id,归属是**写入时**记下的。会杀掉的改法:
// 改成从 query 收一串 id 再照单返回。
func TestUserTokenLiveStatsOnlyReturnsTheCallersOwnTokens(t *testing.T) {
	const mineToken, meUser = 772_001, 772_002
	const theirsToken, themUser = 772_003, 772_004
	recordOneRelayRequest(t, mineToken, meUser)
	recordOneRelayRequest(t, theirsToken, themUser)

	code, data := callTokenLiveStats(t, meUser)
	require.Equal(t, http.StatusOK, code)

	stats, ok := data["stats"].(map[string]any)
	require.True(t, ok)
	assert.Contains(t, stats, "772001")
	assert.NotContains(t, stats, "772003")
}

// TestUserTokenLiveStatsOmitsIdleTokens 守零值口径。
//
// 完全没在用的令牌**不在** stats 里,由前端渲染 0。与「今日消耗」那一列
// 逐字同一套口径:缺席与 0 在用户眼里是同一件事,而两者都必须与"取不到"
// 长得不一样 —— 后者是 HTTP 层的事。
func TestUserTokenLiveStatsOmitsIdleTokens(t *testing.T) {
	code, data := callTokenLiveStats(t, 773_001)
	require.Equal(t, http.StatusOK, code)

	stats, ok := data["stats"].(map[string]any)
	require.True(t, ok, "没有数的时候也要给一个空对象,不能给 null")
	assert.Empty(t, stats)
}

// TestUserTokenLiveStatsRejectsAnonymous 守鉴权兜底。
// 路由本来就挂在 UserAuth 之后,这一层是防"将来有人把它挪到别的组里"。
func TestUserTokenLiveStatsRejectsAnonymous(t *testing.T) {
	code, _ := callTokenLiveStats(t, 0)
	assert.Equal(t, http.StatusUnauthorized, code)
}
