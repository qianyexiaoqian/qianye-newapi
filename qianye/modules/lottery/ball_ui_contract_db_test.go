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

// ball_ui_contract_db_test.go —— 双色球**能不能被玩到**的那几条契约。
//
// 纯函数层(ball.go)与开奖复算(fairness_v2_test.go)早就绿了,而玩法在前端
// 依然开不出来、也玩不了 —— 因为断掉的是两处接口形状:
//
//  1. 报名请求体里根本没有 pick 这个字段,于是每一张双色球票都在 acceptPick
//     被判成"选号不合法"。摇号算得再对也没有一张票能进名单。
//  2. 大厅列表不下发 draw_mode 与本期池子,于是一场双色球在卡片上与普通抽奖
//     长得一模一样,而它最要紧的那个数(可派发的池子)显示的是 Σ(amount×count)
//     —— 浮动奖档的 amount 恒为 0,一个滚了几期的大奖池会被显示成 0。
//
// 这两条都不会报错、不会有日志,只会让功能静默不存在。所以它们各要一条断言。

// TestBallEntryRequestPickReachesTheChainBytes 锁住"用户填的号码真的会进链"。
//
// 断言的是**端到端的那一段字节**:前端发出的 JSON → entryRequest → EntryInput
// → acceptPick 归一化 → 落库并进 ChainNext 的那份字符串。中间任何一步漏抄
// pick,这条都会红。
func TestBallEntryRequestPickReachesTheChainBytes(t *testing.T) {
	gin.SetMode(gin.TestMode)

	ball := &Activity{
		ActNo: "LA-ball", Kind: KindDraw, DrawMode: DrawModeBall,
		BallRedPool: 12, BallRedPick: 3, BallBluePool: 4, BallBluePick: 1,
	}
	rank := &Activity{ActNo: "LA-rank", Kind: KindDraw, DrawMode: DrawModeRank}

	cases := []struct {
		name string
		act  *Activity
		body string
		want string
		err  error
	}{
		{
			// 前端按 FormatPick 的格式提交,原样归一化后进链。
			name: "规范格式原样进链",
			act:  ball,
			body: `{"client_request_id":"c-1","pick":"03,05,12|02"}`,
			want: "03,05,12|02",
		},
		{
			// 归一化必须真的发生:乱序与缺前导零是前端最容易发出的两种形状,
			// 而回执与"我的参与"上显示的必须是**进链的那一份**,不是提交的那一份。
			name: "乱序与缺前导零都被归一化",
			act:  ball,
			body: `{"client_request_id":"c-2","pick":"12,3,5|2"}`,
			want: "03,05,12|02",
		},
		{
			name: "个数不对",
			act:  ball,
			body: `{"client_request_id":"c-3","pick":"03,05|02"}`,
			err:  errBadPickInput,
		},
		{
			name: "红球重号",
			act:  ball,
			body: `{"client_request_id":"c-4","pick":"03,03,05|02"}`,
			err:  errBadPickInput,
		},
		{
			name: "号码越出池子",
			act:  ball,
			body: `{"client_request_id":"c-5","pick":"03,05,13|02"}`,
			err:  errBadPickInput,
		},
		{
			name: "双色球必须选号",
			act:  ball,
			body: `{"client_request_id":"c-6"}`,
			err:  errBadPickInput,
		},
		{
			// 非双色球带号一律拒绝而不是静默忽略:静默忽略会让一个填了号码的
			// 请求照常成功,用户以为自己买的是那组号。
			name: "普通抽奖带号被拒绝",
			act:  rank,
			body: `{"client_request_id":"c-7","pick":"03,05,12|02"}`,
			err:  errPickNotAllowed,
		},
		{
			name: "普通抽奖不带号照常放行",
			act:  rank,
			body: `{"client_request_id":"c-8"}`,
			want: "",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var req entryRequest
			require.NoError(t, common.UnmarshalJsonStr(tc.body, &req))

			c, _ := gin.CreateTestContext(httptest.NewRecorder())
			c.Request = httptest.NewRequest(http.MethodPost, "/entries", nil)
			c.Set("id", 4001)

			in := entryInputOf(c, tc.act.ActNo, req)
			require.Equal(t, tc.act.ActNo, in.ActNo)
			require.Equal(t, 4001, in.UserId)

			got, err := acceptPick(tc.act, in)
			if tc.err != nil {
				require.ErrorIs(t, err, tc.err)
				return
			}
			require.NoError(t, err)
			assert.Equal(t, tc.want, got)
		})
	}
}

// TestHallTellsBallActivitiesApart 锁住"大厅能把双色球与普通抽奖分开"。
//
// 打的是**真实的用户端点**而不是构造 activityBrief:字段少一个、名字写错一个,
// 前端就只能显示一场看起来毫无区别的抽奖,而纯函数测试全绿。
func TestHallTellsBallActivitiesApart(t *testing.T) {
	gin.SetMode(gin.TestMode)
	gdb := newPayoutEnv(t, config.Lottery{Enabled: true})

	// 一场双色球:开局池 60000,本期已投注 20000、其中 70% 入池。
	// 于是本期可派发的池子 = 60000 + 14000 = 74000。
	ball := seedActivity(t, gdb, func(a *Activity) {
		a.Title = "双色球第 7 期"
		a.DrawMode = DrawModeBall
		a.Algo = AlgoV2
		a.SeriesNo = "LS-abc"
		a.IssueNo = 7
		a.PoolOpenQuota = 60_000
		a.PoolQuota = 20_000
		a.PoolShareBps = 7000
		a.BallRedPool = 12
		a.BallRedPick = 3
		a.BallBluePool = 4
		a.BallBluePick = 1
	})
	// 一场普通抽奖,用来证明这几个字段在非双色球上是干净的零值 ——
	// 前端正是按 draw_mode 决定要不要渲染那一整块。
	rank := seedActivity(t, gdb, func(a *Activity) {
		a.Title = "普通抽奖"
		a.DrawMode = DrawModeRank
		a.PoolQuota = 20_000
	})
	require.NoError(t, gdb.Create(&Prize{
		ActId: rank.Id, Tier: 1, Name: "一等奖", AmountQuota: 500, Count: 3,
		PrizeType: PrizeTypeQuota,
	}).Error)
	// 双色球的浮动奖档:额度恒为 0,占池 60%。prize_total_quota 因此是 0,
	// 而卡片上那个"能赢多少"必须来自 pool_open_quota。
	require.NoError(t, gdb.Create(&Prize{
		ActId: ball.Id, Tier: 1, Name: "一等奖", AmountQuota: 0, Count: 1,
		PrizeType: PrizeTypeQuota, RedMatch: 3, BlueMatch: 1, PoolShareBps: 6000,
	}).Error)

	router := gin.New()
	router.GET("/lottery/activities", func(c *gin.Context) {
		c.Set("id", 4002)
		handleListActivities(c)
	})
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/lottery/activities", nil))
	require.Equal(t, http.StatusOK, rec.Code)

	var envelope struct {
		Success bool `json:"success"`
		Data    struct {
			Items []activityBrief `json:"items"`
		} `json:"data"`
	}
	require.NoError(t, common.Unmarshal(rec.Body.Bytes(), &envelope))
	require.True(t, envelope.Success)

	byNo := make(map[string]activityBrief, len(envelope.Data.Items))
	for _, item := range envelope.Data.Items {
		byNo[item.ActNo] = item
	}
	got := byNo[ball.ActNo]
	require.Equal(t, DrawModeBall, got.DrawMode, "大厅不下发 draw_mode,双色球就与普通抽奖无从分辨")
	assert.Equal(t, "LS-abc", got.SeriesNo)
	assert.Equal(t, 7, got.IssueNo)
	assert.EqualValues(t, 74_000, got.PoolOpenQuota,
		"卡片上的奖池必须是本期真正可派发的那一份(开局基数 + 本期投注入池部分)")
	assert.Zero(t, got.PrizeTotalQuota,
		"浮动奖档的额度恒为 0,所以 prize_total_quota 对双色球恒是错的数,卡片不能用它")
	assert.Equal(t, 12, got.BallRedPool)
	assert.Equal(t, 3, got.BallRedPick)
	assert.Equal(t, 4, got.BallBluePool)
	assert.Equal(t, 1, got.BallBluePick)

	plain := byNo[rank.ActNo]
	assert.Equal(t, DrawModeRank, plain.DrawMode)
	assert.Zero(t, plain.PoolOpenQuota, "普通抽奖没有期次池,这一位必须是零而不是投注额")
	assert.EqualValues(t, 1500, plain.PrizeTotalQuota)
	assert.Zero(t, plain.BallRedPool)
}
