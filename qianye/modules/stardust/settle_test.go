package stardust

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/qianye/config"
	"github.com/QuantumNous/new-api/qianye/modules/invite"

	"github.com/shopspring/decimal"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

// settle_test.go —— 消费返日结(design-15 §4.1)。
//
// 断言全部打在**账本行**上:日桶的 status / gross / 冻结档位、流水的 kind / amount / 幂等键、
// 余额行的 available / carry / hold_reason。调度层(运行记录、重试次数)只在它决定
// "钱发不发、发几次"的地方被断言。

func TestSettleTargetDayFollowsCommissionDayline(t *testing.T) {
	sep4 := time.Date(2026, 9, 4, 0, 0, 0, 0, time.UTC).Unix()
	cases := []struct {
		name      string
		offsetMin int
		now       int64
		delay     int
		runDate   string
		day       string
		ready     bool
	}{
		{"UTC 日界后 10 分钟,门槛 30 分钟:未到", 0, sep4 + 600, 30, "20260904", "20260903", false},
		{"UTC 日界后恰好 30 分钟:到了", 0, sep4 + 1800, 30, "20260904", "20260903", true},
		{"delay=0 退化为日界后第一次心跳开跑", 0, sep4, 0, "20260904", "20260903", true},
		{"日界前一秒仍是前一天", 0, sep4 - 1, 0, "20260903", "20260902", true},
		{"负的 delay 按 0 处理", 0, sep4, -5, "20260904", "20260903", true},
		{"UTC+8:09-04T15:59Z 是东八区 09-04 深夜", 480, sep4 + 15*3600 + 59*60, 30, "20260904", "20260903", true},
		{"UTC+8:09-04T16:00Z 是东八区 09-05 零点,门槛未到", 480, sep4 + 16*3600, 30, "20260905", "20260904", false},
		{"UTC+8:09-04T16:30Z 过了门槛", 480, sep4 + 16*3600 + 1800, 30, "20260905", "20260904", true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			cfg := stardustConfig(nil)
			cfg.Invite.DayOffsetMinutes = tc.offsetMin
			useConfig(t, cfg)
			runDate, day, ready := settleTargetDay(tc.now, tc.delay)
			assert.Equal(t, tc.runDate, runDate)
			assert.Equal(t, tc.day, day)
			assert.Equal(t, tc.ready, ready)
		})
	}
}

// TestAggregateDayConsumeHonoursSubscriptionSwitch 钉住 stardust.exclude_subscription_consume
// 这道闸:默认开(订阅出资不返),关掉之后同一行进基数。其余排除项在日结 e2e 里覆盖。
func TestAggregateDayConsumeHonoursSubscriptionSwitch(t *testing.T) {
	env := newTaskEnv(t, nil)
	ctx := context.Background()
	day := invite.DayKey(invite.DayStart(common.GetTimestamp()) - 1)
	start, ok := invite.DayKeyStart(day)
	require.True(t, ok)
	require.NoError(t, env.main.Create(&[]model.Log{
		consumeLog(31, start+10, 500_000, `{"billing_source":"balance"}`),
		consumeLog(31, start+20, 500_000, `{"billing_source":"subscription"}`),
	}).Error)

	rows, err := aggregateDayConsume(ctx, start, start+secondsPerDay, nil)
	require.NoError(t, err)
	assert.Equal(t, []consumeAgg{{UserId: 31, BaseQuota: 500_000}}, rows, "默认排除订阅出资")

	off := false
	useConfig(t, stardustConfig(func(s *config.Stardust) { s.ExcludeSubscriptionConsume = &off }))
	rows, err = aggregateDayConsume(ctx, start, start+secondsPerDay, nil)
	require.NoError(t, err)
	assert.Equal(t, []consumeAgg{{UserId: 31, BaseQuota: 1_000_000}}, rows, "关掉开关后订阅出资照算")
}

// TestRunSettlePaysYesterdayByGroupRateAndHoldsRiskyAccounts 是日结的整条链路:
// 抢运行记录 → 从 logs 重算昨日日桶 → 按结算时刻的分组定档 → 发放 / 暂缓 → 收口。
func TestRunSettlePaysYesterdayByGroupRateAndHoldsRiskyAccounts(t *testing.T) {
	env := newTaskEnv(t, nil)
	ctx := context.Background()
	now := common.GetTimestamp()
	runDate, day, ready := settleTargetDay(now, 0)
	require.True(t, ready, "delay=0 时任何时刻都过门槛")
	start, ok := invite.DayKeyStart(day)
	require.True(t, ok)
	at := start + 3600

	// 分组档:vip 20%;svip 整行未启用 → 回落全站 100%。
	require.NoError(t, env.ext.Create(&[]GroupRate{
		{UserGroup: "vip", ConsumeBps: bpsPtr(2000), Enabled: true},
		{UserGroup: "svip", ConsumeBps: bpsPtr(9), Enabled: false},
	}).Error)
	invalidateGroupRates()

	seedMainUser(t, env.main, model.User{Id: 11, Group: "vip", Quota: 100})
	seedMainUser(t, env.main, model.User{Id: 12})
	seedMainUser(t, env.main, model.User{Id: 13, Quota: -1})
	seedMainUser(t, env.main, model.User{Id: 14, DeletedAt: gorm.DeletedAt{Time: time.Now(), Valid: true}})
	seedMainUser(t, env.main, model.User{Id: 15, Status: common.UserStatusDisabled})
	// 16 在 users 里根本不存在;17 只有被排除的日志;18 挂在未启用的分组档上。
	seedMainUser(t, env.main, model.User{Id: 17})
	seedMainUser(t, env.main, model.User{Id: 18, Group: "svip"})
	// 12 带着上一次结转下来的余数 0.6。
	require.NoError(t, env.ext.Create(&Balance{UserId: 12, Carry: decimal.RequireFromString("0.6")}).Error)

	rows := []model.Log{
		consumeLog(11, at, 3_000_000, ""),
		consumeLog(11, at+1, 750_000, `{"billing_source":"balance"}`),
		// 以下都不该进 11 的基数:三条口径排除、一条兜底排除、一条退款、两条窗口之外。
		consumeLog(11, at+2, 500_000, `{"violation_fee":true}`),
		consumeLog(11, at+3, 500_000, `{"`+model.ChannelTestLogOtherKey+`":true}`),
		consumeLog(11, at+5, 500_000, `{"billing_source":"subscription"}`),
		consumeLog(11, start-1, 500_000, ""),
		consumeLog(11, start+secondsPerDay, 500_000, ""),
		consumeLog(12, at, 1_250_000, ""),
		consumeLog(13, at, 5_000_000, ""),
		consumeLog(14, at, 500_000, ""),
		consumeLog(15, at, 500_000, ""),
		consumeLog(16, at, 500_000, ""),
		consumeLog(17, at, 500_000, `{"violation_fee":true}`),
		consumeLog(18, at, 500_000, ""),
	}
	legacyChannelTest := consumeLog(11, at+4, 500_000, "")
	legacyChannelTest.TokenName, legacyChannelTest.TokenId = model.ChannelTestTokenName, 0
	refund := consumeLog(11, at+6, 300_000, "")
	refund.Type = model.LogTypeRefund
	rows = append(rows, legacyChannelTest, refund)
	require.NoError(t, env.main.Create(&rows).Error)

	runSettle(ctx)

	run := settleRunOf(t, env.ext, runDate)
	require.NotNil(t, run, "过了门槛的心跳必须抢下今天这一行")
	assert.Equal(t, SettleRunDone, run.Status)
	assert.Equal(t, day, run.TargetDate)
	assert.Equal(t, 1, run.Attempts)
	assert.Equal(t, 3, run.Processed)
	assert.Equal(t, 4, run.Held)
	assert.Equal(t, 0, run.Failed)
	assert.EqualValues(t, 5, run.Granted)

	acc := accrualsOfDay(t, env.ext, day)
	require.Len(t, acc, 7, "17 只有被排除的日志,不该有桶")

	// 11:vip 20%,基数 3.75M → gross 1.5 → 发 1、结转 0.5。
	a11 := acc[11]
	assert.Equal(t, AccrualSettled, a11.Status)
	assert.Equal(t, "vip", a11.UserGroup)
	assert.Equal(t, 2000, a11.RateBps)
	assert.EqualValues(t, 500_000, a11.QuotaPerUnit)
	assert.EqualValues(t, 3_750_000, a11.BaseQuota, "退款不冲减,三类排除与窗口外的行都不算")
	assert.True(t, a11.Gross.Equal(decimal.RequireFromString("1.5")), a11.Gross.String())
	assert.NotZero(t, a11.SettledAt)
	assert.Empty(t, a11.HoldReason)
	l11 := ledgerOf(t, env.ext, 11)
	require.Len(t, l11, 1)
	assert.Equal(t, a11.LedgerId, l11[0].Id)
	assert.Equal(t, string(KindConsumeRebate), l11[0].Kind)
	assert.EqualValues(t, 1, l11[0].Amount)
	assert.Equal(t, settleIdemScope, l11[0].IdemScope)
	assert.Equal(t, "sdsettle:"+runDate+":11", l11[0].IdemKey)
	assert.Equal(t, settleRefType, l11[0].RefType)
	assert.Equal(t, runDate, l11[0].RefNo)
	assert.Equal(t, 2000, l11[0].RateBps)
	assert.Equal(t, "vip", l11[0].RateGroup)
	assert.EqualValues(t, 3_750_000, l11[0].BaseQuota)
	b11 := balanceOf(t, env.ext, 11)
	require.NotNil(t, b11)
	assert.EqualValues(t, 1, b11.Available)
	assert.EqualValues(t, 1, b11.TotalEarned)
	assert.True(t, b11.Carry.Equal(decimal.RequireFromString("0.5")), b11.Carry.String())
	assert.Empty(t, b11.HoldReason)

	// 12:全站 100%,基数 1.25M → gross 2.5,加上旧余数 0.6 = 3.1 → 发 3、结转 0.1。
	a12 := acc[12]
	assert.Equal(t, AccrualSettled, a12.Status)
	assert.Equal(t, "default", a12.UserGroup)
	assert.Equal(t, 10_000, a12.RateBps)
	assert.True(t, a12.Gross.Equal(decimal.RequireFromString("2.5")), a12.Gross.String())
	l12 := ledgerOf(t, env.ext, 12)
	require.Len(t, l12, 1)
	assert.EqualValues(t, 3, l12[0].Amount)
	b12 := balanceOf(t, env.ext, 12)
	assert.EqualValues(t, 3, b12.Available)
	assert.True(t, b12.Carry.Equal(decimal.RequireFromString("0.1")), b12.Carry.String())

	// 18:分组档未启用 → 回落全站 100%,冻结的分组名仍是他自己的。
	a18 := acc[18]
	assert.Equal(t, AccrualSettled, a18.Status)
	assert.Equal(t, "svip", a18.UserGroup)
	assert.Equal(t, 10_000, a18.RateBps)
	assert.EqualValues(t, 1, balanceOf(t, env.ext, 18).Available)

	// 13 透支、14 软删、15 封禁、16 不存在:桶 held、档位与 gross 此刻冻结、不 Credit,余额行同步原因。
	for id, reason := range map[int]string{13: HoldOverdraft, 14: HoldAccountRemoved, 15: HoldAccountDisabled, 16: HoldAccountRemoved} {
		a := acc[id]
		assert.Equal(t, AccrualHeld, a.Status, "user %d", id)
		assert.Equal(t, reason, a.HoldReason, "user %d", id)
		assert.Equal(t, 10_000, a.RateBps, "user %d", id)
		assert.EqualValues(t, 500_000, a.QuotaPerUnit, "user %d", id)
		assert.Zero(t, a.LedgerId, "user %d", id)
		assert.Zero(t, a.SettledAt, "user %d", id)
		assert.Empty(t, ledgerOf(t, env.ext, id), "user %d", id)
		b := balanceOf(t, env.ext, id)
		require.NotNil(t, b, "user %d", id)
		assert.Zero(t, b.Available, "user %d", id)
		assert.Equal(t, reason, b.HoldReason, "user %d", id)
	}
	assert.True(t, acc[13].Gross.Equal(decimal.NewFromInt(10)), acc[13].Gross.String())
	assert.Nil(t, balanceOf(t, env.ext, 17), "没有桶的人不建余额行")

	// 13 的余额转正,同时全站比例改成 200%:held 桶的 gross 冻结在首次计算值,重跑按 10 发,不是 20。
	require.NoError(t, env.main.Model(&model.User{}).Where("id = ?", 13).Update("quota", 0).Error)
	useConfig(t, stardustConfig(func(s *config.Stardust) { s.QuotaPerUnit, s.ConsumeBps = 500_000, 20_000 }))

	stats, err := rerunDay(ctx, day)
	require.NoError(t, err)
	assert.Equal(t, RerunStats{Settled: 1, Held: 3, Granted: 10}, stats)
	l13 := ledgerOf(t, env.ext, 13)
	require.Len(t, l13, 1)
	assert.EqualValues(t, 10, l13[0].Amount)
	assert.Equal(t, settleIdemScope, l13[0].IdemScope)
	assert.True(t, strings.HasPrefix(l13[0].IdemKey, "sdrerun:"+day+":13:"), l13[0].IdemKey)
	assert.Equal(t, runDate, l13[0].RefNo)
	a13 := accrualsOfDay(t, env.ext, day)[13]
	assert.Equal(t, AccrualSettled, a13.Status)
	assert.Equal(t, l13[0].Id, a13.LedgerId)
	assert.Empty(t, a13.HoldReason)
	assert.True(t, a13.Gross.Equal(decimal.NewFromInt(10)), a13.Gross.String())
	b13 := balanceOf(t, env.ext, 13)
	assert.EqualValues(t, 10, b13.Available)
	assert.Empty(t, b13.HoldReason, "held 桶归零后余额行的原因清空")
	for _, id := range []int{11, 12, 18} {
		assert.Len(t, ledgerOf(t, env.ext, id), 1, "已 settled 的桶不受重跑影响(user %d)", id)
	}
	held := accrualsOfDay(t, env.ext, day)
	for _, id := range []int{14, 15, 16} {
		assert.Empty(t, ledgerOf(t, env.ext, id), "user %d", id)
		assert.Equal(t, AccrualHeld, held[id].Status, "user %d", id)
	}

	// 再跑一次:一颗星屑都发不出来。
	stats, err = rerunDay(ctx, day)
	require.NoError(t, err)
	assert.Equal(t, RerunStats{Held: 3}, stats)
	assert.Len(t, ledgerOf(t, env.ext, 13), 1)

	// 重跑的是今天的目标日:运行记录被重新武装,下一次心跳复核并收敛成 done。
	run = settleRunOf(t, env.ext, runDate)
	assert.Equal(t, SettleRunPartial, run.Status)
	assert.Equal(t, 0, run.Attempts)
	runSettle(ctx)
	run = settleRunOf(t, env.ext, runDate)
	assert.Equal(t, SettleRunDone, run.Status)
	assert.Equal(t, 1, run.Attempts)
	assert.Equal(t, 3, run.Held)
	assert.Equal(t, 0, run.Processed)

	// 尚未封口的今天与非法日键都拒绝:提前把半天的消费结成 settled,剩下半天会被 (U,D) 唯一键挡在门外。
	_, err = rerunDay(ctx, runDate)
	be, ok := AsBizError(err)
	require.True(t, ok, "%v", err)
	assert.Equal(t, codeBadRequest, be.ErrCode())
	_, err = rerunDay(ctx, "2026-09-03")
	_, ok = AsBizError(err)
	assert.True(t, ok, "%v", err)
}

// TestRunSettleRetriesPartialDayWithinAttemptCapAndRerunRearmsIt 钉住"欠着的星屑怎么补":
// 单人失败 → 当天 partial 且逐次重试;到 settleRunMaxAttempts 停手;管理端重跑发出去并把今天重新武装。
func TestRunSettleRetriesPartialDayWithinAttemptCapAndRerunRearmsIt(t *testing.T) {
	env := newTaskEnv(t, nil)
	ctx := context.Background()
	now := common.GetTimestamp()
	runDate, day, _ := settleTargetDay(now, 0)
	start, ok := invite.DayKeyStart(day)
	require.True(t, ok)
	seedMainUser(t, env.main, model.User{Id: 21})
	require.NoError(t, env.main.Create(&[]model.Log{consumeLog(21, start+10, 1_000_000, "")}).Error)
	// 余额已在上界:Credit 报 ErrOverflow,这个人的结算事务整笔回滚。
	require.NoError(t, env.ext.Create(&Balance{UserId: 21, Available: int64(common.MaxQuota)}).Error)

	for i := 1; i <= settleRunMaxAttempts; i++ {
		runSettle(ctx)
		run := settleRunOf(t, env.ext, runDate)
		require.NotNil(t, run)
		assert.Equal(t, SettleRunPartial, run.Status, "第 %d 次", i)
		assert.Equal(t, i, run.Attempts, "第 %d 次", i)
		assert.Equal(t, 1, run.Failed, "第 %d 次", i)
	}
	assert.Empty(t, ledgerOf(t, env.ext, 21))
	assert.Equal(t, AccrualComputed, accrualsOfDay(t, env.ext, day)[21].Status, "回滚之后桶仍是 computed")

	// 次数用完:即使故障已经消失,当天也再不会自动跑。
	require.NoError(t, env.ext.Model(&Balance{}).Where("user_id = ?", 21).Update("available", 0).Error)
	runSettle(ctx)
	run := settleRunOf(t, env.ext, runDate)
	assert.Equal(t, settleRunMaxAttempts, run.Attempts)
	assert.Equal(t, SettleRunPartial, run.Status)
	assert.Empty(t, ledgerOf(t, env.ext, 21))

	// 管理端重跑把钱发出去,并把今天重新武装;下一次心跳收敛成 done、不再发第二次。
	// 回滚过的桶仍是 computed,重跑先删掉它再重算,所以 Recomputed 是 1。
	stats, err := rerunDay(ctx, day)
	require.NoError(t, err)
	assert.Equal(t, RerunStats{Recomputed: 1, Settled: 1, Granted: 2}, stats)
	l := ledgerOf(t, env.ext, 21)
	require.Len(t, l, 1)
	assert.EqualValues(t, 2, l[0].Amount)
	assert.Equal(t, AccrualSettled, accrualsOfDay(t, env.ext, day)[21].Status)
	run = settleRunOf(t, env.ext, runDate)
	assert.Equal(t, SettleRunPartial, run.Status)
	assert.Equal(t, 0, run.Attempts)

	runSettle(ctx)
	run = settleRunOf(t, env.ext, runDate)
	assert.Equal(t, SettleRunDone, run.Status)
	assert.Equal(t, 1, run.Attempts)
	assert.Equal(t, 0, run.Failed)
	assert.Len(t, ledgerOf(t, env.ext, 21), 1, "已 settled 的桶不会再发")
}
