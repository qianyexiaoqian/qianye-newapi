package lottery

import (
	"context"
	"sync/atomic"
	"testing"
	_ "unsafe" // go:linkname 需要

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/qianye/config"
	"github.com/QuantumNous/new-api/qianye/modules/stardust"

	"github.com/glebarez/sqlite"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

// payout_retry_db_test.go —— "一笔已经确定要发的钱,永远存在一条能把它发出去的路"。
//
// 出款现在是**一个扩展库事务**:CAS paying→paid、活动合计、stardust.Credit 三件事
// 同生共死。失败的尝试整笔回滚、账本上一个字节都不留,所以重试永远是干净的重来。
// 这一组锁住的不变量:
//
//  1. 一笔 planned 出款被 worker 驱动之后:余额 += 金额、账本恰好一行(kind 对、
//     幂等键 lotpay:<payout_no>、act_no 冗余在行上)、出款行 paid 且 order_no 指回流水。
//  2. 重入是安全的:卡在 paying 的行(进程在认领与事务之间崩溃)会被再次捡起并只发一次;
//     已经 paid 的行永远不再入账;账本上同一笔出款的幂等键只可能有一行。
//  3. 失败的处置:预算未耗尽 → failed 退避;耗尽 → held + 红点;到账即溢出 → 立即 held。
//  4. 管理端「重试」只把 held / failed 推回 planned 并清零次数,paid 永远不可重试。
//  5. markPayoutPaid 只认 paying:planned 直接推成 paid 等于系统认为钱给过了而用户永远收不到。

//go:linkname qyDBHandle github.com/QuantumNous/new-api/qianye/db.handle
var qyDBHandle atomic.Pointer[gorm.DB]

//go:linkname qyDBHealthy github.com/QuantumNous/new-api/qianye/db.healthy
var qyDBHealthy atomic.Bool

//go:linkname qyConfig github.com/QuantumNous/new-api/qianye/config.current
var qyConfig atomic.Pointer[config.Config]

// newPayoutEnv 建一个装好扩展库句柄与配置的测试环境。
//
// 星屑的六张表必须一起迁移:出款的判据就是"账本上这一笔到底记没记、余额到底
// 动没动",少了它测的就不是真实的判据。
func newPayoutEnv(t *testing.T, lot config.Lottery) *gorm.DB {
	t.Helper()
	gdb, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	require.NoError(t, err)
	sqlDB, err := gdb.DB()
	require.NoError(t, err)
	sqlDB.SetMaxOpenConns(1)
	require.NoError(t, gdb.AutoMigrate(extTables()...))

	prevHandle := qyDBHandle.Swap(gdb)
	prevHealthy := qyDBHealthy.Swap(true)
	prevCfg := qyConfig.Swap(&config.Config{
		Enabled: true,
		Lottery: lot,
	})
	t.Cleanup(func() {
		qyDBHandle.Store(prevHandle)
		qyDBHealthy.Store(prevHealthy)
		qyConfig.Store(prevCfg)
		_ = sqlDB.Close()
	})
	return gdb
}

func seedPayout(t *testing.T, gdb *gorm.DB, actId int64, mutate func(*Payout)) *Payout {
	t.Helper()
	p := &Payout{
		PayoutNo: newPayoutNo(), ActId: actId, EntryId: 1, Kind: PayoutPrize,
		UserId: 9, AmountQuota: 500, Status: PayoutPlanned, CreatedAt: common.GetTimestamp(),
	}
	if mutate != nil {
		mutate(p)
	}
	require.NoError(t, gdb.Create(p).Error)
	return p
}

func reloadPayout(t *testing.T, gdb *gorm.DB, payoutNo string) *Payout {
	t.Helper()
	var p Payout
	require.NoError(t, gdb.Where("payout_no = ?", payoutNo).Take(&p).Error)
	return &p
}

// ledgerByIdem 读账本上某个幂等键的全部行。同一笔出款只可能有一行 —— 这是
// "重试永远不会重复发钱"的账本侧证据。
func ledgerByIdem(t *testing.T, gdb *gorm.DB, scope, key string) []stardust.Ledger {
	t.Helper()
	var rows []stardust.Ledger
	require.NoError(t, gdb.Where("idem_scope = ? AND idem_key = ?", scope, key).Find(&rows).Error)
	return rows
}

// 一笔 planned 出款被 worker 驱动之后:钱到账、账本恰好一行、出款行指回那一行。
func TestDrivePayouts_CreditsOnceAndLinksTheLedgerRow(t *testing.T) {
	gdb := newPayoutEnv(t, config.Lottery{Enabled: true, PayoutMaxAttempts: 8})
	act := seedActivity(t, gdb, nil)
	prize := seedPayout(t, gdb, act.Id, func(p *Payout) { p.AmountQuota = 1500 })
	refund := seedPayout(t, gdb, act.Id, func(p *Payout) {
		p.EntryId, p.Kind, p.UserId, p.AmountQuota = 2, PayoutRefund, 10, 700
	})

	DrivePayouts(context.Background())

	afterPrize := reloadPayout(t, gdb, prize.PayoutNo)
	assert.Equal(t, PayoutPaid, afterPrize.Status)
	assert.NotZero(t, afterPrize.SettledAt)
	assert.Equal(t, 1, afterPrize.Attempts)
	assert.EqualValues(t, 1500, stardustOf(t, gdb, 9), "中奖者的星屑必须正好多出奖金")

	rows := ledgerByIdem(t, gdb, idemScopePayout, payoutIdemKey(prize.PayoutNo))
	require.Len(t, rows, 1, "一笔出款在账本上恰好一行")
	assert.Equal(t, string(stardust.KindLotPrize), rows[0].Kind)
	assert.EqualValues(t, 1500, rows[0].Amount)
	assert.Equal(t, prize.PayoutNo, rows[0].RefNo)
	assert.Equal(t, act.ActNo, rows[0].ActNo, "act_no 必须冗余在流水上:活动删除之后只有它还能归拢")
	assert.Equal(t, rows[0].LedgerNo, afterPrize.OrderNo, "出款行必须指回入账的那一行流水")

	afterRefund := reloadPayout(t, gdb, refund.PayoutNo)
	assert.Equal(t, PayoutPaid, afterRefund.Status)
	assert.EqualValues(t, 700, stardustOf(t, gdb, 10))
	refundRows := ledgerByIdem(t, gdb, idemScopePayout, payoutIdemKey(refund.PayoutNo))
	require.Len(t, refundRows, 1)
	assert.Equal(t, string(stardust.KindLotRefund), refundRows[0].Kind, "退款走 lot_refund,不与派奖混在一列")
}

// 重入:卡在 paying 的行会被再次捡起且只发一次;已 paid 的行永远不再入账。
//
// worker 先把行 CAS 成 paying 再开事务。进程若在这两步之间崩溃,这一行就停在
// paying —— 它必须仍在扫描集合里,否则一笔中奖派奖永久丢失还不告警。
func TestDrivePayouts_ReentryPaysExactlyOnce(t *testing.T) {
	gdb := newPayoutEnv(t, config.Lottery{Enabled: true, PayoutMaxAttempts: 8})
	act := seedActivity(t, gdb, nil)
	stuck := seedPayout(t, gdb, act.Id, func(p *Payout) {
		p.Status = PayoutPaying
		p.Attempts = 1
	})

	DrivePayouts(context.Background())
	require.Equal(t, PayoutPaid, reloadPayout(t, gdb, stuck.PayoutNo).Status,
		"卡在 paying 的行必须被再次捡起 —— 只扫 planned/failed 会让它永久丢失")
	require.EqualValues(t, 500, stardustOf(t, gdb, 9))

	// 再跑几轮:paid 是终态,一个字节都不许再动。
	for i := 0; i < 3; i++ {
		DrivePayouts(context.Background())
	}
	assert.EqualValues(t, 500, stardustOf(t, gdb, 9), "已到账的出款绝不能被重复入账")
	assert.Len(t, ledgerByIdem(t, gdb, idemScopePayout, payoutIdemKey(stuck.PayoutNo)), 1)

	// 账本侧的最后一道:即便出款行被人推回 planned(直接改库),幂等键也会让
	// 第二次 Credit 记不上 —— 余额不动、行数不变、出款行照样收尾成 paid。
	require.NoError(t, gdb.Model(&Payout{}).Where("id = ?", stuck.Id).
		Updates(map[string]any{"status": PayoutPlanned, "attempts": 0}).Error)
	DrivePayouts(context.Background())
	assert.EqualValues(t, 500, stardustOf(t, gdb, 9), "同一个幂等键第二次入账必须是空转")
	assert.Len(t, ledgerByIdem(t, gdb, idemScopePayout, payoutIdemKey(stuck.PayoutNo)), 1)
	assert.Equal(t, PayoutPaid, reloadPayout(t, gdb, stuck.PayoutNo).Status)
}

// 预算未耗尽 → failed 退避;耗尽 → held + 红点。事务整体回滚,账本上没有残行。
func TestFailPayout_RetriesWithBackoffThenHolds(t *testing.T) {
	gdb := newPayoutEnv(t, config.Lottery{Enabled: true, PayoutMaxAttempts: 3})
	act := seedActivity(t, gdb, nil)
	p := seedPayout(t, gdb, act.Id, func(p *Payout) {
		p.Status = PayoutPaying
		p.Attempts = 1
	})

	failPayout(context.Background(), gdb, p, assert.AnError)
	after := reloadPayout(t, gdb, p.PayoutNo)
	assert.Equal(t, PayoutFailed, after.Status, "预算还没耗尽时退回 failed 等下一轮")
	assert.Greater(t, after.NextAttemptAt, common.GetTimestamp(), "必须退避,不能立刻重打")
	assert.NotEmpty(t, after.LastError)
	assert.Zero(t, stardustOf(t, gdb, p.UserId), "失败的尝试一分钱都不许留在账上")

	exhausted := reloadPayout(t, gdb, p.PayoutNo)
	require.NoError(t, gdb.Model(&Payout{}).Where("id = ?", exhausted.Id).
		Updates(map[string]any{"status": PayoutPaying, "attempts": 3}).Error)
	exhausted.Status, exhausted.Attempts = PayoutPaying, 3
	failPayout(context.Background(), gdb, exhausted, assert.AnError)

	held := reloadPayout(t, gdb, p.PayoutNo)
	assert.Equal(t, PayoutHeld, held.Status, "预算耗尽必须转人工,绝不留在 paying 或静默放弃")
	var flags []Flag
	require.NoError(t, gdb.Where("act_id = ? AND code = ?", act.Id, FlagPayoutStuck).Find(&flags).Error)
	assert.Len(t, flags, 1, "转人工必须同时落一条红点,否则没人知道有笔钱卡住了")
}

// 到账后余额会超出系统上界:那不是重试能解决的,立即 held 交人。
func TestFailPayout_OverflowHoldsImmediately(t *testing.T) {
	gdb := newPayoutEnv(t, config.Lottery{Enabled: true, PayoutMaxAttempts: 8})
	act := seedActivity(t, gdb, nil)
	p := seedPayout(t, gdb, act.Id, func(p *Payout) {
		p.Status = PayoutPaying
		p.Attempts = 1
	})

	failPayout(context.Background(), gdb, p, stardust.ErrOverflow)

	after := reloadPayout(t, gdb, p.PayoutNo)
	assert.Equal(t, PayoutHeld, after.Status)
	assert.Contains(t, after.LastError, "上界")
}

// 出款 worker 真的撞上溢出:余额贴着上界的人拿一笔奖 → held、余额不动、账本无残行。
func TestDrivePayouts_HoldsWhenCreditWouldOverflow(t *testing.T) {
	gdb := newPayoutEnv(t, config.Lottery{Enabled: true, PayoutMaxAttempts: 8})
	act := seedActivity(t, gdb, nil)
	seedStardust(t, gdb, 9, int64(common.MaxQuota)-10)
	p := seedPayout(t, gdb, act.Id, func(p *Payout) { p.AmountQuota = 11 })

	DrivePayouts(context.Background())

	after := reloadPayout(t, gdb, p.PayoutNo)
	assert.Equal(t, PayoutHeld, after.Status)
	assert.EqualValues(t, int64(common.MaxQuota)-10, stardustOf(t, gdb, 9), "溢出的入账必须整笔回滚")
	assert.Empty(t, ledgerByIdem(t, gdb, idemScopePayout, payoutIdemKey(p.PayoutNo)),
		"回滚之后账本上不许留下幂等残行 —— 留下就等于这一笔永远记不上")
}

// 管理端重试:held / failed 推回 planned 并清零次数;paid 是终态,拒绝。
func TestRetryPayout_RequeuesHeldAndFailedButNeverPaid(t *testing.T) {
	gdb := newPayoutEnv(t, config.Lottery{Enabled: true, PayoutMaxAttempts: 8})
	act := seedActivity(t, gdb, nil)
	held := seedPayout(t, gdb, act.Id, func(p *Payout) {
		p.Status, p.Attempts, p.NextAttemptAt = PayoutHeld, 8, common.GetTimestamp()+300
	})
	failed := seedPayout(t, gdb, act.Id, func(p *Payout) {
		p.EntryId, p.Status, p.Attempts = 2, PayoutFailed, 3
	})
	paid := seedPayout(t, gdb, act.Id, func(p *Payout) { p.EntryId, p.Status = 3, PayoutPaid })

	for _, no := range []string{held.PayoutNo, failed.PayoutNo} {
		require.NoError(t, RetryPayout(context.Background(), no))
		after := reloadPayout(t, gdb, no)
		assert.Equal(t, PayoutPlanned, after.Status)
		assert.Zero(t, after.Attempts, "重排必须清零次数,否则预算早已耗尽的行下一轮就又 held 了")
		assert.Zero(t, after.NextAttemptAt)
	}
	require.ErrorIs(t, RetryPayout(context.Background(), paid.PayoutNo), errStatusConflict)
	assert.Equal(t, PayoutPaid, reloadPayout(t, gdb, paid.PayoutNo).Status)
	require.ErrorIs(t, RetryPayout(context.Background(), "LP-not-there"), errPayoutNotFound)

	// 重排之后 worker 真的能把它发出去 —— 这是"钱永远发得出去"的全部意义。
	DrivePayouts(context.Background())
	assert.Equal(t, PayoutPaid, reloadPayout(t, gdb, held.PayoutNo).Status)
	assert.Equal(t, PayoutPaid, reloadPayout(t, gdb, failed.PayoutNo).Status)
	assert.EqualValues(t, 1000, stardustOf(t, gdb, 9), "两笔各 500 的重排出款都要真的到账")
}

// markPayoutPaid 只认 paying:planned 与 held 都不能被它直接推成 paid。
//
// planned 直接被推成已到账意味着一笔从未执行的出款被记成已给过;held 同理 ——
// 它只能经「重试」回到队列,再由 worker 在同一个事务里入账并收尾。
func TestMarkPayoutPaid_OnlyMovesPayingRows(t *testing.T) {
	gdb := newPayoutEnv(t, config.Lottery{Enabled: true})
	act := seedActivity(t, gdb, nil)
	planned := seedPayout(t, gdb, act.Id, nil)
	held := seedPayout(t, gdb, act.Id, func(p *Payout) { p.EntryId, p.Status = 2, PayoutHeld })
	paying := seedPayout(t, gdb, act.Id, func(p *Payout) { p.EntryId, p.Status = 3, PayoutPaying })

	for _, no := range []string{planned.PayoutNo, held.PayoutNo} {
		var moved bool
		require.NoError(t, gdb.Transaction(func(tx *gorm.DB) error {
			var err error
			moved, err = markPayoutPaid(tx, no)
			return err
		}))
		assert.False(t, moved)
	}
	assert.Equal(t, PayoutPlanned, reloadPayout(t, gdb, planned.PayoutNo).Status,
		"从未执行过的出款被记成已到账,等于系统认为钱给过了而用户永远收不到")
	assert.Equal(t, PayoutHeld, reloadPayout(t, gdb, held.PayoutNo).Status)

	var moved bool
	require.NoError(t, gdb.Transaction(func(tx *gorm.DB) error {
		var err error
		moved, err = markPayoutPaid(tx, paying.PayoutNo)
		return err
	}))
	assert.True(t, moved)
	paidRow := reloadPayout(t, gdb, paying.PayoutNo)
	assert.Equal(t, PayoutPaid, paidRow.Status)
	assert.NotZero(t, paidRow.SettledAt)

	// 第二次是空转:paid 是终态,CAS 只可能成功一次。
	require.NoError(t, gdb.Transaction(func(tx *gorm.DB) error {
		var err error
		moved, err = markPayoutPaid(tx, paying.PayoutNo)
		return err
	}))
	assert.False(t, moved)
}
