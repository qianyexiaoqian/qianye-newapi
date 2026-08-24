package commission

import (
	"path/filepath"
	"strconv"
	"sync/atomic"
	"testing"
	_ "unsafe" // //go:linkname 需要

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/qianye/config"
	qymodel "github.com/QuantumNous/new-api/qianye/model"
	"github.com/QuantumNous/new-api/setting/operation_setting"

	"github.com/glebarez/sqlite"
	"github.com/shopspring/decimal"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// 本文件是 commission 包的数据库测试脚手架。
//
// 为什么必须要有它:本模块被审计出的几条资损缺陷(A4 游标推进、A5 carry
// 永远发不出去、B5 幂等命中被当成新建)全都**不在算术里,而在调度层与
// WHERE 条件里**。只测纯函数的结果是:把修复整段回滚,测试照样全绿 ——
// 这正是上一轮实际发生过的事。要真正捕捉这类缺陷,必须让数据库把
// pendingInviters 的两路查询、settleUser 的事务、writeAccrual 的
// ON CONFLICT 语义各自真跑一遍。
//
// 扩展库受支持的部署方言是 MySQL 与 PostgreSQL(见 qianye/db.DialectorFor),
// 这里用 sqlite 当测试方言。因此本文件里的断言一律只依赖跨库通用语义
// (比较、SUM、DISTINCT、ORDER BY/LIMIT、ON CONFLICT DO NOTHING 的
// RowsAffected=0/1),不碰任何 MySQL 专有语法。
//
// 曾经有一处差异被"刻意不覆盖":writeAccrual 的 Accumulate 分支用
// ON CONFLICT DO UPDATE,而命中时 MySQL 的 RowsAffected 是 2、PostgreSQL 是
// **1**(与新插入同值)、sqlite 是 1,于是 `== 1` 这条"是不是新建"的判据在
// PG 上给出相反答案。不覆盖并不能让问题消失 —— 它只是让下一个开始消费那个
// 返回值的人得不到任何提示。该分支现已改写成 DoNothing + 冲突时补一条
// UPDATE(三家统一),并由 accrual_upsert_crossdb_test.go 在真 MySQL /
// 真 PostgreSQL 上逐格覆盖。

// qyDBHandle 指向 qianye/db 包里的连接句柄。
//
// db.Get() 读的是包内未导出的 atomic.Pointer,而 pendingInviters / settleUser /
// writeAccrual 全都通过 db.Get() 自取句柄、不接受注入。db.Init 又只会拨 MySQL。
// 用 //go:linkname 把那个句柄借出来,是在**不改动任何生产代码**的前提下
// 让这些函数跑在测试库上的唯一办法。
//
//go:linkname qyDBHandle github.com/QuantumNous/new-api/qianye/db.handle
var qyDBHandle atomic.Pointer[gorm.DB]

// qyConfig 同理指向 qianye/config 的当前配置快照。
// config.Load() 只能从磁盘 YAML 读,测试需要直接给出配置结构。
//
//go:linkname qyConfig github.com/QuantumNous/new-api/qianye/config.current
var qyConfig atomic.Pointer[config.Config]

// extTables 是 commission 相关逻辑会碰到的全部扩展库表。
//
// qy_settings 必须一起建:effective() 每次都会查它,表不存在时它会吞掉错误
// 退回 YAML 默认值 —— 那会让"运营覆盖没生效"这类问题在测试里完全看不见。
func extTables() []any {
	return []any{
		&Accrual{}, &Balance{}, &Settlement{}, &InviteRelation{}, &FreezeRecord{},
		&GroupRate{}, &FiatRate{}, &SettleRun{}, &CacheInvalidation{},
		&qymodel.Setting{}, &qymodel.KV{}, &qymodel.AuditLog{},
	}
}

// newTestDB 建一个承载扩展库全部 commission 表的测试库,并把它接到 db.Get()。
//
// 用 t.TempDir() 下的文件库而不是 ":memory:" + SetMaxOpenConns(1):
// settleUser 在自己的事务里调 effective(),而 effective() 走的是 db.Get()
// 拿到的**另一条**连接(它读 qy_settings,不在事务上下文里)。连接数被限成 1
// 时那一步会永远等下去。":memory:" 又按连接隔离,放开连接数就会各看到一个空库,
// 所以只能落到文件上。WAL 让事务持写锁期间另一条连接仍能读。
func newTestDB(t *testing.T) *gorm.DB {
	t.Helper()
	dsn := filepath.Join(t.TempDir(), "qy_ext.db") +
		"?_pragma=journal_mode(WAL)&_pragma=busy_timeout(5000)"
	gdb, err := gorm.Open(sqlite.Open(dsn), &gorm.Config{})
	require.NoError(t, err)
	sqlDB, err := gdb.DB()
	require.NoError(t, err)

	// db.LockForUpdate 无条件挂 clause.Locking{Strength:"UPDATE"}(扩展库固定
	// 是 MySQL,那里不需要 SQLite 分支),而 sqlite 不认 FOR UPDATE 语法。
	// 把 FOR 子句渲染成空,让语句其余部分 —— WHERE / ORDER BY / LIMIT,也就是
	// 这几条缺陷真正藏身的地方 —— 原样交给数据库执行。
	// 测试是单协程的,行锁在这里本来也没有语义;并发加锁语义不在本文件的射程内。
	gdb.ClauseBuilders["FOR"] = func(clause.Clause, clause.Builder) {}

	require.NoError(t, gdb.AutoMigrate(extTables()...))

	prev := qyDBHandle.Swap(gdb)
	resetCommissionCaches()
	t.Cleanup(func() {
		qyDBHandle.Store(prev)
		resetCommissionCaches()
		_ = sqlDB.Close()
	})
	return gdb
}

// resetCommissionCaches 清掉本包所有跨测试残留的进程内缓存。
//
// effective() 缓存 60 秒、blockedInvitees 缓存 60 秒、refSalt 与邀请关系
// 一旦解析就常驻。不清的话上一个测试的库内容会渗进下一个测试,
// 而且渗进来的往往正好是让断言变成永真的那一份。
func resetCommissionCaches() {
	invalidateSettings()
	invalidateBlocked()
	invalidateGroupRates()
	invalidateFiatRates()
	invalidateInviter(0)
	saltOnce.Lock()
	saltCache = ""
	saltOnce.Unlock()
}

// useConfig 临时替换扩展的全局配置快照。
func useConfig(t *testing.T, cfg *config.Config) {
	t.Helper()
	prev := qyConfig.Swap(cfg)
	invalidateSettings()
	t.Cleanup(func() {
		qyConfig.Store(prev)
		invalidateSettings()
	})
}

// useMainDB 临时替换主库句柄(top_ups 扫描要用),只建被测到的表。
func useMainDB(t *testing.T, models ...any) *gorm.DB {
	t.Helper()
	gdb, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	require.NoError(t, err)
	sqlDB, err := gdb.DB()
	require.NoError(t, err)
	sqlDB.SetMaxOpenConns(1)
	require.NoError(t, gdb.AutoMigrate(models...))

	prev := model.DB
	model.DB = gdb
	t.Cleanup(func() {
		model.DB = prev
		_ = sqlDB.Close()
	})
	return gdb
}

// useMoneyGlobals 固定汇率与额度单位,让法币折算的断言可复现。
// 两者都是管理员可热改的全局变量,不钉住就没法断言 available_fiat。
func useMoneyGlobals(t *testing.T, usdRate float64, quotaPerUnit float64) {
	t.Helper()
	prevRate := operation_setting.USDExchangeRate
	prevQPU := common.QuotaPerUnit
	operation_setting.USDExchangeRate = usdRate
	common.QuotaPerUnit = quotaPerUnit
	t.Cleanup(func() {
		operation_setting.USDExchangeRate = prevRate
		common.QuotaPerUnit = prevQPU
	})
}

// seedBalance 插入一行佣金余额。amount 是 unsettled_amount 的十进制字面量。
func seedBalance(t *testing.T, gdb *gorm.DB, userId int, unsettled string) *Balance {
	t.Helper()
	now := common.GetTimestamp()
	b := &Balance{
		UserId:          userId,
		UnsettledAmount: decimal.RequireFromString(unsettled),
		AvailableFiat:   decimal.Zero,
		CreatedAt:       now,
		UpdatedAt:       now,
	}
	require.NoError(t, gdb.Create(b).Error)
	return b
}

// seedAccrual 插入一条计佣行。唯一索引(accrual_no、idem_scope+idem_key)
// 都从 seq 派生,调用方只要给不同的序号就不会互相冲突。
func seedAccrual(t *testing.T, gdb *gorm.DB, seq int, mutate func(*Accrual)) *Accrual {
	t.Helper()
	now := common.GetTimestamp()
	a := &Accrual{
		AccrualNo:     "CA-SEED-" + strconv.Itoa(seq),
		IdemScope:     SourceTopup,
		IdemKey:       "topup:seed-" + strconv.Itoa(seq),
		InviterId:     1,
		InviteeId:     900,
		SourceType:    SourceTopup,
		BaseQuota:     10000,
		BaseMoney:     decimal.Zero,
		RateUnits:     500, // 5%(内部整数费率 = 百分比 × 100)
		GrossAmount:   decimal.NewFromInt(500),
		SettledAmount: decimal.Zero,
		UsdRate:       decimal.NewFromInt(7),
		Status:        StatusAccrued,
		MatureAt:      now - 3600, // 默认已成熟,否则结算根本不会看它
		CreatedAt:     now,
		UpdatedAt:     now,
	}
	if mutate != nil {
		mutate(a)
	}
	require.NoError(t, gdb.Create(a).Error)
	return a
}

// settlementsOf 读出某个邀请人名下的全部结算单,按落库顺序。
func settlementsOf(t *testing.T, gdb *gorm.DB, userId int) []Settlement {
	t.Helper()
	var rows []Settlement
	require.NoError(t, gdb.Where("user_id = ?", userId).Order("id asc").Find(&rows).Error)
	return rows
}

// balanceOf 回读余额行;不存在时返回 nil,用于断言"没有凭空建行"。
func balanceOf(t *testing.T, gdb *gorm.DB, userId int) *Balance {
	t.Helper()
	var rows []Balance
	require.NoError(t, gdb.Where("user_id = ?", userId).Find(&rows).Error)
	if len(rows) == 0 {
		return nil
	}
	return &rows[0]
}

// settleUserOnce 断言 settleUser 既没报错,也没有"还有一批没吸收完"。
//
// settleUser 从只返回 error 改成 (more, error) 是因为一日一结算把它的单轮
// 取批上界(settleAccrualBatch)变成了**日级**上界。既有用例的样本都远小于
// 一批,more 恒为 false —— 若哪天不是了,那说明样本本身已经越过了这条上界,
// 断言必须当场红,而不是让"只结算了一部分"悄悄通过。
func settleUserOnce(t *testing.T, inviterId int) {
	t.Helper()
	more, err := settleUser(inviterId)
	require.NoError(t, err)
	require.False(t, more, "样本超过了单轮取批上界,这条用例的结论不再成立")
}
