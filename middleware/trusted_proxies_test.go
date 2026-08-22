package middleware

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

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

func newClientIPRouter() *gin.Engine {
	router := gin.New()
	router.GET("/client-ip", func(c *gin.Context) {
		c.String(http.StatusOK, c.ClientIP())
	})
	return router
}

func TestConfigureTrustedProxiesPrivateTrustsLoopbackAndPrivateNetworks(t *testing.T) {
	gin.SetMode(gin.TestMode)
	t.Setenv("TRUSTED_PROXIES", "private")
	router := newClientIPRouter()
	require.NoError(t, ConfigureTrustedProxies(router))

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
	gin.SetMode(gin.TestMode)
	t.Setenv("TRUSTED_PROXIES", " PriVate ")
	router := newClientIPRouter()
	require.NoError(t, ConfigureTrustedProxies(router))

	clientIP := requestClientIP(router, "198.51.100.10:12345", "203.0.113.10")
	assert.Equal(t, "198.51.100.10", clientIP, "a public peer must not make a spoofed X-Forwarded-For authoritative")
}

func TestConfigureTrustedProxiesPrivateStopsAtPublicClientInForwardedChain(t *testing.T) {
	gin.SetMode(gin.TestMode)
	t.Setenv("TRUSTED_PROXIES", "private")
	router := newClientIPRouter()
	require.NoError(t, ConfigureTrustedProxies(router))

	clientIP := requestClientIP(router, "172.20.0.2:12345", "192.0.2.99, 203.0.113.10")
	assert.Equal(t, "203.0.113.10", clientIP, "the first public hop from the trusted proxy must win over a client-supplied prefix")
}

func TestConfigureTrustedProxiesNoneDisablesForwardedHeaders(t *testing.T) {
	gin.SetMode(gin.TestMode)
	t.Setenv("TRUSTED_PROXIES", " NoNe ")
	router := newClientIPRouter()
	require.NoError(t, ConfigureTrustedProxies(router))

	clientIP := requestClientIP(router, "127.0.0.1:12345", "203.0.113.10")
	assert.Equal(t, "127.0.0.1", clientIP)
}

func TestConfigureTrustedProxiesAcceptsTrimmedIPsAndCIDRs(t *testing.T) {
	gin.SetMode(gin.TestMode)
	t.Setenv("TRUSTED_PROXIES", " 192.0.2.0/24, 198.51.100.30 ")
	router := newClientIPRouter()
	require.NoError(t, ConfigureTrustedProxies(router))

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
			t.Setenv("TRUSTED_PROXIES", testCase.value)
			router := newClientIPRouter()
			assert.Error(t, ConfigureTrustedProxies(router))
		})
	}
}

// TestConfigureTrustedProxiesUnsetTrustsNoProxy 是这一轮的 blocker 回归。
//
// ClientIP() 不是日志字段,它是两处**安全判据**的取值来源:令牌的 allow_ips
// (用户在密钥泄漏之后唯一的自助止损手段,middleware/auth.go 的 TokenAuth 与
// TokenAuthReadOnly 都直接拿它比对)与全部按 IP 计的限流(桶键就是它)。
//
// 未配置时的旧默认是「信任回环 + 全部 RFC1918 + fc00::/7」。只要调用方的**直连
// 对端**落在这些网段里(容器网段、K8s Pod 网段、同一 VPC 的其他主机、以及本机),
// 它就能自带一个 X-Forwarded-For 把 ClientIP() 指成任意值。备份库实测:
// allow_ips=203.0.113.5/32 的令牌从 127.0.0.1 请求 /v1/models,不带 XFF 403、
// 带上 `X-Forwarded-For: 203.0.113.5` 变 200;GlobalAPIRateLimit 用轮换的
// 10.20.x.x 打 915 次一条 429 都没有。
//
// 问题不在于这条默认宽松,而在于它**默默地**把一个安全判据的取值交给了调用方,
// 而部署者不知道自己需要做任何事。fail-closed 的表现相反且可见:反代后面的部署
// 会立刻看到所有人的 IP 都是反代的 IP,当天就有人去配 TRUSTED_PROXIES。
func TestConfigureTrustedProxiesUnsetTrustsNoProxy(t *testing.T) {
	gin.SetMode(gin.TestMode)
	for _, value := range []string{"", " \t "} {
		t.Setenv("TRUSTED_PROXIES", value)
		router := newClientIPRouter()
		require.NoError(t, ConfigureTrustedProxies(router))

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
				"未配置 TRUSTED_PROXIES 时,来自 %s 的 X-Forwarded-For 绝不能变成 ClientIP() —— "+
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
	gin.SetMode(gin.TestMode)

	t.Setenv("TRUSTED_PROXIES", "private")
	optedIn := newClientIPRouter()
	require.NoError(t, ConfigureTrustedProxies(optedIn))
	assert.Equal(t, "203.0.113.10",
		requestClientIP(optedIn, "172.20.0.2:12345", "203.0.113.10"),
		"显式选了 private,私网对端的 XFF 就该作数")

	t.Setenv("TRUSTED_PROXIES", "")
	unset := newClientIPRouter()
	require.NoError(t, ConfigureTrustedProxies(unset))
	assert.Equal(t, "172.20.0.2",
		requestClientIP(unset, "172.20.0.2:12345", "203.0.113.10"),
		"没选就没有 —— 这两条断言的差别就是这次改动的全部内容")
}
