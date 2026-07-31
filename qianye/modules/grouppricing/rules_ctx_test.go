package grouppricing

import (
	"context"
	"testing"

	"github.com/shopspring/decimal"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// rules_ctx_test.go —— M7 在本模块的那一份拷贝的回归防线。
//
// violation/rules.go 与本文件被测的 rules.go 曾是逐字相同的两份:guard.HotAsync 的
// 闭包收到 ctx 却直接调 reload(false),而 reload 的签名里根本没有 context 参数 ——
// ctx 在语法上不可能抵达那两条 SELECT,hot_async_timeout_ms(3 秒)完全落空。
//
// 两个模块的刷新周期同为 60 秒、由同一批 relay 流量触发,天然相位锁定,很容易同时
// 占满仅有的 2 个 hot worker 长达 readTimeout(30 秒);这期间 commission.consume
// 事件会把 4096 槽队列填满并溢出丢弃,而返佣没有 outbox 补偿。
//
// 现有测试跑 SQLite(没有 readTimeout),带不带 ctx 表现完全相同 —— 所以只能用
// **已取消的 ctx** 来观察:接上了就一定失败,漏接就一定成功。

// reloadCtx 的每一条语句都必须挂着调用方的 ctx。
func TestReloadCtx_StatementsCarryCallerBudget(t *testing.T) {
	gdb := newTestDB(t)
	require.NoError(t, gdb.Create(&Rule{
		GroupName: "vip", ModelName: "gpt-4o", Mode: ModeRatio,
		Value:   decimal.RequireFromString("0.5"),
		Enabled: true, CreatedAt: 1, UpdatedAt: 1,
	}).Error)

	require.NoError(t, reloadCtx(context.Background(), true))
	require.NotNil(t, current.Load(), "正常路径必须真的把规则读进快照")

	current.Store(nil)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	err := reloadCtx(ctx, true)

	require.Error(t, err,
		"调用方预算已耗尽时 reload 必须报错 —— 不报错就说明 ctx 没挂到 GORM 语句上,"+
			"hot_async_timeout_ms 对这条链路完全失效")
	assert.ErrorIs(t, err, context.Canceled)
	assert.Nil(t, current.Load(), "加载失败绝不能替换掉已有快照")
}

// 无 ctx 入口(管理端写入后刷新、启动预热)必须自带冷路径预算,
// 而不是退化成没有任何上界的裸查询。
func TestReload_ColdEntryStillLoads(t *testing.T) {
	gdb := newTestDB(t)
	require.NoError(t, gdb.Create(&Rule{
		GroupName: "vip", ModelName: "gpt-4o", Mode: ModeRatio,
		Value:   decimal.RequireFromString("0.5"),
		Enabled: true, CreatedAt: 1, UpdatedAt: 1,
	}).Error)

	require.NoError(t, reload(true))
	require.NotNil(t, current.Load(),
		"冷路径入口必须仍能完成加载:给它加预算不等于把它掐死")
}
