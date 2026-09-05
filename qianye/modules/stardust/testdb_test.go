package stardust

import (
	"path/filepath"
	"sync/atomic"
	"testing"
	_ "unsafe" // //go:linkname 需要

	"github.com/QuantumNous/new-api/qianye/config"
	qymodel "github.com/QuantumNous/new-api/qianye/model"

	"github.com/glebarez/sqlite"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// 本文件是 stardust 包的数据库测试脚手架,照 commission/testdb_test.go。
//
// Credit / Debit 的几条契约(幂等重放不动余额、不足时不留残行、条件 UPDATE 的
// WHERE)全都**不在算术里,而在 ON CONFLICT 语义与 WHERE 条件里**。只测纯函数的
// 结果是:把 DoNothing 换成 DO UPDATE、把 RowsAffected 判据删掉,测试照样全绿。
// 要真正捕捉这类缺陷,必须让数据库把每一条语句真跑一遍。
//
// 扩展库受支持的部署方言是 MySQL 与 PostgreSQL(见 qianye/db.DialectorFor),
// 这里用 sqlite 当测试方言。断言一律只依赖跨库通用语义(比较、SUM、ORDER BY、
// ON CONFLICT DO NOTHING 的 RowsAffected=0/1),不碰任何 MySQL 专有语法。

// qyDBHandle 指向 qianye/db 包里的连接句柄。
//
// effective() / groupRates() 通过 db.Get() 自取句柄、不接受注入,而 db.Init 只会拨
// MySQL / PostgreSQL。用 //go:linkname 把那个句柄借出来,是在**不改动任何生产代码**
// 的前提下让这些函数跑在测试库上的唯一办法。
//
//go:linkname qyDBHandle github.com/QuantumNous/new-api/qianye/db.handle
var qyDBHandle atomic.Pointer[gorm.DB]

// qyConfig 同理指向 qianye/config 的当前配置快照。
//
//go:linkname qyConfig github.com/QuantumNous/new-api/qianye/config.current
var qyConfig atomic.Pointer[config.Config]

// extTables 是本包逻辑会碰到的全部扩展库表。
//
// qy_settings 必须一起建:effective() 每次都会查它,表不存在时它会吞掉错误
// 退回 YAML 默认值 —— 那会让"运营覆盖没生效"这类问题在测试里完全看不见。
func extTables() []any {
	return append(Tables(), &qymodel.Setting{}, &qymodel.KV{})
}

// newTestDB 建一个承载本包全部表的测试库,并把它接到 db.Get()。
//
// 用 t.TempDir() 下的文件库而不是 ":memory:":effective() 走 db.Get() 拿到的是
// **另一条**连接,":memory:" 按连接隔离会各看到一个空库。WAL 让事务持写锁期间
// 另一条连接仍能读。
func newTestDB(t *testing.T) *gorm.DB {
	t.Helper()
	dsn := filepath.Join(t.TempDir(), "qy_ext.db") +
		"?_pragma=journal_mode(WAL)&_pragma=busy_timeout(5000)"
	gdb, err := gorm.Open(sqlite.Open(dsn), &gorm.Config{})
	require.NoError(t, err)
	sqlDB, err := gdb.DB()
	require.NoError(t, err)

	// db.LockForUpdate 对 sqlite 方言本来就跳过 FOR UPDATE;这里再把 FOR 子句渲染成空
	// 是兜底 —— 那是驱动的实现细节而不是契约。测试是单协程的,行锁在这里没有语义。
	gdb.ClauseBuilders["FOR"] = func(clause.Clause, clause.Builder) {}

	require.NoError(t, gdb.AutoMigrate(extTables()...))

	prev := qyDBHandle.Swap(gdb)
	resetCaches()
	t.Cleanup(func() {
		qyDBHandle.Store(prev)
		resetCaches()
		_ = sqlDB.Close()
	})
	return gdb
}

// resetCaches 清掉本包所有跨测试残留的进程内缓存。
// 不清的话上一个测试的库内容会渗进下一个测试,而且渗进来的往往正好是让断言变成永真的那一份。
func resetCaches() {
	invalidateSettings()
	invalidateGroupRates()
}

// useConfig 临时替换扩展的全局配置快照。
func useConfig(t *testing.T, cfg *config.Config) {
	t.Helper()
	prev := qyConfig.Swap(cfg)
	invalidateSettings()
	t.Cleanup(func() {
		qyConfig.Store(prev)
		invalidateSettings()
	})
}

// balanceOf 回读余额行;不存在时返回 nil,用于断言"没有凭空建行"。
func balanceOf(t *testing.T, gdb *gorm.DB, userId int) *Balance {
	t.Helper()
	var rows []Balance
	require.NoError(t, gdb.Where("user_id = ?", userId).Find(&rows).Error)
	if len(rows) == 0 {
		return nil
	}
	return &rows[0]
}

// ledgerOf 读出某个用户的全部流水,按落库顺序。
func ledgerOf(t *testing.T, gdb *gorm.DB, userId int) []Ledger {
	t.Helper()
	var rows []Ledger
	require.NoError(t, gdb.Where("user_id = ?", userId).Order("id asc").Find(&rows).Error)
	return rows
}
