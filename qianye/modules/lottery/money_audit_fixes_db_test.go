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

// ─────────────── 系列滚存池的上界必须拦在注资那一刻 ───────────────

// 发行上限(issue_cap_quota)管的是**平台注资**,管不住滚存:pool_quota 还会
// 从 settleSeriesPool 收回用户投注的入池部分,所以它能在 seed_total 远低于
// issue_cap 的情况下越过 common.MaxQuota。越过之后这个系列**永久开不出新一期**
// (checkBallPoolCovers 的 open > MaxQuota 是无条件的,奖级配置救不了),
// 而唯一还能按的按钮(关闭系列)会把整池作废 —— 其中包含真实的用户投注。
// 闸门只能落在注资这一刻,那是这条链上唯一还能改的时刻。
func TestFundSeriesRejectsPoolAboveQuotaCeiling(t *testing.T) {
	s := &Series{
		Status:         SeriesOpen,
		IssueCapQuota:  int64(common.MaxQuota),
		SeedTotalQuota: 1,
		// 滚存已经贴着上界 —— 这一格由用户投注滚回来,发行上限从来没管过它。
		PoolQuota: int64(common.MaxQuota),
	}
	err := checkFundable(s, 1)
	require.Error(t, err)
	be, ok := AsBizError(err)
	require.True(t, ok)
	assert.Equal(t, "qy_lot_series_pool_ceiling", be.ErrCode(),
		"必须与 pool_short 分开报:那一条的文案是「请先注资」,而这里再注资只会更糟")

	// 贴边但不越界仍然放行 —— 闸门不许顺手把合法注资也拦掉。
	s.PoolQuota = int64(common.MaxQuota) - 10
	require.NoError(t, checkFundable(s, 10))
	require.Error(t, checkFundable(s, 11))
}

// 条件 UPDATE 才是执行点:两个管理员同时注资时,checkFundable 各自读到的都是
// 旧值,只有语句里的条件挡得住。
func TestFundSeriesPoolStatementEnforcesTheCeiling(t *testing.T) {
	ext := newPicksCapEnv(t)
	s := &Series{
		SeriesNo: newSeriesNo(), Status: SeriesOpen,
		IssueCapQuota: int64(common.MaxQuota), SeedTotalQuota: 0,
		PoolQuota: int64(common.MaxQuota),
	}
	require.NoError(t, ext.Create(s).Error)

	err := fundSeriesPool(ext, s.Id, 1)
	require.Error(t, err, "越过滚存上界的注资必须被那条语句自己挡住")

	var after Series
	require.NoError(t, ext.Take(&after, s.Id).Error)
	assert.EqualValues(t, int64(common.MaxQuota), after.PoolQuota, "被拒的注资不许改动池子")
	assert.EqualValues(t, 0, after.SeedTotalQuota)
}

// ─────────────── 已结束的活动同样要复核名单与收支 ───────────────

// runReconcile 只扫 published/locked/settling;auditFinishedChains 此前只调
// checkMaterializedInvariants,而后者刻意只查三个 O(1) 量。于是对一场**已结束**
// 活动改动中间某条参与、只要总额守恒,平台侧零告警,而任何第三方按公开 proof
// 复算 roster_hash 都会得到 FAIL。
func TestFinishedActivityStillChecksRosterCommitment(t *testing.T) {
	ext := newPicksCapEnv(t)
	act := seedFinishedActivityWithRoster(t, ext)

	// 守恒篡改:两条参与的金额一增一减,总额、条目数、最大序号、链尾全不变。
	require.NoError(t, ext.Model(&Entry{}).Where("act_id = ? AND seq = ?", act.Id, 1).
		Update("amount", act.StakeQuota+50).Error)
	require.NoError(t, ext.Model(&Entry{}).Where("act_id = ? AND seq = ?", act.Id, 2).
		Update("amount", act.StakeQuota-50).Error)

	chainAuditCursor = 0
	auditFinishedChains(context.Background(), ext)

	assert.EqualValues(t, 1, flagCountOf(t, ext, act.Id, FlagRosterDrift),
		"名单承诺被事后改动,已结束的活动上必须照样报出来")
	assert.EqualValues(t, 0, flagCountOf(t, ext, act.Id, FlagPoolMismatch),
		"总额守恒,奖池那条不该响 —— 这正是它覆盖不到的那一类改动")
}

// 干净的已结束活动不许被误报。
func TestFinishedActivityAuditIsQuietWhenNothingChanged(t *testing.T) {
	ext := newPicksCapEnv(t)
	act := seedFinishedActivityWithRoster(t, ext)

	chainAuditCursor = 0
	auditFinishedChains(context.Background(), ext)

	assert.EqualValues(t, 0, flagCountOf(t, ext, act.Id, FlagRosterDrift))
	assert.EqualValues(t, 0, flagCountOf(t, ext, act.Id, FlagTotalsDrift))
}

// platform_fee_quota / payout_quota / refund_quota 是管理端「本场收支」直读的
// 三列,而此前没有任何一条对账不变量覆盖它们:库里就有一场 payout_quota 记
// 27505 而实付 137005,管理端把一场净亏 107005 的活动显示成净赚 2495。
func TestFinishedActivityChecksPayoutTotals(t *testing.T) {
	ext := newPicksCapEnv(t)
	act := seedFinishedActivityWithRoster(t, ext)

	require.NoError(t, ext.Create(&Payout{
		PayoutNo: newNo(prefixPayout), ActId: act.Id, UserId: ballE2EUserId,
		Kind: PayoutPrize, Status: PayoutPaid, AmountQuota: 137005,
	}).Error)
	// 活动行上记的是另一个数(收尾之后才补发的 held 不会回写这一列)。
	require.NoError(t, ext.Model(&Activity{}).Where("id = ?", act.Id).
		Update("payout_quota", 27505).Error)

	chainAuditCursor = 0
	auditFinishedChains(context.Background(), ext)

	assert.EqualValues(t, 1, flagCountOf(t, ext, act.Id, FlagTotalsDrift),
		"活动行上的收支口径与出款表对不上时必须报出来,否则它可以永久说谎")
}

// ─────────────── 报错里念出来的那个数必须自己填得进去 ───────────────

// buildActivity 在 Normalize 之后才拿 MaxAttemptsPerUser 去比 perUserCapHard,
// 而 Normalize 在只填了参与上限时把尝试上限补成 +3。于是运营填 500(报错文案
// 念的正是这个数)会被派生出的 503 顶回来,真实可用上界是 497 而没人说得出。
func TestNormalizeClampsDerivedAttemptCapToTheHardBound(t *testing.T) {
	for _, entries := range []int{497, 498, 499, 500} {
		r := Rules{MaxEntriesPerUser: entries}.Normalize()
		assert.LessOrEqualf(t, r.MaxAttemptsPerUser, perUserCapHard,
			"每人参与上限填 %d(合法)不许被派生出的尝试上限顶成非法", entries)
		assert.GreaterOrEqual(t, r.MaxAttemptsPerUser, r.MaxEntriesPerUser,
			"尝试上限永远不得低于参与上限")
	}
	// 没顶到硬顶时 +3 的便利必须照旧。
	assert.Equal(t, 13, Rules{MaxEntriesPerUser: 10}.Normalize().MaxAttemptsPerUser)
}

// ─────────────── helpers ───────────────

func seedFinishedActivityWithRoster(t *testing.T, ext *gorm.DB) *Activity {
	t.Helper()
	act := seedBallActivity(t, ext, func(a *Activity) {
		a.MaxPicksPerRequest = 10
		a.MaxTotalEntries = 50_000
	})
	prev := ""
	for seq := 1; seq <= 2; seq++ {
		e := &Entry{
			ActId: act.Id, EntryNo: newNo(prefixEntry), Seq: seq,
			UserId: ballE2EUserId + seq, UserRef: "ref-" + itoaTest(seq),
			Amount: act.StakeQuota, Status: EntrySuccess,
			IdemKey: "roster-seed-" + itoaTest(seq), PrevHash: prev,
		}
		if seq == 1 {
			e.PrevHash = act.CommitHash
		}
		e.ChainHash = ChainNextFor(act.Algo, e.PrevHash, act.ActNo, e.Seq,
			e.EntryNo, e.UserRef, e.OptNo, e.Amount, e.Pick)
		prev = e.ChainHash
		require.NoError(t, ext.Create(e).Error)
	}
	roster, err := loadRoster(context.Background(), ext, act.Id)
	require.NoError(t, err)
	hash, count := RosterHashFor(act.Algo, act.ActNo, act.CommitHash, rosterLines(roster))
	require.NoError(t, ext.Model(&Activity{}).Where("id = ?", act.Id).Updates(map[string]any{
		"status":       StatusFinished,
		"entry_seq":    2,
		"active_count": 2,
		"pool_quota":   act.StakeQuota * 2,
		"chain_head":   prev,
		"roster_hash":  hash,
		"roster_count": count,
	}).Error)
	var out Activity
	require.NoError(t, ext.Take(&out, act.Id).Error)
	return &out
}

func flagCountOf(t *testing.T, ext *gorm.DB, actId int64, code string) int64 {
	t.Helper()
	var n int64
	require.NoError(t, ext.Model(&Flag{}).
		Where("act_id = ? AND code = ?", actId, code).Count(&n).Error)
	return n
}

func itoaTest(n int) string {
	if n < 10 {
		return string(rune('0' + n))
	}
	return "x"
}

var _ = config.Lottery{}
