package lottery

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/qianye/config"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// ball_result_visibility_db_test.go —— 「我中了没有」这句话在接口上答不答得了。
//
// # 被投诉的那个形状
//
// 项目方原话:「关于双色球,已开奖的抽奖为什么不显示双色球号码?」
// 实测复现之后,缺的不是开奖号(详情页与大厅卡片一直都有),而是**两组号从来
// 没有在同一份响应里出现过**:
//
//   - `GET /lottery/activities/:act_no` 有 ball_result,没有"我买的号";
//   - `GET /lottery/my-entries` 有 pick,没有 ball_result。
//
// 于是无论前端怎么画,那句话都答不出来 —— 唯一并排的地方是「为什么是这个结果」
// 弹窗,而它要整份拉证据链再用 WebCrypto 复算。
//
// 这一组用例打的是**真实的用户端点**,断言的是"同一份响应里两组号都在",
// 而不是构造 DTO:字段少一个、名字写错一个,前端就退回到答不出来的状态,
// 而纯函数测试全绿。

// ballVisibilityEnv 起一个跑得起 handleGetActivity / handleListMyEntries 的环境。
//
// 两条路由都手动 c.Set("id", ...) —— 线上那一步由 UserAuth 做,而这里要能
// **换着身份**打同一条路由:跨用户隔离是这份响应体上最贵的一条,
// 详情页现在会带上"我的票",一旦过滤条件写错就是把别人买的号发出去。
func ballVisibilityEnv(t *testing.T, userId *int) *gin.Engine {
	t.Helper()
	r := gin.New()
	r.GET("/lottery/activities/:act_no", func(c *gin.Context) {
		c.Set("id", *userId)
		handleGetActivity(c)
	})
	r.GET("/lottery/my-entries", func(c *gin.Context) {
		c.Set("id", *userId)
		handleListMyEntries(c)
	})
	return r
}

func getActivityDetail(t *testing.T, r *gin.Engine, actNo string) activityDetail {
	t.Helper()
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/lottery/activities/"+actNo, nil))
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	var envelope struct {
		Success bool           `json:"success"`
		Data    activityDetail `json:"data"`
	}
	require.NoError(t, common.Unmarshal(rec.Body.Bytes(), &envelope))
	require.True(t, envelope.Success)
	return envelope.Data
}

func listMyEntries(t *testing.T, r *gin.Engine) []myEntryView {
	t.Helper()
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/lottery/my-entries", nil))
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	var envelope struct {
		Success bool `json:"success"`
		Data    struct {
			Items []myEntryView `json:"items"`
		} `json:"data"`
	}
	require.NoError(t, common.Unmarshal(rec.Body.Bytes(), &envelope))
	require.True(t, envelope.Success)
	return envelope.Data.Items
}

// seedBallTicket 落一张已成功的双色球票。走 Create 而不是 reserveEntry:
// 这一组用例测的是**读**路径的形状,报名链路自己有一整套用例。
func seedBallTicket(t *testing.T, act *Activity, userId, seq int, pick string) *Entry {
	t.Helper()
	entryNo := newEntryNo()
	e := &Entry{
		ActId: act.Id, EntryNo: entryNo, UserId: userId, Seq: seq,
		// idem_key 上有 uk(act_id, idem_key) —— 留空会让同一场里的第二张票
		// 直接撞唯一键。线上那一份来自客户端的幂等键,这里给一个逐票唯一的值。
		IdemKey: entryNo,
		Amount:  act.StakeQuota, Status: EntrySuccess, Pick: pick,
		UserRef: "ref-" + pick, CreatedAt: common.GetTimestamp(),
	}
	require.NoError(t, qyDBHandle.Load().Create(e).Error)
	return e
}

// TestBallDetailCarriesMyTicketsBesideTheDrawnNumbers 钉住详情页那一份响应里
// **开奖号与我的号同时在**,而且"中了哪一档、赔多少"与"这张票没有派奖行"
// 是两个能分开的取值。
//
// 顺带钉住零值口径:won_kind 为空串**不等于**没中 —— 它只说明没有派奖行,
// 是不是"没中"要看 ball_result 空不空(见 `myTicketView` 的注释)。
func TestBallDetailCarriesMyTicketsBesideTheDrawnNumbers(t *testing.T) {
	gin.SetMode(gin.TestMode)
	gdb := newPayoutEnv(t, config.Lottery{Enabled: true})

	const (
		winnerId = 7001
		loserId  = 7002
	)
	act := seedActivity(t, gdb, func(a *Activity) {
		a.Title = "双色球第 3 期"
		a.Kind = KindDraw
		a.DrawMode = DrawModeBall
		a.Algo = AlgoV2
		a.Status = StatusFinished
		a.Outcome = OutcomeDrawn
		a.SeriesNo = "LS-vis"
		a.IssueNo = 3
		a.BallRedPool = 12
		a.BallRedPick = 3
		a.BallBluePool = 4
		a.BallBluePick = 1
		a.BallResult = "08,10,11|03"
		a.PoolOpenQuota = 200_000
		a.PoolCarryQuota = 40_000
	})
	require.NoError(t, gdb.Create(&Prize{
		ActId: act.Id, Tier: 2, Name: "二等奖", AmountQuota: 0, Count: 5,
		PrizeType: PrizeTypeQuota, RedMatch: 2, BlueMatch: 0, PoolShareBps: 3000,
	}).Error)

	// 中奖者两张票:一张中了二等奖,一张什么都没中。同一个人身上两种结果,
	// 才能证明"有没有派奖行"是**逐票**判定而不是整场判定。
	won := seedBallTicket(t, act, winnerId, 1, "10,11,12|04")
	lost := seedBallTicket(t, act, winnerId, 2, "01,02,03|01")
	// 另一个人的票。它绝不能出现在中奖者的响应里。
	other := seedBallTicket(t, act, loserId, 3, "05,06,07|02")
	require.NoError(t, gdb.Create(&Payout{
		ActId: act.Id, EntryId: won.Id, UserId: winnerId, PayoutNo: newPayoutNo(),
		Kind: PayoutPrize, Tier: 2, AmountQuota: 60_840, Status: PayoutPaid,
	}).Error)

	me := winnerId
	r := ballVisibilityEnv(t, &me)
	detail := getActivityDetail(t, r, act.ActNo)

	require.Equal(t, "08,10,11|03", detail.BallResult,
		"详情页少了开奖号,「开的是哪几个号」就无处可答")
	require.Len(t, detail.MyTickets, 2,
		"详情页必须带上我自己的票 —— 少了它「我中了没有」在这一页答不出来")

	byEntry := make(map[string]myTicketView, len(detail.MyTickets))
	for _, ticket := range detail.MyTickets {
		byEntry[ticket.EntryNo] = ticket
	}
	hit := byEntry[won.EntryNo]
	assert.Equal(t, "10,11,12|04", hit.Pick)
	assert.Equal(t, PayoutPrize, hit.WonKind)
	assert.Equal(t, 2, hit.WonTier)
	assert.EqualValues(t, 60_840, hit.WonAmount)

	miss := byEntry[lost.EntryNo]
	assert.Equal(t, "01,02,03|01", miss.Pick)
	assert.Empty(t, miss.WonKind, "没有派奖行时 won_kind 必须是空串")
	assert.Zero(t, miss.WonTier, "奖级从 1 起,0 是「没有派奖行」的零值")
	assert.Zero(t, miss.WonAmount)

	// 跨用户隔离。这条不是洁癖:详情页现在会把票列出来,过滤条件一旦写错,
	// 泄漏的是全场每个人买的号 —— 而在开奖之前那正是最不该外泄的东西。
	for _, ticket := range detail.MyTickets {
		assert.NotEqual(t, other.EntryNo, ticket.EntryNo,
			"详情页把别人的票发给了我")
		assert.NotEqual(t, "05,06,07|02", ticket.Pick)
	}

	// 换个身份打同一条路由:另一个人拿到的必须是他自己那一张。
	me = loserId
	otherDetail := getActivityDetail(t, r, act.ActNo)
	require.Len(t, otherDetail.MyTickets, 1)
	assert.Equal(t, "05,06,07|02", otherDetail.MyTickets[0].Pick)
	assert.Empty(t, otherDetail.MyTickets[0].WonKind)
}

// TestBallMyEntriesCarryTheDrawnNumbers 钉住「我的参与」那份响应里
// **我的号与开奖号同时在**。
//
// 少了 ball_result,这张表就只能显示"我买了 10,11,12|04",而"中没中"要逐行
// 点开一个会整份下载证据链的弹窗 —— 那正是改造前的状态。
func TestBallMyEntriesCarryTheDrawnNumbers(t *testing.T) {
	gin.SetMode(gin.TestMode)
	gdb := newPayoutEnv(t, config.Lottery{Enabled: true})

	const userId = 7101
	ball := seedActivity(t, gdb, func(a *Activity) {
		a.Kind = KindDraw
		a.DrawMode = DrawModeBall
		a.Algo = AlgoV2
		a.Status = StatusFinished
		a.Outcome = OutcomeDrawn
		a.BallRedPool = 12
		a.BallRedPick = 3
		a.BallBluePool = 4
		a.BallBluePick = 1
		a.BallResult = "08,10,11|03"
	})
	// 一场**已取消**的双色球:reveal 从未执行,ball_result 恒为空串。
	// 前端正是靠这一条把「没中」与「还没开奖 / 已退款」分开,
	// 在这一行上写「未中奖」就是把退款说成输钱。
	voided := seedActivity(t, gdb, func(a *Activity) {
		a.Kind = KindDraw
		a.DrawMode = DrawModeBall
		a.Algo = AlgoV2
		a.Status = StatusFinished
		a.Outcome = OutcomeCancelled
		a.BallRedPool = 12
		a.BallRedPick = 3
		a.BallBluePool = 4
		a.BallBluePick = 1
	})
	// 一场普通抽奖:这两个字段在非双色球上必须是干净的零值,
	// 前端按 pick 是否非空决定要不要出这两列。
	rank := seedActivity(t, gdb, func(a *Activity) {
		a.Kind = KindDraw
		a.DrawMode = DrawModeRank
		a.Status = StatusFinished
		a.Outcome = OutcomeDrawn
	})

	seedBallTicket(t, ball, userId, 1, "10,11,12|04")
	seedBallTicket(t, voided, userId, 1, "01,02,03|01")
	seedBallTicket(t, rank, userId, 1, "")

	me := userId
	items := listMyEntries(t, ballVisibilityEnv(t, &me))
	require.Len(t, items, 3)

	byAct := make(map[string]myEntryView, len(items))
	for _, item := range items {
		byAct[item.ActNo] = item
	}

	drawn := byAct[ball.ActNo]
	assert.Equal(t, "10,11,12|04", drawn.Pick)
	require.Equal(t, "08,10,11|03", drawn.BallResult,
		"「我的参与」少了开奖号,这张表就回答不了「我中了没有」")
	assert.Equal(t, DrawModeBall, drawn.DrawMode)

	cancelled := byAct[voided.ActNo]
	assert.Equal(t, "01,02,03|01", cancelled.Pick)
	assert.Empty(t, cancelled.BallResult,
		"取消的场次从未开出号码,这里必须是空串 —— 前端据此才不会写「未中奖」")
	assert.Equal(t, DrawModeBall, cancelled.DrawMode)

	plain := byAct[rank.ActNo]
	assert.Empty(t, plain.Pick)
	assert.Empty(t, plain.BallResult, "普通抽奖不该出现开奖号")
	assert.Equal(t, DrawModeRank, plain.DrawMode)
}

// TestNonBallDetailHasNoTickets 钉住"只有双色球才列票"。
//
// 非双色球的 pick 恒为空串,列出来是一排一格内容都没有的行;而这一段查询
// 每次详情请求都要多打两条 SQL,给它一个不设条件的入口是白花的钱。
func TestNonBallDetailHasNoTickets(t *testing.T) {
	gin.SetMode(gin.TestMode)
	gdb := newPayoutEnv(t, config.Lottery{Enabled: true})

	const userId = 7201
	rank := seedActivity(t, gdb, func(a *Activity) {
		a.Kind = KindDraw
		a.DrawMode = DrawModeRank
		a.Status = StatusFinished
		a.Outcome = OutcomeDrawn
	})
	seedBallTicket(t, rank, userId, 1, "")

	me := userId
	detail := getActivityDetail(t, ballVisibilityEnv(t, &me), rank.ActNo)
	assert.Empty(t, detail.MyTickets, "普通抽奖不该列票")
	assert.Equal(t, 1, detail.MyEntryCount,
		"「我参与了几次」与「列出哪几张票」是两件事,前者对每种玩法都要给")
}
