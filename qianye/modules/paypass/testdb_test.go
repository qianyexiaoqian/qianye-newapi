package paypass

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

// 本文件是 paypass 包的数据库脚手架。
//
// 为什么必须真跑数据库:本模块被要求守住的两条不变量 —— "并发输错不能只算一次"
// 与"联系人不构成豁免" —— 都不在纯函数里,而在 SQL 的原子性与调用顺序里。
// 只测 validateStrength / compareHash 这类纯函数的话,把 counter.go 整段换成
// 读-改-写,测试照样全绿 —— 那正是本仓反复出现的"假回归"。
//
// 生产的扩展库固定是 MySQL,这里跑 sqlite,因此断言一律只依赖跨库通用语义
// (自增表达式 UPDATE、带 WHERE 的条件 UPDATE、ON CONFLICT DO NOTHING 的
// RowsAffected)。counter.go 里那段"为什么不用 CASE WHEN"的说明正是为了
// 避开 MySQL 与 SQLite 在赋值求值顺序上的差异。

//go:linkname qyDBHandle github.com/QuantumNous/new-api/qianye/db.handle
var qyDBHandle atomic.Pointer[gorm.DB]

// qyDBHealthy 指向 db 包的健康标志。db.Available() 读它,而 audit.Write 又读
// db.Available() —— 不置位的话审计断言会全部变成"永远没写进去"的假绿。
//
//go:linkname qyDBHealthy github.com/QuantumNous/new-api/qianye/db.healthy
var qyDBHealthy atomic.Bool

//go:linkname qyConfig github.com/QuantumNous/new-api/qianye/config.current
var qyConfig atomic.Pointer[config.Config]

// extTables 是本模块会碰到的全部扩展库表。
//
// qy_settings 必须一起建:loadPolicy 每次都查它,表不存在时它会吞掉错误回落
// 默认值 —— 那会让"运营覆盖没生效"这类问题在测试里完全看不见。
// qy_audit_logs 同理,管理端审计断言依赖它。
func extTables() []any {
	return []any{&PayPassword{}, &qymodel.Setting{}, &qymodel.AuditLog{}}
}

// newTestDB 建一个扩展库测试实例并接到 db.Get()。
//
// 用 t.TempDir() 下的文件库 + WAL 而不是 ":memory:":并发测试要开多条连接,
// 而 ":memory:" 按连接隔离,各自会看到一个空库。
func newTestDB(t *testing.T) *gorm.DB {
	t.Helper()
	dsn := filepath.Join(t.TempDir(), "qy_paypass.db") +
		"?_pragma=journal_mode(WAL)&_pragma=busy_timeout(10000)"
	gdb, err := gorm.Open(sqlite.Open(dsn), &gorm.Config{})
	require.NoError(t, err)
	sqlDB, err := gdb.DB()
	require.NoError(t, err)
	// db.LockForUpdate 无条件渲染 FOR UPDATE(扩展库固定 MySQL),sqlite 不认。
	// 本模块当前不用行锁,但脚手架保持与 commission 一致,免得将来加了行锁才发现。
	gdb.ClauseBuilders["FOR"] = func(clause.Clause, clause.Builder) {}
	require.NoError(t, gdb.AutoMigrate(extTables()...))

	prevHandle := qyDBHandle.Swap(gdb)
	prevHealthy := qyDBHealthy.Swap(true)
	prevCfg := qyConfig.Swap(&config.Config{
		Enabled:  true,
		Transfer: config.Transfer{Enabled: true},
	})
	t.Cleanup(func() {
		qyDBHandle.Store(prevHandle)
		qyDBHealthy.Store(prevHealthy)
		qyConfig.Store(prevCfg)
		_ = sqlDB.Close()
	})
	return gdb
}

// useMainDB 临时替换主库句柄并建 users 表。
// 管理端接口与邮箱找回都要读 model.GetUserById,不建这张表它们只会返回"用户不存在"。
func useMainDB(t *testing.T) *gorm.DB {
	t.Helper()
	gdb, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	require.NoError(t, err)
	sqlDB, err := gdb.DB()
	require.NoError(t, err)
	sqlDB.SetMaxOpenConns(1)
	require.NoError(t, gdb.AutoMigrate(&model.User{}))

	prev := model.DB
	model.DB = gdb
	t.Cleanup(func() {
		model.DB = prev
		_ = sqlDB.Close()
	})
	return gdb
}

// seedUser 往主库插一个用户。email 为空表示"未绑定邮箱"。
//
// aff_code 必须逐个不同:上游 users.aff_code 上有唯一索引,留空的话第二个用户
// 直接撞 UNIQUE 约束 —— 而每个管理端测试都要种两个用户(操作者 + 目标)。
func seedUser(t *testing.T, gdb *gorm.DB, id int, username, email string) {
	t.Helper()
	require.NoError(t, gdb.Create(&model.User{
		Id: id, Username: username, Email: email,
		AffCode: "aff" + strconv.Itoa(id),
		Status:  common.UserStatusEnabled, Group: "default",
	}).Error)
}

// setPassword 直接给某个用户落一个已生效的支付密码,跳过 HTTP 层。
func setPassword(t *testing.T, gdb *gorm.DB, userId int, password string) {
	t.Helper()
	hash, err := hashPassword(password)
	require.NoError(t, err)
	now := common.GetTimestamp()
	require.NoError(t, gdb.Create(&PayPassword{
		UserId: userId, Algo: algoBcrypt, Hash: hash,
		SetAt: now, ChangedAt: now, UpdatedAt: now,
	}).Error)
}

// rowOf 回读一个用户的支付密码行。
func rowOf(t *testing.T, gdb *gorm.DB, userId int) PayPassword {
	t.Helper()
	var row PayPassword
	require.NoError(t, gdb.Where("user_id = ?", userId).First(&row).Error)
	return row
}

// putSetting 写一条运营覆盖到 qy_settings。
//
// scope 与键名一律用**字面量**,绝不复用 settings.go 里的 settingScope /
// keyMaxAttempts 常量。用常量的话,读写两侧会一起跟着常量走 ——
// 把 scope 改成 "paypass"(也就是"自己另起一套配置"这个缺陷)测试照样全绿。
// 实测过:第一版这里写的是常量,M5 变异一次都没抓到。
func putSetting(t *testing.T, gdb *gorm.DB, key, value string) {
	t.Helper()
	require.NoError(t, gdb.Clauses(clause.OnConflict{
		Columns:   []clause.Column{{Name: "scope"}, {Name: "k"}},
		DoUpdates: clause.AssignmentColumns([]string{"v", "updated_at"}),
	}).Create(&qymodel.Setting{
		Scope: "transfer", K: key, V: value, UpdatedAt: common.GetTimestamp(),
	}).Error)
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
