package middleware

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strconv"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/logger"
	"github.com/gin-gonic/gin"
)

const redisRateLimitNamespace = "rateLimit:v2"

// Redis rate limiting intentionally uses a fixed window. The single Lua script
// makes increment, expiry, and the limit decision atomic, while retaining the
// simple fixed-window behavior: traffic at a window boundary can burst up to
// twice the configured limit. Do not replace this with a sliding-window ZSET
// unless that externally visible behavior is intentionally changed.
const redisFixedWindowScript = `
local count = redis.call('INCR', KEYS[1])
if count == 1 then
  redis.call('EXPIRE', KEYS[1], ARGV[2])
end
local ttl = redis.call('TTL', KEYS[1])
if ttl < 0 then
  redis.call('EXPIRE', KEYS[1], ARGV[2])
  ttl = redis.call('TTL', KEYS[1])
end
if count > tonumber(ARGV[1]) then
  return {0, count, ttl}
end
return {1, count, ttl}
`

var inMemoryRateLimiter common.InMemoryRateLimiter

var defNext = func(c *gin.Context) {
	c.Next()
}

func redisIPRateLimitKey(mark string, clientIP string) string {
	return fmt.Sprintf("%s:ip:%s:%s", redisRateLimitNamespace, mark, clientIP)
}

func redisUserRateLimitKey(mark string, userID int) string {
	return fmt.Sprintf("%s:user:%s:%d", redisRateLimitNamespace, mark, userID)
}

func redisReplyInteger(value interface{}) (int64, error) {
	switch typed := value.(type) {
	case int64:
		return typed, nil
	case string:
		return strconv.ParseInt(typed, 10, 64)
	case []byte:
		return strconv.ParseInt(string(typed), 10, 64)
	default:
		return 0, fmt.Errorf("unexpected Redis integer reply type %T", value)
	}
}

func redisFixedWindowTake(ctx context.Context, key string, maxRequestNum int, duration int64) (bool, int64, int64, error) {
	if common.RDB == nil {
		return false, 0, 0, errors.New("Redis client is not initialized")
	}
	if key == "" {
		return false, 0, 0, errors.New("rate limit key is empty")
	}
	if maxRequestNum <= 0 {
		return false, 0, 0, errors.New("rate limit maximum must be positive")
	}
	if duration <= 0 {
		return false, 0, 0, errors.New("rate limit duration must be positive")
	}

	values, err := common.RDB.Eval(
		ctx,
		redisFixedWindowScript,
		[]string{key},
		maxRequestNum,
		duration,
	).Slice()
	if err != nil {
		return false, 0, 0, err
	}
	if len(values) != 3 {
		return false, 0, 0, fmt.Errorf("unexpected Redis rate limit reply length %d", len(values))
	}

	allowedValue, err := redisReplyInteger(values[0])
	if err != nil {
		return false, 0, 0, err
	}
	count, err := redisReplyInteger(values[1])
	if err != nil {
		return false, 0, 0, err
	}
	ttlSeconds, err := redisReplyInteger(values[2])
	if err != nil {
		return false, 0, 0, err
	}

	return allowedValue == 1, count, ttlSeconds, nil
}

func redisRateLimiter(c *gin.Context, maxRequestNum int, duration int64, mark string) {
	allowed, _, ttlSeconds, err := redisFixedWindowTake(
		c.Request.Context(),
		redisIPRateLimitKey(mark, common.ClientIP(c)),
		maxRequestNum,
		duration,
	)
	if err != nil {
		logger.LogError(c.Request.Context(), fmt.Sprintf("rate limit check failed (mark=%s): %v", mark, err))
		c.Status(http.StatusInternalServerError)
		c.Abort()
		return
	}
	if !allowed {
		writeRateLimited(c, ttlSeconds)
	}
}

func memoryRateLimiter(c *gin.Context, maxRequestNum int, duration int64, mark string) {
	key := mark + common.ClientIP(c)
	if !inMemoryRateLimiter.Request(key, maxRequestNum, duration) {
		writeRateLimited(c, duration)
		return
	}
}

// writeRateLimited rejects the request with 429 and a Retry-After hint so
// clients can back off instead of treating the rejection as a fatal error.
// The in-memory limiter cannot report the remaining window, so callers
// without a TTL pass the full window duration as a conservative upper bound.
func writeRateLimited(c *gin.Context, retryAfterSeconds int64) {
	if retryAfterSeconds > 0 {
		c.Header("Retry-After", strconv.FormatInt(retryAfterSeconds, 10))
	}
	c.Status(http.StatusTooManyRequests)
	c.Abort()
}

func rateLimitFactory(maxRequestNum int, duration int64, mark string) func(c *gin.Context) {
	return rateLimitFactoryScoped(maxRequestNum, duration, mark, false)
}

// rateLimitFactoryScoped 造一个限流中间件。perRoute 为真时,桶的 key 里带上路由
// 模板(c.FullPath()),即**每条路由各有一个**计数器。
//
// 关键接口(CriticalRateLimit)必须 perRoute:此前它们共用同一个字面量 mark
// "CT",于是充值/兑换码、划转、提现、登录/注册、甚至匿名的 GET /api/ratio_config
// 与前端每 15 分钟一次的 auth/refresh 全部挤在同一个 20 次/20 分钟的 IP 计数器
// 里。同一出口 IP(公司 NAT、机场、基站)下任何一个人刷任意一个关键接口,就能让
// 其余所有人 20 分钟内提不了现、充不了值、登不上;NAT 后十几个活跃标签页的正常
// 续期就足以自己把桶吃光,不需要攻击者。
//
// 按路由分桶不削弱防护:登录爆破看的是登录那条路由自己的 20 次,与「有没有人在
// 另一个接口上刷」无关。全局限流(GlobalWebRateLimit / GlobalAPIRateLimit)刻意
// 不分路由 —— 它们要度量的就是这个 IP 的总量。
func rateLimitFactoryScoped(maxRequestNum int, duration int64, mark string, perRoute bool) func(c *gin.Context) {
	scopedMark := func(c *gin.Context) string {
		if !perRoute {
			return mark
		}
		return mark + ":" + c.FullPath()
	}
	if common.RedisEnabled {
		return func(c *gin.Context) {
			redisRateLimiter(c, maxRequestNum, duration, scopedMark(c))
		}
	}
	// It's safe to call multi times.
	inMemoryRateLimiter.Init(common.RateLimitKeyExpirationDuration)
	return func(c *gin.Context) {
		memoryRateLimiter(c, maxRequestNum, duration, scopedMark(c))
	}
}

func GlobalWebRateLimit() func(c *gin.Context) {
	if common.GlobalWebRateLimitEnable {
		return rateLimitFactory(common.GlobalWebRateLimitNum, common.GlobalWebRateLimitDuration, "GW")
	}
	return defNext
}

func GlobalAPIRateLimit() func(c *gin.Context) {
	if common.GlobalApiRateLimitEnable {
		return rateLimitFactory(common.GlobalApiRateLimitNum, common.GlobalApiRateLimitDuration, "GA")
	}
	return defNext
}

func CriticalRateLimit() func(c *gin.Context) {
	if common.CriticalRateLimitEnable {
		return rateLimitFactoryScoped(common.CriticalRateLimitNum, common.CriticalRateLimitDuration, "CT", true)
	}
	return defNext
}

func UserCriticalRateLimit(scope string) func(c *gin.Context) {
	if !common.CriticalRateLimitEnable {
		return defNext
	}
	return userRateLimitFactory(
		common.CriticalRateLimitNum,
		common.CriticalRateLimitDuration,
		"UC:"+scope,
	)
}

func DownloadRateLimit() func(c *gin.Context) {
	return rateLimitFactory(common.DownloadRateLimitNum, common.DownloadRateLimitDuration, "DW")
}

func UploadRateLimit() func(c *gin.Context) {
	return rateLimitFactory(common.UploadRateLimitNum, common.UploadRateLimitDuration, "UP")
}

// userRateLimitFactory creates a rate limiter keyed by authenticated user ID
// instead of client IP, making it resistant to proxy rotation attacks.
// Must be used AFTER authentication middleware (UserAuth).
func userRateLimitFactory(maxRequestNum int, duration int64, mark string) func(c *gin.Context) {
	if common.RedisEnabled {
		return func(c *gin.Context) {
			userID := c.GetInt("id")
			if userID == 0 {
				c.Status(http.StatusUnauthorized)
				c.Abort()
				return
			}
			userRedisRateLimiter(c, maxRequestNum, duration, redisUserRateLimitKey(mark, userID))
		}
	}
	// It's safe to call multi times.
	inMemoryRateLimiter.Init(common.RateLimitKeyExpirationDuration)
	return func(c *gin.Context) {
		userID := c.GetInt("id")
		if userID == 0 {
			c.Status(http.StatusUnauthorized)
			c.Abort()
			return
		}
		key := fmt.Sprintf("%s:user:%d", mark, userID)
		if !inMemoryRateLimiter.Request(key, maxRequestNum, duration) {
			writeRateLimited(c, duration)
			return
		}
	}
}

// userRedisRateLimiter is like redisRateLimiter but accepts a pre-built key
// (to support user-ID-based keys).
func userRedisRateLimiter(c *gin.Context, maxRequestNum int, duration int64, key string) {
	allowed, _, ttlSeconds, err := redisFixedWindowTake(c.Request.Context(), key, maxRequestNum, duration)
	if err != nil {
		logger.LogError(c.Request.Context(), fmt.Sprintf("rate limit check failed (key=%s): %v", key, err))
		c.Status(http.StatusInternalServerError)
		c.Abort()
		return
	}
	if !allowed {
		writeRateLimited(c, ttlSeconds)
	}
}

// SearchRateLimit returns a per-user rate limiter for search endpoints.
// Configurable via SEARCH_RATE_LIMIT_ENABLE / SEARCH_RATE_LIMIT / SEARCH_RATE_LIMIT_DURATION.
func SearchRateLimit() func(c *gin.Context) {
	if !common.SearchRateLimitEnable {
		return defNext
	}
	return userRateLimitFactory(common.SearchRateLimitNum, common.SearchRateLimitDuration, "SR")
}
