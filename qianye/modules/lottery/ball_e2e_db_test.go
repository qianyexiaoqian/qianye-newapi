package lottery

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
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

// ball_e2e_db_test.go —— 一整期双色球走完流程,然后从零复算开奖号与中奖档位。
//
// # 为什么不能只靠 ball_golden_test.go
//
// 黄金向量证明的是 `BallDraw` 这个纯函数与验证脚本一致。但验证者拿到的不是那个
// 函数,是**证据链端点吐出来的那份 JSON**:号池四元组有没有真的进承诺、开奖号
// 有没有落库、选号有没有原样进哈希链、平台公布的 `ball_result` 是不是真的等于
// 用公开种子摇出来的那一组 —— 每一条都能在纯函数全绿的前提下坏掉。
//
// 所以这里:建活动(号池 + 池子)→ 发布(生成承诺)→ 四个人各自选号报名 →
// 封盘 → 开奖 → 打真实的匿名证据链端点 → 用一份**不调用本模块任何函数**的
// 实现重摇号、重定档,与公布值逐位比对。
//
// 重算那一段只用标准库,与 qianye/docs/lottery-verify.py 是同一套编码,也与
// web/src/features/qy/pages/lottery/lib/verify.ts 新增的 `verifyBallResult`
// 走同一条判据 —— 三方在这条测试里被同时钉住。

// recomputeBallFromProof 是第三方验证者会写的那二十行:从证据链里的
// `final_seed` 原料重摇一次号,再按各档门槛给每一张票定档。
//
// 与 BallDraw / MatchTier 共用任何代码都会让断言退化成"实现与自己一致",
// 所以这里连 SEP 都重新写一遍字面量。
func recomputeBallFromProof(t *testing.T, doc *proofDocument) (string, map[string]int) {
	t.Helper()
	const sep = "\x1f"

	// ── final_seed:与 proof_e2e 那份逐字相同的推导 ──
	sum := sha256.Sum256([]byte(strings.Join([]string{
		"qylot-final-v1", doc.ActNo, doc.Seed, doc.RosterHash,
		strconv.Itoa(doc.RosterCount), doc.Algo,
	}, sep)))
	key := sum[:]

	draw := func(color string, poolN, pickK int) []int {
		type scored struct {
			n int
			h string
		}
		all := make([]scored, 0, poolN)
		for n := 1; n <= poolN; n++ {
			mac := hmac.New(sha256.New, key)
			mac.Write([]byte(strings.Join([]string{
				"qylot-ball-v2", doc.ActNo, color, strconv.Itoa(n),
			}, sep)))
			all = append(all, scored{n: n, h: hex.EncodeToString(mac.Sum(nil))})
		}
		sort.SliceStable(all, func(i, j int) bool {
			if all[i].h != all[j].h {
				return all[i].h < all[j].h
			}
			return all[i].n < all[j].n
		})
		out := make([]int, 0, pickK)
		for i := 0; i < pickK && i < len(all); i++ {
			out = append(out, all[i].n)
		}
		sort.Ints(out)
		return out
	}

	reds := draw("red", doc.BallRedPool, doc.BallRedPick)
	blues := draw("blue", doc.BallBluePool, doc.BallBluePick)

	pad := func(ns []int) string {
		parts := make([]string, 0, len(ns))
		for _, n := range ns {
			parts = append(parts, strconv.Itoa(n/10)+strconv.Itoa(n%10))
		}
		return strings.Join(parts, ",")
	}
	result := pad(reds) + "|" + pad(blues)

	// ── 定档:tier 升序、命中即停、一票只中一档 ──
	type need struct{ tier, red, blue int }
	needs := make([]need, 0, len(doc.Spec))
	for _, s := range doc.Spec {
		needs = append(needs, need{tier: s.Tier, red: s.RedMatch, blue: s.BlueMatch})
	}
	sort.SliceStable(needs, func(i, j int) bool { return needs[i].tier < needs[j].tier })

	inSet := func(list []int, n int) bool {
		for _, x := range list {
			if x == n {
				return true
			}
		}
		return false
	}
	countHits := func(drawn []int, raw string) int {
		hits := 0
		for _, field := range strings.Split(raw, ",") {
			n, err := strconv.Atoi(strings.TrimSpace(field))
			if err != nil {
				continue
			}
			if inSet(drawn, n) {
				hits++
			}
		}
		return hits
	}

	winners := make(map[string]int, len(doc.Entries))
	for _, e := range doc.Entries {
		if e.Status != EntrySuccess {
			continue
		}
		parts := strings.SplitN(e.Pick, "|", 2)
		if len(parts) != 2 {
			continue
		}
		matchRed := countHits(reds, parts[0])
		matchBlue := countHits(blues, parts[1])
		for _, nd := range needs {
			if matchRed >= nd.red && matchBlue >= nd.blue {
				winners[e.EntryNo] = nd.tier
				break
			}
		}
	}
	return result, winners
}

// TestBallRound_ResultAndTiersAreIndependentlyReproducible 是双色球的端到端公正性。
func TestBallRound_ResultAndTiersAreIndependentlyReproducible(t *testing.T) {
	gin.SetMode(gin.TestMode)
	gdb := newPayoutEnv(t, config.Lottery{
		Enabled:                true,
		PayoutMaxAttempts:      8,
		EntryCloseGraceSeconds: 0,
		RevealDelaySeconds:     0,
		MaxStakeQuota:          5_000_000,
	})

	// 号池取 12 选 3 / 4 选 1:小到可以在这条测试里人工穷举,大到四个奖级
	// 都有非零概率。号池进承诺,所以它同时是"运营不能靠调号码空间改概率"的
	// 那份原像。
	const redPool, redPick = 12, 3
	const bluePool, bluePick = 4, 1

	now := common.GetTimestamp()
	specs := []PrizeSpec{
		{Tier: 1, Name: "一等奖", PrizeType: PrizeTypeQuota, RedMatch: 3, BlueMatch: 1, PoolShareBps: 6000},
		{Tier: 2, Name: "二等奖", PrizeType: PrizeTypeQuota, RedMatch: 3, BlueMatch: 0, PoolShareBps: 2000},
		{Tier: 3, Name: "三等奖", PrizeType: PrizeTypeQuota, RedMatch: 2, BlueMatch: 1, AmountQuota: 500, Count: 20},
		{Tier: 4, Name: "四等奖", PrizeType: PrizeTypeQuota, RedMatch: 1, BlueMatch: 0, AmountQuota: 50, Count: 100},
	}
	specLines := make([]string, 0, len(specs))
	for _, s := range specs {
		specLines = append(specLines, PrizeSpecLineV2(s))
	}

	act := seedActivity(t, gdb, func(a *Activity) {
		a.Title = "双色球第 1 期"
		a.Status = StatusDraft
		a.Kind = KindDraw
		a.Algo = AlgoV2
		a.DrawMode = DrawModeBall
		// 双色球强制不去重:每张票独立中奖,那是"概率严格等于组合数"的前提。
		a.AllowMultiWin = true
		a.OpenAt = now - 3600
		a.CloseAt = now - 2
		a.DrawAt = now - 1
		a.RulesText = `{"min_quota":0}`
		a.RulesHash = RulesHash(`{"min_quota":0}`)
		a.SeriesNo = "LS-e2e"
		a.IssueNo = 1
		a.PoolSeedQuota = 100_000
		a.PoolCarryQuota = 0
		a.PoolOpenQuota = 100_000
		a.PoolShareBps = 7000
		a.BallRedPool = redPool
		a.BallRedPick = redPick
		a.BallBluePool = bluePool
		a.BallBluePick = bluePick
		// 双色球恒是 lot-v2:v2 的 spec 域前缀与 v1 不同,用错的话
		// checkSpecIntegrity 会在开奖那一刻把这场活动挂起。
		a.SpecHash = SpecHashFor(AlgoV2, specLines)
		a.SpecText = strings.Join(specLines, SEP)
		a.CommitHash = ""
	})
	require.NoError(t, gdb.Create(&Seed{
		ActId: act.Id, Seed: newSecret(), RefSalt: newSecret(), IpSalt: newSecret(),
		CreatedAt: now,
	}).Error)
	prizes := make([]Prize, 0, len(specs))
	for _, s := range specs {
		prizes = append(prizes, Prize{
			ActId: act.Id, Tier: s.Tier, Name: s.Name, PrizeType: PrizeTypeQuota,
			AmountQuota: s.AmountQuota, Count: s.Count,
			RedMatch: s.RedMatch, BlueMatch: s.BlueMatch, PoolShareBps: s.PoolShareBps,
		})
	}
	require.NoError(t, gdb.Create(&prizes).Error)

	// ── 发布:承诺在这一刻生成并冻结,号池四元组与池子三项都在原像里 ──
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

	// ── 报名:六个人各自选号 ──
	//
	// 号码刻意写死而不是机选:这条测试要断言的是"平台没有改我的号",
	// 而随机选号会让失败信息退化成一串看不出所以然的数字。
	salts, err := loadSalts(context.Background(), gdb, act.Id)
	require.NoError(t, err)
	picks := []struct {
		userId int
		pick   string
	}{
		{201, "01,02,03|01"},
		{202, "04,05,06|02"},
		{203, "07,08,09|03"},
		{204, "10,11,12|04"},
		{205, "01,05,09|02"},
		{206, "02,06,10|03"},
	}
	entryNos := make(map[string]string, len(picks)) // pick → entry_no
	for _, p := range picks {
		e := &Entry{
			EntryNo: newEntryNo(), ActId: act.Id, IdemKey: buildIdemKey(act.ActNo, newEntryNo()),
			UserId: p.userId, UserRef: UserRef(salts.RefSalt, p.userId),
			Amount: act.StakeQuota, Pick: p.pick,
			Status: EntryPending, OrderNo: "LE-" + newEntryNo(), CreatedAt: common.GetTimestamp(),
		}
		cur := loadAct(t, gdb, act.Id)
		require.NoError(t, gdb.Transaction(func(tx *gorm.DB) error {
			return reserveEntry(tx, cur, Rules{}, e)
		}))
		require.NoError(t, gdb.Transaction(func(tx *gorm.DB) error {
			return markEntrySuccess(tx, e.EntryNo, nil)
		}))
		entryNos[p.pick] = e.EntryNo
	}

	// ── 封盘 → 开奖 ──
	require.NoError(t, gdb.Model(&Activity{}).Where("id = ?", act.Id).
		Update("close_at", now-2).Error)
	runLock(context.Background())
	locked := loadAct(t, gdb, act.Id)
	require.Equal(t, StatusLocked, locked.Status)
	require.NotEmpty(t, locked.RosterHash, "封盘必须公开名单哈希,而且它先于种子")
	require.Empty(t, locked.BallResult, "封盘时还不能有开奖号 —— 名单必须先于号码冻结")

	runReveal(context.Background())
	drawn := loadAct(t, gdb, act.Id)
	require.Equal(t, StatusSettling, drawn.Status)
	require.Equal(t, OutcomeDrawn, drawn.Outcome)
	require.NotEmpty(t, drawn.BallResult, "开奖之后必须公布开奖号")

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
		Message string        `json:"message"`
	}
	require.NoError(t, common.Unmarshal(rec.Body.Bytes(), &envelope))
	require.True(t, envelope.Success)
	doc := &envelope.Data

	// 证据链必须自洽:验证者拿到这一份 JSON 就能离线复算,不需要再访问本站。
	require.NotEmpty(t, doc.Seed, "开奖之后种子必须公开,否则没人能复算")
	require.Equal(t, DrawModeBall, doc.DrawMode)
	require.Equal(t, redPool, doc.BallRedPool, "号池不下发,第三方连摇都摇不了")
	require.Equal(t, redPick, doc.BallRedPick)
	require.Equal(t, bluePool, doc.BallBluePool)
	require.Equal(t, bluePick, doc.BallBluePick)
	require.Equal(t, drawn.BallResult, doc.BallResult)
	require.Len(t, doc.Entries, len(picks))
	for _, p := range picks {
		found := false
		for _, e := range doc.Entries {
			if e.EntryNo == entryNos[p.pick] {
				assert.Equal(t, p.pick, e.Pick,
					"选号必须原样进证据链 —— 它进哈希链,改一个数字整条链就断")
				found = true
			}
		}
		assert.True(t, found, "报名条目 %s 不在证据链里", p.pick)
	}

	// ── ① 独立复算开奖号与档位 ──
	independentResult, independentTiers := recomputeBallFromProof(t, doc)
	assert.Equal(t, doc.BallResult, independentResult,
		"独立复算的开奖号必须与平台公布的逐字一致 —— 这一条不成立时,"+
			"平台可以在开奖后把号码改成任何一组而没有任何自动化环节会报红")

	systemTiers := make(map[string]int, len(doc.Winners))
	for _, w := range doc.Winners {
		systemTiers[w.EntryNo] = w.Tier
	}
	assert.Equal(t, independentTiers, systemTiers,
		"独立复算的中奖名单(谁中了、中的第几档)必须与系统公布的逐条一致")

	// ── ② 后端原语与端点公布值一致 ──
	//
	// 上面那一份是"第三方视角",这一份是"本模块自己算的",两者都必须等于
	// 落库的那个串。少了这一条,一次 BallResultText 的格式漂移会同时骗过
	// 复算(它自己也 pad 两位)与落库值。
	final := FinalSeed(drawn.ActNo, doc.Seed, drawn.RosterHash, drawn.RosterCount, drawn.Algo)
	reds := BallDraw(final, drawn.ActNo, "red", redPool, redPick)
	blues := BallDraw(final, drawn.ActNo, "blue", bluePool, bluePick)
	assert.Equal(t, doc.BallResult, BallResultText(reds, blues))
	assert.Len(t, reds, redPick)
	assert.Len(t, blues, bluePick)

	// ── ③ 「我为什么没中」必须能被算出来 ──
	//
	// 用户侧那个弹窗给出的正是这三个数(开奖号 / 我的号 / 命中几个),而它们
	// 全部来自证据链里已有的字段。任何一个人 —— 中了或没中 —— 都必须能算出
	// 自己的档位,包括 tier=0 这一档:"没中"是一等公民结果,不是查不到。
	explained := 0
	for _, e := range doc.Entries {
		if e.Status != EntrySuccess {
			continue
		}
		tier, matchRed, matchBlue := MatchTier(reds, blues, e.Pick, specsToBallTiers(specs))
		assert.Equal(t, systemTiers[e.EntryNo], tier,
			"票 %s(选号 %s,命中红 %d 蓝 %d)算出的档位与公布名单不一致",
			e.EntryNo, e.Pick, matchRed, matchBlue)
		assert.True(t, matchRed >= 0 && matchRed <= redPick)
		assert.True(t, matchBlue >= 0 && matchBlue <= bluePick)
		explained++
	}
	assert.Equal(t, len(picks), explained,
		"每一张票都必须能自证中没中,包括没中的那些")

	// 把三方复算所需的全部原料打出来:任何人可以拿这几行去 lottery-verify.py
	// 或浏览器控制台重跑一遍。测试自己不打这些数字的话,"三方一致"只能靠信任。
	t.Logf("act_no=%s algo=%s draw_mode=%s", doc.ActNo, doc.Algo, doc.DrawMode)
	t.Logf("seed=%s roster_hash=%s roster_count=%d", doc.Seed, doc.RosterHash, doc.RosterCount)
	t.Logf("final_seed=%s", final)
	t.Logf("pool=红%d选%d 蓝%d选%d", redPool, redPick, bluePool, bluePick)
	t.Logf("ball_result(平台公布)=%s", doc.BallResult)
	t.Logf("ball_result(独立复算)=%s", independentResult)
	t.Logf("ball_result(本模块原语)=%s", BallResultText(reds, blues))
	for _, e := range doc.Entries {
		tier, mr, mb := MatchTier(reds, blues, e.Pick, specsToBallTiers(specs))
		t.Logf("  票 %s 选号 %s 命中红%d蓝%d → tier=%d(平台公布 tier=%d)",
			e.EntryNo, e.Pick, mr, mb, tier, systemTiers[e.EntryNo])
	}

	// 没中的人在 winners 里查不到自己,而 MatchTier 会给出 0 —— 这两件事
	// 必须同时成立,否则"我为什么没中"只能得到一句"查无此人"。
	for _, e := range doc.Entries {
		if _, won := systemTiers[e.EntryNo]; won {
			continue
		}
		tier, _, _ := MatchTier(reds, blues, e.Pick, specsToBallTiers(specs))
		assert.Zero(t, tier, "不在中奖名单里的票必须复算出 tier=0")
	}
}

// specsToBallTiers 把奖档规格折成定档用的门槛表。
func specsToBallTiers(specs []PrizeSpec) []BallTier {
	out := make([]BallTier, 0, len(specs))
	for _, s := range specs {
		out = append(out, BallTier{
			Tier: s.Tier, RedMatch: s.RedMatch, BlueMatch: s.BlueMatch,
			Count: s.Count, Amount: s.AmountQuota, PoolShareBps: s.PoolShareBps,
			PrizeType: s.PrizeType,
		})
	}
	return out
}
