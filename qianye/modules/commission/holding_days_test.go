package commission

import (
	"context"
	"sort"
	"testing"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/model"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

// holding_days_test.go —— 「改成熟期当天」的口径回归。
//
// 缺陷形状:日聚合桶的 ON CONFLICT DoUpdates 只累加 base_quota / gross_amount,
// **不写 mature_at**。成熟期不进幂等键的话,运营中午把 holding_days 从 7 改成 0,
// 当天已经建过桶的下线在那之后产生的消费会被累加进一行标着旧成熟期的记录里 ——
// 那部分钱按旧策略再压 7 天,而管理端与用户端都按新配置显示 T+1。
//
// 三条恒等式(base × rate = gross、gross = settled + outstanding、usd_rate
// 加权平均)在这种情形下**全部成立**,没有任何降级计数器会响。

// accrualsOfInvitee 读出某个下线名下的全部计佣行,按 mature_at 升序。
//
// 与 accrualsOfInviteeByDay 的区别正是本文件要测的东西:那个按 bucket_date
// 归并成一行,而"同一天里改过成熟期"恰恰会在同一个 bucket_date 上落**两行**。
func accrualsOfInvitee(t *testing.T, gdb *gorm.DB, inviteeId int) []Accrual {
	t.Helper()
	var rows []Accrual
	require.NoError(t, gdb.Where("invitee_id = ?", inviteeId).Find(&rows).Error)
	sort.Slice(rows, func(i, j int) bool { return rows[i].MatureAt < rows[j].MatureAt })
	return rows
}

// TestBucketMatureAtIsFrozenPerHoldingDays 是"改成熟期当天"的账本侧回归。
//
// 场景:同一个自然日里消费两次,中间运营把 holding_days 从 7 改成 0。
//
// 期望(独立算出来的,不是从实现读回来的):
//
//	日键 D 的日界 = dayKeyStart(D),桶封板 = 日界 + 86400
//	改配置**之前**那笔    mature_at = 封板 + 7×86400
//	改配置**之后**那笔    mature_at = 封板 + 0
//	两笔各自成行,base_quota 各 10000,谁也没被并进谁
//
// 变异验证:
//   - 把 consumeIdemKey 里的 ":h" 段删掉 → 只剩一行、base 20000、
//     mature_at 停在旧的 +7 天,两条 Len 断言与 mature_at 断言一起红;
//   - 把 bucketMatureAt 的负数钳位删掉 → 下面那条 -3 的子用例红。
func TestBucketMatureAtIsFrozenPerHoldingDays(t *testing.T) {
	gdb := newTestDB(t)
	cfg := commissionRateConfig("10", "5")
	cfg.Commission.HoldingDays = 7
	useConfig(t, cfg)
	mainDB := useMainDB(t, &model.User{})

	const inviter, invitee = 81, 82
	now := common.GetTimestamp()
	seedUser(t, mainDB, inviter, "qy-inviter-81", 0, now-90*86400)
	seedUser(t, mainDB, invitee, "qy-downline-82", inviter, now-90*86400)

	// 固定在同一个自然日里的两个时刻,免得用例卡在日界上偶发跨桶。
	day := bucketDate(now)
	dayStartTs, ok := dayKeyStart(day)
	require.True(t, ok)
	morning := dayStartTs + 3600
	afternoon := dayStartTs + 12*3600
	sealed := dayStartTs + secondsPerDay // 桶封板时刻 = 这一天结束

	ctx := context.Background()
	require.NoError(t, accrueConsume(ctx, consumeEvent{InviteeId: invitee, Quota: 10000, At: morning}))

	// 运营中午把成熟期改成 0(立等次日到账)。走配置快照替换,与管理端
	// 改 YAML/热载走的是同一条路。
	after := commissionRateConfig("10", "5")
	after.Commission.HoldingDays = 0
	useConfig(t, after)

	require.NoError(t, accrueConsume(ctx, consumeEvent{InviteeId: invitee, Quota: 10000, At: afternoon}))

	rows := accrualsOfInvitee(t, gdb, invitee)
	require.Len(t, rows, 2,
		"改成熟期当天必须落新的一行:并进旧行意味着改配置之后挣的钱按旧成熟期压着,"+
			"而界面按新配置显示 T+1")

	assert.Equal(t, sealed+0*secondsPerDay, rows[0].MatureAt, "改配置之后那笔:封板 + 0 天")
	assert.Equal(t, sealed+7*secondsPerDay, rows[1].MatureAt, "改配置之前那笔:封板 + 7 天,不被追溯改写")
	for _, r := range rows {
		assert.Equal(t, day, r.BucketDate, "两行仍然属于同一个自然日")
		assert.EqualValues(t, 10000, r.BaseQuota, "两笔消费各自成行,金额不该互相吞并")
		assert.Equal(t, "500", r.GrossAmount.String(), "10000 × 5%")
	}
}

// TestEarliestPendingMatureAtIsLedgerFact 守用户端那个"下一笔什么时候成熟"。
//
// 这个数存在的全部理由是:成熟期逐行冻结,改配置只影响此后的消费,所以界面
// 不能拿当前配置去反算历史。它必须是账本上写着的那个时刻。
//
// 变异验证:
//   - 把 settled_amount <> gross_amount 那半条 WHERE 删掉 → 已结清的那行
//     (mature_at 更早)会被选中,第一条断言红;
//   - 把 status = accrued 删掉 → voided 那行被选中,第二条断言红。
func TestEarliestPendingMatureAtIsLedgerFact(t *testing.T) {
	gdb := newTestDB(t)
	const inviter = 91

	got, err := earliestPendingMatureAt(gdb, inviter)
	require.NoError(t, err)
	assert.EqualValues(t, 0, got, "没有在途佣金时是 0,不是某个凭空算出来的时刻")

	// 已经结清的一行:mature_at 最早,但它不该再被当成"在途"。
	seedAccrual(t, gdb, 1, func(a *Accrual) {
		a.InviterId = inviter
		a.MatureAt = 1000
		a.SettledAmount = a.GrossAmount
	})
	// 作废的一行:同样最早,同样不算。
	seedAccrual(t, gdb, 2, func(a *Accrual) {
		a.InviterId = inviter
		a.MatureAt = 2000
		a.Status = StatusVoided
	})
	// 真正在途的两行。
	seedAccrual(t, gdb, 3, func(a *Accrual) {
		a.InviterId = inviter
		a.MatureAt = 9000
	})
	seedAccrual(t, gdb, 4, func(a *Accrual) {
		a.InviterId = inviter
		a.MatureAt = 5000
	})

	got, err = earliestPendingMatureAt(gdb, inviter)
	require.NoError(t, err)
	assert.EqualValues(t, 5000, got, "取的是在途行里最早的那个 mature_at")

	// 别人的行不该串进来。
	seedAccrual(t, gdb, 5, func(a *Accrual) {
		a.InviterId = inviter + 1
		a.MatureAt = 100
	})
	got, err = earliestPendingMatureAt(gdb, inviter)
	require.NoError(t, err)
	assert.EqualValues(t, 5000, got)
}
