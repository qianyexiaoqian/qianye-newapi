package middleware

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/i18n"
	"github.com/QuantumNous/new-api/model"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// token_owner_deleted_test.go —— 令牌所属账号被软删除之后的拒绝形状。
//
// 两条软删除路径(用户自助 DELETE /api/user/self、管理端 POST /api/user/manage
// action=delete)都只 tx.Delete(user),**完全不碰 tokens 表**,也没有任何后台
// 任务会去清理;只有 DELETE /api/user/:id 的硬删除才会连带 Unscoped 删令牌。
// 所以"账号没了、令牌还活着、脚本还在调"是一个长期存在的稳定状态,而不是异常。
//
// 它一直是 fail-closed 的(请求确实被拒、不计费),错的是**表达**:
// 500 + "Database error" 让这类高频重试在监控里与真实数据库故障同色。
// 会话链早就回 401(ValidateAccessToken 命中软删除作用域后返回 nil,nil),
// 令牌链在这里与它对齐。

func TestSoftDeletedOwnerTokenIsRejectedAsUnauthorizedNotDatabaseError(t *testing.T) {
	setupDashboardAuthMiddlewareTest(t)
	require.NoError(t, model.DB.AutoMigrate(&model.Token{}))
	// 换 DB 句柄的测试必须自己初始化方言列名,否则 GetTokenByKey 的裸 SQL 片段是空串。
	model.InitCol()

	deleted := createRestrictedProbeUser(t, "owner-gone", "owner-gone-pat",
		common.RoleCommonUser, common.UserStatusEnabled)
	alive := createRestrictedProbeUser(t, "owner-alive", "owner-alive-pat",
		common.RoleCommonUser, common.UserStatusEnabled)
	for _, seed := range []struct {
		key    string
		userID int
	}{
		{"goneownerkey", deleted.Id},
		{"aliveownerkey", alive.Id},
	} {
		require.NoError(t, model.DB.Create(&model.Token{
			UserId: seed.userID, Key: seed.key, Name: seed.key,
			Status: common.TokenStatusEnabled, ExpiredTime: -1, UnlimitedQuota: true,
		}).Error)
	}
	// 走 GORM 的软删除,与生产上 user.Delete() 完全同形:users.deleted_at 落值,
	// tokens 行一个字节都不动。
	require.NoError(t, model.DB.Delete(&model.User{Id: deleted.Id}).Error)
	var survivingTokens int64
	require.NoError(t, model.DB.Model(&model.Token{}).
		Where("user_id = ?", deleted.Id).Count(&survivingTokens).Error)
	require.EqualValues(t, 1, survivingTokens, "软删除不清理 tokens —— 这正是本用例的前提")

	gin.SetMode(gin.TestMode)
	engine := gin.New()
	ok := func(c *gin.Context) { c.JSON(http.StatusOK, gin.H{"ok": true}) }
	engine.POST("/v1/chat/completions", TokenAuth(), ok)
	engine.GET("/api/usage/token/", TokenAuthReadOnly(), ok)

	require.NoError(t, i18n.Init())
	ownerMissing := i18n.Translate(i18n.LangEn, i18n.MsgTokenOwnerMissing)
	require.NotEmpty(t, ownerMissing)
	require.NotEqual(t, i18n.MsgTokenOwnerMissing, ownerMissing,
		"三份 locale 里必须真有这条 key,否则回落成 key 名本身")
	databaseError := i18n.Translate(i18n.LangEn, i18n.MsgDatabaseError)

	for _, tc := range []struct {
		name       string
		method     string
		url        string
		key        string
		wantStatus int
	}{
		{"relay 链:账号已删", http.MethodPost, "/v1/chat/completions", "goneownerkey", http.StatusUnauthorized},
		{"只读令牌链:账号已删", http.MethodGet, "/api/usage/token/", "goneownerkey", http.StatusUnauthorized},
		{"relay 链:账号还在", http.MethodPost, "/v1/chat/completions", "aliveownerkey", http.StatusOK},
		{"只读令牌链:账号还在", http.MethodGet, "/api/usage/token/", "aliveownerkey", http.StatusOK},
	} {
		t.Run(tc.name, func(t *testing.T) {
			request := httptest.NewRequest(tc.method, tc.url, nil)
			request.Header.Set("Authorization", "Bearer "+tc.key)
			request.Header.Set("Accept-Language", "en")
			response := httptest.NewRecorder()
			engine.ServeHTTP(response, request)

			require.Equal(t, tc.wantStatus, response.Code, response.Body.String())
			if tc.wantStatus != http.StatusUnauthorized {
				return
			}
			body := response.Body.String()
			assert.Contains(t, body, ownerMissing,
				"必须明说是账号没了,否则运维只能去查数据库")
			assert.NotContains(t, body, databaseError,
				"这不是数据库故障 —— 说成故障会把真故障的告警淹掉")
		})
	}
}
