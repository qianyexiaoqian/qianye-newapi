package subscription

import (
	"path/filepath"
	"strconv"
	"sync/atomic"
	"testing"
	_ "unsafe" // //go:linkname 需要

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/qianye/config"
	qymodel "github.com/QuantumNous/new-api/qianye/model"

	"github.com/glebarez/sqlite"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// 本文件是 subscription 包的数据库脚手架。
//
// 为什么必须真跑数据库:本模块要守住的东西 —— 名额的**去重人数**口径、
// 删除的两条分支、级联之后那两个"严重后果"确实不再成立 —— 全都不在纯函数里,
// 而在 SQL 的语义(COUNT DISTINCT)与事务的原子性里。把 gateSeat 的
// `Distinct("user_id")` 悄悄换成普通 Count,只测纯函数的用例照样全绿,
// 而那正是本仓反复出现的"假回归"。
//
// 生产环境主库可以是 SQLite/MySQL/PostgreSQL 三者之一、扩展库固定 MySQL,
// 这里两边都跑 sqlite,因此断言只依赖跨库通用语义(COUNT DISTINCT、
// 条件 UPDATE 的 RowsAffected、ON CONFLICT DO UPDATE)。

//go:linkname qyDBHandle github.com/QuantumNous/new-api/qianye/db.handle
var qyDBHandle atomic.Pointer[gorm.DB]

// qyDBHealthy 指向 db 包的健康标志。db.Available() 读它,而 audit.Write 与
// guard.RequireAPI 又读 db.Available() —— 不置位的话审计断言会全部变成
// "永远没写进去"的假绿,管理端接口则一律 503。
//
//go:linkname qyDBHealthy github.com/QuantumNous/new-api/qianye/db.healthy
var qyDBHealthy atomic.Bool

//go:linkname qyConfig github.com/QuantumNous/new-api/qianye/config.current
var qyConfig atomic.Pointer[config.Config]

// modelCommonGroupCol 指向 model 包里那个保留字列名(users.group 要加反引号或双引号)。
//
// 生产环境由 model.initCol() 在 InitDB 时按数据库类型填好,而测试直接替换
// model.DB 句柄、绕过了 InitDB,于是它是空串 —— 上游 getUserGroupByIdTx 的
// `Select(commonGroupCol)` 会发出 `SELECT  FROM users` 这种语法错误的 SQL。
// 强制删除的分组回落正好走那条路径,不接上这个变量,那条用例断言的就不是
// "分组没回落",而是"测试环境没搭好"。
//
//go:linkname modelCommonGroupCol github.com/QuantumNous/new-api/model.commonGroupCol
var modelCommonGroupCol string

// extTables 是本模块会碰到的全部扩展库表。
// qy_audit_logs 必须一起建:删除与改名额的审计断言依赖它。
func extTables() []any {
	return []any{&PlanSeat{}, &qymodel.AuditLog{}}
}

// newExtDB 建一个扩展库测试实例并接到 db.Get()。
//
// 用 t.TempDir() 下的文件库而不是 ":memory:":":memory:" 按连接隔离,
// 多开一条连接就会看到一个空库。
func newExtDB(t *testing.T) *gorm.DB {
	t.Helper()
	dsn := filepath.Join(t.TempDir(), "qy_subscription.db") +
		"?_pragma=journal_mode(WAL)&_pragma=busy_timeout(10000)"
	gdb, err := gorm.Open(sqlite.Open(dsn), &gorm.Config{})
	require.NoError(t, err)
	sqlDB, err := gdb.DB()
	require.NoError(t, err)
	// db.LockForUpdate 无条件渲染 FOR UPDATE(扩展库固定 MySQL),sqlite 不认。
	gdb.ClauseBuilders["FOR"] = func(clause.Clause, clause.Builder) {}
	require.NoError(t, gdb.AutoMigrate(extTables()...))

	prevHandle := qyDBHandle.Swap(gdb)
	prevHealthy := qyDBHealthy.Swap(true)
	prevCfg := qyConfig.Swap(&config.Config{Enabled: true})
	t.Cleanup(func() {
		qyDBHandle.Store(prevHandle)
		qyDBHealthy.Store(prevHealthy)
		qyConfig.Store(prevCfg)
		_ = sqlDB.Close()
		resetCache()
	})
	resetCache()
	return gdb
}

// newMainDB 临时替换主库句柄并建订阅相关的表。
//
// model.QyLockForUpdate 在 sqlite 下自动降级为空操作(见 model.lockForUpdate),
// 所以删除路径的行锁在这里不会炸,但也不构成保护 —— 并发语义不在本文件的
// 断言范围内(见 gate.go 对 R1 的说明)。
//
// 必须是**文件库 + WAL**,不能用 ":memory:" + MaxOpenConns(1):
// model.CreateUserSubscriptionFromPlanTx 内部会调 GetDBTimestamp(),而那个函数
// 走的是全局 model.DB 而不是调用方的 tx。只有一条连接时,它会在事务持有连接的
// 情况下再去申请一条,整个测试直接挂死在连接池上(实测挂了 10 分钟)。
func newMainDB(t *testing.T) *gorm.DB {
	t.Helper()
	dsn := filepath.Join(t.TempDir(), "main.db") +
		"?_pragma=journal_mode(WAL)&_pragma=busy_timeout(10000)"
	gdb, err := gorm.Open(sqlite.Open(dsn), &gorm.Config{})
	require.NoError(t, err)
	sqlDB, err := gdb.DB()
	require.NoError(t, err)
	require.NoError(t, gdb.AutoMigrate(
		&model.User{},
		&model.SubscriptionPlan{},
		&model.UserSubscription{},
		&model.SubscriptionOrder{},
		// 预扣费的幂等记录表。delete_test 里要真的跑一次上游的
		// PreConsumeUserSubscription 来证明级联确实堵住了"后果 1"。
		&model.SubscriptionPreConsumeRecord{},
	))

	prev := model.DB
	prevGroupCol := modelCommonGroupCol
	// common.RedisEnabled 的包级默认值是 true(生产环境由 InitRedisClient 在
	// 没有连接串时改成 false)。不关掉的话,分组回落之后的缓存刷新会去解引用
	// 一个 nil 的 common.RDB 直接 panic —— 与 paypass / violation / withdraw
	// 几个模块的测试同口径。
	prevRedis := common.RedisEnabled
	model.DB = gdb
	modelCommonGroupCol = "`group`" // sqlite 与 MySQL 同形,PostgreSQL 用双引号
	common.RedisEnabled = false
	t.Cleanup(func() {
		model.DB = prev
		modelCommonGroupCol = prevGroupCol
		common.RedisEnabled = prevRedis
		_ = sqlDB.Close()
	})
	return gdb
}

// seedPlan 往主库插一个套餐。
func seedPlan(t *testing.T, gdb *gorm.DB, id int, title string) *model.SubscriptionPlan {
	t.Helper()
	plan := &model.SubscriptionPlan{
		Id: id, Title: title, Enabled: true,
		DurationUnit: model.SubscriptionDurationMonth, DurationValue: 1,
	}
	require.NoError(t, gdb.Create(plan).Error)
	return plan
}

// seedSubscription 往主库插一条用户订阅。
func seedSubscription(t *testing.T, gdb *gorm.DB, userId, planId int, status string) {
	t.Helper()
	require.NoError(t, gdb.Create(&model.UserSubscription{
		UserId: userId, PlanId: planId, Status: status,
		StartTime: 1, EndTime: 1 << 40,
	}).Error)
}

// seedOrder 往主库插一条订阅订单。
func seedOrder(t *testing.T, gdb *gorm.DB, userId, planId int, tradeNo, status string) {
	t.Helper()
	require.NoError(t, gdb.Create(&model.SubscriptionOrder{
		UserId: userId, PlanId: planId, TradeNo: tradeNo, Status: status,
		CreateTime: common.GetTimestamp(),
	}).Error)
}

// seedUser 往主库插一个用户。aff_code 必须逐个不同:上游 users.aff_code 上有
// 唯一索引,留空的话第二个用户直接撞 UNIQUE 约束。
func seedUser(t *testing.T, gdb *gorm.DB, id int, username string) {
	t.Helper()
	require.NoError(t, gdb.Create(&model.User{
		Id: id, Username: username, AffCode: "aff" + strconv.Itoa(id),
		Status: common.UserStatusEnabled, Group: "default",
	}).Error)
}

// putSeat 直接往扩展库写一行名额配置,跳过 HTTP 层。
//
// 表名与列名一律用**字面量**,不复用 PlanSeat 结构体:用结构体的话,读写两侧会
// 一起跟着结构体走 —— 把 TableName() 改掉(也就是"换了一张表"这个缺陷)
// 测试照样全绿。
func putSeat(t *testing.T, gdb *gorm.DB, planId, capacity int) {
	t.Helper()
	require.NoError(t, gdb.Exec(
		"INSERT INTO qy_subscription_plan_seats (plan_id, capacity, updated_by, created_at, updated_at) "+
			"VALUES (?, ?, 0, 1, 1) ON CONFLICT(plan_id) DO UPDATE SET capacity = excluded.capacity",
		planId, capacity).Error)
	resetCache()
}

// auditActions 读出全部审计记录的 action + result,按写入顺序。
func auditActions(t *testing.T, gdb *gorm.DB) []string {
	t.Helper()
	var rows []qymodel.AuditLog
	require.NoError(t, gdb.Order("id asc").Find(&rows).Error)
	out := make([]string, 0, len(rows))
	for _, r := range rows {
		out = append(out, r.Action+":"+r.Result)
	}
	return out
}
