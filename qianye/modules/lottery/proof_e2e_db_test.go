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

// proof_e2e_db_test.go —— 一整场抽奖走完全流程,然后**从零复算**中奖名单。
//
// # 为什么这一条必须端到端
//
// 公正性的全部价值在于"任何人都能自己算一遍并得到同一个结果"。逐个纯函数的
// 单测证明不了这件事:它们只能说明每一步与自己一致,而验证者拿到的是**证据链
// 端点吐出来的那份 JSON**。字段名写错一个、种子在该揭示时没揭示、名单顺序与
// 哈希用的不是同一份——每一种都会让线上那份 proof 谁都验不了,而纯函数测试
// 全绿。
//
// 所以这里:建活动 → 发布(生成承诺)→ 逐条报名 → 到点封盘 → 到点开奖 →
// 打真实的 HTTP 端点取 proof → 用一份**不调用本模块任何函数**的实现重算名单。
// 重算那一段刻意只用标准库,与 qianye/docs/lottery-verify.py 是同一套编码。

// recomputeWinnersFromProof 是一份独立实现:只用标准库,不碰本模块的任何函数。
//
// 它就是第三方验证者会写的那三十行。与 PickWinners 共用任何代码都会让这条
// 断言退化成"实现与自己一致"。
func recomputeWinnersFromProof(t *testing.T, doc *proofDocument) []string {
	t.Helper()
	const sep = "\x1f"

	type row struct{ entryNo, userRef string }
	roster := make([]row, 0, len(doc.Entries))
	lines := make([]string, 0, len(doc.Entries))
	for _, e := range doc.Entries {
		if e.Status != EntrySuccess {
			continue
		}
		roster = append(roster, row{entryNo: e.EntryNo, userRef: e.UserRef})
		lines = append(lines, strings.Join([]string{
			e.EntryNo, e.UserRef, strconv.Itoa(e.OptNo), strconv.FormatInt(e.Amount, 10),
		}, sep))
	}
	sort.Slice(roster, func(i, j int) bool { return roster[i].entryNo < roster[j].entryNo })
	sort.Strings(lines)

	sum := sha256.Sum256([]byte(strings.Join(append([]string{
		"qylot-roster-v1", doc.ActNo, doc.CommitHash, strconv.Itoa(len(roster)),
	}, strings.Join(lines, "\n")), sep)))
	rosterHash := hex.EncodeToString(sum[:])
	require.Equal(t, doc.RosterHash, rosterHash, "独立复算的名单哈希必须与公开值一致")

	sum = sha256.Sum256([]byte(strings.Join([]string{
		"qylot-final-v1", doc.ActNo, doc.Seed, doc.RosterHash,
		strconv.Itoa(doc.RosterCount), doc.Algo,
	}, sep)))
	key, err := hex.DecodeString(hex.EncodeToString(sum[:]))
	require.NoError(t, err)

	tickets := make(map[string]string, len(roster))
	for _, r := range roster {
		mac := hmac.New(sha256.New, key)
		mac.Write([]byte(strings.Join([]string{"qylot-ticket-v1", doc.ActNo, r.entryNo}, sep)))
		tickets[r.entryNo] = hex.EncodeToString(mac.Sum(nil))
	}
	sort.SliceStable(roster, func(i, j int) bool {
		if tickets[roster[i].entryNo] != tickets[roster[j].entryNo] {
			return tickets[roster[i].entryNo] < tickets[roster[j].entryNo]
		}
		return roster[i].entryNo < roster[j].entryNo
	})
	if !doc.AllowMultiWin {
		seen := make(map[string]bool, len(roster))
		uniq := make([]row, 0, len(roster))
		for _, r := range roster {
			if seen[r.userRef] {
				continue
			}
			seen[r.userRef] = true
			uniq = append(uniq, r)
		}
		roster = uniq
	}

	spec := make([]proofSpecItem, len(doc.Spec))
	copy(spec, doc.Spec)
	sort.SliceStable(spec, func(i, j int) bool { return spec[i].Tier < spec[j].Tier })

	out := make([]string, 0, len(roster))
	idx := 0
	for _, s := range spec {
		for n := 0; n < s.Count; n++ {
			if idx >= len(roster) {
				break // 票不够则该档空缺,绝不补抽
			}
			out = append(out, roster[idx].entryNo)
			idx++
		}
	}
	return out
}

func TestProofEndpoint_WinnersAreIndependentlyReproducible(t *testing.T) {
	gin.SetMode(gin.TestMode)
	gdb := newPayoutEnv(t, config.Lottery{
		Enabled:                true,
		PayoutMaxAttempts:      8,
		EntryCloseGraceSeconds: 0,
		RevealDelaySeconds:     0,
		MaxStakeQuota:          5_000_000,
	})

	// 四个时刻直接落在"刚刚过去"的位置:封盘与开奖只由时间触发,而承诺哈希
	// 覆盖的正是这四个值。报名那一步会临时把 close_at 推到未来(只为了让
	// reserveEntry 的窗口判定放行),随后原样还回**被承诺的那份值** ——
	// 于是开奖前的承诺校验走的仍然是真实路径,而不是被测试绕过去的。
	now := common.GetTimestamp()
	act := seedActivity(t, gdb, func(a *Activity) {
		a.Status = StatusDraft
		a.Kind = KindDraw
		a.AllowMultiWin = false
		a.OpenAt = now - 3600
		a.CloseAt = now - 2
		a.DrawAt = now - 1
		a.RulesText = `{"min_quota":0}`
		a.RulesHash = RulesHash(`{"min_quota":0}`)
		// 这一场刻意留在 lot-v1:它同时是 v1 证据链的回归护栏 ——
		// 只要有人不小心动了 v1 的原像,这条端到端就会当场变红。
		a.Algo = AlgoV1
		specLines := []string{
			prizeSpecLineV1(1, "头奖", 5000, 1),
			prizeSpecLineV1(2, "二奖", 1000, 2),
		}
		a.SpecHash = SpecHash(specLines)
		// spec_text 就是进哈希的那份字节。开奖前 checkSpecIntegrity 会从奖档表
		// 重算并同时比对两者 —— 少写这一列等于在测试里绕开那道校验。
		a.SpecText = strings.Join(specLines, SEP)
		a.CommitHash = ""
	})
	require.NoError(t, gdb.Create(&Seed{
		ActId: act.Id, Seed: newSecret(), RefSalt: newSecret(), IpSalt: newSecret(),
		CreatedAt: now,
	}).Error)
	require.NoError(t, gdb.Create(&[]Prize{
		{ActId: act.Id, Tier: 1, Name: "头奖", AmountQuota: 5000, Count: 1},
		{ActId: act.Id, Tier: 2, Name: "二奖", AmountQuota: 1000, Count: 2},
	}).Error)

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

	// ── 报名:六个人,其中一个人两张票(用来验去重),外加一条失败条目 ──
	salts, err := loadSalts(context.Background(), gdb, act.Id)
	require.NoError(t, err)
	userIds := []int{101, 102, 103, 104, 105, 101}
	for _, uid := range userIds {
		e := &Entry{
			EntryNo: newEntryNo(), ActId: act.Id, IdemKey: buildIdemKey(act.ActNo, newEntryNo()),
			UserId: uid, UserRef: UserRef(salts.RefSalt, uid), Amount: act.StakeQuota,
			Status: EntryPending, OrderNo: "LE-" + newEntryNo(), CreatedAt: common.GetTimestamp(),
		}
		// 活动行在事务**之外**读:内存库只有一条连接,事务里再开一次查询会
		// 自己把自己饿死(线上的 reserveEntry 也是拿调用方读好的活动进来的)。
		cur := loadAct(t, gdb, act.Id)
		require.NoError(t, gdb.Transaction(func(tx *gorm.DB) error {
			return reserveEntry(tx, cur, Rules{}, e, 0)
		}))
		require.NoError(t, gdb.Transaction(func(tx *gorm.DB) error {
			return markEntrySuccess(tx, e.EntryNo, nil)
		}))
	}
	failed := &Entry{
		EntryNo: newEntryNo(), ActId: act.Id, IdemKey: buildIdemKey(act.ActNo, newEntryNo()),
		UserId: 106, UserRef: UserRef(salts.RefSalt, 106), Amount: act.StakeQuota,
		Status: EntryPending, CreatedAt: common.GetTimestamp(),
	}
	curForFail := loadAct(t, gdb, act.Id)
	require.NoError(t, gdb.Transaction(func(tx *gorm.DB) error {
		return reserveEntry(tx, curForFail, Rules{}, failed, 0)
	}))
	require.NoError(t, gdb.Transaction(func(tx *gorm.DB) error {
		return markEntryFailed(tx, failed.EntryNo, "qy_lot_insufficient_quota")
	}))

	// ── 封盘 → 开奖(都只由时间触发)──
	//
	// close_at 还回被承诺的那份值。改回去之后 revealActivity 的承诺校验才可能
	// 通过 —— 校验的是完整原像,四个时刻里改掉任何一个都会被它当场拒绝。
	require.NoError(t, gdb.Model(&Activity{}).Where("id = ?", act.Id).
		Update("close_at", now-2).Error)
	runLock(context.Background())
	locked := loadAct(t, gdb, act.Id)
	require.Equal(t, StatusLocked, locked.Status)
	require.NotEmpty(t, locked.RosterHash, "封盘必须公开名单哈希,而且它先于种子")

	runReveal(context.Background())
	drawn := loadAct(t, gdb, act.Id)
	require.Equal(t, StatusSettling, drawn.Status)
	require.Equal(t, OutcomeDrawn, drawn.Outcome)

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

	require.NotEmpty(t, doc.Seed, "开奖之后种子必须公开,否则没人能复算")
	require.Equal(t, int64(len(userIds)+1), doc.Total, "失败条目也必须留在证据链里")
	require.Len(t, doc.Entries, int(doc.Total))

	// ── 从零复算,与系统公布的名单逐位比对 ──
	system := make([]string, 0, len(doc.Winners))
	sorted := make([]proofWinner, len(doc.Winners))
	copy(sorted, doc.Winners)
	sort.SliceStable(sorted, func(i, j int) bool { return sorted[i].Pos < sorted[j].Pos })
	for _, w := range sorted {
		system = append(system, w.EntryNo)
	}
	independent := recomputeWinnersFromProof(t, doc)

	assert.Equal(t, independent, system, "独立复算的中奖名单必须与系统公布的逐位一致")
	assert.Len(t, system, 3, "一等奖 1 位 + 二等奖 2 位")

	// 同一个人的两张票在 allow_multi_win=false 下只能中一次。
	refs := make(map[string]bool, len(sorted))
	for _, w := range sorted {
		assert.False(t, refs[w.UserRef], "不允许多中时,同一个参与者标识不能出现两次")
		refs[w.UserRef] = true
	}

	// 把这一份原样打出来:它就是验证者拿到的那份文件,可以直接喂给
	// qianye/docs/lottery-verify.py 复核。
	line, err := common.Marshal(doc)
	require.NoError(t, err)
	t.Log("PROOF_JSON " + string(line))
}
