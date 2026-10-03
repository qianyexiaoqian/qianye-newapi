package stardust

import (
	"bytes"
	"net/http"
	"net/http/httptest"
	"strconv"
	"sync/atomic"
	"testing"
	_ "unsafe" // //go:linkname 需要

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/qianye/config"
	qymodel "github.com/QuantumNous/new-api/qianye/model"

	"github.com/gin-gonic/gin"
	"github.com/glebarez/sqlite"
	"github.com/shopspring/decimal"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
	gormlogger "gorm.io/gorm/logger"
)

// api_helpers_test.go —— HTTP 接口测试的脚手架,叠在 testdb_test.go 之上。
//
// 接口用例一律从 gin 路由树进(Mod{}.RegisterUserRoutes / RegisterAdminRoutes),
// 而不是直接调 handler:路由挂没挂、闸门顺序对不对、RootActionGate 拒了之后 handler
// 有没有被执行,只有走完整条链才看得见。

// qyDBHealthy 是熔断的健康标志。guard.RequireAPI 要求它为 true,
// 而它只有真的 db.Init 过才会被置上。
//
//go:linkname qyDBHealthy github.com/QuantumNous/new-api/qianye/db.healthy
var qyDBHealthy atomic.Bool

// apiEnv 是一次接口测试的全部数据库:扩展库(含审计表)与主库(users / logs)。
type apiEnv struct {
	ext  *gorm.DB
	main *gorm.DB
}

// newAPIEnv 建扩展库与主库,并把扩展置成"库健康"让 guard.RequireAPI 放行。
//
// 主库要有 users(操作人判据回查角色、余额列表取用户名)与 logs(RootActionGate
// 拒绝时经 model.RecordOperationAuditLog 写一行);common.RedisEnabled 包级默认是
// true 而 common.RDB 是 nil,用户名回查那一步会空指针,必须关掉。
// 关键操作限流按 IP + 路由 计数且全进程共用一个内存桶,20 次 / 20 分钟,
// 同包几十个用例打同一条路由会撞 429 —— 测试里关掉它,闸门顺序由 AST 守卫钉住。
func newAPIEnv(t *testing.T, mutate func(*config.Stardust)) apiEnv {
	t.Helper()
	ext := newTestDB(t)
	require.NoError(t, ext.AutoMigrate(&qymodel.AuditLog{}))
	useConfig(t, stardustConfig(mutate))

	prevHealthy := qyDBHealthy.Swap(true)
	prevCrit := common.CriticalRateLimitEnable
	prevRedis := common.RedisEnabled
	common.CriticalRateLimitEnable = false
	common.RedisEnabled = false
	t.Cleanup(func() {
		qyDBHealthy.Store(prevHealthy)
		common.CriticalRateLimitEnable = prevCrit
		common.RedisEnabled = prevRedis
	})

	main, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{Logger: gormlogger.Discard})
	require.NoError(t, err)
	sqlDB, err := main.DB()
	require.NoError(t, err)
	sqlDB.SetMaxOpenConns(1)
	require.NoError(t, main.AutoMigrate(&model.User{}, &model.Log{}, &model.AuditLog{}))
	prevMain, prevLog := model.DB, model.LOG_DB
	prevType, prevLogType := common.MainDatabaseType(), common.LogDatabaseType()
	model.DB, model.LOG_DB = main, main
	common.SetMainDatabaseType(common.DatabaseTypeSQLite)
	common.SetLogDatabaseType(common.DatabaseTypeSQLite)
	t.Cleanup(func() {
		model.DB, model.LOG_DB = prevMain, prevLog
		common.SetMainDatabaseType(prevType)
		common.SetLogDatabaseType(prevLogType)
		_ = sqlDB.Close()
	})
	return apiEnv{ext: ext, main: main}
}

// seedUser 往主库插一个账号。aff_code 是唯一索引,空串会互相撞。
func (e apiEnv) seedUser(t *testing.T, id int, name string, role int) {
	t.Helper()
	require.NoError(t, e.main.Create(&model.User{
		Id: id, Username: name, Password: "x", Role: role, Status: common.UserStatusEnabled,
		Email: name + "@x.test", Group: "default", AffCode: "aff" + strconv.Itoa(id),
	}).Error)
}

// seedAccrual 插一行日桶。
func seedAccrual(t *testing.T, gdb *gorm.DB, userId int, day, status, gross string, ledgerId int64) Accrual {
	t.Helper()
	now := common.GetTimestamp()
	a := Accrual{
		UserId: userId, BucketDate: day, UserGroup: "default", RateBps: 10_000, QuotaPerUnit: 500_000,
		BaseQuota: 3_700_000, Gross: decimal.RequireFromString(gross), Status: status,
		LedgerId: ledgerId, ComputedAt: now,
	}
	if status == AccrualSettled {
		a.SettledAt = now
	}
	if status == AccrualHeld {
		a.HoldReason = HoldOverdraft
	}
	require.NoError(t, gdb.Create(&a).Error)
	return a
}

// userRouter 建一棵挂了 UserAuth 等价上下文的用户端路由树。
func userRouter(userId int) *gin.Engine {
	gin.SetMode(gin.TestMode)
	e := gin.New()
	g := e.Group("/api/qy", func(c *gin.Context) {
		c.Set("id", userId)
		c.Set("username", "user"+strconv.Itoa(userId))
		c.Set("role", common.RoleCommonUser)
		c.Next()
	})
	Mod{}.RegisterUserRoutes(g)
	return e
}

// adminRouter 建一棵挂了 AdminAuth 等价上下文的管理端路由树。
//
// middleware.AdminAuth() 在生产里必然同时写入 id / username / role;role 不能省:
// 动钱接口的越级判据(guard.ManageableTarget)与 RootActionGate 读的就是它。
func adminRouter(actorId, role int) *gin.Engine {
	gin.SetMode(gin.TestMode)
	e := gin.New()
	g := e.Group("/api/qy/admin", func(c *gin.Context) {
		c.Set("id", actorId)
		c.Set("username", "admin"+strconv.Itoa(actorId))
		c.Set("role", role)
		c.Next()
	})
	Mod{}.RegisterAdminRoutes(g)
	return e
}

func call(t *testing.T, e *gin.Engine, method, target, body string) *httptest.ResponseRecorder {
	t.Helper()
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(method, target, bytes.NewBufferString(body))
	req.Header.Set("Content-Type", "application/json")
	e.ServeHTTP(rec, req)
	return rec
}

// envelope 是响应信封;非 200 带 code。
type envelope struct {
	Success bool   `json:"success"`
	Code    string `json:"code"`
	Message string `json:"message"`
	Data    any    `json:"data"`
}

func decode(t *testing.T, rec *httptest.ResponseRecorder) envelope {
	t.Helper()
	var env envelope
	require.NoError(t, common.Unmarshal(rec.Body.Bytes(), &env), rec.Body.String())
	return env
}

// dataOf 断言 200 并返回 data 对象。
func dataOf(t *testing.T, rec *httptest.ResponseRecorder) map[string]any {
	t.Helper()
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	env := decode(t, rec)
	require.True(t, env.Success)
	data, ok := env.Data.(map[string]any)
	require.True(t, ok, "data 必须是对象: %s", rec.Body.String())
	return data
}

// codeOf 断言状态码并返回错误 code。
func codeOf(t *testing.T, rec *httptest.ResponseRecorder, wantStatus int) string {
	t.Helper()
	require.Equal(t, wantStatus, rec.Code, rec.Body.String())
	env := decode(t, rec)
	require.False(t, env.Success)
	return env.Code
}

// itemsOf 取分页响应里的 items。
func itemsOf(t *testing.T, data map[string]any) []map[string]any {
	t.Helper()
	raw, ok := data["items"].([]any)
	require.True(t, ok, "items 必须是数组(nil 切片会序列化成 null)")
	out := make([]map[string]any, 0, len(raw))
	for _, it := range raw {
		m, ok := it.(map[string]any)
		require.True(t, ok)
		out = append(out, m)
	}
	return out
}

func auditRowsOf(t *testing.T, gdb *gorm.DB, action string) []qymodel.AuditLog {
	t.Helper()
	var rows []qymodel.AuditLog
	require.NoError(t, gdb.Where("action = ?", action).Order("id asc").Find(&rows).Error)
	return rows
}

func countResults(rows []qymodel.AuditLog) (ok, fail int) {
	for _, r := range rows {
		if r.Result == qymodel.ResultOK {
			ok++
		} else {
			fail++
		}
	}
	return ok, fail
}
