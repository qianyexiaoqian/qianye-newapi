package middleware

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/QuantumNous/new-api/common"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// critical_rate_limit_scope_test.go —— 关键接口的限流桶必须按路由分开。
//
// 缺陷形态:CriticalRateLimit() 对**所有**关键接口用同一个字面量 mark "CT",
// 桶的 key 只有 mark + ClientIP。于是充值/兑换码、划转、提现、登录/注册、匿名的
// GET /api/ratio_config、以及前端每 15 分钟一次的 auth/refresh 全部挤在同一个
// 20 次/20 分钟的 IP 计数器里:同一出口 IP(公司 NAT、机场、基站)下任何一个人
// 刷任意一个关键接口,就能让其余所有人 20 分钟内提不了现、充不了值、登不上;
// NAT 后十几个活跃标签页的正常续期就足以自己把桶吃光,不需要攻击者。
//
// 按路由分桶不削弱防护:登录爆破看的仍是登录那条路由自己的 20 次。

func newCriticalRateLimitEngine(t *testing.T, limit int) *gin.Engine {
	t.Helper()
	gin.SetMode(gin.TestMode)

	prevEnable, prevNum, prevDuration, prevRedis := common.CriticalRateLimitEnable, common.CriticalRateLimitNum, common.CriticalRateLimitDuration, common.RedisEnabled
	common.CriticalRateLimitEnable = true
	common.CriticalRateLimitNum = limit
	common.CriticalRateLimitDuration = 1200
	common.RedisEnabled = false
	t.Cleanup(func() {
		common.CriticalRateLimitEnable, common.CriticalRateLimitNum, common.CriticalRateLimitDuration, common.RedisEnabled = prevEnable, prevNum, prevDuration, prevRedis
	})

	router := gin.New()
	handler := func(c *gin.Context) { c.Status(http.StatusOK) }
	router.POST("/api/user/topup", CriticalRateLimit(), handler)
	router.POST("/api/qy/withdraw", CriticalRateLimit(), handler)
	router.POST("/api/user/login", CriticalRateLimit(), handler)
	return router
}

func hitRoute(t *testing.T, router *gin.Engine, path, ip string) int {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, path, nil)
	req.RemoteAddr = ip + ":12345"
	recorder := httptest.NewRecorder()
	router.ServeHTTP(recorder, req)
	return recorder.Code
}

func TestCriticalRateLimitBucketsAreScopedPerRoute(t *testing.T) {
	const limit = 3
	router := newCriticalRateLimitEngine(t, limit)
	const ip = "198.51.100.7"

	// 把充值那条路由的桶打空。
	for i := 0; i < limit; i++ {
		require.Equal(t, http.StatusOK, hitRoute(t, router, "/api/user/topup", ip), "第 %d 次充值应当放行", i+1)
	}
	assert.Equal(t, http.StatusTooManyRequests, hitRoute(t, router, "/api/user/topup", ip),
		"超出配额的同一条路由必须被拦")

	// 同一个 IP 的提现与登录不该受牵连 —— 这正是缺陷造成的共谋面。
	assert.Equal(t, http.StatusOK, hitRoute(t, router, "/api/qy/withdraw", ip),
		"刷充值不该让同一出口 IP 下的人提不了现")
	assert.Equal(t, http.StatusOK, hitRoute(t, router, "/api/user/login", ip),
		"刷充值不该让同一出口 IP 下的人登不上")
}

func TestCriticalRateLimitStillCapsASingleRoutePerIP(t *testing.T) {
	const limit = 2
	router := newCriticalRateLimitEngine(t, limit)

	// 按路由分桶不能变成"不限流":同一条路由、同一个 IP 仍然只有 limit 次。
	for i := 0; i < limit; i++ {
		require.Equal(t, http.StatusOK, hitRoute(t, router, "/api/user/login", "198.51.100.8"))
	}
	assert.Equal(t, http.StatusTooManyRequests, hitRoute(t, router, "/api/user/login", "198.51.100.8"),
		"登录爆破仍要被这条路由自己的配额挡住")

	// 另一个 IP 有自己的桶。
	assert.Equal(t, http.StatusOK, hitRoute(t, router, "/api/user/login", "198.51.100.9"))
}
