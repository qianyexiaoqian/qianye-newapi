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

// trusted_proxies_test.go —— 取值策略**接线**这一层的回归。
//
// 取值逻辑本身(各种部署形态、信任链剥离、归一化)由 common/client_ip_test.go
// 覆盖。这里守的是另一件事:ConfigureTrustedProxies 真的把策略装进了进程,
// ClientIPResolver 真的挂上了,而业务代码调 common.ClientIP(c) 拿到的就是它。
//
// 这两层必须分开测。只测 common 的话,「策略写好了但没人装载」会全绿通过 ——
// 而那正是本仓反复出现的形状:实现了、能配、能回读,线上永不生效。

// newClientIPRouter 造一个与 main.go 同构的最小引擎:
// ConfigureTrustedProxies → ClientIPResolver → 业务 handler。
func newClientIPRouter(t *testing.T) *gin.Engine {
	t.Helper()
	gin.SetMode(gin.TestMode)
	router := gin.New()
	require.NoError(t, ConfigureTrustedProxies(router))
	t.Cleanup(func() { common.SetClientIPPolicy(nil) })
	router.Use(ClientIPResolver())
	router.GET("/client-ip", func(c *gin.Context) {
		c.String(http.StatusOK, common.ClientIP(c))
	})
	return router
}

func requestClientIP(router http.Handler, remoteAddr string, forwardedFor string) string {
	recorder := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodGet, "/client-ip", nil)
	request.RemoteAddr = remoteAddr
	if forwardedFor != "" {
		request.Header.Set("X-Forwarded-For", forwardedFor)
	}
	router.ServeHTTP(recorder, request)
	return recorder.Body.String()
}

func setTrustedProxiesEnv(t *testing.T, value string) {
	t.Helper()
	t.Setenv("TRUSTED_PROXIES", value)
	t.Setenv("CLIENT_IP_HEADERS", "")
	t.Setenv("BIND_ADDRESS", "")
	t.Setenv("TRUSTED_PROXIES_CLOUDFLARE_FILE", "")
}

func TestConfigureTrustedProxiesPrivateTrustsLoopbackAndPrivateNetworks(t *testing.T) {
	setTrustedProxiesEnv(t, "private")
	router := newClientIPRouter(t)

	testCases := []struct {
		name       string
		remoteAddr string
	}{
		{name: "IPv4 loopback", remoteAddr: "127.0.0.1:12345"},
		{name: "IPv6 loopback", remoteAddr: "[::1]:12345"},
		{name: "10 private network", remoteAddr: "10.20.30.40:12345"},
		{name: "172 private network", remoteAddr: "172.20.0.2:12345"},
		{name: "192 private network", remoteAddr: "192.168.10.2:12345"},
		{name: "IPv6 unique local network", remoteAddr: "[fd12:3456::2]:12345"},
	}

	for _, testCase := range testCases {
		t.Run(testCase.name, func(t *testing.T) {
			clientIP := requestClientIP(router, testCase.remoteAddr, "203.0.113.10")
			assert.Equal(t, "203.0.113.10", clientIP)
		})
	}
}

func TestConfigureTrustedProxiesPrivateRejectsPublicPeerHeaders(t *testing.T) {
	setTrustedProxiesEnv(t, " PriVate ")
	router := newClientIPRouter(t)

	clientIP := requestClientIP(router, "198.51.100.10:12345", "203.0.113.10")
	assert.Equal(t, "198.51.100.10", clientIP, "a public peer must not make a spoofed X-Forwarded-For authoritative")
}

func TestConfigureTrustedProxiesPrivateStopsAtPublicClientInForwardedChain(t *testing.T) {
	setTrustedProxiesEnv(t, "private")
	router := newClientIPRouter(t)

	clientIP := requestClientIP(router, "172.20.0.2:12345", "192.0.2.99, 203.0.113.10")
	assert.Equal(t, "203.0.113.10", clientIP, "the first public hop from the trusted proxy must win over a client-supplied prefix")
}

func TestConfigureTrustedProxiesNoneDisablesForwardedHeaders(t *testing.T) {
	setTrustedProxiesEnv(t, " NoNe ")
	router := newClientIPRouter(t)

	clientIP := requestClientIP(router, "127.0.0.1:12345", "203.0.113.10")
	assert.Equal(t, "127.0.0.1", clientIP)
}

func TestConfigureTrustedProxiesAcceptsTrimmedIPsAndCIDRs(t *testing.T) {
	setTrustedProxiesEnv(t, " 192.0.2.0/24, 198.51.100.30 ")
	router := newClientIPRouter(t)

	trustedClientIP := requestClientIP(router, "192.0.2.10:12345", "203.0.113.20")
	assert.Equal(t, "203.0.113.20", trustedClientIP)

	trustedExactIP := requestClientIP(router, "198.51.100.30:12345", "203.0.113.21")
	assert.Equal(t, "203.0.113.21", trustedExactIP)

	untrustedClientIP := requestClientIP(router, "198.51.100.20:12345", "203.0.113.22")
	assert.Equal(t, "198.51.100.20", untrustedClientIP)

	defaultProxyIP := requestClientIP(router, "127.0.0.1:12345", "203.0.113.23")
	assert.Equal(t, "127.0.0.1", defaultProxyIP, "an explicit list must replace, not extend, the compatibility defaults")
}

func TestConfigureTrustedProxiesRejectsInvalidConfiguration(t *testing.T) {
	gin.SetMode(gin.TestMode)
	testCases := []struct {
		name  string
		value string
	}{
		{name: "no entries", value: ", ,"},
		{name: "invalid entry", value: "not-an-ip"},
		{name: "mixed valid and invalid entries", value: "127.0.0.1, not-an-ip"},
		{name: "none mixed with valid entry", value: "none,127.0.0.1"},
		{name: "valid entry mixed with none", value: "127.0.0.1,NONE"},
	}

	for _, testCase := range testCases {
		t.Run(testCase.name, func(t *testing.T) {
			setTrustedProxiesEnv(t, testCase.value)
			assert.Error(t, ConfigureTrustedProxies(gin.New()))
		})
	}
}

// TestConfigureTrustedProxiesUnsetMatchesUpstreamDefaults 守本轮的撤回。
//
// 上游 new-api 未配置 TRUSTED_PROXIES 时装的是
// {127.0.0.0/8, ::1, 10.0.0.0/8, 172.16.0.0/12, 192.168.0.0/16, fc00::/7}
// 并打一条 WARNING;上游没有任何强制,也不拒绝启动。本仓前两轮把这一档改成了
// 「谁都不信」——那是一次行为变更(装在反代后面、从没配过这个变量的部署会在
// 升级那一秒看到全站客户端 IP 变成反代地址),现在撤回。
//
// 这一层守的是**接线**:common 那边把默认改回来了,而这条要求 gin 引擎上
// 真的跑着同一份默认。
func TestConfigureTrustedProxiesUnsetMatchesUpstreamDefaults(t *testing.T) {
	for _, value := range []string{"", " \t "} {
		setTrustedProxiesEnv(t, value)
		router := newClientIPRouter(t)

		for _, peer := range []string{
			"127.0.0.1:12345",
			"[::1]:12345",
			"10.20.30.40:12345",
			"172.20.0.2:12345",
			"192.168.10.2:12345",
			"[fd12:3456::2]:12345",
		} {
			assert.Equal(t, "203.0.113.10", requestClientIP(router, peer, "203.0.113.10"),
				"未配置 TRUSTED_PROXIES 时,来自 %s 的 X-Forwarded-For 必须与上游一样被采信", peer)
		}
		// 上游那份默认里没有公网地址,所以公网对端的转发头照样作废。
		assert.Equal(t, "198.51.100.7",
			requestClientIP(router, "198.51.100.7:12345", "203.0.113.10"))
	}
}

// TestConfigureTrustedProxiesUnsetDoesNotRefuseToStart 守「只给提示,不做强制」。
//
// 项目方的判决:上游没有任何强制,我们也不加。未配置是**合法配置**,
// 它必须能起得来,而代价由 policy.Warning(启动日志里那条 WARNING)表达。
func TestConfigureTrustedProxiesUnsetDoesNotRefuseToStart(t *testing.T) {
	gin.SetMode(gin.TestMode)
	for _, value := range []string{"", " \t "} {
		setTrustedProxiesEnv(t, value)
		require.NoError(t, ConfigureTrustedProxies(gin.New()),
			"未配置 TRUSTED_PROXIES 不是错误,不能让进程起不来")
		t.Cleanup(func() { common.SetClientIPPolicy(nil) })

		policy := common.ActiveClientIPPolicy()
		require.NotNil(t, policy)
		assert.Equal(t, common.ClientIPStrategyDefaultPrivate, policy.Strategy)
		assert.NotEmpty(t, policy.Warning,
			"上游在这一档打 WARNING,提示必须留着 —— 撤掉的是强制,不是提示")
	}
}

// TestClientIPResolverFeedsTheObservationDesk 守那条提示真的接上了。
//
// 上游默认覆盖不到反代坐在公网地址上的部署(CDN 回源、独立 LB 主机):
// 那种站点一切照常 200,只是所有人的 IP 都成了反代的地址。观测台是这一档
// 唯一的信号,而它必须由**中间件**来喂 —— 挂在业务代码里的话,没调过 ClientIP
// 的路径就观测不到,那正是最需要被观测到的那些(静态资源、健康检查、
// 被 401 挡掉的探测)。它只提示,不改变任何结论。
func TestClientIPResolverFeedsTheObservationDesk(t *testing.T) {
	common.ResetClientIPObservations()
	t.Cleanup(common.ResetClientIPObservations)

	setTrustedProxiesEnv(t, "")
	router := newClientIPRouter(t)

	assert.Equal(t, "198.51.100.5", requestClientIP(router, "198.51.100.5:12345", "203.0.113.10"))
	assert.Equal(t, "198.51.100.5", requestClientIP(router, "198.51.100.5:12346", "203.0.113.11"))
	// 没带转发头的请求不该进观测台:它不是"有反代却没配",它就是直连。
	assert.Equal(t, "198.51.100.4", requestClientIP(router, "198.51.100.4:12345", ""))
	// 私网对端在上游默认下是受信的,它没有任何错配可报 —— 进了观测台就是噪声。
	assert.Equal(t, "203.0.113.12", requestClientIP(router, "172.18.0.5:12345", "203.0.113.12"))

	observations, dropped := common.ClientIPObservations()
	require.Len(t, observations, 1)
	assert.Zero(t, dropped)
	assert.Equal(t, "198.51.100.5", observations[0].Peer)
	assert.EqualValues(t, 2, observations[0].Count)
	assert.Equal(t, "198.51.100.5/32", observations[0].Suggestion,
		"诊断必须给出可以直接粘贴的值 —— 只说「配置有问题」等于把人推回去猜")
}

// TestClientIPResolverResolvesOncePerRequest 守缓存。
//
// 一条请求上限流、鉴权、台账、日志会各取一次。取值必须只发生一次:
// 除了浪费之外,重复解析给"同一请求内两次取值不一致"留了缝 ——
// 中间件链上任何一处改了请求头,后面取到的就是另一个值,而那两个值分别
// 决定"放不放行"与"台账记什么"。
func TestClientIPResolverResolvesOncePerRequest(t *testing.T) {
	setTrustedProxiesEnv(t, "private")
	gin.SetMode(gin.TestMode)
	router := gin.New()
	require.NoError(t, ConfigureTrustedProxies(router))
	t.Cleanup(func() { common.SetClientIPPolicy(nil) })
	router.Use(ClientIPResolver())

	var first, second string
	router.GET("/client-ip", func(c *gin.Context) {
		first = common.ClientIP(c)
		// 模拟链路后段有人改了转发头(压缩、重写、代理中间件都干过这种事)。
		c.Request.Header.Set("X-Forwarded-For", "198.51.100.99")
		second = common.ClientIP(c)
		c.String(http.StatusOK, second)
	})

	assert.Equal(t, "203.0.113.10", requestClientIP(router, "172.20.0.2:12345", "203.0.113.10"))
	assert.Equal(t, "203.0.113.10", first)
	assert.Equal(t, "203.0.113.10", second,
		"同一请求内两次取值必须相同,否则「放不放行」与「台账记什么」会依据两个不同的地址")
}
