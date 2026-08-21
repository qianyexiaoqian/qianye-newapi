package lottery

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

// hall_phase_db_test.go —— 大厅「进行中 / 已结束」这条分区口径。
//
// # 这里守的缺陷长什么样
//
// 项目方反馈「娱乐抽奖,当前已结束和进行中没有进行区分」。原因不是 UI 上没做
// 分区 —— 两张标签一直都在 —— 而是**参数名对不上**:前端发的是
// `status=open|done`,后端读的是 `phase`,而那个 switch 没有 default 分支。
// 于是两张标签发出的是两个不同的 URL、拿回的是同一份列表(全部非草稿),
// 全链路没有任何一处报错、没有一条日志、没有一个类型能发现它。
//
// 库里实测 draft 34 / finished 64、published+locked+settling 一条都没有,
// 所以用户点开"进行中"看到的是 64 条**已经结束**的活动 —— 症状与反馈逐字吻合。
//
// # 为什么必须真的跑数据库
//
// 被测的东西整个住在 WHERE 与 ORDER BY 里。断言"源码里写了 phase 这个字符串"
// 在缺陷存在时同样为真;只有把行插进去、把 SQL 跑出来、看回来的顺序,
// 才分得清"过滤生效了"与"过滤被忽略了"。

func newHallTestDB(t *testing.T) *gorm.DB {
	t.Helper()
	return newFundTestDB(t)
}

// hallAct 造一行活动。时间四元组显式给,因为排序断言全靠它们。
func hallAct(actNo, status, outcome string, closeAt, drawAt, settledAt int64) *Activity {
	return &Activity{
		ActNo: actNo, Kind: KindDraw, Status: status, Outcome: outcome,
		Title: actNo, StakeQuota: 1000,
		OpenAt: 0, CloseAt: closeAt, DrawAt: drawAt, SettledAt: settledAt,
		Algo: AlgoV1,
	}
}

// actNos 把查询结果压成一串活动号,断言直接比这一串 —— 期望值在用例里手写,
// 不从产品代码回读。
func actNos(t *testing.T, q *gorm.DB) []string {
	t.Helper()
	rows := make([]Activity, 0, 16)
	require.NoError(t, q.Find(&rows).Error)
	out := make([]string, 0, len(rows))
	for i := range rows {
		out = append(out, rows[i].ActNo)
	}
	return out
}

// TestHallPhasePartitionsByStatus 锁住分区本身:每一段包含哪些状态。
//
// 五个状态各插一行(finished 插两行以覆盖不同 outcome),然后按段核对。
// 期望是独立数出来的,不是从 hallPhases 读的 —— 从被测数据结构回读期望值,
// 等于断言"它等于它自己"。
func TestHallPhasePartitionsByStatus(t *testing.T) {
	gdb := newHallTestDB(t)
	seed := []*Activity{
		hallAct("A-draft", StatusDraft, OutcomeNone, 100, 200, 0),
		hallAct("A-published", StatusPublished, OutcomeNone, 100, 200, 0),
		hallAct("A-locked", StatusLocked, OutcomeNone, 100, 200, 0),
		hallAct("A-settling", StatusSettling, OutcomeNone, 100, 200, 0),
		hallAct("A-drawn", StatusFinished, OutcomeDrawn, 100, 200, 300),
		hallAct("A-cancelled", StatusFinished, OutcomeCancelled, 100, 200, 301),
	}
	for _, a := range seed {
		require.NoError(t, gdb.Create(a).Error)
	}

	cases := []struct {
		name  string
		phase string
		want  []string
	}{
		{
			// 这一条是缺陷的正面:修复前 live 段返回的是下面 "不分区" 那一行的
			// 全部 5 条,里面有 2 条已经结束的活动。
			// 三行的 close_at/draw_at 全一样,所以 published 组先出,
			// 其余两行按 id 倒序(settling 后建)——这也顺带证明 id 只是并列时的
			// 稳定兜底,不再是主键序。
			name:  "进行中只有 published/locked/settling",
			phase: "live",
			want:  []string{"A-published", "A-settling", "A-locked"},
		},
		{
			name:  "已结束只有 finished",
			phase: "ended",
			want:  []string{"A-cancelled", "A-drawn"},
		},
		{
			// 不带分区参数仍然可用(全部非草稿),但草稿依旧不在里面。
			name:  "不分区时是全部非草稿",
			phase: "",
			want: []string{
				"A-cancelled", "A-drawn", "A-settling", "A-locked", "A-published",
			},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			q, err := hallQuery(gdb, "", tc.phase, allPlaysShown())
			require.NoError(t, err)
			assert.Equal(t, tc.want, actNos(t, q))
		})
	}
}

// TestHallPhaseNeverLeaksDraft 草稿在**每一条**通路上都不下发。
//
// 单独一条而不是塞进上面的表:草稿泄漏是"用户看到一份还可能被改掉的规则",
// 与分区错误不是同一量级的事,它值得一条只讲这件事的断言。分区参数、玩法参数
// 的任意组合都过一遍 —— 漏出去往往就发生在"某个参数组合下走了另一条分支"。
func TestHallPhaseNeverLeaksDraft(t *testing.T) {
	gdb := newHallTestDB(t)
	for _, a := range []*Activity{
		hallAct("D-draw-draft", StatusDraft, OutcomeNone, 100, 200, 0),
		hallAct("D-pub", StatusPublished, OutcomeNone, 100, 200, 0),
	} {
		require.NoError(t, gdb.Create(a).Error)
	}
	guessDraft := hallAct("D-guess-draft", StatusDraft, OutcomeNone, 100, 200, 0)
	guessDraft.Kind = KindGuess
	require.NoError(t, gdb.Create(guessDraft).Error)

	for _, phase := range []string{"", "live", "ended"} {
		for _, kind := range []string{"", KindDraw, KindGuess} {
			q, err := hallQuery(gdb, kind, phase, allPlaysShown())
			require.NoError(t, err)
			for _, no := range actNos(t, q) {
				assert.NotContains(t, no, "draft",
					"phase=%q kind=%q 把草稿下发给了用户", phase, kind)
			}
		}
	}
}

// TestHallPhaseRejectsUnknownPhase 是这次缺陷的**根因守卫**。
//
// 老代码是一个没有 default 的 switch:参数名一漂移就静默返回全量。这里要求
// 未登记的取值变成一次可见的 400 —— 下一次前端把 `phase` 写成别的字样,
// 大厅会当场空掉并报错,而不是安静地退回"两张标签一模一样"。
func TestHallPhaseRejectsUnknownPhase(t *testing.T) {
	gdb := newHallTestDB(t)
	for _, bad := range []string{"open", "done", "all", "LIVE", "finished"} {
		q, err := hallQuery(gdb, "", bad, allPlaysShown())
		assert.Nil(t, q, "phase=%q 不该拼出查询", bad)
		require.ErrorIs(t, err, errBadPhase, "phase=%q 被静默忽略了", bad)
	}
}

// TestHallPhaseOrdersLiveByNextDeadline 进行中:能参加的排在前面,组内按
// **下一个关键时刻**升序。
//
// 期望顺序在注释里逐条算出来:
//
//	published 组(按 close_at 升序):P-soon(1000) < P-late(9000)
//	其余组   (按 draw_at  升序):L-soon(2000) < S-late(8000)
//
// 缺陷形状是"按 id 倒序" —— 那会给出 S-late, L-soon, P-late, P-soon,
// 即今晚就截止的那一场沉到最后。
func TestHallPhaseOrdersLiveByNextDeadline(t *testing.T) {
	gdb := newHallTestDB(t)
	// 插入顺序刻意与期望顺序相反,这样"没排序"与"排对了"不会碰巧一致。
	for _, a := range []*Activity{
		hallAct("P-soon", StatusPublished, OutcomeNone, 1000, 5000, 0),
		hallAct("L-soon", StatusLocked, OutcomeNone, 500, 2000, 0),
		hallAct("P-late", StatusPublished, OutcomeNone, 9000, 9500, 0),
		hallAct("S-late", StatusSettling, OutcomeNone, 400, 8000, 0),
	} {
		require.NoError(t, gdb.Create(a).Error)
	}

	q, err := hallQuery(gdb, "", "live", allPlaysShown())
	require.NoError(t, err)
	assert.Equal(t, []string{"P-soon", "P-late", "L-soon", "S-late"}, actNos(t, q))
}

// TestHallPhaseOrdersEndedBySettledAt 已结束:最近落定的排在最前。
//
// 关键在于**不能用 draw_at**:C-cancelled 在开奖前就被取消(settled_at=100),
// 而它的 draw_at 仍停在原定的 9000 —— 按 draw_at 倒序会把一场早就退完款的
// 活动顶到"最近结束"的第一位,而昨天刚开的奖排在它后面。
func TestHallPhaseOrdersEndedBySettledAt(t *testing.T) {
	gdb := newHallTestDB(t)
	for _, a := range []*Activity{
		hallAct("C-cancelled", StatusFinished, OutcomeCancelled, 50, 9000, 100),
		hallAct("D-old", StatusFinished, OutcomeDrawn, 100, 200, 200),
		hallAct("D-new", StatusFinished, OutcomeDrawn, 300, 400, 900),
	} {
		require.NoError(t, gdb.Create(a).Error)
	}

	q, err := hallQuery(gdb, "", "ended", allPlaysShown())
	require.NoError(t, err)
	assert.Equal(t, []string{"D-new", "D-old", "C-cancelled"}, actNos(t, q))
}

// TestHallPhaseKeepsAllSixOutcomesInEnded 六种结局一条都不许被分区吞掉。
//
// 分区按 status 切,而结局住在 outcome 上 —— 一旦有人"顺手"把 ended 段改成
// 按 outcome 过滤(比如只留 drawn),五种流局/取消就会从历史里消失,
// 而那正是"历史公正查询"最需要留住的几场。
func TestHallPhaseKeepsAllSixOutcomesInEnded(t *testing.T) {
	gdb := newHallTestDB(t)
	outcomes := []string{
		OutcomeDrawn, OutcomeCancelled, OutcomeVoidMinEntries,
		OutcomeVoidNoWinner, OutcomeVoidAllCorrect, OutcomeVoidDeadline,
	}
	for i, oc := range outcomes {
		a := hallAct("O-"+oc, StatusFinished, oc, 100, 200, int64(1000-i))
		require.NoError(t, gdb.Create(a).Error)
	}

	q, err := hallQuery(gdb, "", "ended", allPlaysShown())
	require.NoError(t, err)
	got := actNos(t, q)
	require.Len(t, got, len(outcomes))
	for _, oc := range outcomes {
		assert.Contains(t, got, "O-"+oc)
	}
}

// ── 变异验证(逐条改产品代码、实测这些用例会不会红。baseline 全绿)──
//
//	G1 live 段的 statuses 里加上 StatusFinished
//	   → TestHallPhasePartitionsByStatus/进行中 红
//	G2 ended 段改按 draw_at 倒序
//	   → TestHallPhaseOrdersEndedBySettledAt 红(取消场顶到第一位)
//	G3 live 段的 order 退回 "id desc"
//	   → TestHallPhasePartitionsByStatus/进行中 + TestHallPhaseOrdersLiveByNextDeadline 红
//	G4 未知 phase 改回静默返回全量(去掉 errBadPhase)
//	   → TestHallPhaseRejectsUnknownPhase 红
//	G5 去掉 `status <> StatusDraft`
//	   → TestHallPhasePartitionsByStatus/不分区 + TestHallPhaseNeverLeaksDraft 红
//	G6 ended 段顺手多加一句 `outcome <> void_deadline`
//	   → TestHallPhaseKeepsAllSixOutcomesInEnded 红
