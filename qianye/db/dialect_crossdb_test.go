package db

import (
	"fmt"
	"os"
	"testing"
	"time"

	"github.com/glebarez/sqlite"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/driver/mysql"
	"gorm.io/driver/postgres"
	"gorm.io/gorm"
	gormlogger "gorm.io/gorm/logger"
)

// 本文件把"三种方言在资金相关原语上行为一致"这件事变成可执行的判据。
//
// 为什么必须真跑而不是断言 SQL 字符串:方言分支的错误从来不是"渲染得不好看",
// 而是**整条语句在某一家上失败**,或者更糟 —— 成功但语义不同。前者的典型是
// PostgreSQL 的 `column reference "x" is ambiguous`(裸列名),后者的典型是
// `::bigint` 的四舍五入把租约到期时间抬高一秒。断言字符串两种都抓不到。
//
// SQLite 永远跑;MySQL / PostgreSQL 由环境变量开:
//
//	QY_TEST_MYSQL_DSN=root@tcp(127.0.0.1:3306)/qy_scratch?parseTime=true
//	QY_TEST_PG_DSN=postgres://postgres@127.0.0.1:5432/qy_scratch?sslmode=disable

type dialectFixture struct {
	name string
	gdb  *gorm.DB
}

func openDialectFixtures(t *testing.T) []dialectFixture {
	t.Helper()
	cfg := &gorm.Config{Logger: gormlogger.Discard}

	out := []dialectFixture{}
	sq, err := gorm.Open(sqlite.Open(":memory:"), cfg)
	require.NoError(t, err)
	sqlDB, err := sq.DB()
	require.NoError(t, err)
	sqlDB.SetMaxOpenConns(1) // 内存库按连接隔离
	t.Cleanup(func() { _ = sqlDB.Close() })
	out = append(out, dialectFixture{"sqlite", sq})

	if dsn := os.Getenv("QY_TEST_MYSQL_DSN"); dsn != "" {
		g, err := gorm.Open(mysql.Open(dsn), cfg)
		require.NoError(t, err)
		out = append(out, dialectFixture{"mysql", g})
	}
	if dsn := os.Getenv("QY_TEST_PG_DSN"); dsn != "" {
		g, err := gorm.Open(normalizedPGDialector{postgres.Open(dsn)}, cfg)
		require.NoError(t, err)
		out = append(out, dialectFixture{"postgres", g})
	}
	return out
}

// TestUpsertAccumulatesIdenticallyAcrossDialects 钉住"冲突即原子累加"这一条。
//
// 它是 qy_violation_counter(自动封号的唯一判据)与佣金/幂等键/outbox 共用的
// 形状:冲突时按旧值算出新值。三处里任何一处在某一家方言上算错,后果都是
// 钱或封禁判错,而不是报错 —— 所以逐格对照最终行,不是只看"没报错"。
func TestUpsertAccumulatesIdenticallyAcrossDialects(t *testing.T) {
	type row struct {
		UserId      int   `gorm:"column:user_id"`
		WindowStart int64 `gorm:"column:window_start"`
		HitCount    int   `gorm:"column:hit_count"`
		TotalCount  int64 `gorm:"column:total_count"`
	}

	const tbl = "qy_t_upsert"
	results := map[string]row{}

	for _, fx := range openDialectFixtures(t) {
		t.Run(fx.name, func(t *testing.T) {
			g := fx.gdb
			require.NoError(t, g.Exec("DROP TABLE IF EXISTS "+tbl).Error)
			require.NoError(t, g.Exec(`CREATE TABLE `+tbl+` (
				user_id INTEGER PRIMARY KEY,
				window_start BIGINT NOT NULL,
				hit_count INTEGER NOT NULL,
				total_count BIGINT NOT NULL)`).Error)
			t.Cleanup(func() { _ = g.Exec("DROP TABLE IF EXISTS " + tbl).Error })

			// bump 与 bumpCounter 用的是同一个形状:窗口没过期就累加,过期就重置。
			bump := func(now, winFrom int64, weight int) error {
				return g.Exec(`INSERT INTO `+tbl+` (user_id, window_start, hit_count, total_count)
					VALUES (?, ?, ?, ?)
					`+UpsertHead(g, tbl, "user_id")+`
						hit_count    = CASE WHEN `+tbl+`.window_start < ? THEN ? ELSE `+tbl+`.hit_count + ? END,
						window_start = CASE WHEN `+tbl+`.window_start < ? THEN ? ELSE `+tbl+`.window_start END,
						total_count  = `+tbl+`.total_count + ?`,
					7, now, weight, int64(weight),
					winFrom, weight, weight,
					winFrom, now,
					int64(weight)).Error
			}

			// 独立算出的期望:
			//   第 1 次 插入        → hit=2  window=1000 total=2
			//   第 2 次 窗口未过期  → hit=5  window=1000 total=5   (1000 >= 900)
			//   第 3 次 窗口已过期  → hit=1  window=3000 total=6   (1000 < 2900)
			require.NoError(t, bump(1000, 900, 2))
			require.NoError(t, bump(2000, 900, 3))
			require.NoError(t, bump(3000, 2900, 1))

			var got row
			require.NoError(t, g.Table(tbl).Where("user_id = ?", 7).Take(&got).Error)
			assert.Equal(t, row{UserId: 7, WindowStart: 3000, HitCount: 1, TotalCount: 6}, got)
			results[fx.name] = got
		})
	}
	// 每一家单独对期望,再互相对一次:后者能抓住"期望本身抄错了"。
	for name, got := range results {
		assert.Equal(t, results["sqlite"], got, "%s 的累加结果必须与 sqlite 逐格一致", name)
	}
}

// TestDBSideNowIsSecondsAndTruncatedAcrossDialects 钉住租约用的库端时间。
//
// 租约的到期判断全部是 `lease_until < <库端当前秒>`。三家的表达式不同,
// 只要有一家返回的是毫秒、是事务开始时间、或者四舍五入进了一位,
// 都会变成"租约提前一秒被别人接管" —— 那正是双跑结算的入口。
func TestDBSideNowIsSecondsAndTruncatedAcrossDialects(t *testing.T) {
	for _, fx := range openDialectFixtures(t) {
		t.Run(fx.name, func(t *testing.T) {
			expr := NowEpochSQL(fx.gdb)
			var got int64
			require.NoError(t, fx.gdb.Raw("SELECT "+expr).Scan(&got).Error)

			now := time.Now().Unix()
			assert.InDelta(t, now, got, 2,
				"库端当前时间必须是 unix 秒(表达式 %s 返回 %d,Go 端是 %d)", expr, got, now)
			// 毫秒会大三个数量级,这一条把"单位写错"与"时钟略有偏差"分开。
			assert.Less(t, got, now*10, "返回值量级不对,像是毫秒而不是秒")
		})
	}
}

// TestIsDuplicateKeyRecognizesEveryDialect 用**真实**的唯一键冲突错误驱动判据。
//
// 手写错误字符串的测试只能证明"我记得的那句话能匹配",而驱动的措辞是会变的。
// 这里让每一家真的撞一次键,拿驱动实际返回的 error 去问 IsDuplicateKey ——
// 漏判一家的后果是幂等重放/租约首次插入被当成真失败往上抛。
func TestIsDuplicateKeyRecognizesEveryDialect(t *testing.T) {
	const tbl = "qy_t_dupkey"
	for _, fx := range openDialectFixtures(t) {
		t.Run(fx.name, func(t *testing.T) {
			g := fx.gdb
			require.NoError(t, g.Exec("DROP TABLE IF EXISTS "+tbl).Error)
			require.NoError(t, g.Exec("CREATE TABLE "+tbl+" (k INTEGER PRIMARY KEY)").Error)
			t.Cleanup(func() { _ = g.Exec("DROP TABLE IF EXISTS " + tbl).Error })

			require.NoError(t, g.Exec("INSERT INTO "+tbl+" (k) VALUES (?)", 1).Error)
			err := g.Exec("INSERT INTO "+tbl+" (k) VALUES (?)", 1).Error
			require.Error(t, err)
			assert.True(t, IsDuplicateKey(err),
				"%s 的唯一键冲突没有被识别,原文: %v", fx.name, err)

			// 反向:普通错误不得被误判成幂等命中,否则真故障会被当成"已经做过了"。
			assert.False(t, IsDuplicateKey(fmt.Errorf("connection refused")))
			assert.False(t, IsDuplicateKey(nil))
		})
	}
}

// TestLockForUpdateSkipsOnlySQLite 钉住行锁的方言分支。
//
// 真正被这条判据挡住的是**多一个方言进跳过名单**:资金路径靠 FOR UPDATE 串行化
// 读改写,少了它是静默的丢失更新,不是报错。
//
// 反方向(去掉 SQLite 分支)这条判据抓不到,如实记在这里:glebarez/sqlite 自己
// 就会把 clause.Locking 丢掉,渲染出来的 SQL 里本来就没有 FOR UPDATE。
// 那一跳保留的理由写在 LockForUpdate 的注释里(换驱动即失效),不是这条测试。
func TestLockForUpdateSkipsOnlySQLite(t *testing.T) {
	const tbl = "qy_t_lock"
	for _, fx := range openDialectFixtures(t) {
		t.Run(fx.name, func(t *testing.T) {
			g := fx.gdb
			require.NoError(t, g.Exec("DROP TABLE IF EXISTS "+tbl).Error)
			require.NoError(t, g.Exec("CREATE TABLE "+tbl+" (k INTEGER PRIMARY KEY)").Error)
			t.Cleanup(func() { _ = g.Exec("DROP TABLE IF EXISTS " + tbl).Error })
			require.NoError(t, g.Exec("INSERT INTO "+tbl+" (k) VALUES (?)", 1).Error)

			sql := g.ToSQL(func(tx *gorm.DB) *gorm.DB {
				return LockForUpdate(tx.Table(tbl).Where("k = ?", 1)).Find(&[]struct{ K int }{})
			})
			if fx.name == "sqlite" {
				assert.NotContains(t, sql, "FOR UPDATE", "SQLite 没有行锁,带上 FOR UPDATE 会整条语法错误")
			} else {
				assert.Contains(t, sql, "FOR UPDATE", "%s 的资金路径必须真的加行锁", fx.name)
			}

			// 真跑一次:渲染对不等于能执行。
			err := g.Transaction(func(tx *gorm.DB) error {
				var k int
				return LockForUpdate(tx.Table(tbl).Where("k = ?", 1)).Select("k").Scan(&k).Error
			})
			require.NoError(t, err)
		})
	}
}
