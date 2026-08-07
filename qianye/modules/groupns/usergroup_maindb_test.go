package groupns

// usergroup_maindb_test.go —— 用户分组生命周期用例的**主库**脚手架。
//
// 删除与改名的全部危险都在主库那一半(users / user_subscriptions /
// subscription_plans / options),用一张假快照替代它等于把唯一会出事的地方
// 挖掉再测剩下的部分。所以这里真的过一遍 SQLite。

import (
	"path/filepath"
	"sync/atomic"
	"testing"
	_ "unsafe" // //go:linkname 需要

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/model"
	qymodel "github.com/QuantumNous/new-api/qianye/model"

	"github.com/glebarez/sqlite"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
	gormlogger "gorm.io/gorm/logger"
)

// modelCommonGroupCol 指向 model 包里那个保留字列名(users.group 要加反引号或双引号)。
//
// 生产由 model.initCol() 在 InitDB 时按数据库类型填好,而测试直接替换 model.DB
// 句柄、绕过了 InitDB,于是它是空串 —— 本包大量 `SELECT `+col+` AS grp` 会发出
// `SELECT  AS grp` 这种语法错误的 SQL。不接上它,用例断言的就不是"迁移正确",
// 而是"测试环境没搭好"。手法与 subscription/testdb_test.go 同源。
//
//go:linkname modelCommonGroupCol github.com/QuantumNous/new-api/model.commonGroupCol
var modelCommonGroupCol string

// qyDBHealthy 是 guard.RequireAPI 第三道判据(db.Available())读的那个标志位。
//
// 不置为 true 的话每一个 handler 用例都会拿到 503,而 503 与"handler 逻辑正确"
// 在只断言"不是 200"的用例里长得一模一样。
//
//go:linkname qyDBHealthy github.com/QuantumNous/new-api/qianye/db.healthy
var qyDBHealthy atomic.Bool

// enableExtAPI 让 guard.RequireAPI(FlagCore) 放行,并建出审计表。
//
// 审计表必须一起建:本组 handler 全部写审计,而 audit.WriteConfigUpdate 写失败
// **不会**让 handler 报错 —— 表建不出来时用例断言过的是一条没有审计的路径。
func enableExtAPI(t *testing.T, gdb *gorm.DB) {
	t.Helper()
	nsConfig(t, true, "", "")
	prev := qyDBHealthy.Swap(true)
	t.Cleanup(func() { qyDBHealthy.Store(prev) })
	require.NoError(t, gdb.AutoMigrate(&qymodel.AuditLog{}))
}

// newMainDB 建一个主库测试实例并接到 model.DB。
func newMainDB(t *testing.T) *gorm.DB {
	t.Helper()
	dsn := filepath.Join(t.TempDir(), "main.db") +
		"?_pragma=journal_mode(WAL)&_pragma=busy_timeout(10000)"
	gdb, err := gorm.Open(sqlite.Open(dsn), &gorm.Config{Logger: gormlogger.Discard})
	require.NoError(t, err)
	sqlDB, err := gdb.DB()
	require.NoError(t, err)
	require.NoError(t, gdb.AutoMigrate(
		&model.User{}, &model.Token{}, &model.Ability{}, &model.Option{},
		&model.SubscriptionPlan{}, &model.UserSubscription{},
	))

	prevDB, prevCol := model.DB, modelCommonGroupCol
	prevMem, prevRedis := common.MemoryCacheEnabled, common.RedisEnabled
	prevOptions := common.OptionMap
	model.DB = gdb
	modelCommonGroupCol = "`group`"
	// common.OptionMap 在生产由 InitOptionMap 建好,而测试直接替换 model.DB
	// 绕过了它 —— 于是 model.UpdateOption 里那句 `common.OptionMap[key] = value`
	// 对一张 nil map 赋值,当场 panic。删除/改名路径必然经过它(两处 options 键),
	// 不接上这个变量,用例连 handler 都进不去。
	common.OptionMap = map[string]string{}
	// Redis 关掉:InvalidateUserCache 在未启用时直接返回 nil,
	// 于是 CacheMisses 恒为 0 而不是"每个用户都失败"。
	common.MemoryCacheEnabled = false
	common.RedisEnabled = false
	t.Cleanup(func() {
		model.DB, modelCommonGroupCol = prevDB, prevCol
		common.MemoryCacheEnabled, common.RedisEnabled = prevMem, prevRedis
		common.OptionMap = prevOptions
		_ = sqlDB.Close()
	})
	return gdb
}

// seedUser 往主库插一个用户 + 一个空分组令牌。
//
// 令牌必须一起插:项目方点名要「多少用户、多少令牌」,而 tokens 是靠 user_id
// join 回 users.group 数出来的 —— 只插用户的话那个数恒为 0,断言就永远是绿的。
func seedUser(t *testing.T, gdb *gorm.DB, id int, group string) {
	t.Helper()
	// aff_code 有唯一索引:全部留空的话第二个用户就撞 UNIQUE,
	// 而报出来的错与分组毫无关系。
	require.NoError(t, gdb.Create(&model.User{
		Id: id, Username: "u" + itoa(id), Group: group,
		AffCode: "aff" + itoa(id), Status: common.UserStatusEnabled,
	}).Error)
	require.NoError(t, gdb.Create(&model.Token{
		Id: id, UserId: id, Name: "t" + itoa(id), Key: "k" + itoa(id),
		Group: "", Status: common.TokenStatusEnabled,
	}).Error)
}

func itoa(v int) string {
	if v == 0 {
		return "0"
	}
	out := ""
	for v > 0 {
		out = string(rune('0'+v%10)) + out
		v /= 10
	}
	return out
}
