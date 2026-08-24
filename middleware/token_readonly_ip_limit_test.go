package middleware

import (
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/i18n"
	"github.com/QuantumNous/new-api/model"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// token_readonly_ip_limit_test.go —— 令牌的 IP 白名单必须覆盖只读链。
//
// TokenAuthReadOnly 的函数头只声明了「不检查令牌状态、过期时间和额度」,
// IP 限制不在豁免之列 —— 而它原先一道都没判:一把已泄漏、随后被用户用
// allow_ips 绑死的 key,仍能从任意 IP 通过 GET /api/usage/token/ 与
// GET /api/log/token 读到令牌名、总额度/已用/剩余、模型限制,以及逐条调用日志。
// 后者还带着**合法调用方的真实来源 IP**(等于把白名单里有哪些地址交出去)
// 与账号登录名。IP 绑定是用户为「密钥泄漏后限制损失」而设的唯一自助手段,
// 只覆盖 relay 等于用户以为绑死了、其实没有。

func TestTokenAuthReadOnlyEnforcesTheTokenIpWhitelist(t *testing.T) {
	setupDashboardAuthMiddlewareTest(t)
	require.NoError(t, model.DB.AutoMigrate(&model.Token{}))
	model.InitCol()
	require.NoError(t, i18n.Init())

	owner := createRestrictedProbeUser(t, "ip-limit-owner", "ip-limit-owner-pat",
		common.RoleCommonUser, common.UserStatusEnabled)
	const limitedKey = "iplimitedkey"
	const openKey = "ipopenkey"
	require.NoError(t, model.DB.Create(&model.Token{
		UserId: owner.Id, Key: limitedKey, Name: "bound",
		Status: common.TokenStatusEnabled, ExpiredTime: -1, UnlimitedQuota: true,
		AllowIps: strPtr("203.0.113.0/32"),
	}).Error)
	require.NoError(t, model.DB.Create(&model.Token{
		UserId: owner.Id, Key: openKey, Name: "open",
		Status: common.TokenStatusEnabled, ExpiredTime: -1, UnlimitedQuota: true,
	}).Error)

	// 直接改 RemoteAddr,不经转发头 —— common.ClientIP 在未装载策略时返回的
	// 就是直连对端,与生产上「用户从别的 IP 打过来」等价。
	prevProxies := os.Getenv("TRUSTED_PROXIES")
	t.Cleanup(func() { _ = os.Setenv("TRUSTED_PROXIES", prevProxies) })

	gin.SetMode(gin.TestMode)
	engine := gin.New()
	engine.GET("/api/usage/token/", TokenAuthReadOnly(),
		func(c *gin.Context) { c.JSON(http.StatusOK, gin.H{"ok": true}) })

	call := func(key, clientIP string) *httptest.ResponseRecorder {
		req := httptest.NewRequest(http.MethodGet, "/api/usage/token/", nil)
		req.Header.Set("Authorization", "Bearer sk-"+key)
		req.RemoteAddr = clientIP + ":54321"
		res := httptest.NewRecorder()
		engine.ServeHTTP(res, req)
		return res
	}

	blocked := call(limitedKey, "198.51.100.7")
	assert.Equal(t, http.StatusForbidden, blocked.Code,
		"IP 不在白名单里的只读查询必须被挡,否则被绑死的 key 仍能持续泄露用量与调用历史")
	assert.Contains(t, blocked.Body.String(),
		i18n.Translate(i18n.LangEn, i18n.MsgTokenIpNotAllowed))
	assert.NotContains(t, blocked.Body.String(), `"ok":true`)

	allowed := call(limitedKey, "203.0.113.0")
	assert.Equal(t, http.StatusOK, allowed.Code, "白名单内的 IP 必须照常放行")

	// 没配白名单的令牌一个字都不受影响 —— 绝大多数令牌是这一类。
	unrestricted := call(openKey, "198.51.100.7")
	assert.Equal(t, http.StatusOK, unrestricted.Code)
}

// authHelper 必须在 status / role 两道判据**之前**记下「凭据验过了的这个人」。
//
// role 不足时下面直接 AbortWithStatusJSON,setDashboardAuthContext 永远不会执行,
// 于是请求台账把一次已登录账号的越权探测记成与真匿名扫描完全同形的一行。
// 这条从源码顺序上钉死接线 —— 只断言"函数里出现过这个调用"会在它被挪到
// 判据之后时照样为真。
func TestAuthHelperNotesTheIdentityBeforeTheRoleGate(t *testing.T) {
	raw, err := os.ReadFile("auth.go")
	require.NoError(t, err)
	src := string(raw)

	start := strings.Index(src, "func authHelper(c *gin.Context, minRole int) {")
	require.GreaterOrEqual(t, start, 0, "authHelper 不见了")
	end := strings.Index(src[start:], "\nfunc ")
	require.Greater(t, end, 0)
	body := src[start : start+end]

	noteAt := strings.Index(body, "noteAuthenticatedIdentity(c, user")
	require.GreaterOrEqual(t, noteAt, 0,
		"authHelper 必须记下凭据验过了的那个人,否则 403 分支上的台账只剩一个可伪造的 IP")

	statusAt := strings.Index(body, "admitRestrictedUser(c, user.Id, minRole)")
	roleAt := strings.Index(body, "AUTH_INSUFFICIENT_PRIVILEGE")
	require.GreaterOrEqual(t, statusAt, 0)
	require.GreaterOrEqual(t, roleAt, 0)

	assert.Less(t, noteAt, statusAt, "必须排在 status 判据之前")
	assert.Less(t, noteAt, roleAt, "必须排在 role 判据之前 —— 排在后面等于永远不执行")
}

func strPtr(s string) *string { return &s }
