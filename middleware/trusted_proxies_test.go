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

// TestConfigureTrustedProxiesUnsetTrustsNoProxy 是这一批的 blocker 回归。
//
// 客户端 IP 不是日志字段,它是四处**安全判据**的取值来源:令牌的 allow_ips
// (用户在密钥泄漏之后唯一的自助止损手段,middleware/auth.go 的 TokenAuth 与
// TokenAuthReadOnly 都直接拿它比对)、全部按 IP 计的限流(桶键就是它)、
// 审计与资金台账、以及风控去重。
//
// 未配置时的旧默认是「信任回环 + 全部 RFC1918 + fc00::/7」。只要调用方的**直连
// 对端**落在这些网段里(容器网段、K8s Pod 网段、同一 VPC 的其他主机、以及本机),
// 它就能自带一个 X-Forwarded-For 把客户端 IP 指成任意值。备份库实测:
// allow_ips=203.0.113.5/32 的令牌从 127.0.0.1 请求 /v1/models,不带 XFF 403、
// 带上 `X-Forwarded-For: 203.0.113.5` 变 200;GlobalAPIRateLimit 用轮换的
// 10.20.x.x 打 915 次一条 429 都没有。
//
// 问题不在于这条默认宽松,而在于它**默默地**把一个安全判据的取值交给了调用方,
// 而部署者不知道自己需要做任何事。fail-closed 的表现相反且可见:反代后面的部署
// 会立刻看到所有人的 IP 都是反代的 IP,而管理端
// GET /api/qy/admin/client-ip 会直接给出该填哪个 CIDR。
func TestConfigureTrustedProxiesUnsetTrustsNoProxy(t *testing.T) {
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
			clientIP := requestClientIP(router, peer, "203.0.113.10")
			assert.NotEqual(t, "203.0.113.10", clientIP,
				"未配置 TRUSTED_PROXIES 时,来自 %s 的 X-Forwarded-For 绝不能变成客户端 IP —— "+
					"那等于让任何能直连到本端口的东西自己决定令牌 IP 白名单与限流桶的取值", peer)
		}
		assert.Equal(t, "127.0.0.1",
			requestClientIP(router, "127.0.0.1:12345", "203.0.113.10"))
		assert.Equal(t, "10.20.30.40",
			requestClientIP(router, "10.20.30.40:12345", "203.0.113.10"))
	}
}

// TestConfigureTrustedProxiesPrivateIsOptInOnly 守「旧行为仍然拿得到,但必须
// 显式要」。
//
// 反代确实在私网里、地址又不固定(容器编排)的部署需要一条一行的出路,否则
// fail-closed 的代价就是逼他们去关掉别的东西。区别只在于:现在这是一个**写在
// 部署配置里的决定**,而不是一条没人知道自己继承了的默认。
func TestConfigureTrustedProxiesPrivateIsOptInOnly(t *testing.T) {
	setTrustedProxiesEnv(t, "private")
	optedIn := newClientIPRouter(t)
	assert.Equal(t, "203.0.113.10",
		requestClientIP(optedIn, "172.20.0.2:12345", "203.0.113.10"),
		"显式选了 private,私网对端的 XFF 就该作数")

	setTrustedProxiesEnv(t, "")
	unset := newClientIPRouter(t)
	assert.Equal(t, "172.20.0.2",
		requestClientIP(unset, "172.20.0.2:12345", "203.0.113.10"),
		"没选就没有 —— 这两条断言的差别就是这次改动的全部内容")
}

// TestClientIPResolverFeedsTheObservationDesk 守 fail-closed 的配套诊断真的接上了。
//
// fail-closed 的代价落在「装在反代后面又从没配过」的站点上:一切照常 200,
// 只是所有人的 IP 都成了反代的地址。观测台是这一档唯一的信号,而它必须由
// **中间件**来喂 —— 挂在业务代码里的话,没调过 ClientIP 的路径就观测不到,
// 那正是最需要被观测到的那些(静态资源、健康检查、被 401 挡掉的探测)。
func TestClientIPResolverFeedsTheObservationDesk(t *testing.T) {
	common.ResetClientIPObservations()
	t.Cleanup(common.ResetClientIPObservations)

	setTrustedProxiesEnv(t, "")
	router := newClientIPRouter(t)

	assert.Equal(t, "172.18.0.5", requestClientIP(router, "172.18.0.5:12345", "203.0.113.10"))
	assert.Equal(t, "172.18.0.5", requestClientIP(router, "172.18.0.5:12346", "203.0.113.11"))
	// 没带转发头的请求不该进观测台:它不是"有反代却没配",它就是直连。
	assert.Equal(t, "198.51.100.4", requestClientIP(router, "198.51.100.4:12345", ""))

	observations, dropped := common.ClientIPObservations()
	require.Len(t, observations, 1)
	assert.Zero(t, dropped)
	assert.Equal(t, "172.18.0.5", observations[0].Peer)
	assert.EqualValues(t, 2, observations[0].Count)
	assert.Equal(t, "172.18.0.5/32", observations[0].Suggestion,
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
