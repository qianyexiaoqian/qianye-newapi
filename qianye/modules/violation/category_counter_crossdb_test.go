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

// bumpCategoryCounter 是**单类型封号线**唯一的推进语句,而它是一条 GORM 的
// OnConflict upsert。upsert 的赋值表达式里如果写裸列名,PostgreSQL 会在
// **计划阶段**就报 `column reference "hit_count" is ambiguous`(42702)——
// 因为目标表与 excluded 伪表同名。计划阶段意味着连第一次、根本没有冲突的
// INSERT 都失败:PG 部署上 qy_violation_cat_counter 一行都写不进去。
//
// 后果不是"报错然后有人看见":调用方(guard.persist)只打一条 SysError 就继续,
// 于是 Record.category_counter_after 恒为 0(管理端"离该类型封号还差几次"
// 永远显示 0)、CatReached 恒 false(运营为某一类单独配的阈值一次都不会触发,
// 而界面上它显示得好好的)、revertCategoryCounter 那条退计数链永远是空操作。
// MySQL 与 SQLite **接受**裸列名,所以这一条只能靠真 PG 抓 —— 单测照绿。
//
// 判据必须对照最终的计数值,不是只看"没报错"。
//
// MySQL / PostgreSQL 由环境变量开(不设就只跑 SQLite):
//
//	QY_TEST_MYSQL_DSN / QY_TEST_PG_DSN
func TestBumpCategoryCounterAdvancesIdenticallyAcrossDialects(t *testing.T) {
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
			require.NoError(t, g.Exec("DROP TABLE IF EXISTS qy_violation_cat_counter").Error)
			t.Cleanup(func() { _ = g.Exec("DROP TABLE IF EXISTS qy_violation_cat_counter").Error })
			return g
		}})
	}

	// 独立算出的期望,三家必须逐格一致(阈值 5,窗口 24 小时):
	//   ① 首次       weight 2 → hit 2 total 2 reached=false  (**建行**,PG 上正是这一步曾经整条失败)
	//   ② 同窗口再来 weight 3 → hit 5 total 5 reached=true   (累加并越线)
	//   ③ 窗口过期后 weight 1 → hit 1 total 6 reached=false  (hit 重置为本次权重,total 只增)
	want := []struct {
		hit     int
		total   int64
		reached bool
	}{{2, 2, false}, {5, 5, true}, {1, 6, false}}

	cat := Category{Id: 4242, Key: "crossdb_probe", Enabled: true, WindowHours: 24, Threshold: 5}

	for _, fx := range fixtures {
		t.Run(fx.name, func(t *testing.T) {
			gdb := fx.open(t)
			require.NoError(t, gdb.AutoMigrate(&CategoryCounter{}))

			const userId = 40142
			for i, w := range []int{2, 3, 1} {
				if i == 2 {
					require.NoError(t, gdb.Model(&CategoryCounter{}).
						Where("user_id = ? AND category_id = ?", userId, cat.Id).
						Update("window_start", int64(1)).Error)
				}
				hit, reached, err := bumpCategoryCounter(context.Background(), gdb, userId, cat, w)
				require.NoError(t, err, "%s 上第 %d 次推进必须成功", fx.name, i+1)
				assert.Equal(t, want[i].hit, hit, "%s 第 %d 次推进的返回 hit", fx.name, i+1)
				assert.Equal(t, want[i].reached, reached, "%s 第 %d 次推进的封号线判定", fx.name, i+1)

				var row CategoryCounter
				require.NoError(t, gdb.Where("user_id = ? AND category_id = ?", userId, cat.Id).
					Take(&row).Error, "%s 第 %d 次推进后必须**存在**这一行", fx.name, i+1)
				assert.Equal(t, want[i].hit, row.HitCount)
				assert.Equal(t, want[i].total, row.TotalCount)
			}
		})
	}
}
