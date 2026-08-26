package billingexpr

import (
	"math"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// 表达式算出 ±Inf / NaN 时必须是一个 error,而不是一个"很大的数"。
//
// 守的是两条真实害链,两条都在这一层断掉:
//   - +Inf 沿结算侧走到 common.QuotaRoundChecked 会被**饱和**成 common.MaxQuota,
//     一次请求扣掉整个额度上界(默认刻度 ＄17,592,186);
//   - 带工具附加费那一支走 decimal.NewFromFloat(±Inf),shopspring/decimal 直接
//     panic,而 panic 发生在响应体已经写给客户端之后、结算之前 —— 钱扣了、
//     消费日志一行都没有。
func TestRunExprRejectsNonFiniteResults(t *testing.T) {
	cases := []struct {
		name   string
		expr   string
		params TokenParams
	}{
		{
			name:   "除零得到正无穷",
			expr:   `tier("t", p * 3 + c * 15 + (cr == 0 ? 0 : 1 / (cr - 4096)))`,
			params: TokenParams{P: 1000, C: 200, Len: 5096, CR: 4096},
		},
		{
			name:   "量级过大得到正无穷,不需要写除法",
			expr:   `tier("t", p * 1e308 * 10)`,
			params: TokenParams{P: 1, C: 1, Len: 1},
		},
		{
			name:   "负无穷",
			expr:   `tier("t", 0 - p * 1e308 * 10)`,
			params: TokenParams{P: 1, C: 1, Len: 1},
		},
		{
			name:   "无穷相减得到 NaN",
			expr:   `tier("t", p * 1e308 * 10 - c * 1e308 * 10)`,
			params: TokenParams{P: 1, C: 1, Len: 1},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, _, err := RunExpr(tc.expr, tc.params)
			require.Error(t, err, "非有限结果必须报错")
			assert.Contains(t, err.Error(), "not a finite number")
			assert.Zero(t, got, "报错时返回值必须是 0,不能把 ±Inf 漏出去")
		})
	}
}

// 同一族表达式在有限的那一档必须照常放行 —— 否则上面那道闸就是把功能一起关了。
func TestRunExprStillAcceptsFiniteResults(t *testing.T) {
	cost, trace, err := RunExpr(
		`tier("t", p * 3 + c * 15 + (cr == 0 ? 0 : 1 / (cr - 4096)))`,
		TokenParams{P: 1000, C: 200, Len: 1000},
	)
	require.NoError(t, err)
	assert.Equal(t, "t", trace.MatchedTier)
	assert.Equal(t, 6000.0, cost)
	assert.False(t, math.IsInf(cost, 0))
}
