package middleware

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/QuantumNous/new-api/common"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// redeem_rate_limit_test.go —— 兑换码入口的限流必须同时按 IP 和按账号计数。
//
// 兑换码本身是 122 bit,枚举不动;能被枚举的是**猜测次数**这一侧。而爆破兑换码
// 需要的只是一个能登录的账号加一批出口 IP:
//
//   - 只有 CriticalRateLimit(按 ClientIP)时,换一个代理就换一个桶,同一个账号
//     的尝试次数不受任何约束 —— 代理池的规模直接等于放大倍数;
//   - 只有按账号那一把时,注册一批号同样能线性放大。
//
// 两把一起,攻击者得同时凑齐 N 个 IP 和 N 个账号。下面两条用例各自证明一把,
// 缺哪一把哪一条会红。

func newRedeemRateLimitEngine(t *testing.T, limit int) *gin.Engine {
	t.Helper()
	gin.SetMode(gin.TestMode)

	prevEnable, prevNum := common.CriticalRateLimitEnable, common.CriticalRateLimitNum
	prevDuration, prevRedis := common.CriticalRateLimitDuration, common.RedisEnabled
	common.CriticalRateLimitEnable = true
	common.CriticalRateLimitNum = limit
	common.CriticalRateLimitDuration = 1200
	common.RedisEnabled = false
	t.Cleanup(func() {
		common.CriticalRateLimitEnable, common.CriticalRateLimitNum = prevEnable, prevNum
		common.CriticalRateLimitDuration, common.RedisEnabled = prevDuration, prevRedis
	})

	router := gin.New()
	// 与 router/api-router.go 里 POST /api/user/topup 的挂法一致:
	// 会话鉴权把 id 放进上下文,然后 IP 桶、账号桶各拦一次。
	fakeAuth := func(c *gin.Context) { c.Set("id", parseUserHeader(c)) }
	router.POST("/api/user/topup", fakeAuth, CriticalRateLimit(),
		UserCriticalRateLimit("redeem"), func(c *gin.Context) { c.Status(http.StatusOK) })
	return router
}

func parseUserHeader(c *gin.Context) int {
	id := 0
	_, _ = fmt.Sscanf(c.GetHeader("X-Test-User"), "%d", &id)
	return id
}

func hitRedeem(t *testing.T, router *gin.Engine, ip string, userID int) int {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, "/api/user/topup", nil)
	req.RemoteAddr = ip + ":12345"
	req.Header.Set("X-Test-User", fmt.Sprintf("%d", userID))
	recorder := httptest.NewRecorder()
	router.ServeHTTP(recorder, req)
	return recorder.Code
}

// 一个账号换一批 IP:IP 桶每次都是新的,账号桶必须把它按住。
func TestRedeemRateLimitCapsOneAccountAcrossRotatingIPs(t *testing.T) {
	const limit = 3
	const userID = 90001
	router := newRedeemRateLimitEngine(t, limit)

	for i := 0; i < limit; i++ {
		ip := fmt.Sprintf("203.0.113.%d", i+1)
		require.Equal(t, http.StatusOK, hitRedeem(t, router, ip, userID),
			"第 %d 次尝试来自新 IP,应当放行", i+1)
	}

	// 第 limit+1 次继续换 IP —— IP 那一把完全没被消耗,拦住它的只能是账号那一把。
	assert.Equal(t, http.StatusTooManyRequests,
		hitRedeem(t, router, "203.0.113.250", userID),
		"换代理就重置计数 = 代理池规模直接等于猜测次数的放大倍数")
}

// 一批账号共用一个 IP:账号桶每次都是新的,IP 桶必须把它按住。
func TestRedeemRateLimitStillCapsOneIPAcrossManyAccounts(t *testing.T) {
	const limit = 3
	const ip = "198.51.100.42"
	router := newRedeemRateLimitEngine(t, limit)

	for i := 0; i < limit; i++ {
		require.Equal(t, http.StatusOK, hitRedeem(t, router, ip, 91000+i),
			"第 %d 次尝试来自新账号,应当放行", i+1)
	}

	assert.Equal(t, http.StatusTooManyRequests, hitRedeem(t, router, ip, 91999),
		"注册一批号就重置计数 = 账号成本直接等于猜测次数的放大倍数")
}
