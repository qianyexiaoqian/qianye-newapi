package mall

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	_ "unsafe" // //go:linkname 需要

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/qianye/config"
	qymodel "github.com/QuantumNous/new-api/qianye/model"
	"github.com/QuantumNous/new-api/qianye/modules/paypass"
	"github.com/QuantumNous/new-api/qianye/modules/stardust"

	"github.com/gin-gonic/gin"
	"github.com/glebarez/sqlite"
	"github.com/stretchr/testify/require"
	"golang.org/x/crypto/bcrypt"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
	gormlogger "gorm.io/gorm/logger"
)

// testdb_test.go —— 本包的数据库测试脚手架(照 stardust/testdb_test.go 与
// lottery/ball_full_e2e_db_test.go)。
//
// 扩展库(商城五张表 + 星屑账本 + 资金单 + 审计 + 支付密码)与主库(用户 / 套餐 /
// 订阅 / 订阅订单 / 兑换码 / 探针 / 日志)都用 sqlite 文件库真跑一遍:下单的几条契约
// (幂等重放不动余额、售罄不留残行、条件 UPDATE 的 WHERE、twophase 两库协议)全都
// **不在算术里,而在 ON CONFLICT 语义、WHERE 条件与事务回滚里**,只测纯函数抓不到。

//go:linkname qyDBHandle github.com/QuantumNous/new-api/qianye/db.handle
var qyDBHandle atomic.Pointer[gorm.DB]

//go:linkname qyDBHealthy github.com/QuantumNous/new-api/qianye/db.healthy
var qyDBHealthy atomic.Bool

//go:linkname qyConfig github.com/QuantumNous/new-api/qianye/config.current
var qyConfig atomic.Pointer[config.Config]

const (
	testAdminId = 9100
	testUserId  = 9101
	testOtherId = 9102
	// testSecretKey 是 32 个零字节的 base64:密钥内容不重要,规格(32 字节)才重要。
	testSecretKey = "AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA="
	testPayPwd    = "pay-123456"
	// testUserHeader 让同一条路由能以不同用户身份调用。
	testUserHeader = "X-Test-Uid"
)

// planIdSeq 给每个测试分配独立的套餐 id:GetSubscriptionPlanById 有一层进程内缓存
// (300 秒),两个测试复用同一个 id 会让后一个读到前一个的套餐。
var planIdSeq atomic.Int32

func nextPlanId() int { return 1000 + int(planIdSeq.Add(1)) }

type mallEnv struct {
	ext  *gorm.DB
	main *gorm.DB
}

// newMallEnv 装好扩展库、主库与配置快照。mutate 可以改配置(例如关掉 outbox)。
func newMallEnv(t *testing.T, mutate func(*config.Config)) *mallEnv {
	t.Helper()
	open := func(name string) *gorm.DB {
		dsn := filepath.Join(t.TempDir(), name) +
			"?_pragma=journal_mode(WAL)&_pragma=busy_timeout(5000)"
		gdb, err := gorm.Open(sqlite.Open(dsn), &gorm.Config{Logger: gormlogger.Discard})
		require.NoError(t, err)
		sqlDB, err := gdb.DB()
		require.NoError(t, err)
		t.Cleanup(func() { _ = sqlDB.Close() })
		return gdb
	}

	ext := open("qy_ext.db")
	// db.LockForUpdate 对 sqlite 方言本来就跳过 FOR UPDATE;再把 FOR 子句渲染成空是兜底。
	ext.ClauseBuilders["FOR"] = func(clause.Clause, clause.Builder) {}
	tables := append(Tables(), stardust.Tables()...)
	tables = append(tables, &qymodel.FundOrder{}, &qymodel.AuditLog{}, &qymodel.Setting{},
		&qymodel.KV{}, &qymodel.TaskLease{}, &paypass.PayPassword{})
	require.NoError(t, ext.AutoMigrate(tables...))

	main := open("main.db")
	require.NoError(t, main.AutoMigrate(&model.User{}, &model.Log{}, &model.QyFundOutbox{},
		&model.SubscriptionPlan{}, &model.UserSubscription{}, &model.SubscriptionOrder{}, &model.Redemption{}))

	prevType := common.MainDatabaseType()
	common.SetMainDatabaseType(common.DatabaseTypeSQLite)
	model.InitCol()
	prevDB, prevLogDB := model.DB, model.LOG_DB
	model.DB, model.LOG_DB = main, main
	prevMem, prevRedis := common.MemoryCacheEnabled, common.RedisEnabled
	common.MemoryCacheEnabled, common.RedisEnabled = false, false
	prevOptions := common.OptionMap
	common.OptionMap = map[string]string{}
	// 关键操作限流按 IP 计、跨测试累积,一个进程里的用例数很容易把 20 次/20 分钟打满。
	prevCrit := common.CriticalRateLimitEnable
	common.CriticalRateLimitEnable = false

	prevHandle := qyDBHandle.Swap(ext)
	prevHealthy := qyDBHealthy.Swap(true)
	prevCfg := qyConfig.Swap(testConfig(mutate))
	invalidateSettings()
	// 生产上由 Mod.InstallHooks 注册:裁决与对账的 Success 分支都靠 Resolver 把订单推到 done。
	InstallResolvers()

	t.Cleanup(func() {
		qyDBHandle.Store(prevHandle)
		qyDBHealthy.Store(prevHealthy)
		qyConfig.Store(prevCfg)
		invalidateSettings()
		model.DB, model.LOG_DB = prevDB, prevLogDB
		common.SetMainDatabaseType(prevType)
		model.InitCol()
		common.MemoryCacheEnabled, common.RedisEnabled = prevMem, prevRedis
		common.OptionMap = prevOptions
		common.CriticalRateLimitEnable = prevCrit
	})

	for _, u := range []model.User{
		{Id: testAdminId, Username: "root", Password: "x", AffCode: "aff-root", Group: "default", Role: common.RoleRootUser, Status: common.UserStatusEnabled},
		{Id: testUserId, Username: "buyer", Password: "x", AffCode: "aff-buyer", Group: "default", Role: common.RoleCommonUser, Status: common.UserStatusEnabled},
		{Id: testOtherId, Username: "other", Password: "x", AffCode: "aff-other", Group: "default", Role: common.RoleCommonUser, Status: common.UserStatusEnabled},
	} {
		require.NoError(t, main.Create(&u).Error)
	}
	return &mallEnv{ext: ext, main: main}
}

func testConfig(mutate func(*config.Config)) *config.Config {
	on := true
	cfg := &config.Config{
		Enabled: true,
		Runtime: config.Runtime{ColdPathTimeoutMs: 3000},
		TwoPhase: config.TwoPhase{
			MainOutboxEnabled: &on, PendingGraceSeconds: 60, BatchSize: 200,
			MaxProbeAttempts: 5, ManualReviewAfterSeconds: 600, OutboxRetentionDays: 30,
			CompensateIntervalSeconds: 30,
		},
		Audit:    config.Audit{Enabled: &on},
		Stardust: config.Stardust{Enabled: true, Name: "星屑"},
		Mall: config.Mall{
			Enabled: true, SecretKey: testSecretKey, SecretKeyVersion: 1,
			AddressRetentionDays: 90, MaxProducts: 200, CodeUploadMax: 500, PendingGraceSeconds: 60,
		},
	}
	if mutate != nil {
		mutate(cfg)
	}
	return cfg
}

// ─────────────────────────── 种子 ───────────────────────────

// grantStardust 用手调入账给用户发星屑。
func grantStardust(t *testing.T, env *mallEnv, userId int, amount int64) {
	t.Helper()
	require.NoError(t, env.ext.Transaction(func(tx *gorm.DB) error {
		_, err := stardust.Credit(tx, stardust.Posting{
			UserId: userId, Kind: stardust.KindManual, Amount: amount,
			IdemScope: "test", IdemKey: common.GetUUID(), OperatorId: testAdminId,
		})
		return err
	}))
}

func balanceOf(t *testing.T, env *mallEnv, userId int) int64 {
	t.Helper()
	rows := make([]stardust.Balance, 0, 1)
	require.NoError(t, env.ext.Where("user_id = ?", userId).Find(&rows).Error)
	if len(rows) == 0 {
		return 0
	}
	return rows[0].Available
}

// ledgerKinds 读出某个用户的全部流水 kind,按落库顺序。
func ledgerKinds(t *testing.T, env *mallEnv, userId int) []string {
	t.Helper()
	rows := make([]stardust.Ledger, 0, 4)
	require.NoError(t, env.ext.Where("user_id = ?", userId).Order("id asc").Find(&rows).Error)
	out := make([]string, 0, len(rows))
	for _, r := range rows {
		out = append(out, r.Kind)
	}
	return out
}

// seedPayPassword 直接落一行支付密码(bcrypt,与 paypass 的算法一致)。
func seedPayPassword(t *testing.T, env *mallEnv, userId int, pwd string) {
	t.Helper()
	hash, err := bcrypt.GenerateFromPassword([]byte(pwd), bcrypt.MinCost)
	require.NoError(t, err)
	now := common.GetTimestamp()
	require.NoError(t, env.ext.Create(&paypass.PayPassword{
		UserId: userId, Algo: "bcrypt", Hash: string(hash), SetAt: now, ChangedAt: now, UpdatedAt: now,
	}).Error)
}

func seedProduct(t *testing.T, env *mallEnv, kind string, price int64, mutate func(*Product)) *Product {
	t.Helper()
	now := common.GetTimestamp()
	p := &Product{
		ProductNo: newProductNo(), Kind: kind, Title: kind + " 商品", Price: price,
		Stock: StockUnlimited, Enabled: true, CreatedAt: now, UpdatedAt: now,
	}
	if mutate != nil {
		mutate(p)
	}
	require.NoError(t, env.ext.Create(p).Error)
	return p
}

// seedCodes 给兑换码商品预存几枚明文码(入库即密文)。
func seedCodes(t *testing.T, env *mallEnv, p *Product, plains ...string) {
	t.Helper()
	for _, plain := range plains {
		row := CodeStock{ProductId: p.Id, Status: CodeUnused, CreatedAt: common.GetTimestamp()}
		require.NoError(t, sealCode(&row, plain, p.ProductNo))
		require.NoError(t, env.ext.Create(&row).Error)
	}
}

func seedPlan(t *testing.T, env *mallEnv, mutate func(*model.SubscriptionPlan)) *model.SubscriptionPlan {
	t.Helper()
	plan := &model.SubscriptionPlan{
		Id: nextPlanId(), Title: "月卡", PriceAmount: 9.9, Currency: "USD",
		DurationUnit: model.SubscriptionDurationMonth, DurationValue: 1,
		Enabled: true, TotalAmount: 1000, QuotaResetPeriod: "never",
	}
	if mutate != nil {
		mutate(plan)
	}
	require.NoError(t, env.main.Create(plan).Error)
	return plan
}

func loadOrder(t *testing.T, env *mallEnv, orderNo string) Order {
	t.Helper()
	var o Order
	require.NoError(t, env.ext.Where("order_no = ?", orderNo).Take(&o).Error)
	return o
}

func loadProduct(t *testing.T, env *mallEnv, id int64) Product {
	t.Helper()
	var p Product
	require.NoError(t, env.ext.Where("id = ?", id).Take(&p).Error)
	return p
}

// auditRows 读出某个动作的审计行。
func auditRows(t *testing.T, env *mallEnv, action string) []qymodel.AuditLog {
	t.Helper()
	rows := make([]qymodel.AuditLog, 0, 4)
	require.NoError(t, env.ext.Where("category = ? AND action = ?", auditCategory, action).
		Order("id asc").Find(&rows).Error)
	return rows
}

// ─────────────────────────── HTTP ───────────────────────────

// newRouter 把真实路由挂起来(含 paypass.Middleware / RootActionGate),
// 身份由伪造的鉴权中间件按请求头注入。
func newRouter() *gin.Engine {
	gin.SetMode(gin.TestMode)
	r := gin.New()
	Mod{}.RegisterPublicRoutes(r.Group("/api/qy"))
	user := r.Group("/api/qy", func(c *gin.Context) {
		uid := testUserId
		if v := c.GetHeader(testUserHeader); v != "" {
			uid, _ = strconv.Atoi(v)
		}
		c.Set("id", uid)
		c.Set("role", common.RoleCommonUser)
		c.Set("username", "buyer")
	})
	Mod{}.RegisterUserRoutes(user)
	admin := r.Group("/api/qy/admin", func(c *gin.Context) {
		role := common.RoleRootUser
		if v := c.GetHeader("X-Test-Role"); v != "" {
			role, _ = strconv.Atoi(v)
		}
		c.Set("id", testAdminId)
		c.Set("role", role)
		c.Set("username", "root")
	})
	Mod{}.RegisterAdminRoutes(admin)
	return r
}

// call 打一次真实请求,返回 HTTP 码与解开的响应信封。
func call(t *testing.T, r *gin.Engine, method, path, body string, headers map[string]string) (int, map[string]any) {
	t.Helper()
	req := httptest.NewRequest(method, path, strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	var env map[string]any
	if w.Body.Len() > 0 {
		require.NoErrorf(t, json.Unmarshal(w.Body.Bytes(), &env), "响应不是 JSON: %s", w.Body.String())
	}
	return w.Code, env
}

func dataOf(env map[string]any) map[string]any {
	d, _ := env["data"].(map[string]any)
	return d
}

func codeOf(env map[string]any) string {
	s, _ := env["code"].(string)
	return s
}

func orderBody(productNo, crid string, extra map[string]any) string {
	m := map[string]any{"product_no": productNo, "client_request_id": crid}
	for k, v := range extra {
		m[k] = v
	}
	b, _ := json.Marshal(m)
	return string(b)
}

func requireOK(t *testing.T, status int, env map[string]any) {
	t.Helper()
	require.Equalf(t, http.StatusOK, status, "响应: %v", env)
}
