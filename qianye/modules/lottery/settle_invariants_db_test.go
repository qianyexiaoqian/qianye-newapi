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

// settle_invariants_db_test.go —— 封盘与收尾两步上"钱不会被永久扣着"。
//
// 这两条都不是理论风险,而是曾经真实存在的死局:
//
//	封盘把名单读在事务外   → 一张票恰好在读完之后落库,活动被自己的防篡改校验
//	                        永久拒绝开奖,全场的钱既不派也不退。
//	收尾不核对退款覆盖     → 一张刚落库的票被当成"已结算"放行,活动进 finished,
//	                        而 runSettle 再也不扫 finished,那笔钱永久退不回来。

// 封盘之后,库里的 success 集合必须与已公开的 roster_hash 逐字一致。
//
// 名单必须在状态 CAS 之后、同一个事务内读:并发的报名要么排在 CAS 前面(进名单),
// 要么排在后面(被 reserveEntry 的 status='published' 复检整笔顶回),没有第三种。
func TestLockActivity_FreezesRosterConsistentWithStoredEntries(t *testing.T) {
	gdb := newFundTestDB(t)
	now := common.GetTimestamp()
	act := seedActivity(t, gdb, func(a *Activity) { a.CloseAt = now + 3600 })
	seedSuccessEntry(t, gdb, act, 1, 1000)
	seedSuccessEntry(t, gdb, act, 2, 1000)

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
	assert.Equal(t, 2, count)

	// 封盘之后的报名必须被活动行上的状态复检整笔顶回:名单不会在冻结之后长出第 N+1 条。
	late := &Entry{EntryNo: newEntryNo(), ActId: act.Id, UserId: 3, Amount: 1000, Status: EntrySuccess}
	err = gdb.Transaction(func(tx *gorm.DB) error {
		_, err := reserveEntry(tx, after, Rules{}, late, 0)
		return err
	})
	require.ErrorIs(t, err, errClosingSoon)
	assert.Equal(t, 2, loadAct(t, gdb, act.Id).EntrySeq)
}

// 全额退款的活动:只要还有一张票没被登记退款,就绝不能收尾。
//
// finishIfDone 只看"出款是否都到终态"时,一张在 planFullRefund 读完名单之后才
// 落库的票会通过 —— 活动被推成 finished,而 runSettle 再也不扫 finished,
// 那个人的参与费永久退不回来。
func TestFinishIfDone_WaitsUntilEveryRefundIsPlanned(t *testing.T) {
	gdb := newFundTestDB(t)
	act := seedActivity(t, gdb, nil)
	seedSuccessEntry(t, gdb, act, 6, 1000)
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
	assert.EqualValues(t, 1000, payouts[0].AmountQuota, "退款金额来自账本流水")
	require.NoError(t, gdb.Model(&Payout{}).Where("id = ?", payouts[0].Id).
		Updates(map[string]any{"status": PayoutPaid, "settled_at": common.GetTimestamp()}).Error)

	finishIfDone(context.Background(), gdb, loadAct(t, gdb, act.Id))
	final := loadAct(t, gdb, act.Id)
	assert.Equal(t, StatusFinished, final.Status)
	assert.Equal(t, int64(1000), final.RefundQuota)
}

// 竞猜奖池不允许越过单笔出款的容量。
//
// 越界的池子会让独中的那个人的赔付在 stardust.Credit 入口就被拒,活动永远收不了尾;
// 而逐笔截断里的额度饱和又会把超出的部分静默吞掉,分配结果与第三方复算不一致。
// 判定在活动行锁内、以**含本次**的 pool_quota 为准,所以走 reserveEntry 而不是
// 直接调 checkCaps:那条 UPDATE 先累加、闸门再判,被拒时整笔回滚。
func TestReserveEntry_RejectsGuessEntryThatWouldOverflowPool(t *testing.T) {
	gdb := newFundTestDB(t)
	act := seedActivity(t, gdb, func(a *Activity) {
		a.Kind = KindGuess
		a.PoolQuota = int64(common.MaxQuota) - 10
	})

	e := &Entry{EntryNo: newEntryNo(), ActId: act.Id, UserId: 8, OptNo: 1, Amount: 100}
	err := gdb.Transaction(func(tx *gorm.DB) error {
		_, err := reserveEntry(tx, act, Rules{}, e, 0)
		return err
	})
	require.ErrorIs(t, err, errCapReached)

	after := loadAct(t, gdb, act.Id)
	assert.EqualValues(t, int64(common.MaxQuota)-10, after.PoolQuota, "被拒的那一注不许留在奖池里")
	assert.Zero(t, after.EntrySeq)

	// 差 10 就够时照常放行 —— 否则这条闸门就是把竞猜整个关掉。
	ok := &Entry{EntryNo: newEntryNo(), ActId: act.Id, UserId: 9, OptNo: 1, Amount: 10}
	require.NoError(t, gdb.Transaction(func(tx *gorm.DB) error {
		_, err := reserveEntry(tx, act, Rules{}, ok, 0)
		return err
	}))
	assert.EqualValues(t, int64(common.MaxQuota), loadAct(t, gdb, act.Id).PoolQuota)
}

// 池子已经越界时,分配层必须整体失败而不是发出一笔被静默钳过的钱。
func TestSplitPool_RefusesPoolBeyondSingleTransferCap(t *testing.T) {
	all := []RosterLine{{EntryNo: "a", Amount: int64(common.MaxQuota)}, {EntryNo: "b", Amount: 1000}}
	winners := []RosterLine{{EntryNo: "a", Amount: int64(common.MaxQuota)}}

	_, _, err := SplitPool(int64(common.MaxQuota)+1000, 500, all, winners)
	require.ErrorIs(t, err, ErrPoolNotConserved)
}

// 没填单注上限的竞猜必须兜到 max_stake_stardust,而不是算术上界。
func TestAcceptAmount_FallsBackToMaxStakeStardust(t *testing.T) {
	prev := qyConfig.Swap(&config.Config{
		Enabled: true,
		Lottery: config.Lottery{Enabled: true, MaxStakeStardust: 5_000_000},
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
