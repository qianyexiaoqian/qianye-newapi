package commission

import (
	"context"
	"os"
	"testing"

	qydb "github.com/QuantumNous/new-api/qianye/db"

	"github.com/glebarez/sqlite"
	"github.com/shopspring/decimal"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
	gormlogger "gorm.io/gorm/logger"
)

// writeAccrual 的返回值 inserted 区分"新建"与"幂等命中",三个调用点靠它决定
// 要不要写审计、要不要计冲正次数、要不要 409 拒绝重放。
//
// 它曾经写成 `res.RowsAffected == 1`,而这条判据的口径三家**不一致**:
//
//	MySQL      ON DUPLICATE KEY UPDATE 命中 → 2
//	PostgreSQL ON CONFLICT DO UPDATE  命中 → 1   ← 与新插入同值
//	三家        ON CONFLICT DO NOTHING 命中 → 0
//
// 也就是说日聚合桶(Accumulate)那条路在 PostgreSQL 上会把一次冲突累加判成
// "新插入"。当时唯一的 Accumulate 调用方丢弃了这个 bool,所以线上没有症状 ——
// 但那正是本包 testdb_test.go 顶部写下"刻意不覆盖那条分支"的原因,而
// "不覆盖"意味着下一个开始消费返回值的人不会得到任何提示。
//
// 修法是把两种模式都收敛到 DoNothing(三家统一)+ 冲突时补一条 UPDATE 累加,
// 于是 inserted 在三种数据库上都是精确的,这条分支也终于可以被覆盖。
//
//	QY_TEST_MYSQL_DSN / QY_TEST_PG_DSN
func TestWriteAccrualInsertedFlagIsExactOnEveryDialect(t *testing.T) {
	type fixture struct {
		name string
		open func(*testing.T) *gorm.DB
	}
	fixtures := []fixture{{
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
	for _, env := range []struct{ name, key string }{
		{"mysql", "QY_TEST_MYSQL_DSN"}, {"postgres", "QY_TEST_PG_DSN"},
	} {
		dsn := os.Getenv(env.key)
		if dsn == "" {
			continue
		}
		fixtures = append(fixtures, fixture{name: env.name, open: func(t *testing.T) *gorm.DB {
			d, err := qydb.DialectorFor(dsn)
			require.NoError(t, err)
			g, err := gorm.Open(d, &gorm.Config{Logger: gormlogger.Discard})
			require.NoError(t, err)
			require.NoError(t, g.Exec("DROP TABLE IF EXISTS qy_commission_accrual").Error)
			t.Cleanup(func() { _ = g.Exec("DROP TABLE IF EXISTS qy_commission_accrual").Error })
			return g
		}})
	}

	for _, fx := range fixtures {
		t.Run(fx.name, func(t *testing.T) {
			gdb := fx.open(t)
			require.NoError(t, gdb.AutoMigrate(&Accrual{}))
			prev := qyDBHandle.Swap(gdb)
			t.Cleanup(func() { qyDBHandle.Store(prev) })

			mk := func(key string, accumulate bool) accrualInput {
				return accrualInput{
					IdemKey:    key,
					SourceType: "consume", InviterId: 901, InviteeId: 902,
					BaseQuota: 1000, Gross: decimal.NewFromInt(50),
					RateUnits: 500, RateGroup: "default",
					UsdRate:    decimal.NewFromInt(7),
					Accumulate: accumulate,
				}
			}

			t.Run("累加模式:第一次是新建,第二次是命中并且真的累加了", func(t *testing.T) {
				ins1, err := writeAccrual(context.Background(), mk("acc-1", true))
				require.NoError(t, err)
				assert.True(t, ins1, "第一次必须报为新建")

				ins2, err := writeAccrual(context.Background(), mk("acc-1", true))
				require.NoError(t, err)
				assert.False(t, ins2, "第二次是幂等命中,绝不能报为新建 —— "+
					"PostgreSQL 的 ON CONFLICT DO UPDATE 命中同样返回 RowsAffected=1")

				var row Accrual
				require.NoError(t, gdb.Where("idem_key = ?", "acc-1").
					Take(&row).Error)
				// 独立算出的期望:两次各 1000 基数 / 50 佣金 → 2000 / 100。
				assert.Equal(t, int64(2000), row.BaseQuota, "累加语义不能因为改写而丢")
				assert.True(t, decimal.NewFromInt(100).Equal(row.GrossAmount),
					"gross 应为 100,实际 %s", row.GrossAmount)

				var n int64
				require.NoError(t, gdb.Model(&Accrual{}).
					Where("idem_key = ?", "acc-1").Count(&n).Error)
				assert.Equal(t, int64(1), n, "幂等键必须只有一行")
			})

			t.Run("非累加模式:命中就是命中,金额一分不动", func(t *testing.T) {
				ins1, err := writeAccrual(context.Background(), mk("noacc-1", false))
				require.NoError(t, err)
				assert.True(t, ins1)

				ins2, err := writeAccrual(context.Background(), mk("noacc-1", false))
				require.NoError(t, err)
				assert.False(t, ins2)

				var row Accrual
				require.NoError(t, gdb.Where("idem_key = ?", "noacc-1").
					Take(&row).Error)
				assert.Equal(t, int64(1000), row.BaseQuota, "DoNothing 不许改动已有行")
				assert.True(t, decimal.NewFromInt(50).Equal(row.GrossAmount))
			})
		})
	}
}
