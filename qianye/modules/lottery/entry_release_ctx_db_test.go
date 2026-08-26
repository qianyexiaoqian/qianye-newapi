package lottery

import (
	"context"
	"testing"

	"github.com/QuantumNous/new-api/qianye/config"
	qymodel "github.com/QuantumNous/new-api/qianye/model"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// 预占回滚必须切断调用方的取消链。
//
// 这是全链路上唯一一处收尾还沿用调用方 ctx 的地方,而它恰好在**预算已经耗尽**
// 的那一支上被调用:整批多注共用一份按注数给的预算,预算在 ChargeEntry 内部
// 到期时 twophase 用自己的 settleContext 把资金单置成 failed(写得进去),
// 回到 releaseEntryOnFailure 时调用方 ctx 已经过期,第一条语句就
// context deadline exceeded。留下的那条 status=pending 的孤儿票会让 checkCaps
// 对这个用户在**这一场**一律返回 errEntryInFlight —— 恰恰是回执里那句
// 「剩下的没有扣费,可以再提交一次」指的那次重提,而且没有任何自动出口
// (Compensate 只扫 pending/in_doubt 的资金单,这一支的资金单终态是 failed)。
//
// 这条用例把它做成确定性的:直接给一个**已经取消**的 ctx,断言票仍然被回滚。
// 仓库自带的 TestEntryBatchTruncatesSafelyWhenBudgetRunsOut 覆盖同一件事,
// 但它靠真实计时踩窗口,改造前也只有约四分之一的概率会红。
func TestReleaseEntryOnFailureRollsBackWithACancelledContext(t *testing.T) {
	lot := config.Lottery{
		Enabled: true, PayoutMaxAttempts: 8,
		EntryCloseGraceSeconds: 0, RevealDelaySeconds: 0,
		MaxStakeQuota: 5_000_000, MaxTotalPrizeQuota: 5_000_000,
		MaxActiveActivities: 16, MaxPrizeTiers: 8, MaxOptions: 8,
		MaxTotalEntriesHard: 50_000, EntryBatchMaxMs: 120_000,
	}
	ext, _ := newFileBackedEnv(t, lot, 5_000_000)

	act := seedBallActivity(t, ext, func(a *Activity) {
		a.MaxPicksPerRequest = 10
		a.MaxTotalEntries = 50_000
	})

	pending := &Entry{
		ActId: act.Id, EntryNo: "LE-RELEASE-PROBE-1", Seq: 1,
		UserId: ballE2EUserId, Amount: act.StakeQuota,
		Status: EntryPending, OrderNo: "LT-RELEASE-PROBE-ORDER",
		IdemKey: "release-probe-1",
	}
	require.NoError(t, ext.Create(pending).Error)

	order := &qymodel.FundOrder{
		OrderNo: "LT-RELEASE-PROBE-ORDER",
		Status:  qymodel.StatusFailed,
		RefId:   pending.EntryNo,
	}

	// 调用方的预算已经用完 —— 这正是多注截断那一支的现场。
	cancelled, cancel := context.WithCancel(context.Background())
	cancel()

	releaseEntryOnFailure(cancelled, order, pending, errBatchBudget)

	var got Entry
	require.NoError(t, ext.Where("entry_no = ?", pending.EntryNo).Take(&got).Error)
	assert.Equal(t, EntryFailed, got.Status,
		"回滚沿用了已过期的 ctx —— 库里会留下一条 pending 孤儿票,"+
			"此后这个用户在本场的每一次提交都被判成「上一次还在处理中」")

	var stillPending int64
	require.NoError(t, ext.Model(&Entry{}).
		Where("act_id = ? AND status = ?", act.Id, EntryPending).Count(&stillPending).Error)
	assert.EqualValues(t, 0, stillPending, "不许留下任何 pending 条目")
}
