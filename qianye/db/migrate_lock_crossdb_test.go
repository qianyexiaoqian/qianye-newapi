package db

import (
	"context"
	"database/sql"
	"os"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/QuantumNous/new-api/qianye/config"
)

// 迁移互斥锁的行为判据。
//
// 这把锁是"多 master 并发 AutoMigrate"的唯一防线,而 MySQL 与 PostgreSQL 的
// 实现在每一个维度上都不同(键类型、超时、作用域、死锁检测,逐条对照见
// acquireMigrateLock 的文档)。只有"净效果"被刻意对齐了,所以判据只能打在净效果上:
//
//	① 第一个抢锁的会拿到;
//	② 第二条连接在锁被持有期间抢不到,且**不会**永远阻塞(PostgreSQL 的
//	   pg_advisory_lock 是无限等待的,轮询 try 版是唯一能复刻 MySQL 超时语义的写法);
//	③ 释放之后别人立刻能拿到;
//	④ 持锁连接关闭时锁自动释放(两家都靠这个兜底,否则一次进程崩溃会把
//	   整个集群的迁移永久堵死)。
//
// ② 用一个很短的超时跑,否则一次运行要等 30 秒。
func TestMigrateLockMutualExclusionAcrossDialects(t *testing.T) {
	cases := []struct {
		name string
		env  string
	}{
		{"mysql", "QY_TEST_MYSQL_DSN"},
		{"postgres", "QY_TEST_PG_DSN"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			dsn := os.Getenv(tc.env)
			if dsn == "" {
				t.Skipf("未设置 %s,跳过(锁语义只能在真库上验证)", tc.env)
			}
			dialect := DetectDialect(dsn)

			pool, err := openMigrationConnWith(config.Database{DSN: dsn})
			require.NoError(t, err)
			t.Cleanup(func() { _ = pool.Close() })

			ctx := context.Background()
			holder, err := pool.Conn(ctx)
			require.NoError(t, err)

			got, err := acquireMigrateLock(ctx, holder, dialect)
			require.NoError(t, err)
			require.True(t, got, "第一个抢锁的必须拿到")

			// ② 第二条连接抢不到,而且必须在有界时间内返回。
			rival, err := pool.Conn(ctx)
			require.NoError(t, err)
			bounded, cancel := context.WithTimeout(ctx, 3*time.Second)
			start := time.Now()
			got2, err := acquireMigrateLock(bounded, rival, dialect)
			cancel()
			require.NoError(t, err)
			assert.False(t, got2, "锁被别人持有时不得抢到")
			assert.Less(t, time.Since(start), 10*time.Second,
				"抢不到必须有界返回;PostgreSQL 的阻塞版咨询锁会永远等下去")

			// ③ 释放之后立刻可得。
			releaseMigrateLock(ctx, holder, dialect)
			got3, err := acquireMigrateLock(ctx, rival, dialect)
			require.NoError(t, err)
			assert.True(t, got3, "释放之后别的节点必须能立刻接手")

			// ④ 持锁连接关闭 = 自动释放。这是两家共同的兜底语义,
			// 没有它,一次进程崩溃会让整个集群再也做不了迁移。
			require.NoError(t, rival.Close())
			third, err := pool.Conn(ctx)
			require.NoError(t, err)
			defer third.Close()
			got4, err := acquireMigrateLockAfterConnDrop(ctx, third, dialect)
			require.NoError(t, err)
			assert.True(t, got4, "持锁连接关闭之后锁必须自动释放")
			releaseMigrateLock(ctx, third, dialect)
			require.NoError(t, holder.Close())
		})
	}
}

// acquireMigrateLockAfterConnDrop 给"连接关闭 → 自动释放"留出一点时间。
//
// 两家释放锁的时机都在服务端处理连接终止的那一刻,不是 Close() 返回的那一刻;
// 直接抢会偶发地抢在服务端清理之前。重试而不是 sleep 固定时长:
// 前者在快的机器上不浪费时间,在慢的机器上也不会假失败。
func acquireMigrateLockAfterConnDrop(ctx context.Context, conn *sql.Conn, dialect Dialect) (bool, error) {
	deadline := time.Now().Add(5 * time.Second)
	for {
		got, err := acquireMigrateLock(ctx, conn, dialect)
		if err != nil || got || time.Now().After(deadline) {
			return got, err
		}
		time.Sleep(100 * time.Millisecond)
	}
}
