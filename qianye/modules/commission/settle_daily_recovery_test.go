package commission

import (
	"testing"

	"github.com/QuantumNous/new-api/common"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestRearmDailyRunGivesTodayAnotherChance 钉住"重试次数烧完之后还有救"。
//
// settleRunMaxAttempts(5)用完之后,claimDailyRun 的三条路径全部卡在
// attempts < 5 上:即使失败原因已经消失,当天也再不会自动跑。实测在**完全没有
// 故障**的库上把 attempts 手工改成 5,后续心跳一个人都不结算;改回 4 就立刻
// 排空 —— 这个计数器本身就是唯一的闸门。生产默认 300 秒心跳下,一次约 25 分钟
// 的偏侧故障就能把当天剩下所有人的佣金推到明天,而运营手上只有"逐个用户手动
// 结算"这一条路。
func TestRearmDailyRunGivesTodayAnotherChance(t *testing.T) {
	gdb := newTestDB(t)
	useConfig(t, dayConfig(0))
	now := common.GetTimestamp()
	day := dayKey(now)

	claimed, err := claimDailyRun(day, now)
	require.NoError(t, err)
	require.True(t, claimed)
	require.NoError(t, finishDailyRun(day, drainStats{Failed: 1}, now))
	require.NoError(t, gdb.Model(&SettleRun{}).Where("run_date = ?", day).
		Update("attempts", settleRunMaxAttempts).Error)

	// 前提:重试次数已经烧完,故障即便消失也没人会再跑。
	claimed, err = claimDailyRun(day, now+1)
	require.NoError(t, err)
	require.False(t, claimed, "前提不成立:attempts 用完之后本来就还能抢到")

	rearmed, err := rearmDailyRun(day, now+2)
	require.NoError(t, err)
	assert.True(t, rearmed)

	claimed, err = claimDailyRun(day, now+3)
	require.NoError(t, err)
	assert.True(t, claimed, "重新排期之后必须能被下一次心跳接手")
	row := runRow(t, gdb, day)
	require.NotNil(t, row)
	assert.Equal(t, settleRunRunning, row.Status)
	assert.Equal(t, 1, row.Attempts, "重新排期之后重试预算要从头算")

	// 今天还没有运行记录时是空操作:那种情况下一次心跳本来就会跑,
	// 不该凭空建出一行"已经跑过"的记录。
	rearmed, err = rearmDailyRun(dayKey(now+86400), now)
	require.NoError(t, err)
	assert.False(t, rearmed)
	assert.Nil(t, runRow(t, gdb, dayKey(now+86400)))
}

// TestClaimDailyRunResetsARunWrittenBeforeItsOwnDay 钉住"未来日期的运行记录不许吃掉一整天"。
//
// run_date 来自 dayKey(now),而 dayKey 受 commission.day_offset_minutes 管辖。
// 把偏移往前调会让进程在今天就为未来某个 run_date 建行并跑完;偏移改回去之后,
// 那一天真正到来时三条抢占路径全部落空(status 已经是 done),**那一整天的结算
// 被永久跳过**,而面板还照常显示 ran_today=true。实测走过一遍:19:06 UTC 把偏移
// 0→291,19:09 就建出 run_date=20260819 并 done;偏移改回 0 之后,真正的
// 20260819 那一天一个人都没结算。
func TestClaimDailyRunResetsARunWrittenBeforeItsOwnDay(t *testing.T) {
	gdb := newTestDB(t)
	useConfig(t, dayConfig(0))
	now := common.GetTimestamp()
	day := dayKey(now)
	start := dayStart(now)

	// 一行"写在本结算日开始之前"的 done 记录 —— 偏移被往前调过又调回来的残留。
	require.NoError(t, gdb.Create(&SettleRun{
		RunDate:     day,
		Status:      settleRunDone,
		Holder:      "偏移调整期间的另一个口径",
		Attempts:    1,
		StartedAt:   start - 7200,
		FinishedAt:  start - 7100,
		HeartbeatAt: start - 7100,
		CreatedAt:   start - 7200,
		UpdatedAt:   start - 7100,
	}).Error)

	claimed, err := claimDailyRun(day, now)
	require.NoError(t, err)
	assert.True(t, claimed, "这一天从来没有真的跑过,必须还能被抢到")

	row := runRow(t, gdb, day)
	require.NotNil(t, row)
	assert.Equal(t, settleRunRunning, row.Status)
	assert.Equal(t, now, row.CreatedAt,
		"created_at 必须一起改成现在,否则每次心跳都会重新命中这条路径,变成每天重跑一整轮")

	require.NoError(t, finishDailyRun(day, drainStats{Drained: true}, now))
	claimed, err = claimDailyRun(day, now+1)
	require.NoError(t, err)
	assert.False(t, claimed, "重置并跑完之后,今天不该再被抢一次")
}

// TestDailySettleSnapshotNeverLabelsAFutureRunAsYesterday 钉住面板那一格不许给反向线索。
//
// "昨天那一跑"是专门为「昨天没跑成是今天才会发现」设的。表里存在比今天更晚的
// run_date 时(偏移被调小、或多节点偏移口径不一致),不过滤就会把一个**未来**的
// 日期标成昨天,同时把真正的昨天挤出这两行的窗口 —— 运营正在排查偏移问题的
// 那一天,面板给的恰好是把凶手标成受害者。
func TestDailySettleSnapshotNeverLabelsAFutureRunAsYesterday(t *testing.T) {
	gdb := newTestDB(t)
	useConfig(t, dayConfig(0))
	now := common.GetTimestamp()
	day := dayKey(now)
	yesterday := dayKey(now - 86400)
	tomorrow := dayKey(now + 86400)

	for _, r := range []SettleRun{
		{RunDate: yesterday, Status: settleRunPartial, Failed: 3, CreatedAt: now - 86400},
		{RunDate: day, Status: settleRunDone, CreatedAt: now},
		{RunDate: tomorrow, Status: settleRunDone, Holder: "偏移被调小之后留下的未来行", CreatedAt: now},
	} {
		require.NoError(t, gdb.Create(&r).Error)
	}

	snap := dailySettleSnapshot(now)
	assert.Equal(t, day, snap["today"])
	assert.Equal(t, true, snap["ran_today"])

	cur, ok := snap["current"].(map[string]any)
	require.True(t, ok)
	assert.Equal(t, day, cur["run_date"])

	prev, ok := snap["previous"].(map[string]any)
	require.True(t, ok, "真正的昨天必须还在同一屏里")
	assert.Equal(t, yesterday, prev["run_date"],
		"未来日期的行冒充了昨天,而真正的昨天被挤出了窗口")
	assert.Equal(t, 3, prev["failed"])
}
