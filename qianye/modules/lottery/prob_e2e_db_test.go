package lottery

import (
	"context"
	"net/http"
	"net/http/httptest"
	"sort"
	"strconv"
	"strings"
	"testing"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/qianye/config"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

// prob_e2e_db_test.go —— 一整场**概率制 + 混合奖档**的抽奖走完全流程,
// 然后从证据链端点吐出来的那份 JSON 从零复算。
//
// 与 proof_e2e_db_test.go 的分工:那一份守 lot-v1 的名次制,这一份守 lot-v2 的
// 概率制与文本奖。两份都必须存在 —— 逐个纯函数的单测证明不了"线上那份 proof
// 谁都能验",因为验证者拿到的是端点吐出来的那份 JSON,而字段名写错一个、
// 概率表没下发、文本奖没进 winners,每一种都会让它验不了,而纯函数测试全绿。

// TestProbDrawIsReproducibleFromTheProofEndpoint 建活动 → 发布 → 报名 → 封盘 →
// 开奖 → 取 proof → 独立复算。
func TestProbDrawIsReproducibleFromTheProofEndpoint(t *testing.T) {
	gin.SetMode(gin.TestMode)
	gdb := newPayoutEnv(t, config.Lottery{
		Enabled:                true,
		PayoutMaxAttempts:      8,
		EntryCloseGraceSeconds: 0,
		RevealDelaySeconds:     0,
		MaxStakeQuota:          5_000_000,
	})

	now := common.GetTimestamp()
	// 奖档:一档额度(必中区间的一半)、一档文本(另一半)。
	// 两档合计 100% ppm,于是每一张票都会中其中一档 —— 这样断言才有内容,
	// 而"落在全部区间之外"那一支由 fairness_v2_test.go 覆盖。
	prizes := []Prize{
		{Tier: 1, Name: "额度奖", AmountQuota: 2000, Count: 50,
			PrizeType: PrizeTypeQuota, WinPpm: 500000},
		{Tier: 2, Name: "兑换码", AmountQuota: 0, Count: 3,
			PrizeType: PrizeTypeText, WinPpm: 500000, TextDesc: "请在 8 月 31 日前联系客服领取"},
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
		// 概率制强制不去重:均分制的全部主张是"每张票独立、概率严格等于公示值"。
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

	// ── 发布:承诺在这一刻生成并冻结 ──
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

	// ── 报名:20 张票 ──
	salts, err := loadSalts(context.Background(), gdb, act.Id)
	require.NoError(t, err)
	for uid := 201; uid < 221; uid++ {
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

	// ── 封盘 → 开奖 ──
	require.NoError(t, gdb.Model(&Activity{}).Where("id = ?", act.Id).
		Update("close_at", now-2).Error)
	runLock(context.Background())
	locked := loadAct(t, gdb, act.Id)
	require.Equal(t, StatusLocked, locked.Status)
	require.NotEmpty(t, locked.RosterHash, "封盘必须公开名单哈希,而且它先于种子")

	runReveal(context.Background())
	drawn := loadAct(t, gdb, act.Id)
	require.Equal(t, StatusSettling, drawn.Status, "承诺校验必须走真实路径并通过")
	require.Equal(t, OutcomeDrawn, drawn.Outcome)
	require.Positive(t, drawn.TextGrantCount, "文本奖的中奖位数必须与派奖计划同事务落库")

	// 两条派奖腿的落点:额度走 planned,文本一步到位落 granted。
	var planned, granted int64
	require.NoError(t, gdb.Model(&Payout{}).
		Where("act_id = ? AND kind = ? AND status = ?", act.Id, PayoutPrize, PayoutPlanned).
		Count(&planned).Error)
	require.NoError(t, gdb.Model(&Payout{}).
		Where("act_id = ? AND kind = ? AND status = ?", act.Id, PayoutText, PayoutGranted).
		Count(&granted).Error)
	assert.EqualValues(t, 20, planned+granted,
		"两档合计 100% 概率,20 张票必须一张不落地各中一档 —— "+
			"文本奖被 PlanPayouts 静默丢掉正是这条断言要抓的事故")
	assert.EqualValues(t, drawn.TextGrantCount, granted)

	// ── 先履行一份文本奖,再去取证据链 ──
	//
	// 顺序是这条断言的全部内容。在履行**之前**取 proof 然后断言"响应里没有
	// secret",库里根本还不存在密文与备注,断言必然通过 —— 它挡不住
	// "给 Payout.SecretCipher 加了个 json tag"这一类改动(空值配 omitempty 时
	// key 也不出现)。所以先塞两个哨兵串进去,再断言这两个**具体的字节序列**
	// 一个都没出现在匿名文件里。
	const (
		secretCanary = "CDK-CANARY-7731"
		noteCanary   = "NOTE-CANARY-8842"
	)
	var oneText Payout
	require.NoError(t, gdb.Where("act_id = ? AND kind = ?", act.Id, PayoutText).
		Take(&oneText).Error)
	nonce, cipher, keyVer, err := sealPrizeSecret(secretCanary, oneText.PayoutNo)
	require.NoError(t, err)
	require.NoError(t, gdb.Model(&Payout{}).Where("id = ?", oneText.Id).
		Updates(map[string]any{
			"fulfilled_at": common.GetTimestamp(), "fulfilled_by": 9001,
			"fulfill_note": noteCanary,
			"secret_nonce": nonce, "secret_cipher": cipher, "secret_key_version": keyVer,
		}).Error)

	// ── 打真实的匿名端点取证据链 ──
	router := gin.New()
	router.GET("/lottery/public/:act_no/proof", handleGetProof)
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, httptest.NewRequest(http.MethodGet,
		"/lottery/public/"+act.ActNo+"/proof?page_size=1000", nil))
	require.Equal(t, http.StatusOK, rec.Code)

	var envelope struct {
		Success bool          `json:"success"`
		Data    proofDocument `json:"data"`
	}
	require.NoError(t, common.Unmarshal(rec.Body.Bytes(), &envelope))
	require.True(t, envelope.Success)
	doc := &envelope.Data

	require.NotEmpty(t, doc.Seed, "开奖之后种子必须公开,否则没人能复算")
	require.Equal(t, DrawModeProb, doc.DrawMode, "定档方式必须下发,否则验证者不知道该用哪把尺子")
	require.Len(t, doc.Entries, int(doc.Total))

	// 概率表必须在证据链里。它进 spec 原像 → spec_hash → commit_hash,
	// 不下发就等于第一步都算不了。
	byTier := map[int]proofSpecItem{}
	for _, s := range doc.Spec {
		byTier[s.Tier] = s
	}
	assert.Equal(t, 500000, byTier[1].WinPpm)
	assert.Equal(t, PrizeTypeQuota, byTier[1].PrizeType)
	assert.Equal(t, PrizeTypeText, byTier[2].PrizeType)
	assert.Equal(t, "请在 8 月 31 日前联系客服领取", byTier[2].TextDesc,
		"文本奖的**公开说明**要下发,它本来就要展示给所有人")

	// 兑换码本体与履行备注一个字节都不能出现在这份匿名文件里。
	// 两个哨兵串在上面已经真的写进库了 —— 这条断言因此不是空跑。
	body := rec.Body.String()
	assert.NotContains(t, body, secretCanary, "兑换码明文泄漏进了匿名证据链")
	assert.NotContains(t, body, noteCanary, "履行备注泄漏进了匿名证据链")
	assert.NotContains(t, body, "secret")
	assert.NotContains(t, body, "fulfill_note")

	// ── 从零复算,与系统公布的名单逐位比对 ──
	system := make([]string, 0, len(doc.Winners))
	ordered := make([]proofWinner, len(doc.Winners))
	copy(ordered, doc.Winners)
	sort.SliceStable(ordered, func(i, j int) bool { return ordered[i].Pos < ordered[j].Pos })
	for _, w := range ordered {
		system = append(system, w.EntryNo+"@"+strconv.Itoa(w.Tier)+"@"+strconv.FormatInt(w.Amount, 10))
	}

	roster := make([]RosterLine, 0, len(doc.Entries))
	for _, e := range doc.Entries {
		if e.Status != EntrySuccess {
			continue
		}
		roster = append(roster, RosterLine{
			EntryNo: e.EntryNo, UserRef: e.UserRef, OptNo: e.OptNo, Amount: e.Amount, Pick: e.Pick,
		})
	}
	sort.SliceStable(roster, func(i, j int) bool { return roster[i].EntryNo < roster[j].EntryNo })

	spec := make([]PrizeSpec, 0, len(doc.Spec))
	for _, s := range doc.Spec {
		spec = append(spec, PrizeSpec{
			Tier: s.Tier, Name: s.Name, PrizeType: s.PrizeType,
			AmountQuota: s.AmountQuota, Count: s.Count, WinPpm: s.WinPpm, TextDesc: s.TextDesc,
		})
	}
	final := FinalSeed(doc.ActNo, doc.Seed, doc.RosterHash, doc.RosterCount, doc.Algo)
	assert.Equal(t, independentProbWinners(final, doc.ActNo, roster, spec), system,
		"独立复算的中奖名单必须与系统公布的逐位一致(含金额与档位)")

	// 文本奖必须出现在 winners 里,而且只带一个 fulfilled 布尔。
	texts, fulfilled := 0, 0
	for _, w := range ordered {
		if w.PrizeType == PrizeTypeText {
			texts++
			assert.Zero(t, w.Amount)
			if w.Fulfilled {
				fulfilled++
			}
		}
	}
	assert.Equal(t, 1, fulfilled,
		"上面刚履行了一份,证据链里必须如实反映 —— 恒 false 说明这个布尔根本没接上")
	assert.EqualValues(t, granted, texts,
		"文本奖不进 winners 的话,一场混合奖档活动的复算名单会比公布的多出几位,"+
			"验证脚本报 FAIL,而真实情况是平台完全诚实")

	// 把这一份原样打出来:它就是验证者拿到的那份文件,可以直接喂给
	// qianye/docs/lottery-verify.py 复核(含 --explain)。
	line, err := common.Marshal(doc)
	require.NoError(t, err)
	t.Log("PROOF_JSON " + string(line))
}

// 概率制里**没中的那些人**同样要能被独立复算出来。
//
// 上面那一场刻意配成两档合计 100%,于是每张票都中 —— 那验的是"每一条都被判定
// 过一次",验不到"落在全部区间之外"。而"我为什么没中"才是概率制真正要回答的
// 问题:名次制下用户至少能看到一个排名,概率制下如果不能独立复算,
// "历史公正查询"就退化成了平台的一面之词。
//
// 这一场三档合计 40%,剩下 60% 是落选区间。断言的是:落选者在 winners 里
// 一个都不出现,而独立复算(只用标准库、连 HMAC 都是手写的)算出的名单
// 与系统公布的逐位一致 —— 也就是说,平台既没有多发,也没有把某个本该中的人
// 悄悄拿掉。
func TestProbLosersAreIndependentlyReproducible(t *testing.T) {
	gin.SetMode(gin.TestMode)
	gdb := newPayoutEnv(t, config.Lottery{
		Enabled:                true,
		PayoutMaxAttempts:      8,
		EntryCloseGraceSeconds: 0,
		RevealDelaySeconds:     0,
		MaxStakeQuota:          5_000_000,
	})

	now := common.GetTimestamp()
	prizes := []Prize{
		{Tier: 1, Name: "一等奖", AmountQuota: 5000, Count: 40,
			PrizeType: PrizeTypeQuota, WinPpm: 100000},
		{Tier: 2, Name: "兑换码", AmountQuota: 0, Count: 5,
			PrizeType: PrizeTypeText, WinPpm: 100000, TextDesc: "凭此码联系客服领取周边"},
		{Tier: 3, Name: "安慰奖", AmountQuota: 500, Count: 40,
			PrizeType: PrizeTypeQuota, WinPpm: 200000},
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
	for uid := 401; uid < 441; uid++ {
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
	runReveal(context.Background())
	require.Equal(t, StatusSettling, loadAct(t, gdb, act.Id).Status)

	router := gin.New()
	router.GET("/lottery/public/:act_no/proof", handleGetProof)
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, httptest.NewRequest(http.MethodGet,
		"/lottery/public/"+act.ActNo+"/proof?page_size=1000", nil))
	require.Equal(t, http.StatusOK, rec.Code)

	var envelope struct {
		Success bool          `json:"success"`
		Data    proofDocument `json:"data"`
	}
	require.NoError(t, common.Unmarshal(rec.Body.Bytes(), &envelope))
	doc := &envelope.Data

	roster := make([]RosterLine, 0, len(doc.Entries))
	for _, e := range doc.Entries {
		if e.Status != EntrySuccess {
			continue
		}
		roster = append(roster, RosterLine{
			EntryNo: e.EntryNo, UserRef: e.UserRef, OptNo: e.OptNo, Amount: e.Amount, Pick: e.Pick,
		})
	}
	sort.SliceStable(roster, func(i, j int) bool { return roster[i].EntryNo < roster[j].EntryNo })

	spec := make([]PrizeSpec, 0, len(doc.Spec))
	for _, s := range doc.Spec {
		spec = append(spec, PrizeSpec{
			Tier: s.Tier, Name: s.Name, PrizeType: s.PrizeType,
			AmountQuota: s.AmountQuota, Count: s.Count, WinPpm: s.WinPpm, TextDesc: s.TextDesc,
		})
	}

	ordered := make([]proofWinner, len(doc.Winners))
	copy(ordered, doc.Winners)
	sort.SliceStable(ordered, func(i, j int) bool { return ordered[i].Pos < ordered[j].Pos })
	system := make([]string, 0, len(ordered))
	won := make(map[string]bool, len(ordered))
	for _, w := range ordered {
		system = append(system, w.EntryNo+"@"+strconv.Itoa(w.Tier)+"@"+strconv.FormatInt(w.Amount, 10))
		won[w.EntryNo] = true
	}

	final := FinalSeed(doc.ActNo, doc.Seed, doc.RosterHash, doc.RosterCount, doc.Algo)
	require.Equal(t, independentProbWinners(final, doc.ActNo, roster, spec), system,
		"独立复算的中奖名单必须与系统公布的逐位一致")

	losers := make([]string, 0, len(roster))
	for _, l := range roster {
		if !won[l.EntryNo] {
			losers = append(losers, l.EntryNo)
		}
	}
	require.NotEmpty(t, losers,
		"三档合计只有 40%,必须有人落选 —— 全中的话这条用例验不到「为什么没中」")
	assert.Less(t, len(ordered), len(roster), "不能所有人都中")

	// 落选者的证据链形状:没有 payout、没有中奖位,而他那一行仍然完整地留在
	// entries 里并进了链与名单哈希。任何人都能拿这份文件自己算出他的 r
	// 落在全部区间之外 —— 这就是"你为什么没中"的全部答案。
	for _, no := range losers {
		for _, w := range ordered {
			require.NotEqual(t, no, w.EntryNo)
		}
	}
	t.Logf("PARTIAL_PROOF losers=%d winners=%d roster=%d", len(losers), len(ordered), len(roster))

	line, err := common.Marshal(doc)
	require.NoError(t, err)
	t.Log("PROOF_JSON_PARTIAL " + string(line))
}
