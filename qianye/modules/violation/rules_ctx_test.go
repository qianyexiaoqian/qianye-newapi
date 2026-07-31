package violation

import (
	"context"
	"path/filepath"
	"sync/atomic"
	"testing"
	_ "unsafe" // //go:linkname 需要

	"github.com/glebarez/sqlite"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
	gormlogger "gorm.io/gorm/logger"
)

// rules_ctx_test.go —— M7 的回归防线:规则热刷新必须把调用方的预算带到 GORM 语句上。
//
// 缺陷原样:guard.HotAsync 的闭包收到 ctx 却直接调 reload(false),而 reload 的签名里
// 根本没有 context 参数 —— ctx 在语法上不可能抵达那两条 SELECT。
//
// 为什么这不只是"一条语句慢":本模块与 grouppricing 的刷新周期同为 60 秒、由同一批
// relay 流量触发,天然相位锁定,很容易同时占满仅有的 2 个 hot worker 长达
// readTimeout(30 秒)。这期间 commission.consume 事件把 4096 槽队列填满并溢出丢弃,
// 而 guard 自己的注释写着"丢弃是『用户该拿的钱没拿到』的唯一路径",返佣没有 outbox 补偿。
//
// 现有测试完全无法覆盖它:它们跑 SQLite,而 SQLite 没有 readTimeout,一条不带 ctx 的
// 查询与一条带 ctx 的查询在测试里表现完全相同。所以这里必须用**已取消的 ctx**
// 来观察 —— 接上了就一定失败,漏接就一定成功。

//go:linkname qyDBHandleForCtxTest github.com/QuantumNous/new-api/qianye/db.handle
var qyDBHandleForCtxTest atomic.Pointer[gorm.DB]

// newRuleDB 建一个只承载规则表的测试库并接到 db.Get(),顺带塞一条启用规则。
func newRuleDB(t *testing.T) *gorm.DB {
	t.Helper()
	dsn := filepath.Join(t.TempDir(), "qy_ext.db") +
		"?_pragma=journal_mode(WAL)&_pragma=busy_timeout(5000)"
	gdb, err := gorm.Open(sqlite.Open(dsn), &gorm.Config{Logger: gormlogger.Discard})
	require.NoError(t, err)
	sqlDB, err := gdb.DB()
	require.NoError(t, err)
	require.NoError(t, gdb.AutoMigrate(&Rule{}, &RuleVersion{}))
	require.NoError(t, gdb.Create(&Rule{
		Name: "ctx 探针规则", Enabled: true, Priority: 1,
		Phase: PhasePrompt, MatchType: MatchKeyword, Pattern: "违禁词",
		Action: ActionRecord, FeeMode: FeeNone, CountWeight: 1,
		CreatedAt: 1, UpdatedAt: 1,
	}).Error)

	prev := qyDBHandleForCtxTest.Swap(gdb)
	prevSnap := current.Load()
	current.Store(nil)
	t.Cleanup(func() {
		qyDBHandleForCtxTest.Store(prev)
		current.Store(prevSnap)
		nextRefreshAt.Store(0)
		_ = sqlDB.Close()
	})
	return gdb
}

// reloadCtx 的每一条语句都必须挂着调用方的 ctx。
func TestReloadCtx_StatementsCarryCallerBudget(t *testing.T) {
	newRuleDB(t)

	require.NoError(t, reloadCtx(context.Background(), true))
	require.True(t, Snapshot().hasPrompt(), "正常路径必须真的把规则读进快照")

	current.Store(nil)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	err := reloadCtx(ctx, true)

	require.Error(t, err,
		"调用方预算已耗尽时 reload 必须报错 —— 不报错就说明 ctx 没挂到 GORM 语句上,"+
			"hot_async_timeout_ms 对这条链路完全失效")
	assert.ErrorIs(t, err, context.Canceled)
	assert.False(t, Snapshot().hasPrompt(), "加载失败绝不能替换掉已有快照")
}

// 无 ctx 入口(管理端写入后刷新、启动预热)必须自带冷路径预算,
// 而不是退化成没有任何上界的裸查询。
func TestReload_ColdEntryStillLoads(t *testing.T) {
	newRuleDB(t)

	require.NoError(t, reload(true))
	assert.True(t, Snapshot().hasPrompt(),
		"冷路径入口必须仍能完成加载:给它加预算不等于把它掐死")
}
