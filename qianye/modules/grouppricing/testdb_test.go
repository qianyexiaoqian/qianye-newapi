package grouppricing

import (
	"context"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	_ "unsafe" // //go:linkname 需要

	"github.com/QuantumNous/new-api/qianye/config"
	relaycommon "github.com/QuantumNous/new-api/relay/common"

	"github.com/glebarez/sqlite"
	"github.com/shopspring/decimal"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
	gormlogger "gorm.io/gorm/logger"
)

// 本文件是 grouppricing 包的测试脚手架。
//
// 为什么必须让规则真的过一遍数据库:本扩展被审计出的资损缺陷全都**不在算术里,
// 而在调度层与配置消费层** —— 纯函数写对了、把修复整段回滚测试照样全绿。
// 这里的缓存回落、版本号推进、写入后刷新,每一条都只有在真的读写一次库时
// 才能被证伪。

// qyDBHandle 指向 qianye/db 包里的连接句柄。
//
// reload / flushShadow / buildShadowSummary 全都通过 db.Get() 自取句柄、
// 不接受注入,而 db.Init 只会拨 MySQL。用 //go:linkname 把那个句柄借出来,
// 是在**不改动任何生产代码**的前提下让这些函数跑在测试库上的唯一办法。
//
//go:linkname qyDBHandle github.com/QuantumNous/new-api/qianye/db.handle
var qyDBHandle atomic.Pointer[gorm.DB]

// qyConfig 同理指向 qianye/config 的当前配置快照。
// config.Load() 只能从磁盘 YAML 读,测试需要直接给出配置结构。
//
//go:linkname qyConfig github.com/QuantumNous/new-api/qianye/config.current
var qyConfig atomic.Pointer[config.Config]

func extTables() []any {
	return []any{&Rule{}, &RuleVersion{}, &ShadowBucket{}}
}

// newTestDB 建一个承载 grouppricing 全部扩展表的测试库,并把它接到 db.Get()。
func newTestDB(t *testing.T) *gorm.DB {
	t.Helper()
	dsn := filepath.Join(t.TempDir(), "qy_ext.db") +
		"?_pragma=journal_mode(WAL)&_pragma=busy_timeout(5000)"
	// 静默 GORM 日志:好几条用例刻意制造"表不存在"来验证降级路径,
	// 默认日志会把真正的失败断言淹没在预期内的 SQL 错误里。
	gdb, err := gorm.Open(sqlite.Open(dsn), &gorm.Config{
		Logger: gormlogger.Discard,
	})
	require.NoError(t, err)
	sqlDB, err := gdb.DB()
	require.NoError(t, err)
	require.NoError(t, gdb.AutoMigrate(extTables()...))

	prev := qyDBHandle.Swap(gdb)
	resetCaches()
	t.Cleanup(func() {
		qyDBHandle.Store(prev)
		resetCaches()
		_ = sqlDB.Close()
	})
	return gdb
}

// detachDB 把 db.Get() 置空,模拟"扩展库不可用"。
func detachDB(t *testing.T) {
	t.Helper()
	prev := qyDBHandle.Swap(nil)
	t.Cleanup(func() { qyDBHandle.Store(prev) })
}

// resetCaches 清掉所有跨测试残留的进程内状态。
//
// 不清的话上一个测试的快照会渗进下一个,而渗进来的往往正好是让断言变成永真的那一份。
func resetCaches() {
	current.Store(nil)
	loadedAt.Store(0)
	nextRefreshAt.Store(0)
	refreshFails.Store(0)
	staleDrops.Store(0)
	hotShadow = sync.Map{}
	hotShadowKeys.Store(0)
	shadowDropped.Store(0)
	shadowFlushed.Store(0)
	shadowFlushFail.Store(0)
}

// useConfig 临时替换扩展的全局配置快照。
func useConfig(t *testing.T, enabled, shadow bool) {
	t.Helper()
	cfg := &config.Config{Enabled: true}
	cfg.GroupPricing = config.GroupPricing{
		Enabled:                    enabled,
		ShadowMode:                 &shadow,
		RuleCacheSeconds:           60,
		MaxStaleSeconds:            300,
		ShadowFlushIntervalSeconds: 60,
		ShadowRetentionDays:        90,
		MaxRules:                   2000,
	}
	prev := qyConfig.Swap(cfg)
	t.Cleanup(func() { qyConfig.Store(prev) })
}

// syncHotAsync 把影子记录的异步派发换成同步执行。
//
// 生产路径走 guard.HotAsync(有界队列 + 独立 worker),单元测试里既起不来
// 也无法等待完成,于是"影子模式下差额确实被记下来了"这条断言就只能靠猜。
// TestHotAsyncDefaultsToGuard 保证生产默认值没有被顺手改掉。
func syncHotAsync(t *testing.T) {
	t.Helper()
	prev := hotAsync
	hotAsync = func(name string, fn func(ctx context.Context) error) {
		_ = fn(context.Background())
	}
	t.Cleanup(func() { hotAsync = prev })
}

// seedRule 插入一条启用规则并推进版本号,然后强制重建快照。
func seedRule(t *testing.T, gdb *gorm.DB, group, modelName, mode, value string) *Rule {
	t.Helper()
	r := &Rule{
		GroupName: group, ModelName: modelName, Mode: mode,
		Value:   decimal.RequireFromString(value),
		Enabled: true, CreatedAt: 1, UpdatedAt: 1,
	}
	require.NoError(t, gdb.Create(r).Error)
	require.NoError(t, bumpVersion(gdb))
	require.NoError(t, reload(true))
	return r
}

// relayInfo 构造一个只带计价所需字段的 RelayInfo。
//
// userGroup 与 usingGroup 刻意分开传:两者不同正是 auto 分组重试的形态,
// 也是"取错分组"这个缺陷唯一能被观察到的地方。
func relayInfo(userGroup, usingGroup, modelName string) *relaycommon.RelayInfo {
	return &relaycommon.RelayInfo{
		UserGroup:       userGroup,
		UsingGroup:      usingGroup,
		OriginModelName: modelName,
		RequestId:       "req-test-1",
	}
}
