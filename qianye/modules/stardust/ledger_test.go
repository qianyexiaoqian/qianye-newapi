package stardust

import (
	"errors"
	"math/rand"
	"strconv"
	"strings"
	"testing"

	"github.com/QuantumNous/new-api/common"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

// ledger_test.go —— 写账入口的契约(design §3.3)。
//
// 每一条都对应一个"把实现改坏之后不会有任何症状"的缺陷形状:幂等重放多扣一次、
// 不足时留下一行幂等残行、kind 落错列让 I0 悄悄失效。

func TestCreditFirstPostingMovesBalanceAndItsColumn(t *testing.T) {
	gdb := newTestDB(t)

	res, err := Credit(gdb, Posting{UserId: 7, Kind: KindLotPrize, Amount: 120,
		IdemScope: "lotpay", IdemKey: "LP-1", RefType: "lot_payout", RefNo: "LP-1", ActNo: "LT-1"})
	require.NoError(t, err)
	assert.True(t, res.Inserted)
	assert.EqualValues(t, 120, res.BalanceAfter)
	assert.NotZero(t, res.LedgerId)
	assert.True(t, strings.HasPrefix(res.LedgerNo, "SD"))

	bal := balanceOf(t, gdb, 7)
	require.NotNil(t, bal)
	assert.EqualValues(t, 120, bal.Available)
	assert.EqualValues(t, 120, bal.TotalEarned)
	assert.Zero(t, bal.TotalSpent)
	assert.Zero(t, bal.TotalRefunded)
	assert.Zero(t, bal.TotalAdjusted)

	rows := ledgerOf(t, gdb, 7)
	require.Len(t, rows, 1)
	assert.Equal(t, res.LedgerId, rows[0].Id)
	assert.Equal(t, string(KindLotPrize), rows[0].Kind)
	assert.EqualValues(t, 120, rows[0].Amount, "入账行的金额为正")
	assert.EqualValues(t, 120, rows[0].BalanceAfter)
	assert.Equal(t, "LT-1", rows[0].ActNo)
	assert.NotZero(t, rows[0].CreatedAt)
}

func TestSameIdemKeyReplaysWithoutTouchingBalance(t *testing.T) {
	gdb := newTestDB(t)
	first, err := Credit(gdb, Posting{UserId: 7, Kind: KindInviteTopup, Amount: 50, IdemScope: "topup", IdemKey: "T-1"})
	require.NoError(t, err)
	require.True(t, first.Inserted)

	// 重放时连金额都不同:幂等按键判,不按内容 —— 内容是否一致由调用方拿 LedgerId 读回比对。
	again, err := Credit(gdb, Posting{UserId: 7, Kind: KindInviteTopup, Amount: 999, IdemScope: "topup", IdemKey: "T-1"})
	require.NoError(t, err)
	assert.False(t, again.Inserted)
	assert.Equal(t, first.LedgerId, again.LedgerId)
	assert.Equal(t, first.LedgerNo, again.LedgerNo)
	assert.Equal(t, first.BalanceAfter, again.BalanceAfter)

	bal := balanceOf(t, gdb, 7)
	require.NotNil(t, bal)
	assert.EqualValues(t, 50, bal.Available, "重放不得再动余额")
	assert.EqualValues(t, 50, bal.TotalEarned)
	assert.Len(t, ledgerOf(t, gdb, 7), 1, "重放不得再落流水")

	// 同一个键换了方向同样是重放:Debit 不会因为键已存在就把钱扣掉。
	deb, err := Debit(gdb, Posting{UserId: 7, Kind: KindManual, Amount: 10, IdemScope: "topup", IdemKey: "T-1"})
	require.NoError(t, err)
	assert.False(t, deb.Inserted)
	assert.EqualValues(t, 50, balanceOf(t, gdb, 7).Available)
}

func TestDebitBeyondAvailableFailsBeforeAnyWrite(t *testing.T) {
	gdb := newTestDB(t)
	_, err := Credit(gdb, Posting{UserId: 9, Kind: KindLotPrize, Amount: 50, IdemScope: "lotpay", IdemKey: "LP-9"})
	require.NoError(t, err)

	// 在事务里跑并按契约回滚:不足时第 3 步就返回,第 4 步的幂等行根本没插。
	txErr := gdb.Transaction(func(tx *gorm.DB) error {
		_, err := Debit(tx, Posting{UserId: 9, Kind: KindLotStake, Amount: 80, IdemScope: "lot_stake", IdemKey: "E-9"})
		return err
	})
	require.ErrorIs(t, txErr, ErrInsufficient)

	rows := ledgerOf(t, gdb, 9)
	require.Len(t, rows, 1, "不足时不得留下任何流水行,否则重试会被判成重放")
	assert.Equal(t, "LP-9", rows[0].IdemKey)
	assert.EqualValues(t, 50, balanceOf(t, gdb, 9).Available)

	// 没有余额行的人扣减同样是不足,而不是报错或建出负数。
	_, err = Debit(gdb, Posting{UserId: 10, Kind: KindMallOrder, Amount: 1, IdemScope: "mall", IdemKey: "M-10"})
	require.ErrorIs(t, err, ErrInsufficient)
	assert.Empty(t, ledgerOf(t, gdb, 10))
	if bal := balanceOf(t, gdb, 10); bal != nil {
		assert.Zero(t, bal.Available)
	}
}

func TestCreditPastMaxQuotaFailsBeforeAnyWrite(t *testing.T) {
	gdb := newTestDB(t)
	limit := int64(common.MaxQuota)

	// 恰好到上界是允许的:上界是闭区间。
	res, err := Credit(gdb, Posting{UserId: 11, Kind: KindManual, Amount: limit, IdemScope: "manual", IdemKey: "cap"})
	require.NoError(t, err)
	assert.True(t, res.Inserted)
	assert.Equal(t, limit, balanceOf(t, gdb, 11).Available)

	_, err = Credit(gdb, Posting{UserId: 11, Kind: KindLotPrize, Amount: 1, IdemScope: "lotpay", IdemKey: "LP-11"})
	require.ErrorIs(t, err, ErrOverflow)
	assert.Len(t, ledgerOf(t, gdb, 11), 1, "超上界时不得留下流水行")
	assert.Equal(t, limit, balanceOf(t, gdb, 11).Available)

	// 单笔超过上界是参数错误,与账户状态无关。
	_, err = Credit(gdb, Posting{UserId: 12, Kind: KindLotPrize, Amount: limit + 1, IdemScope: "lotpay", IdemKey: "LP-12"})
	require.ErrorIs(t, err, ErrBadAmount)
	assert.Nil(t, balanceOf(t, gdb, 12), "参数校验在加锁之前,不得凭空建余额行")
}

func TestEveryKindLandsOnExactlyItsColumn(t *testing.T) {
	type columns struct{ earned, spent, refunded, adjusted int64 }
	cases := []struct {
		kind  Kind
		debit bool
		want  columns
	}{
		{KindConsumeRebate, false, columns{earned: 10}},
		{KindInviteTopup, false, columns{earned: 10}},
		{KindInviteRedeem, false, columns{earned: 10}},
		{KindInviteRegister, false, columns{earned: 10}},
		{KindPlanBuyer, false, columns{earned: 10}},
		{KindPlanInviter, false, columns{earned: 10}},
		{KindLotPrize, false, columns{earned: 10}},
		{KindLotRefund, false, columns{refunded: 10}},
		{KindMallRefund, false, columns{refunded: 10}},
		{KindLotStake, true, columns{spent: 10}},
		{KindMallOrder, true, columns{spent: 10}},
		{KindManual, false, columns{adjusted: 10}},
		{KindManual, true, columns{adjusted: -10}},
	}
	gdb := newTestDB(t)
	for i, tc := range cases {
		name := string(tc.kind)
		if tc.debit {
			name += "/debit"
		}
		t.Run(name, func(t *testing.T) {
			userId := 100 + i
			// 扣减类先垫 100 星屑,而且垫的那一笔走 lot_prize,不会污染被测的列。
			want := tc.want
			if tc.debit {
				_, err := Credit(gdb, Posting{UserId: userId, Kind: KindLotPrize, Amount: 100, IdemScope: "seed", IdemKey: "seed-" + strconv.Itoa(userId)})
				require.NoError(t, err)
				want.earned += 100
			}
			p := Posting{UserId: userId, Kind: tc.kind, Amount: 10, IdemScope: "t", IdemKey: "k-" + strconv.Itoa(userId)}
			var err error
			if tc.debit {
				_, err = Debit(gdb, p)
			} else {
				_, err = Credit(gdb, p)
			}
			require.NoError(t, err)

			bal := balanceOf(t, gdb, userId)
			require.NotNil(t, bal)
			assert.Equal(t, want, columns{bal.TotalEarned, bal.TotalSpent, bal.TotalRefunded, bal.TotalAdjusted})
			assert.Equal(t, bal.TotalEarned-bal.TotalSpent+bal.TotalRefunded+bal.TotalAdjusted, bal.Available, "I0")
		})
	}
}

func TestDirectionMismatchIsRejectedAsBadKind(t *testing.T) {
	gdb := newTestDB(t)
	_, err := Credit(gdb, Posting{UserId: 20, Kind: KindLotPrize, Amount: 100, IdemScope: "seed", IdemKey: "seed-20"})
	require.NoError(t, err)

	cases := []struct {
		name  string
		debit bool
		kind  Kind
	}{
		{"Credit 花掉类", false, KindLotStake},
		{"Credit 花掉类(商城)", false, KindMallOrder},
		{"Debit 获得类", true, KindLotPrize},
		{"Debit 退回类", true, KindLotRefund},
		{"未知 kind", false, Kind("bogus")},
		{"空 kind", true, Kind("")},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			p := Posting{UserId: 20, Kind: tc.kind, Amount: 1, IdemScope: "t", IdemKey: tc.name}
			var err error
			if tc.debit {
				_, err = Debit(gdb, p)
			} else {
				_, err = Credit(gdb, p)
			}
			require.ErrorIs(t, err, ErrBadKind)
		})
	}
	assert.Len(t, ledgerOf(t, gdb, 20), 1, "方向错误发生在写入之前")
	assert.EqualValues(t, 100, balanceOf(t, gdb, 20).Available)
}

func TestManualAdjustmentsAccumulateAsSignedNet(t *testing.T) {
	gdb := newTestDB(t)
	_, err := Credit(gdb, Posting{UserId: 30, Kind: KindManual, Amount: 100, IdemScope: "manual", IdemKey: "1:a", OperatorId: 1, Remark: "补发"})
	require.NoError(t, err)
	res, err := Debit(gdb, Posting{UserId: 30, Kind: KindManual, Amount: 30, IdemScope: "manual", IdemKey: "1:b", OperatorId: 1, Remark: "扣回"})
	require.NoError(t, err)
	assert.EqualValues(t, 70, res.BalanceAfter)

	bal := balanceOf(t, gdb, 30)
	require.NotNil(t, bal)
	assert.EqualValues(t, 70, bal.Available)
	assert.EqualValues(t, 70, bal.TotalAdjusted, "手调列是带符号净额")
	assert.Zero(t, bal.TotalEarned)
	assert.Zero(t, bal.TotalSpent)

	rows := ledgerOf(t, gdb, 30)
	require.Len(t, rows, 2)
	assert.EqualValues(t, -30, rows[1].Amount, "扣减行的金额为负")
	assert.EqualValues(t, 70, rows[1].BalanceAfter)
	assert.Equal(t, 1, rows[1].OperatorId)
	assert.Equal(t, "扣回", rows[1].Remark)

	// 手调扣减同样受 available ≥ 0 约束。
	_, err = Debit(gdb, Posting{UserId: 30, Kind: KindManual, Amount: 71, IdemScope: "manual", IdemKey: "1:c"})
	require.ErrorIs(t, err, ErrInsufficient)
	assert.Len(t, ledgerOf(t, gdb, 30), 2)
}

// TestInvariantsHoldAfterMixedPostings 用固定种子跑一串混合记账,再逐用户核对
// I0(列恒等式)与 I1(Σ流水 = 余额,且 balance_after 链式衔接)。
// 固定种子让失败可复现;它不是模糊测试,是"两条恒等式对任意合法序列都成立"的一个证据。
func TestInvariantsHoldAfterMixedPostings(t *testing.T) {
	gdb := newTestDB(t)
	rng := rand.New(rand.NewSource(20260904))
	credits := []Kind{KindConsumeRebate, KindInviteTopup, KindLotPrize, KindLotRefund, KindMallRefund, KindPlanBuyer}
	debits := []Kind{KindLotStake, KindMallOrder}
	users := []int{201, 202, 203}

	var insufficient, replayed int
	for i := 0; i < 240; i++ {
		userId := users[rng.Intn(len(users))]
		amount := int64(rng.Intn(400) + 1)
		// 每十笔重放一次前面的键,确认重放在混合序列里同样不动账。
		key := "k-" + strconv.Itoa(i)
		if i%10 == 9 {
			key = "k-" + strconv.Itoa(rng.Intn(i))
		}
		p := Posting{UserId: userId, Amount: amount, IdemScope: "mix", IdemKey: key}
		var res Result
		var err error
		switch rng.Intn(4) {
		case 0, 1:
			p.Kind = credits[rng.Intn(len(credits))]
			res, err = Credit(gdb, p)
		case 2:
			p.Kind = debits[rng.Intn(len(debits))]
			res, err = Debit(gdb, p)
		default:
			p.Kind = KindManual
			if rng.Intn(2) == 0 {
				res, err = Credit(gdb, p)
			} else {
				res, err = Debit(gdb, p)
			}
		}
		switch {
		case errors.Is(err, ErrInsufficient):
			insufficient++
		case err != nil:
			require.NoError(t, err)
		case !res.Inserted:
			replayed++
		}
	}
	require.Positive(t, insufficient, "样本里必须撞到过不足,否则条件扣减没被测到")
	require.Positive(t, replayed, "样本里必须撞到过重放")

	for _, userId := range users {
		bal := balanceOf(t, gdb, userId)
		require.NotNil(t, bal)
		assert.GreaterOrEqual(t, bal.Available, int64(0), "available 恒 ≥ 0")
		assert.Equal(t, bal.TotalEarned-bal.TotalSpent+bal.TotalRefunded+bal.TotalAdjusted, bal.Available, "I0 用户 %d", userId)

		var sum, running int64
		var earned, spent, refunded, adjusted int64
		for _, row := range ledgerOf(t, gdb, userId) {
			sum += row.Amount
			running += row.Amount
			assert.Equal(t, running, row.BalanceAfter, "balance_after 链式衔接,流水 %s", row.LedgerNo)
			col, err := kindColumn(Kind(row.Kind))
			require.NoError(t, err)
			switch col {
			case colTotalEarned:
				earned += row.Amount
			case colTotalSpent:
				spent += -row.Amount
			case colTotalRefunded:
				refunded += row.Amount
			case colTotalAdjusted:
				adjusted += row.Amount
			}
		}
		assert.Equal(t, sum, bal.Available, "I1 用户 %d", userId)
		assert.Equal(t, earned, bal.TotalEarned)
		assert.Equal(t, spent, bal.TotalSpent)
		assert.Equal(t, refunded, bal.TotalRefunded)
		assert.Equal(t, adjusted, bal.TotalAdjusted)
	}
}

func TestPostingParameterGuards(t *testing.T) {
	gdb := newTestDB(t)
	cases := []struct {
		name string
		p    Posting
		want error
	}{
		{"金额为 0", Posting{UserId: 1, Kind: KindLotPrize, Amount: 0, IdemScope: "s", IdemKey: "k"}, ErrBadAmount},
		{"金额为负", Posting{UserId: 1, Kind: KindLotPrize, Amount: -5, IdemScope: "s", IdemKey: "k"}, ErrBadAmount},
		{"缺 user_id", Posting{UserId: 0, Kind: KindLotPrize, Amount: 5, IdemScope: "s", IdemKey: "k"}, ErrBadPosting},
		{"缺 idem_scope", Posting{UserId: 1, Kind: KindLotPrize, Amount: 5, IdemKey: "k"}, ErrBadPosting},
		{"缺 idem_key", Posting{UserId: 1, Kind: KindLotPrize, Amount: 5, IdemScope: "s"}, ErrBadPosting},
		{"idem_scope 超宽", Posting{UserId: 1, Kind: KindLotPrize, Amount: 5, IdemScope: strings.Repeat("s", 33), IdemKey: "k"}, ErrBadPosting},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := Credit(gdb, tc.p)
			require.ErrorIs(t, err, tc.want)
		})
	}
	assert.Nil(t, balanceOf(t, gdb, 1), "参数校验在加锁之前,不得凭空建余额行")
	_, err := Credit(nil, Posting{UserId: 1, Kind: KindLotPrize, Amount: 5, IdemScope: "s", IdemKey: "k"})
	require.Error(t, err)
}

func TestOverlongIdemKeyIsFoldedInjectively(t *testing.T) {
	gdb := newTestDB(t)
	long := strings.Repeat("trade-no-", 30) // 270 字节,远超 varchar(96)
	first, err := Credit(gdb, Posting{UserId: 40, Kind: KindInviteTopup, Amount: 5, IdemScope: "topup", IdemKey: long + "A"})
	require.NoError(t, err)
	require.True(t, first.Inserted)

	again, err := Credit(gdb, Posting{UserId: 40, Kind: KindInviteTopup, Amount: 5, IdemScope: "topup", IdemKey: long + "A"})
	require.NoError(t, err)
	assert.False(t, again.Inserted, "同一个超长键必须命中同一行")

	other, err := Credit(gdb, Posting{UserId: 40, Kind: KindInviteTopup, Amount: 5, IdemScope: "topup", IdemKey: long + "B"})
	require.NoError(t, err)
	assert.True(t, other.Inserted, "只差最后一个字节的两个超长键不得撞成同一个幂等键 —— 截断会让第二笔永远记不上")

	for _, row := range ledgerOf(t, gdb, 40) {
		assert.LessOrEqual(t, len(row.IdemKey), 96)
		assert.True(t, strings.HasPrefix(row.IdemKey, "h:"))
	}
	assert.EqualValues(t, 10, balanceOf(t, gdb, 40).Available)
}

func TestLedgerNoIsUniqueAndFitsColumn(t *testing.T) {
	seen := make(map[string]bool, 5000)
	for i := 0; i < 5000; i++ {
		no := NewLedgerNo()
		assert.True(t, strings.HasPrefix(no, "SD"), no)
		assert.LessOrEqual(t, len(no), 32, "必须装得进 varchar(32):%s", no)
		require.False(t, seen[no], "流水号重复: %s", no)
		seen[no] = true
	}
}
