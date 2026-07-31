package usergroup

import (
	"sync"
	"sync/atomic"
	"testing"

	"github.com/QuantumNous/new-api/common"
	qymodel "github.com/QuantumNous/new-api/qianye/model"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

// 本文件守 M9:默认分组的读取不得持锁查库,且失败之后必须有负缓存。
//
// 为什么不能只断言"读出来的分组对不对":那种测试在缺陷存在时照样全绿 ——
// 缺陷不在算出来的值里,而在**这条 SELECT 是在什么状态下、以什么频率发出的**。
// 这个 hook 跑在 model/user.go 的 prepareForInsert 里,位于用户创建的主库事务
// 内部:持锁查库时全进程的注册会在 cacheMu 上排成一队,每个约一个
// hot_path_timeout_ms;失败不推进时间戳时,一条打爆预算的慢 SELECT 会让**每一次**
// 注册都回源一遍。两者叠加就是主库连接池被注册洪峰吃光。
// 因此下面的断言一律从运行时状态入手:一类在查询进行中反向探测互斥锁,
// 一类直接数这段时间里到底发出了几条 SELECT。

// lockProbe 记录"某条 SQL 执行期间,cacheMu 是不是被同一个协程占着"。
//
// TryLock 在持锁协程自己调用时返回 false —— 正是这个性质让它能在回调里
// 分辨出"查库这一步在不在临界区内"。
type lockProbe struct {
	fired bool
	busy  bool
}

func probeCacheLockDuringQuery(t *testing.T, gdb *gorm.DB) *lockProbe {
	t.Helper()
	p := &lockProbe{}
	const name = "test:probe_cache_lock"
	require.NoError(t, gdb.Callback().Query().Before("gorm:query").Register(name, func(tx *gorm.DB) {
		if tx.Statement == nil {
			return
		}
		p.fired = true
		if cacheMu.TryLock() {
			cacheMu.Unlock()
			return
		}
		p.busy = true
	}))
	t.Cleanup(func() { _ = gdb.Callback().Query().Remove(name) })
	return p
}

// countSettingQueries 统计此后打到扩展库的查询条数。
func countSettingQueries(t *testing.T, gdb *gorm.DB) *atomic.Int64 {
	t.Helper()
	n := &atomic.Int64{}
	const name = "test:count_setting_queries"
	require.NoError(t, gdb.Callback().Query().Before("gorm:query").Register(name, func(tx *gorm.DB) {
		n.Add(1)
	}))
	t.Cleanup(func() { _ = gdb.Callback().Query().Remove(name) })
	return n
}

// expireDefaultGroupCache 让 60 秒的正常缓存刚好过期,但保留已缓存的分组值。
// 直接改时间戳而不是 resetCache():后者会把快照一起清掉,而"失败时沿用上一次
// 成功的快照"正是要被验证的行为之一。
func expireDefaultGroupCache() {
	cacheMu.Lock()
	cachedAt = common.GetTimestamp() - cacheSeconds
	cacheMu.Unlock()
}

// TestDefaultGroupQueriesOutsideCacheLock 锁定"查 qy_settings 时 cacheMu 是空闲的"。
//
// 把查库那一步挪回临界区内(改回 Lock + defer Unlock 全程持锁的写法),这条立刻变红。
func TestDefaultGroupQueriesOutsideCacheLock(t *testing.T) {
	gdb := newExtDB(t)
	seedDefaultGroup(t, gdb, "vip")

	probe := probeCacheLockDuringQuery(t, gdb)

	require.Equal(t, "vip", currentDefaultGroup(), "前提:本次确实回库读了一遍配置")
	require.True(t, probe.fired, "前提:探针挂上了 —— 没触发说明这条测试什么都没验")
	assert.False(t, probe.busy,
		"查 qy_settings 期间 cacheMu 被占着:全进程的注册会串在这把锁上,"+
			"而每个排队者都攥着一个已打开的主库事务")
}

// TestDefaultGroupNegativeCacheStopsRefetchStorm 是负缓存的回归。
//
// 场景:扩展库"可达但慢/查询失败",readDefaultGroup 返回 ok=false。没有负缓存时
// cachedAt 不被推进,于是下一次注册必然再查一次 —— 注册洪峰里的每一个人都要
// 撞一遍同一条失败查询,而每个人都攥着一个主库事务。
func TestDefaultGroupNegativeCacheStopsRefetchStorm(t *testing.T) {
	gdb := newExtDB(t)
	seedDefaultGroup(t, gdb, "vip")
	require.Equal(t, "vip", currentDefaultGroup(), "前提:先成功填充一次快照")

	expireDefaultGroupCache()
	queries := countSettingQueries(t, gdb)
	// 把表删掉,让此后每一次回源都真的失败(不是连接级错误,不会触发熔断,
	// 因此"没再查"只可能是负缓存生效,而不是 guard.Available() 提前挡掉)。
	require.NoError(t, gdb.Migrator().DropTable(&qymodel.Setting{}))

	for i := 0; i < 4; i++ {
		assert.Equal(t, "vip", currentDefaultGroup(), "读失败必须沿用上一次成功的快照,而不是清空")
	}
	assert.EqualValues(t, 1, queries.Load(),
		"一次失败之后必须进入负缓存:每一次注册都回源一遍,就是主库连接池被注册洪峰吃光的那条路径")
}

// TestDefaultGroupInvalidationSurvivesInFlightLoad 是把查库挪出临界区之后
// 新开的那个窗口的回归(与 commission 的 settingsEpoch 同一形状)。
//
// 场景:hook 的 SELECT 已经读到旧分组 vip → 管理员恰在这一刻保存了 svip,
// storeDefaultGroup 把新值写进缓存 → 在途的旧快照回来把它无条件盖掉,
// 此后 60 秒的新用户全部落进 vip,而管理端显示的是 svip。
func TestDefaultGroupInvalidationSurvivesInFlightLoad(t *testing.T) {
	gdb := newExtDB(t)
	seedDefaultGroup(t, gdb, "vip")

	const name = "test:save_during_query"
	var once sync.Once
	require.NoError(t, gdb.Callback().Query().After("gorm:query").Register(name, func(tx *gorm.DB) {
		once.Do(func() { storeDefaultGroup("svip") })
	}))
	t.Cleanup(func() { _ = gdb.Callback().Query().Remove(name) })

	assert.Equal(t, "svip", currentDefaultGroup(),
		"管理端已经保存了新分组,在途的旧快照不得把它按回去")
	assert.Equal(t, "svip", currentDefaultGroup(),
		"被按回去的旧值还会带着一个新鲜时间戳,让后续 60 秒都读不到真值")
}
