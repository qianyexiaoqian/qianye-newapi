package groupmatrix

import (
	"context"
	"path/filepath"
	"sync/atomic"
	"testing"
	_ "unsafe" // //go:linkname 需要

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/qianye/config"
	"github.com/QuantumNous/new-api/setting"
	"github.com/QuantumNous/new-api/setting/ratio_setting"

	"github.com/glebarez/sqlite"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
	gormlogger "gorm.io/gorm/logger"
)

// 本文件是 groupmatrix 包的测试脚手架。
//
// 为什么必须让清单真的过一遍数据库:本模块唯一会造成生产事故的失败形状是
// 「快照与库里的东西不一致」,而那只有在真的读写一次库时才能被证伪。
// 纯内存的假快照能让每一条断言都通过,同时让缺陷原样留在代码里。

//go:linkname qyDBHandle github.com/QuantumNous/new-api/qianye/db.handle
var qyDBHandle atomic.Pointer[gorm.DB]

//go:linkname qyConfig github.com/QuantumNous/new-api/qianye/config.current
var qyConfig atomic.Pointer[config.Config]

// extTables 刻意与 Mod.Tables() 保持一致 —— 不含已退役的 qy_group_seen。
func extTables() []any { return []any{&Scope{}, &Grant{}, &WriteDeny{}} }

func newTestDB(t *testing.T) *gorm.DB {
	t.Helper()
	dsn := filepath.Join(t.TempDir(), "qy_gm.db") +
		"?_pragma=journal_mode(WAL)&_pragma=busy_timeout(5000)"
	gdb, err := gorm.Open(sqlite.Open(dsn), &gorm.Config{Logger: gormlogger.Discard})
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

// resetCaches 清掉所有跨测试残留的进程内状态。
// 不清的话上一个测试的快照会渗进下一个,而渗进来的往往正好是让断言变成永真的那一份。
func resetCaches() {
	current.Store(nil)
	loadedAt.Store(0)
	nextRefreshAt.Store(0)
	refreshFails.Store(0)
	staleWarns.Store(0)
	fallbackRequests.Store(0)
	droppedWarns.Store(0)
}

// useConfig 临时替换扩展的全局配置快照。
func useConfig(t *testing.T, matrixEnabled bool) {
	t.Helper()
	writeGuard := true
	cfg := &config.Config{Enabled: true}
	cfg.GroupMatrix = config.GroupMatrix{
		Enabled:            matrixEnabled,
		CacheSeconds:       30,
		MaxStaleSeconds:    300,
		PreviewLogDays:     7,
		MaxPreviewPairs:    500,
		PreviewSampleLimit: 20,
		MaxGrants:          2000,
		WriteGuardEnabled:  &writeGuard,
	}
	prev := qyConfig.Swap(cfg)
	t.Cleanup(func() { qyConfig.Store(prev) })
}

// syncHotAsync 把异步刷新换成同步执行。
//
// 生产路径走 guard.HotAsync(有界队列 + 独立 worker),单元测试里既起不来
// 也无法等待完成,于是"快照确实刷新了"这条断言就只能靠猜。
func syncHotAsync(t *testing.T) {
	t.Helper()
	prev := hotAsync
	hotAsync = func(name string, fn func(ctx context.Context) error) {
		_ = fn(context.Background())
	}
	t.Cleanup(func() { hotAsync = prev })
}

// useUpstreamGroups 显式构造上游的两处分组状态并在测试结束时还原。
//
// 必须显式构造:GetUserUsableGroups 的输出同时取决于全局白名单与
// GroupSpecialUsableGroup,借用进程里恰好残留的那一份会让断言在别的机器上翻车。
func useUpstreamGroups(t *testing.T, whitelist map[string]string,
	specials map[string]map[string]string, groupRatio map[string]float64) {
	t.Helper()

	prevWhitelist := setting.UserUsableGroups2JSONString()
	prevRatio := ratio_setting.GroupRatio2JSONString()
	specialMap := ratio_setting.GetGroupRatioSetting().GroupSpecialUsableGroup
	prevSpecials := specialMap.ReadAll()

	require.NoError(t, setting.UpdateUserUsableGroupsByJSONString(mustJSON(t, whitelist)))
	require.NoError(t, ratio_setting.UpdateGroupRatioByJSONString(mustJSON(t, groupRatio)))
	specialMap.Clear()
	specialMap.AddAll(specials)

	t.Cleanup(func() {
		_ = setting.UpdateUserUsableGroupsByJSONString(prevWhitelist)
		_ = ratio_setting.UpdateGroupRatioByJSONString(prevRatio)
		specialMap.Clear()
		specialMap.AddAll(prevSpecials)
	})
}

func mustJSON(t *testing.T, v any) string {
	t.Helper()
	b, err := common.Marshal(v)
	require.NoError(t, err)
	return string(b)
}

func seedScope(t *testing.T, gdb *gorm.DB, userGroup, mode string, allowAuto bool, grants ...string) {
	t.Helper()
	require.NoError(t, gdb.Create(newScope(userGroup, mode, allowAuto, "", 1, 1)).Error)
	for i, mg := range grants {
		require.NoError(t, gdb.Create(&Grant{
			UserGroup: userGroup, ModelGroup: mg, SortOrder: i,
			CreatedAt: 1, UpdatedAt: 1,
		}).Error)
	}
	require.NoError(t, reload())
}
