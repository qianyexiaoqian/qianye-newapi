package commission

import (
	"context"
	"strconv"
	"testing"

	"github.com/QuantumNous/new-api/common"

	"github.com/shopspring/decimal"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

// seedMaturedConsumeRows 给一个邀请人播 n 行已成熟、未吸收的消费日聚合计佣。
//
// 刻意用**消费日聚合**而不是充值行:充值行吸收后直接封成 settled,status 那一
// 条件顺带把重跑挡住;日聚合桶吸收后仍停在 accrued,那才是取批上界真正咬人
// 的形状。
func seedMaturedConsumeRows(t *testing.T, gdb *gorm.DB, inviterId, n int, gross string) {
	t.Helper()
	now := common.GetTimestamp()
	rows := make([]Accrual, 0, n)
	for i := 0; i < n; i++ {
		tag := strconv.Itoa(inviterId) + "-" + strconv.Itoa(i)
		rows = append(rows, Accrual{
			AccrualNo:     "CA-DRAIN-" + tag,
			IdemScope:     SourceConsume,
			IdemKey:       "consume:drain:" + tag,
			InviterId:     inviterId,
			InviteeId:     100000 + i,
			SourceType:    SourceConsume,
			BaseQuota:     10000,
			BaseMoney:     decimal.Zero,
			RateUnits:     500,
			GrossAmount:   decimal.RequireFromString(gross),
			SettledAmount: decimal.Zero,
			UsdRate:       decimal.NewFromInt(7),
			Status:        StatusAccrued,
			MatureAt:      now - 3600,
			BucketDate:    dayKey(now),
			CreatedAt:     now,
			UpdatedAt:     now,
		})
	}
	require.NoError(t, gdb.CreateInBatches(rows, 200).Error)
}

func untouchedAccruals(t *testing.T, gdb *gorm.DB, inviterId int) int64 {
	t.Helper()
	var n int64
	require.NoError(t, gdb.Model(&Accrual{}).
		Where("inviter_id = ? AND settled_amount <> gross_amount", inviterId).
		Count(&n).Error)
	return n
}

// TestSettleUserDrainAbsorbsPastTheBatchCap 钉住"一个人名下的计佣行必须一次吸收完"。
//
// settleUser 单次最多取 settleAccrualBatch(1000)行。旧的 300 秒调度下这个上界
// 无害:同一个人一天还有 287 次机会。改成一日一结算之后它变成**日级**上界 ——
// 实测给一个邀请人插 1400 行已成熟计佣,一次日结只发出 1000 行,剩下 400 行原样
// 留到明天,而这一跑照常报 status=done / failed=0 / remark 空。积压每天只消化
// 1000 行,日新增超过 1000 就永久发散,面板全程说"今天跑完了、零失败"。
func TestSettleUserDrainAbsorbsPastTheBatchCap(t *testing.T) {
	gdb := newTestDB(t)
	useConfig(t, dayConfig(0))
	useMoneyGlobals(t, 7, 500000)

	const inviter = 4242
	const rows = settleAccrualBatch + 400 // 1400:一次取批装不下
	seedMaturedConsumeRows(t, gdb, inviter, rows, "1")

	drained, err := settleUserDrain(inviter)
	require.NoError(t, err)
	assert.True(t, drained)

	assert.EqualValues(t, 0, untouchedAccruals(t, gdb, inviter),
		"还有计佣行没被吸收 —— 那正是「第 1001 行要等到明天」")

	bal := balanceOf(t, gdb, inviter)
	require.NotNil(t, bal)
	assert.EqualValues(t, rows, bal.AvailableQuota,
		"每行 gross 1,%d 行就该发满 %d", rows, rows)
	assert.Equal(t, "0", bal.UnsettledAmount.String())
}

// TestDrainSettleNeverReportsDoneWhileMoneyIsStillStuck 钉住"排空了"这个结论不许说谎。
//
// 选人 SQL 那一侧取空了,不等于钱发完了:同一个人名下的计佣行可能多到一次运行
// 都吸收不完。此时这一天必须是 partial(当天还会重试),绝不能是 done。
func TestDrainSettleNeverReportsDoneWhileMoneyIsStillStuck(t *testing.T) {
	gdb := newTestDB(t)
	useConfig(t, dayConfig(0))
	useMoneyGlobals(t, 7, 500000)
	now := common.GetTimestamp()
	day := dayKey(now)

	const inviter = 4243
	seedMaturedConsumeRows(t, gdb, inviter, settleAccrualBatch+50, "1")

	claimed, err := claimDailyRun(day, now)
	require.NoError(t, err)
	require.True(t, claimed)

	st := drainSettle(context.Background(), day)
	require.NoError(t, finishDailyRun(day, st, common.GetTimestamp()))

	assert.Zero(t, st.Capped, "取批上界必须由 settleUserDrain 自己吃掉,不该冒到调度层")
	assert.EqualValues(t, 0, untouchedAccruals(t, gdb, inviter))
	assert.Equal(t, settleRunDone, runRow(t, gdb, day).Status)

	// 反过来:真出现了"一次吸收不完"的人(Capped>0),这一天必须是 partial。
	// 这条单独钉是因为上面那一段现在永远 Capped=0 —— 它证明的是修复生效,
	// 而不是"Capped 会不会挡住 done"。
	require.NoError(t, gdb.Where("run_date = ?", day).Delete(&SettleRun{}).Error)
	claimed, err = claimDailyRun(day, common.GetTimestamp())
	require.NoError(t, err)
	require.True(t, claimed)
	require.NoError(t, finishDailyRun(day,
		drainStats{Rounds: 1, Processed: 1, Drained: true, Capped: 1},
		common.GetTimestamp()))
	after := runRow(t, gdb, day)
	assert.Equal(t, settleRunPartial, after.Status,
		"还有人的钱压着却报 done = 这一天欠的钱再也不会被自动补上")
	assert.NotEmpty(t, after.Remark, "面板上必须能看出这一天为什么没跑完")
}
