package availability

import (
	"context"
	"os"
	"testing"

	"github.com/glebarez/sqlite"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
	gormlogger "gorm.io/gorm/logger"

	qydb "github.com/QuantumNous/new-api/qianye/db"
)

// deleteBefore 曾经写的是 `DELETE ... WHERE bucket_ts < ? LIMIT ?`。
//
// DELETE 上的 LIMIT 是 MySQL 专有扩展,PostgreSQL 直接语法错误。而这条路径的
// 错误只走 warnThrottled 进日志,表现是**可用率桶表永远不清理** ——
// 5 分钟桶每天十几万行,几个月后是这张库最大的表,而没有任何告警说过它。
//
// 判据必须同时覆盖两件事,少一件都能被"把清理整段删掉"骗过去:
//
//	① 到期的行确实被删掉了;
//	② 未到期的行一行都没少(改成分批取主键之后,取错列或删错条件的表现正是"多删")。
func TestDeleteBeforeRemovesOnlyExpiredRowsAcrossDialects(t *testing.T) {
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
			require.NoError(t, g.Migrator().DropTable(&Bucket{}))
			t.Cleanup(func() { _ = g.Migrator().DropTable(&Bucket{}) })
			return g
		}})
	}

	for _, fx := range fixtures {
		t.Run(fx.name, func(t *testing.T) {
			gdb := fx.open(t)
			require.NoError(t, gdb.AutoMigrate(&Bucket{}))

			// 300 / 600 过期,900 / 1200 不过期(cutoff = 900)。
			for i, ts := range []int64{300, 600, 900, 1200} {
				require.NoError(t, gdb.Create(&Bucket{
					BucketTs: ts, GroupName: "g", ModelName: "m" + string(rune('a'+i)),
				}).Error)
			}

			deleteBefore(context.Background(), gdb, bucketTable, 900)

			var left []int64
			require.NoError(t, gdb.Model(&Bucket{}).Order("bucket_ts").Pluck("bucket_ts", &left).Error)
			assert.Equal(t, []int64{900, 1200}, left,
				"%s:cutoff 之前的桶必须清掉,之后的一行都不能少", fx.name)
		})
	}
}
