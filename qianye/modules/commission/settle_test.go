package commission

import (
	"testing"

	"github.com/QuantumNous/new-api/common"

	"github.com/shopspring/decimal"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestSmallConsumeCommissionIsNeverLost 是本模块的核心数值走查。
//
// 场景:费率 5%,每次消费 10 额度,一天 2000 次。
// 每次的佣金是 0.5,用 int(float64(10)*0.05) 会得到 0,2000 次全部归零 ——
// 用户忙活一天佣金是 0,而钱被平台吞了。全精度累计 + floor 结算必须恰好
// 入账 1000,一分不多一分不少。
func TestSmallConsumeCommissionIsNeverLost(t *testing.T) {
	const (
		rateBps = 500
		perCall = int64(10)
		calls   = 2000
	)

	t.Run("日聚合后一次结算", func(t *testing.T) {
		bucket := decimal.Zero
		for i := 0; i < calls; i++ {
			bucket = bucket.Add(calcGross(perCall, rateBps))
		}
		require.Equal(t, "1000", bucket.String())

		out := computeSettlement(decimal.Zero, bucket, 0, 1, -1)
		assert.EqualValues(t, 1000, out.NetQuota)
		assert.True(t, out.CarryAfter.IsZero(), "余数应恰好归零,实际 %s", out.CarryAfter)
		assert.Nil(t, out.Clamp)
	})

	// 最坏情况:每一次消费都立刻单独结算一轮。余数机制必须让 2000 轮的
	// 发放总额仍然精确等于 1000,否则每轮的零头就会被吃掉。
	t.Run("每次消费都单独结算", func(t *testing.T) {
		carry := decimal.Zero
		var granted int64
		for i := 0; i < calls; i++ {
			out := computeSettlement(carry, calcGross(perCall, rateBps), granted, 1, -1)
			granted += out.NetQuota
			carry = out.CarryAfter
			assert.False(t, carry.IsNegative(), "第 %d 轮余数不应为负", i)
			assert.True(t, carry.LessThan(decimal.NewFromInt(1)), "第 %d 轮余数应小于 1", i)
		}
		assert.EqualValues(t, 1000, granted)
		assert.True(t, carry.IsZero())
	})

	// 结算门槛不是"丢弃",只是"推迟"。
	t.Run("未达结算门槛也不丢失", func(t *testing.T) {
		carry := decimal.Zero
		var granted int64
		for i := 0; i < calls; i++ {
			out := computeSettlement(carry, calcGross(perCall, rateBps), granted, 1000, -1)
			granted += out.NetQuota
			carry = out.CarryAfter
		}
		assert.EqualValues(t, 1000, granted)
		assert.True(t, carry.IsZero())
	})
}

func TestComputeSettlementNeverOverpays(t *testing.T) {
	cases := []struct {
		name      string
		carry     string
		delta     string
		minSettle int64
		wantNet   int64
		wantCarry string
	}{
		{"整数正好", "0", "10", 1, 10, "0"},
		{"向下取整不四舍五入", "0", "10.9", 1, 10, "0.9"},
		{"接近进位仍不超发", "0", "10.999999999", 1, 10, "0.999999999"},
		{"余数跨轮累积", "0.6", "0.6", 1, 1, "0.2"},
		{"未达门槛全部留存", "0", "3.4", 10, 0, "3.4"},
		{"零增量零发放", "0.25", "0", 1, 0, "0.25"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			carry := decimal.RequireFromString(tc.carry)
			delta := decimal.RequireFromString(tc.delta)
			out := computeSettlement(carry, delta, 0, tc.minSettle, -1)
			assert.EqualValues(t, tc.wantNet, out.NetQuota)
			assert.Equal(t, tc.wantCarry, out.CarryAfter.String())
			// 不变量:发放额 + 剩余余数 恒等于 上轮余数 + 本轮增量。
			assert.True(t, decimal.NewFromInt(out.NetQuota).Add(out.CarryAfter).Equal(carry.Add(delta)))
		})
	}
}

// TestClawbackReclaimAndDebt 覆盖"佣金已被提现后又发生退款"这条唯一会
// 产生欠账的路径。回收只能动未提现的可用余额,绝不能倒扣主库余额。
func TestClawbackReclaimAndDebt(t *testing.T) {
	t.Run("可用余额足够时全额回收", func(t *testing.T) {
		out := computeSettlement(decimal.Zero, decimal.RequireFromString("-10.5"), 100, 1, -1)
		assert.EqualValues(t, -11, out.NetQuota)
		// 多回收的 0.5 留在余数里,下轮自动还给用户。
		assert.Equal(t, "0.5", out.CarryAfter.String())
		assert.False(t, out.CarryAfter.IsNegative())
	})

	t.Run("可用余额不足则记欠账", func(t *testing.T) {
		out := computeSettlement(decimal.Zero, decimal.RequireFromString("-10.5"), 4, 1, -1)
		assert.EqualValues(t, -4, out.NetQuota, "只能回收未提现的 4")
		assert.Equal(t, "-6.5", out.CarryAfter.String(), "剩余部分记为欠账")
		assert.True(t, out.CarryAfter.IsNegative())
	})

	t.Run("欠账被后续佣金抵扣后自动解除", func(t *testing.T) {
		out := computeSettlement(decimal.RequireFromString("-6.5"), decimal.RequireFromString("10"), 0, 1, -1)
		assert.EqualValues(t, 3, out.NetQuota)
		assert.Equal(t, "0.5", out.CarryAfter.String())
		assert.False(t, out.CarryAfter.IsNegative())
	})

	t.Run("可用余额为零时不产生负发放", func(t *testing.T) {
		out := computeSettlement(decimal.Zero, decimal.RequireFromString("-3"), 0, 1, -1)
		assert.EqualValues(t, 0, out.NetQuota)
		assert.Equal(t, "-3", out.CarryAfter.String())
	})
}

func TestComputeSettlementDailyCap(t *testing.T) {
	out := computeSettlement(decimal.Zero, decimal.RequireFromString("100.7"), 0, 1, 50)
	assert.EqualValues(t, 50, out.NetQuota)
	assert.EqualValues(t, 50, out.Clipped)
	// 被封顶削掉的部分留在余数里,明天继续发,不是作废。
	assert.Equal(t, "50.7", out.CarryAfter.String())

	exhausted := computeSettlement(decimal.Zero, decimal.RequireFromString("100.7"), 0, 1, 0)
	assert.EqualValues(t, 0, exhausted.NetQuota)
	assert.Equal(t, "100.7", exhausted.CarryAfter.String())
}

// TestComputeSettlementSaturates 确认单轮结算触顶 int32 时不会回绕成负数,
// 且未发完的部分仍然留在余数里。
func TestComputeSettlementSaturates(t *testing.T) {
	huge := decimal.NewFromInt(3_000_000_000)
	out := computeSettlement(decimal.Zero, huge, 0, 1, -1)
	require.NotNil(t, out.Clamp, "触顶必须被记录下来供审计")
	assert.EqualValues(t, common.MaxQuota, out.NetQuota)
	assert.Equal(t, huge.Sub(decimal.NewFromInt(int64(common.MaxQuota))).String(), out.CarryAfter.String())
	assert.True(t, out.CarryAfter.IsPositive())
}

func TestApplyFiatUsesFrozenRate(t *testing.T) {
	rate := decimal.RequireFromString("7.3")

	t.Run("发放按加权冻结汇率折算", func(t *testing.T) {
		bal := &Balance{AvailableQuota: 0, AvailableFiat: decimal.Zero}
		// 1000000 额度 / 500000 每单位 = 2 美元 × 7.3 = 14.6
		delta, after := applyFiat(bal, 1_000_000, rate)
		assert.Equal(t, "14.6", delta.String())
		assert.Equal(t, "14.6", after.String())
	})

	t.Run("回收按额度比例缩减", func(t *testing.T) {
		bal := &Balance{AvailableQuota: 100, AvailableFiat: decimal.RequireFromString("10")}
		delta, after := applyFiat(bal, -40, rate)
		assert.Equal(t, "6", after.String())
		assert.Equal(t, "-4", delta.String())
	})

	t.Run("回收到零则法币清零", func(t *testing.T) {
		bal := &Balance{AvailableQuota: 40, AvailableFiat: decimal.RequireFromString("10")}
		_, after := applyFiat(bal, -40, rate)
		assert.True(t, after.IsZero())
	})
}

func TestScaleFiatKeepsAverageRate(t *testing.T) {
	fiat := decimal.RequireFromString("73")
	// 冻结一半额度,法币也应恰好剩一半 —— 剩余额度对应的均价不变。
	assert.Equal(t, "36.5", scaleFiat(fiat, 500, 1000).String())
	assert.True(t, scaleFiat(fiat, 0, 1000).IsZero())
	assert.True(t, scaleFiat(fiat, 500, 0).IsZero())
}
