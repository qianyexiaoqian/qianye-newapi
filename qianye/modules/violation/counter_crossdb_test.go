package violation

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

// bumpCounter 是**唯一**推进自动封号判据的语句,而它是本模块里唯一一条手写的
// upsert:窗口过期判断与累加都塞在一条原子语句里(见 bumpCounter 的说明)。
//
// 手写 upsert 意味着三家方言的差异全都落在这一条语句上:
//
//   - 冲突子句头部:MySQL 是 ON DUPLICATE KEY UPDATE(不接受冲突列),
//     PostgreSQL / SQLite 必须写出冲突目标;
//   - 条件表达式:IF(c,a,b) 是 MySQL 专有,CASE WHEN 才是三家交集;
//   - SET 右侧的列引用:PostgreSQL 上裸列名会因为目标表与 excluded 伪表同名
//     直接报 "column reference is ambiguous",整条语句失败。
//
// 这三处任何一处写回 MySQL 形态,后果都不是"报错然后有人看见" ——
// 调用方(guard.persist)只把错误写进日志,表现是**违规计数永远不涨、
// 自动封号在 PostgreSQL 上整体静默失效**。所以判据必须在真库上跑,
// 而且必须对照最终的计数值,不是只看"没报错"。
//
// MySQL / PostgreSQL 由环境变量开(不设就只跑 SQLite):
//
//	QY_TEST_MYSQL_DSN / QY_TEST_PG_DSN
func TestBumpCounterAdvancesIdenticallyAcrossDialects(t *testing.T) {
	useTestConfig(t, "  enabled: true\n  auto_ban_threshold: 100\n")

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
			require.NoError(t, g.Exec("DROP TABLE IF EXISTS qy_violation_counter").Error)
			t.Cleanup(func() { _ = g.Exec("DROP TABLE IF EXISTS qy_violation_counter").Error })
			return g
		}})
	}

	// 独立算出的期望,三家必须逐格一致:
	//   ① 首次       weight 2 → hit 2  total 2  (建行)
	//   ② 同窗口再来 weight 3 → hit 5  total 5  (累加,窗口不动)
	//   ③ 窗口过期后 weight 1 → hit 1  total 6  (hit 重置为本次权重,total 只增)
	want := []struct {
		hit   int
		total int64
	}{{2, 2}, {5, 5}, {1, 6}}

	for _, fx := range fixtures {
		t.Run(fx.name, func(t *testing.T) {
			gdb := fx.open(t)
			require.NoError(t, gdb.AutoMigrate(&Counter{}))

			const userId = 40041
			for i, w := range []int{2, 3, 1} {
				if i == 2 {
					// 把窗口起点推回到远古,让下一次 bump 落进"窗口已过期"分支。
					require.NoError(t, gdb.Model(&Counter{}).Where("user_id = ?", userId).
						Update("window_start", int64(1)).Error)
				}
				st, err := bumpCounter(context.Background(), gdb, userId, w, "")
				require.NoError(t, err, "%s 上第 %d 次推进必须成功", fx.name, i+1)
				assert.Equal(t, want[i].hit, st.HitCount,
					"%s 第 %d 次推进后的 hit_count", fx.name, i+1)

				var row Counter
				require.NoError(t, gdb.Where("user_id = ?", userId).Take(&row).Error)
				assert.Equal(t, want[i].hit, row.HitCount)
				assert.Equal(t, want[i].total, row.TotalCount)
			}
		})
	}
}
