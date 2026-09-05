package middleware

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// fakeClock 是一个可以被测试推着走的秒钟。
// 这一族用例守的全部是**时间窗口**的行为,拿真实时钟测等于拿 sleep 换确定性。
type fakeClock struct{ sec int64 }

func (f *fakeClock) now() int64  { return f.sec }
func (f *fakeClock) add(d int64) { f.sec += d }

// TestQyTokenLiveTracksInFlightAndRecentRequests 是这套计数的主用例:
// 两个数是**独立**的两件事 —— 在途随 release 立刻回落,近 1 分钟不会。
//
// 会杀掉的改法:把 requests 实现成 inFlight 的别名(两个数一起回落),
// 或者 release 时顺手把窗口计数也减掉。
func TestQyTokenLiveTracksInFlightAndRecentRequests(t *testing.T) {
	clock := &fakeClock{sec: 1_700_000_000}
	reg := newQyTokenLiveRegistry(clock.now)

	first := reg.enter(7, 42)
	require.NotNil(t, first)
	second := reg.enter(7, 42)
	require.NotNil(t, second)

	assert.Equal(t, QyTokenLive{InFlight: 2, Requests: 2}, reg.snapshot(42)[7])

	first()
	assert.Equal(t, QyTokenLive{InFlight: 1, Requests: 2}, reg.snapshot(42)[7],
		"还回一个在途位不该改变'这一分钟发过几次'")

	second()
	assert.Equal(t, QyTokenLive{InFlight: 0, Requests: 2}, reg.snapshot(42)[7])
}

// TestQyTokenLiveRequestsLeaveTheWindow 守窗口的两个端点。
//
// 59 秒仍在窗口内、60 秒滑出,是「近 1 分钟」这句话的全部含义。
// 会杀掉的改法:环长写成 59 / 61,或者 roll 时漏清跳过的那些秒。
func TestQyTokenLiveRequestsLeaveTheWindow(t *testing.T) {
	clock := &fakeClock{sec: 1_700_000_000}
	reg := newQyTokenLiveRegistry(clock.now)

	reg.enter(7, 42)()

	clock.add(QyTokenLiveWindowSeconds - 1)
	assert.Equal(t, 1, reg.snapshot(42)[7].Requests, "第 59 秒还在窗口里")

	clock.add(1)
	assert.Zero(t, reg.snapshot(42)[7].Requests, "第 60 秒必须滑出窗口")
}

// TestQyTokenLiveSnapshotIsScopedToItsOwner 守越权面。
//
// 这条接口按会话身份取数、不接受前端传令牌 id,而属主是**写入时**记下的。
// 会杀掉的改法:snapshot 改成扫全表(于是每个人都看得见所有人的并发)。
func TestQyTokenLiveSnapshotIsScopedToItsOwner(t *testing.T) {
	clock := &fakeClock{sec: 1_700_000_000}
	reg := newQyTokenLiveRegistry(clock.now)

	reg.enter(7, 42)
	reg.enter(9, 43)

	mine := reg.snapshot(42)
	assert.Len(t, mine, 1)
	assert.Contains(t, mine, 7)
	assert.NotContains(t, mine, 9)

	assert.Empty(t, reg.snapshot(0), "没有会话身份时不该看见任何人的数")
}

// TestQyTokenLiveReleaseIsIdempotent 守「多还一次」。
//
// 多还一次的表现是并发数越用越小、最后恒为 0 —— 一个只会骗人、不会报错的
// 失败形态。会杀掉的改法:把 enter 返回的闭包里的 sync.Once 去掉。
func TestQyTokenLiveReleaseIsIdempotent(t *testing.T) {
	clock := &fakeClock{sec: 1_700_000_000}
	reg := newQyTokenLiveRegistry(clock.now)

	keep := reg.enter(7, 42)
	require.NotNil(t, keep)
	extra := reg.enter(7, 42)
	require.NotNil(t, extra)

	extra()
	extra()
	extra()

	assert.Equal(t, 1, reg.snapshot(42)[7].InFlight, "还三次也只该还掉一个位")
}

// TestQyTokenLiveForgetsIdleTokens 守这张表不会只增不减。
//
// 它是一个**常驻进程内**的 map,键是全站的令牌 id。不回收的话,一个跑了半年的
// 站点会把每一把用过的 key 都留在内存里。会杀掉的改法:去掉 sweepLocked,
// 或者把回收判据写成"只看在途"(于是刚发过请求的行被提前删掉、计数归零)。
func TestQyTokenLiveForgetsIdleTokens(t *testing.T) {
	clock := &fakeClock{sec: 1_700_000_000}
	reg := newQyTokenLiveRegistry(clock.now)

	reg.enter(7, 42)()
	require.Len(t, reg.tokens, 1)

	// 清扫只在写入路径上跑,所以推进时钟之后要再来一次请求才会触发。
	clock.add(qyTokenLiveSweepEverySeconds + QyTokenLiveWindowSeconds)
	reg.enter(8, 43)()

	assert.NotContains(t, reg.tokens, 7, "静默满一个窗口的令牌应被回收")
	assert.NotContains(t, reg.byUser, 42, "属主索引要跟着一起清,否则它自己会只增不减")
	assert.Contains(t, reg.tokens, 8, "刚刚发过请求的那一把不能被顺手删掉")
}

// TestQyTokenLiveSurvivesClockGoingBackwards 守 NTP 校时 / 运维改系统时间。
//
// 挂钟回拨时保留旧桶,会让"近 1 分钟"把回拨之前那一段的计数重复算进来。
// 宁可偏小:一个偏小的显示位不会让任何人做出错误决定。
func TestQyTokenLiveSurvivesClockGoingBackwards(t *testing.T) {
	clock := &fakeClock{sec: 1_700_000_000}
	reg := newQyTokenLiveRegistry(clock.now)

	reg.enter(7, 42)()
	reg.enter(7, 42)()

	clock.add(-30)
	reg.enter(7, 42)()

	assert.Equal(t, 1, reg.snapshot(42)[7].Requests,
		"回拨之后只该算回拨之后的那一次")
}

// TestQyTokenLiveStatsMiddlewareCountsWhileTheHandlerRuns 守中间件这一层:
// 在途位必须在 handler **正在跑**的时候就已经占上,并在它返回后归还。
//
// 会杀掉的改法:把 enter 挪到 c.Next() 之后(于是并发数恒为 0),
// 或者忘了 defer release(于是并发数只增不减)。
func TestQyTokenLiveStatsMiddlewareCountsWhileTheHandlerRuns(t *testing.T) {
	gin.SetMode(gin.TestMode)

	const tokenID, userID = 918_273, 918_274
	var seenInFlight int

	engine := gin.New()
	engine.Use(func(c *gin.Context) {
		c.Set("token_id", tokenID)
		c.Set("id", userID)
		c.Next()
	})
	engine.Use(QyTokenLiveStats())
	engine.GET("/relay", func(c *gin.Context) {
		seenInFlight = QyTokenLiveSnapshot(userID)[tokenID].InFlight
		c.Status(http.StatusOK)
	})

	engine.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "/relay", nil))

	assert.Equal(t, 1, seenInFlight, "handler 跑着的时候在途数必须是 1")
	after := QyTokenLiveSnapshot(userID)[tokenID]
	assert.Zero(t, after.InFlight, "handler 返回后在途位必须还回去")
	assert.Equal(t, 1, after.Requests, "这一次请求要留在近 1 分钟的计数里")
}

// TestQyTokenLiveStatsMiddlewareSkipsRequestsWithoutAToken 守没有令牌身份的路由。
//
// 中间件挂在 TokenAuth 之后,但 TokenAuth 之前被拦掉、或者别处误挂时,
// c 里没有 token_id。那时不能凭空建一行(键会是 0,所有无令牌请求挤在一起)。
func TestQyTokenLiveStatsMiddlewareSkipsRequestsWithoutAToken(t *testing.T) {
	gin.SetMode(gin.TestMode)

	engine := gin.New()
	engine.Use(QyTokenLiveStats())
	engine.GET("/relay", func(c *gin.Context) { c.Status(http.StatusOK) })
	engine.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "/relay", nil))

	qyTokenLive.mu.Lock()
	defer qyTokenLive.mu.Unlock()
	assert.NotContains(t, qyTokenLive.tokens, 0, "没有令牌身份时不该建出 id=0 那一行")
}
