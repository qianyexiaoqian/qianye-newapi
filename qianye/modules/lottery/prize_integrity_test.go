package lottery

import (
	"context"
	"strings"
	"testing"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/qianye/config"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

// prize_integrity_test.go —— 三条与"发出去多少钱、发给谁"直接相关的契约:
//
//  1. 名次制的文本奖中奖位必须带上 prize_type(漏掉 = 整档奖静默消失)
//  2. spec 原像的分隔符不可被业务字符串注入(注入 = 承诺哈希可碰撞)
//  3. 额度支出不得超过奖档表自己的预算(净增发的结构性上界)
//
// 三条都不是实现细节:第 1、3 条决定用户能不能拿到承诺的东西,
// 第 2 条决定"公示的奖品与概率为真"这句话是不是真的。

// ─────────────── 1. 名次制的文本奖 ───────────────

// PickWinners 必须把 prize_type 原样带出去。
//
// 漏掉它的后果是一条完整的静默链:文本奖位落成 amount=0 的额度腿 →
// PlanPayouts 的「amount<=0 跳过」把它整批丢掉 → 连 payout 行都不落 →
// text_grant_count=0 让 finishIfDone 与 auditTextPrizes 的复核双双退化成
// 0>=0 恒真 → 活动照常 finished,全链路零告警。中奖者永远收不到兑换码,
// 而外部验证者按种子重算会多出这几位并判整场 FAIL。
//
// prob 与 ball 两条分支各自都带了它,唯独 rank 漏了 —— 而 rank 是默认玩法。
func TestPickWinnersCarriesPrizeType(t *testing.T) {
	const (
		seed  = "3d1f0c9a7b5e2648a0c1d2e3f4051627384950617283940a1b2c3d4e5f607182"
		actNo = "LT20260101-0123456789abcdef"
	)
	roster := []RosterLine{
		{EntryNo: "LE-01", UserRef: "u1"}, {EntryNo: "LE-02", UserRef: "u2"},
		{EntryNo: "LE-03", UserRef: "u3"}, {EntryNo: "LE-04", UserRef: "u4"},
		{EntryNo: "LE-05", UserRef: "u5"},
	}
	tiers := []Tier{
		{Tier: 1, Count: 2, Amount: 0, PrizeType: PrizeTypeText},
		{Tier: 2, Count: 3, Amount: 1000, PrizeType: PrizeTypeQuota},
	}

	winners := PickWinners(seed, actNo, roster, tiers, true)
	require.Len(t, winners, 5, "5 张票、5 个奖位,应当发满")

	byTier := map[int][]Winner{}
	for _, w := range winners {
		byTier[w.Tier] = append(byTier[w.Tier], w)
	}
	require.Len(t, byTier[1], 2)
	require.Len(t, byTier[2], 3)
	for _, w := range byTier[1] {
		assert.Equal(t, PrizeTypeText, w.PrizeType,
			"文本奖位丢了 prize_type —— 它会被 PlanPayouts 静默丢掉,中奖者什么都拿不到")
		assert.EqualValues(t, 0, w.Amount)
	}
	for _, w := range byTier[2] {
		assert.Equal(t, PrizeTypeQuota, w.PrizeType)
		assert.EqualValues(t, 1000, w.Amount)
	}
}

// 一整场 rank + 混合奖档走到派奖计划落库,断言文本腿真的出现在 qy_lot_payout 里。
//
// 上面那条纯函数断言证明不了这件事:中间还隔着 revealActivity 的分腿与
// PlanPayouts 的跳过规则,任何一处再把 prize_type 抹掉,单测照样全绿。
func TestRankTextPrizeReachesPayoutTable(t *testing.T) {
	gdb := newPayoutEnv(t, config.Lottery{
		Enabled:                true,
		PayoutMaxAttempts:      8,
		EntryCloseGraceSeconds: 0,
		RevealDelaySeconds:     0,
		MaxStakeQuota:          5_000_000,
	})

	now := common.GetTimestamp()
	prizes := []Prize{
		{Tier: 1, Name: "实物奖", AmountQuota: 0, Count: 2,
			PrizeType: PrizeTypeText, TextDesc: "凭此码联系客服领取"},
		{Tier: 2, Name: "额度奖", AmountQuota: 1000, Count: 3, PrizeType: PrizeTypeQuota},
	}
	specLines := make([]string, 0, len(prizes))
	for _, p := range prizes {
		specLines = append(specLines, prizeSpecLineOf(AlgoV2, p))
	}

	act := seedActivity(t, gdb, func(a *Activity) {
		a.Status = StatusDraft
		a.Kind = KindDraw
		a.Algo = AlgoV2
		a.DrawMode = DrawModeRank
		a.AllowMultiWin = true
		a.OpenAt = now - 3600
		a.CloseAt = now - 2
		a.DrawAt = now - 1
		a.RulesText = `{"min_quota":0}`
		a.RulesHash = RulesHash(`{"min_quota":0}`)
		a.SpecHash = SpecHashV2(specLines)
		a.SpecText = strings.Join(specLines, SEP)
		a.CommitHash = ""
	})
	require.NoError(t, gdb.Create(&Seed{
		ActId: act.Id, Seed: newSecret(), RefSalt: newSecret(), IpSalt: newSecret(),
		CreatedAt: now,
	}).Error)
	for i := range prizes {
		prizes[i].ActId = act.Id
	}
	require.NoError(t, gdb.Create(&prizes).Error)

	ctx := context.Background()
	commit, err := computeCommit(ctx, gdb, act)
	require.NoError(t, err)
	require.NoError(t, gdb.Model(&Activity{}).Where("id = ?", act.Id).
		Updates(map[string]any{
			"status": StatusPublished, "commit_hash": commit, "chain_head": commit,
			"published_at": now, "close_at": now + 3600,
		}).Error)

	salts, err := loadSalts(ctx, gdb, act.Id)
	require.NoError(t, err)
	for uid := 301; uid < 306; uid++ {
		e := &Entry{
			EntryNo: newEntryNo(), ActId: act.Id, IdemKey: buildIdemKey(act.ActNo, newEntryNo()),
			UserId: uid, UserRef: UserRef(salts.RefSalt, uid), Amount: act.StakeQuota,
			Status: EntryPending, OrderNo: "LE-" + newEntryNo(), CreatedAt: common.GetTimestamp(),
		}
		cur := loadAct(t, gdb, act.Id)
		require.NoError(t, gdb.Transaction(func(tx *gorm.DB) error {
			return reserveEntry(tx, cur, Rules{}, e)
		}))
		require.NoError(t, gdb.Transaction(func(tx *gorm.DB) error {
			return markEntrySuccess(tx, e.EntryNo, nil)
		}))
	}

	require.NoError(t, gdb.Model(&Activity{}).Where("id = ?", act.Id).
		Update("close_at", now-2).Error)
	runLock(ctx)
	require.Equal(t, StatusLocked, loadAct(t, gdb, act.Id).Status)
	runReveal(ctx)

	drawn := loadAct(t, gdb, act.Id)
	require.Equal(t, StatusSettling, drawn.Status)
	require.Equal(t, OutcomeDrawn, drawn.Outcome)

	var granted, planned int64
	require.NoError(t, gdb.Model(&Payout{}).
		Where("act_id = ? AND kind = ? AND status = ?", act.Id, PayoutText, PayoutGranted).
		Count(&granted).Error)
	require.NoError(t, gdb.Model(&Payout{}).
		Where("act_id = ? AND kind = ? AND status = ?", act.Id, PayoutPrize, PayoutPlanned).
		Count(&planned).Error)

	assert.EqualValues(t, 2, granted, "名次制的文本奖必须真的落成 granted 出款行")
	assert.EqualValues(t, 2, drawn.TextGrantCount,
		"text_grant_count 落成 0 会让 finishIfDone 与 auditTextPrizes 的复核退化成恒真")
	assert.EqualValues(t, 3, planned)
	assert.EqualValues(t, 3000, drawn.PayoutQuota, "额度腿的总额不受文本腿影响")
}

// 已结束活动的条目篡改必须被对账抓到。
//
// runReconcile 的主循环只扫 published/locked/settling —— 而"历史公正查询"的
// 全部内容恰好就是 finished 的那一批。实测同一处篡改在 published 活动上 30 秒内
// 触发 pool_mismatch,在 finished 活动上观察四分钟一条 flag 都没有,只有用户
// 自己下载证据链跑脚本才发现得了。auditSpecDrift 早就为奖档表做了跨 finished
// 的扫描,这条断言把同一条纪律扩到条目与哈希链上。
func TestReconcileAuditsFinishedActivities(t *testing.T) {
	gdb := textEnv(t)
	act := seedActivity(t, gdb, func(a *Activity) {
		a.Status = StatusFinished
		a.Outcome = OutcomeDrawn
		a.PoolQuota = 3000
		a.ActiveCount = 3
		a.EntrySeq = 3
		a.ChainHead = "deadbeef"
	})
	for seq := 1; seq <= 3; seq++ {
		require.NoError(t, gdb.Create(&Entry{
			EntryNo: newEntryNo(), ActId: act.Id, Seq: seq,
			IdemKey: buildIdemKey(act.ActNo, newEntryNo()),
			UserId:  400 + seq, Amount: 1000, Status: EntrySuccess,
			ChainHash: "deadbeef", CreatedAt: common.GetTimestamp(),
		}).Error)
	}

	// 干净的现场:不许误报。
	runReconcile(context.Background())
	var clean int64
	require.NoError(t, gdb.Model(&Flag{}).Where("act_id = ?", act.Id).Count(&clean).Error)
	require.Zero(t, clean, "对账在一份自洽的已结束活动上误报了")

	// 篡改一条已结束活动的投注金额。
	require.NoError(t, gdb.Model(&Entry{}).
		Where("act_id = ? AND seq = ?", act.Id, 2).Update("amount", 9000).Error)
	chainAuditCursor = 0
	runReconcile(context.Background())

	var flags []Flag
	require.NoError(t, gdb.Where("act_id = ? AND code = ?", act.Id, FlagPoolMismatch).
		Find(&flags).Error)
	assert.NotEmpty(t, flags, "已结束活动的条目篡改必须落 flag —— 它们才是历史公正查询的全部内容")
}

// ─────────────── 2. spec 原像的分隔符注入 ───────────────

// 奖档名称里的 SEP 可以让两张**结构不同**的奖档表拼出同一个 spec_text。
//
// 这不是理论:实测把一档
// `名称 = "奖" + SEP + "quota" + SEP + "1000" + ... + SEP + "二等奖"` 的活动发布
// 出去,封盘后把 qy_lot_prize 换成对应的两档表,承诺哈希复算通过、
// checkSpecIntegrity 的 spec_hash 与 spec_text 两条比对**双双通过**、
// 一条 flag 都不落,连仓库自带的 lottery-verify.py 都输出「全部通过」——
// 而实发额度超过了发布时承诺的支出上界。
//
// 这条用例先把碰撞本身钉下来(说明为什么必须拦),再断言创建期确实拦住了。
func TestSpecPreimageRejectsSeparatorInjection(t *testing.T) {
	honest := []Prize{
		{Tier: 1, Name: "奖", AmountQuota: 1000, Count: 1, PrizeType: PrizeTypeQuota},
		{Tier: 2, Name: "二等奖", AmountQuota: 5000, Count: 1, PrizeType: PrizeTypeQuota},
	}
	honestText := strings.Join([]string{
		prizeSpecLineOf(AlgoV2, honest[0]), prizeSpecLineOf(AlgoV2, honest[1]),
	}, SEP)

	// 一档,但名字里塞进了整整一行半的原像。
	injected := Prize{
		Tier: 1,
		Name: strings.Join([]string{
			"奖", PrizeTypeQuota, "1000", "1", "0", "", "0", "0", "0", "2", "二等奖",
		}, SEP),
		AmountQuota: 5000, Count: 1, PrizeType: PrizeTypeQuota,
	}
	require.Equal(t, honestText, prizeSpecLineOf(AlgoV2, injected),
		"两张结构不同的奖档表拼出了同一份 spec 原像 —— 这正是必须在入口拒绝控制字符的理由")

	cfg := config.Lottery{MaxPrizeTiers: 8, MaxTotalEntriesHard: 10000}
	set := opSettings{MaxTotalPrizeQuota: 50_000_000}
	act := &Activity{DrawMode: DrawModeRank, Algo: AlgoV2, MaxTotalEntries: 100}

	t.Run("奖档名称", func(t *testing.T) {
		_, _, err := buildPrizes([]prizeInput{
			{Tier: 1, Name: "奖" + SEP + "quota", AmountQuota: 1000, Count: 1},
		}, cfg, set, act)
		require.Error(t, err)
	})

	t.Run("文本奖履行说明", func(t *testing.T) {
		_, _, err := buildPrizes([]prizeInput{
			{Tier: 1, Name: "实物奖", Count: 1, PrizeType: PrizeTypeText,
				TextDesc: "联系客服" + SEP + "0"},
		}, cfg, set, act)
		require.Error(t, err)
	})

	t.Run("竞猜选项文案", func(t *testing.T) {
		_, _, err := buildOptions([]optionInput{
			{OptNo: 1, Label: "A 队胜" + SEP + "2"},
			{OptNo: 2, Label: "B 队胜"},
		}, config.Lottery{MaxOptions: 12})
		require.Error(t, err)
	})

	// 不止 0x1F:NUL 会截断日志,ESC 能改写 lottery-verify.py 打印到终端的输出。
	for name, bad := range map[string]string{
		"NUL": "奖\x00品", "ESC": "奖\x1b[31m品", "换行": "奖\n品", "DEL": "奖\x7f品",
	} {
		t.Run("控制字符/"+name, func(t *testing.T) {
			_, _, err := buildPrizes([]prizeInput{
				{Tier: 1, Name: bad, AmountQuota: 1000, Count: 1},
			}, cfg, set, act)
			require.Error(t, err)
		})
	}

	// 正常的中英文、标点、emoji 一律照旧放行 —— 这道闸门不能变成一个字都填不进去。
	t.Run("正常文案照旧放行", func(t *testing.T) {
		rows, _, err := buildPrizes([]prizeInput{
			{Tier: 1, Name: "一等奖 · Grand Prize 🎉", AmountQuota: 1000, Count: 1},
		}, cfg, set, act)
		require.NoError(t, err)
		require.Len(t, rows, 1)
	})
}

// ─────────────── 3. 额度支出的结构性上界 ───────────────

// 双色球在 PickWinnersBall 里逐笔累加比对 poolOpen、竞猜在 SplitPool 结尾断言
// Σpay + fee == pool,只有 rank/prob 此前没有任何开奖期的支出上界断言 ——
// 它们从奖档行读 amount×count 就发钱,而抽奖派奖是对用户额度的净增发。
func TestCheckQuotaBudget(t *testing.T) {
	tiers := []Tier{
		{Tier: 1, Count: 1, Amount: 5000, PrizeType: PrizeTypeQuota},
		{Tier: 2, Count: 2, Amount: 2000, PrizeType: PrizeTypeQuota},
		// 文本奖不占额度预算:amount 恒为 0,发的是兑换码。
		{Tier: 3, Count: 100, Amount: 0, PrizeType: PrizeTypeText},
	}

	cases := []struct {
		name      string
		payoutSum int64
		wantErr   bool
	}{
		{"未发满", 5000, false},
		{"恰好发满", 9000, false},
		{"超出一个额度", 9001, true},
		{"零支出", 0, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := checkQuotaBudget(tiers, tc.payoutSum)
			if tc.wantErr {
				require.ErrorIs(t, err, ErrPoolNotConserved)
				return
			}
			require.NoError(t, err)
		})
	}

	t.Run("文本档不能撑大预算", func(t *testing.T) {
		require.Error(t, checkQuotaBudget(
			[]Tier{{Tier: 1, Count: 100, Amount: 0, PrizeType: PrizeTypeText}}, 1))
	})
}
