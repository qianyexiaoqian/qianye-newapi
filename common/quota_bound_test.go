package common

import (
	"math"
	"testing"

	"github.com/shopspring/decimal"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// quota_bound_test.go —— MaxQuota 抬高之后,那条推导的每一个前提都要能被跑出来。
//
// quota_math_test.go 盯的是"越界会饱和/报错"这一类行为;这里盯的是**上界这个
// 数本身选得对不对**。两者不重叠:把 MaxQuota 换成任何一个数,那边照样全绿。

// TestMaxQuotaStaysExactInFloat64 —— saturateQuota 收的是 float64,而它做的
// `int(value)` 只在 2^53 以下才精确。上界若越过 2^53,"in-range 的值原样返回"
// 这句话就不再成立:float64 表示不出相邻整数,一次结算会静默差出几个额度。
//
// 这条断言不是理论:它把边界附近的整数在 int -> float64 -> int 之间走一圈,
// 任何一步不精确都会当场对不上。
func TestMaxQuotaStaysExactInFloat64(t *testing.T) {
	require.LessOrEqual(t, int64(MaxQuota), int64(1)<<53,
		"MaxQuota 必须落在 float64 的精确整数区间内,否则 saturateQuota 的 int(value) 会失真")
	require.LessOrEqual(t, int64(MaxQuota), int64(9007199254740991),
		"MaxQuota 还必须落在 JS 的 Number.MAX_SAFE_INTEGER 之内 —— 额度是以 JSON 数字下发给管理端的")

	for _, v := range []int{
		MaxQuota - 1, MaxQuota - 2, MaxQuota / 2, MaxQuota/2 + 1, 1, 0,
		MinQuota + 1, MinQuota + 2, MinQuota / 2,
	} {
		assert.Equal(t, v, int(float64(v)), "%d 在 float64 上不精确,上界选得太大", v)
	}

	// 上界本身与 in-range 的最后一格:前者饱和,后者原样返回。差一即错位。
	assert.Equal(t, MaxQuota-1, QuotaFromFloat(float64(MaxQuota-1)))
	assert.Equal(t, MaxQuota, QuotaFromFloat(float64(MaxQuota)))
	assert.Equal(t, MinQuota+1, QuotaFromFloat(float64(MinQuota+1)))
	assert.Equal(t, MinQuota, QuotaFromFloat(float64(MinQuota)))
}

// TestMaxQuotaLeavesInt64HeadroomForEveryUncheckedMultiplier 手算那两族乘法。
//
// 全站的资金路径上,一个被 MaxQuota 夹住的值只会遇到两族**没有就地溢出检查**
// 的 int64 乘法:
//
//   - 万分比:`(PoolQuota + Amount) * PoolShareBps / 10000`
//     (qianye/modules/lottery/entry.go)。两个加数各自 ≤ MaxQuota,
//     PoolShareBps ≤ 10000,所以最坏是 2 * MaxQuota * 10^4。
//   - 名单规模:`hit * p.AmountQuota`(qianye/modules/lottery/api_admin.go)。
//     hit ≤ 全场参与上限 ≤ lottery.max_total_entries_hard,后者由
//     qianye/config 夹在 MaxQuotaWorstMultiplier。
//
// 这里逐族算出最坏乘积并断言它仍是正数且留有余量。乘积一旦回绕成负数,
// 后面每一道 `total > x` 的判定会全部通过 —— 溢出在那里不是崩溃,是静默放行。
func TestMaxQuotaLeavesInt64HeadroomForEveryUncheckedMultiplier(t *testing.T) {
	for _, tc := range []struct {
		name       string
		multiplier int64
		addends    int64 // 参与乘法的操作数最多由几个 MaxQuota 相加而来
	}{
		{name: "万分比(池子 + 单注)", multiplier: 10000, addends: 2},
		{name: "名单规模", multiplier: MaxQuotaWorstMultiplier, addends: 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			operand := tc.addends * int64(MaxQuota)
			require.Positive(t, operand, "操作数本身就已回绕")
			// 先证除法这一步:a*b 不溢出 <=> a <= MaxInt64/b。
			require.LessOrEqual(t, operand, int64(math.MaxInt64)/tc.multiplier,
				"最坏操作数 %d × 乘数 %d 会顶穿 int64", operand, tc.multiplier)
			product := operand * tc.multiplier
			assert.Positive(t, product, "乘积回绕成负数 —— 溢出在资金路径上表现为静默放行")
			assert.LessOrEqual(t, product, int64(1)<<62,
				"最坏乘积 %d 必须落在 int64 容量的一半以内", product)
		})
	}
}

// TestQuotaConversionsAgreeAtTheBoundary —— 三个换算入口在同一条边界上必须
// 给出同一个答案。它们各有各的取整规则(截断 / 四舍五入 / decimal),
// 分叉的表现是同一笔钱在预扣与结算两侧差一。
func TestQuotaConversionsAgreeAtTheBoundary(t *testing.T) {
	for _, tc := range []struct {
		name      string
		value     float64
		want      int
		wantClamp bool
	}{
		{name: "上界之下最后一格", value: float64(MaxQuota - 1), want: MaxQuota - 1},
		{name: "上界本身即饱和", value: float64(MaxQuota), want: MaxQuota, wantClamp: true},
		{name: "旧 int32 上界现在只是普通值", value: math.MaxInt32, want: math.MaxInt32},
		{name: "旧 int32 上界 +1 现在也是普通值", value: math.MaxInt32 + 1, want: math.MaxInt32 + 1},
		{name: "下界之上最后一格", value: float64(MinQuota + 1), want: MinQuota + 1},
		{name: "下界本身即饱和", value: float64(MinQuota), want: MinQuota, wantClamp: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			gotFloat, clampFloat := QuotaFromFloatChecked(tc.value)
			gotRound, clampRound := QuotaRoundChecked(tc.value)
			gotDec, clampDec := QuotaFromDecimalChecked(decimal.NewFromFloat(tc.value))

			assert.Equal(t, tc.want, gotFloat)
			assert.Equal(t, tc.want, gotRound)
			assert.Equal(t, tc.want, gotDec)
			assert.Equal(t, tc.wantClamp, clampFloat != nil)
			assert.Equal(t, tc.wantClamp, clampRound != nil)
			assert.Equal(t, tc.wantClamp, clampDec != nil)

			// Strict 变体在同一条边界上必须**报错**而不是给一个被夹住的数:
			// 抬高上界之后,一次真实请求更不可能接近它,碰到就是异常。
			_, err := QuotaFromFloatStrict(tc.value)
			if tc.wantClamp {
				assert.Error(t, err)
			} else {
				assert.NoError(t, err)
			}
		})
	}
}

// TestQuotaAddCheckedTracksTheBound —— 钱包加法的饱和点必须跟着 MaxQuota 走。
func TestQuotaAddCheckedTracksTheBound(t *testing.T) {
	for _, tc := range []struct {
		name        string
		base, delta int
		want        int
		wantClamp   bool
	}{
		{name: "旧 int32 上界附近不再饱和", base: math.MaxInt32, delta: math.MaxInt32, want: 2 * math.MaxInt32},
		{name: "顶到新上界才饱和", base: MaxQuota - 1, delta: 2, want: MaxQuota, wantClamp: true},
		{name: "恰好落在上界不算越界", base: MaxQuota - 2, delta: 2, want: MaxQuota},
		{name: "下界同理", base: MinQuota + 1, delta: -2, want: MinQuota, wantClamp: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, clamp := QuotaAddChecked(tc.base, tc.delta)
			assert.Equal(t, tc.want, got)
			assert.Equal(t, tc.wantClamp, clamp != nil)
		})
	}
}
