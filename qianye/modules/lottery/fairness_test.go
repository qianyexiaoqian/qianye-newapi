package lottery

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// fairness_test.go —— 公正性协议 lot-v1 的契约。
//
// 这里断言的每一条都是**对外承诺**:承诺覆盖面、名单可复算、奖池守恒。
// 它们不是实现细节 —— 任何一条变红都意味着已经发布出去的验证脚本会算出
// 不同的结果,而那正是这套系统唯一不能出的事。

func baseActivity() *Activity {
	return &Activity{
		ActNo:            "LT20260101-0123456789abcdef",
		Kind:             KindDraw,
		Algo:             AlgoV1,
		RulesHash:        "rh",
		SpecHash:         "sh",
		StakeQuota:       1000,
		OpenAt:           1000,
		CloseAt:          2000,
		DrawAt:           3000,
		SettleDeadline:   4000,
		AllowMultiWin:    false,
		FeeBps:           500,
		MinEntriesToHold: 3,
	}
}

// 承诺必须覆盖每一个会影响结果的量。漏掉任何一项,管理员都能在不碰种子的
// 前提下算出想要的结果,而验证者只会看到"对不上"却举证不出是哪一边改的。
func TestCommitHashCoversEveryPromisedField(t *testing.T) {
	const seed = "00112233445566778899aabbccddeeff00112233445566778899aabbccddeeff"
	base := CommitHash(baseActivity(), seed)

	cases := []struct {
		name   string
		mutate func(*Activity)
	}{
		{"活动号", func(a *Activity) { a.ActNo = "LT20260101-ffffffffffffffff" }},
		{"类型", func(a *Activity) { a.Kind = KindGuess }},
		{"算法版本", func(a *Activity) { a.Algo = "lot-v2" }},
		{"参与条件", func(a *Activity) { a.RulesHash = "rh2" }},
		{"奖档或选项", func(a *Activity) { a.SpecHash = "sh2" }},
		{"参与费", func(a *Activity) { a.StakeQuota = 1001 }},
		{"开始时刻", func(a *Activity) { a.OpenAt = 1001 }},
		{"截止时刻", func(a *Activity) { a.CloseAt = 2001 }},
		{"开奖时刻", func(a *Activity) { a.DrawAt = 3001 }},
		{"结算截止", func(a *Activity) { a.SettleDeadline = 4001 }},
		{"是否允许一人多中", func(a *Activity) { a.AllowMultiWin = true }},
		{"手续费", func(a *Activity) { a.FeeBps = 501 }},
		{"最低成场人数", func(a *Activity) { a.MinEntriesToHold = 4 }},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			a := baseActivity()
			tc.mutate(a)
			assert.NotEqualf(t, base, CommitHash(a, seed),
				"改动「%s」没有改变承诺哈希 —— 管理员可以在不碰种子的前提下改掉它而不被发现", tc.name)
		})
	}

	t.Run("随机源", func(t *testing.T) {
		other := "ffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffff"
		assert.NotEqual(t, base, CommitHash(baseActivity(), other))
	})
}

// 承诺必须把"全部猜错怎么办"钉死。没有事前公示,事后无论怎么处理都会被指控
// 临时改规则 —— 所以它是原像的一部分,而不是一个可配置项。
func TestCommitHashPinsNoWinnerPolicy(t *testing.T) {
	assert.Equal(t, "refund_all", NoWinnerPolicy,
		"全部猜错的口径写死为全额退回。改它等于让平台在没人猜中时有收益,"+
			"而那会给平台设置「不可能达成的选项」的动机 —— 这个漏洞任何审计都补不上")
}

func line(entryNo, userRef string, optNo int, amount int64) RosterLine {
	return RosterLine{EntryNo: entryNo, UserRef: userRef, OptNo: optNo, Amount: amount}
}

// 哈希链让每个参与者在报名成功那一刻就持有自己那一环:
// 事后插入/删除/改动任何一条,该条之后所有人手里的值全部对不上。
func TestChainDetectsAnyTamperedEntry(t *testing.T) {
	const actNo = "LT20260101-0123456789abcdef"
	commit := "c0"

	build := func(rows []RosterLine) []string {
		out := make([]string, 0, len(rows))
		prev := commit
		for i, r := range rows {
			prev = ChainNext(prev, actNo, i+1, r.EntryNo, r.UserRef, r.OptNo, r.Amount)
			out = append(out, prev)
		}
		return out
	}

	rows := []RosterLine{
		line("LE-a", "ref-a", 0, 100),
		line("LE-b", "ref-b", 0, 100),
		line("LE-c", "ref-c", 0, 100),
	}
	original := build(rows)

	t.Run("改金额", func(t *testing.T) {
		tampered := append([]RosterLine(nil), rows...)
		tampered[1].Amount = 200
		got := build(tampered)
		assert.Equal(t, original[0], got[0], "被改条目之前的环不应受影响")
		assert.NotEqual(t, original[1], got[1])
		assert.NotEqual(t, original[2], got[2], "被改条目之后的每一环都必须失配")
	})

	t.Run("删条目", func(t *testing.T) {
		got := build([]RosterLine{rows[0], rows[2]})
		assert.NotEqual(t, original[1], got[1])
	})

	t.Run("插条目", func(t *testing.T) {
		got := build([]RosterLine{rows[0], line("LE-x", "ref-x", 0, 100), rows[1], rows[2]})
		assert.NotEqual(t, original[1], got[1])
	})
}

// 名单哈希把"到底哪些票有效"钉死在揭示种子之前。它与哈希链防的是不同的攻击:
// 链不含 status(否则每次扣费失败就破链),而抽签只用 success 的集合。
func TestRosterHashDependsOnTheExactSet(t *testing.T) {
	const actNo, commit = "LT-1", "c0"
	rows := []RosterLine{line("LE-a", "ref-a", 1, 10), line("LE-b", "ref-b", 2, 20)}

	h, n := RosterHash(actNo, commit, rows)
	require.Equal(t, 2, n)

	h2, _ := RosterHash(actNo, commit, []RosterLine{rows[0]})
	assert.NotEqual(t, h, h2, "少一条票必须算出不同的名单哈希")

	swapped := []RosterLine{rows[1], rows[0]}
	h3, _ := RosterHash(actNo, commit, swapped)
	assert.NotEqual(t, h, h3, "名单哈希依赖顺序,调用方必须按 entry_no 字节序升序传入")

	// 随机源必须绑定名单:任何后来者的加入都会重排全部票面,
	// 知道种子的人因此无法在封盘前锁定自己的名次。
	f1 := FinalSeed(actNo, "seed", h, n, AlgoV1)
	f2 := FinalSeed(actNo, "seed", h2, 1, AlgoV1)
	assert.NotEqual(t, f1, f2)
}

func TestPickWinnersIsDeterministicAndSlicesByTier(t *testing.T) {
	const actNo = "LT-1"
	final := FinalSeed(actNo, "seed", "roster", 5, AlgoV1)
	roster := []RosterLine{
		line("LE-1", "ref-1", 0, 100),
		line("LE-2", "ref-2", 0, 100),
		line("LE-3", "ref-3", 0, 100),
		line("LE-4", "ref-4", 0, 100),
		line("LE-5", "ref-5", 0, 100),
	}
	tiers := []Tier{{Tier: 2, Count: 2, Amount: 50}, {Tier: 1, Count: 1, Amount: 500}}

	got := PickWinners(final, actNo, roster, tiers, false)
	require.Len(t, got, 3)

	// 档位必须按 tier 升序切片,而不是按调用方传进来的顺序。
	assert.Equal(t, 1, got[0].Tier)
	assert.EqualValues(t, 500, got[0].Amount)
	assert.Equal(t, 2, got[1].Tier)
	assert.Equal(t, 2, got[2].Tier)
	for i, w := range got {
		assert.Equal(t, i, w.Pos, "draw_pos 必须是去重后序列里的 0 基下标")
	}

	// 可复现性本身就是公正性的一部分:同一份输入必须永远算出同一份名单。
	assert.Equal(t, got, PickWinners(final, actNo, roster, tiers, false))

	// 中奖者必须来自名单本身。
	inRoster := map[string]bool{}
	for _, r := range roster {
		inRoster[r.EntryNo] = true
	}
	seen := map[string]bool{}
	for _, w := range got {
		assert.True(t, inRoster[w.EntryNo])
		assert.False(t, seen[w.EntryNo], "同一张票不能中两次")
		seen[w.EntryNo] = true
	}
}

func TestPickWinnersLeavesTierShortWhenTicketsRunOut(t *testing.T) {
	const actNo = "LT-1"
	final := FinalSeed(actNo, "seed", "roster", 2, AlgoV1)
	roster := []RosterLine{line("LE-1", "ref-1", 0, 1), line("LE-2", "ref-2", 0, 1)}

	got := PickWinners(final, actNo, roster, []Tier{{Tier: 1, Count: 5, Amount: 10}}, false)
	assert.Len(t, got, 2, "票不够时该档如实空缺,绝不补抽 —— "+
		"补抽等于用一个没被承诺的规则决定谁中奖")
}

func TestPickWinnersHonoursAllowMultiWin(t *testing.T) {
	const actNo = "LT-1"
	final := FinalSeed(actNo, "seed", "roster", 4, AlgoV1)
	// 同一个人(同一个 user_ref)持有四张票。
	roster := []RosterLine{
		line("LE-1", "ref-same", 0, 1),
		line("LE-2", "ref-same", 0, 1),
		line("LE-3", "ref-same", 0, 1),
		line("LE-4", "ref-same", 0, 1),
	}
	tiers := []Tier{{Tier: 1, Count: 3, Amount: 10}}

	assert.Len(t, PickWinners(final, actNo, roster, tiers, false), 1,
		"allow_multi_win=false 时同一个 user_ref 只保留首次出现")
	assert.Len(t, PickWinners(final, actNo, roster, tiers, true), 3,
		"allow_multi_win=true 时每张票各自参与")
}

func TestSplitPoolConservesThePool(t *testing.T) {
	cases := []struct {
		name    string
		pool    int64
		feeBps  int
		all     []RosterLine
		winners []RosterLine
		wantFee int64
	}{
		{
			name: "三人均分一百且无手续费",
			pool: 100, feeBps: 0,
			all:     []RosterLine{line("a", "ra", 1, 40), line("b", "rb", 1, 30), line("c", "rc", 2, 30)},
			winners: []RosterLine{line("a", "ra", 1, 40), line("b", "rb", 1, 30)},
			wantFee: 0,
		},
		{
			name: "七人分一百万且收百分之五",
			pool: 1000000, feeBps: 500,
			all: []RosterLine{
				line("a", "ra", 1, 100000), line("b", "rb", 1, 100000), line("c", "rc", 1, 100000),
				line("d", "rd", 1, 100000), line("e", "re", 1, 100000), line("f", "rf", 1, 100000),
				line("g", "rg", 1, 100000), line("z", "rz", 2, 300000),
			},
			winners: []RosterLine{
				line("a", "ra", 1, 100000), line("b", "rb", 1, 100000), line("c", "rc", 1, 100000),
				line("d", "rd", 1, 100000), line("e", "re", 1, 100000), line("f", "rf", 1, 100000),
				line("g", "rg", 1, 100000),
			},
			wantFee: 50000,
		},
		{
			name: "四单位分给三个赢家(必然产生截断残差)",
			pool: 4, feeBps: 0,
			all: []RosterLine{
				line("a", "ra", 1, 1), line("b", "rb", 1, 1),
				line("c", "rc", 1, 1), line("d", "rd", 2, 1),
			},
			winners: []RosterLine{line("a", "ra", 1, 1), line("b", "rb", 1, 1), line("c", "rc", 1, 1)},
			wantFee: 0,
		},
		{
			name: "单人独中全场",
			pool: 300, feeBps: 1000,
			all:     []RosterLine{line("a", "ra", 1, 100), line("b", "rb", 2, 200)},
			winners: []RosterLine{line("a", "ra", 1, 100)},
			wantFee: 30,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			fee, shares, err := SplitPool(tc.pool, tc.feeBps, tc.all, tc.winners)
			require.NoError(t, err)
			assert.Equal(t, tc.wantFee, fee)

			var sum int64
			for _, s := range shares {
				assert.GreaterOrEqual(t, s.Amount, int64(0), "任何一笔份额都不得为负")
				assert.False(t, s.Refund, "有赢家时不应产生退款")
				sum += s.Amount
			}
			// 守恒式必须**精确**成立:少一分是黑掉用户的钱,多一分是平台倒贴。
			assert.Equal(t, tc.pool, sum+fee,
				"Σ份额 + 手续费必须精确等于奖池,否则这场结算根本不该被发出去")
			assert.Len(t, shares, len(tc.winners))
		})
	}
}

// 全部猜错 / 无输家 / 无对手盘,一律全额退回本金且手续费一分不收。
//
// 手续费的对价是"平台撮合了一次真实发生的再分配"。没有再分配发生时收费没有
// 对价;更要紧的是激励层面 —— 只要平台在没人猜中时有收益,它就有动机去设置
// 不可能达成的选项,而竞猜的结果本来就是管理员手工指定的。
func TestSplitPoolRefundsEveryoneWhenThereIsNoRedistribution(t *testing.T) {
	all := []RosterLine{line("a", "ra", 1, 100), line("b", "rb", 1, 200), line("c", "rc", 1, 300)}

	t.Run("全部猜错", func(t *testing.T) {
		fee, shares, err := SplitPool(600, 2000, all, nil)
		require.NoError(t, err)
		assert.Zero(t, fee, "没有再分配发生,手续费一分不收")
		require.Len(t, shares, 3)
		for i, s := range shares {
			assert.True(t, s.Refund)
			assert.Equal(t, all[i].Amount, s.Amount, "退的必须是本金原额")
		}
	})

	t.Run("无输家", func(t *testing.T) {
		fee, shares, err := SplitPool(600, 2000, all, all)
		require.NoError(t, err)
		assert.Zero(t, fee)
		require.Len(t, shares, 3)
		var sum int64
		for _, s := range shares {
			assert.True(t, s.Refund)
			sum += s.Amount
		}
		assert.EqualValues(t, 600, sum,
			"所有人都押对却因手续费人人亏钱,是最糟的体验且同样没有对价")
	})
}

// user_ref 必须是每活动加盐的:盐不同就必须算出不同的标识,
// 否则同一个人在两场活动里可被关联,而那正是脱敏要防的事。
func TestUserRefIsSaltedPerActivity(t *testing.T) {
	a := UserRef("salt-a", 42)
	b := UserRef("salt-b", 42)
	assert.Len(t, a, 32)
	assert.NotEqual(t, a, b)
	assert.Equal(t, a, UserRef("salt-a", 42), "同一活动内同一个人必须稳定")
	assert.NotEqual(t, a, UserRef("salt-a", 43))
}
