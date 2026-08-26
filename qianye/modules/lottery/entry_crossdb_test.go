package lottery

import (
	"os"
	"testing"

	qydb "github.com/QuantumNous/new-api/qianye/db"
	qymodel "github.com/QuantumNous/new-api/qianye/model"

	"github.com/glebarez/sqlite"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
	gormlogger "gorm.io/gorm/logger"
)

// 本模块此前**一条真实方言的测试都没有**:47 个测试文件里 0 个引用
// QY_TEST_MYSQL_DSN / QY_TEST_PG_DSN,全部跑在 SQLite 上,而
// qianye/db/dialect.go 明确**拒绝** SQLite 作为扩展库(「资金路径依赖行锁,
// SQLite 无法提供」)。也就是说这个模块唯一被测过的方言恰恰是生产上永远
// 不会用的那一种,而它真正运行的两种从未被自动化覆盖。
// 对照:violation 5 个 crossdb 文件、commission / transfer / withdraw 各 1 个,
// lottery 是唯一一个 0。
//
// 这一条先补上最要命的那一块:幂等键的唯一索引在三方言上必须**同一个口径**。
// 它不是假想 —— 同一对只差大小写的 client_request_id,MySQL 的库默认排序规则
// (0900_ai_ci / general_ci)判成同一个键,PostgreSQL / SQLite 按字节比较判成
// 两个,于是同一份代码在两种官方支持的方言上对同一对资金请求给出相反的答案:
// 一边静默吞掉第二笔,一边正常扣款出票。
//
//	QY_TEST_MYSQL_DSN / QY_TEST_PG_DSN
func TestEntryIdemKeyUniquenessAgreesOnEveryDialect(t *testing.T) {
	for _, fx := range lotteryCrossDBFixtures(t) {
		t.Run(fx.name, func(t *testing.T) {
			gdb := fx.open(t)
			require.NoError(t, gdb.AutoMigrate(&Entry{}))

			mk := func(no, idem string, seq int) *Entry {
				return &Entry{
					ActId: 1, EntryNo: no, Seq: seq, UserId: 42,
					Amount: 1000, Status: EntrySuccess, IdemKey: idem,
				}
			}
			// 规范化之后客户端那一段只剩 [a-z0-9_-],所以库里永远不会同时存在
			// 只差大小写的两个键 —— 这一条把「规范化之后的键在三方言上表现一致」
			// 钉住:同一个键第二次插入必须冲突,不同的键必须都插得进去。
			require.NoError(t, gdb.Create(mk("LE-CD-1", "lt-cd:case-a", 1)).Error)

			err := gdb.Create(mk("LE-CD-2", "lt-cd:case-a", 2)).Error
			require.Error(t, err, "同一个幂等键必须撞唯一索引 —— 这是不重复扣钱的唯一保证")

			require.NoError(t, gdb.Create(mk("LE-CD-3", "lt-cd:case-b", 3)).Error,
				"不同的幂等键必须都插得进去")

			var n int64
			require.NoError(t, gdb.Model(&Entry{}).Where("act_id = ?", 1).Count(&n).Error)
			assert.EqualValues(t, 2, n)

			// 规范化本身在这里再走一遍:进库的永远是折叠过的形式,
			// 所以 MySQL 的大小写不敏感排序规则再也没有可折叠的东西。
			folded, ok := qymodel.NormalizeIdemClientKey("CASE-A")
			require.True(t, ok)
			assert.Equal(t, "case-a", folded)
			assert.True(t, qymodel.IsCollationNeutralIdemKey("lt-cd:"+folded))
		})
	}
}

// 参与金额与奖池必须在三方言上都用 64 位承载 —— 这是 common.MaxQuota 那条
// 算术上界的存放前提,而它此前只在主库的 model 包里被举证过,扩展库这一侧
// (qy_lot_entry.amount / qy_lot_activity.pool_quota)从来没有。
func TestLotteryMoneyColumnsAre64BitOnEveryDialect(t *testing.T) {
	wide := map[string]map[string]bool{
		"sqlite":   {"integer": true},
		"mysql":    {"bigint": true},
		"postgres": {"int8": true, "bigint": true},
	}
	for _, fx := range lotteryCrossDBFixtures(t) {
		t.Run(fx.name, func(t *testing.T) {
			gdb := fx.open(t)
			require.NoError(t, gdb.AutoMigrate(&Entry{}, &Activity{}, &Series{}))
			for _, col := range []struct{ table, column string }{
				{Entry{}.TableName(), "amount"},
				{Activity{}.TableName(), "pool_quota"},
				{Activity{}.TableName(), "payout_quota"},
				{Series{}.TableName(), "pool_quota"},
				{Series{}.TableName(), "issue_cap_quota"},
			} {
				types, err := gdb.Migrator().ColumnTypes(col.table)
				require.NoError(t, err)
				found := false
				for _, ct := range types {
					if ct.Name() != col.column {
						continue
					}
					found = true
					got := lowerASCII(ct.DatabaseTypeName())
					assert.Truef(t, wide[fx.name][got],
						"%s.%s 建出来的是 %s —— 额度上界 common.MaxQuota = 2^43 装不进 32 位",
						col.table, col.column, got)
				}
				assert.Truef(t, found, "%s.%s 必须存在", col.table, col.column)
			}
		})
	}
}

type lotteryCrossDBFixture struct {
	name string
	open func(*testing.T) *gorm.DB
}

func lotteryCrossDBFixtures(t *testing.T) []lotteryCrossDBFixture {
	t.Helper()
	out := []lotteryCrossDBFixture{{
		name: "sqlite",
		open: func(t *testing.T) *gorm.DB {
			g, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{Logger: gormlogger.Discard})
			require.NoError(t, err)
			sqlDB, err := g.DB()
			require.NoError(t, err)
			sqlDB.SetMaxOpenConns(1)
			g.ClauseBuilders["FOR"] = func(clause.Clause, clause.Builder) {}
			t.Cleanup(func() { _ = sqlDB.Close() })
			return g
		},
	}}
	tables := []string{Entry{}.TableName(), Activity{}.TableName(), Series{}.TableName()}
	for _, env := range []struct{ name, key string }{
		{"mysql", "QY_TEST_MYSQL_DSN"}, {"postgres", "QY_TEST_PG_DSN"},
	} {
		dsn := os.Getenv(env.key)
		if dsn == "" {
			continue
		}
		out = append(out, lotteryCrossDBFixture{name: env.name, open: func(t *testing.T) *gorm.DB {
			d, err := qydb.DialectorFor(dsn)
			require.NoError(t, err)
			g, err := gorm.Open(d, &gorm.Config{Logger: gormlogger.Discard})
			require.NoError(t, err)
			drop := func() {
				for _, tbl := range tables {
					_ = g.Exec("DROP TABLE IF EXISTS " + tbl).Error
				}
			}
			drop()
			t.Cleanup(drop)
			return g
		}})
	}
	return out
}

func lowerASCII(s string) string {
	b := []byte(s)
	for i := range b {
		if b[i] >= 'A' && b[i] <= 'Z' {
			b[i] += 'a' - 'A'
		}
	}
	return string(b)
}
