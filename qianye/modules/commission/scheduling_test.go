package commission

import (
	"testing"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/setting/operation_setting"

	"github.com/shopspring/decimal"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestMergeInviterIdsKeepsCarryOnlySource 锁定结算调度的选人口径。
//
// 缺陷复现:absorbAccruals 会把本批**全部** accrual 的 settled_amount 写成
// gross_amount,所以被日封顶削掉的 4000 只留在 unsettled_amount 里,而按
// "还有未被吸收的 accrual 行"选人的那一路第二轮就再也命中不了这个邀请人。
// 只剩余数的邀请人必须由第二路来源补进来,否则下线一停消费就永久拿不到。
func TestMergeInviterIdsKeepsCarryOnlySource(t *testing.T) {
	cases := []struct {
		name    string
		accrual []int
		carry   []int
		limit   int
		want    []int
	}{
		{"只有计佣行来源", []int{7, 3}, nil, 10, []int{3, 7}},
		{"只剩余数的邀请人必须被选中", nil, []int{42}, 10, []int{42}},
		{"两路合并去重", []int{3, 7}, []int{7, 42}, 10, []int{3, 7, 42}},
		{"非法 id 一律丢弃", []int{0, -1, 5}, []int{0}, 10, []int{5}},
		// 截断必须在合并之后做:先截第一路会让 carry 来源整批被挤掉,
		// 那正是缺陷本身"carry 永远排不上队"的另一种形态。
		{"截断发生在合并之后", []int{9, 8}, []int{1}, 2, []int{1, 8}},
		{"limit 非正视为不限", []int{2}, []int{1}, 0, []int{1, 2}},
		{"两路都空", nil, nil, 10, []int{}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			assert.Equal(t, tc.want, mergeInviterIds(tc.accrual, tc.carry, tc.limit))
		})
	}
}

// TestCarryFloorMatchesPayoutCondition 确认 carry-only 的选人门槛与
// computeSettlement 的发放条件是同一个数。
//
// 按 >= 1 选人的话,零头落在 1..minSettle 之间的邀请人会被每个结算周期
// 反复选中、反复加锁、又永远发不出来;按 >= minSettle 选,选中即可发。
func TestCarryFloorMatchesPayoutCondition(t *testing.T) {
	cases := []struct {
		name      string
		minSettle int64
		wantFloor int64
	}{
		{"常规门槛", 1000, 1000},
		{"门槛为 1", 1, 1},
		{"门槛未配置时退回 1", 0, 1},
		{"门槛为负时退回 1", -5, 1},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			floor := carryFloor(tc.minSettle)
			require.Equal(t, tc.wantFloor, floor)

			// 恰好等于门槛的余数必须发得出去(delta=0 的 carry-only 结算)。
			out := computeSettlement(decimal.NewFromInt(floor), decimal.Zero, 0, tc.minSettle, -1)
			assert.Equal(t, floor, out.NetQuota, "选中却发不出来 = 白跑一次加锁事务")
			assert.True(t, settleNeeded(0, out.NetQuota))

			// 差一个额度就发不出去,因此也不该被选中。
			below := computeSettlement(decimal.NewFromInt(floor-1), decimal.Zero, 0, tc.minSettle, -1)
			if floor > 1 {
				assert.EqualValues(t, 0, below.NetQuota)
				assert.False(t, settleNeeded(0, below.NetQuota))
			}
		})
	}
}

// TestSettleNeededFlushesCarryWithoutNewAccruals 是 A5 的核心断言:
// 没有新计佣行时结算也必须继续跑下去,把 carry 刷出去。
//
// 场景照抄审计报告:日封顶 1000,已成熟计佣 5000。第一轮发 1000、carry 4000;
// 第二轮起没有任何新的 accrual 行,旧实现在 len(rows)==0 处直接 return nil,
// 4000 就此停在 unsettled_amount 里。
func TestSettleNeededFlushesCarryWithoutNewAccruals(t *testing.T) {
	const dailyCap = int64(1000)
	const minSettle = int64(1)

	first := computeSettlement(decimal.Zero, decimal.NewFromInt(5000), 0, minSettle, dailyCap)
	require.EqualValues(t, 1000, first.NetQuota)
	require.Equal(t, "4000", first.CarryAfter.String())
	require.EqualValues(t, 4000, first.Clipped)

	// 后续每一轮都没有新增量,只有 carry。必须逐轮发满日封顶直到发完。
	carry := first.CarryAfter
	granted := first.NetQuota
	for round := 0; round < 4; round++ {
		out := computeSettlement(carry, decimal.Zero, granted, minSettle, dailyCap)
		require.True(t, settleNeeded(0, out.NetQuota),
			"第 %d 轮 carry-only 结算被跳过 = 这笔佣金永远发不出去", round)
		granted += out.NetQuota
		carry = out.CarryAfter
	}
	assert.EqualValues(t, 5000, granted, "被日封顶削掉的部分必须一分不少地补发完")
	assert.True(t, carry.IsZero())
}

// TestSettleNeededSkipsEmptyRounds 是上一条的反面约束:
// 没有增量、余数也不够发时不能落单,否则审计表会按
// "邀请人数 × 结算周期"无限膨胀。
func TestSettleNeededSkipsEmptyRounds(t *testing.T) {
	cases := []struct {
		name         string
		accrualCount int
		net          int64
		want         bool
	}{
		{"有计佣行即使 net 为零也要落单", 3, 0, true},
		{"有计佣行且有发放", 3, 500, true},
		{"carry-only 且发得出去", 0, 1000, true},
		{"carry-only 且是回收", 0, -20, true},
		{"carry-only 且发不出去", 0, 0, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			assert.Equal(t, tc.want, settleNeeded(tc.accrualCount, tc.net))
		})
	}
}

// TestBatchRateNeverZeroOnCarryOnlyRound 确认 carry-only 结算不会把法币余额落下。
//
// delta 为零时本批没有加权汇率可算。若退回 decimal.Zero,applyFiat 会一分
// 法币都不加而额度照加,AvailableFiat 与 AvailableQuota 就此永久漂移,
// 提现模块按 AvailableFiat 折算会少给用户钱。
func TestBatchRateNeverZeroOnCarryOnlyRound(t *testing.T) {
	original := operation_setting.USDExchangeRate
	operation_setting.USDExchangeRate = 7.3
	defer func() { operation_setting.USDExchangeRate = original }()

	t.Run("有增量时用本批加权均值", func(t *testing.T) {
		// 30 @ 6.0 + 10 @ 10.0 → (180 + 100) / 40 = 7
		weightedSum := decimal.NewFromInt(30).Mul(decimal.NewFromInt(6)).
			Add(decimal.NewFromInt(10).Mul(decimal.NewFromInt(10)))
		assert.Equal(t, "7", batchRate(weightedSum, decimal.NewFromInt(40)).String())
	})

	t.Run("carry-only 轮退回当前汇率而不是零", func(t *testing.T) {
		rate := batchRate(decimal.Zero, decimal.Zero)
		require.False(t, rate.IsZero(), "汇率留零 = 发了额度却不加法币,两边永久漂移")
		assert.Equal(t, "7.3", rate.String())

		// 折算链路端到端确认:1000000 额度 / 500000 每单位 = 2 美元 × 7.3。
		originalQPU := common.QuotaPerUnit
		common.QuotaPerUnit = 500000
		defer func() { common.QuotaPerUnit = originalQPU }()
		bal := &Balance{AvailableQuota: 0, AvailableFiat: decimal.Zero}
		delta, after := applyFiat(bal, 1_000_000, rate)
		assert.Equal(t, "14.6", delta.String())
		assert.Equal(t, "14.6", after.String())
	})
}
