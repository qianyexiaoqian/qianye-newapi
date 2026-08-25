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

// tamper_guard_db_test.go —— "发布之后没人能再改结果"这条主张的执行点。
//
// 承诺链本身只证明了 spec_hash / rules_hash 这几个**字符串**没被改。
// 抽取名单、算金额读的却是 qy_lot_prize 的**行**。这两者之间原本没有任何
// 校验:封盘之后 roster_hash 已公开、final_seed 对持有种子的人已可算,
// 此刻改一行 win_ppm 就等于点名挑中奖者(每张票的 r 已确定,把区间挪到覆盖
// 目标票即可),改一行 amount_quota 就绕过了发布期那道净增发闸门。
// 两者原本都只有"用户自己下载证据链跑脚本"才会发现。

// probActivityForTamper 造一场发布完毕、名单已冻结、等着开奖的概率制活动。
//
// 走的是真实路径:computeCommit 生成承诺、reserveEntry 落条目、runLock 封盘。
// 只有"改奖档"那一步是测试自己动的手 —— 那正是要抓的攻击。
func probActivityForTamper(t *testing.T, gdb *gorm.DB) *Activity {
	t.Helper()
	now := common.GetTimestamp()
	prizes := []Prize{
		{Tier: 1, Name: "额度奖", AmountQuota: 3000, Count: 40,
			PrizeType: PrizeTypeQuota, WinPpm: 100000},
	}
	specLines := make([]string, 0, len(prizes))
	for _, p := range prizes {
		specLines = append(specLines, prizeSpecLineOf(AlgoV2, p))
	}

	act := seedActivity(t, gdb, func(a *Activity) {
		a.Status = StatusDraft
		a.Kind = KindDraw
		a.Algo = AlgoV2
		a.DrawMode = DrawModeProb
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

	commit, err := computeCommit(context.Background(), gdb, act)
	require.NoError(t, err)
	require.NoError(t, gdb.Model(&Activity{}).Where("id = ?", act.Id).
		Updates(map[string]any{
			"status": StatusPublished, "commit_hash": commit, "chain_head": commit,
			"published_at": now,
		}).Error)
	require.NoError(t, gdb.Model(&Activity{}).Where("id = ?", act.Id).
		Update("close_at", now+3600).Error)
	act = loadAct(t, gdb, act.Id)

	salts, err := loadSalts(context.Background(), gdb, act.Id)
	require.NoError(t, err)
	for uid := 301; uid < 311; uid++ {
		e := &Entry{
			EntryNo: newEntryNo(), ActId: act.Id, IdemKey: buildIdemKey(act.ActNo, newEntryNo()),
			UserId: uid, UserRef: UserRef(salts.RefSalt, uid), Amount: act.StakeQuota,
			Status: EntryPending, OrderNo: "LE-" + newEntryNo(), CreatedAt: common.GetTimestamp(),
		}
		cur := loadAct(t, gdb, act.Id)
		require.NoError(t, gdb.Transaction(func(tx *gorm.DB) error {
			return reserveEntry(tx, cur, Rules{}, e, 0)
		}))
		require.NoError(t, gdb.Transaction(func(tx *gorm.DB) error {
			return markEntrySuccess(tx, e.EntryNo, nil)
		}))
	}

	require.NoError(t, gdb.Model(&Activity{}).Where("id = ?", act.Id).
		Update("close_at", now-2).Error)
	runLock(context.Background())
	locked := loadAct(t, gdb, act.Id)
	require.Equal(t, StatusLocked, locked.Status)
	return locked
}

// 概率被事后改动 → 拒绝开奖,而不是照常开出一份"点名"的名单。
//
// 这是本轮唯一一条能在**平台侧**发现概率篡改的机制。没有它,检出完全依赖
// "有用户下载证据链并跑脚本",而那时钱已经发出去了。
func TestRevealRefusesWhenWinOddsWereChangedAfterPublish(t *testing.T) {
	gdb := textEnv(t)
	act := probActivityForTamper(t, gdb)

	// 封盘之后把 1% 改成 90%:每张票的 r 此刻已经确定,挪区间等于点名。
	require.NoError(t, gdb.Model(&Prize{}).Where("act_id = ? AND tier = ?", act.Id, 1).
		Update("win_ppm", 900000).Error)

	runReveal(context.Background())

	after := loadAct(t, gdb, act.Id)
	assert.Equal(t, StatusLocked, after.Status, "奖档被改过就绝不能开奖")
	assert.Equal(t, OutcomeNone, after.Outcome)

	var payouts int64
	require.NoError(t, gdb.Model(&Payout{}).Where("act_id = ?", act.Id).Count(&payouts).Error)
	assert.Zero(t, payouts, "一条派奖计划都不该落下")

	var flags []Flag
	require.NoError(t, gdb.Where("act_id = ? AND code = ?", act.Id, FlagRevealRefuse).
		Find(&flags).Error)
	assert.NotEmpty(t, flags, "拒绝开奖必须落异常标记,否则活动只是安静地不动")
}

// 奖金额被事后改大 → 同样拒绝开奖。
//
// 发布期那道 Σ(count × amount) ≤ max_total_prize_quota 是**唯一**能拦住
// "奖品金额多写一个零"的闸门,而抽奖派奖是对用户额度的净增发。
func TestRevealRefusesWhenPrizeAmountWasChangedAfterPublish(t *testing.T) {
	gdb := textEnv(t)
	act := probActivityForTamper(t, gdb)

	require.NoError(t, gdb.Model(&Prize{}).Where("act_id = ? AND tier = ?", act.Id, 1).
		Update("amount_quota", 300000).Error)

	runReveal(context.Background())
	assert.Equal(t, StatusLocked, loadAct(t, gdb, act.Id).Status)
}

// 没被改过的那一场必须照常开出去 —— 否则上面两条只是把开奖整个关掉了。
func TestRevealStillSucceedsOnAnUntouchedActivity(t *testing.T) {
	gdb := textEnv(t)
	act := probActivityForTamper(t, gdb)

	runReveal(context.Background())

	after := loadAct(t, gdb, act.Id)
	assert.Equal(t, StatusSettling, after.Status)
	assert.Equal(t, OutcomeDrawn, after.Outcome)
}

// 对账每一轮也要复核一次奖档表:开奖前那道拒绝只在一瞬间跑过。
func TestReconcileFlagsSpecDrift(t *testing.T) {
	gdb := textEnv(t)
	act := probActivityForTamper(t, gdb)

	require.NoError(t, gdb.Model(&Prize{}).Where("act_id = ?", act.Id).
		Update("name", "改了个名字").Error)

	reconcileActivity(context.Background(), gdb, loadAct(t, gdb, act.Id))

	var flags []Flag
	require.NoError(t, gdb.Where("act_id = ? AND code = ?", act.Id, FlagSpecDrift).
		Find(&flags).Error)
	assert.NotEmpty(t, flags, "奖档漂移必须在对账里持续可见,而不是只在开奖那一刻")
}

// ─────────────────────── 双色球的浮动奖 ───────────────────────

// 「一等奖 = 池子的 X%」必须能被创建出来。
//
// buildPrizes 跑在 applyBallSpec 之前。它原本对所有 quota 档一律要求
// amount_quota > 0,而浮动奖的额度**必须为 0**(与占池比例互斥)——
// 两条互斥的结果是双色球需求原话里的核心玩法一次都发不出去,
// 而 ballTierAmounts 的占池分支、checkBallPoolCovers 的 share 项、
// verify.py 的 pool_share_bps 分支、前端的占池列全部成了永不执行的死路径。
func TestFloatingBallTierIsStructurallyCreatable(t *testing.T) {
	cfg := config.Lottery{MaxPrizeTiers: 8, MaxTotalEntriesHard: 10000}
	set := opSettings{MaxTotalPrizeQuota: 1_000_000}
	act := &Activity{DrawMode: DrawModeBall, Algo: AlgoV2, MaxTotalEntries: 1000}

	rows, lines, err := buildPrizes([]prizeInput{
		{Tier: 1, Name: "一等奖", AmountQuota: 0, Count: 1, PoolShareBps: 5000,
			RedMatch: 4, BlueMatch: 1},
	}, cfg, set, act)
	require.NoError(t, err, "浮动奖档必须能建出来 —— 它是双色球的核心玩法")
	require.Len(t, rows, 1)
	assert.EqualValues(t, 0, rows[0].AmountQuota)
	assert.Len(t, lines, 1)

	// 反过来:浮动奖同时写死额度仍然要被拒 —— 到底按哪个发不能靠代码里的
	// 先后顺序回答。
	_, _, err = buildPrizes([]prizeInput{
		{Tier: 1, Name: "一等奖", AmountQuota: 1000, Count: 1, PoolShareBps: 5000},
	}, cfg, set, act)
	require.Error(t, err)

	// 固定奖档(占池比例为 0)照旧必须填正额度。
	_, _, err = buildPrizes([]prizeInput{
		{Tier: 1, Name: "一等奖", AmountQuota: 0, Count: 1},
	}, cfg, set, act)
	require.Error(t, err)
}

// 均分把人均摊到 0 时 fail-closed,而不是静默发 0。
//
// PlanPayouts 对 amount<=0 的额度计划**直接跳过且不报错**,于是那些人连
// payout 行都不会有;而证据链的 winners 是从 payout 表拼出来的,验证脚本
// 会把他们算出来并判 FAIL —— 平台看起来像在作弊,实际是漏发。
func TestBallSplitFailsClosedInsteadOfPayingZero(t *testing.T) {
	hits := []RosterLine{{EntryNo: "a"}, {EntryNo: "b"}, {EntryNo: "c"}}

	_, err := ballSplitEven(1, 2, hits)
	require.ErrorIs(t, err, ErrPoolNotConserved, "预算 2 摊给 3 个人必须报错,不能有人拿 0")

	got, err := ballSplitEven(1, 10, hits)
	require.NoError(t, err)
	assert.Equal(t, []int64{3, 3, 4}, got, "残差归 entry_no 最大者,总额精确守恒")

	// 没有人中签时不是错误,只是空结果。
	got, err = ballSplitEven(1, 0, nil)
	require.NoError(t, err)
	assert.Empty(t, got)
}

// 双色球的参与费入池部分必须被 int32 夹住。
//
// 系列池一旦越过 MaxQuota,checkBallPoolCovers 会让这个系列**永久开不出新一期**,
// 而且没有任何接口能把池子降下来 —— 玩家出资形成的奖池就此不可达。
func TestBallEntryIsRejectedBeforeThePoolCanOverflowInt32(t *testing.T) {
	gdb := textEnv(t)
	act := seedActivity(t, gdb, func(a *Activity) {
		a.DrawMode = DrawModeBall
		a.PoolShareBps = 10000 // 投注全额入池
		a.PoolOpenQuota = int64(common.MaxQuota) - 100
		a.PoolQuota = 0
		a.StakeQuota = 1000
	})

	e := &Entry{
		EntryNo: newEntryNo(), ActId: act.Id, IdemKey: buildIdemKey(act.ActNo, newEntryNo()),
		UserId: 1, UserRef: "r", Amount: 1000,
		Status: EntryPending, CreatedAt: common.GetTimestamp(),
	}
	cur := loadAct(t, gdb, act.Id)
	err := gdb.Transaction(func(tx *gorm.DB) error {
		return reserveEntry(tx, cur, Rules{}, e, 0)
	})
	require.ErrorIs(t, err, errCapReached,
		"开局池已经贴着 int32 上限,再进一笔就会让系列永久开不出新一期")

	// 池子离上限还远时照常放行 —— 否则这条闸门就是把双色球整个关掉。
	require.NoError(t, gdb.Model(&Activity{}).Where("id = ?", act.Id).
		Update("pool_open_quota", 1000).Error)
	ok := &Entry{
		EntryNo: newEntryNo(), ActId: act.Id, IdemKey: buildIdemKey(act.ActNo, newEntryNo()),
		UserId: 2, UserRef: "r2", Amount: 1000,
		Status: EntryPending, CreatedAt: common.GetTimestamp(),
	}
	relaxed := loadAct(t, gdb, act.Id)
	require.NoError(t, gdb.Transaction(func(tx *gorm.DB) error {
		return reserveEntry(tx, relaxed, Rules{}, ok, 0)
	}))
}

// ─────────────────────── 文本奖 ───────────────────────

// 对账的文本奖扫描必须走游标,不能永远只看最老的那一批。
//
// 有文本奖的活动几秒内就 finished 且 text_grant_count 恒 > 0,这张结果集只增
// 不减:一个固定 LIMIT 恒返回 id 最小的那一批,第 N+1 场之后的丢行永久零告警,
// 而那正是这条对账唯一要抓的场景。
func TestTextPrizeAuditAdvancesPastTheFirstBatch(t *testing.T) {
	gdb := textEnv(t)
	t.Cleanup(func() { textAuditCursor = 0 })
	textAuditCursor = 0

	// 前 batchPerRound*10 场都是健康的(登记 1 位、库里也有 1 行)。
	batch := batchPerRound * 10
	for i := 0; i < batch; i++ {
		a := seedActivity(t, gdb, func(a *Activity) {
			a.Status = StatusFinished
			a.Outcome = OutcomeDrawn
			a.TextGrantCount = 1
		})
		seedPayout(t, gdb, a.Id, func(p *Payout) {
			p.Kind = PayoutText
			p.Status = PayoutGranted
			p.AmountQuota = 0
			p.EntryId = a.Id
		})
	}
	// 第 batch+1 场:登记了 1 位,但那一行被删掉了(运维误删 / 迁移丢失)。
	victim := seedActivity(t, gdb, func(a *Activity) {
		a.Status = StatusFinished
		a.Outcome = OutcomeDrawn
		a.TextGrantCount = 1
	})

	// 第一轮只覆盖到前 batch 场 —— 受害者还轮不到。
	auditTextPrizes(context.Background(), gdb)
	var flags []Flag
	require.NoError(t, gdb.Where("act_id = ? AND code = ?", victim.Id, FlagTextGrantMissing).
		Find(&flags).Error)
	require.Empty(t, flags, "第一轮本来就扫不到它 —— 这一步只是把游标推过第一批")

	// 第二轮游标已经推过第一批,必须扫到它。固定 LIMIT 的写法在这里永远为空。
	auditTextPrizes(context.Background(), gdb)
	require.NoError(t, gdb.Where("act_id = ? AND code = ?", victim.Id, FlagTextGrantMissing).
		Find(&flags).Error)
	assert.NotEmpty(t, flags,
		"第 201 场之后的文本奖丢行必须能被扫到,否则中奖者的奖品静默消失且三处零信号")
}

// 撤销履行之后再次履行,**上一串码不能消失**。
//
// 撤销刻意不清密文(用户可能已经用掉了那串码),但再次履行的 CAS 只判
// fulfilled_at = 0,会把上一串整列覆盖;而 Event 与审计快照按设计都不含明文,
// 覆盖之后"用户当初拿到的是哪一串"在系统里彻底消失 ——
// 那恰恰是撤销这个功能自己承诺要保住的东西。
func TestRefulfillArchivesTheSupersededCiphertext(t *testing.T) {
	gdb := textEnv(t)
	act := seedActivity(t, gdb, nil)
	p := seedPayout(t, gdb, act.Id, func(p *Payout) {
		p.Kind = PayoutText
		p.Status = PayoutGranted
		p.AmountQuota = 0
	})

	first, err := sealAndStore(t, gdb, p.PayoutNo, "CDK-AAAA-1111", "第一次")
	require.NoError(t, err)
	require.EqualValues(t, 1, first)

	// 撤销:账面清零,密文保留。
	require.NoError(t, gdb.Model(&Payout{}).Where("payout_no = ?", p.PayoutNo).
		Updates(map[string]any{"fulfilled_at": 0, "fulfilled_by": 0, "fulfill_note": ""}).Error)

	// 再次履行:覆盖前必须先归档。
	cur := reloadPayout(t, gdb, p.PayoutNo)
	require.NoError(t, gdb.Transaction(func(tx *gorm.DB) error {
		return archivePrizeSecret(tx, cur, 7)
	}))
	_, err = sealAndStore(t, gdb, p.PayoutNo, "CDK-BBBB-2222", "第二次")
	require.NoError(t, err)

	hist, err := loadSupersededSecrets(context.Background(), gdb, p.PayoutNo)
	require.NoError(t, err)
	require.Len(t, hist, 1, "被顶替掉的那一串必须留在只增不改的履历里")
	assert.Equal(t, 1, hist[0].Seq)
	assert.Equal(t, "CDK-AAAA-1111", hist[0].Secret,
		"争议的原话永远是「我用的那串码失效了」,回答它需要看到当初发出去的那一串")
	// 备注在撤销那一步就被清掉了(它是账面的一部分,不是密文),
	// 因此归档里是空的 —— 它的去处是 unfulfill 那条不可删除的 Event 的
	// cleared_note 字段。这里如实断言,而不是假装归档保住了它。
	assert.Empty(t, hist[0].Note)
	assert.EqualValues(t, 7, hist[0].SupersededBy)

	// 当前行上是第二串,两者互不覆盖。
	after, err := openPrizeSecret(reloadPayout(t, gdb, p.PayoutNo).SecretNonce,
		reloadPayout(t, gdb, p.PayoutNo).SecretCipher, p.PayoutNo, 0)
	require.NoError(t, err)
	assert.Equal(t, "CDK-BBBB-2222", after)
}

// sealAndStore 走与 handleFulfillPrize 同一条 CAS,返回受影响行数。
func sealAndStore(t *testing.T, gdb *gorm.DB, payoutNo, secret, note string) (int64, error) {
	t.Helper()
	nonce, cipher, keyVersion, err := sealPrizeSecret(secret, payoutNo)
	if err != nil {
		return 0, err
	}
	res := gdb.Model(&Payout{}).
		Where("payout_no = ? AND kind = ? AND fulfilled_at = 0", payoutNo, PayoutText).
		Updates(map[string]any{
			"fulfilled_at": common.GetTimestamp(), "fulfilled_by": 7, "fulfill_note": note,
			"secret_nonce": nonce, "secret_cipher": cipher, "secret_key_version": keyVersion,
		})
	return res.RowsAffected, res.Error
}

// 已结束的活动同样要被持续复核。
//
// reconcileActivity 只扫 published/locked/settling,而"历史公正查询"的全部内容
// 恰恰是已结束的那些:改一个 win_ppm 之后,所有拿着这份证据链跑验证脚本的用户
// 都会算出一份对不上的名单并判 FAIL —— 平台看起来像在作弊,而平台侧零信号。
func TestSpecDriftIsAuditedOnFinishedActivities(t *testing.T) {
	gdb := textEnv(t)
	t.Cleanup(func() { specAuditCursor = 0 })
	specAuditCursor = 0

	prizes := []Prize{{Tier: 1, Name: "头奖", AmountQuota: 3000, Count: 1,
		PrizeType: PrizeTypeQuota, WinPpm: 100000}}
	lines := []string{prizeSpecLineOf(AlgoV2, prizes[0])}
	act := seedActivity(t, gdb, func(a *Activity) {
		a.Status = StatusFinished
		a.Outcome = OutcomeDrawn
		a.Kind = KindDraw
		a.Algo = AlgoV2
		a.DrawMode = DrawModeProb
		a.SpecHash = SpecHashV2(lines)
		a.SpecText = strings.Join(lines, SEP)
	})
	prizes[0].ActId = act.Id
	require.NoError(t, gdb.Create(&prizes).Error)

	// 没被动过时不该误报。
	auditSpecDrift(context.Background(), gdb)
	var clean []Flag
	require.NoError(t, gdb.Where("act_id = ? AND code = ?", act.Id, FlagSpecDrift).
		Find(&clean).Error)
	require.Empty(t, clean)

	require.NoError(t, gdb.Model(&Prize{}).Where("act_id = ?", act.Id).
		Update("win_ppm", 900000).Error)
	auditSpecDrift(context.Background(), gdb)

	var flags []Flag
	require.NoError(t, gdb.Where("act_id = ? AND code = ?", act.Id, FlagSpecDrift).
		Find(&flags).Error)
	assert.NotEmpty(t, flags, "已结束活动的奖档漂移必须能被扫到")
}
