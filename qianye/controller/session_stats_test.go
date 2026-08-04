package controller

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/model"

	"github.com/gin-gonic/gin"
	"github.com/glebarez/sqlite"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

// session_stats_test.go —— 保护「已到期会话数」这个数字的三条口径。
//
// 这个端点存在的全部理由就是上游列表接口给不出「已到期」(它的 WHERE 带
// expires_at > now)。所以测试必须证明的第一件事是:**已到期的行真的被数到了**。
// 只断言接口返回 200 或只断言 active 正确,都测不到它为什么存在。

func useMainDBForSessions(t *testing.T) *gorm.DB {
	t.Helper()
	gdb, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	require.NoError(t, err)
	sqlDB, err := gdb.DB()
	require.NoError(t, err)
	sqlDB.SetMaxOpenConns(1)
	require.NoError(t, gdb.AutoMigrate(&model.User{}, &model.UserSession{}))

	prev := model.DB
	model.DB = gdb
	t.Cleanup(func() {
		model.DB = prev
		_ = sqlDB.Close()
	})
	return gdb
}

// seedSession 插一条会话。offset 是相对当前时刻的秒数:正数=未到期,负数=已到期。
func seedSession(t *testing.T, gdb *gorm.DB, sid string, userId int, authVer int64, status string, offset int64) {
	t.Helper()
	now := common.GetTimestamp()
	require.NoError(t, gdb.Create(&model.UserSession{
		SID:             sid,
		UserID:          userId,
		UserAuthVersion: authVer,
		Status:          status,
		RefreshHash:     "h-" + sid,
		ExpiresAt:       now + offset,
		CreatedAt:       now,
		LastActiveAt:    now,
	}).Error)
}

func callSessionStats(t *testing.T, userId int) (int, map[string]any) {
	t.Helper()
	gin.SetMode(gin.TestMode)
	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	c.Request = httptest.NewRequest(http.MethodGet, "/api/qy/session-stats", nil)
	c.Set("id", userId)
	UserSessionStats(c)

	var body struct {
		Success bool           `json:"success"`
		Data    map[string]any `json:"data"`
	}
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &body))
	return rec.Code, body.Data
}

func TestUserSessionStatsCountsLapsedSessionsUpstreamNeverReturns(t *testing.T) {
	gdb := useMainDBForSessions(t)
	require.NoError(t, gdb.Create(&model.User{
		Id: 7, Username: "u7", AuthVersion: 3, AffCode: "a7",
	}).Error)

	// 两条有效、三条已到期。已到期这三条正是 model.ListActiveUserSessions
	// 结构性不会下发的那一类 —— 前端拿列表算这个数恒为 0,所以它们必须由本端点数出来。
	seedSession(t, gdb, "ok-1", 7, 3, model.UserSessionStatusActive, +3600)
	seedSession(t, gdb, "ok-2", 7, 3, model.UserSessionStatusActive, +86400)
	seedSession(t, gdb, "gone-1", 7, 3, model.UserSessionStatusActive, -1)
	seedSession(t, gdb, "gone-2", 7, 3, model.UserSessionStatusActive, -3600)
	seedSession(t, gdb, "gone-3", 7, 3, model.UserSessionStatusActive, -864000)

	code, data := callSessionStats(t, 7)
	require.Equal(t, http.StatusOK, code)
	assert.EqualValues(t, 2, data["active"])
	assert.EqualValues(t, 3, data["expired"],
		"已到期数是这个端点唯一的存在理由 —— 它为 0 说明又退回了上游列表接口的口径")
}

func TestUserSessionStatsExcludesRevokedAndStaleAuthVersion(t *testing.T) {
	gdb := useMainDBForSessions(t)
	require.NoError(t, gdb.Create(&model.User{
		Id: 8, Username: "u8", AuthVersion: 5, AffCode: "a8",
	}).Error)

	seedSession(t, gdb, "live", 8, 5, model.UserSessionStatusActive, +3600)
	seedSession(t, gdb, "lapsed", 8, 5, model.UserSessionStatusActive, -60)

	// 被吊销的不是「到期」。主动登出一台设备之后把它报成"已到期",
	// 会让用户以为是会话自然过期,两种性质混为一谈。
	seedSession(t, gdb, "revoked-live", 8, 5, model.UserSessionStatusRevoked, +3600)
	seedSession(t, gdb, "revoked-lapsed", 8, 5, model.UserSessionStatusRevoked, -60)
	seedSession(t, gdb, "revoking", 8, 5, model.UserSessionStatusRevoking, +3600)

	// 改密码之前的历史会话:status 还是 active、行还在库里,但 auth_version 已经旧了。
	// 漏掉这一维,"已到期"会把用户历史上所有登录都算进来,数字凭空变大。
	seedSession(t, gdb, "stale-live", 8, 4, model.UserSessionStatusActive, +3600)
	seedSession(t, gdb, "stale-lapsed", 8, 4, model.UserSessionStatusActive, -60)

	// 别人的会话。
	seedSession(t, gdb, "other", 9, 1, model.UserSessionStatusActive, -60)

	code, data := callSessionStats(t, 8)
	require.Equal(t, http.StatusOK, code)
	assert.EqualValues(t, 1, data["active"])
	assert.EqualValues(t, 1, data["expired"])
}

func TestUserSessionStatsIsZeroForAUserWithNoSessions(t *testing.T) {
	gdb := useMainDBForSessions(t)
	require.NoError(t, gdb.Create(&model.User{
		Id: 10, Username: "u10", AuthVersion: 1, AffCode: "a10",
	}).Error)

	code, data := callSessionStats(t, 10)
	require.Equal(t, http.StatusOK, code)
	// 显式的 0 而不是缺字段:前端拿 undefined 会渲染成空白,读起来像"没读到"。
	assert.EqualValues(t, 0, data["active"])
	assert.EqualValues(t, 0, data["expired"])
}
