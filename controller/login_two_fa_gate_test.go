package controller

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/i18n"
	"github.com/QuantumNous/new-api/model"

	"github.com/gin-gonic/gin"
	"github.com/glebarez/sqlite"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

// 两步验证的闸门原先内联在密码登录处理器里,于是 OAuth / 微信 / Telegram 三条
// 通道签发会话时完全不看它:一个开了两步验证又绑了第三方账号的人,攻击者拿下
// 那个第三方账号就能直接登进来,一次动态码都不用输。闸门现在挪到了 setupLogin
// —— 除 passkey 外每条主登录通道的共同出口。
//
// 这份用例钉的就是"每条通道都被挡住",以及三条不能连带破坏的性质:没开两步
// 验证的人照常登录、passkey 不被要求第二因子、挑战里记得住原始通道名。
func setupTwoFAGateTest(t *testing.T) *gorm.DB {
	t.Helper()
	// 挑战应答里的文案走 i18n,没初始化 bundle 的话 i18n.T 直接 panic。
	require.NoError(t, i18n.Init())
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	require.NoError(t, err)
	// 登录成功那一条路径会写登录审计,LOG_DB 不接就在 createLog 里空指针。
	require.NoError(t, db.AutoMigrate(&model.User{}, &model.UserSession{}, &model.AuthFlow{}, &model.TwoFA{}, &model.Log{}))

	previousDB, previousLogDB, previousRedis := model.DB, model.LOG_DB, common.RedisEnabled
	previousActive := common.UserSessionActiveLimit
	model.DB, model.LOG_DB = db, db
	common.RedisEnabled = false
	common.UserSessionActiveLimit = 10
	t.Cleanup(func() {
		model.DB, model.LOG_DB = previousDB, previousLogDB
		common.RedisEnabled = previousRedis
		common.UserSessionActiveLimit = previousActive
	})
	return db
}

func newTwoFAGateUser(t *testing.T, db *gorm.DB, username string, twoFAEnabled bool) *model.User {
	t.Helper()
	user := &model.User{
		Username: username, Password: "unused", Role: common.RoleCommonUser,
		Status: common.UserStatusEnabled, Group: "default", AuthVersion: 1,
	}
	require.NoError(t, db.Create(user).Error)
	if twoFAEnabled {
		require.NoError(t, db.Create(&model.TwoFA{
			UserId: user.Id, Secret: "JBSWY3DPEHPK3PXP", IsEnabled: true,
		}).Error)
	}
	return user
}

// loginThrough 让 user 走一遍 route 代表的那条登录通道,返回应答。
// route 用的是 gin 的路由模板,因为 loginMethodFromContext 就是按它推导通道名的。
func loginThrough(t *testing.T, route, path string, run func(*gin.Context)) *httptest.ResponseRecorder {
	t.Helper()
	gin.SetMode(gin.TestMode)
	recorder := httptest.NewRecorder()
	router := gin.New()
	router.GET(route, func(c *gin.Context) { run(c) })
	router.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, path, nil))
	return recorder
}

func TestTwoFAGateChallengesEveryNonPasskeyLoginTransport(t *testing.T) {
	for _, tc := range []struct {
		name       string
		route      string
		path       string
		wantMethod string
	}{
		{"password", "/api/user/login", "/api/user/login", "password"},
		{"oauth", "/api/oauth/:provider", "/api/oauth/github", "oauth:github"},
		{"wechat", "/api/oauth/wechat", "/api/oauth/wechat", "wechat"},
		{"telegram", "/api/oauth/telegram/login", "/api/oauth/telegram/login", "telegram"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			db := setupTwoFAGateTest(t)
			user := newTwoFAGateUser(t, db, "gate-"+tc.name, true)

			recorder := loginThrough(t, tc.route, tc.path, func(c *gin.Context) {
				setupLogin(user, c)
			})

			require.Equal(t, http.StatusOK, recorder.Code)
			var response struct {
				Success bool `json:"success"`
				Data    struct {
					Require2FA  bool   `json:"require_2fa"`
					FlowToken   string `json:"flow_token"`
					AccessToken string `json:"access_token"`
				} `json:"data"`
			}
			require.NoError(t, common.Unmarshal(recorder.Body.Bytes(), &response))
			assert.True(t, response.Success)
			assert.True(t, response.Data.Require2FA, "%s 必须先过两步验证", tc.name)
			assert.NotEmpty(t, response.Data.FlowToken)
			assert.Empty(t, response.Data.AccessToken, "被挑战时不得同时签发会话")

			var sessions int64
			require.NoError(t, db.Model(&model.UserSession{}).Where("user_id = ?", user.Id).Count(&sessions).Error)
			assert.Zero(t, sessions, "被挑战时不得留下会话")

			// 挑战里必须记住原始通道,否则第二步之后的登录审计会把 OAuth 登录
			// 记成 "2fa",出事时查不出人是从哪条路进来的。
			var flow model.AuthFlow
			require.NoError(t, db.Where("purpose = ? AND user_id = ?", model.AuthFlowPurposeTwoFALogin, user.Id).First(&flow).Error)
			var payload twoFALoginFlowPayload
			require.NoError(t, common.UnmarshalJsonStr(flow.Payload, &payload))
			assert.Equal(t, tc.wantMethod, payload.Method)
			assert.Equal(t, user.AuthVersion, payload.AuthVersion, "AuthVersion 必须是库里的当前值,否则第二步会判成会话过期")
		})
	}
}

func TestTwoFAGateLeavesUnenrolledAndPasskeyLoginsAlone(t *testing.T) {
	t.Run("没开两步验证的人照常拿到会话", func(t *testing.T) {
		db := setupTwoFAGateTest(t)
		user := newTwoFAGateUser(t, db, "gate-no-2fa", false)

		recorder := loginThrough(t, "/api/oauth/:provider", "/api/oauth/github", func(c *gin.Context) {
			setupLogin(user, c)
		})

		require.Equal(t, http.StatusOK, recorder.Code)
		var response struct {
			Data struct {
				Require2FA  bool   `json:"require_2fa"`
				AccessToken string `json:"access_token"`
			} `json:"data"`
		}
		require.NoError(t, common.Unmarshal(recorder.Body.Bytes(), &response))
		assert.False(t, response.Data.Require2FA)
		assert.NotEmpty(t, response.Data.AccessToken)
	})

	t.Run("passkey 登录不被要求第二因子", func(t *testing.T) {
		db := setupTwoFAGateTest(t)
		user := newTwoFAGateUser(t, db, "gate-passkey", true)

		// passkey 的处理器刻意绕开 setupLogin 直接签发会话:它本身就是一个强
		// 因子,再要一次动态码等于把两个因子串起来,只用 passkey 的人会登不进去。
		recorder := loginThrough(t, "/api/user/passkey/login/finish", "/api/user/passkey/login/finish", func(c *gin.Context) {
			setupLoginAtAuthVersion(user, 0, c)
		})

		require.Equal(t, http.StatusOK, recorder.Code)
		var response struct {
			Data struct {
				Require2FA  bool   `json:"require_2fa"`
				AccessToken string `json:"access_token"`
			} `json:"data"`
		}
		require.NoError(t, common.Unmarshal(recorder.Body.Bytes(), &response))
		assert.False(t, response.Data.Require2FA)
		assert.NotEmpty(t, response.Data.AccessToken)
	})
}
