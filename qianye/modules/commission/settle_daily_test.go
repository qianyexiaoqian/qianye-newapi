package commission

import (
	"context"
	"testing"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/qianye/service/lease"

	"github.com/shopspring/decimal"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

// runRow 回读今天那一行运行记录;不存在时返回 nil。
func runRow(t *testing.T, gdb *gorm.DB, day string) *SettleRun {
	t.Helper()
	var rows []SettleRun
	require.NoError(t, gdb.Where("run_date = ?", day).Find(&rows).Error)
	if len(rows) == 0 {
		return nil
	}
	return &rows[0]
}

// TestDailyRunClaimsOncePerDay 钉住"今天跑过了没有"这个状态。
//
// 它必须落在库里而不是进程内变量:进程内变量重启就忘,一天可能跑很多次;
// 而多实例部署里"今天跑过了"是全站一件事,不是每个进程一件事。
//
// 重复跑本身是安全的(见 TestDailyRunRestartDoesNotDoublePay),但没有这一行
// 的话,每次重启都会把整个队列重扫一遍 —— 白打一遍库,而且日封顶的"今日已发"
// 会被反复推着走,运营看到的"今天结算了 N 次"没有任何解释。
func TestDailyRunClaimsOncePerDay(t *testing.T) {
	t.Run("同一天只抢得到一次", func(t *testing.T) {
		gdb := newTestDB(t)
		useConfig(t, dayConfig(0))
		now := common.GetTimestamp()
		day := dayKey(now)

		ok, err := claimDailyRun(day, now)
		require.NoError(t, err)
		require.True(t, ok, "第一次心跳必须抢得到")

		ok, err = claimDailyRun(day, now)
		require.NoError(t, err)
		assert.False(t, ok, "同一天抢到第二次 = 队列被重扫,日封顶窗口被推着走")

		row := runRow(t, gdb, day)
		require.NotNil(t, row)
		assert.Equal(t, settleRunRunning, row.Status)
		assert.Equal(t, 1, row.Attempts)
		assert.Equal(t, lease.Holder(), row.Holder)
	})

	t.Run("跑完的那一天不再被抢", func(t *testing.T) {
		gdb := newTestDB(t)
		useConfig(t, dayConfig(0))
		now := common.GetTimestamp()
		day := dayKey(now)

		require.NoError(t, func() error { _, err := claimDailyRun(day, now); return err }())
		require.NoError(t, finishDailyRun(day, drainStats{Drained: true}, now))
		require.Equal(t, settleRunDone, runRow(t, gdb, day).Status)

		ok, err := claimDailyRun(day, now)
		require.NoError(t, err)
		assert.False(t, ok, "已经 done 的一天被重抢")
	})

	t.Run("没排空或有人失败的那一天当天重试,但有次数上界", func(t *testing.T) {
		gdb := newTestDB(t)
		useConfig(t, dayConfig(0))
		now := common.GetTimestamp()
		day := dayKey(now)

		// 第一次:有人报错 → partial。
		ok, err := claimDailyRun(day, now)
		require.NoError(t, err)
		require.True(t, ok)
		require.NoError(t, finishDailyRun(day, drainStats{Drained: true, Failed: 1}, now))
		require.Equal(t, settleRunPartial, runRow(t, gdb, day).Status,
			"有人失败却标成 done = 这一天欠的钱再也不会被补上")

		for attempt := 2; attempt <= settleRunMaxAttempts; attempt++ {
			ok, err := claimDailyRun(day, now)
			require.NoError(t, err)
			require.True(t, ok, "第 %d 次重试必须抢得到", attempt)
			require.Equal(t, attempt, runRow(t, gdb, day).Attempts)
			require.NoError(t, finishDailyRun(day, drainStats{Drained: true, Failed: 1}, now))
		}

		ok, err = claimDailyRun(day, now)
		require.NoError(t, err)
		assert.False(t, ok, "重试没有上界 = 一个持续失败的邀请人会让队列被重跑一整天")
	})

	t.Run("心跳停了的运行可以被接管接着跑", func(t *testing.T) {
		gdb := newTestDB(t)
		useConfig(t, dayConfig(0))
		now := common.GetTimestamp()
		day := dayKey(now)

		ok, err := claimDailyRun(day, now)
		require.NoError(t, err)
		require.True(t, ok)

		// 进程被 kill:status 停在 running,心跳不再刷新。
		require.NoError(t, gdb.Model(&SettleRun{}).Where("run_date = ?", day).
			Update("heartbeat_at", now-settleRunStaleSecs-1).Error)

		ok, err = claimDailyRun(day, now)
		require.NoError(t, err)
		require.True(t, ok, "心跳已停却没人敢接手 = 当天剩下的人一分钱都拿不到")
		row := runRow(t, gdb, day)
		assert.Equal(t, 2, row.Attempts)
		assert.EqualValues(t, 0, row.FinishedAt)
	})

	t.Run("心跳还新鲜的运行不许被抢", func(t *testing.T) {
		gdb := newTestDB(t)
		useConfig(t, dayConfig(0))
		now := common.GetTimestamp()
		day := dayKey(now)

		ok, err := claimDailyRun(day, now)
		require.NoError(t, err)
		require.True(t, ok)
		require.NoError(t, gdb.Model(&SettleRun{}).Where("run_date = ?", day).
			Update("heartbeat_at", now-settleRunStaleSecs+1).Error)

		ok, err = claimDailyRun(day, now)
		require.NoError(t, err)
		assert.False(t, ok, "另一个节点还在跑就被抢 = 同一批人被两个节点同时结算")
	})

	t.Run("跨日必须重新开一行", func(t *testing.T) {
		gdb := newTestDB(t)
		useConfig(t, dayConfig(0))
		now := common.GetTimestamp()
		day := dayKey(now)

		ok, err := claimDailyRun(day, now)
		require.NoError(t, err)
		require.True(t, ok)
		require.NoError(t, finishDailyRun(day, drainStats{Drained: true}, now))

		tomorrow := dayKey(nextDayStart(now))
		require.NotEqual(t, day, tomorrow)
		ok, err = claimDailyRun(tomorrow, nextDayStart(now))
		require.NoError(t, err)
		assert.True(t, ok, "跨过日界之后抢不到 = 佣金从此再也不发了")
		assert.NotNil(t, runRow(t, gdb, tomorrow))
	})
}

// TestDrainSettleEmptiesTheWholeQueue 是一日一结算最要紧的那条断言。
//
// 每 300 秒跑一轮时,"取一批 500 人就收工"是无害的:5 分钟后还有 287 次机会。
// 改成一天一次之后,同一句话变成**第 501 个人要等到明天** —— 600 个活跃邀请人
// 就是天天有 100 人延后一天,而且延后的是谁完全取决于排序键,不会有任何信号。
//
// 所以这里seed 的人数刻意跨过 settleInviterBatch:排空必须循环取批直到取空。
func TestDrainSettleEmptiesTheWholeQueue(t *testing.T) {
	gdb := newTestDB(t)
	useConfig(t, dayConfig(0))
	useMoneyGlobals(t, 7, 500000)

	const total = settleInviterBatch + 100 // 600:一个批量装不下
	rows := make([]Balance, 0, total)
	now := common.GetTimestamp()
	for i := 1; i <= total; i++ {
		rows = append(rows, Balance{
			UserId:          i,
			UnsettledAmount: decimal.NewFromInt(4000),
			AvailableFiat:   decimal.Zero,
			CreatedAt:       now,
			UpdatedAt:       now,
		})
	}
	require.NoError(t, gdb.CreateInBatches(rows, 200).Error)

	st := drainSettle(context.Background(), dayKey(now))

	assert.True(t, st.Drained, "队列没排空 = 排在后面的人今天一分钱都拿不到")
	assert.Equal(t, total, st.Processed)
	assert.Zero(t, st.Failed)
	assert.Greater(t, st.Rounds, 1, "只取了一批就收工,那正是这次要修的东西")
	assert.LessOrEqual(t, st.Rounds, settleDrainMaxRounds)

	var unpaid int64
	require.NoError(t, gdb.Model(&Balance{}).
		Where("available_quota <> ?", 4000).Count(&unpaid).Error)
	assert.EqualValues(t, 0, unpaid, "有 %d 个人的 4000 没发出去", unpaid)

	var leftover int64
	require.NoError(t, gdb.Model(&Balance{}).
		Where("unsettled_amount <> ?", 0).Count(&leftover).Error)
	assert.EqualValues(t, 0, leftover, "还有 %d 个人的余数留在表里", leftover)
}

// TestDrainSettleContinuesPastAFailedInviter 钉住"中途失败要能续跑"。
//
// 一日一结算之下,让第 300 个人的错误吃掉后面 300 个人**当天**的佣金
// 是不可接受的:他们要等到明天,而明天那一跑同样可能撞上同一个坏行。
//
// 故障注入用的是一行读不出来的 decimal:把 unsettled_amount 写成非数字,
// lockBalance 扫描时报错,settleUser 整笔回滚。这是真实故障(库里存过脏数据、
// 迁移写坏过一列)的最小复现,不是为了让代码走某个分支而摆的姿势。
func TestDrainSettleContinuesPastAFailedInviter(t *testing.T) {
	gdb := newTestDB(t)
	useConfig(t, dayConfig(0))
	useMoneyGlobals(t, 7, 500000)
	now := common.GetTimestamp()
	day := dayKey(now)

	// user_id 1 排在最前面(两路都按次级键 user_id 升序),它必须失败。
	seedBalance(t, gdb, 1, "4000")
	seedBalance(t, gdb, 2, "4000")
	seedBalance(t, gdb, 3, "4000")
	require.NoError(t, gdb.Exec(
		`UPDATE qy_commission_balance SET unsettled_amount = 'oops' WHERE user_id = ?`, 1).Error)

	ok, err := claimDailyRun(day, now)
	require.NoError(t, err)
	require.True(t, ok)

	st := drainSettle(context.Background(), day)
	assert.Equal(t, 1, st.Failed)
	assert.Equal(t, 2, st.Processed, "排在坏行后面的人被整批放弃了")
	assert.True(t, st.Drained)

	for _, id := range []int{2, 3} {
		bal := balanceOf(t, gdb, id)
		require.NotNil(t, bal)
		assert.EqualValues(t, 4000, bal.AvailableQuota, "邀请人 %d 当天没拿到钱", id)
	}

	// 有人失败 → 这一天标成 partial → 当天还会重试。
	require.NoError(t, finishDailyRun(day, st, common.GetTimestamp()))
	row := runRow(t, gdb, day)
	require.NotNil(t, row)
	assert.Equal(t, settleRunPartial, row.Status)
	assert.Equal(t, 1, row.Failed)
	assert.Equal(t, 2, row.Processed)
	assert.Positive(t, row.Rounds)

	retry, err := claimDailyRun(day, common.GetTimestamp())
	require.NoError(t, err)
	assert.True(t, retry, "有人失败的那一天不再重试 = 这笔钱要等到明天")
}

// TestDailyRunRestartDoesNotDoublePay 是"重启不重复发钱"的直接证据。
//
// 一日一结算把"今天跑过了没有"落进了库,而落库状态的第一个问题永远是:
// 状态丢了会怎样。这里刻意把状态改回"跑到一半崩了",让第二次运行真的重跑
// 一遍整个队列,然后断言账本一个数都没变。
//
// 依据是 absorbAccruals 的幂等性:它用 CAS 把 settled_amount 写成 gross_amount,
// 而选人与吸收都只看 settled_amount <> gross_amount。这条性质是"崩溃后接着跑"
// 能成立的全部前提,所以必须被直接钉住,不能只是假设。
func TestDailyRunRestartDoesNotDoublePay(t *testing.T) {
	gdb := newTestDB(t)
	useConfig(t, dayConfig(0))
	useMoneyGlobals(t, 7, 500000)
	now := common.GetTimestamp()
	day := dayKey(now)

	// 用**消费日聚合行**而不是充值行,这一点是这条测试的全部要害。
	//
	// 充值/兑换码是一次性来源,absorbAccruals 吸收之后直接把它们封成 settled,
	// 于是 status 那一条件顺带兜住了重跑 —— 拿它当样本,把
	// "settled_amount <> gross_amount" 整条去掉测试也是绿的(实测 SURVIVED)。
	//
	// 日聚合桶不会被封板(它当天还会继续增长,见 absorbAccruals 的封板余量),
	// 吸收之后仍然停在 accrued。**这才是重跑真的可能重复发钱的那一行**,
	// 它唯一的护栏就是 settled_amount <> gross_amount。
	seedAccrual(t, gdb, 1, func(a *Accrual) {
		a.InviterId = 42
		a.SourceType = SourceConsume
		a.IdemScope = SourceConsume
		a.IdemKey = "consume:900:" + dayKey(now) + ":default:500"
		a.BucketDate = dayKey(now)
		a.GrossAmount = decimal.NewFromInt(5000)
		a.MatureAt = now - 3600
	})

	runSettle(context.Background())

	first := balanceOf(t, gdb, 42)
	require.NotNil(t, first)
	require.EqualValues(t, 5000, first.AvailableQuota, "第一次运行就没发对,后面的断言没有意义")

	var absorbed []Accrual
	require.NoError(t, gdb.Where("inviter_id = ?", 42).Find(&absorbed).Error)
	require.Len(t, absorbed, 1)
	require.Equal(t, StatusAccrued, absorbed[0].Status,
		"前提:这一行吸收之后仍然停在 accrued —— 它一旦被封板,本测试就退化成"+
			"『status 兜住了重跑』,再也测不到 settled_amount 这道护栏")
	// 这一条刻意用 assert 而不是 require:它记录的是"吸收有没有落库",
	// 而落库失败的**后果**是下面那几条金额断言。提前 fatal 掉,读日志的人
	// 只会看到一句"Should be true",看不到那笔钱真的被发了第二次。
	assert.True(t, absorbed[0].SettledAmount.Equal(absorbed[0].GrossAmount),
		"吸收没有落库 = 重跑会把同一笔再发一次")
	firstFiat := first.AvailableFiat.String()
	require.Equal(t, settleRunDone, runRow(t, gdb, day).Status)

	var settlementsAfterFirst int64
	require.NoError(t, gdb.Model(&Settlement{}).Count(&settlementsAfterFirst).Error)
	require.EqualValues(t, 1, settlementsAfterFirst)

	// 进程崩在半路:今天这一行停在 running,心跳早已过期。重启后的心跳会接管它。
	require.NoError(t, gdb.Model(&SettleRun{}).Where("run_date = ?", day).
		Updates(map[string]any{
			"status":       settleRunRunning,
			"finished_at":  0,
			"heartbeat_at": now - settleRunStaleSecs - 1,
		}).Error)

	runSettle(context.Background())

	after := balanceOf(t, gdb, 42)
	require.NotNil(t, after)
	assert.EqualValues(t, 5000, after.AvailableQuota, "重跑把同一笔佣金又发了一次")
	assert.EqualValues(t, 5000, after.TotalEarnedQuota)
	assert.Equal(t, firstFiat, after.AvailableFiat.String(), "法币余额被重复累加")
	assert.True(t, after.UnsettledAmount.IsZero())

	var settlements int64
	require.NoError(t, gdb.Model(&Settlement{}).Count(&settlements).Error)
	assert.EqualValues(t, 1, settlements, "重跑多落了一张结算单")

	// 接管本身必须留痕:attempts 记着这一天被跑了两次。
	row := runRow(t, gdb, day)
	assert.Equal(t, 2, row.Attempts)
	assert.Equal(t, settleRunDone, row.Status)
}

// TestManualSettleIgnoresDailyRunState 钉住"一日一结算是自动调度的节奏,
// 不是对手动的限制"。
//
// 运营在管理端点「立即结算」,常常正是因为今天那一跑出了问题。这条路要是
// 被"今天已经跑过了"挡住,那就恰好在最需要它的时候不可用。
func TestManualSettleIgnoresDailyRunState(t *testing.T) {
	gdb := newTestDB(t)
	useConfig(t, dayConfig(0))
	useMoneyGlobals(t, 7, 500000)
	now := common.GetTimestamp()
	day := dayKey(now)

	runSettle(context.Background())
	require.Equal(t, settleRunDone, runRow(t, gdb, day).Status, "前提:今天已经跑完了")

	// 今天这一跑之后才成熟的一笔(holding_days 到期与那一跑错开)。
	seedAccrual(t, gdb, 1, func(a *Accrual) {
		a.InviterId = 77
		a.GrossAmount = decimal.NewFromInt(3000)
		a.MatureAt = now - 1
	})

	require.NoError(t, settleOne(77))

	bal := balanceOf(t, gdb, 77)
	require.NotNil(t, bal)
	assert.EqualValues(t, 3000, bal.AvailableQuota, "今天跑过了就不许手动结算 = 运营最需要它时它不可用")

	// 手动结算不该动今天那一行运行记录:它记的是自动调度跑了什么。
	row := runRow(t, gdb, day)
	assert.Equal(t, settleRunDone, row.Status)
	assert.Equal(t, 1, row.Attempts)
	assert.Zero(t, row.Processed)
}

// TestDailySettleSnapshotAnswersThePanelQuestions 钉住健康面板那一段真的能
// 回答那四个问题:今天跑过了吗、跑了多久、处理了多少人、有没有中途失败。
//
// 只有 pending_inviters 的时候这四个问题一个都答不了:一天里绝大部分时间
// 它都是"有一堆人等着",而"今天那一跑挂在半路"在它上面长得一模一样。
func TestDailySettleSnapshotAnswersThePanelQuestions(t *testing.T) {
	gdb := newTestDB(t)
	useConfig(t, dayConfig(480))
	now := common.GetTimestamp()
	day := dayKey(now)

	t.Run("还没跑过", func(t *testing.T) {
		snap := dailySettleSnapshot(now)
		assert.Equal(t, day, snap["today"])
		assert.Equal(t, false, snap["ran_today"])
		assert.Equal(t, 480, snap["day_offset_minutes"], "面板必须说清用的是哪个日界")
		assert.Equal(t, nextDayStart(now), snap["next_run_after"])
		assert.NotContains(t, snap, "current")
	})

	t.Run("跑完之后四个问题都答得上", func(t *testing.T) {
		ok, err := claimDailyRun(day, now-120)
		require.NoError(t, err)
		require.True(t, ok)
		require.NoError(t, finishDailyRun(day,
			drainStats{Rounds: 3, Processed: 617, Failed: 2, Drained: true, Note: "两个人报错"}, now))

		snap := dailySettleSnapshot(now)
		assert.Equal(t, false, snap["ran_today"], "有人失败却报『今天跑过了』")
		cur, _ := snap["current"].(map[string]any)
		require.NotNil(t, cur)
		assert.Equal(t, settleRunPartial, cur["status"])
		assert.EqualValues(t, 120, cur["duration_sec"], "跑了多久")
		assert.Equal(t, 617, cur["processed"], "处理了多少人")
		assert.Equal(t, 2, cur["failed"], "有没有中途失败")
		assert.Equal(t, "两个人报错", cur["remark"])
	})

	t.Run("昨天那一行同屏可见", func(t *testing.T) {
		yesterday := dayKey(dayStart(now) - 1)
		require.NoError(t, gdb.Create(&SettleRun{
			RunDate: yesterday, Status: settleRunDone, Attempts: 1,
			StartedAt: now - 86400, FinishedAt: now - 86300, Processed: 500,
		}).Error)

		snap := dailySettleSnapshot(now)
		prev, _ := snap["previous"].(map[string]any)
		require.NotNil(t, prev, "昨天跑成什么样是今天才会被发现的事,面板上必须看得见")
		assert.Equal(t, yesterday, prev["run_date"])
		assert.Equal(t, settleRunDone, prev["status"])
	})
}
