package lottery

import (
	"crypto/sha256"
	"encoding/hex"
	"math/big"
	"sort"
	"strconv"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// fairness_v2_test.go —— 公正性协议 lot-v2 的契约:概率制与文本奖。
//
// 这里断言的每一条都是**对外承诺**。它们不是实现细节 ——
// 任何一条变红都意味着已经发布出去的验证脚本会算出不同的结果。
//
// # v1 黄金向量为什么放在这里
//
// lot-v2 的四个原像是抄着 v1 改出来的。改的时候最容易做的一件事,就是"顺手"
// 把 v1 的那份也一起改了 —— 而那会让**所有已开完活动**的历史公正查询集体
// 变成 FAIL。所以 v1 的四个原像在这里被钉成写死的十六进制串:
// 它们与任何实现都无关,只与协议本身有关。

// ─────────────────────────── v1 冻结 ───────────────────────────

// v1 的四个原像已经发布出去了,任何一个字节的改动都是对已有用户的背信。
func TestV1PreimagesAreFrozen(t *testing.T) {
	t.Run("prize_spec_line", func(t *testing.T) {
		assert.Equal(t, "1\x1f头奖\x1f5000\x1f1", prizeSpecLineV1(1, "头奖", 5000, 1))
	})

	t.Run("spec_hash", func(t *testing.T) {
		assert.Equal(t,
			"6b06d2652e3c5da00a11215aadac133a54b0822a37edfcbdda2c166039bb000d",
			SpecHash([]string{prizeSpecLineV1(1, "头奖", 5000, 1)}),
			"v1 的 spec 原像形状是 域前缀 + 逐行,不能变")
	})

	// 完整的一份端到端向量:活动 → 承诺 → 链 → 名单 → 票面。
	// 这五个值是写死的,与本包的任何函数都无关。
	t.Run("golden_vector", func(t *testing.T) {
		a := &Activity{
			ActNo: "LT20260101-0123456789abcdef", Kind: KindDraw, Algo: AlgoV1,
			RulesHash: "rh", SpecHash: "sh", StakeQuota: 1000,
			OpenAt: 1000, CloseAt: 2000, DrawAt: 3000, SettleDeadline: 4000,
			AllowMultiWin: false, FeeBps: 500, MinEntriesToHold: 3,
		}
		const seed = "00112233445566778899aabbccddeeff00112233445566778899aabbccddeeff"

		commit := CommitHash(a, seed)
		assert.Equal(t,
			"9bb42d091765f93110d4871fc5ab5e6489d56bdd737a85ea9862b170415606a5", commit,
			"v1 承诺哈希被改了 —— 所有已开完活动的历史公正查询会集体变成 FAIL")

		chain := ChainNext(commit, a.ActNo, 1, "LE-a", "ref-a", 0, 1000)
		assert.Equal(t,
			"407c20c6e3b14812f764b5e8ee9ca95ab94a02986e718f59ee204dab6c04ac62", chain,
			"v1 链原像被改了 —— 已经发给用户的报名回执会全部对不上")

		rosterHash, n := RosterHash(a.ActNo, commit, []RosterLine{
			{EntryNo: "LE-a", UserRef: "ref-a", Amount: 1000},
			{EntryNo: "LE-b", UserRef: "ref-b", Amount: 1000},
		})
		require.Equal(t, 2, n)
		assert.Equal(t,
			"18d8787c3ede1847b54d63394eae828c6e71065f10e54b18e548c9b6e19f671e", rosterHash,
			"v1 名单原像被改了 —— 揭示前已公开的那份快照会全部对不上")

		final := FinalSeed(a.ActNo, seed, rosterHash, 2, AlgoV1)
		assert.Equal(t,
			"33f0e59d2c2cdb9517a49e981fe8e81d53de65a432f843d16ad627186c04b74a", final)
		assert.Equal(t,
			"ec178d092076c82eb8ac2074e71fb12216c2c25cf4f1e02f2f14ea37b8c48efe",
			Ticket(final, a.ActNo, "LE-a"),
			"票面推导在 v2 里刻意一个字节都没改,这条向量同时守着 v1 与 v2")

		// v1 的名单行**不含 pick**:即使 RosterLine 上带了值也必须被忽略,
		// 否则一场存量活动会因为新加的列而算出不同的名单哈希。
		withPick, _ := RosterHash(a.ActNo, commit, []RosterLine{
			{EntryNo: "LE-a", UserRef: "ref-a", Amount: 1000, Pick: "03,05,12|08"},
			{EntryNo: "LE-b", UserRef: "ref-b", Amount: 1000},
		})
		assert.Equal(t, rosterHash, withPick,
			"v1 名单行绝不能读 pick —— 那会让存量活动的证据链集体失效")
	})
}

// lot-v2 的四个原像同样钉成写死的十六进制串。
//
// 这四个值已经与 qianye/docs/lottery-verify.py 的 v2 分支逐位核对过 ——
// 它们变了就意味着离线验证脚本会算出不同的结果,而用户手里那份脚本不会
// 跟着一起变。
func TestV2PreimageGoldenVector(t *testing.T) {
	const seed = "00112233445566778899aabbccddeeff00112233445566778899aabbccddeeff"

	assert.Equal(t,
		"180bfc90138bff6f0988dc603987e75d7cc7f973b7d33a04853d63d53cf04862",
		CommitHashV2(baseActivityV2(), nil, seed))

	assert.Equal(t,
		"ee80e6294d646e73039c887183425d6651122debe0a197d23f93767360e34eb1",
		ChainNextV2("prev", "LT20260101-0123456789abcdef", 1, "LE-a", "ref-a", 0, 1000, "03,05,12|08"))

	rh, n := RosterHashV2("LT20260101-0123456789abcdef", "c0", []RosterLine{
		{EntryNo: "LE-a", UserRef: "ref-a", Amount: 1000, Pick: "03,05,12|08"},
	})
	require.Equal(t, 1, n)
	assert.Equal(t, "b4439e64e877f0918474698be5ec943d60fee5c51c96725a290eef1cf2b7e6dc", rh)

	assert.Equal(t,
		"d820e4b0b060b7552b329ad79cdec0b3cde99c9eed733c0f26fe5efbd72d4c6a",
		SpecHashV2([]string{PrizeSpecLineV2(PrizeSpec{
			Tier: 1, Name: "头奖", PrizeType: PrizeTypeQuota,
			AmountQuota: 5000, Count: 1, WinPpm: 1000,
		})}))
}

// v1 与 v2 必须算出不同的哈希,否则一份 v2 的原像可以被当作 v1 的重放。
func TestV2DomainsAreSeparatedFromV1(t *testing.T) {
	lines := []string{"x"}
	assert.NotEqual(t, SpecHash(lines), SpecHashV2(lines))

	a := &Activity{ActNo: "LT-1", Kind: KindDraw, Algo: AlgoV2, DrawMode: DrawModeRank}
	assert.NotEqual(t, CommitHash(a, "s"), CommitHashV2(a, nil, "s"))
	assert.NotEqual(t,
		ChainNext("p", "LT-1", 1, "e", "u", 0, 1),
		ChainNextV2("p", "LT-1", 1, "e", "u", 0, 1, ""))

	h1, _ := RosterHash("LT-1", "c", []RosterLine{{EntryNo: "e", UserRef: "u"}})
	h2, _ := RosterHashV2("LT-1", "c", []RosterLine{{EntryNo: "e", UserRef: "u"}})
	assert.NotEqual(t, h1, h2)
}

// ─────────────────────────── v2 承诺覆盖面 ───────────────────────────

func baseActivityV2() *Activity {
	return &Activity{
		ActNo:            "LT20260101-0123456789abcdef",
		Kind:             KindDraw,
		Algo:             AlgoV2,
		DrawMode:         DrawModeProb,
		RulesHash:        "rh",
		SpecHash:         "sh",
		StakeQuota:       1000,
		OpenAt:           1000,
		CloseAt:          2000,
		DrawAt:           3000,
		SettleDeadline:   4000,
		AllowMultiWin:    true,
		FeeBps:           0,
		MinEntriesToHold: 3,
	}
}

// v2 承诺必须覆盖定档方式与整段期次快照。
//
// 少一个占位就等于允许管理员在不碰种子的前提下,把一场按"名次制"公示的活动
// 改成概率制、或者把一场普通抽奖改成双色球 —— 而验证者只会看到结果对不上,
// 却举证不出是哪一边改的。
func TestCommitHashV2CoversDrawModeAndSeries(t *testing.T) {
	const seed = "00112233445566778899aabbccddeeff00112233445566778899aabbccddeeff"
	base := CommitHashV2(baseActivityV2(), nil, seed)

	t.Run("定档方式", func(t *testing.T) {
		a := baseActivityV2()
		a.DrawMode = DrawModeRank
		assert.NotEqual(t, base, CommitHashV2(a, nil, seed),
			"改动定档方式没有改变承诺 —— 管理员可以把概率制悄悄换成名次制")
	})

	cases := []struct {
		name   string
		mutate func(*SeriesSnapshot)
	}{
		{"期次系列号", func(s *SeriesSnapshot) { s.SeriesNo = "SR-1" }},
		{"期号", func(s *SeriesSnapshot) { s.IssueNo = 1 }},
		{"本期注资", func(s *SeriesSnapshot) { s.PoolSeedQuota = 1 }},
		{"上期滚存", func(s *SeriesSnapshot) { s.PoolCarryQuota = 1 }},
		{"开局池子", func(s *SeriesSnapshot) { s.PoolOpenQuota = 1 }},
		{"入池比例", func(s *SeriesSnapshot) { s.PoolShareBps = 1 }},
		{"红球池", func(s *SeriesSnapshot) { s.BallRedPool = 1 }},
		{"红球选号数", func(s *SeriesSnapshot) { s.BallRedPick = 1 }},
		{"蓝球池", func(s *SeriesSnapshot) { s.BallBluePool = 1 }},
		{"蓝球选号数", func(s *SeriesSnapshot) { s.BallBluePick = 1 }},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			s := &SeriesSnapshot{}
			tc.mutate(s)
			assert.NotEqualf(t, base, CommitHashV2(baseActivityV2(), s, seed),
				"改动「%s」没有改变承诺哈希", tc.name)
		})
	}

	t.Run("nil 与全零快照等价", func(t *testing.T) {
		assert.Equal(t, base, CommitHashV2(baseActivityV2(), &SeriesSnapshot{}, seed),
			"非双色球活动传 nil 与传全零快照必须完全等价,否则同一场活动会有两个承诺")
	})
}

// 概率表进 spec 原像。发布之后改一个数字就开不了奖 ——
// 这是"公示的概率为真"这条主张的执行点。
func TestPrizeSpecLineV2CoversEveryPrizeField(t *testing.T) {
	base := PrizeSpec{
		Tier: 1, Name: "头奖", PrizeType: PrizeTypeQuota,
		AmountQuota: 5000, Count: 1, WinPpm: 1000,
	}
	line := PrizeSpecLineV2(base)

	cases := []struct {
		name   string
		mutate func(*PrizeSpec)
	}{
		{"档位", func(p *PrizeSpec) { p.Tier = 2 }},
		{"名称", func(p *PrizeSpec) { p.Name = "二奖" }},
		{"奖品类型", func(p *PrizeSpec) { p.PrizeType = PrizeTypeText }},
		{"额度", func(p *PrizeSpec) { p.AmountQuota = 5001 }},
		{"数量", func(p *PrizeSpec) { p.Count = 2 }},
		{"中奖概率", func(p *PrizeSpec) { p.WinPpm = 1001 }},
		{"文本说明", func(p *PrizeSpec) { p.TextDesc = "联系客服" }},
		{"红球命中门槛", func(p *PrizeSpec) { p.RedMatch = 1 }},
		{"蓝球命中门槛", func(p *PrizeSpec) { p.BlueMatch = 1 }},
		{"占池比例", func(p *PrizeSpec) { p.PoolShareBps = 1 }},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			p := base
			tc.mutate(&p)
			assert.NotEqualf(t, line, PrizeSpecLineV2(p),
				"改动「%s」没有改变奖档原像", tc.name)
		})
	}
}

// pick 必须进链与名单两处原像。
//
// 少任何一处,平台都可以在开奖之后把某个人的号改成中奖号,
// 而链尾、条目计数、名单重算三道校验会**照常全部通过**。
func TestPickEntersBothChainAndRoster(t *testing.T) {
	a := ChainNextV2("p", "LT-1", 1, "e", "u", 0, 1, "03,05,12|08")
	b := ChainNextV2("p", "LT-1", 1, "e", "u", 0, 1, "03,05,12|09")
	assert.NotEqual(t, a, b, "改掉选号必须破链")

	h1, _ := RosterHashV2("LT-1", "c", []RosterLine{{EntryNo: "e", UserRef: "u", Pick: "03,05,12|08"}})
	h2, _ := RosterHashV2("LT-1", "c", []RosterLine{{EntryNo: "e", UserRef: "u", Pick: "03,05,12|09"}})
	assert.NotEqual(t, h1, h2, "改掉选号必须改变名单哈希")
}

// ─────────────────────────── 摇号 ───────────────────────────

// RollPpm 必须与 Python 的 (u64 * 10**6) >> 64 逐位一致。
//
// 这里用 math/big 独立复算一遍 —— 它走的是与 bits.Mul64 完全不同的代码路径,
// 因此这条断言真的在互证,而不是"实现与自己一致"。
func TestRollPpmMatchesBigIntReference(t *testing.T) {
	den := new(big.Int).SetUint64(PpmDen)
	shift := uint(64)

	cases := []string{
		"0000000000000000" + strings.Repeat("0", 48),
		"ffffffffffffffff" + strings.Repeat("f", 48),
		"8000000000000000" + strings.Repeat("0", 48),
		"0000000000000001" + strings.Repeat("0", 48),
		"1a3f5c7e9b0d2f41" + strings.Repeat("a", 48),
		"7fffffffffffffff" + strings.Repeat("0", 48),
	}
	for _, tick := range cases {
		u, err := strconv.ParseUint(tick[:16], 16, 64)
		require.NoError(t, err)
		want := new(big.Int).SetUint64(u)
		want.Mul(want, den)
		want.Rsh(want, shift)

		got := RollPpm(tick)
		assert.Equalf(t, want.Uint64(), uint64(got), "票面 %s 的摇号结果对不上", tick[:16])
		assert.Lessf(t, got, uint32(PpmDen), "摇号结果必须落在 [0, %d)", PpmDen)
	}
}

// 畸形票面必须落在**全部区间之外**,而不是回落成 0。
//
// 回落成 0 会让一张算不出来的票直接中一等奖 —— 方向完全错误的失败。
func TestRollPpmFailsClosed(t *testing.T) {
	assert.EqualValues(t, PpmDen, RollPpm(""))
	assert.EqualValues(t, PpmDen, RollPpm("zzzz"))
	assert.EqualValues(t, PpmDen, RollPpm("zzzzzzzzzzzzzzzz"))
}

// ─────────────────────────── 区间 ───────────────────────────

func TestBandsAreDisjointAndOrdered(t *testing.T) {
	bands, err := Bands([]Tier{
		{Tier: 2, Count: 10, Amount: 100, WinPpm: 10000},
		{Tier: 1, Count: 1, Amount: 5000, WinPpm: 1000},
		{Tier: 3, Count: 0, Amount: 0, WinPpm: 0, PrizeType: PrizeTypeText},
	})
	require.NoError(t, err)
	require.Len(t, bands, 3)

	assert.Equal(t, 1, bands[0].Tier, "必须按 tier 升序,而不是按调用方传进来的顺序")
	assert.EqualValues(t, 0, bands[0].LoPpm)
	assert.EqualValues(t, 1000, bands[0].HiPpm)
	assert.EqualValues(t, 1000, bands[1].LoPpm)
	assert.EqualValues(t, 11000, bands[1].HiPpm)
	// win_ppm=0 的档得到一个空区间,永远不可能被命中:公示 0% 就该是 0%。
	assert.Equal(t, bands[2].LoPpm, bands[2].HiPpm)
}

func TestBandsRejectOverflow(t *testing.T) {
	_, err := Bands([]Tier{
		{Tier: 1, WinPpm: 600000},
		{Tier: 2, WinPpm: 500000},
	})
	require.ErrorIs(t, err, ErrPpmOverflow,
		"概率之和超过 100% 意味着区间重叠,而一张票同时中两档会在派奖层撞唯一键")

	_, err = Bands([]Tier{{Tier: 1, WinPpm: -1}})
	require.ErrorIs(t, err, ErrPpmOverflow)

	_, err = Bands([]Tier{{Tier: 1, WinPpm: 400000}, {Tier: 2, WinPpm: 600000}})
	assert.NoError(t, err, "恰好 100% 是合法的")
}

// ─────────────────────────── 概率制抽取 ───────────────────────────

func probRoster(n int) []RosterLine {
	out := make([]RosterLine, 0, n)
	for i := 0; i < n; i++ {
		no := "LE-" + strconv.Itoa(1000+i)
		out = append(out, RosterLine{EntryNo: no, UserRef: "ref-" + strconv.Itoa(i), Amount: 100})
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].EntryNo < out[j].EntryNo })
	return out
}

// 概率制的判定必须**只**依赖 (final_seed, act_no, entry_no) 与公开的区间表。
//
// 这条性质是"一张票的结果完全不依赖其他票"的全部内容:它同时意味着
// (a) 落选者能自己算出自己为什么没中,(b) 多买多摇没有可利用的结构。
func TestProbResultDependsOnlyOnOwnTicket(t *testing.T) {
	const actNo = "LT-1"
	final := FinalSeed(actNo, "seed", "roster", 50, AlgoV2)
	tiers := []Tier{{Tier: 1, Count: 50, Amount: 100, WinPpm: 500000}}

	full := probRoster(50)
	got, err := PickWinnersProb(final, actNo, full, tiers)
	require.NoError(t, err)

	// 把名单砍掉一半:留下来那些票的**中/不中**必须一个都不变。
	half := full[:25]
	gotHalf, err := PickWinnersProb(final, actNo, half, tiers)
	require.NoError(t, err)

	wonInFull := map[string]bool{}
	for _, w := range got {
		wonInFull[w.EntryNo] = true
	}
	for _, w := range gotHalf {
		assert.Truef(t, wonInFull[w.EntryNo],
			"票 %s 在小名单里中了、在大名单里却没中 —— 结果不该依赖别人报了多少票", w.EntryNo)
	}
	for _, l := range half {
		hit := false
		for _, w := range gotHalf {
			if w.EntryNo == l.EntryNo {
				hit = true
			}
		}
		assert.Equalf(t, wonInFull[l.EntryNo], hit, "票 %s 的判定在两份名单上不一致", l.EntryNo)
	}
}

// 每一条 entry 都被判定过一次:落选者与中奖者走同一段代码。
//
// 这不是写法偏好 —— 它是"平台无法制造一个只有失败者看不到的暗门"这条主张
// 在代码里的落点。
func TestProbJudgesEveryEntryExactlyOnce(t *testing.T) {
	const actNo = "LT-1"
	final := FinalSeed(actNo, "seed", "roster", 200, AlgoV2)
	roster := probRoster(200)
	tiers := []Tier{
		{Tier: 1, Count: 200, Amount: 100, WinPpm: 100000},
		{Tier: 2, Count: 200, Amount: 10, WinPpm: 200000},
	}

	winners, err := PickWinnersProb(final, actNo, roster, tiers)
	require.NoError(t, err)

	seen := map[string]int{}
	for _, w := range winners {
		seen[w.EntryNo]++
	}
	for _, w := range winners {
		assert.Equalf(t, 1, seen[w.EntryNo], "票 %s 中了不止一档", w.EntryNo)
	}

	// 手工复算一遍:每张票的 r 必须能唯一地解释它的结果。
	bands, err := Bands(tiers)
	require.NoError(t, err)
	byEntry := map[string]Winner{}
	for _, w := range winners {
		byEntry[w.EntryNo] = w
	}
	for _, l := range roster {
		r := RollPpm(Ticket(final, actNo, l.EntryNo))
		wantTier := 0
		for _, b := range bands {
			if r >= b.LoPpm && r < b.HiPpm {
				wantTier = b.Tier
			}
		}
		w, won := byEntry[l.EntryNo]
		if wantTier == 0 {
			assert.Falsef(t, won, "票 %s 的 r=%d 落在全部区间之外,却出现在中奖名单里", l.EntryNo, r)
			continue
		}
		require.Truef(t, won, "票 %s 的 r=%d 落在第 %d 档区间内,却没中", l.EntryNo, r, wantTier)
		assert.Equal(t, wantTier, w.Tier)
		assert.EqualValues(t, r, w.RollPpm, "中奖位上必须带着摇号结果,否则前端解释不了「为什么」")
	}
}

// 公示的概率在**任何人数下**都必须为真:超募时浮动的是金额,不是概率。
//
// 这是本轮最关键的一处取舍。按票面顺序截断前 count 名的做法会让一张票的实际
// 中奖概率变成 win_ppm × min(1, count/W) —— 依赖当期人数,于是卡片上印的
// "中奖概率 1%" 在超募时就是假的。
func TestProbOversubscriptionSplitsBudgetInsteadOfTruncating(t *testing.T) {
	const actNo = "LT-1"
	final := FinalSeed(actNo, "seed", "roster", 100, AlgoV2)
	roster := probRoster(100)

	// 100% 命中、但只有 1 份预算:全部 100 人都必须中,共分那一份预算。
	tiers := []Tier{{Tier: 1, Count: 1, Amount: 100000, WinPpm: PpmDen}}
	winners, err := PickWinnersProb(final, actNo, roster, tiers)
	require.NoError(t, err)

	assert.Len(t, winners, 100,
		"命中者一个都不能被截断掉,否则公示的中奖概率在超募时就是假的")

	var sum int64
	for _, w := range winners {
		assert.Positive(t, w.Amount, "任何一位的份额都不得为 0 —— "+
			"PlanPayouts 会跳过 amount<=0,那是一次静默漏发")
		sum += w.Amount
	}
	// 支出上界与名次制**一模一样**:概率模式不引入任何新的发行风险。
	assert.EqualValues(t, 100000, sum,
		"Σ份额必须精确等于本档预算 count×amount,一分不多一分不少")

	// 残差归 entry_no 字节序最大的那一位,与竞猜奖池分配同一套口径。
	last := winners[len(winners)-1]
	assert.Equal(t, roster[len(roster)-1].EntryNo, last.EntryNo)
}

// 未超募时每人拿足额,不做任何摊薄。
func TestProbPaysFullAmountWhenNotOversubscribed(t *testing.T) {
	const actNo = "LT-1"
	final := FinalSeed(actNo, "seed", "roster", 10, AlgoV2)
	roster := probRoster(10)
	tiers := []Tier{{Tier: 1, Count: 100, Amount: 777, WinPpm: PpmDen}}

	winners, err := PickWinnersProb(final, actNo, roster, tiers)
	require.NoError(t, err)
	require.Len(t, winners, 10)
	for _, w := range winners {
		assert.EqualValues(t, 777, w.Amount)
	}
}

// 摊薄到 0 必须**整场失败**,而不是让 PlanPayouts 静默跳过那些人。
//
// 创建期的 `count × amount ≥ 全场参与上限` 已经把这一步堵死;这里守的是
// "万一那道校验被绕过了"的最后一道 —— 宁可挂起等人,也绝不漏发。
func TestProbFailsRatherThanSilentlyDroppingZeroShares(t *testing.T) {
	const actNo = "LT-1"
	final := FinalSeed(actNo, "seed", "roster", 100, AlgoV2)
	roster := probRoster(100)
	// 预算 10,却有 100 个人命中 → 人均 0。
	tiers := []Tier{{Tier: 1, Count: 1, Amount: 10, WinPpm: PpmDen}}

	_, err := PickWinnersProb(final, actNo, roster, tiers)
	require.Error(t, err, "有人会被摊薄到 0 时必须报错,而不是发一份少了几个人的名单")
	assert.ErrorIs(t, err, ErrPoolNotConserved)
}

// 文本奖不摊薄:兑换码劈不开,全部命中者都中,金额恒为 0。
func TestProbTextPrizeGrantsEveryHitterAndCarriesNoAmount(t *testing.T) {
	const actNo = "LT-1"
	final := FinalSeed(actNo, "seed", "roster", 30, AlgoV2)
	roster := probRoster(30)
	tiers := []Tier{
		{Tier: 1, Count: 1, Amount: 0, WinPpm: PpmDen, PrizeType: PrizeTypeText},
	}

	winners, err := PickWinnersProb(final, actNo, roster, tiers)
	require.NoError(t, err)
	require.Len(t, winners, 30, "文本奖不摊薄,全部命中者都中")
	for _, w := range winners {
		assert.Equal(t, PrizeTypeText, w.PrizeType)
		assert.Zero(t, w.Amount)
	}
}

// 概率之和越界时**整场拒绝**,绝不猜一个"大概是这个意思"的解释。
func TestProbRefusesToDrawWhenBandsOverflow(t *testing.T) {
	_, err := PickWinnersProb(FinalSeed("LT-1", "s", "r", 1, AlgoV2), "LT-1",
		probRoster(3), []Tier{{Tier: 1, WinPpm: 900000}, {Tier: 2, WinPpm: 900000}})
	require.ErrorIs(t, err, ErrPpmOverflow)
}

// 可复现性本身就是公正性的一部分:同一份输入必须永远算出同一份名单。
func TestProbIsDeterministic(t *testing.T) {
	const actNo = "LT-1"
	final := FinalSeed(actNo, "seed", "roster", 40, AlgoV2)
	roster := probRoster(40)
	tiers := []Tier{{Tier: 1, Count: 40, Amount: 100, WinPpm: 300000}}

	first, err := PickWinnersProb(final, actNo, roster, tiers)
	require.NoError(t, err)
	second, err := PickWinnersProb(final, actNo, roster, tiers)
	require.NoError(t, err)
	assert.Equal(t, first, second)
}

// ─────────────────────────── 独立实现互证 ───────────────────────────

// independentProbWinners 是一份**只用标准库**的概率制复算:不碰本包的任何函数。
//
// 它就是第三方验证者会写的那三十行,与 qianye/docs/lottery-verify.py 的
// prob_winners 是同一套编码。与 PickWinnersProb 共用任何代码都会让这条断言
// 退化成"实现与自己一致"。
func independentProbWinners(final, actNo string, roster []RosterLine, spec []PrizeSpec) []string {
	const sep = "\x1f"
	key, _ := hex.DecodeString(final)

	sorted := make([]PrizeSpec, len(spec))
	copy(sorted, spec)
	sort.SliceStable(sorted, func(i, j int) bool { return sorted[i].Tier < sorted[j].Tier })

	type band struct {
		tier   int
		lo, hi uint64
		count  int
		amount int64
		text   bool
	}
	bands := make([]band, 0, len(sorted))
	var acc uint64
	for _, s := range sorted {
		bands = append(bands, band{
			tier: s.Tier, lo: acc, hi: acc + uint64(s.WinPpm),
			count: s.Count, amount: s.AmountQuota, text: s.PrizeType == PrizeTypeText,
		})
		acc += uint64(s.WinPpm)
	}

	hits := make(map[int][]string, len(bands))
	for _, l := range roster {
		mac := hmacSHA256(key, strings.Join([]string{"qylot-ticket-v1", actNo, l.EntryNo}, sep))
		u := new(big.Int).SetBytes(mac[:8])
		u.Mul(u, big.NewInt(1000000))
		u.Rsh(u, 64)
		r := u.Uint64()
		for _, b := range bands {
			if r >= b.lo && r < b.hi {
				hits[b.tier] = append(hits[b.tier], l.EntryNo)
				break
			}
		}
	}

	out := make([]string, 0, len(roster))
	for _, b := range bands {
		h := hits[b.tier]
		if len(h) == 0 {
			continue
		}
		budget := b.amount * int64(b.count)
		var paid int64
		for i, no := range h {
			var pay int64
			switch {
			case b.text:
				pay = 0
			case len(h) <= b.count:
				pay = b.amount
			case i == len(h)-1:
				pay = budget - paid
			default:
				pay = budget / int64(len(h))
			}
			paid += pay
			out = append(out, no+"@"+strconv.Itoa(b.tier)+"@"+strconv.FormatInt(pay, 10))
		}
	}
	return out
}

func hmacSHA256(key []byte, msg string) []byte {
	blockSize := 64
	k := make([]byte, blockSize)
	if len(key) > blockSize {
		sum := sha256.Sum256(key)
		copy(k, sum[:])
	} else {
		copy(k, key)
	}
	ipad := make([]byte, blockSize)
	opad := make([]byte, blockSize)
	for i := 0; i < blockSize; i++ {
		ipad[i] = k[i] ^ 0x36
		opad[i] = k[i] ^ 0x5c
	}
	inner := sha256.Sum256(append(append([]byte{}, ipad...), []byte(msg)...))
	outer := sha256.Sum256(append(append([]byte{}, opad...), inner[:]...))
	return outer[:]
}

// 生产实现与一份从零写起的实现必须逐位一致。
//
// 独立那一份连 HMAC 都是手写的(ipad/opad),因此它真的在互证协议本身,
// 而不是在互证 crypto/hmac 与自己一致。
func TestProbMatchesIndependentImplementation(t *testing.T) {
	const actNo = "LT20260101-0123456789abcdef"
	final := FinalSeed(actNo, "seedseed", "rosterhash", 120, AlgoV2)
	roster := probRoster(120)

	spec := []PrizeSpec{
		{Tier: 1, Name: "头奖", PrizeType: PrizeTypeQuota, AmountQuota: 10000, Count: 1, WinPpm: 20000},
		{Tier: 2, Name: "二奖", PrizeType: PrizeTypeQuota, AmountQuota: 500, Count: 5, WinPpm: 150000},
		{Tier: 3, Name: "兑换码", PrizeType: PrizeTypeText, Count: 3, WinPpm: 100000, TextDesc: "联系客服"},
	}
	tiers := make([]Tier, 0, len(spec))
	for _, s := range spec {
		tiers = append(tiers, Tier{
			Tier: s.Tier, Count: s.Count, Amount: s.AmountQuota,
			PrizeType: s.PrizeType, WinPpm: s.WinPpm,
		})
	}

	winners, err := PickWinnersProb(final, actNo, roster, tiers)
	require.NoError(t, err)

	got := make([]string, 0, len(winners))
	for _, w := range winners {
		got = append(got, w.EntryNo+"@"+strconv.Itoa(w.Tier)+"@"+strconv.FormatInt(w.Amount, 10))
	}
	assert.Equal(t, independentProbWinners(final, actNo, roster, spec), got,
		"生产实现与独立复算对不上 —— 线上那份 proof 谁都验不了")
	require.NotEmpty(t, got, "这组参数下必须有人中奖,否则这条断言什么都没验")
}
