package middleware

import (
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/constant"
	"github.com/QuantumNous/new-api/setting"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// withRateLimitTables 把两张限流表换成用例自己的,并在结束时还原。
func withRateLimitTables(t *testing.T, rpm map[string][2]int, concurrency map[string]int) {
	t.Helper()
	setting.ModelRequestRateLimitMutex.Lock()
	prevRPM := setting.ModelRequestRateLimitGroup
	prevCon := setting.ModelRequestConcurrencyGroup
	setting.ModelRequestRateLimitGroup = rpm
	setting.ModelRequestConcurrencyGroup = concurrency
	setting.ModelRequestRateLimitMutex.Unlock()
	t.Cleanup(func() {
		setting.ModelRequestRateLimitMutex.Lock()
		setting.ModelRequestRateLimitGroup = prevRPM
		setting.ModelRequestConcurrencyGroup = prevCon
		setting.ModelRequestRateLimitMutex.Unlock()
	})
}

func ctxWithGroups(userGroup string) *gin.Context {
	c, _ := gin.CreateTestContext(httptest.NewRecorder())
	if userGroup != "" {
		common.SetContextKey(c, constant.ContextKeyUserGroup, userGroup)
	}
	return c
}

// 分组限流表是按**用户分组**配的,查表就必须按用户分组查。
//
// 这条是本次改动的全部意义:改之前热路径拿到的是模型分组(auto 令牌下更是
// 字面量 "auto"),于是运营在页面上填的用户分组名永远查不到,那一行静默失效。
func TestQyRateLimitGroupPrefersTheUserGroup(t *testing.T) {
	withRateLimitTables(t, map[string][2]int{"vip-user": {10, 5}}, nil)

	// 上游算出来的是模型分组 / auto,都不该赢过用户分组。
	for _, upstream := range []string{"", "auto", "vip-models"} {
		assert.Equal(t, "vip-user", QyRateLimitGroup(ctxWithGroups("vip-user"), upstream),
			"upstream=%q 时仍必须按用户分组查表", upstream)
	}
}

// 升级兼容:用户分组没配、而旧的那个键(模型分组名或 auto)配了,那一行仍要生效。
//
// 限流是安全设施,直接改口径会让升级前配好的表在升级那一刻集体失效,
// 而静默失效比配错更糟。
func TestQyRateLimitGroupFallsBackToTheLegacyKey(t *testing.T) {
	withRateLimitTables(t, map[string][2]int{"auto": {3, 1}}, nil)

	assert.Equal(t, "auto", QyRateLimitGroup(ctxWithGroups("default"), "auto"))
}

// 两边都没配时返回用户分组:调用方查不到,自然回落全站默认值。
// 关键是**不能**返回那个模型分组名 —— 否则将来运营给模型分组起了个与用户分组
// 同名的名字,配置会突然开始命中一个它从没打算命中的桶。
func TestQyRateLimitGroupReturnsUserGroupWhenNothingConfigured(t *testing.T) {
	withRateLimitTables(t, map[string][2]int{}, nil)

	assert.Equal(t, "default", QyRateLimitGroup(ctxWithGroups("default"), "some-model-group"))
}

// 完全没有用户分组(理论上不该发生)时保持上游原样,不改变任何行为。
func TestQyRateLimitGroupKeepsUpstreamWhenUserGroupMissing(t *testing.T) {
	withRateLimitTables(t, map[string][2]int{"auto": {3, 1}}, nil)

	assert.Equal(t, "auto", QyRateLimitGroup(ctxWithGroups(""), "auto"))
}

// 并发闸:占满之后必须拒,归还之后必须能再占。
func TestConcurrencyAcquireAndRelease(t *testing.T) {
	counter := &qyConcurrencyCounter{inFlight: map[string]int{}}

	r1, ok := counter.acquire("g", 2)
	require.True(t, ok)
	r2, ok := counter.acquire("g", 2)
	require.True(t, ok)

	_, ok = counter.acquire("g", 2)
	assert.False(t, ok, "占满之后必须拒绝第三个")

	r1()
	r3, ok := counter.acquire("g", 2)
	assert.True(t, ok, "归还一个之后必须能再占一个")

	// 重复归还必须是幂等的:中间件用 defer 调它,而任何一次"保险起见再调一次"
	// 都会把计数扣穿,表现是这个分组的并发额度凭空变大。
	r1()
	r1()
	r2()
	r3()
	assert.Zero(t, counter.inFlight["g"], "全部归还之后计数必须归零")
	assert.NotContains(t, counter.inFlight, "g", "归零的键要删掉,否则这张表只增不减")
}

// limit <= 0 = 不限:既不计数,也不返回需要归还的 release。
func TestConcurrencyZeroMeansUnlimited(t *testing.T) {
	counter := &qyConcurrencyCounter{inFlight: map[string]int{}}
	for i := 0; i < 100; i++ {
		release, ok := counter.acquire("g", 0)
		require.True(t, ok)
		assert.Nil(t, release)
	}
	assert.Empty(t, counter.inFlight)
}

// 分组之间互不影响。
func TestConcurrencyIsPerGroup(t *testing.T) {
	counter := &qyConcurrencyCounter{inFlight: map[string]int{}}
	_, ok := counter.acquire("a", 1)
	require.True(t, ok)
	_, ok = counter.acquire("a", 1)
	require.False(t, ok)
	_, ok = counter.acquire("b", 1)
	assert.True(t, ok, "b 组的额度不该被 a 组占掉")
}

// 中间件层面:超限回 429,且**不会漏掉归还** —— 哪怕 handler panic。
func TestConcurrencyMiddlewareRejectsAndNeverLeaks(t *testing.T) {
	gin.SetMode(gin.TestMode)
	withRateLimitTables(t, nil, map[string]int{"tiny": 1})
	t.Cleanup(func() {
		qyConcurrency.mu.Lock()
		delete(qyConcurrency.inFlight, "tiny")
		qyConcurrency.mu.Unlock()
	})

	hold := make(chan struct{})
	done := make(chan struct{})
	router := gin.New()
	router.Use(gin.Recovery())
	router.Use(func(c *gin.Context) {
		common.SetContextKey(c, constant.ContextKeyUserGroup, "tiny")
		c.Next()
	})
	router.Use(QyModelRequestConcurrencyLimit())
	router.GET("/hold", func(c *gin.Context) { <-hold; c.Status(http.StatusOK) })
	router.GET("/boom", func(c *gin.Context) { panic("handler exploded") })

	// 第一个请求占住唯一的位。
	go func() {
		defer close(done)
		router.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "/hold", nil))
	}()
	require.Eventually(t, func() bool { return qyInFlight("tiny") == 1 },
		2*time.Second, 5*time.Millisecond, "第一个请求应当占住那个位")

	// 第二个必须被拒。
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/hold", nil))
	assert.Equal(t, http.StatusTooManyRequests, rec.Code)

	close(hold)
	<-done
	assert.Zero(t, qyInFlight("tiny"), "请求结束后必须归还")

	// panic 的那一条同样要归还:defer 在栈展开时会跑到,而 gin 的 Recovery 在更外层。
	rec = httptest.NewRecorder()
	router.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/boom", nil))
	assert.Equal(t, http.StatusInternalServerError, rec.Code)
	assert.Zero(t, qyInFlight("tiny"), "handler panic 之后位子也必须还回来,否则这个分组会被一次崩溃永久扣掉一个额度")
}

// 并发下计数不许错:2000 次 acquire/release 之后必须精确归零。
// 用 -race 跑这条才有全部意义。
func TestConcurrencyCounterIsRaceFree(t *testing.T) {
	counter := &qyConcurrencyCounter{inFlight: map[string]int{}}
	var wg sync.WaitGroup
	for i := 0; i < 200; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := 0; j < 10; j++ {
				if release, ok := counter.acquire("g", 50); ok && release != nil {
					release()
				}
			}
		}()
	}
	wg.Wait()
	assert.Zero(t, counter.inFlight["g"])
}

// 保存限流配置与热路径读取并发发生时不许 data race。
//
// 原先 UpdateModelRequestRateLimitGroupByJSONString 写 map 拿的是 RLock,
// 两个读锁互不排斥 —— Go 运行时对并发读写 map 是直接 fatal,不是"读到旧值"。
func TestRateLimitTableUpdateIsRaceFree(t *testing.T) {
	withRateLimitTables(t, map[string][2]int{"g": {1, 1}}, nil)

	stop := make(chan struct{})
	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for {
				select {
				case <-stop:
					return
				default:
					setting.GetGroupRateLimit("g")
				}
			}
		}()
	}
	for i := 0; i < 50; i++ {
		require.NoError(t, setting.UpdateModelRequestRateLimitGroupByJSONString(`{"g":[10,5]}`))
	}
	close(stop)
	wg.Wait()

	total, success, found := setting.GetGroupRateLimit("g")
	require.True(t, found)
	assert.Equal(t, 10, total)
	assert.Equal(t, 5, success)
}

// 解析失败时那张表必须**保持原样**,不能被清空。
// 清空的后果是全站分组限流静默失效,而管理员只会看到一句"保存失败"。
func TestRateLimitTableSurvivesABadPayload(t *testing.T) {
	withRateLimitTables(t, map[string][2]int{"g": {7, 3}}, nil)

	require.Error(t, setting.UpdateModelRequestRateLimitGroupByJSONString(`{"g":`))

	total, success, found := setting.GetGroupRateLimit("g")
	require.True(t, found, "解析失败不该把已有配置抹掉")
	assert.Equal(t, 7, total)
	assert.Equal(t, 3, success)
}

// 并发上限表:0 与缺键都表示不限,负数在校验期就要被拒。
func TestConcurrencyGroupValidation(t *testing.T) {
	require.NoError(t, setting.CheckModelRequestConcurrencyGroup(`{"a":0,"b":16}`))
	require.Error(t, setting.CheckModelRequestConcurrencyGroup(`{"a":-1}`))
	require.Error(t, setting.CheckModelRequestConcurrencyGroup(`not json`))
}
