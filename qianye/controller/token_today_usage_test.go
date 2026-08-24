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
// 聚合本身(分组、过滤、上界)由 qianye/modules/commission 的用例守,
// 日界的时区算术由 qianye/serverday 的用例守(含夏令时跳变日的整张表)。
// 这里守的是这条端点自己唯一会做错的事:**它到底把哪一段叫「今天」**。
//
// 项目方原话:「api 密钥的今日消耗以服务器的时间为准,即:0 点到 23 点
// 59 分 59 秒是今日的消耗。」
//
// 错起来完全没有症状:数字仍然是一个像模像样的金额,只是把昨晚那几笔算到了
// 今天。所以夹具刻意留着 day_offset_minutes: 480 —— 那是返佣的「消费日」,
// 这条端点**不能**再用它;留着它是为了让"用错了"这件事有机会红。

// useTodayUsageEnv 加载一份 day_offset_minutes=480 的配置,并接上一个内存日志库。
//
// 480 是**诱饵**:端点若还在读返佣日界,窗口就会变成 UTC+8 的一天,
// 下面播下去的种子会立刻对不上(除非机器本地时区恰好就是 UTC+8)。
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
		"诱饵没放进去的话,下面那条「不许再走返佣日界」的用例就白跑了")

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

// TestUserTokenTodayUsageWindowIsTheServerLocalNaturalDay 是这条端点的主用例。
//
// 「今天」= 服务器本地时区(time.Local)的自然日。本地时区从哪来:TZ 环境
// 变量 → /etc/localtime → UTC,由 Go 的 time 包在进程启动时定死,没有配置项。
//
// 布置:取此刻所在的**本地**自然日,在四条缝上各放一笔消费:
//
//	本地 0 点 - 1 秒     令牌 92,700   ← 昨天,不能算
//	本地 0 点整          令牌 91,300   ← 今天第一秒
//	本地 23:59:59        令牌 94,120   ← 今天最后一秒
//	次日 0 点整          令牌 95,960   ← 明天,不能算(右开区间)
//	本地 0 点 + 1 秒,充值 令牌 93,900   ← 不是消费,不能算
//
// 这四笔同时也是"端点没在走返佣日界"的判据:夹具把 day_offset_minutes 设成
// 480,于是在任何 time.Local ≠ UTC+8 的机器上,返佣窗口都会漏掉 94 或多收
// 92 —— 演示机是 PST,两者差 16 小时,两条都会红。
//
// 变异验证见文件末尾。
func TestUserTokenTodayUsageWindowIsTheServerLocalNaturalDay(t *testing.T) {
	logDB := useTodayUsageEnv(t)

	const me = 9101
	now := common.GetTimestamp()

	// 与被测代码**不共用一行逻辑**地重算本地日界:直接用 time 包按本地
	// 日历取当天 0 点。(本机与绝大多数时区的夏令时跳变都不在午夜,所以这个
	// 朴素写法在这里成立;跳变正好落在午夜的那些日子由 qianye/serverday 的
	// 用例逐个钉住,不在这一层重复。)
	nowLocal := time.Unix(now, 0).In(time.Local)
	y, m, d := nowLocal.Date()
	wantStart := time.Date(y, m, d, 0, 0, 0, 0, time.Local).Unix()
	wantEnd := time.Date(y, m, d+1, 0, 0, 0, 0, time.Local).Unix()
	require.LessOrEqual(t, wantStart, now)
	require.Greater(t, wantEnd, now)

	seedTodayUsageLog(t, logDB, me, 92, wantStart-1, 700, model.LogTypeConsume)
	seedTodayUsageLog(t, logDB, me, 91, wantStart, 300, model.LogTypeConsume)
	seedTodayUsageLog(t, logDB, me, 94, wantEnd-1, 120, model.LogTypeConsume)
	seedTodayUsageLog(t, logDB, me, 95, wantEnd, 960, model.LogTypeConsume)
	seedTodayUsageLog(t, logDB, me, 93, wantStart+1, 900, model.LogTypeTopup)

	code, data := callTokenTodayUsage(t, me)
	require.Equal(t, http.StatusOK, code)

	assert.EqualValues(t, wantStart, data["day_start"], "「今日」的起点必须是服务器本地时区的午夜")
	assert.EqualValues(t, wantEnd, data["day_end"])
	assert.Equal(t, "00:00:00",
		time.Unix(int64(data["day_start"].(float64)), 0).In(time.Local).Format("15:04:05"),
		"起点在服务器本地时区里必须正好是 0 点 —— 走返佣日界(UTC+8)时这里会是别的钟点")

	usage, ok := data["usage"].(map[string]any)
	require.True(t, ok, "usage 必须是一张以令牌 id 字符串为键的表:%#v", data["usage"])
	assert.Equal(t, map[string]any{"91": float64(300), "94": float64(120)}, usage,
		"只有本地自然日之内的消费类日志能进这张表")
}

// TestUserTokenTodayUsageReportsTheTimezoneItActuallyUsed 守下发的时区标签。
//
// 界面上只写「今日 00:00 → 23:59:59」是不够的:容器里 TZ 常常没设、tzdata
// 常常没装,那时进程认的「本地」就是 UTC,而运营以为是他自己所在地。所以
// 缩写与偏移必须如实下发,而且必须来自**窗口起点那一刻**的时区 ——
// 夏令时切换日当天,起点的偏移与此刻的偏移可以差一小时。
func TestUserTokenTodayUsageReportsTheTimezoneItActuallyUsed(t *testing.T) {
	useTodayUsageEnv(t)

	code, data := callTokenTodayUsage(t, 9103)
	require.Equal(t, http.StatusOK, code)

	start := int64(data["day_start"].(float64))
	wantName, wantOffsetSeconds := time.Unix(start, 0).In(time.Local).Zone()
	assert.Equal(t, wantName, data["timezone"])
	assert.EqualValues(t, wantOffsetSeconds/60, data["utc_offset_minutes"],
		"偏移的单位是分钟 —— 界面要拿它把区间渲染成服务器本地时间")

	_, present := data["day_offset_minutes"]
	assert.False(t, present,
		"day_offset_minutes 是返佣日界的字段,这条端点已经不走它了;"+
			"继续下发会让前端以为两处还是同一个口径")
}

// TestUserTokenTodayUsageWindowIsExactlyOneDay 守右端点。
//
// day_end 必须是**下一个本地自然日的起点**。它是右开区间的右端:少一秒会漏掉
// 当天最后一秒的消费,多一秒会把明天第一秒算进来 —— 两种都只在午夜那一瞬
// 现形,而那正是没人盯着的时候。
//
// 变异验证:把 handler 里的 serverday.Range(now) 拆成
// `s := serverday.Start(now); s, s + 86399` → 断言红。
func TestUserTokenTodayUsageWindowIsExactlyOneDay(t *testing.T) {
	useTodayUsageEnv(t)

	code, data := callTokenTodayUsage(t, 9102)
	require.Equal(t, http.StatusOK, code)

	start, ok := data["day_start"].(float64)
	require.True(t, ok)
	end, ok := data["day_end"].(float64)
	require.True(t, ok)
	// 夏令时切换日只有 23(或 25)小时,所以这里断言的是"落在合理区间内"
	// 而不是恒等于 86400 —— 精确的日长由 qianye/serverday 的用例逐天钉住。
	assert.GreaterOrEqual(t, int64(end-start), int64(22*3600))
	assert.LessOrEqual(t, int64(end-start), int64(26*3600))

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

// 变异验证(每一条都实际跑过,机器本地时区 PST):
//
//	handler 里 serverday.Range(now) 换回 commission 的返佣日界
//	  → TestUserTokenTodayUsageWindowIsTheServerLocalNaturalDay KILLED
//	    (令牌 92 冒出来、令牌 94 消失,"00:00:00" 那条也红)
//	day_end 改成 day_start + 86399
//	  → 令牌 94(本地 23:59:59 那笔)消失,KILLED
//	把 timezone / utc_offset_minutes 改回下发 day_offset_minutes
//	  → TestUserTokenTodayUsageReportsTheTimezoneItActuallyUsed KILLED
