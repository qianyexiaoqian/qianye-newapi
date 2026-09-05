package commission

import (
	"go/ast"
	"net/http/httptest"
	"path/filepath"
	"strconv"
	"sync/atomic"
	"testing"
	_ "unsafe" // //go:linkname 需要

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/qianye/config"
	qymodel "github.com/QuantumNous/new-api/qianye/model"
	"github.com/QuantumNous/new-api/qianye/modules/invite"
	"github.com/QuantumNous/new-api/qianye/modules/stardust"
	"github.com/QuantumNous/new-api/setting/operation_setting"

	"github.com/gin-gonic/gin"
	"github.com/glebarez/sqlite"
	"github.com/shopspring/decimal"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// 本文件是 commission 包的数据库测试脚手架。
//
// 为什么必须要有它:本模块被审计出的几条资损缺陷(游标推进、carry 永远发不出去、
// 幂等命中被当成新建)全都**不在算术里,而在调度层与 WHERE 条件里**。只测纯函数
// 的结果是:把修复整段回滚,测试照样全绿。要真正捕捉这类缺陷,必须让数据库把
// pendingInviters 的两路查询、settleUser 的事务、writeAccrual 的 ON CONFLICT 语义、
// 自动入账的两阶段各自真跑一遍。
//
// 扩展库受支持的部署方言是 MySQL 与 PostgreSQL(见 qianye/db.DialectorFor),
// 这里用 sqlite 当测试方言。因此本文件里的断言一律只依赖跨库通用语义
// (比较、SUM、DISTINCT、ORDER BY/LIMIT、ON CONFLICT DO NOTHING 的
// RowsAffected=0/1),不碰任何 MySQL 专有语法。

// qyDBHandle 指向 qianye/db 包里的连接句柄。
//
// db.Get() 读的是包内未导出的 atomic.Pointer,而本包的函数全都通过 db.Get()
// 自取句柄、不接受注入。用 //go:linkname 把那个句柄借出来,是在**不改动任何
// 生产代码**的前提下让这些函数跑在测试库上的唯一办法。
//
//go:linkname qyDBHandle github.com/QuantumNous/new-api/qianye/db.handle
var qyDBHandle atomic.Pointer[gorm.DB]

//go:linkname qyDBHealthy github.com/QuantumNous/new-api/qianye/db.healthy
var qyDBHealthy atomic.Bool

// qyConfig 同理指向 qianye/config 的当前配置快照。
//
//go:linkname qyConfig github.com/QuantumNous/new-api/qianye/config.current
var qyConfig atomic.Pointer[config.Config]

// extTables 是 commission 相关逻辑会碰到的全部扩展库表。
//
// qy_settings 必须一起建:effective() 每次都会查它,表不存在时它会吞掉错误
// 退回 YAML 默认值 —— 那会让"运营覆盖没生效"这类问题在测试里完全看不见。
// 邀请关系快照与跨节点失效流水归 invite,但计佣路径会写它们,同样要建。
func extTables() []any {
	tables := append(Mod{}.Tables(),
		&invite.InviteRelation{}, &invite.CacheInvalidation{},
		&qymodel.Setting{}, &qymodel.KV{}, &qymodel.AuditLog{},
		&qymodel.FundOrder{}, &qymodel.TaskLease{},
	)
	// 星屑那七张表也要在:D-16 之后自动入账把钱写进 qy_sd_balance / qy_sd_ledger,
	// 而那一步与佣金余额的搬运在**同一个事务**里。少了它们,入账不是"断言失败"
	// 而是整条链路报 no such table,看起来像是测试夹具坏了。
	return append(tables, stardust.Tables()...)
}

// newTestDB 建一个承载扩展库全部 commission 表的测试库,并把它接到 db.Get()。
//
// 用 t.TempDir() 下的文件库而不是 ":memory:" + SetMaxOpenConns(1):
// settleUser 在自己的事务里调 effective(),而 effective() 走的是 db.Get()
// 拿到的**另一条**连接。连接数被限成 1 时那一步会永远等下去。":memory:" 又按
// 连接隔离,放开连接数就会各看到一个空库,所以只能落到文件上。
func newTestDB(t *testing.T) *gorm.DB {
	t.Helper()
	dsn := filepath.Join(t.TempDir(), "qy_ext.db") +
		"?_pragma=journal_mode(WAL)&_pragma=busy_timeout(5000)"
	gdb, err := gorm.Open(sqlite.Open(dsn), &gorm.Config{})
	require.NoError(t, err)
	sqlDB, err := gdb.DB()
	require.NoError(t, err)

	// db.LockForUpdate 对 sqlite 方言本来就跳过 FOR UPDATE;再把 FOR 子句渲染成空
	// 是兜底。测试是单协程的,行锁在这里本来也没有语义。
	gdb.ClauseBuilders["FOR"] = func(clause.Clause, clause.Builder) {}

	require.NoError(t, gdb.AutoMigrate(extTables()...))

	prev := qyDBHandle.Swap(gdb)
	prevHealthy := qyDBHealthy.Swap(true)
	resetCommissionCaches()
	t.Cleanup(func() {
		qyDBHandle.Store(prev)
		qyDBHealthy.Store(prevHealthy)
		resetCommissionCaches()
		_ = sqlDB.Close()
	})
	return gdb
}

// resetCommissionCaches 清掉本包所有跨测试残留的进程内缓存。
//
// effective() 缓存 60 秒、分组费率缓存 60 秒、invite 的邀请关系一旦解析就常驻。
// 不清的话上一个测试的库内容会渗进下一个测试,而且渗进来的往往正好是让断言
// 变成永真的那一份。
func resetCommissionCaches() {
	invalidateSettings()
	invalidateGroupRates()
	invite.InvalidateInviter(0)
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

// commissionConfig 返回一份开着佣金与邀请的最小配置。
//
// invite.enabled 必须为真:邀请关系判定(invite.InviteeEligible)在它关着时对
// 所有人回"不合格",本包的每一条计佣路径都会安静地什么都不写。
func commissionConfig(minSettle int64) *config.Config {
	c := &config.Config{}
	c.Enabled = true
	c.Invite.Enabled = true
	c.Invite.InviterCacheSecs = 300
	c.Commission.Enabled = true
	c.Commission.MinSettleStardust = minSettle
	c.Commission.Levels = 1
	c.Commission.MinCreditStardust = 500000
	c.Commission.CreditIntervalSecs = 300
	c.Commission.SettleIntervalSecs = 300
	return c
}

// commissionRateConfig 返回一份带全局默认费率(百分比入参)的配置。
func commissionRateConfig(topupPercent, consumePercent string) *config.Config {
	c := commissionConfig(1)
	c.Commission.TopupRateBps = mustRateUnits(topupPercent)
	c.Commission.ConsumeRateBps = mustRateUnits(consumePercent)
	return c
}

func mustRateUnits(percent string) int {
	units, err := config.RatePercentUnits(percent)
	if err != nil {
		panic(err)
	}
	return units
}

// dayConfig 返回一份把日界偏移设成 offsetMinutes 的配置(日界归 invite 段)。
func dayConfig(offsetMinutes int) *config.Config {
	c := commissionConfig(0)
	c.Invite.DayOffsetMinutes = offsetMinutes
	return c
}

// useMainDB 临时替换主库句柄,只建被测到的表。
//
// 主库方言同样是 sqlite:invite 的邀请关系解析、自动入账的 MainApply 都会真的
// 往这里读写。
func useMainDB(t *testing.T, models ...any) *gorm.DB {
	t.Helper()
	dsn := filepath.Join(t.TempDir(), "qy_main.db") +
		"?_pragma=journal_mode(WAL)&_pragma=busy_timeout(5000)"
	gdb, err := gorm.Open(sqlite.Open(dsn), &gorm.Config{})
	require.NoError(t, err)
	sqlDB, err := gdb.DB()
	require.NoError(t, err)
	require.NoError(t, gdb.AutoMigrate(models...))

	prev := model.DB
	model.DB = gdb
	invite.InvalidateInviter(0)
	t.Cleanup(func() {
		model.DB = prev
		invite.InvalidateInviter(0)
		_ = sqlDB.Close()
	})
	return gdb
}

// ensureMainDB 在测试还没装主库时装一个只有 users 表的主库。
func ensureMainDB(t *testing.T) *gorm.DB {
	t.Helper()
	if model.DB != nil {
		return model.DB
	}
	return useMainDB(t, &model.User{})
}

// seedUser 往主库插一个账号:id / 用户名 / 上线 / 注册时刻,并让邀请缓存按主库重读。
func seedUser(t *testing.T, mainDB *gorm.DB, id int, username string, inviterId int, createdAt int64) {
	t.Helper()
	require.NoError(t, mainDB.Clauses(clause.OnConflict{UpdateAll: true}).Create(&model.User{
		Id: id, Username: username, Password: "x", InviterId: inviterId, CreatedAt: createdAt,
		Group: "default", Status: common.UserStatusEnabled, Role: common.RoleCommonUser,
		AffCode: "aff" + strconv.Itoa(id),
	}).Error)
	invite.InvalidateInviter(id)
}

// cacheUser 把一个账号(及其上线与分组)放进主库,让 invite 的邀请关系解析能读到它。
//
// inviterId 为 0 表示"这个人自己没有上线",上线正是这种样子 ——
// resolveInviterPricing 读的是同一份缓存里那条记录的 Group(= 这个账号的分组)。
// 曾经它直接往邀请缓存里塞条目;缓存归 invite 之后只能走主库这条真路径。
func cacheUser(t *testing.T, userId, inviterId int, group string) {
	t.Helper()
	mainDB := ensureMainDB(t)
	// 调用方可能只建了 top_ups 一类的表,users 表按需补建。
	require.NoError(t, mainDB.AutoMigrate(&model.User{}))
	require.NoError(t, mainDB.Clauses(clause.OnConflict{UpdateAll: true}).Create(&model.User{
		Id: userId, Username: "u" + itoa(userId), Password: "x", InviterId: inviterId,
		CreatedAt: common.GetTimestamp() - 30*86400, Group: group,
		Status: common.UserStatusEnabled, Role: common.RoleCommonUser,
		AffCode: "aff" + itoa(userId),
	}).Error)
	invite.InvalidateInviter(userId)
}

// useMoneyGlobals 固定额度单位,让换算类断言可复现。
func useMoneyGlobals(t *testing.T, _ float64, quotaPerUnit float64) {
	t.Helper()
	prevQPU := common.QuotaPerUnit
	common.QuotaPerUnit = quotaPerUnit
	t.Cleanup(func() {
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
		RateUnits:     500, // 5%(万分比)
		GrossAmount:   decimal.NewFromInt(500),
		SettledAmount: decimal.Zero,
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
func settleUserOnce(t *testing.T, inviterId int) {
	t.Helper()
	more, err := settleUser(inviterId)
	require.NoError(t, err)
	require.False(t, more, "样本超过了单轮取批上界,这条用例的结论不再成立")
}

// assertLedgerIdentity 断言 I2:可用 + 已入账 == 已结算 − 已冲正。
//
// D-16 之前中间还有一项「在途」—— 佣金记星辉、入账跨库,存在"已开单、主库还没
// 落定"的中间态。现在入账是本地事务,那一项不再存在。
func assertLedgerIdentity(t *testing.T, b *Balance) {
	t.Helper()
	require.NotNil(t, b)
	assert.Equal(t, b.TotalEarned-b.TotalClawback,
		b.Available+b.Credited,
		"可用+已入账 必须恒等于 已结算−已冲正,破了这条账本就与结算流水对不回去了")
}

// isSelectorCall 判断一个调用是不是 x.<name>(...) 的形状。
func isSelectorCall(call *ast.CallExpr, name string) bool {
	sel, ok := call.Fun.(*ast.SelectorExpr)
	return ok && sel.Sel.Name == name
}

// withCompliance 把支付合规门置成 confirmed:充值计佣与星屑侧的下线充值返共用这道闸,
// 没确认时 accrueTopUp 一进门就返回 nil、什么都不写。
func withCompliance(t *testing.T, confirmed bool) {
	t.Helper()
	ps := operation_setting.GetPaymentSetting()
	prev := *ps
	ps.ComplianceConfirmed = confirmed
	if confirmed {
		ps.ComplianceTermsVersion = operation_setting.CurrentComplianceTermsVersion
	}
	t.Cleanup(func() { *ps = prev })
}

// callUserHandler 以普通用户身份调一个用户端处理器(UserAuth 会写 id / username / role)。
func callUserHandler(t *testing.T, userId int, method, target string, h gin.HandlerFunc) *httptest.ResponseRecorder {
	t.Helper()
	gin.SetMode(gin.TestMode)
	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	c.Request = httptest.NewRequest(method, target, nil)
	c.Set("id", userId)
	c.Set("username", "u"+itoa(userId))
	c.Set("role", common.RoleCommonUser)
	h(c)
	return rec
}

func decimalZero() decimal.Decimal { return decimal.Zero }
