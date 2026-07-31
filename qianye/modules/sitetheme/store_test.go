package sitetheme

import (
	"path/filepath"
	"sync/atomic"
	"testing"
	_ "unsafe" // //go:linkname 需要

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/qianye/config"
	qymodel "github.com/QuantumNous/new-api/qianye/model"

	"github.com/glebarez/sqlite"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

// 本文件守 M5:回源失败的兜底值绝不能被当成正常快照缓存。
//
// 为什么"读出来的主题对不对"这类断言守不住它:失败兜底值与"运营没配过"的
// 成功结果长得一模一样({default,false}),看返回值分不出来。缺陷在于**那个值
// 被存进了一个没有 TTL、唯一失效点是 save() 的缓存**——一次几秒的抖动就让本
// 进程直到重启前都对所有访客下发上游默认主题,而前端会把它写进每个访客的
// localStorage。因此下面的断言从"故障恢复之后还能不能回到真值"入手。

//go:linkname qyDBHandle github.com/QuantumNous/new-api/qianye/db.handle
var qyDBHandle atomic.Pointer[gorm.DB]

//go:linkname qyDBHealthy github.com/QuantumNous/new-api/qianye/db.healthy
var qyDBHealthy atomic.Bool

//go:linkname qyConfig github.com/QuantumNous/new-api/qianye/config.current
var qyConfig atomic.Pointer[config.Config]

// newExtDB 建一个扩展库并接到 db.Get(),同时把扩展置为「已启用且健康」。
func newExtDB(t *testing.T) *gorm.DB {
	t.Helper()
	dsn := filepath.Join(t.TempDir(), "qy_ext.db") + "?_pragma=busy_timeout(5000)"
	gdb, err := gorm.Open(sqlite.Open(dsn), &gorm.Config{})
	require.NoError(t, err)
	sqlDB, err := gdb.DB()
	require.NoError(t, err)
	require.NoError(t, gdb.AutoMigrate(&qymodel.Setting{}))

	prevHandle := qyDBHandle.Swap(gdb)
	prevHealthy := qyDBHealthy.Swap(true)
	prevCfg := qyConfig.Swap(&config.Config{Enabled: true})
	resetThemeCache()

	t.Cleanup(func() {
		qyDBHandle.Store(prevHandle)
		qyDBHealthy.Store(prevHealthy)
		qyConfig.Store(prevCfg)
		resetThemeCache()
		_ = sqlDB.Close()
	})
	return gdb
}

func resetThemeCache() {
	snapshot.Store(nil)
	failUntil.Store(0)
}

func seedTheme(t *testing.T, gdb *gorm.DB, preset string, force bool) {
	t.Helper()
	now := common.GetTimestamp()
	require.NoError(t, gdb.Create(&qymodel.Setting{
		Scope: settingScope, K: keyPreset, V: preset, UpdatedAt: now,
	}).Error)
	require.NoError(t, gdb.Create(&qymodel.Setting{
		Scope: settingScope, K: keyForce, V: boolStr(force), UpdatedAt: now,
	}).Error)
	resetThemeCache()
}

// countThemeQueries 统计此后打到扩展库的查询条数。
func countThemeQueries(t *testing.T, gdb *gorm.DB) *atomic.Int64 {
	t.Helper()
	n := &atomic.Int64{}
	const name = "test:count_theme_queries"
	require.NoError(t, gdb.Callback().Query().Before("gorm:query").Register(name, func(tx *gorm.DB) {
		n.Add(1)
	}))
	t.Cleanup(func() { _ = gdb.Callback().Query().Remove(name) })
	return n
}

// TestCurrentDoesNotCacheFallbackOnFailure 是 M5 的回归。
//
// 场景:运营已把默认主题配成 steins-gate + 强制生效。进程刚起、缓存还空着时,
// 第一个 GET /api/qy/config 恰好撞上扩展库不可用 → 兜底值被存进 snapshot →
// 库几秒后恢复,但 Current() 再也不回源,本进程直到重启前对所有访客下发
// default/force=false。
func TestCurrentDoesNotCacheFallbackOnFailure(t *testing.T) {
	gdb := newExtDB(t)
	seedTheme(t, gdb, "steins-gate", true)

	qyDBHealthy.Store(false) // 扩展库瞬时不可用
	preset, force := Current()
	assert.Equal(t, DefaultPreset, preset, "读不到时必须回落上游默认,绝不能让引导端点失败")
	assert.False(t, force)
	assert.Nil(t, snapshot.Load(),
		"失败兜底值绝不能进 snapshot:这个缓存没有 TTL,唯一失效点是 save(),存进去就是永久的")

	qyDBHealthy.Store(true) // 库恢复
	failUntil.Store(0)      // 负缓存到期(测试不等实时)
	preset, force = Current()
	assert.Equal(t, "steins-gate", preset, "库恢复后必须重新回源,拿到运营真正配的主题")
	assert.True(t, force)
}

// TestCurrentNegativeCacheStopsQueryStorm 锁定"失败之后有短负缓存"。
//
// 不写缓存的代价是故障期间每一次页面加载都要查一次库(/api/qy/config 是匿名
// 引导端点)。负缓存把它压成每 loadRetrySeconds 秒一次,同时保持"恢复后能回到
// 真值"这条性质 —— 两者必须同时成立,只做前者就是把 M5 换了个形状。
func TestCurrentNegativeCacheStopsQueryStorm(t *testing.T) {
	gdb := newExtDB(t)
	seedTheme(t, gdb, "steins-gate", true)

	queries := countThemeQueries(t, gdb)
	// 删表让每一次回源都真的失败:"no such table" 不是连接级错误,不会触发熔断,
	// 因此"没再查"只可能是负缓存生效。
	require.NoError(t, gdb.Migrator().DropTable(&qymodel.Setting{}))

	for i := 0; i < 4; i++ {
		preset, force := Current()
		assert.Equal(t, DefaultPreset, preset)
		assert.False(t, force)
	}
	assert.EqualValues(t, 1, queries.Load(),
		"故障期间每一次页面加载都回源一次,是把展示层配置的降级变成扩展库的压力源")
}

// TestCurrentCachesSuccessfulLoad 是反向约束:成功读到的结果必须被缓存住。
//
// 没有它,"只要不写缓存就不会中毒"这种过度修复会全绿,而 /api/qy/config 会
// 变成每一次页面加载都查一次库。
func TestCurrentCachesSuccessfulLoad(t *testing.T) {
	gdb := newExtDB(t)
	seedTheme(t, gdb, "lake-view", false)

	preset, _ := Current()
	require.Equal(t, "lake-view", preset, "前提:第一次调用成功回源")

	queries := countThemeQueries(t, gdb)
	require.NoError(t, gdb.Migrator().DropTable(&qymodel.Setting{}))
	for i := 0; i < 3; i++ {
		preset, force := Current()
		assert.Equal(t, "lake-view", preset)
		assert.False(t, force)
	}
	assert.EqualValues(t, 0, queries.Load(), "成功读到之后不该再回源")
}
