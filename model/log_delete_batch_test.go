package model

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// log_delete_batch_test.go —— 日志清理必须真的按批删。
//
// 这条判据守的是一个跨库缺陷,而且是"全绿地"炸的那一种:
// `Where(...).Limit(n).Delete(&Log{})` 里的 LIMIT 只有 MySQL 驱动会渲染
// (driver/mysql 的 DeleteClauses 含 "LIMIT"),postgres 与 glebarez/sqlite
// 的 DeleteClauses 里没有 LIMIT,GORM **静默**把它丢掉,一条无界 DELETE
// 把整段历史一次删光 —— 返回值仍是成功,任务仍会走完,没有任何报错。
//
// 后果不是"删多了数据"(删除范围与操作意图一致),而是:单个长事务、与删除量
// 成正比的 WAL 与死元组、调用方的 ctx 取消再也无法在批间生效、BatchSize
// 这个配置项在两种受支持的库上完全失效、进度条从 0 直接跳到 100%。
//
// 用例跑在 sqlite 上,而 sqlite 恰好是丢 LIMIT 的那一侧 —— 也就是说这条判据
// 在默认的 `go test ./...` 里就能把缺陷打红,不需要任何 DSN。
func TestDeleteOldLogBatchDeletesOnlyOneBatch(t *testing.T) {
	require.NoError(t, LOG_DB.Exec("DELETE FROM logs").Error)
	t.Cleanup(func() { LOG_DB.Exec("DELETE FROM logs") })

	const (
		cutoff   = 2_000_000_000
		oldRows  = 250
		newRows  = 5
		batch    = 100
		firstId  = 700000
		keepFrom = 800000
	)
	for i := 0; i < oldRows; i++ {
		require.NoError(t, LOG_DB.Create(&Log{
			Id: firstId + i, UserId: 1, CreatedAt: cutoff - 1000, Type: LogTypeConsume,
		}).Error)
	}
	for i := 0; i < newRows; i++ {
		require.NoError(t, LOG_DB.Create(&Log{
			Id: keepFrom + i, UserId: 1, CreatedAt: cutoff + 1000, Type: LogTypeConsume,
		}).Error)
	}

	ctx := context.Background()

	// ① 第一批:恰好 batch 行,一行不多。多删的表现就是 LIMIT 被丢掉了。
	affected, err := DeleteOldLogBatch(ctx, cutoff, batch)
	require.NoError(t, err)
	assert.Equal(t, int64(batch), affected, "一次调用只许删一批;LIMIT 被方言丢掉时这里会是 250")

	remaining, err := CountOldLog(ctx, cutoff)
	require.NoError(t, err)
	assert.Equal(t, int64(oldRows-batch), remaining)

	// ② 剩下的按批走完,总数守恒,cutoff 之后的行一条不许动。
	total := affected
	for {
		affected, err = DeleteOldLogBatch(ctx, cutoff, batch)
		require.NoError(t, err)
		if affected == 0 {
			break
		}
		assert.LessOrEqual(t, affected, int64(batch), "每一批都不许超过批大小")
		total += affected
	}
	assert.Equal(t, int64(oldRows), total)

	remaining, err = CountOldLog(ctx, cutoff)
	require.NoError(t, err)
	assert.Zero(t, remaining, "cutoff 之前的行必须全部删完")

	var survivors int64
	require.NoError(t, LOG_DB.Model(&Log{}).Count(&survivors).Error)
	assert.Equal(t, int64(newRows), survivors, "cutoff 之后的行一条都不许被误删")
}

// 批大小大于剩余行数时,一次删完并如实报数(不能因为 IN 列表短了就少删或报错)。
func TestDeleteOldLogBatchHandlesBatchLargerThanRemaining(t *testing.T) {
	require.NoError(t, LOG_DB.Exec("DELETE FROM logs").Error)
	t.Cleanup(func() { LOG_DB.Exec("DELETE FROM logs") })

	const cutoff = 2_000_000_000
	for i := 0; i < 7; i++ {
		require.NoError(t, LOG_DB.Create(&Log{
			Id: 710000 + i, UserId: 1, CreatedAt: cutoff - 1, Type: LogTypeConsume,
		}).Error)
	}

	ctx := context.Background()
	affected, err := DeleteOldLogBatch(ctx, cutoff, 100)
	require.NoError(t, err)
	assert.Equal(t, int64(7), affected)

	affected, err = DeleteOldLogBatch(ctx, cutoff, 100)
	require.NoError(t, err)
	assert.Zero(t, affected, "没有可删的行时必须返回 0 而不是报错")
}
