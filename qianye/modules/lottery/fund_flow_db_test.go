package lottery

import (
	"testing"

	"github.com/QuantumNous/new-api/common"

	"github.com/glebarez/sqlite"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

// fund_flow_db_test.go —— "任何一步失败,钱都不会丢、也不会多发"。
//
// # 为什么这些用例必须真的跑一遍数据库
//
// 本模块的资金正确性几乎全部住在 **WHERE 条件与 RowsAffected** 里:
// `WHERE status = 'pending'` 的 CAS、`WHERE pending_count > 0` 的非负保护、
// uk(act_id, entry_id, kind) 的唯一键。mock 掉 GORM 等于把被测对象换成测试
// 自己写的假设 —— 那正是这一类缺陷能活下来的原因。
//
// # 这里锁住的四条不变量
//
//  1. 收尾函数**幂等**:markEntrySuccess / markPayoutPaid 被调第二次不会
//     二次记账。这是 settleGuard 敢在 Execute 之后补做一次的前提。
//  2. 失败回滚**只回滚预占,不回滚链**:seq 与 chain_hash 永久保留,
//     计数带非负保护。
//  3. 出款计划**重复触发不产生双份**:唯一键在计划层就把它挡住,
//     而不是等到重复发钱之后再对账。
//  4. 奖池分配**精确守恒**:Σpay + fee == pool,一个单位都不许多也不许少。

func newFundTestDB(t *testing.T) *gorm.DB {
	t.Helper()
	gdb, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	require.NoError(t, err)
	sqlDB, err := gdb.DB()
	require.NoError(t, err)
	// 内存库按连接隔离,多连接会各看到一个空库。
	sqlDB.SetMaxOpenConns(1)
	require.NoError(t, gdb.AutoMigrate(tables()...))
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

// seedPendingEntry 走真实的 reserveEntry 落一条 pending 参与,
// 这样计数与链的初始状态与线上完全一致。
func seedPendingEntry(t *testing.T, gdb *gorm.DB, act *Activity, userId int, amount int64) *Entry {
	t.Helper()
	e := &Entry{
		EntryNo:   newEntryNo(),
		ActId:     act.Id,
		IdemKey:   buildIdemKey(act.ActNo, newEntryNo()),
		UserId:    userId,
		UserRef:   UserRef("salt", userId),
		Amount:    amount,
		Status:    EntryPending,
		OrderNo:   "TR-" + newEntryNo(),
		CreatedAt: common.GetTimestamp(),
	}
	require.NoError(t, gdb.Transaction(func(tx *gorm.DB) error {
		return reserveEntry(tx, act, Rules{}, e)
	}))
	return e
}

func loadAct(t *testing.T, gdb *gorm.DB, id int64) *Activity {
	t.Helper()
	var a Activity
	require.NoError(t, gdb.Where("id = ?", id).Take(&a).Error)
	return &a
}

// ─────────────────── 1. 预占:闸门在锁内,失败不留残余 ───────────────────

// 时间窗之外的报名必须整体回滚 —— 连 seq 都不能消耗。
//
// 这一条守的是"失败尝试膨胀哈希链"的下界:reserveEntry 的第一条 UPDATE
// 同时是闸门与序号分配,闸门不中就没有任何写入落地。
func TestReserveEntry_ClosedWindowLeavesNoTrace(t *testing.T) {
	gdb := newFundTestDB(t)
	now := common.GetTimestamp()
	act := seedActivity(t, gdb, func(a *Activity) { a.CloseAt = now - 1 })

	e := &Entry{EntryNo: newEntryNo(), ActId: act.Id, UserId: 7, Amount: 1000, Status: EntryPending}
	err := gdb.Transaction(func(tx *gorm.DB) error { return reserveEntry(tx, act, Rules{}, e) })
	require.ErrorIs(t, err, errClosingSoon)

	after := loadAct(t, gdb, act.Id)
	assert.Equal(t, 0, after.EntrySeq, "闸门不中时绝不能消耗序号,否则链上会出现无主的空洞")
	assert.Equal(t, 0, after.PendingCount)

	var n int64
	require.NoError(t, gdb.Model(&Entry{}).Count(&n).Error)
	assert.Zero(t, n)
}

// 全场名额是在活动行 X 锁下判定的,超出即整体回滚。
func TestReserveEntry_TotalEntryCapRejectsAndRollsBack(t *testing.T) {
	gdb := newFundTestDB(t)
	act := seedActivity(t, gdb, func(a *Activity) { a.MaxTotalEntries = 2 })

	seedPendingEntry(t, gdb, act, 1, 1000)
	// 第一条推成功,腾出 pending 名额但占住 active。
	require.NoError(t, gdb.Transaction(func(tx *gorm.DB) error {
		var first Entry
		require.NoError(t, tx.Where("act_id = ?", act.Id).Take(&first).Error)
		return markEntrySuccess(tx, first.EntryNo, nil)
	}))
	seedPendingEntry(t, gdb, act, 2, 1000)

	third := &Entry{EntryNo: newEntryNo(), ActId: act.Id, UserId: 3, Amount: 1000, Status: EntryPending}
	err := gdb.Transaction(func(tx *gorm.DB) error { return reserveEntry(tx, act, Rules{}, third) })
	require.ErrorIs(t, err, errCapReached)

	after := loadAct(t, gdb, act.Id)
	assert.Equal(t, 2, after.EntrySeq, "被拒的那次不留序号")
	assert.Equal(t, 1, after.ActiveCount)
	assert.Equal(t, 1, after.PendingCount)
}

// 同一用户存在未结算参与时必须拒绝。
//
// 这是**资金正确性条件**而不是风控偏好:两笔都在飞的时候,余额与名单的差额
// 无法归因到哪一笔,失败回滚也就无法确定该退谁。
func TestReserveEntry_RejectsWhileUserHasPendingEntry(t *testing.T) {
	gdb := newFundTestDB(t)
	act := seedActivity(t, gdb, nil)
	seedPendingEntry(t, gdb, act, 9, 1000)

	second := &Entry{EntryNo: newEntryNo(), ActId: act.Id, UserId: 9, Amount: 1000, Status: EntryPending}
	err := gdb.Transaction(func(tx *gorm.DB) error { return reserveEntry(tx, act, Rules{}, second) })
	assert.ErrorIs(t, err, errEntryInFlight)
}

// 链在 reserveEntry 里逐条推进,且第一条挂在 commit_hash 上。
func TestReserveEntry_ChainStartsAtCommitAndAdvances(t *testing.T) {
	gdb := newFundTestDB(t)
	act := seedActivity(t, gdb, nil)

	first := seedPendingEntry(t, gdb, act, 1, 1000)
	assert.Equal(t, act.CommitHash, first.PrevHash, "chain_0 必须是 commit_hash")
	assert.Equal(t, 1, first.Seq)

	// 推成功之后再来一条,prev 必须是上一条的 chain_hash。
	require.NoError(t, gdb.Transaction(func(tx *gorm.DB) error {
		return markEntrySuccess(tx, first.EntryNo, nil)
	}))
	second := seedPendingEntry(t, gdb, act, 2, 1000)

	assert.Equal(t, first.ChainHash, second.PrevHash)
	assert.Equal(t, 2, second.Seq)
	assert.Equal(t, second.ChainHash, loadAct(t, gdb, act.Id).ChainHead)
	assert.NotEqual(t, first.ChainHash, second.ChainHash)
}

// ─────────────────── 2. 收尾:幂等,且只记一次账 ───────────────────

// markEntrySuccess 被调第二次不能二次记账。
//
// 这是 settleGuard 的立身之本:它在 Execute 之后**无条件**再补做一次,
// 因为分不清 LocalCommit 到底跑没跑(路径 B/D/E 的返回值完全一样)。
// 这个函数一旦不幂等,每一笔正常参与都会把奖池记成两倍。
func TestMarkEntrySuccess_SecondCallDoesNotDoubleCount(t *testing.T) {
	gdb := newFundTestDB(t)
	act := seedActivity(t, gdb, nil)
	e := seedPendingEntry(t, gdb, act, 5, 2500)

	snap := &quotaSnapshot{Before: 10000, After: 7500, Applied: true}
	require.NoError(t, gdb.Transaction(func(tx *gorm.DB) error {
		return markEntrySuccess(tx, e.EntryNo, snap)
	}))
	require.NoError(t, gdb.Transaction(func(tx *gorm.DB) error {
		return markEntrySuccess(tx, e.EntryNo, snap)
	}))

	after := loadAct(t, gdb, act.Id)
	assert.Equal(t, 1, after.ActiveCount)
	assert.Equal(t, 0, after.PendingCount)
	assert.Equal(t, int64(2500), after.PoolQuota, "奖池只能被计入一次")

	var stored Entry
	require.NoError(t, gdb.Where("entry_no = ?", e.EntryNo).Take(&stored).Error)
	assert.Equal(t, EntrySuccess, stored.Status)
	assert.Equal(t, int64(7500), stored.QuotaAfter)
}

// 封盘时被标 excluded 的条目,后到的成功回写不得把它拉回 success。
//
// 这是"名单在揭示前已冻结"的下界:CAS 的 WHERE status='pending' 就是那道门。
// 拉回去意味着一张票在 roster_hash 公开之后才进入有效名单 —— 承诺当场作废。
func TestMarkEntrySuccess_DoesNotResurrectExcludedEntry(t *testing.T) {
	gdb := newFundTestDB(t)
	act := seedActivity(t, gdb, nil)
	e := seedPendingEntry(t, gdb, act, 6, 1000)

	require.NoError(t, gdb.Model(&Entry{}).Where("entry_no = ?", e.EntryNo).
		Update("status", EntryExcluded).Error)

	require.NoError(t, gdb.Transaction(func(tx *gorm.DB) error {
		return markEntrySuccess(tx, e.EntryNo, nil)
	}))

	var stored Entry
	require.NoError(t, gdb.Where("entry_no = ?", e.EntryNo).Take(&stored).Error)
	assert.Equal(t, EntryExcluded, stored.Status)
	assert.Zero(t, loadAct(t, gdb, act.Id).PoolQuota, "excluded 的票绝不能进奖池")
}

// 找不到明细时返回 nil 而不是 error:让补偿任务把资金单推进终态,
// 而不是在一条孤儿数据上无限重试。异常本身由对账任务与告警交给人。
func TestMarkEntrySuccess_MissingEntryDoesNotBlockCompensation(t *testing.T) {
	gdb := newFundTestDB(t)
	assert.NoError(t, gdb.Transaction(func(tx *gorm.DB) error {
		return markEntrySuccess(tx, "LE-不存在", nil)
	}))
}

// ─────────────────── 3. 失败回滚:只退预占,不动链 ───────────────────

// 失败必须回滚 pending_count 与选项聚合,但**不回滚 seq 与链环**。
//
// 删掉失败条目就是破链:它之后每一个用户手里的 chain_hash 都会对不上,
// 而那些值是平台自己签发给用户的,改不回来。
func TestMarkEntryFailed_RollsBackReservationButKeepsChain(t *testing.T) {
	gdb := newFundTestDB(t)
	act := seedActivity(t, gdb, func(a *Activity) { a.Kind = KindGuess })
	require.NoError(t, gdb.Create(&Option{ActId: act.Id, OptNo: 1, Label: "甲"}).Error)
	require.NoError(t, gdb.Create(&Option{ActId: act.Id, OptNo: 2, Label: "乙"}).Error)

	e := &Entry{
		EntryNo: newEntryNo(), ActId: act.Id, UserId: 4, OptNo: 1,
		Amount: 3000, Status: EntryPending, UserRef: UserRef("s", 4),
		CreatedAt: common.GetTimestamp(),
	}
	require.NoError(t, gdb.Transaction(func(tx *gorm.DB) error {
		return reserveEntry(tx, act, Rules{}, e)
	}))

	require.NoError(t, gdb.Transaction(func(tx *gorm.DB) error {
		return markEntryFailed(tx, e.EntryNo, "qy_lot_insufficient_quota")
	}))

	after := loadAct(t, gdb, act.Id)
	assert.Equal(t, 0, after.PendingCount)
	assert.Equal(t, 0, after.ActiveCount)
	assert.Equal(t, int64(0), after.PoolQuota)
	assert.Equal(t, 1, after.EntrySeq, "seq 绝不回收 —— 回收就是破链")
	assert.Equal(t, e.ChainHash, after.ChainHead, "链头停在失败条目上,它是链的一环")

	var opt Option
	require.NoError(t, gdb.Where("act_id = ? AND opt_no = ?", act.Id, 1).Take(&opt).Error)
	assert.Equal(t, int64(0), opt.BetQuota)
	assert.Equal(t, 0, opt.BetCount)

	var stored Entry
	require.NoError(t, gdb.Where("entry_no = ?", e.EntryNo).Take(&stored).Error)
	assert.Equal(t, EntryFailed, stored.Status)
	assert.Equal(t, "qy_lot_insufficient_quota", stored.FailCode)
	assert.NotEmpty(t, stored.ChainHash)
}

// 重复回滚不能把计数扣成负数。
//
// pending_count 一旦为负,全场名额闸门(active+pending > max)就永久失效,
// 那比少统计一笔严重得多 —— 这正是 `WHERE pending_count > 0` 存在的理由。
func TestMarkEntryFailed_NeverDrivesCountersNegative(t *testing.T) {
	gdb := newFundTestDB(t)
	act := seedActivity(t, gdb, nil)
	e := seedPendingEntry(t, gdb, act, 8, 1000)

	for i := 0; i < 3; i++ {
		require.NoError(t, gdb.Transaction(func(tx *gorm.DB) error {
			return markEntryFailed(tx, e.EntryNo, "boom")
		}))
	}
	after := loadAct(t, gdb, act.Id)
	assert.Equal(t, 0, after.PendingCount)
	assert.GreaterOrEqual(t, after.PendingCount, 0)
}

// ─────────────────── 4. 出款:计划不重复,收尾幂等 ───────────────────

// 开奖跑两遍不能产生双份出款计划。
//
// 重复触发是常态而不是异常:lease 易主、两节点同时到 draw_at、网络重试。
// uk(act_id, entry_id, kind) 让第二遍在**计划层**整体撞键,
// 而不是等到重复发钱之后再靠对账去追。
func TestPlanPayouts_RerunCreatesNoDuplicate(t *testing.T) {
	gdb := newFundTestDB(t)
	act := seedActivity(t, gdb, nil)
	plans := []PayoutPlan{
		{EntryId: 11, UserId: 1, Kind: PayoutPrize, Tier: 1, Amount: 5000},
		{EntryId: 12, UserId: 2, Kind: PayoutPrize, Tier: 2, Amount: 2000},
	}
	for i := 0; i < 2; i++ {
		require.NoError(t, gdb.Transaction(func(tx *gorm.DB) error {
			return PlanPayouts(tx, act.Id, plans)
		}))
	}
	var n int64
	require.NoError(t, gdb.Model(&Payout{}).Where("act_id = ?", act.Id).Count(&n).Error)
	assert.Equal(t, int64(2), n, "同一张票的同一类出款只能有一行")
}

// 同一张票可以同时有奖金与退款两行(kind 不同),但每一类只有一行。
func TestPlanPayouts_DifferentKindsCoexist(t *testing.T) {
	gdb := newFundTestDB(t)
	act := seedActivity(t, gdb, nil)
	require.NoError(t, gdb.Transaction(func(tx *gorm.DB) error {
		return PlanPayouts(tx, act.Id, []PayoutPlan{
			{EntryId: 21, UserId: 1, Kind: PayoutPrize, Amount: 100},
			{EntryId: 21, UserId: 1, Kind: PayoutRefund, Amount: 100},
		})
	}))
	var n int64
	require.NoError(t, gdb.Model(&Payout{}).Where("entry_id = ?", 21).Count(&n).Error)
	assert.Equal(t, int64(2), n)
}

// 0 元计划不落库:twophase 的入口要求 amount > 0,而 0 元出款在账面上
// 也不表达任何事实。它只可能来自奖池截断的残差为 0,守恒式仍然成立。
func TestPlanPayouts_SkipsZeroAmount(t *testing.T) {
	gdb := newFundTestDB(t)
	act := seedActivity(t, gdb, nil)
	require.NoError(t, gdb.Transaction(func(tx *gorm.DB) error {
		return PlanPayouts(tx, act.Id, []PayoutPlan{
			{EntryId: 31, UserId: 1, Kind: PayoutWin, Amount: 0},
			{EntryId: 32, UserId: 2, Kind: PayoutWin, Amount: 7},
		})
	}))
	var rows []Payout
	require.NoError(t, gdb.Where("act_id = ?", act.Id).Find(&rows).Error)
	require.Len(t, rows, 1)
	assert.Equal(t, int64(7), rows[0].AmountQuota)
}

// markPayoutPaid 只认 paying,且第二次调用是空操作。
//
// 只认 paying 是关键:planned 行直接被推成 paid 意味着一笔从未执行的出款
// 被记成已到账 —— 用户永远收不到钱,而系统认为已经给过了。
func TestMarkPayoutPaid_OnlyFromPayingAndIdempotent(t *testing.T) {
	gdb := newFundTestDB(t)
	act := seedActivity(t, gdb, nil)

	planned := &Payout{
		PayoutNo: newPayoutNo(), ActId: act.Id, EntryId: 41, Kind: PayoutPrize,
		UserId: 1, AmountQuota: 900, Status: PayoutPlanned, CreatedAt: common.GetTimestamp(),
	}
	require.NoError(t, gdb.Create(planned).Error)

	require.NoError(t, gdb.Transaction(func(tx *gorm.DB) error {
		return markPayoutPaid(tx, planned.PayoutNo)
	}))
	var still Payout
	require.NoError(t, gdb.Where("payout_no = ?", planned.PayoutNo).Take(&still).Error)
	assert.Equal(t, PayoutPlanned, still.Status, "planned 绝不能被直接推成已到账")

	require.NoError(t, gdb.Model(&Payout{}).Where("id = ?", planned.Id).
		Update("status", PayoutPaying).Error)
	for i := 0; i < 2; i++ {
		require.NoError(t, gdb.Transaction(func(tx *gorm.DB) error {
			return markPayoutPaid(tx, planned.PayoutNo)
		}))
	}
	var paid Payout
	require.NoError(t, gdb.Where("payout_no = ?", planned.PayoutNo).Take(&paid).Error)
	assert.Equal(t, PayoutPaid, paid.Status)
	assert.NotZero(t, paid.SettledAt)
}

// ─────────────────── 5. 奖池分配:精确守恒 ───────────────────

// SplitPool 的守恒式必须逐个用例精确成立,并且残差恰好归最后一名赢家。
//
// 各自四舍五入会让前 n-1 笔之和超过 net,最后一笔变成负数;而负额度出款
// 在 twophase 入口会被拒,整场结算就此卡死。这组用例锁的正是那条边界。
func TestSplitPool_ConservationInvariant(t *testing.T) {
	line := func(no string, amt int64) RosterLine {
		return RosterLine{EntryNo: no, UserRef: "u" + no, Amount: amt}
	}
	cases := []struct {
		name    string
		pool    int64
		feeBps  int
		all     []RosterLine
		winners []RosterLine
		wantFee int64
		wantPay []int64
	}{
		{
			name: "三人均分 100 且无手续费", pool: 100, feeBps: 0,
			all:     []RosterLine{line("a", 40), line("b", 30), line("c", 30)},
			winners: []RosterLine{line("a", 40), line("b", 30), line("c", 30)},
			// 全员猜中 = 无输家,全额退回本金,手续费一分不收。
			wantFee: 0, wantPay: []int64{40, 30, 30},
		},
		{
			name: "1 单位奖池分给 3 个赢家:残差归最后一名", pool: 4, feeBps: 0,
			all:     []RosterLine{line("a", 1), line("b", 1), line("c", 1), line("d", 1)},
			winners: []RosterLine{line("a", 1), line("b", 1), line("c", 1)},
			// net=4,前两笔各 floor(4*1/3)=1,最后一笔拿残差 2。
			wantFee: 0, wantPay: []int64{1, 1, 2},
		},
		{
			name: "5% 手续费,单人独中", pool: 1000, feeBps: 500,
			all:     []RosterLine{line("a", 400), line("b", 600)},
			winners: []RosterLine{line("a", 400)},
			wantFee: 50, wantPay: []int64{950},
		},
		{
			name: "全部猜错:全额退回,平台零收益", pool: 700, feeBps: 2000,
			all:     []RosterLine{line("a", 300), line("b", 400)},
			winners: nil,
			wantFee: 0, wantPay: []int64{300, 400},
		},
		{
			name: "百万级奖池的截断残差归最后一名赢家", pool: 1000000, feeBps: 500,
			all: []RosterLine{line("a", 333333), line("b", 333334), line("c", 333333)},
			// net = 950000,win = 666667。第一笔 floor(950000×333333/666667)
			// = 474999(真值 474999.28…),残差 475001 归 entry_no 最大的赢家。
			winners: []RosterLine{line("a", 333333), line("b", 333334)},
			wantFee: 50000, wantPay: []int64{474999, 475001},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			fee, shares, err := SplitPool(tc.pool, tc.feeBps, tc.all, tc.winners)
			require.NoError(t, err)
			assert.Equal(t, tc.wantFee, fee)

			got := make([]int64, 0, len(shares))
			var sum int64
			for _, s := range shares {
				got = append(got, s.Amount)
				sum += s.Amount
				assert.GreaterOrEqual(t, s.Amount, int64(0), "任何一笔出款都不得为负")
			}
			assert.Equal(t, tc.wantPay, got)
			assert.Equal(t, tc.pool, sum+fee,
				"守恒式必须精确成立:Σpay + fee == pool,一个单位都不许多也不许少")
		})
	}
}

// 手续费不产生资金单,因此它只体现为"少发出去的那一部分"。
// 这条断言把它钉死:平台拿走的永远等于 pool 减去实际发出的总额。
func TestSplitPool_PlatformTakeIsExactlyUnpaidRemainder(t *testing.T) {
	all := []RosterLine{
		{EntryNo: "a", Amount: 111},
		{EntryNo: "b", Amount: 222},
		{EntryNo: "c", Amount: 333},
	}
	winners := []RosterLine{all[0], all[1]}
	pool := int64(666)

	fee, shares, err := SplitPool(pool, 777, all, winners)
	require.NoError(t, err)

	var paid int64
	for _, s := range shares {
		paid += s.Amount
	}
	assert.Equal(t, fee, pool-paid)
	assert.Less(t, fee, pool, "手续费绝不能吃掉整个奖池")
}

// ─────────────────── 6. 抽取算法:确定且可复现 ───────────────────

// 同一份输入必须抽出同一份名单,且 allow_multi_win=false 时每人只占一个位。
//
// 可复现性本身就是公正性的一部分:验证者要能在自己的机器上算出同一个结果,
// 否则"你们说他中了"和"我算出来是他"之间没有任何桥梁。
func TestPickWinners_DeterministicAndDeduplicatesByUser(t *testing.T) {
	roster := []RosterLine{
		{EntryNo: "LE-a", UserRef: "u1", Amount: 1},
		{EntryNo: "LE-b", UserRef: "u1", Amount: 1},
		{EntryNo: "LE-c", UserRef: "u2", Amount: 1},
		{EntryNo: "LE-d", UserRef: "u3", Amount: 1},
	}
	tiers := []Tier{{Tier: 1, Count: 1, Amount: 500}, {Tier: 2, Count: 2, Amount: 100}}
	final := FinalSeed("ACT1", "deadbeef", "rosterhash", len(roster), AlgoV1)

	first := PickWinners(final, "ACT1", roster, tiers, false)
	second := PickWinners(final, "ACT1", roster, tiers, false)
	assert.Equal(t, first, second, "同一输入必须抽出同一份名单")

	require.Len(t, first, 3)
	seen := map[string]bool{}
	for _, w := range first {
		assert.False(t, seen[w.UserRef], "allow_multi_win=false 时同一人只能占一个中奖位")
		seen[w.UserRef] = true
	}
	assert.Equal(t, 1, first[0].Tier)
	assert.Equal(t, int64(500), first[0].Amount)
	assert.Equal(t, []int{0, 1, 2}, []int{first[0].Pos, first[1].Pos, first[2].Pos})
}

// 票不够时该档如实空缺,**绝不补抽**。
// 补抽等于用一个没被承诺的规则决定谁中奖。
func TestPickWinners_ShortRosterLeavesTiersEmpty(t *testing.T) {
	roster := []RosterLine{{EntryNo: "LE-a", UserRef: "u1", Amount: 1}}
	tiers := []Tier{{Tier: 1, Count: 3, Amount: 500}}
	final := FinalSeed("ACT2", "abcdef", "rh", 1, AlgoV1)

	winners := PickWinners(final, "ACT2", roster, tiers, false)
	assert.Len(t, winners, 1)
}

// 名单里多一条(哪怕是最后一秒加入的),全部票面都必须重排。
//
// 这是 final_seed 绑定 roster_hash 的全部意义:知道种子的人无法在封盘前
// 锁定任何结果,除非他能保证自己是最后一个报名的人。
func TestFinalSeed_AnyRosterChangeReshufflesEveryTicket(t *testing.T) {
	base := []RosterLine{
		{EntryNo: "LE-a", UserRef: "u1", Amount: 1},
		{EntryNo: "LE-b", UserRef: "u2", Amount: 1},
	}
	grown := append(append([]RosterLine{}, base...), RosterLine{EntryNo: "LE-c", UserRef: "u3", Amount: 1})

	h1, n1 := RosterHash("ACT", "commit", base)
	h2, n2 := RosterHash("ACT", "commit", grown)
	require.NotEqual(t, h1, h2)
	require.NotEqual(t, n1, n2)

	f1 := FinalSeed("ACT", "seedhex", h1, n1, AlgoV1)
	f2 := FinalSeed("ACT", "seedhex", h2, n2, AlgoV1)
	require.NotEqual(t, f1, f2)

	// 同一张票在两份名单下的票面必须不同 —— 否则先报名的人可以提前锁定名次。
	assert.NotEqual(t, Ticket(f1, "ACT", "LE-a"), Ticket(f2, "ACT", "LE-a"))
}
