package commission

import (
	"context"
	"testing"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/qianye/config"

	"github.com/shopspring/decimal"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// percap_test.go —— 单笔封顶(commission.max_per_order_quota)必须留痕。
//
// 被审计出来的形态:封顶命中时 gross 被静默削掉,而 base_quota 照常累加。
// 那一行从此 base_quota × rate_bps / 10000 ≠ gross_amount,并且没有任何
// 字段、日志、计数器记录过这次削减 —— 事后拿流水复算得出 3000、库里躺着
// 1600,"触顶少发"与"费率被人改错"在账面上完全同形。默认值 50,000,000
// 在 12% 的充值档下相当于"单笔充值 ≥ $833 即触顶",是真实站点够得着的金额。

// capConfig 给出一份带单笔封顶的配置(全局默认 充值 10% / 消费 5%)。
func capConfig(maxPerOrder int64) *config.Config {
	c := commissionRateConfig("10", "5")
	c.Commission.MaxPerOrderQuota = maxPerOrder
	return c
}

// TestCappedAccrualStaysRecomputable 是恒等式回归:
//
//	base_quota × rate_bps / 10000 == gross_amount + capped_amount
//
// 消费路径(日聚合、逐笔累加)与一单一行路径(兑换码)各测一次。
func TestCappedAccrualStaysRecomputable(t *testing.T) {
	t.Run("消费日聚合:两笔各被削一次,削减量必须一起累加", func(t *testing.T) {
		gdb := newTestDB(t)
		useConfig(t, capConfig(100))
		cacheUser(42, 0, "default")
		cacheUser(900, 42, "default")

		ctx := context.Background()
		// 10000 × 5% = 500,上限 100 ⇒ 每笔削掉 400。
		require.NoError(t, accrueConsume(ctx,
			consumeEvent{InviteeId: 900, Quota: 10000, At: common.GetTimestamp()}))
		require.NoError(t, accrueConsume(ctx,
			consumeEvent{InviteeId: 900, Quota: 10000, At: common.GetTimestamp()}))

		row := accrualOfInvitee(t, gdb, 900)
		assert.Equal(t, int64(20000), row.BaseQuota)
		assert.Equal(t, 500, row.RateUnits)
		assert.Equal(t, "200", row.GrossAmount.String(), "两笔各封到 100")
		assert.Equal(t, "800", row.CappedAmount.String(), "两笔各削掉 400,必须累加")
		assertAccrualRecomputable(t, row)
	})

	t.Run("兑换码一单一行:连'一天混了两段增量'这个借口都没有", func(t *testing.T) {
		gdb := newTestDB(t)
		useConfig(t, capConfig(100))
		cacheUser(42, 0, "default")
		cacheUser(900, 42, "default")

		// 400000 × 10%(充值档)= 40000,上限 100 ⇒ 削掉 39900。
		require.NoError(t, accrueOneShot(context.Background(), 900, 400000, decimal.Zero,
			SourceRedemption, "redemption:777", "RD777"))

		row := accrualOfInvitee(t, gdb, 900)
		assert.Equal(t, "100", row.GrossAmount.String())
		assert.Equal(t, "39900", row.CappedAmount.String())
		assertAccrualRecomputable(t, row)
	})

	t.Run("没触顶的行 capped 必须是 0,不能虚记", func(t *testing.T) {
		gdb := newTestDB(t)
		useConfig(t, capConfig(1000000))
		cacheUser(42, 0, "default")
		cacheUser(900, 42, "default")

		require.NoError(t, accrueConsume(context.Background(),
			consumeEvent{InviteeId: 900, Quota: 10000, At: common.GetTimestamp()}))

		row := accrualOfInvitee(t, gdb, 900)
		assert.Equal(t, "500", row.GrossAmount.String())
		assert.True(t, row.CappedAmount.IsZero(), "0 的语义是'没被削过',不是'未知'")
		assertAccrualRecomputable(t, row)
	})
}

// TestClawbackReversesWhatWasActuallyAccrued 守住封顶的第二个资金后果。
//
// 冲正此前按 calcGross(refundQuota, origin.RateUnits) 重算,完全不看原单
// 实际计到了多少。原单被削到 100 之后,退掉同一笔消费会按 500 冲正 ——
// 上线为一笔只挣到 100 的事件被扣掉 5 倍,而 netAccrued 只在净额见底时
// 才截住,截不住中间那一大段。
func TestClawbackReversesWhatWasActuallyAccrued(t *testing.T) {
	gdb := newTestDB(t)
	useConfig(t, capConfig(100))
	cacheUser(42, 0, "default")
	cacheUser(900, 42, "default")

	ctx := context.Background()
	// 三笔消费:每笔理论 500、实际各计 100,净计佣 300。
	for i := 0; i < 3; i++ {
		require.NoError(t, accrueConsume(ctx,
			consumeEvent{InviteeId: 900, Quota: 10000, At: common.GetTimestamp()}))
	}
	origin := accrualOfInvitee(t, gdb, 900)
	require.Equal(t, "300", origin.GrossAmount.String())
	require.Equal(t, "1200", origin.CappedAmount.String())

	// 退掉其中一笔(10000 quota)。按费率重算是 500(该事件实际只发了 100),
	// 等比冲正是 500 × 300/1500 = 100 —— 正好是这一笔实际发出去的钱。
	require.NoError(t, clawback(ctx, 900, 10000, "clawback:test:1", "TASK-1", "退款"))

	var rows []Accrual
	require.NoError(t, gdb.Where("invitee_id = ? AND source_type = ?", 900, SourceClawback).
		Find(&rows).Error)
	require.Len(t, rows, 1)
	assert.Equal(t, "-100", rows[0].GrossAmount.String(),
		"冲正额必须等于该事件实际计到的佣金,不是按费率重算的 500")

	net, err := netAccrued(gdb, 900, 42)
	require.NoError(t, err)
	assert.Equal(t, "200", net.String(), "冲掉一笔之后还剩两笔各 100")
}

// TestClawbackWithoutCapIsUnchanged 是等价性断言:没有封顶命中时,
// 等比冲正必须逐位退化成"按原单费率重算",否则这次修复本身就是一次改价。
func TestClawbackWithoutCapIsUnchanged(t *testing.T) {
	gdb := newTestDB(t)
	useConfig(t, capConfig(0)) // 0 = 不限制
	cacheUser(42, 0, "default")
	cacheUser(900, 42, "default")

	ctx := context.Background()
	require.NoError(t, accrueConsume(ctx,
		consumeEvent{InviteeId: 900, Quota: 10000, At: common.GetTimestamp()}))
	require.NoError(t, clawback(ctx, 900, 4000, "clawback:test:2", "TASK-2", "退款"))

	var rows []Accrual
	require.NoError(t, gdb.Where("invitee_id = ? AND source_type = ?", 900, SourceClawback).
		Find(&rows).Error)
	require.Len(t, rows, 1)
	assert.Equal(t, "-200", rows[0].GrossAmount.String(), "4000 × 5% = 200")
}

// assertAccrualRecomputable 是本模块对一条计佣行的可复算性判据。
func assertAccrualRecomputable(t *testing.T, row Accrual) {
	t.Helper()
	want := calcGross(row.BaseQuota, row.RateUnits)
	got := row.GrossAmount.Add(row.CappedAmount)
	assert.True(t, want.Equal(got),
		"base(%d) × rate(%d) 应为 %s,而 gross(%s) + capped(%s) = %s",
		row.BaseQuota, row.RateUnits, want.String(),
		row.GrossAmount.String(), row.CappedAmount.String(), got.String())
}
