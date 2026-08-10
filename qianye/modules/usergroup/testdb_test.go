package usergroup

import (
	"path/filepath"
	"sync/atomic"
	"testing"
	_ "unsafe" // //go:linkname 需要

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/qianye/config"
	qymodel "github.com/QuantumNous/new-api/qianye/model"
	"github.com/QuantumNous/new-api/service"
	"github.com/QuantumNous/new-api/setting/ratio_setting"

	"github.com/glebarez/sqlite"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

// 本文件是 usergroup 包的测试脚手架。
//
// 为什么必须让测试真的跑数据库、真的走 model.User 的插入路径:本模块的价值
// 全部落在两处「接线」上 —— ① model/user.go 的 prepareForInsert 里那一行同包
// 调用,② 管理端保存前的分组存在性校验。这两处任何一处断掉,纯函数层的单测
// 都会照样全绿:resolveNewUserGroup 自己返回什么都对,但没人调用它;
// validateDefaultGroup 自己拒绝什么都对,但 handler 没调它。
// 因此下面的测试一律从**外部入口**进去:插入一个真实的 model.User,
// 或者打一次真实的 HTTP handler。
//
// 生产环境的扩展库固定是 MySQL,这里用 sqlite。本文件的断言只依赖跨库通用
// 语义(等值查询、ON CONFLICT 的 upsert、列默认值),不碰任何 MySQL 专有语法。

// qyDBHandle 指向 qianye/db 包里的连接句柄。db.Get() 读的是包内未导出的
// atomic.Pointer,而 db.Init 只会拨 MySQL。用 //go:linkname 把句柄借出来,
// 是在**不改动任何生产代码**的前提下让这些函数跑在测试库上的唯一办法。
//
//go:linkname qyDBHandle github.com/QuantumNous/new-api/qianye/db.handle
var qyDBHandle atomic.Pointer[gorm.DB]

// qyDBHealthy 是熔断的健康标志。readDefaultGroup 走 guard.Available(),
// 它要求 healthy 为 true,而这个标志只有真的 Init 过才会被置上。
//
//go:linkname qyDBHealthy github.com/QuantumNous/new-api/qianye/db.healthy
var qyDBHealthy atomic.Bool

// qyConfig 指向 qianye/config 的当前配置快照。config.Load() 只能从磁盘 YAML 读,
// 测试需要直接给出配置结构(guard.Available 会读 config.Enabled)。
//
//go:linkname qyConfig github.com/QuantumNous/new-api/qianye/config.current
var qyConfig atomic.Pointer[config.Config]

// newExtDB 建一个扩展库(qy_settings + 审计表)并接到 db.Get(),
// 同时把扩展置为「已启用且健康」。
func newExtDB(t *testing.T) *gorm.DB {
	t.Helper()
	dsn := filepath.Join(t.TempDir(), "qy_ext.db") + "?_pragma=busy_timeout(5000)"
	gdb, err := gorm.Open(sqlite.Open(dsn), &gorm.Config{})
	require.NoError(t, err)
	sqlDB, err := gdb.DB()
	require.NoError(t, err)
	require.NoError(t, gdb.AutoMigrate(&qymodel.Setting{}, &qymodel.AuditLog{}))

	prevHandle := qyDBHandle.Swap(gdb)
	prevHealthy := qyDBHealthy.Swap(true)
	prevCfg := qyConfig.Swap(&config.Config{Enabled: true})
	resetCache()
	resetStaleWarn()

	t.Cleanup(func() {
		qyDBHandle.Store(prevHandle)
		qyDBHealthy.Store(prevHealthy)
		qyConfig.Store(prevCfg)
		resetCache()
		resetStaleWarn()
		_ = sqlDB.Close()
	})
	return gdb
}

// resetStaleWarn 清掉「分组已失效」告警的限频状态,避免测试之间互相吞告警。
func resetStaleWarn() {
	staleWarnMu.Lock()
	staleWarnAt = 0
	staleWarnMu.Unlock()
}

// newMainDB 建一个主库(users + abilities)并接到 model.DB。
//
// 建 users 是为了让测试能真的调 model.User.InsertWithTx —— 只有真的插一行,
// gorm:"default:'default'" 这个兜底才会被数据库执行到,「未配置时行为不变」
// 才是被验证过的而不是被假设的。
func newMainDB(t *testing.T) *gorm.DB {
	t.Helper()
	dsn := filepath.Join(t.TempDir(), "qy_main.db") + "?_pragma=busy_timeout(5000)"
	gdb, err := gorm.Open(sqlite.Open(dsn), &gorm.Config{})
	require.NoError(t, err)
	sqlDB, err := gdb.DB()
	require.NoError(t, err)
	require.NoError(t, gdb.AutoMigrate(&model.User{}, &model.Ability{}))

	prev := model.DB
	model.DB = gdb
	t.Cleanup(func() {
		model.DB = prev
		_ = sqlDB.Close()
	})
	return gdb
}

// useGroupRatio 临时替换全站分组倍率表 —— 也就是「本站有哪些分组」的事实清单。
func useGroupRatio(t *testing.T, jsonStr string) {
	t.Helper()
	prev := ratio_setting.GroupRatio2JSONString()
	require.NoError(t, ratio_setting.UpdateGroupRatioByJSONString(jsonStr))
	t.Cleanup(func() {
		require.NoError(t, ratio_setting.UpdateGroupRatioByJSONString(prev))
	})
}

// useDeclaredUserGroups 临时替换用户分组登记表的读取口 —— 也就是「本站有哪几档
// 人」的事实清单。
//
// 替 service 上那个 hook 变量而不是去建 qy_user_groups 表:本包看得见的只有
// service.QyDeclaredUserGroups 这一个入口(groupns 在启动时把实现塞进去),
// 而本包要验的正是「有没有把它读进判据里」。
func useDeclaredUserGroups(t *testing.T, names ...string) {
	t.Helper()
	prev := service.QyDeclaredUserGroups
	service.QyDeclaredUserGroups = func() []string { return names }
	t.Cleanup(func() { service.QyDeclaredUserGroups = prev })
}

// installHook 装上 hook 并在测试结束后卸掉。
//
// 直接调 Mod{}.InstallHooks() 而不是手工赋值:注入这一步本身也是被测对象,
// 手工赋值会让「模块忘了注入」这类缺陷测不出来。
func installHook(t *testing.T) {
	t.Helper()
	prevResolve := model.QyResolveNewUserGroup
	prevQuery := model.QyNewUserGroup
	Mod{}.InstallHooks()
	t.Cleanup(func() {
		model.QyResolveNewUserGroup = prevResolve
		model.QyNewUserGroup = prevQuery
	})
}

// seedDefaultGroup 直接往扩展库写一条配置行,绕过管理端接口。
func seedDefaultGroup(t *testing.T, gdb *gorm.DB, value string) {
	t.Helper()
	require.NoError(t, gdb.Create(&qymodel.Setting{
		Scope: settingScope, K: keyDefaultGroup, V: value,
		UpdatedAt: common.GetTimestamp(),
	}).Error)
	resetCache()
}

// insertUser 走上游真实的建号路径,返回数据库里最终落下的分组。
//
// 用 InsertWithTx 而不是 Insert:两者共用 prepareForInsert(hook 就挂在那里),
// 但 Insert 之后还有 finishInsert —— 它会写日志库、刷用户缓存、按角色生成
// 边栏配置,那些都与本模块无关,却会把测试拖进一堆无关的全局状态里。
func insertUser(t *testing.T, gdb *gorm.DB, username string, group string) string {
	t.Helper()
	u := &model.User{Username: username, DisplayName: username, Group: group}
	require.NoError(t, gdb.Transaction(func(tx *gorm.DB) error {
		return u.InsertWithTx(tx, 0)
	}))

	var stored model.User
	require.NoError(t, gdb.Where("username = ?", username).Take(&stored).Error)
	return stored.Group
}
