package lottery

import (
	"bufio"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"math/big"
	"net/http"
	"os"
	"sort"
	"strconv"
	"strings"
	"sync"
	"testing"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/qianye/config"
	qymodel "github.com/QuantumNous/new-api/qianye/model"
	"github.com/QuantumNous/new-api/qianye/modules/stardust"

	"github.com/gin-gonic/gin"
	"github.com/glebarez/sqlite"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
	gormlogger "gorm.io/gorm/logger"
)

// wheel_e2e_db_test.go —— 一整场星屑转盘走完全流程,再从证据链端点吐出来的那份
// JSON 从零复算。
//
// 走的全是真实 HTTP handler:建活动(none 档派生、ppmSum 断言)→ 发布(库存 = count)
// → N 次转动(真实档中奖、文本奖 granted + text_grant_count、谢谢参与、库存耗尽落空、
// 耗尽当场封盘)→ 幂等重放回原结果 / 换 client_seed 409 → 揭示 → 证据链复算全部 PASS。
//
// 每一转的落档都是**测试自己挑的**:读出种子之后按 WheelTicket 逐个试 client_seed,
// 直到摇号量落进想要的区间。这恰恰就是 D-13 里"能读到种子的人可以挑自己的下一转"
// 那条残余风险的原样 —— 在这里它是让用例确定的手段,在线上它是协议明说的不保证。

const (
	wheelAdminId = 9101 // 创建者,同时下场转一次(D-13:管理员可参与)
	wheelUserA   = 9102
	wheelUserB   = 9103
	wheelUserC   = 9104
)

const wheelStake = 100

// newWheelMainDB 建一个只装着四个 users 行的主库并接到 model.DB,同时给每人在
// **扩展库**种下 start 星屑(与 newBallMainDB 同一条纪律:钱只经账本进出)。
func newWheelMainDB(t *testing.T, ext *gorm.DB, start map[int]int64) {
	t.Helper()
	gdb, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{Logger: gormlogger.Discard})
	require.NoError(t, err)
	sqlDB, err := gdb.DB()
	require.NoError(t, err)
	sqlDB.SetMaxOpenConns(1)
	require.NoError(t, gdb.AutoMigrate(&model.User{}))

	prevType := common.MainDatabaseType()
	common.SetMainDatabaseType(common.DatabaseTypeSQLite)
	prevDB, prevLogDB := model.DB, model.LOG_DB
	prevMem, prevRedis := common.MemoryCacheEnabled, common.RedisEnabled
	prevOptions := common.OptionMap
	model.DB, model.LOG_DB = gdb, gdb
	common.OptionMap = map[string]string{}
	common.MemoryCacheEnabled, common.RedisEnabled = false, false
	t.Cleanup(func() {
		model.DB, model.LOG_DB = prevDB, prevLogDB
		common.SetMainDatabaseType(prevType)
		common.MemoryCacheEnabled, common.RedisEnabled = prevMem, prevRedis
		common.OptionMap = prevOptions
		_ = sqlDB.Close()
	})

	for uid, amount := range start {
		role := common.RoleCommonUser
		if uid == wheelAdminId {
			role = common.RoleAdminUser
		}
		require.NoError(t, gdb.Create(&model.User{
			Id: uid, Username: "wheel-" + strconv.Itoa(uid), Password: "x",
			AffCode: "aff" + strconv.Itoa(uid), Group: "default", Quota: 0,
			Role: role, Status: common.UserStatusEnabled,
		}).Error)
		if amount > 0 {
			seedStardust(t, ext, uid, amount)
		}
	}
}

// wheelEnv 是一套装好的转盘测试环境:扩展库 + 主库 + 真实路由,当前用户可切换。
type wheelEnv struct {
	ext    *gorm.DB
	router *gin.Engine
	// user 是下一次请求以谁的身份发出。用变量而不是每个身份一套路由:
	// 转盘的断言全在"同一场活动、不同的人接连转"上。
	user int
}

func newWheelEnv(t *testing.T, start map[int]int64) *wheelEnv {
	t.Helper()
	gin.SetMode(gin.TestMode)
	ext := newPayoutEnv(t, config.Lottery{
		Enabled:                true,
		PayoutMaxAttempts:      8,
		EntryCloseGraceSeconds: 0,
		RevealDelaySeconds:     0,
		MaxActiveActivities:    16,
		MaxPrizeTiers:          8,
		MaxOptions:             8,
		MaxTotalEntriesHard:    1_000,
		EntryBatchMaxMs:        45_000,
		SpendMaxLookbackDays:   90,
	})
	require.NoError(t, ext.AutoMigrate(&qymodel.AuditLog{}))
	invalidateSettings()
	newWheelMainDB(t, ext, start)

	env := &wheelEnv{ext: ext, user: wheelUserA}
	r := gin.New()
	as := func(h gin.HandlerFunc) gin.HandlerFunc {
		return func(c *gin.Context) { c.Set("id", env.user); h(c) }
	}
	r.POST("/admin/lottery/activities", as(handleCreateActivity))
	r.POST("/admin/lottery/activities/:act_no/publish", as(handlePublishActivity))
	r.POST("/admin/lottery/activities/:act_no/cancel", as(handleCancelActivity))
	r.POST("/admin/lottery/activities/:act_no/guess-result", as(handleSetGuessResult))
	r.PUT("/admin/lottery/activities/:act_no/schedule", as(handleSetWheelSchedule))
	r.PUT("/admin/lottery/activities/:act_no/basics", as(handleSetActivityBasics))
	r.GET("/lottery/activities", as(handleListActivities))
	r.GET("/lottery/activities/:act_no", as(handleGetActivity))
	r.POST("/lottery/activities/:act_no/spins", as(handleSpin))
	r.GET("/lottery/activities/:act_no/spins/me", as(handleListMySpins))
	r.POST("/lottery/activities/:act_no/entries", as(handleCreateEntry))
	r.GET("/lottery/public/:act_no/proof", handleGetProof)
	env.router = r
	return env
}

// call 以当前用户身份打一次请求。
func (e *wheelEnv) call(t *testing.T, method, path, body string) (int, []byte) {
	t.Helper()
	return callJSON(t, e.router, method, path, body)
}

// wheelCreateBody 是一份合法的转盘草稿:两档真实奖(头奖 500 星屑 × 2 份 30%、
// 兑换码 1 份 20%),谢谢参与由服务端派生成 50%。
func wheelCreateBody(t *testing.T, mutate func(m map[string]any)) string {
	t.Helper()
	now := common.GetTimestamp()
	m := map[string]any{
		"kind": KindDraw, "draw_mode": DrawModeWheel, "title": "星屑转盘",
		"stake_quota": wheelStake,
		"open_at":     now - 60, "close_at": now + 3600, "draw_at": now + 3601,
		"min_entries_to_hold": 0,
		"rules":               map[string]any{},
		"prizes": []map[string]any{
			{"tier": 1, "name": "头奖", "amount_quota": 500, "count": 2, "prize_type": PrizeTypeQuota, "win_ppm": 300000},
			{"tier": 2, "name": "兑换码", "count": 1, "prize_type": PrizeTypeText, "text_desc": "联系客服领取", "win_ppm": 200000},
		},
	}
	if mutate != nil {
		mutate(m)
	}
	raw, err := common.Marshal(m)
	require.NoError(t, err)
	return string(raw)
}

// publishWheel 走真实的建活动 + 发布,返回活动号。
func (e *wheelEnv) publishWheel(t *testing.T) string {
	t.Helper()
	e.user = wheelAdminId
	code, body := e.call(t, http.MethodPost, "/admin/lottery/activities", wheelCreateBody(t, nil))
	require.Equalf(t, http.StatusOK, code, "建活动失败: %s", body)
	actNo := jsonString(t, body, "data", "act_no")
	code, body = e.call(t, http.MethodPost, "/admin/lottery/activities/"+actNo+"/publish", "{}")
	require.Equalf(t, http.StatusOK, code, "发布失败: %s", body)
	e.user = wheelUserA
	return actNo
}

// spinBody 拼一次转动的请求体。
func spinBody(t *testing.T, crid, clientSeed string) string {
	t.Helper()
	raw, err := common.Marshal(map[string]any{"client_request_id": crid, "client_seed": clientSeed})
	require.NoError(t, err)
	return string(raw)
}

// decodeSpin 把转动接口的响应信封解成回执。走响应字节,不走 handler 的返回值。
func decodeSpin(t *testing.T, body []byte) spinResult {
	t.Helper()
	var env struct {
		Success bool       `json:"success"`
		Data    spinResult `json:"data"`
	}
	require.NoErrorf(t, common.Unmarshal(body, &env), "响应不是合法 JSON: %s", body)
	require.Truef(t, env.Success, "响应不是成功信封: %s", body)
	return env.Data
}

// seedOf 直接从库里读出种子。测试是"能读到库的人",这正是 D-13 里那一档。
func seedOf(t *testing.T, ext *gorm.DB, actId int64) string {
	t.Helper()
	var s Seed
	require.NoError(t, ext.Where("act_id = ?", actId).Take(&s).Error)
	return s.Seed
}

// steerClientSeed 找一个能让第 seq 转的摇号量落进 [lo, hi) 的 client_seed。
func steerClientSeed(t *testing.T, seedHex, actNo string, seq int, lo, hi uint32) string {
	t.Helper()
	for i := 0; i < 100000; i++ {
		cs := "cs" + strconv.Itoa(i)
		ticket, err := WheelTicket(seedHex, actNo, seq, cs)
		require.NoError(t, err)
		if r := RollPpm(ticket); r >= lo && r < hi {
			return cs
		}
	}
	require.FailNow(t, "十万次都没落进区间,区间八成写错了")
	return ""
}

func stakeRowsOf(t *testing.T, ext *gorm.DB, userId int, actNo string) int {
	t.Helper()
	n := 0
	for _, row := range ledgerRowsOf(t, ext, userId, actNo) {
		if row.Kind == string(stardust.KindLotStake) {
			n++
		}
	}
	return n
}

// 区间:头奖 [0, 300000)、兑换码 [300000, 500000)、谢谢参与 [500000, 1000000)。
const (
	bandT1Lo, bandT1Hi     = 0, 300000
	bandT2Lo, bandT2Hi     = 300000, 500000
	bandNoneLo, bandNoneHi = 500000, 1000000
)

func TestWheelEndToEndIsReproducibleFromTheProofEndpoint(t *testing.T) {
	env := newWheelEnv(t, map[int]int64{
		wheelAdminId: 1000, wheelUserA: 1000, wheelUserB: 1000, wheelUserC: 1000,
	})
	ext := env.ext

	// ── 建活动:none 档派生、ppmSum 铺满、min_entries_to_hold 钉死为 0 ──
	env.user = wheelAdminId
	code, body := env.call(t, http.MethodPost, "/admin/lottery/activities", wheelCreateBody(t, nil))
	require.Equalf(t, http.StatusOK, code, "建活动失败: %s", body)
	actNo := jsonString(t, body, "data", "act_no")
	var res activityWriteResult
	{
		var envelope struct {
			Data activityWriteResult `json:"data"`
		}
		require.NoError(t, common.Unmarshal(body, &envelope))
		res = envelope.Data
	}
	assert.EqualValues(t, 1000, res.PrizeTotalQuota, "Σ(count × amount) = 2 × 500,派生的 none 行不计")
	assert.EqualValues(t, 1000, res.WorstCaseNetIssue)
	assert.Equal(t, 1, res.WorstCaseTextGrants, "文本奖最坏履行份数 = text 真实档的 count")
	assert.EqualValues(t, 3, res.ExpectWinners)

	var act Activity
	require.NoError(t, ext.Where("act_no = ?", actNo).Take(&act).Error)
	var prizes []Prize
	require.NoError(t, ext.Where("act_id = ?", act.Id).Order("tier asc").Find(&prizes).Error)
	require.Len(t, prizes, 3, "两档真实奖 + 一行派生的谢谢参与")
	none := prizes[2]
	assert.Equal(t, 3, none.Tier)
	assert.Equal(t, PrizeTypeNone, none.PrizeType)
	assert.Equal(t, 500000, none.WinPpm, "none 的概率 = 1e6 − Σ其余")
	assert.Zero(t, none.Count)
	assert.Zero(t, none.AmountQuota)
	assert.Zero(t, none.StockLeft)
	assert.Equal(t, 2, prizes[0].StockLeft, "草稿期库存就是 count")
	assert.Equal(t, 1, prizes[1].StockLeft)
	// none 行进了 spec 原像:重算的 spec_hash 必须等于落库的那份。
	lines := make([]string, 0, 3)
	for _, p := range prizes {
		lines = append(lines, prizeSpecLineOf(AlgoV2, p))
	}
	assert.Equal(t, SpecHashV2(lines), act.SpecHash)
	assert.Contains(t, act.SpecText, PrizeTypeNone)

	// ── 发布 ──
	code, body = env.call(t, http.MethodPost, "/admin/lottery/activities/"+actNo+"/publish", "{}")
	require.Equalf(t, http.StatusOK, code, "发布失败: %s", body)
	act = *loadAct(t, ext, act.Id)
	require.Equal(t, StatusPublished, act.Status)
	require.NotEmpty(t, act.CommitHash)
	require.NoError(t, ext.Where("act_id = ?", act.Id).Order("tier asc").Find(&prizes).Error)
	assert.Equal(t, []int{2, 1, 0}, []int{prizes[0].StockLeft, prizes[1].StockLeft, prizes[2].StockLeft},
		"发布时库存 = count")

	seedHex := seedOf(t, ext, act.Id)
	spinPath := "/lottery/activities/" + actNo + "/spins"

	// ── 第 1 转(管理员,D-13):落头奖 ──
	env.user = wheelAdminId
	cs1 := steerClientSeed(t, seedHex, actNo, 1, bandT1Lo, bandT1Hi)
	code, body = env.call(t, http.MethodPost, spinPath, spinBody(t, "spin-1", cs1))
	require.Equalf(t, http.StatusOK, code, "管理员转动失败(D-13:转盘不禁管理员): %s", body)
	s1 := decodeSpin(t, body)
	assert.Equal(t, 1, s1.Seq)
	assert.Equal(t, 1, s1.ResultTier)
	assert.Zero(t, s1.ExhaustedTier)
	assert.EqualValues(t, 500, s1.Amount)
	assert.Equal(t, PrizeTypeQuota, s1.PrizeType)
	assert.False(t, s1.Replayed)
	assert.EqualValues(t, 1000-wheelStake+500, stardustOf(t, ext, wheelAdminId), "扣一转、当场到账头奖")

	// ── 第 2 转(A):落兑换码,text 档 granted + text_grant_count 逐转累加 ──
	env.user = wheelUserA
	cs2 := steerClientSeed(t, seedHex, actNo, 2, bandT2Lo, bandT2Hi)
	code, body = env.call(t, http.MethodPost, spinPath, spinBody(t, "spin-2", cs2))
	require.Equalf(t, http.StatusOK, code, "%s", body)
	s2 := decodeSpin(t, body)
	assert.Equal(t, 2, s2.ResultTier)
	assert.Equal(t, PrizeTypeText, s2.PrizeType)
	assert.Zero(t, s2.Amount, "文本奖不动钱")
	assert.EqualValues(t, 1000-wheelStake, stardustOf(t, ext, wheelUserA))
	assert.Equal(t, 1, loadAct(t, ext, act.Id).TextGrantCount)

	// ── 第 3 转(A):谢谢参与 ──
	cs3 := steerClientSeed(t, seedHex, actNo, 3, bandNoneLo, bandNoneHi)
	code, body = env.call(t, http.MethodPost, spinPath, spinBody(t, "spin-3", cs3))
	require.Equalf(t, http.StatusOK, code, "%s", body)
	s3 := decodeSpin(t, body)
	assert.Zero(t, s3.ResultTier)
	assert.Zero(t, s3.ExhaustedTier)
	assert.Equal(t, PrizeTypeNone, s3.PrizeType)
	assert.EqualValues(t, 1000-2*wheelStake, stardustOf(t, ext, wheelUserA))

	// ── 幂等重放:原样回原结果,不再摇;换 client_seed 409;换人 409 ──
	code, body = env.call(t, http.MethodPost, spinPath, spinBody(t, "spin-3", cs3))
	require.Equalf(t, http.StatusOK, code, "%s", body)
	replayed := decodeSpin(t, body)
	assert.True(t, replayed.Replayed)
	assert.Equal(t, s3.EntryNo, replayed.EntryNo)
	assert.Equal(t, s3.Seq, replayed.Seq)
	assert.Equal(t, s3.Ppm, replayed.Ppm)
	assert.Equal(t, s3.ChainHead, replayed.ChainHead)
	assert.EqualValues(t, 1000-2*wheelStake, stardustOf(t, ext, wheelUserA), "重放一分钱都不扣")
	assert.Equal(t, 2, stakeRowsOf(t, ext, wheelUserA, actNo), "账本上仍然只有两笔扣款")
	code, body = env.call(t, http.MethodPost, spinPath, spinBody(t, "spin-3", cs3+"x"))
	assert.Equal(t, http.StatusConflict, code)
	assert.Equal(t, "qy_lot_idem_conflict", errorCode(t, body), "换 client_seed 重放是换参,一律 409")
	env.user = wheelUserB
	code, body = env.call(t, http.MethodPost, spinPath, spinBody(t, "spin-3", cs3))
	assert.Equal(t, http.StatusConflict, code)
	assert.Equal(t, "qy_lot_idem_conflict", errorCode(t, body), "换人用同一个 crid 是换参")
	assert.Equal(t, 3, loadAct(t, ext, act.Id).EntrySeq, "重放与冲突都不占序号")

	// ── 第 4 转(C):摇中兑换码但已发完 → 落空、记 exhausted_tier ──
	env.user = wheelUserC
	cs4 := steerClientSeed(t, seedHex, actNo, 4, bandT2Lo, bandT2Hi)
	code, body = env.call(t, http.MethodPost, spinPath, spinBody(t, "spin-4", cs4))
	require.Equalf(t, http.StatusOK, code, "%s", body)
	s4 := decodeSpin(t, body)
	assert.Zero(t, s4.ResultTier)
	assert.Equal(t, 2, s4.ExhaustedTier, "摇中的档已发完:结果落空,exhausted_tier 记下那一档")
	assert.Equal(t, PrizeTypeNone, s4.PrizeType)
	assert.EqualValues(t, 1000-wheelStake, stardustOf(t, ext, wheelUserC), "落空同样扣本金")

	// ── 第 5 转(B):最后一份头奖 → 全部真实档库存归零 → 当场封盘 ──
	env.user = wheelUserB
	cs5 := steerClientSeed(t, seedHex, actNo, 5, bandT1Lo, bandT1Hi)
	code, body = env.call(t, http.MethodPost, spinPath, spinBody(t, "spin-5", cs5))
	require.Equalf(t, http.StatusOK, code, "%s", body)
	s5 := decodeSpin(t, body)
	assert.Equal(t, 1, s5.ResultTier)
	assert.EqualValues(t, 1000-wheelStake+500, stardustOf(t, ext, wheelUserB))
	locked := loadAct(t, ext, act.Id)
	assert.Equal(t, StatusLocked, locked.Status, "库存耗尽必须当场封盘")
	assert.Equal(t, OutcomeNone, locked.Outcome)
	assert.NotZero(t, locked.LockedAt)
	assert.NotEmpty(t, locked.RosterHash, "封盘要冻结名单")
	assert.Equal(t, 5, locked.RosterCount)
	assert.Equal(t, act.CloseAt, locked.CloseAt, "close_at 不动:封盘之后排期已无意义,揭示按 max(draw_at, locked_at+delay)")
	assert.Equal(t, act.DrawAt, locked.DrawAt)
	require.NoError(t, ext.Where("act_id = ?", act.Id).Order("tier asc").Find(&prizes).Error)
	assert.Equal(t, []int{0, 0, 0}, []int{prizes[0].StockLeft, prizes[1].StockLeft, prizes[2].StockLeft})

	// ── 第 6 转:已封盘 → 409,不占序号、不扣钱 ──
	env.user = wheelUserA
	code, body = env.call(t, http.MethodPost, spinPath, spinBody(t, "spin-6", "zzz"))
	assert.Equal(t, http.StatusConflict, code)
	assert.Equal(t, "qy_lot_wheel_closed", errorCode(t, body))
	assert.Equal(t, 5, loadAct(t, ext, act.Id).EntrySeq)
	assert.EqualValues(t, 1000-2*wheelStake, stardustOf(t, ext, wheelUserA))

	// ── 账面:出款行与合计 ──
	var payouts []Payout
	require.NoError(t, ext.Where("act_id = ?", act.Id).Order("id asc").Find(&payouts).Error)
	require.Len(t, payouts, 3, "两笔头奖 + 一笔文本奖;落空与谢谢参与不落 payout 行")
	kinds := map[string]int{}
	for _, p := range payouts {
		kinds[p.Kind+"/"+p.Status]++
		if p.Kind == PayoutPrize {
			assert.NotEmpty(t, p.OrderNo, "当场派奖的出款行必须指回入账流水")
			assert.NotZero(t, p.SettledAt)
			rows := ledgerByIdem(t, ext, idemScopePayout, payoutIdemKey(p.PayoutNo))
			require.Len(t, rows, 1)
			assert.Equal(t, rows[0].LedgerNo, p.OrderNo)
		}
	}
	assert.Equal(t, map[string]int{"prize/paid": 2, "text/granted": 1}, kinds)
	assert.EqualValues(t, 1000, locked.PayoutQuota, "payout_quota 逐转累加")
	assert.Equal(t, 1, locked.TextGrantCount)
	assert.EqualValues(t, 5*wheelStake, locked.PoolQuota)
	assert.Equal(t, 5, locked.ActiveCount)

	// ── 我的转动(分页,最近的在前)──
	code, body = env.call(t, http.MethodGet, "/lottery/activities/"+actNo+"/spins/me?page_size=10", "")
	require.Equalf(t, http.StatusOK, code, "%s", body)
	var mine struct {
		Data struct {
			Items []mySpinView `json:"items"`
			Total int64        `json:"total"`
		} `json:"data"`
	}
	require.NoError(t, common.Unmarshal(body, &mine))
	require.Len(t, mine.Data.Items, 2)
	assert.EqualValues(t, 2, mine.Data.Total)
	assert.Equal(t, 3, mine.Data.Items[0].Seq)
	assert.Equal(t, PrizeTypeNone, mine.Data.Items[0].PrizeType)
	assert.Equal(t, 2, mine.Data.Items[1].Seq)
	assert.Equal(t, PrizeTypeText, mine.Data.Items[1].PrizeType)
	assert.Equal(t, cs2, mine.Data.Items[1].ClientSeed)
	assert.Equal(t, s2.ChainHead, mine.Data.Items[1].ChainHash)

	// ── 列表:转盘只从 draw_mode=wheel 出去,大厅全量与三张夹都拿不到它 ──
	code, body = env.call(t, http.MethodGet, "/lottery/activities?draw_mode=wheel&phase=live", "")
	require.Equalf(t, http.StatusOK, code, "%s", body)
	var list struct {
		Data struct {
			Items []activityBrief `json:"items"`
		} `json:"data"`
	}
	require.NoError(t, common.Unmarshal(body, &list))
	require.Len(t, list.Data.Items, 1)
	assert.Equal(t, actNo, list.Data.Items[0].ActNo)
	assert.Equal(t, CurrencyStardust, list.Data.Items[0].Currency)
	require.Len(t, list.Data.Items[0].Tiers, 3, "转盘卡片要带各档剩余/初始")
	require.NotNil(t, list.Data.Items[0].Tiers[0].StockLeft)
	assert.Zero(t, *list.Data.Items[0].Tiers[0].StockLeft)
	assert.Equal(t, 2, list.Data.Items[0].Tiers[0].Count)
	for _, q := range []string{"", "?lane=draw", "?lane=ball", "?lane=guess", "?phase=live"} {
		code, body = env.call(t, http.MethodGet, "/lottery/activities"+q, "")
		require.Equalf(t, http.StatusOK, code, "%s", body)
		require.NoError(t, common.Unmarshal(body, &list))
		assert.Emptyf(t, list.Data.Items, "转盘不进大厅(%q)", q)
	}
	code, body = env.call(t, http.MethodGet, "/lottery/activities?draw_mode=wheel&lane=draw", "")
	assert.Equal(t, http.StatusBadRequest, code)
	assert.Equal(t, "qy_lot_bad_draw_mode", errorCode(t, body))

	// 详情页的奖档带 stock_left(0 也要发)。
	code, body = env.call(t, http.MethodGet, "/lottery/activities/"+actNo, "")
	require.Equalf(t, http.StatusOK, code, "%s", body)
	detail := activityDetailOf(t, env.router, actNo)
	require.Len(t, detail.Spec, 3)
	require.NotNil(t, detail.Spec[0].StockLeft)
	assert.Equal(t, PrizeTypeNone, detail.Spec[2].PrizeType)

	// ── 揭示:不抽签、不登记计划,只公开种子并复核合计 ──
	//
	// 直接调 revealActivity 而不是等 runReveal 扫到 draw_at:draw_at 进承诺原像,
	// 在库里把它改到过去会让承诺校验(正确地)拒绝开奖;扫描条件本身与玩法无关,
	// 由批次玩法的用例覆盖。
	require.NoError(t, revealActivity(context.Background(), ext, loadAct(t, ext, act.Id)))
	drawn := loadAct(t, ext, act.Id)
	require.Equal(t, StatusSettling, drawn.Status, "转盘揭示必须走真实路径并通过三道校验")
	assert.Equal(t, OutcomeDrawn, drawn.Outcome)
	assert.NotZero(t, drawn.RevealedAt)
	assert.EqualValues(t, 1000, drawn.PayoutQuota)
	var payoutRows int64
	require.NoError(t, ext.Model(&Payout{}).Where("act_id = ?", act.Id).Count(&payoutRows).Error)
	assert.EqualValues(t, 3, payoutRows, "揭示不许再登记任何出款计划")
	runSettle(context.Background())
	finished := loadAct(t, ext, act.Id)
	assert.Equal(t, StatusFinished, finished.Status)
	assert.EqualValues(t, 1000, finished.PayoutQuota)
	assert.Zero(t, finished.RefundQuota)

	// ── 证据链:JSON 版 ──
	code, body = env.call(t, http.MethodGet, "/lottery/public/"+actNo+"/proof?page_size=1000", "")
	require.Equalf(t, http.StatusOK, code, "%s", body)
	var envelope struct {
		Data proofDocument `json:"data"`
	}
	require.NoError(t, common.Unmarshal(body, &envelope))
	doc := &envelope.Data
	require.Equal(t, seedHex, doc.RevealedSeed, "揭示之后种子必须公开")
	require.Equal(t, DrawModeWheel, doc.DrawMode)
	assert.Equal(t, CurrencyStardust, doc.Currency)
	require.Len(t, doc.Entries, 5)
	require.Len(t, doc.Spins, 5)
	require.Len(t, doc.Tiers, 3)
	assert.Equal(t, PrizeTypeNone, doc.Tiers[2].PrizeType)
	assert.Equal(t, 2, doc.Tiers[0].Count)
	assert.Zero(t, doc.Tiers[0].StockLeft)
	assert.Contains(t, doc.Notice, "不保证")
	assert.Contains(t, doc.Notice, "user_ref")
	assert.NotContains(t, doc.Notice, "任何人都无法预知")
	for i, s := range doc.Spins {
		assert.Equal(t, i+1, s.Seq)
		assert.Equal(t, doc.Entries[i].UserRef, s.UserRef)
		assert.Equal(t, doc.Entries[i].ChainHash, s.Chain)
		assert.Empty(t, doc.Entries[i].Pick, "转盘条目的 pick 列恒为空:结果编码只进链原像")
	}
	assert.Equal(t, []int{1, 2, 0, 0, 1}, []int{doc.Spins[0].Tier, doc.Spins[1].Tier, doc.Spins[2].Tier, doc.Spins[3].Tier, doc.Spins[4].Tier})
	assert.Equal(t, 2, doc.Spins[3].ExhaustedTier)

	// 从零复算:只用标准库,不碰本包的 WheelTicket / Bands。
	replay := independentWheelReplay(t, doc)
	assert.Equal(t, replay.chainHead, doc.ChainHead, "按 WheelPick 编码逐环推出的链尾必须等于公布的 chain_head")
	assert.Empty(t, replay.mismatches, "每一转的摇号量 / 落档 / 耗尽档必须与公布一致")
	assert.Equal(t, map[int]int{1: 0, 2: 0}, replay.stock, "按序重放库存递减的终态 = 公布的 stock_left")
	// 名单哈希对转盘照常:条目的 pick 为空串。
	roster := make([]RosterLine, 0, len(doc.Entries))
	for _, e := range doc.Entries {
		roster = append(roster, RosterLine{EntryNo: e.EntryNo, UserRef: e.UserRef, OptNo: e.OptNo, Amount: e.Amount, Pick: e.Pick})
	}
	sort.SliceStable(roster, func(i, j int) bool { return roster[i].EntryNo < roster[j].EntryNo })
	hash, count := RosterHashV2(doc.ActNo, doc.CommitHash, roster)
	assert.Equal(t, doc.RosterHash, hash)
	assert.Equal(t, doc.RosterCount, count)

	// ── 证据链:NDJSON 版,一行一转 ──
	code, body = env.call(t, http.MethodGet, "/lottery/public/"+actNo+"/proof?format=ndjson", "")
	require.Equalf(t, http.StatusOK, code, "%s", body)
	scanner := bufio.NewScanner(bytes.NewReader(body))
	scanner.Buffer(make([]byte, 0, 1<<20), 1<<20)
	require.True(t, scanner.Scan())
	var header proofDocument
	require.NoError(t, common.Unmarshal(scanner.Bytes(), &header))
	assert.Empty(t, header.Entries)
	assert.Empty(t, header.Spins, "NDJSON 的头里不带任何一转:它们随条目行流下去")
	assert.Len(t, header.Tiers, 3)
	streamed := 0
	for scanner.Scan() {
		var line proofEntry
		require.NoError(t, common.Unmarshal(scanner.Bytes(), &line))
		require.NotNil(t, line.Spin, "NDJSON 里每一条转盘条目都要带上那一转")
		assert.Equal(t, line.Seq, line.Spin.Seq)
		assert.Equal(t, doc.Spins[streamed], *line.Spin)
		streamed++
	}
	assert.Equal(t, 5, streamed)

	// 把 JSON 版原样导出:它就是 verify.py 与 verify.ts 要吃的那份 fixture。
	if out := os.Getenv("QY_WHEEL_PROOF_OUT"); out != "" {
		raw, err := common.Marshal(doc)
		require.NoError(t, err)
		require.NoError(t, os.WriteFile(out, raw, 0o644))
	}
	line, err := common.Marshal(doc)
	require.NoError(t, err)
	t.Log("WHEEL_PROOF_JSON " + string(line))
}

// wheelReplay 是独立复算的结论。
type wheelReplay struct {
	chainHead  string
	mismatches []string
	stock      map[int]int
}

// independentWheelReplay 是一份**只用标准库**的转盘复算:不碰本包的 WheelTicket /
// Bands / ChainNextV2,连 HMAC 都是手写的(hmacSHA256,见 fairness_v2_test.go)。
// 它就是第三方验证者会写的那几十行,与 lottery-verify.py 的 wheel_replay 同一套编码。
func independentWheelReplay(t *testing.T, doc *proofDocument) wheelReplay {
	t.Helper()
	const sep = "\x1f"
	h := func(parts ...string) string {
		sum := sha256.Sum256([]byte(strings.Join(parts, sep)))
		return hex.EncodeToString(sum[:])
	}
	key, err := hex.DecodeString(doc.RevealedSeed)
	require.NoError(t, err)

	type band struct {
		tier   int
		lo, hi uint64
		count  int
		none   bool
	}
	tiers := make([]proofTier, len(doc.Tiers))
	copy(tiers, doc.Tiers)
	sort.SliceStable(tiers, func(i, j int) bool { return tiers[i].Tier < tiers[j].Tier })
	bands := make([]band, 0, len(tiers))
	var acc uint64
	for _, tr := range tiers {
		bands = append(bands, band{tier: tr.Tier, lo: acc, hi: acc + uint64(tr.WinPpm), count: tr.Count, none: tr.PrizeType == PrizeTypeNone})
		acc += uint64(tr.WinPpm)
	}
	require.EqualValues(t, PpmDen, acc, "转盘的摇号轴必须被铺满")

	stock := map[int]int{}
	for _, b := range bands {
		if !b.none {
			stock[b.tier] = b.count
		}
	}
	spins := map[int]proofSpin{}
	for _, s := range doc.Spins {
		spins[s.Seq] = s
	}
	entries := make([]proofEntry, len(doc.Entries))
	copy(entries, doc.Entries)
	sort.SliceStable(entries, func(i, j int) bool { return entries[i].Seq < entries[j].Seq })

	out := wheelReplay{chainHead: doc.CommitHash, stock: stock}
	for i, e := range entries {
		require.Equal(t, i+1, e.Seq, "seq 必须从 1 起连续无缺口")
		s, ok := spins[e.Seq]
		require.Truef(t, ok, "第 %d 转缺少转动记录", e.Seq)
		mac := hmacSHA256(key, strings.Join([]string{"qylot-wheel-v2", doc.ActNo, strconv.Itoa(e.Seq), s.ClientSeed}, sep))
		u := new(big.Int).SetBytes(mac[:8])
		u.Mul(u, big.NewInt(PpmDen))
		u.Rsh(u, 64)
		r := u.Uint64()
		tier, exhausted := 0, 0
		for _, b := range bands {
			if r < b.lo || r >= b.hi {
				continue
			}
			switch {
			case b.none:
			case stock[b.tier] > 0:
				stock[b.tier]--
				tier = b.tier
			default:
				exhausted = b.tier
			}
			break
		}
		if int64(r) != s.Ppm || tier != s.Tier || exhausted != s.ExhaustedTier {
			out.mismatches = append(out.mismatches, fmt.Sprintf("seq %d: mine (%d,%d,%d) theirs (%d,%d,%d)",
				e.Seq, r, tier, exhausted, s.Ppm, s.Tier, s.ExhaustedTier))
		}
		pick := strings.Join([]string{"w", strconv.Itoa(s.Tier), strconv.FormatInt(s.Ppm, 10), strconv.Itoa(s.ExhaustedTier), s.ClientSeed}, "|")
		out.chainHead = h("qylot-chain-v2", out.chainHead, doc.ActNo, strconv.Itoa(e.Seq), e.EntryNo, e.UserRef,
			strconv.Itoa(e.OptNo), strconv.FormatInt(e.Amount, 10), pick)
		assert.Equalf(t, e.ChainHash, out.chainHead, "第 %d 转的链环对不上", e.Seq)
	}
	return out
}

// 星屑不足:整笔回滚,不占序号、不留流水、不落票。
func TestWheelSpinInsufficientStardustTakesNoSeq(t *testing.T) {
	env := newWheelEnv(t, map[int]int64{wheelAdminId: 1000, wheelUserA: 1000, wheelUserB: wheelStake - 1})
	actNo := env.publishWheel(t)
	var act Activity
	require.NoError(t, env.ext.Where("act_no = ?", actNo).Take(&act).Error)

	env.user = wheelUserB
	code, body := env.call(t, http.MethodPost, "/lottery/activities/"+actNo+"/spins", spinBody(t, "poor-1", "abc"))
	assert.Equal(t, http.StatusUnprocessableEntity, code, "锁外资格预判就该拦下:星屑不够扣这一转")
	assert.Equal(t, "qy_lot_ineligible", errorCode(t, body))

	// 绕过预判(余额在预判之后被花掉)的形状:直接跑事务。
	seedStardust(t, env.ext, wheelUserC, wheelStake-1)
	cur := loadAct(t, env.ext, act.Id)
	e := &Entry{
		EntryNo: newEntryNo(), ActId: act.Id, IdemKey: buildIdemKey(actNo, "poor-2"),
		UserId: wheelUserC, UserRef: UserRef("salt", wheelUserC), Amount: wheelStake,
		Status: EntrySuccess, CreatedAt: common.GetTimestamp(),
	}
	_, err := spinWheelTxForTest(env.ext, cur, e)
	require.ErrorIs(t, err, stardust.ErrInsufficient)

	after := loadAct(t, env.ext, act.Id)
	assert.Zero(t, after.EntrySeq, "失败的转动不占序号")
	assert.Zero(t, after.ActiveCount)
	assert.Zero(t, after.PoolQuota)
	assert.Equal(t, act.CommitHash, after.ChainHead)
	assert.EqualValues(t, wheelStake-1, stardustOf(t, env.ext, wheelUserC))
	var tickets int64
	require.NoError(t, env.ext.Model(&Entry{}).Where("act_id = ?", act.Id).Count(&tickets).Error)
	assert.Zero(t, tickets)
	assert.Zero(t, stakeRowsOf(t, env.ext, wheelUserC, actNo), "回滚之后账本上不许留下幂等残行")
	var prizes []Prize
	require.NoError(t, env.ext.Where("act_id = ?", act.Id).Order("tier asc").Find(&prizes).Error)
	assert.Equal(t, 2, prizes[0].StockLeft, "库存一份都不动")
}

// spinWheelTxForTest 直接跑转动事务(绕过 handler 的锁外预判)。
func spinWheelTxForTest(ext *gorm.DB, act *Activity, e *Entry) (spinOutcome, error) {
	var out spinOutcome
	err := ext.Transaction(func(tx *gorm.DB) error {
		o, err := spinTx(context.Background(), tx, act, e)
		out = o
		return err
	})
	return out, err
}

// 奖档被篡改:每一转都校验,对不上就拒绝、挂旗、写一条(去重的)系统审计,不占序号不扣钱。
func TestWheelSpinRefusesOnSpecDriftAndRaisesTheFlag(t *testing.T) {
	env := newWheelEnv(t, map[int]int64{wheelAdminId: 1000, wheelUserA: 1000})
	actNo := env.publishWheel(t)
	var act Activity
	require.NoError(t, env.ext.Where("act_no = ?", actNo).Take(&act).Error)

	// 发布之后把头奖概率从 30% 改成 90%:批次玩法要等到开奖才发现,转盘下一转就发现。
	require.NoError(t, env.ext.Model(&Prize{}).Where("act_id = ? AND tier = ?", act.Id, 1).
		Update("win_ppm", 900000).Error)

	env.user = wheelUserA
	for i := 0; i < 2; i++ {
		code, body := env.call(t, http.MethodPost, "/lottery/activities/"+actNo+"/spins",
			spinBody(t, "drift-"+strconv.Itoa(i), "abc"))
		assert.Equal(t, http.StatusConflict, code)
		assert.Equal(t, "qy_lot_spec_drift", errorCode(t, body))
	}
	after := loadAct(t, env.ext, act.Id)
	assert.Zero(t, after.EntrySeq, "被拒绝的转动不占序号")
	assert.EqualValues(t, 1000, stardustOf(t, env.ext, wheelUserA), "一分钱不扣")

	var flags []Flag
	require.NoError(t, env.ext.Where("act_id = ? AND code = ?", act.Id, FlagSpecDrift).Find(&flags).Error)
	require.Len(t, flags, 1, "漂移必须挂旗,而且同一场只挂一条")
	assert.Contains(t, flags[0].Detail, "spec_hash")
	var audits int64
	require.NoError(t, env.ext.Model(&qymodel.AuditLog{}).
		Where("action = ? AND trace_no = ?", "lottery.spin", actNo).Count(&audits).Error)
	assert.EqualValues(t, 1, audits, "系统审计跟着 flag 的去重走:两次尝试只写一条")
}

// 同一用户并发两路转动:seq 连续、恰好扣两次、两条链环首尾相接。
//
// SQLite 内存库只有一条连接,两路在连接池上排队 —— 它验的是"两路都能落定、
// 序号不撞不跳、账本恰好两行",而不是真正的行锁竞争(那要真 MySQL,见 design-15 §12)。
func TestWheelConcurrentSpinsBySameUserTakeContiguousSeqs(t *testing.T) {
	env := newWheelEnv(t, map[int]int64{wheelAdminId: 1000, wheelUserA: 1000})
	actNo := env.publishWheel(t)
	var act Activity
	require.NoError(t, env.ext.Where("act_no = ?", actNo).Take(&act).Error)
	env.user = wheelUserA

	var wg sync.WaitGroup
	codes := make([]int, 2)
	bodies := make([][]byte, 2)
	for i := 0; i < 2; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			codes[i], bodies[i] = callJSON(t, env.router, http.MethodPost,
				"/lottery/activities/"+actNo+"/spins", spinBody(t, "par-"+strconv.Itoa(i), "s"+strconv.Itoa(i)))
		}(i)
	}
	wg.Wait()

	seqs := make([]int, 0, 2)
	prizeSum := int64(0)
	for i := 0; i < 2; i++ {
		require.Equalf(t, http.StatusOK, codes[i], "第 %d 路: %s", i, bodies[i])
		s := decodeSpin(t, bodies[i])
		seqs = append(seqs, s.Seq)
		prizeSum += s.Amount
	}
	sort.Ints(seqs)
	assert.Equal(t, []int{1, 2}, seqs, "两路各拿一个序号,连续无缺口")
	assert.Equal(t, 2, stakeRowsOf(t, env.ext, wheelUserA, actNo), "恰好扣两次")
	assert.EqualValues(t, 1000-2*wheelStake+prizeSum, stardustOf(t, env.ext, wheelUserA))

	var rows []Entry
	require.NoError(t, env.ext.Where("act_id = ?", act.Id).Order("seq asc").Find(&rows).Error)
	require.Len(t, rows, 2)
	assert.Equal(t, act.CommitHash, rows[0].PrevHash)
	assert.Equal(t, rows[0].ChainHash, rows[1].PrevHash)
	assert.Equal(t, rows[1].ChainHash, loadAct(t, env.ext, act.Id).ChainHead)
	assert.Equal(t, 2, loadAct(t, env.ext, act.Id).EntrySeq)
}

// 入口纪律:转盘不接受报名、非转盘不接受转动、client_seed 与 crid 的形状校验。
func TestWheelEntrypointsRejectTheWrongDoor(t *testing.T) {
	env := newWheelEnv(t, map[int]int64{wheelAdminId: 1000, wheelUserA: 1000})
	actNo := env.publishWheel(t)
	env.user = wheelUserA

	code, body := env.call(t, http.MethodPost, "/lottery/activities/"+actNo+"/entries",
		`{"client_request_id":"e-1"}`)
	assert.Equal(t, http.StatusBadRequest, code, "转盘活动打到报名接口必须拒绝: %s", body)
	assert.Equal(t, "qy_lot_bad_request", errorCode(t, body))

	for _, tc := range []struct{ name, body, code string }{
		{"client_seed 含分隔符", spinBody(t, "x-1", "a|b"), "qy_lot_bad_client_seed"},
		{"client_seed 超长", spinBody(t, "x-2", strings.Repeat("a", 65)), "qy_lot_bad_client_seed"},
		{"crid 缺失", `{"client_seed":"a"}`, "qy_lot_bad_request_id"},
		{"crid 带 #", spinBody(t, "x#1", "a"), "qy_lot_bad_request_id"},
	} {
		code, body := env.call(t, http.MethodPost, "/lottery/activities/"+actNo+"/spins", tc.body)
		assert.Equalf(t, http.StatusBadRequest, code, "%s: %s", tc.name, body)
		assert.Equalf(t, tc.code, errorCode(t, body), tc.name)
	}
	// 空 client_seed 合法:按空分量进原像。
	code, body = env.call(t, http.MethodPost, "/lottery/activities/"+actNo+"/spins", spinBody(t, "x-3", ""))
	require.Equalf(t, http.StatusOK, code, "%s", body)
	assert.Equal(t, 1, decodeSpin(t, body).Seq)

	// 非转盘活动打到转动接口。
	prob := seedActivity(t, env.ext, func(a *Activity) {
		a.Algo = AlgoV2
		a.DrawMode = DrawModeProb
	})
	require.NoError(t, env.ext.Create(&Seed{
		ActId: prob.Id, Seed: newSecret(), RefSalt: newSecret(), IpSalt: newSecret(), CreatedAt: common.GetTimestamp(),
	}).Error)
	code, body = env.call(t, http.MethodPost, "/lottery/activities/"+prob.ActNo+"/spins", spinBody(t, "x-4", "a"))
	assert.Equal(t, http.StatusBadRequest, code)
	assert.Equal(t, "qy_lot_bad_request", errorCode(t, body))
	code, body = env.call(t, http.MethodGet, "/lottery/activities/"+prob.ActNo+"/spins/me", "")
	assert.Equal(t, http.StatusBadRequest, code, "%s", body)
}
