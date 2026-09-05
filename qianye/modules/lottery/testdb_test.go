package lottery

import (
	"strconv"
	"testing"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/qianye/modules/stardust"

	"github.com/glebarez/sqlite"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

// testdb_test.go —— 本包数据库测试的共用夹具:扩展库 + 星屑账本。
//
// # 为什么这些用例必须真的跑一遍数据库
//
// 本模块的资金正确性几乎全部住在 **WHERE 条件与 RowsAffected** 里:reserveEntry
// 那条带 status='published' 的 CAS、stardust.Debit 的 `available >= ?`、
// uk(act_id, entry_id, kind) 的唯一键。mock 掉 GORM 等于把被测对象换成测试
// 自己写的假设 —— 那正是这一类缺陷能活下来的原因。
//
// # 钱只经账本进出
//
// 参与费与派奖全部走 stardust.Credit / Debit(扩展库单事务),所以每一个要跑
// 报名 / 退款 / 派奖的夹具都必须把 stardust 的表一起建出来,种子余额也只能经
// 账本入账 —— 直接 INSERT qy_sd_balance 会让 I0(余额 = Σ流水)在测试里就不
// 成立,而那正是 refundAmountOf 等判据赖以成立的前提。

// extTables 是本包逻辑会碰到的全部扩展库表:自己的十二张 + stardust 的六张。
func extTables() []any {
	return append(tables(), stardust.Tables()...)
}

// newFundTestDB 建一个承载 extTables 的内存库。
//
// 不接到 db.Get():只有那些直接拿句柄调包内函数的用例用它;要跑 handler 的
// 用例走 newPayoutEnv(payout_retry_db_test.go),那一份会把句柄借给 db.Get()。
func newFundTestDB(t *testing.T) *gorm.DB {
	t.Helper()
	gdb, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	require.NoError(t, err)
	sqlDB, err := gdb.DB()
	require.NoError(t, err)
	// 内存库按连接隔离,多连接会各看到一个空库。
	sqlDB.SetMaxOpenConns(1)
	require.NoError(t, gdb.AutoMigrate(extTables()...))
	t.Cleanup(func() { _ = sqlDB.Close() })
	return gdb
}

// seedActivity 插入一场已发布、正在开放的活动。
func seedActivity(t *testing.T, gdb *gorm.DB, mutate func(*Activity)) *Activity {
	t.Helper()
	now := common.GetTimestamp()
	a := &Activity{
		ActNo:            newActNo(),
		Kind:             KindDraw,
		Status:           StatusPublished,
		Title:            "测试场",
		StakeQuota:       1000,
		OpenAt:           now - 60,
		CloseAt:          now + 3600,
		DrawAt:           now + 7200,
		SettleDeadline:   now + 86400,
		Algo:             AlgoV1,
		CommitHash:       "c0mm1t",
		MinEntriesToHold: 0,
		CreatedAt:        now,
	}
	if mutate != nil {
		mutate(a)
	}
	require.NoError(t, gdb.Create(a).Error)
	return a
}

func loadAct(t *testing.T, gdb *gorm.DB, id int64) *Activity {
	t.Helper()
	var a Activity
	require.NoError(t, gdb.Where("id = ?", id).Take(&a).Error)
	return &a
}

// seedStardust 给一个用户入账 amount 星屑(kind=manual,与管理员手调同形)。
//
// 这是**唯一**允许在测试里凭空造星屑的口子,而且它走的就是生产上的记账函数:
// 余额行、累计列、流水行三者由 Credit 一次写齐,I0 从种子那一刻起就成立。
func seedStardust(t *testing.T, gdb *gorm.DB, userId int, amount int64) {
	t.Helper()
	require.NoError(t, gdb.Transaction(func(tx *gorm.DB) error {
		_, err := stardust.Credit(tx, stardust.Posting{
			UserId:    userId,
			Kind:      stardust.KindManual,
			Amount:    amount,
			IdemScope: "test_seed",
			IdemKey:   strconv.Itoa(userId) + ":" + stardust.NewLedgerNo(),
			Remark:    "测试种子",
		})
		return err
	}))
}

// stardustOf 读某人此刻可用的星屑;没有余额行视为 0(从没获得过星屑的人本来就是 0)。
func stardustOf(t *testing.T, gdb *gorm.DB, userId int) int64 {
	t.Helper()
	var rows []stardust.Balance
	require.NoError(t, gdb.Where("user_id = ?", userId).Find(&rows).Error)
	if len(rows) == 0 {
		return 0
	}
	return rows[0].Available
}

// ledgerRowsOf 读出某人在一场活动上的全部流水(按落库顺序)。
func ledgerRowsOf(t *testing.T, gdb *gorm.DB, userId int, actNo string) []stardust.Ledger {
	t.Helper()
	var rows []stardust.Ledger
	require.NoError(t, gdb.Where("user_id = ? AND act_no = ?", userId, actNo).
		Order("id asc").Find(&rows).Error)
	return rows
}

// seedTicket 把一张调用方拼好的票(用户、金额、选项、选号、user_ref)走**真实的参与
// 事务**(reserveEntry → stardust.Debit → 落票 → 推进 chain_head)落库,计数、链与
// 流水的初始状态因此与线上完全一致。票号、幂等键、指纹、时间戳缺省时补上。
//
// 先给该用户入账恰好 e.Amount 星屑再扣掉:调用前后余额不变,而账本上多出
// 一正一负两行 —— 退款侧按票上的 ledger_no 取权威金额(refundAmountOf),
// 少了那一行流水,"钱确实收过"就没有证据。
//
// 活动行在事务之外读:内存库只有一条连接,事务里再开一次查询会自己把自己饿死
// (线上的 settleEntryTx 也是拿调用方读好的活动进来的)。
func seedTicket(t *testing.T, gdb *gorm.DB, act *Activity, e *Entry) {
	t.Helper()
	seedStardust(t, gdb, e.UserId, e.Amount)
	cur := loadAct(t, gdb, act.Id)
	e.ActId = cur.Id
	e.Status = EntrySuccess
	if e.EntryNo == "" {
		e.EntryNo = newEntryNo()
	}
	if e.IdemKey == "" {
		e.IdemKey = buildIdemKey(cur.ActNo, newEntryNo())
	}
	if e.UserRef == "" {
		e.UserRef = UserRef("salt", e.UserId)
	}
	if e.CreatedAt == 0 {
		e.CreatedAt = common.GetTimestamp()
	}
	if e.Fingerprint == "" {
		e.Fingerprint = entryFingerprint(cur, e.UserId, e.Amount, e.OptNo, e.Pick)
	}
	require.NoError(t, gdb.Transaction(func(tx *gorm.DB) error {
		return settleEntryTx(tx, cur, Rules{}, e, 0)
	}))
}

// seedSuccessEntry 是 seedTicket 的常用形状:某人按金额买一张不带选号的票。
func seedSuccessEntry(t *testing.T, gdb *gorm.DB, act *Activity, userId int, amount int64) *Entry {
	t.Helper()
	e := &Entry{UserId: userId, Amount: amount}
	seedTicket(t, gdb, act, e)
	return e
}
