package groupns

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

// 本文件是 groupns 包的测试脚手架。手法与 groupmatrix/testdb_test.go 逐条同源:
// 登记表真的过一遍 SQLite、配置快照整份替换、HotAsync 换成同步执行。
//
// 纯内存的假快照能让每一条断言都通过,同时让「快照与库里的东西不一致」这个
// 唯一会造成生产事故的失败形状原样留在代码里。

//go:linkname qyDBHandle github.com/QuantumNous/new-api/qianye/db.handle
var qyDBHandle atomic.Pointer[gorm.DB]

//go:linkname qyConfig github.com/QuantumNous/new-api/qianye/config.current
var qyConfig atomic.Pointer[config.Config]

func newTestDB(t *testing.T) *gorm.DB {
	t.Helper()
	dsn := filepath.Join(t.TempDir(), "qy_gns.db") +
		"?_pragma=journal_mode(WAL)&_pragma=busy_timeout(5000)"
	gdb, err := gorm.Open(sqlite.Open(dsn), &gorm.Config{Logger: gormlogger.Discard})
	require.NoError(t, err)
	sqlDB, err := gdb.DB()
	require.NoError(t, err)
	require.NoError(t, gdb.AutoMigrate(&UserGroup{}, &ModelGroup{}))

	prev := qyDBHandle.Swap(gdb)
	ResetForTest()
	t.Cleanup(func() {
		qyDBHandle.Store(prev)
		ResetForTest()
		_ = sqlDB.Close()
	})
	return gdb
}

// nsConfig 临时替换扩展的全局配置快照。
//
// 三个参数分别对应三个独立开关,而它们的默认值方向不同:模块总开关默认关、
// 默认解析子开关默认开、闸门默认 off。测试必须能各自设定,否则"某一档其实没生效"
// 会藏在一个看起来全绿的用例里。
func nsConfig(t *testing.T, enabled bool, missingPolicy, fundingMode string) {
	t.Helper()
	defaultOn := true
	autoBackfill := false
	cfg := &config.Config{Enabled: true}
	cfg.GroupNamespace = config.GroupNamespace{
		Enabled:                  enabled,
		CacheSeconds:             30,
		MaxStaleSeconds:          300,
		DefaultModelGroupEnabled: &defaultOn,
		MissingRatioPolicy:       missingPolicy,
		FundingGateMode:          fundingMode,
		AutoBackfill:             &autoBackfill,
	}
	prev := qyConfig.Swap(cfg)
	t.Cleanup(func() { qyConfig.Store(prev) })
}

// syncHotAsync 把异步刷新换成同步执行:生产路径走 guard.HotAsync
// (有界队列 + 独立 worker),单元测试里既起不来也无法等待完成,
// 于是"快照确实刷新了"这条断言就只能靠猜。
func syncHotAsync(t *testing.T) {
	t.Helper()
	prev := hotAsync
	hotAsync = func(name string, fn func(ctx context.Context) error) {
		_ = fn(context.Background())
	}
	t.Cleanup(func() { hotAsync = prev })
}

// useUpstreamGroups 设定上游的全局白名单与分组倍率表。
func useUpstreamGroups(t *testing.T, whitelist map[string]string, groupRatio map[string]float64) {
	t.Helper()
	prevWhitelist := setting.UserUsableGroups2JSONString()
	prevRatio := ratio_setting.GroupRatio2JSONString()

	require.NoError(t, setting.UpdateUserUsableGroupsByJSONString(mustJSON(t, whitelist)))
	require.NoError(t, ratio_setting.UpdateGroupRatioByJSONString(mustJSON(t, groupRatio)))

	t.Cleanup(func() {
		_ = setting.UpdateUserUsableGroupsByJSONString(prevWhitelist)
		_ = ratio_setting.UpdateGroupRatioByJSONString(prevRatio)
	})
}

func mustJSON(t *testing.T, v any) string {
	t.Helper()
	b, err := common.Marshal(v)
	require.NoError(t, err)
	return string(b)
}

// usePlanUnlock 替换套餐余额接缝。
func usePlanUnlock(t *testing.T, fn func(userId int, modelGroup string) (bool, bool)) {
	t.Helper()
	prev := PlanFundedUnlock
	PlanFundedUnlock = fn
	t.Cleanup(func() { PlanFundedUnlock = prev })
}

func seedUserGroup(t *testing.T, gdb *gorm.DB, name, mode, defaultModelGroup string) {
	t.Helper()
	row := newUserGroup(name, 1, now())
	row.DefaultMode = mode
	row.DefaultModelGroup = defaultModelGroup
	require.NoError(t, gdb.Create(row).Error)
}
