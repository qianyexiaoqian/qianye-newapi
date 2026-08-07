package planentitlement

import (
	"context"
	"path/filepath"
	"sync/atomic"
	"testing"
	_ "unsafe" // //go:linkname 需要

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/qianye/config"
	qymodel "github.com/QuantumNous/new-api/qianye/model"
	"github.com/QuantumNous/new-api/setting/ratio_setting"

	"github.com/glebarez/sqlite"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// 本文件是 planentitlement 包的数据库脚手架。
//
// 为什么必须真跑数据库:本模块要守住的东西 —— 「解锁并入可选清单」「余额范围
// 过滤不改变最先到期优先的顺序」「展示顺序与扣费顺序一致」—— 有一半不在纯函数
// 里,而在上游那条 `Order("end_time asc, id asc")` 的 SQL 语义与事务里。
// 只测纯函数的用例照样全绿,而那正是本仓反复出现的"假回归"。
//
// 两个库都跑 sqlite,因此断言只依赖跨库通用语义(ORDER BY、事务、
// ON CONFLICT DO UPDATE)。

//go:linkname qyDBHandle github.com/QuantumNous/new-api/qianye/db.handle
var qyDBHandle atomic.Pointer[gorm.DB]

//go:linkname qyDBHealthy github.com/QuantumNous/new-api/qianye/db.healthy
var qyDBHealthy atomic.Bool

//go:linkname qyConfig github.com/QuantumNous/new-api/qianye/config.current
var qyConfig atomic.Pointer[config.Config]

//go:linkname modelCommonGroupCol github.com/QuantumNous/new-api/model.commonGroupCol
var modelCommonGroupCol string

func extTables() []any {
	return []any{&PlanGrant{}, &PlanBalancePolicy{}, &qymodel.AuditLog{}}
}

// newExtDB 建一个扩展库测试实例并接到 db.Get(),同时把配置置成"扩展已启用"。
func newExtDB(t *testing.T) *gorm.DB {
	t.Helper()
	dsn := filepath.Join(t.TempDir(), "qy_planentitlement.db") +
		"?_pragma=journal_mode(WAL)&_pragma=busy_timeout(10000)"
	gdb, err := gorm.Open(sqlite.Open(dsn), &gorm.Config{})
	require.NoError(t, err)
	sqlDB, err := gdb.DB()
	require.NoError(t, err)
	// db.LockForUpdate 无条件渲染 FOR UPDATE(扩展库固定 MySQL),sqlite 不认。
	gdb.ClauseBuilders["FOR"] = func(clause.Clause, clause.Builder) {}
	require.NoError(t, gdb.AutoMigrate(extTables()...))

	prevHandle := qyDBHandle.Swap(gdb)
	prevHealthy := qyDBHealthy.Swap(true)
	prevCfg := qyConfig.Swap(&config.Config{
		Enabled: true,
		PlanEntitlement: config.PlanEntitlement{
			CacheSeconds:        30,
			UserCacheSeconds:    60,
			UserMaxStaleSeconds: 300,
		},
	})
	// 刷新在生产里走 guard.HotAsync(有界队列 + 独立 worker)。测试里同步执行:
	// 异步刷新会让"改完配置立刻断言"变成偶发失败,而那种不稳定用例最后一定会被
	// 加一句 sleep 或者直接删掉。
	prevHot := hotAsync
	hotAsync = func(_ string, fn func(ctx context.Context) error) { _ = fn(context.Background()) }

	t.Cleanup(func() {
		qyDBHandle.Store(prevHandle)
		qyDBHealthy.Store(prevHealthy)
		qyConfig.Store(prevCfg)
		hotAsync = prevHot
		_ = sqlDB.Close()
		resetCaches()
	})
	resetCaches()
	return gdb
}

// newMainDB 临时替换主库句柄并建订阅相关的表。
//
// 必须是**文件库 + WAL**:上游 PreConsumeUserSubscription 内部会调
// GetDBTimestamp(),而那个函数走全局 model.DB 而不是调用方的 tx ——
// 只有一条连接时会在事务持有连接的情况下再申请一条,直接挂死在连接池上。
func newMainDB(t *testing.T) *gorm.DB {
	t.Helper()
	dsn := filepath.Join(t.TempDir(), "main.db") +
		"?_pragma=journal_mode(WAL)&_pragma=busy_timeout(10000)"
	gdb, err := gorm.Open(sqlite.Open(dsn), &gorm.Config{})
	require.NoError(t, err)
	sqlDB, err := gdb.DB()
	require.NoError(t, err)
	require.NoError(t, gdb.AutoMigrate(
		&model.User{},
		&model.SubscriptionPlan{},
		&model.UserSubscription{},
		&model.SubscriptionPreConsumeRecord{},
	))

	prev := model.DB
	prevGroupCol := modelCommonGroupCol
	prevRedis := common.RedisEnabled
	model.DB = gdb
	modelCommonGroupCol = "`group`"
	common.RedisEnabled = false
	t.Cleanup(func() {
		model.DB = prev
		modelCommonGroupCol = prevGroupCol
		common.RedisEnabled = prevRedis
		_ = sqlDB.Close()
	})
	return gdb
}

// setGroupRatios 装载一份分组倍率表。
//
// 它是模型分组的**事实清单**:写入校验与快照编译都对它做判定,不装的话
// 每一条绑定都会被判成"分组不存在"而剔除,用例会以一种指错方向的方式全绿。
func setGroupRatios(t *testing.T, json string) {
	t.Helper()
	prev := ratio_setting.GroupRatio2JSONString()
	require.NoError(t, ratio_setting.UpdateGroupRatioByJSONString(json))
	t.Cleanup(func() { _ = ratio_setting.UpdateGroupRatioByJSONString(prev) })
}

// enforceBalanceScope 模拟"订阅侧已经把 CandidateUsable 接到扣费路径上"。
//
// 不装它的话,余额范围过滤按设计恒返回"可用"(见 seams.go 的 balanceScopeEnforced),
// 于是所有 restricted 用例都会以一种指错方向的方式变绿。
func enforceBalanceScope(t *testing.T) {
	t.Helper()
	prev := balanceScopeEnforced.Load()
	MarkBalanceScopeEnforced()
	t.Cleanup(func() { balanceScopeEnforced.Store(prev) })
}

// seedPlan 往主库插一个套餐(不重置额度)。
func seedPlan(t *testing.T, gdb *gorm.DB, id int, title string) *model.SubscriptionPlan {
	t.Helper()
	return seedPlanWithReset(t, gdb, id, title, model.SubscriptionResetNever, 0)
}

// seedPlanWithReset 插一个指定重置周期的套餐。
//
// 周期必须能显式指定:「订阅行上的 next_reset_time 到点了」与「这个套餐真的会
// 重置」是两件事,而套餐的周期在创建订阅之后还能被管理员改掉(改的时候不回写
// 已有订阅的 next_reset_time)。只用一个 never 套餐做夹具的话,这条分叉永远测不到。
func seedPlanWithReset(t *testing.T, gdb *gorm.DB, id int, title, period string, customSeconds int64) *model.SubscriptionPlan {
	t.Helper()
	plan := &model.SubscriptionPlan{
		Id: id, Title: title, Enabled: true,
		DurationUnit: model.SubscriptionDurationMonth, DurationValue: 1,
		QuotaResetPeriod: period, QuotaResetCustomSeconds: customSeconds,
	}
	require.NoError(t, gdb.Create(plan).Error)
	return plan
}

// seedSubscription 插一条活跃订阅。endTime 是**相对现在**的秒数偏移。
func seedSubscription(t *testing.T, gdb *gorm.DB, id, userId, planId int, endOffset int64, total, used int64) *model.UserSubscription {
	t.Helper()
	sub := &model.UserSubscription{
		Id: id, UserId: userId, PlanId: planId, Status: statusActive,
		StartTime: common.GetTimestamp() - 60, EndTime: common.GetTimestamp() + endOffset,
		AmountTotal: total, AmountUsed: used,
		AllowWalletOverflow: true,
	}
	require.NoError(t, gdb.Create(sub).Error)
	return sub
}

// putGrant 直接往扩展库写一条解锁绑定,跳过 HTTP 层。
//
// 表名与列名一律用**字面量**,不复用 PlanGrant 结构体:用结构体的话,读写两侧
// 会一起跟着结构体走 —— 把 TableName() 改掉(也就是"换了一张表"这个缺陷)
// 测试照样全绿。
func putGrant(t *testing.T, gdb *gorm.DB, planId int, modelGroup string) {
	t.Helper()
	require.NoError(t, gdb.Exec(
		"INSERT INTO qy_plan_group_grants (plan_id, model_group, sort_order, operator_id, created_at, updated_at) "+
			"VALUES (?, ?, 0, 0, 1, 1)", planId, modelGroup).Error)
}

// putPolicy 直接往扩展库写一条余额使用范围。
func putPolicy(t *testing.T, gdb *gorm.DB, planId int, scope string) {
	t.Helper()
	require.NoError(t, gdb.Exec(
		"INSERT INTO qy_plan_balance_policy (plan_id, balance_scope, note, operator_id, updated_at) "+
			"VALUES (?, ?, '', 0, 1) ON CONFLICT(plan_id) DO UPDATE SET balance_scope = excluded.balance_scope",
		planId, scope).Error)
}

// auditActions 读出全部审计记录的 action + result,按写入顺序。
func auditActions(t *testing.T, gdb *gorm.DB) []string {
	t.Helper()
	var rows []qymodel.AuditLog
	require.NoError(t, gdb.Order("id asc").Find(&rows).Error)
	out := make([]string, 0, len(rows))
	for _, r := range rows {
		out = append(out, r.Action+":"+r.Result)
	}
	return out
}
