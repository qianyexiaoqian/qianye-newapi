package transfer

import (
	"context"
	"os"
	"testing"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/qianye/config"
	qydb "github.com/QuantumNous/new-api/qianye/db"

	"github.com/glebarez/sqlite"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
	gormlogger "gorm.io/gorm/logger"
)

// pruneLookupLogs 曾经写成 `Where(...).Limit(1000).Delete(&LookupLog{})`。
//
// GORM 只有在方言把 "LIMIT" 列进 DeleteClauses 时才渲染 DELETE 上的 LIMIT,
// 而只有 MySQL 驱动这么做 —— postgres 驱动**静默丢掉** LIMIT。于是 PG 部署上
// "按批清理"退化成一条无界 DELETE:第一次跨过保留期时把过期段一次删光,
// 单个长事务、与删除量成正比的 WAL 与死元组、随后一轮 autovacuum 突刺,
// 而且租约的 ctx 取消在删除中途不再有任何作用。不报错、不告警,唯一的痕迹
// 是 SysLog 里一个远超批量上界的数字。
//
// 这条判据必须在**真库**上跑:SQLite 与 MySQL 上"按批"天然成立(前者
// glebarez 驱动也丢 LIMIT,但它不是受支持的部署方言),差异只有 PG 能抓。
//
//	QY_TEST_MYSQL_DSN / QY_TEST_PG_DSN
func TestPruneLookupLogsHonorsItsBatchBoundOnEveryDialect(t *testing.T) {
	// 保留期显式配 30 天:<=0 表示关掉清理,那样本用例会永远绿。
	prevCfg := qyConfig.Swap(&config.Config{
		Enabled:  true,
		Transfer: config.Transfer{Enabled: true, LookupLogRetainDays: 30},
	})
	t.Cleanup(func() { qyConfig.Store(prevCfg) })

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
			require.NoError(t, g.Exec("DROP TABLE IF EXISTS qy_transfer_lookup_logs").Error)
			t.Cleanup(func() { _ = g.Exec("DROP TABLE IF EXISTS qy_transfer_lookup_logs").Error })
			return g
		}})
	}

	// 独立算出的期望:批量上界 1000,种 1500 条全部过期的行。
	//   第一轮后剩 500(不是 0);第二轮后剩 0;第三轮空转不报错。
	const seedRows = pruneLookupLogBatch + 500

	for _, fx := range fixtures {
		t.Run(fx.name, func(t *testing.T) {
			gdb := fx.open(t)
			require.NoError(t, gdb.AutoMigrate(&LookupLog{}))
			prev := qyDBHandle.Swap(gdb)
			t.Cleanup(func() { qyDBHandle.Store(prev) })

			old := common.GetTimestamp() - 400*86400
			rows := make([]LookupLog, 0, seedRows)
			for i := 0; i < seedRows; i++ {
				rows = append(rows, LookupLog{UserId: 1, Identifier: "probe", ByType: "id", CreatedAt: old})
			}
			require.NoError(t, gdb.CreateInBatches(rows, 200).Error)

			count := func() int64 {
				var n int64
				require.NoError(t, gdb.Model(&LookupLog{}).Count(&n).Error)
				return n
			}
			require.Equal(t, int64(seedRows), count())

			pruneLookupLogs(context.Background(), gdb)
			assert.Equal(t, int64(seedRows-pruneLookupLogBatch), count(),
				"%s:一轮最多删 %d 条 —— 一次删光说明 LIMIT 被方言丢掉了",
				fx.name, pruneLookupLogBatch)

			pruneLookupLogs(context.Background(), gdb)
			assert.Equal(t, int64(0), count(), "%s:第二轮应把剩下的删完", fx.name)

			pruneLookupLogs(context.Background(), gdb)
			assert.Equal(t, int64(0), count(), "%s:空转不该出错", fx.name)
		})
	}
}
