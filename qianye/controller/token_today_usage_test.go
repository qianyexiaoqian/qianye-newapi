package controller

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/qianye/config"

	"github.com/gin-gonic/gin"
	"github.com/glebarez/sqlite"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

// token_today_usage_test.go —— 密钥页「今日消耗」那条端点的**窗口**。
//
// 聚合本身(分组、过滤、上界)由 qianye/modules/commission 的用例守。
// 这里守的是这条端点自己唯一会做错的事:**「今天」是哪一段**。
//
// 它错起来完全没有症状:数字仍然是一个像模像样的金额,只是把凌晨那几笔算到了
// 昨天,而日消费明细算今天。所以用例把日界钉在 UTC+8 上,再放一笔只有在
// UTC+8 口径下才属于"今天"的消费 —— 走 UTC 午夜的实现拿不到它。

// useTodayUsageEnv 加载一份 day_offset_minutes=480 的配置,并接上一个内存日志库。
func useTodayUsageEnv(t *testing.T) *gorm.DB {
	t.Helper()

	yaml := "enabled: true\n" +
		"database:\n  dsn: \"u:p@tcp(127.0.0.1:3306)/qy\"\n" +
		"commission:\n  enabled: true\n  day_offset_minutes: 480\n"
	p := filepath.Join(t.TempDir(), "qianye.yaml")
	require.NoError(t, os.WriteFile(p, []byte(yaml), 0o600))
	t.Setenv(config.EnvConfigPath, p)
	require.NoError(t, config.Load())
	require.Equal(t, 480, config.Get().Commission.DayOffsetMinutes,
		"配置没生效的话下面整条用例都在测 UTC")

	gdb, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	require.NoError(t, err)
	sqlDB, err := gdb.DB()
	require.NoError(t, err)
	sqlDB.SetMaxOpenConns(1) // 内存库按连接隔离
	require.NoError(t, gdb.AutoMigrate(&model.Log{}))

	prev := model.LOG_DB
	model.LOG_DB = gdb
	t.Cleanup(func() {
		model.LOG_DB = prev
		_ = sqlDB.Close()
	})
	return gdb
}

func seedTodayUsageLog(t *testing.T, gdb *gorm.DB, userId, tokenId int, at int64, quota, logType int) {
	t.Helper()
	require.NoError(t, gdb.Create(&model.Log{
		UserId: userId, TokenId: tokenId, CreatedAt: at,
		Type: logType, Quota: quota, ModelName: "qy-test",
	}).Error)
}

func callTokenTodayUsage(t *testing.T, userId int) (int, map[string]any) {
	t.Helper()
	gin.SetMode(gin.TestMode)
	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	c.Request = httptest.NewRequest(http.MethodGet, "/api/qy/token-usage/today", nil)
	c.Set("id", userId)
	UserTokenTodayUsage(c)

	var body struct {
		Success bool           `json:"success"`
		Data    map[string]any `json:"data"`
	}
	require.NoError(t, common.Unmarshal(rec.Body.Bytes(), &body), rec.Body.String())
	require.True(t, body.Success || rec.Code != http.StatusOK, rec.Body.String())
	return rec.Code, body.Data
}

// TestUserTokenTodayUsageWindowFollowsTheConsumeDayline 是这条端点的主用例。
//
// 布置:day_offset_minutes = 480(UTC+8)。取"此刻"所在的 UTC+8 日界,
// 往里放三笔:
//
//	日界 + 1 秒                令牌 91,300     ← 属于今天
//	日界 - 1 秒                令牌 92,700     ← 属于昨天,不能算进来
//	日界 + 1 秒,type=充值      令牌 93,900     ← 不是消费,不能算进来
//
// 关键在第二笔:UTC+8 的日界比 UTC 午夜早 8 小时,所以「日界 - 1 秒」这一刻
// 在**大多数时间点上**仍然落在同一个 UTC 日历日里。走 UTC 午夜的实现会把它
// 算进"今天",而正确的实现不会。
//
// 变异验证:把 handler 里的 commission.ConsumeDayStart(now) 换成
// `now - now%86400`(UTC 午夜)→ day_start 断言红,并且在 UTC 时刻 08:00 之后
// 令牌 92 会冒出来。
func TestUserTokenTodayUsageWindowFollowsTheConsumeDayline(t *testing.T) {
	logDB := useTodayUsageEnv(t)

	const me = 9101
	now := common.GetTimestamp()
	// 与 commission.dayStart 同一算法,但在测试里**独立重算**一遍:
	// 直接调那个导出函数就成了"用被测代码验证被测代码"。
	const offset = int64(480 * 60)
	shifted := now + offset
	wantStart := shifted - shifted%86400 - offset
	wantEnd := wantStart + 86400
	require.LessOrEqual(t, wantStart, now)
	require.Greater(t, wantEnd, now)

	seedTodayUsageLog(t, logDB, me, 91, wantStart+1, 300, model.LogTypeConsume)
	seedTodayUsageLog(t, logDB, me, 92, wantStart-1, 700, model.LogTypeConsume)
	seedTodayUsageLog(t, logDB, me, 93, wantStart+1, 900, model.LogTypeTopup)

	code, data := callTokenTodayUsage(t, me)
	require.Equal(t, http.StatusOK, code)

	assert.EqualValues(t, wantStart, data["day_start"], "「今日」的起点必须落在消费日界上")
	assert.EqualValues(t, wantEnd, data["day_end"])
	assert.EqualValues(t, 480, data["day_offset_minutes"],
		"偏移必须如实下发 —— 界面要靠它把「今日」是哪一段写给用户看")

	usage, ok := data["usage"].(map[string]any)
	require.True(t, ok, "usage 必须是一张以令牌 id 字符串为键的表:%#v", data["usage"])
	assert.Equal(t, map[string]any{"91": float64(300)}, usage,
		"只有今天的消费类日志能进这张表")
	_, present := usage["92"]
	assert.False(t, present, "昨天最后一秒那笔不属于今天")
}

// TestUserTokenTodayUsageWindowIsExactlyOneDay 守右端点。
//
// day_end 必须正好是 day_start + 一天。它是右开区间的右端:少一秒会漏掉
// 当天最后一秒的消费,多一秒会把明天第一秒算进来 —— 两种都只在午夜那一瞬
// 现形,而那正是没人盯着的时候。
//
// 变异验证:把 handler 里的 `dayStart + commission.ConsumeDaySeconds` 换成
// `dayStart + 86399` → 断言红。
func TestUserTokenTodayUsageWindowIsExactlyOneDay(t *testing.T) {
	useTodayUsageEnv(t)

	code, data := callTokenTodayUsage(t, 9102)
	require.Equal(t, http.StatusOK, code)

	start, ok := data["day_start"].(float64)
	require.True(t, ok)
	end, ok := data["day_end"].(float64)
	require.True(t, ok)
	assert.EqualValues(t, int64(24*time.Hour/time.Second), int64(end-start))

	usage, ok := data["usage"].(map[string]any)
	require.True(t, ok)
	assert.Empty(t, usage, "一条日志都没有的用户拿到的是空表,不是 null")
}

// TestUserTokenTodayUsageRejectsAnonymous 守鉴权兜底。
//
// 路由挂在 UserAuth 之后,所以这里的 id 不可能为 0 —— 但 handler 自己也必须
// 判一次:c.GetInt("id") 为 0 时若继续往下走,聚合的 WHERE user_id = 0 会
// 命中"没有令牌的那些日志"(后台任务、渠道测试),把它们当成某个人的消费。
func TestUserTokenTodayUsageRejectsAnonymous(t *testing.T) {
	useTodayUsageEnv(t)

	gin.SetMode(gin.TestMode)
	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	c.Request = httptest.NewRequest(http.MethodGet, "/api/qy/token-usage/today", nil)
	UserTokenTodayUsage(c)

	assert.Equal(t, http.StatusUnauthorized, rec.Code, rec.Body.String())
}
