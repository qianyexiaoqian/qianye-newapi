package model

import (
	"os"
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
	gormlogger "gorm.io/gorm/logger"

	"github.com/QuantumNous/new-api/qianye/db"
)

// gateProbe 是一张只在本测试里存在的表,充当"被闸门保护的资源"。
type gateProbe struct {
	Id int64 `gorm:"primaryKey;autoIncrement"`
}

func (gateProbe) TableName() string { return "qy_t_gate_probe" }

// LockGate 必须让「先数一下再决定放不放行」在并发下也只放行到上限为止。
//
// # 为什么必须跑真库、且必须并发
//
// 这道闸门原来靠的是 `SELECT COUNT(*) ... FOR UPDATE`,而那句话的正确性完全
// 建立在 MySQL InnoDB 的 next-key 锁上。它在另外两种方言上以两种不同的方式失效
// (PostgreSQL 直接拒绝聚合上的 FOR UPDATE;拆开写又不阻止并发插入),
// 而**两种失效都不是单线程能观察到的**:顺序执行时计数永远是对的。
//
// 断言是【精确条数】而不是「不超过上限」:后者在"所有写入都因为别的原因失败"
// 时同样为真,那是一个假绿。
func TestLockGateSerializesCountThenInsert(t *testing.T) {
	const cap = 5
	const workers = 12

	for _, tc := range []struct{ name, env string }{
		{"mysql", "QY_TEST_MYSQL_DSN"},
		{"postgres", "QY_TEST_PG_DSN"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dsn := os.Getenv(tc.env)
			if dsn == "" {
				t.Skipf("未设置 %s,跳过(闸门的正确性只有真库的并发才能观察到)", tc.env)
			}
			dialector, err := db.DialectorFor(dsn)
			require.NoError(t, err)
			gdb, err := gorm.Open(dialector, &gorm.Config{
				Logger: gormlogger.Discard,
				// 与 db.Init 一致:单条语句不自动包事务,否则被测事务的边界与生产不同。
				SkipDefaultTransaction: true,
			})
			require.NoError(t, err)
			sqlDB, err := gdb.DB()
			require.NoError(t, err)
			// 连接数必须大于并发数:少一条就会有协程在连接池里排队,
			// 那种"串行"是测试自己造出来的,会把闸门失效掩盖过去。
			sqlDB.SetMaxOpenConns(workers + 4)

			require.NoError(t, gdb.Migrator().DropTable(&gateProbe{}, &KV{}))
			require.NoError(t, gdb.AutoMigrate(&gateProbe{}, &KV{}))
			t.Cleanup(func() { _ = gdb.Migrator().DropTable(&gateProbe{}, &KV{}) })

			var wg sync.WaitGroup
			errs := make([]error, workers)
			for i := 0; i < workers; i++ {
				wg.Add(1)
				go func(i int) {
					defer wg.Done()
					errs[i] = gdb.Transaction(func(tx *gorm.DB) error {
						if err := LockGate(tx, "test:cap"); err != nil {
							return err
						}
						var n int64
						if err := tx.Model(&gateProbe{}).Count(&n).Error; err != nil {
							return err
						}
						if n >= cap {
							return nil // 闸门拦下,这不是错误
						}
						return tx.Create(&gateProbe{}).Error
					})
				}(i)
			}
			wg.Wait()
			for i, err := range errs {
				require.NoErrorf(t, err, "第 %d 个并发请求出错;闸门应当靠排队而不是靠报错生效", i)
			}

			var got int64
			require.NoError(t, gdb.Model(&gateProbe{}).Count(&got).Error)
			assert.EqualValues(t, cap, got,
				"%s:%d 个并发请求抢一个上限 %d 的闸门,最终必须恰好 %d 条",
				tc.name, workers, cap, cap)
		})
	}
}
