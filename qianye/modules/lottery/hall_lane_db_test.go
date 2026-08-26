package lottery

import (
	"sort"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// hall_lane_db_test.go —— 大厅那三张选择夹(抽奖 / 竞猜 / 双色球)。
//
// 项目方原话:「把双色球和竞猜分开选择夹,抽奖-竞猜-双色球。」
//
// 这条改动最容易静默失败的地方不是"分错了",而是"某一类活动一张夹都进不去":
// 双色球从抽奖夹里被摘出去,如果没人在另一头把它接住,库里那一批 ball 活动
// 就在用户端彻底消失 —— 接口 200、列表非空(别的活动还在)、没有任何一处报错。
// 所以下面第一条测的是**划分**(每个玩法恰好归属一张夹),而不是逐条归类。

// TestHallLanesPartitionEveryPlay 三张选择夹恰好把四种玩法分完。
//
// 期望值手写,不从 Plays / hallLanes 互相回读 —— 那等于用被测数据证明被测数据。
func TestHallLanesPartitionEveryPlay(t *testing.T) {
	assert.Equal(t, map[string][]string{
		LaneDraw:  {PlayDrawRank, PlayDrawProb},
		LaneBall:  {PlayDrawBall},
		LaneGuess: {PlayGuess},
	}, hallLanes)

	// 每个玩法恰好被一张夹覆盖:少一张 = 那类活动永远进不了大厅,
	// 多一张 = 同一场活动在两张标签里各出现一次。
	for _, play := range Plays {
		lanes := make([]string, 0, 1)
		for _, lane := range []string{LaneDraw, LaneBall, LaneGuess} {
			if laneCovers(lane, play) {
				lanes = append(lanes, lane)
			}
		}
		assert.Lenf(t, lanes, 1,
			"玩法 %s 落在 %v 张选择夹里(应恰好 1 张)", play, lanes)
	}

	// 空 lane = 不限。管理端与不带参数的旧调用靠它拿全量,
	// 若它退化成"什么都不覆盖",大厅会在没有任何报错的情况下整体空掉。
	for _, play := range Plays {
		assert.Truef(t, laneCovers("", play), "空 lane 该覆盖 %s", play)
	}
}

// TestHallQueryRejectsUnknownLane 未登记的选择夹必须 400,而不是静默退回全量。
//
// 与 phase 那条闸门同源:lane 的取值与 kind 长得像(draw / guess 同名),
// 一次"顺手按 kind 的直觉传值"就会送进 `rank`、`prob`、`Ball` 这类词。
// 静默忽略的表现是三张标签拿回同一份列表 —— 正是项目方上一轮投诉的形状。
func TestHallQueryRejectsUnknownLane(t *testing.T) {
	gdb := newHallTestDB(t)
	for _, bad := range []string{
		"rank", "prob", "Ball", "DRAW", "all", "draw_ball", "lottery",
	} {
		q, err := hallQuery(gdb, bad, "", allPlaysShown())
		assert.Nilf(t, q, "lane=%q 不该拼出查询", bad)
		require.ErrorIsf(t, err, errBadLane, "lane=%q 被静默忽略了", bad)
	}
}

// TestHallLanesCoverEveryActivityExactlyOnce 三张夹的并集 = 大厅全量,交集为空。
//
// 逐夹断言(在 TestHallQueryDropsHiddenPlays 里)只能证明每一夹拿到的是对的,
// 证明不了**没有活动掉在夹缝里**:把 hallLanes 里 LaneBall 那一行删掉,逐夹
// 断言全绿(draw 夹与 guess 夹拿到的仍然一模一样),而双色球在用户端消失。
func TestHallLanesCoverEveryActivityExactlyOnce(t *testing.T) {
	gdb := newHallTestDB(t)
	seed := []*Activity{
		playAct("L-rank", KindDraw, DrawModeRank),
		playAct("L-legacy", KindDraw, ""),
		playAct("L-prob", KindDraw, DrawModeProb),
		playAct("L-ball", KindDraw, DrawModeBall),
		playAct("L-guess", KindGuess, ""),
	}
	for _, a := range seed {
		require.NoError(t, gdb.Create(a).Error)
	}

	seen := map[string]int{}
	for _, lane := range []string{LaneDraw, LaneBall, LaneGuess} {
		q, err := hallQuery(gdb, lane, "", allPlaysShown())
		require.NoError(t, err)
		for _, no := range actNos(t, q) {
			seen[no]++
		}
	}

	got := make([]string, 0, len(seen))
	for no, times := range seen {
		assert.Equalf(t, 1, times, "%s 在 %d 张选择夹里出现", no, times)
		got = append(got, no)
	}
	sort.Strings(got)
	// 期望值手写:五行分别是四种玩法 + 存量空 draw_mode。
	assert.Equal(t,
		[]string{"L-ball", "L-guess", "L-legacy", "L-prob", "L-rank"}, got,
		"有活动掉在三张选择夹的夹缝里 —— 它在用户端彻底不可见,而接口照常 200")
}
