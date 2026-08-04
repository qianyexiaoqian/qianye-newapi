package lottery

import (
	"context"
	"testing"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/qianye/config"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

// settle_invariants_db_test.go —— 封盘、取消与收尾三步上"钱不会被永久扣着"。
//
// 这三条都不是理论风险,而是收敛前真实存在的死局:
//
//	封盘把名单读在事务外   → 一条在途参与恰好落定,活动被自己的防篡改校验
//	                        永久拒绝开奖,全场的钱既不派也不退。
//	取消跳过 pending 清扫  → 在途条目永远没人收敛,活动永久停在 settling,
//	                        还永久占着一个并发活动名额。
//	收尾不核对退款覆盖     → 一条刚落定的参与被当成"已结算"放行,活动进 finished,
//	                        而 runSettle 再也不扫 finished,那笔钱永久退不回来。

// 封盘之后,库里的 success 集合必须与已公开的 roster_hash 逐字一致。
//
// 这正是"名单读在事务外"会破坏的不变量:那时读到的是清扫之前的一份快照,
// 而清扫与落库之间任何一条 pending 转 success,都会让重算结果与公开值对不上,
// 开奖从此被永久拒绝。
func TestLockActivity_FreezesRosterConsistentWithStoredEntries(t *testing.T) {
	gdb := newFundTestDB(t)
	now := common.GetTimestamp()
	act := seedActivity(t, gdb, func(a *Activity) { a.CloseAt = now + 3600 })

	done := seedPendingEntry(t, gdb, act, 1, 1000)
	require.NoError(t, gdb.Transaction(func(tx *gorm.DB) error {
		return markEntrySuccess(tx, done.EntryNo, nil)
	}))
	inFlight := seedPendingEntry(t, gdb, act, 2, 1000)

	// 到点封盘。
	require.NoError(t, gdb.Model(&Activity{}).Where("id = ?", act.Id).
		Update("close_at", now-1).Error)
	require.NoError(t, lockActivity(context.Background(), gdb, loadAct(t, gdb, act.Id)))

	after := loadAct(t, gdb, act.Id)
	require.Equal(t, StatusLocked, after.Status)

	roster, err := loadRoster(context.Background(), gdb, act.Id)
	require.NoError(t, err)
	hash, count := RosterHash(after.ActNo, after.CommitHash, rosterLines(roster))
	assert.Equal(t, after.RosterHash, hash, "公开的名单哈希必须能被库里的数据复算出来")
	assert.Equal(t, after.RosterCount, count)
	assert.Equal(t, 1, count, "在途条目不算进有效名单")

	var excluded Entry
	require.NoError(t, gdb.Where("entry_no = ?", inFlight.EntryNo).Take(&excluded).Error)
	assert.Equal(t, EntryExcluded, excluded.Status)
	assert.Equal(t, 0, after.PendingCount, "pending_count 必须在封盘时一次性回落")
}

// 封盘之后已经被标 excluded 的条目,收尾回调再也拉不回 success。
//
// 这是上一条的另一半:清扫先于名单读取,意味着晚到的收尾必然撞在
// `status = 'pending'` 的 CAS 上落空 —— 名单不会在冻结之后长出第 N+1 条。
func TestMarkEntrySuccess_CannotResurrectExcludedEntry(t *testing.T) {
	gdb := newFundTestDB(t)
	now := common.GetTimestamp()
	act := seedActivity(t, gdb, func(a *Activity) { a.CloseAt = now + 3600 })
	e := seedPendingEntry(t, gdb, act, 3, 1000)

	require.NoError(t, gdb.Model(&Activity{}).Where("id = ?", act.Id).
		Update("close_at", now-1).Error)
	require.NoError(t, lockActivity(context.Background(), gdb, loadAct(t, gdb, act.Id)))

	require.NoError(t, gdb.Transaction(func(tx *gorm.DB) error {
		return markEntrySuccess(tx, e.EntryNo, nil)
	}))

	var stored Entry
	require.NoError(t, gdb.Where("entry_no = ?", e.EntryNo).Take(&stored).Error)
	assert.Equal(t, EntryExcluded, stored.Status)
	assert.Equal(t, int64(0), loadAct(t, gdb, act.Id).PoolQuota, "被排除的条目绝不能计进奖池")
}

// 取消必须与封盘做同一次 pending 清扫。
//
// 少了它,在途条目永远停在 pending:convergeExcluded 只处理 excluded,
// finishIfDone 把 pending 计入未结算,活动永久停在 settling。
func TestExcludePendingEntries_SweepsAndRollsBackCount(t *testing.T) {
	gdb := newFundTestDB(t)
	act := seedActivity(t, gdb, nil)
	a := seedPendingEntry(t, gdb, act, 4, 1000)
	b := seedPendingEntry(t, gdb, act, 5, 1000)
	require.Equal(t, 2, loadAct(t, gdb, act.Id).PendingCount)

	now := common.GetTimestamp()
	var swept int64
	require.NoError(t, gdb.Transaction(func(tx *gorm.DB) error {
		n, err := excludePendingEntries(tx, act.Id, now)
		swept = n
		return err
	}))
	assert.Equal(t, int64(2), swept)
	assert.Equal(t, 0, loadAct(t, gdb, act.Id).PendingCount)

	for _, entryNo := range []string{a.EntryNo, b.EntryNo} {
		var e Entry
		require.NoError(t, gdb.Where("entry_no = ?", entryNo).Take(&e).Error)
		assert.Equal(t, EntryExcluded, e.Status)
	}

	// 重复清扫是空操作:计数绝不能被扣成负数。
	require.NoError(t, gdb.Transaction(func(tx *gorm.DB) error {
		n, err := excludePendingEntries(tx, act.Id, now)
		assert.Equal(t, int64(0), n)
		return err
	}))
	assert.Equal(t, 0, loadAct(t, gdb, act.Id).PendingCount)
}

// 全额退款的活动:只要还有一条有效参与没被登记退款,就绝不能收尾。
//
// finishIfDone 只看"出款是否都到终态"与"条目是否都已结算"时,一条在
// planFullRefund 读完名单之后才落定的参与会同时通过这两道 —— 活动被推成
// finished,而 runSettle 再也不扫 finished,那个人的参与费永久退不回来。
func TestFinishIfDone_WaitsUntilEveryRefundIsPlanned(t *testing.T) {
	gdb := newFundTestDB(t)
	act := seedActivity(t, gdb, nil)
	e := seedPendingEntry(t, gdb, act, 6, 1000)
	require.NoError(t, gdb.Transaction(func(tx *gorm.DB) error {
		return markEntrySuccess(tx, e.EntryNo, nil)
	}))
	require.NoError(t, gdb.Model(&Activity{}).Where("id = ?", act.Id).
		Updates(map[string]any{"status": StatusSettling, "outcome": OutcomeCancelled}).Error)

	// 退款还没登记:此刻收尾就是把一笔该退的钱永久关在门外。
	finishIfDone(context.Background(), gdb, loadAct(t, gdb, act.Id))
	assert.Equal(t, StatusSettling, loadAct(t, gdb, act.Id).Status)

	stored := loadAct(t, gdb, act.Id)
	require.NoError(t, planFullRefund(context.Background(), gdb, stored))
	var payouts []Payout
	require.NoError(t, gdb.Where("act_id = ?", act.Id).Find(&payouts).Error)
	require.Len(t, payouts, 1)
	require.NoError(t, gdb.Model(&Payout{}).Where("id = ?", payouts[0].Id).
		Updates(map[string]any{"status": PayoutPaid, "settled_at": common.GetTimestamp()}).Error)

	finishIfDone(context.Background(), gdb, loadAct(t, gdb, act.Id))
	final := loadAct(t, gdb, act.Id)
	assert.Equal(t, StatusFinished, final.Status)
	assert.Equal(t, int64(1000), final.RefundQuota)
}

// 竞猜奖池不允许越过单笔出款的容量。
//
// 越界的池子会让独中的那个人的赔付在 twophase 入口就被拒,活动永远收不了尾;
// 而逐笔截断里的额度饱和又会把超出的部分静默吞掉,分配结果与第三方复算不一致。
func TestCheckCaps_RejectsGuessEntryThatWouldOverflowPool(t *testing.T) {
	gdb := newFundTestDB(t)
	act := seedActivity(t, gdb, func(a *Activity) {
		a.Kind = KindGuess
		a.PoolQuota = int64(common.MaxQuota) - 10
	})

	e := &Entry{EntryNo: newEntryNo(), ActId: act.Id, UserId: 8, OptNo: 1, Amount: 100}
	err := gdb.Transaction(func(tx *gorm.DB) error {
		var cur Activity
		if err := tx.Where("id = ?", act.Id).Take(&cur).Error; err != nil {
			return err
		}
		return checkCaps(tx, &cur, e)
	})
	require.ErrorIs(t, err, errCapReached)
}

// 池子已经越界时,分配层必须整体失败而不是发出一笔被静默钳过的钱。
func TestSplitPool_RefusesPoolBeyondSingleTransferCap(t *testing.T) {
	all := []RosterLine{{EntryNo: "a", Amount: int64(common.MaxQuota)}, {EntryNo: "b", Amount: 1000}}
	winners := []RosterLine{{EntryNo: "a", Amount: int64(common.MaxQuota)}}

	_, _, err := SplitPool(int64(common.MaxQuota)+1000, 500, all, winners)
	require.ErrorIs(t, err, ErrPoolNotConserved)
}

// 没填单注上限的竞猜必须兜到 max_stake_quota,而不是 int32 上界。
func TestAcceptAmount_FallsBackToMaxStakeQuota(t *testing.T) {
	prev := qyConfig.Swap(&config.Config{
		Enabled: true,
		Lottery: config.Lottery{Enabled: true, MaxStakeQuota: 5_000_000},
	})
	t.Cleanup(func() { qyConfig.Store(prev) })

	act := &Activity{Kind: KindGuess, StakeQuota: 1000}
	_, err := acceptAmount(act, EntryInput{OptNo: 1, Amount: 5_000_001})
	require.ErrorIs(t, err, errBadAmount)

	ok, err := acceptAmount(act, EntryInput{OptNo: 1, Amount: 5_000_000})
	require.NoError(t, err)
	assert.Equal(t, int64(5_000_000), ok)
}

// 增量道绝不往已被重算确认的日桶上再加一遍。
//
// 收敛前这道防护写成 ON CONFLICT ... WHERE,而扩展库只支持 MySQL,
// 那里的驱动**整段丢弃 Where** —— 生产上防护恒为空操作,冷启动时历史消费会被
// 算成两倍,"近 N 日消费"这道门槛直接被腰斩。方向恰好与设计声称的"只会少算"相反。
func TestApplySpendDelta_NeverTouchesFinalizedBuckets(t *testing.T) {
	gdb := newFundTestDB(t)
	require.NoError(t, gdb.Create(&SpendDaily{UserId: 1, Day: 20260101, Quota: 500, Cnt: 1, Final: true}).Error)
	require.NoError(t, gdb.Create(&SpendDaily{UserId: 2, Day: 20260101, Quota: 500, Cnt: 1}).Error)

	err := applySpendDelta(context.Background(), gdb, map[spendKey]*SpendDaily{
		{UserId: 1, Day: 20260101}: {Quota: 300, Cnt: 2},
		{UserId: 2, Day: 20260101}: {Quota: 300, Cnt: 2},
		{UserId: 3, Day: 20260101}: {Quota: 700, Cnt: 3},
	})
	require.NoError(t, err)

	read := func(userId int) SpendDaily {
		var row SpendDaily
		require.NoError(t, gdb.Where("user_id = ? AND day = ?", userId, 20260101).Take(&row).Error)
		return row
	}
	assert.Equal(t, int64(500), read(1).Quota, "重算是权威,已确认的日桶再加一遍就是双计")
	assert.Equal(t, int64(800), read(2).Quota)
	assert.Equal(t, int64(700), read(3).Quota)
	assert.Equal(t, 3, read(2).Cnt)
}
