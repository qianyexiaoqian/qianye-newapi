package middleware

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/model"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type restrictedAuthResponse struct {
	Success    bool   `json:"success"`
	Code       string `json:"code"`
	Restricted bool   `json:"restricted"`
	Message    string `json:"message"`
}

// restrictedProbeRoute 是一条被探测的真实路径。auth 决定它挂在会话链的哪一档,
// 与线上路由表一致(白名单里的全部是 UserAuth)。
type restrictedProbeRoute struct {
	name       string
	method     string
	pattern    string
	requestURL string
	auth       func() func(c *gin.Context)
	// wantAllowed 是**手写**的期望,刻意不调用 RestrictedUserRouteAllowed ——
	// 用被测函数去算期望值,把白名单判据短路掉时这张表会跟着一起变绿。
	wantAllowed bool
}

// restrictedSessionProbes 覆盖白名单内、白名单外、参数段、以及「同路径不同方法」。
// 路径全部逐字抄自真实路由树(router/restricted_user_routes_test.go 会核对
// 白名单条目确实存在于那棵树上)。
var restrictedSessionProbes = []restrictedProbeRoute{
	// ── 白名单内 ────────────────────────────────────────────────
	{"self", http.MethodGet, "/api/user/self", "/api/user/self", UserAuth, true},
	{"ticket config", http.MethodGet, "/api/qy/ticket/config", "/api/qy/ticket/config", UserAuth, true},
	{"ticket list", http.MethodGet, "/api/qy/ticket/list", "/api/qy/ticket/list", UserAuth, true},
	{"ticket detail", http.MethodGet, "/api/qy/ticket/:no", "/api/qy/ticket/TK20260810001", UserAuth, true},
	{"ticket create", http.MethodPost, "/api/qy/ticket", "/api/qy/ticket", UserAuth, true},
	{"ticket reply", http.MethodPost, "/api/qy/ticket/:no/reply", "/api/qy/ticket/TK20260810001/reply", UserAuth, true},
	{"ticket close", http.MethodPost, "/api/qy/ticket/:no/close", "/api/qy/ticket/TK20260810001/close", UserAuth, true},
	{"ticket upload", http.MethodPost, "/api/qy/ticket/images", "/api/qy/ticket/images", UserAuth, true},
	{"ticket image read", http.MethodGet, "/api/qy/ticket/images/:ref", "/api/qy/ticket/images/abc", UserAuth, true},
	{"ticket image discard", http.MethodDelete, "/api/qy/ticket/images/:ref", "/api/qy/ticket/images/abc", UserAuth, true},
	{"violation summary", http.MethodGet, "/api/qy/violation/my-summary", "/api/qy/violation/my-summary", UserAuth, true},
	{"violation records", http.MethodGet, "/api/qy/violation/my-records", "/api/qy/violation/my-records", UserAuth, true},
	{"violation appeal", http.MethodPost, "/api/qy/violation/appeals", "/api/qy/violation/appeals", UserAuth, true},

	// ── 白名单外:同一路径的另一个方法 ────────────────────────────
	// 白名单的 key 是 method+path,GET /api/user/self 放行不等于 PUT 也放行。
	{"profile update", http.MethodPut, "/api/user/self", "/api/user/self", UserAuth, false},
	{"account delete", http.MethodDelete, "/api/user/self", "/api/user/self", UserAuth, false},

	// ── 白名单外:钱 / key / 订阅 / 抽奖 / 凭据 ──────────────────
	{"create token", http.MethodPost, "/api/token/", "/api/token/", UserAuth, false},
	{"list tokens", http.MethodGet, "/api/token/", "/api/token/", UserAuth, false},
	{"transfer", http.MethodPost, "/api/qy/transfer", "/api/qy/transfer", UserAuth, false},
	{"withdraw", http.MethodPost, "/api/qy/withdraw", "/api/qy/withdraw", UserAuth, false},
	{"aff transfer", http.MethodPost, "/api/user/aff_transfer", "/api/user/aff_transfer", UserAuth, false},
	{"topup", http.MethodPost, "/api/user/topup", "/api/user/topup", UserAuth, false},
	{"checkin", http.MethodPost, "/api/user/checkin", "/api/user/checkin", UserAuth, false},
	{"lottery entry", http.MethodPost, "/api/qy/lottery/activities/:act_no/entries", "/api/qy/lottery/activities/A1/entries", UserAuth, false},
	{"subscription pay", http.MethodPost, "/api/subscription/balance/pay", "/api/subscription/balance/pay", UserAuth, false},
	{"pay password", http.MethodPut, "/api/qy/pay-password", "/api/qy/pay-password", UserAuth, false},
	{"pat mint", http.MethodGet, "/api/user/token", "/api/user/token", UserAuth, false},
	{"self logs", http.MethodGet, "/api/log/self", "/api/log/self", UserAuth, false},
	{"playground relay", http.MethodPost, "/pg/chat/completions", "/pg/chat/completions", UserAuth, false},
}

func buildRestrictedProbeEngine() *gin.Engine {
	gin.SetMode(gin.TestMode)
	engine := gin.New()
	for _, probe := range restrictedSessionProbes {
		engine.Handle(probe.method, probe.pattern, probe.auth(), func(c *gin.Context) {
			c.JSON(http.StatusOK, gin.H{"restricted": IsRestrictedUser(c)})
		})
	}
	return engine
}

func doRestrictedProbe(t *testing.T, engine *gin.Engine, probe restrictedProbeRoute, pat string) (*httptest.ResponseRecorder, restrictedAuthResponse) {
	t.Helper()
	request := httptest.NewRequest(probe.method, probe.requestURL, nil)
	request.Header.Set("Authorization", "Bearer "+pat)
	response := httptest.NewRecorder()
	engine.ServeHTTP(response, request)
	var body restrictedAuthResponse
	if response.Body.Len() > 0 {
		require.NoError(t, common.Unmarshal(response.Body.Bytes(), &body))
	}
	return response, body
}

func createRestrictedProbeUser(t *testing.T, username, pat string, role, status int) *model.User {
	t.Helper()
	user := &model.User{
		Username: username, Password: "password-placeholder", Role: role,
		Status: status, Group: "default", AccessToken: &pat, AuthVersion: 1,
		AffCode: "restricted-aff-" + username,
	}
	require.NoError(t, model.DB.Create(user).Error)
	return user
}

// 受限账号在会话鉴权链上只能到达白名单内的接口,其余一律 403 AUTH_USER_RESTRICTED。
func TestRestrictedUserReachesOnlyWhitelistedSessionRoutes(t *testing.T) {
	setupDashboardAuthMiddlewareTest(t)
	const pat = "restricted-probe-pat"
	createRestrictedProbeUser(t, "restricted-probe", pat, common.RoleCommonUser, common.UserStatusDisabled)
	engine := buildRestrictedProbeEngine()

	for _, probe := range restrictedSessionProbes {
		t.Run(probe.name, func(t *testing.T) {
			require.Equal(t, probe.wantAllowed, RestrictedUserRouteAllowed(probe.method, probe.pattern),
				"白名单表与本表的手写期望脱节了")
			response, body := doRestrictedProbe(t, engine, probe, pat)
			if probe.wantAllowed {
				assert.Equal(t, http.StatusOK, response.Code)
				assert.True(t, body.Restricted, "白名单内的 handler 必须能看到受限标记")
				return
			}
			assert.Equal(t, http.StatusForbidden, response.Code,
				"必须是 403 而不是 401:受限账号是登录着的,401 会被前端当成掉线并清空凭据")
			assert.Equal(t, "AUTH_USER_RESTRICTED", body.Code)
			assert.True(t, body.Restricted)
			assert.False(t, body.Success)
		})
	}
}

// 解禁后一切恢复:同一张表、同一批请求,全部 200,且不再带受限标记。
func TestEnabledUserIsUnaffectedByRestrictedWhitelist(t *testing.T) {
	setupDashboardAuthMiddlewareTest(t)
	const pat = "enabled-probe-pat"
	createRestrictedProbeUser(t, "enabled-probe", pat, common.RoleCommonUser, common.UserStatusEnabled)
	engine := buildRestrictedProbeEngine()

	for _, probe := range restrictedSessionProbes {
		t.Run(probe.name, func(t *testing.T) {
			response, body := doRestrictedProbe(t, engine, probe, pat)
			assert.Equal(t, http.StatusOK, response.Code)
			assert.False(t, body.Restricted, "正常账号绝不能被打上受限标记")
		})
	}
}

// 中途禁用:同一个凭据,改一次 users.status 之后下一次请求立刻受限。
// 延迟由 GetUserCache 决定 —— Redis 关闭时它每次回落主库,因此是「下一次请求」。
func TestRestrictedStatusTakesEffectOnNextRequest(t *testing.T) {
	setupDashboardAuthMiddlewareTest(t)
	const pat = "midflight-probe-pat"
	user := createRestrictedProbeUser(t, "midflight-probe", pat, common.RoleCommonUser, common.UserStatusEnabled)
	engine := buildRestrictedProbeEngine()
	probe := restrictedProbeRoute{method: http.MethodPost, pattern: "/api/token/", requestURL: "/api/token/"}

	response, _ := doRestrictedProbe(t, engine, probe, pat)
	require.Equal(t, http.StatusOK, response.Code)

	require.NoError(t, model.DB.Model(&model.User{}).Where("id = ?", user.Id).
		Update("status", common.UserStatusDisabled).Error)

	response, body := doRestrictedProbe(t, engine, probe, pat)
	assert.Equal(t, http.StatusForbidden, response.Code)
	assert.Equal(t, "AUTH_USER_RESTRICTED", body.Code)

	require.NoError(t, model.DB.Model(&model.User{}).Where("id = ?", user.Id).
		Update("status", common.UserStatusEnabled).Error)

	response, _ = doRestrictedProbe(t, engine, probe, pat)
	assert.Equal(t, http.StatusOK, response.Code, "解禁同样是下一次请求即生效")
}

// role 不给受限账号任何旁路:被禁用的管理员在管理端接口上拿到的是
// AUTH_USER_RESTRICTED,而不是 200,也不是「权限不足」。
// 更进一步:即使有人把一条白名单路径挂到 AdminAuth 上,admitRestrictedUser
// 也会因为 minRole > RoleCommonUser 而拒绝。
func TestRestrictedAdminGetsNoRoleBypass(t *testing.T) {
	setupDashboardAuthMiddlewareTest(t)
	const pat = "restricted-admin-pat"
	createRestrictedProbeUser(t, "restricted-admin", pat, common.RoleAdminUser, common.UserStatusDisabled)

	gin.SetMode(gin.TestMode)
	engine := gin.New()
	ok := func(c *gin.Context) { c.JSON(http.StatusOK, gin.H{"restricted": IsRestrictedUser(c)}) }
	engine.POST("/api/user/manage", AdminAuth(), ok)
	engine.GET("/api/qy/admin/health", AdminAuth(), ok)
	// 白名单路径 + 管理档:白名单不该因为路径匹配就放行。
	engine.GET("/api/user/self", AdminAuth(), ok)

	for _, target := range []struct {
		method string
		url    string
	}{
		{http.MethodPost, "/api/user/manage"},
		{http.MethodGet, "/api/qy/admin/health"},
		{http.MethodGet, "/api/user/self"},
	} {
		t.Run(target.method+" "+target.url, func(t *testing.T) {
			response, body := doRestrictedProbe(t, engine,
				restrictedProbeRoute{method: target.method, requestURL: target.url}, pat)
			assert.Equal(t, http.StatusForbidden, response.Code)
			assert.Equal(t, "AUTH_USER_RESTRICTED", body.Code,
				"必须先判 status 再判 role;出现 AUTH_INSUFFICIENT_PRIVILEGE 或 200 都说明顺序错了")
		})
	}
}

// TryUserAuth 这条宽松链过去根本不判 status。它必须与严格链同一个判据:
// 带受限身份 → 403;完全匿名 → 照常放行(否则 /api/pricing 这类页面对未登录
// 访客也会挂掉)。
func TestTryUserAuthDeniesRestrictedIdentityButKeepsAnonymous(t *testing.T) {
	setupDashboardAuthMiddlewareTest(t)
	const restrictedPAT = "tryauth-restricted-pat"
	const enabledPAT = "tryauth-enabled-pat"
	createRestrictedProbeUser(t, "tryauth-restricted", restrictedPAT, common.RoleCommonUser, common.UserStatusDisabled)
	createRestrictedProbeUser(t, "tryauth-enabled", enabledPAT, common.RoleCommonUser, common.UserStatusEnabled)

	gin.SetMode(gin.TestMode)
	engine := gin.New()
	engine.GET("/api/pricing", TryUserAuth(), func(c *gin.Context) {
		c.JSON(http.StatusOK, gin.H{"id": c.GetInt("id")})
	})

	for _, tc := range []struct {
		name       string
		pat        string
		wantStatus int
		wantCode   string
	}{
		{"anonymous", "", http.StatusOK, ""},
		{"enabled", enabledPAT, http.StatusOK, ""},
		{"restricted", restrictedPAT, http.StatusForbidden, "AUTH_USER_RESTRICTED"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			request := httptest.NewRequest(http.MethodGet, "/api/pricing", nil)
			if tc.pat != "" {
				request.Header.Set("Authorization", "Bearer "+tc.pat)
			}
			response := httptest.NewRecorder()
			engine.ServeHTTP(response, request)
			assert.Equal(t, tc.wantStatus, response.Code)
			if tc.wantCode != "" {
				var body restrictedAuthResponse
				require.NoError(t, common.Unmarshal(response.Body.Bytes(), &body))
				assert.Equal(t, tc.wantCode, body.Code)
			}
		})
	}
}

// 白名单判据本身的表驱动:method 与 :param 段都必须逐字匹配。
func TestRestrictedUserRouteAllowedMatchesMethodAndPattern(t *testing.T) {
	for _, tc := range []struct {
		name     string
		method   string
		fullPath string
		want     bool
	}{
		{"ticket detail pattern", http.MethodGet, "/api/qy/ticket/:no", true},
		{"ticket detail wrong param name", http.MethodGet, "/api/qy/ticket/:id", false},
		{"ticket detail concrete url is not a pattern", http.MethodGet, "/api/qy/ticket/TK1", false},
		{"self read", http.MethodGet, "/api/user/self", true},
		{"self write", http.MethodPut, "/api/user/self", false},
		{"ticket create", http.MethodPost, "/api/qy/ticket", true},
		{"ticket list is not create", http.MethodPost, "/api/qy/ticket/list", false},
		{"unmatched route has empty full path", http.MethodGet, "", false},
		{"empty method", "", "/api/user/self", false},
		{"relay", http.MethodPost, "/v1/chat/completions", false},
		{"admin", http.MethodGet, "/api/qy/admin/health", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			assert.Equal(t, tc.want, RestrictedUserRouteAllowed(tc.method, tc.fullPath))
		})
	}
}

// 令牌链与会话链是两套代码,白名单绝不能溢出到令牌链:受限账号的**已有令牌**
// 必须整体失效,否则封号就只剩「不能登管理台」一层皮。
//
// 这里刻意用一条**白名单里有的路径形状**去打 relay(POST /api/qy/ticket 与
// /v1/chat/completions 各来一次):即便路径匹配,令牌链也不查白名单。
func TestRestrictedUserTokensAreDeniedOnRelayChain(t *testing.T) {
	setupDashboardAuthMiddlewareTest(t)
	require.NoError(t, model.DB.AutoMigrate(&model.Token{}))
	// 换 DB 句柄的测试必须自己初始化方言列名,否则 GetTokenByKey 的裸 SQL 片段是空串。
	model.InitCol()
	user := createRestrictedProbeUser(t, "relay-restricted", "relay-restricted-pat", common.RoleCommonUser, common.UserStatusDisabled)
	enabled := createRestrictedProbeUser(t, "relay-enabled", "relay-enabled-pat", common.RoleCommonUser, common.UserStatusEnabled)

	for _, seed := range []struct {
		key    string
		userID int
	}{
		{"restrictedrelaykey", user.Id},
		{"enabledrelaykey", enabled.Id},
	} {
		require.NoError(t, model.DB.Create(&model.Token{
			UserId: seed.userID, Key: seed.key, Name: seed.key,
			Status: common.TokenStatusEnabled, ExpiredTime: -1, UnlimitedQuota: true,
		}).Error)
	}

	gin.SetMode(gin.TestMode)
	engine := gin.New()
	relayOK := func(c *gin.Context) { c.JSON(http.StatusOK, gin.H{"ok": true}) }
	engine.POST("/v1/chat/completions", TokenAuth(), relayOK)
	engine.POST("/api/qy/ticket", TokenAuth(), relayOK)
	engine.GET("/api/usage/token/", TokenAuthReadOnly(), relayOK)

	for _, tc := range []struct {
		name       string
		method     string
		url        string
		key        string
		wantStatus int
	}{
		{"relay with restricted token", http.MethodPost, "/v1/chat/completions", "restrictedrelaykey", http.StatusForbidden},
		{"whitelisted path shape on token chain", http.MethodPost, "/api/qy/ticket", "restrictedrelaykey", http.StatusForbidden},
		{"usage query with restricted token", http.MethodGet, "/api/usage/token/", "restrictedrelaykey", http.StatusForbidden},
		{"relay with enabled token", http.MethodPost, "/v1/chat/completions", "enabledrelaykey", http.StatusOK},
		{"usage query with enabled token", http.MethodGet, "/api/usage/token/", "enabledrelaykey", http.StatusOK},
	} {
		t.Run(tc.name, func(t *testing.T) {
			request := httptest.NewRequest(tc.method, tc.url, nil)
			request.Header.Set("Authorization", "Bearer "+tc.key)
			response := httptest.NewRecorder()
			engine.ServeHTTP(response, request)
			assert.Equal(t, tc.wantStatus, response.Code)
			if tc.wantStatus == http.StatusForbidden {
				assert.NotContains(t, response.Body.String(), "AUTH_USER_RESTRICTED",
					"令牌链的拒绝形状是 OpenAI 错误体,不走会话链的受限码")
			}
		})
	}
}
