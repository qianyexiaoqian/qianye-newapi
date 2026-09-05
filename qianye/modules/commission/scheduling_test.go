package commission

import (
	"testing"

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
//
// # 入参顺序现在是有含义的,不能再排序
//
// 两路来源各自已经按"等得最久的在前"排好(见 pendingInviters 的说明):
// 第一路按 MIN(mature_at),第二路按 last_settled_at。合并时再按 id 排一次
// 会把这个优先级整个丢掉,退化成"id 小的永远优先",也就是活跃邀请人超过
// 批量之后 id 大的那批永远排不进来的那个饥饿。所以下面的期望值一律是
// **轮流取**的结果,不是升序。
func TestMergeInviterIdsKeepsCarryOnlySource(t *testing.T) {
	cases := []struct {
		name    string
		accrual []int
		carry   []int
		limit   int
		want    []int
		// wantTakenA/B 是各路"已经看过"的元素个数。排空循环拿它推进键集游标,
		// 所以它必须精确到个位:多算一个,那个人这一整天就一次都轮不到
		// (游标越过了他,而他并没有被结算);少算一个,下一页会把他再看一遍。
		wantTakenA int
		wantTakenB int
	}{
		// 单路来源必须原样保序:7 排在 3 前面是因为它等得更久。
		{"只有计佣行来源,保持来源顺序", []int{7, 3}, nil, 10, []int{7, 3}, 2, 0},
		{"只剩余数的邀请人必须被选中", nil, []int{42}, 10, []int{42}, 0, 1},
		// 去重丢掉的那一个照样算"看过":他已经在本页的结果里了,
		// 下一页再看一遍只会白跑一次加锁事务。
		{"两路合并去重", []int{3, 7}, []int{7, 42}, 10, []int{3, 7, 42}, 2, 2},
		{"非法 id 一律丢弃", []int{0, -1, 5}, []int{0}, 10, []int{5}, 3, 1},
		// 截断必须在合并之后做,而且要轮流取:先截第一路会让 carry 来源
		// 整批被挤掉,那正是缺陷本身"carry 永远排不上队"的另一种形态。
		// 期望里必须有 1(carry 那一路),这才是这一格的判据。
		//
		// takenA=1 是这一格的第二个判据:第一路读出来两个,只看了一个,
		// 8 必须留给下一页。旧写法"先全量合并再 merged[:limit]"会把 8
		// 算成看过 —— 他这一整天就再也不会被选中。
		{"截断发生在合并之后,两路都拿到名额", []int{9, 8}, []int{1}, 2, []int{9, 1}, 1, 1},
		{"limit 非正视为不限", []int{2}, []int{1}, 0, []int{2, 1}, 1, 1},
		{"一路取空之后剩余名额全给另一路", []int{5}, []int{1, 2, 3}, 10, []int{5, 1, 2, 3}, 1, 3},
		{"两路都空", nil, nil, 10, []int{}, 0, 0},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, takenA, takenB := mergeInviterIds(tc.accrual, tc.carry, tc.limit)
			assert.Equal(t, tc.want, got)
			assert.Equal(t, tc.wantTakenA, takenA, "第一路的游标推进量不对")
			assert.Equal(t, tc.wantTakenB, takenB, "第二路的游标推进量不对")
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
			assert.Equal(t, floor, out.Net, "选中却发不出来 = 白跑一次加锁事务")
			assert.True(t, settleNeeded(out.Net, false))

			// 差一个额度就发不出去,因此也不该被选中。
			below := computeSettlement(decimal.NewFromInt(floor-1), decimal.Zero, 0, tc.minSettle, -1)
			if floor > 1 {
				assert.EqualValues(t, 0, below.Net)
				assert.False(t, settleNeeded(below.Net, false))
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
	require.EqualValues(t, 1000, first.Net)
	require.Equal(t, "4000", first.CarryAfter.String())
	require.EqualValues(t, 4000, first.Clipped)

	// 后续每一轮都没有新增量,只有 carry。必须逐轮发满日封顶直到发完。
	carry := first.CarryAfter
	granted := first.Net
	for round := 0; round < 4; round++ {
		out := computeSettlement(carry, decimal.Zero, granted, minSettle, dailyCap)
		require.True(t, settleNeeded(out.Net, false),
			"第 %d 轮 carry-only 结算被跳过 = 这笔佣金永远发不出去", round)
		granted += out.Net
		carry = out.CarryAfter
	}
	assert.EqualValues(t, 5000, granted, "被日封顶削掉的部分必须一分不少地补发完")
	assert.True(t, carry.IsZero())
}

// TestSettleNeededSkipsEmptyRounds 是上一条的反面约束:
// 这一轮没有钱动过就不能落单,否则结算表会按
// "邀请人数 × 结算周期"无限膨胀(300 秒一轮 = 每天 288 轮)。
//
// 注意"有计佣行"不再是落单理由:那批行照样会被 absorbAccruals 吸收,
// 钱进了余数,只是不为此单独记一张全零单(见 settleNeeded 的说明)。
func TestSettleNeededSkipsEmptyRounds(t *testing.T) {
	cases := []struct {
		name    string
		net     int64
		clamped bool
		want    bool
	}{
		{"有发放", 500, false, true},
		{"有回收", -20, false, true},
		{"零发放零回收 = 不落单", 0, false, false},
		{"零发放但触顶,触顶说明只有结算单装得下", 0, true, true},
		{"有发放且触顶", 500, true, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			assert.Equal(t, tc.want, settleNeeded(tc.net, tc.clamped))
		})
	}
}
