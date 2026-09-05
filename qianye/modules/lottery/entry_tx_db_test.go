package lottery

import (
	"context"
	"testing"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/qianye/config"
	"github.com/QuantumNous/new-api/qianye/modules/stardust"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

// entry_tx_db_test.go —— "任何一步失败,钱都不会丢、也不会多扣"。
//
// 一次参与是一个扩展库事务:reserveEntry(活动行锁 + 序号 + 锁内闸门 + 链环)→
// stardust.Debit(余额行锁 + 条件扣减 + 幂等流水)→ 落票。这里锁住的不变量:
//
//  1. 闸门在锁内,失败**一个字节都不留**:序号不占、计数不动、流水不留。
//  2. 星屑不足整笔回滚:票不落库、seq 不占 —— 失败的尝试从此不在链上留痕。
//  3. 成交的票与它的扣款流水互相指认:order_no = ledger_no,余额前后值取自收据。
//  4. 链在事务里逐条推进,第一条挂在 commit_hash 上。
//  5. 出款计划的唯一键与 0 元跳过;奖池分配与抽取的纯函数守恒、可复现。

// 时间窗之外的报名必须整体回滚 —— 连 seq 都不能消耗。
//
// reserveEntry 的第一条 UPDATE 同时是闸门与序号分配,闸门不中就没有任何写入落地。
func TestReserveEntry_ClosedWindowLeavesNoTrace(t *testing.T) {
	gdb := newFundTestDB(t)
	now := common.GetTimestamp()
	act := seedActivity(t, gdb, func(a *Activity) { a.CloseAt = now - 1 })

	e := &Entry{EntryNo: newEntryNo(), ActId: act.Id, UserId: 7, Amount: 1000, Status: EntrySuccess}
	err := gdb.Transaction(func(tx *gorm.DB) error {
		_, err := reserveEntry(tx, act, Rules{}, e, 0)
		return err
	})
	require.ErrorIs(t, err, errClosingSoon)

	after := loadAct(t, gdb, act.Id)
	assert.Equal(t, 0, after.EntrySeq, "闸门不中时绝不能消耗序号,否则链上会出现无主的空洞")
	assert.Equal(t, 0, after.ActiveCount)
	assert.Zero(t, after.PoolQuota)

	var n int64
	require.NoError(t, gdb.Model(&Entry{}).Count(&n).Error)
	assert.Zero(t, n)
}

// 全场名额是在活动行 X 锁下判定的,超出即整体回滚。
func TestReserveEntry_TotalEntryCapRejectsAndRollsBack(t *testing.T) {
	gdb := newFundTestDB(t)
	act := seedActivity(t, gdb, func(a *Activity) { a.MaxTotalEntries = 2 })
	seedSuccessEntry(t, gdb, act, 1, 1000)
	seedSuccessEntry(t, gdb, act, 2, 1000)

	third := &Entry{EntryNo: newEntryNo(), ActId: act.Id, UserId: 3, Amount: 1000, Status: EntrySuccess}
	cur := loadAct(t, gdb, act.Id)
	err := gdb.Transaction(func(tx *gorm.DB) error {
		_, err := reserveEntry(tx, cur, Rules{}, third, 0)
		return err
	})
	require.ErrorIs(t, err, errCapReached)

	after := loadAct(t, gdb, act.Id)
	assert.Equal(t, 2, after.EntrySeq, "被拒的那次不留序号")
	assert.Equal(t, 2, after.ActiveCount)
	assert.EqualValues(t, 2000, after.PoolQuota, "被拒的那次不许计入奖池")
}

// 星屑不足:整笔回滚,票不落库、seq 不占、账本无残行、计数与链头一个都不动。
//
// 吞掉 ErrInsufficient 继续提交的后果写在 stardust/doc.go:那一行幂等流水会残留,
// 用户下一次重试被判成"重放"而余额一分没动 —— 一笔既没扣成也永远扣不成的账。
func TestSettleEntryTx_InsufficientStardustRollsBackEverything(t *testing.T) {
	gdb := newFundTestDB(t)
	act := seedActivity(t, gdb, nil)
	first := seedSuccessEntry(t, gdb, act, 1, 1000)
	seedStardust(t, gdb, 2, 999)

	poor := &Entry{
		EntryNo: newEntryNo(), ActId: act.Id, IdemKey: buildIdemKey(act.ActNo, "poor-1"),
		UserId: 2, UserRef: UserRef("salt", 2), Amount: 1000, Status: EntrySuccess,
		CreatedAt: common.GetTimestamp(),
	}
	cur := loadAct(t, gdb, act.Id)
	err := gdb.Transaction(func(tx *gorm.DB) error {
		return settleEntryTx(tx, cur, Rules{}, poor, 0)
	})
	require.ErrorIs(t, err, stardust.ErrInsufficient)

	after := loadAct(t, gdb, act.Id)
	assert.Equal(t, 1, after.EntrySeq, "失败的尝试不占序号:单事务之下链上每一个 seq 都对应一张真扣了钱的票")
	assert.Equal(t, 1, after.ActiveCount)
	assert.EqualValues(t, 1000, after.PoolQuota)
	assert.Equal(t, first.ChainHash, after.ChainHead, "链头必须停在上一张成交票上")
	assert.EqualValues(t, 999, stardustOf(t, gdb, 2), "余额一分不动")

	var tickets int64
	require.NoError(t, gdb.Model(&Entry{}).Where("act_id = ?", act.Id).Count(&tickets).Error)
	assert.EqualValues(t, 1, tickets, "失败的尝试不落票")
	var stakes int64
	require.NoError(t, gdb.Model(&stardust.Ledger{}).
		Where("user_id = ? AND kind = ?", 2, string(stardust.KindLotStake)).Count(&stakes).Error)
	assert.Zero(t, stakes, "回滚之后账本上不许留下幂等残行 —— 留下就是下一次重试被判成重放")

	// 补足之后同一个幂等键再来:必须成交,而且就是这一次的票(不是重放)。
	seedStardust(t, gdb, 2, 1)
	retry := &Entry{
		EntryNo: newEntryNo(), ActId: act.Id, IdemKey: poor.IdemKey,
		UserId: 2, UserRef: UserRef("salt", 2), Amount: 1000, Status: EntrySuccess,
		CreatedAt: common.GetTimestamp(),
	}
	curRetry := loadAct(t, gdb, act.Id)
	require.NoError(t, gdb.Transaction(func(tx *gorm.DB) error {
		return settleEntryTx(tx, curRetry, Rules{}, retry, 0)
	}))
	assert.Equal(t, 2, retry.Seq)
	assert.Zero(t, stardustOf(t, gdb, 2))
}

// 成交的票与它的扣款流水互相指认。
func TestSettleEntryTx_TicketAndLedgerRowPointAtEachOther(t *testing.T) {
	gdb := newFundTestDB(t)
	act := seedActivity(t, gdb, nil)
	seedStardust(t, gdb, 5, 2500)
	e := seedSuccessEntry(t, gdb, act, 5, 1000)

	rows := ledgerRowsOf(t, gdb, 5, act.ActNo)
	require.Len(t, rows, 1, "一张票恰好一行扣款流水")
	row := rows[0]
	assert.Equal(t, string(stardust.KindLotStake), row.Kind)
	assert.EqualValues(t, -1000, row.Amount, "扣款行的 amount 带符号")
	assert.Equal(t, refTypeEntry, row.RefType)
	assert.Equal(t, e.EntryNo, row.RefNo, "流水指向票号")
	assert.Equal(t, idemScopeStake, row.IdemScope)
	assert.Equal(t, row.LedgerNo, e.OrderNo, "票指向流水号 —— 退款金额的权威锚点")
	assert.EqualValues(t, 3500, e.QuotaBefore, "余额前后值取自扣款收据")
	assert.EqualValues(t, 2500, e.QuotaAfter)
	assert.EqualValues(t, 2500, stardustOf(t, gdb, 5))

	var stored Entry
	require.NoError(t, gdb.Where("entry_no = ?", e.EntryNo).Take(&stored).Error)
	assert.Equal(t, EntrySuccess, stored.Status)
	assert.Equal(t, e.OrderNo, stored.OrderNo)
	assert.Equal(t, e.Fingerprint, stored.Fingerprint)
	assert.Equal(t, e.ChainHash, loadAct(t, gdb, act.Id).ChainHead)
}

// 链在事务里逐条推进,且第一条挂在 commit_hash 上。
func TestSettleEntryTx_ChainStartsAtCommitAndAdvances(t *testing.T) {
	gdb := newFundTestDB(t)
	act := seedActivity(t, gdb, nil)

	first := seedSuccessEntry(t, gdb, act, 1, 1000)
	assert.Equal(t, act.CommitHash, first.PrevHash, "chain_0 必须是 commit_hash")
	assert.Equal(t, 1, first.Seq)

	second := seedSuccessEntry(t, gdb, act, 2, 1000)
	assert.Equal(t, first.ChainHash, second.PrevHash)
	assert.Equal(t, 2, second.Seq)
	assert.Equal(t, second.ChainHash, loadAct(t, gdb, act.Id).ChainHead)
	assert.NotEqual(t, first.ChainHash, second.ChainHash)
}

// 竞猜的选项聚合与票同一个事务:票落库,选项上的投注额与注数才跟着涨。
func TestSettleEntryTx_GuessAggregatesOptionInTheSameTransaction(t *testing.T) {
	gdb := newFundTestDB(t)
	act := seedActivity(t, gdb, func(a *Activity) { a.Kind = KindGuess })
	require.NoError(t, gdb.Create(&Option{ActId: act.Id, OptNo: 1, Label: "甲"}).Error)
	require.NoError(t, gdb.Create(&Option{ActId: act.Id, OptNo: 2, Label: "乙"}).Error)
	seedStardust(t, gdb, 4, 3000)

	e := &Entry{
		EntryNo: newEntryNo(), ActId: act.Id, IdemKey: buildIdemKey(act.ActNo, "g-1"),
		UserId: 4, OptNo: 1, Amount: 3000, Status: EntrySuccess, UserRef: UserRef("s", 4),
		CreatedAt: common.GetTimestamp(),
	}
	cur := loadAct(t, gdb, act.Id)
	require.NoError(t, gdb.Transaction(func(tx *gorm.DB) error {
		return settleEntryTx(tx, cur, Rules{}, e, 0)
	}))

	var opt Option
	require.NoError(t, gdb.Where("act_id = ? AND opt_no = ?", act.Id, 1).Take(&opt).Error)
	assert.EqualValues(t, 3000, opt.BetQuota)
	assert.Equal(t, 1, opt.BetCount)

	// 不存在的选项:整笔回滚 —— 包括已经扣掉的星屑。
	bad := &Entry{
		EntryNo: newEntryNo(), ActId: act.Id, IdemKey: buildIdemKey(act.ActNo, "g-2"),
		UserId: 4, OptNo: 9, Amount: 1, Status: EntrySuccess, UserRef: UserRef("s", 4),
		CreatedAt: common.GetTimestamp(),
	}
	seedStardust(t, gdb, 4, 1)
	curBad := loadAct(t, gdb, act.Id)
	err := gdb.Transaction(func(tx *gorm.DB) error {
		return settleEntryTx(tx, curBad, Rules{}, bad, 0)
	})
	require.ErrorIs(t, err, errBadOption)
	assert.EqualValues(t, 1, stardustOf(t, gdb, 4), "选项不存在时扣掉的星屑必须随事务回滚")
	assert.Equal(t, 1, loadAct(t, gdb, act.Id).EntrySeq)
}

// ─────────────────── 出款计划:不重复,0 元跳过 ───────────────────

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

// 0 元计划不落库:stardust.Credit 的入口要求 amount > 0,而 0 星屑的出款在账面上
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

// ─────────────────── 奖池分配:精确守恒 ───────────────────

// SplitPool 的守恒式必须逐个用例精确成立,并且残差恰好归最后一名赢家。
//
// 各自四舍五入会让前 n-1 笔之和超过 net,最后一笔变成负数;而负额出款在
// stardust.Credit 入口会被拒,整场结算就此卡死。这组用例锁的正是那条边界。
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

// 手续费不产生任何流水,因此它只体现为"少发出去的那一部分"。
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

// ─────────────────── 抽取算法:确定且可复现 ───────────────────

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

// 封盘 → 取消 → 退款计划 → 出款:Σ余额守恒,每个人拿回自己那一注。
//
// 这是"流局全退"在账本上的形状:参与费经 lot_stake 扣走、经 lot_refund 退回,
// 每个人的余额回到起点,账本上一负一正两行,活动行上 refund_quota 等于退回的总额。
func TestFullRefund_ConservesEveryBalance(t *testing.T) {
	gdb := newPayoutEnv(t, newPayoutTestLottery())
	act := seedActivity(t, gdb, nil)
	users := []int{31, 32, 33}
	for _, u := range users {
		seedSuccessEntry(t, gdb, act, u, 1000)
		require.Zero(t, stardustOf(t, gdb, u), "夹具:参与费已经扣走")
	}

	require.NoError(t, lockActivity(context.Background(), gdb, loadAct(t, gdb, act.Id)))
	require.NoError(t, gdb.Model(&Activity{}).Where("id = ?", act.Id).
		Updates(map[string]any{"status": StatusSettling, "outcome": OutcomeCancelled}).Error)

	runSettle(context.Background())
	for i := 0; i < 5; i++ {
		DrivePayouts(context.Background())
	}
	runSettle(context.Background())

	final := loadAct(t, gdb, act.Id)
	assert.Equal(t, StatusFinished, final.Status, "退款全部到账之后活动必须收尾")
	assert.EqualValues(t, 3000, final.RefundQuota)
	for _, u := range users {
		assert.EqualValuesf(t, 1000, stardustOf(t, gdb, u), "用户 %d 必须拿回自己那一注", u)
		rows := ledgerRowsOf(t, gdb, u, act.ActNo)
		require.Lenf(t, rows, 2, "用户 %d 在这一场上恰好一笔扣款、一笔退款", u)
		assert.Equal(t, string(stardust.KindLotStake), rows[0].Kind)
		assert.Equal(t, string(stardust.KindLotRefund), rows[1].Kind)
		assert.EqualValues(t, 0, rows[0].Amount+rows[1].Amount, "一扣一退必须相抵")
	}
}

// newPayoutTestLottery 是跑得动封盘 / 收尾 / 出款三条后台路径的最小配置。
func newPayoutTestLottery() config.Lottery {
	return config.Lottery{Enabled: true, PayoutMaxAttempts: 8, MaxStakeStardust: 5_000_000}
}
