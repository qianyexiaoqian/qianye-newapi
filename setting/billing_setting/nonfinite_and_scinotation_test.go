package billing_setting

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// 保存闸必须挡住会算出 ±Inf / NaN 的表达式。
//
// 原先 smokeTestExpr 的唯一判据是 `result < 0`,而 `NaN < 0` 与 `+Inf < 0`
// 都是 false —— 于是一条 `1 / (cr - 4096)` 干净落库,预扣侧因为子类变量恒为 0
// 而算出有限值放行,结算侧才翻成 +Inf。
func TestSmokeTestRejectsNonFiniteExpressions(t *testing.T) {
	cases := []struct {
		name string
		expr string
	}{
		{"除零得到正无穷", `tier("t", p * 3 + c * 15 + (cr == 0 ? 0 : 1 / (cr - 4096)))`},
		{"量级过大得到正无穷", `tier("t", p * 1e308 * 10)`},
		{"无穷相减得到 NaN", `tier("t", p * 1e308 * 10 - c * 1e308 * 10)`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := SmokeTestExpr(tc.expr)
			require.Error(t, err, "非有限结果必须在保存时就被拒")
			assert.Contains(t, err.Error(), "not a finite number")
		})
	}
	require.NoError(t, SmokeTestExpr(`tier("t", p * 3 + c * 15)`), "正常表达式不许被误伤")
}

// 数字字面量的三种写法必须被同一道非负烟测覆盖。
//
// exprBoundaryValues 是 smokeTestBoundaryVectors 的唯一取值来源:阈值抓不到,
// 那一档就永远不会被评估。前端可视化编辑器的 NUMERIC_LITERAL_REGEX 明确放行
// 科学计数法与无整数位小数,并把那段文本原样拼进表达式 —— 少收一种,
// 同一条规则换个写法就能绕过保存闸。
func TestBoundaryValuesCoverEveryNumericLiteralForm(t *testing.T) {
	cases := []struct {
		name string
		expr string
		want float64
	}{
		{"十进制", `c <= 2000 ? tier("s", p*3+c*15) : tier("b", p*3+c*15-50000)`, 2000},
		{"科学计数法小写", `c <= 2e3 ? tier("s", p*3+c*15) : tier("b", p*3+c*15-50000)`, 2000},
		{"科学计数法大写", `c <= 2E3 ? tier("s", p*3+c*15) : tier("b", p*3+c*15-50000)`, 2000},
		{"带小数的科学计数法", `len == 1.23456789e8 ? tier("a", p*3-1e9) : tier("b", p*3+c*15)`, 1.23456789e8},
		{"带正号指数", `c <= 1.5e+3 ? tier("s", p*3+c*15) : tier("b", p*3+c*15-50000)`, 1500},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			assert.Contains(t, exprBoundaryValues(tc.expr), tc.want,
				"阈值必须被抓成边界向量,否则那一档永远踩不到")
			err := SmokeTestExpr(tc.expr)
			require.Error(t, err, "越过阈值之后为负的表达式必须被拒")
			assert.Contains(t, err.Error(), "< 0")
		})
	}
}

// 无整数位的小数(.5)前端同样放行,而 `\d+` 打头的老正则抓不到它。
func TestBoundaryValuesCatchLeadingDotLiterals(t *testing.T) {
	assert.Contains(t, exprBoundaryValues(`x > .5 ? 1 : 2`), 0.5)
}

// 标识符里的数字仍然不许被当成阈值 —— 这是老正则前导字符类存在的理由,
// 加上指数段之后它必须还在。
func TestBoundaryValuesStillIgnoreDigitsInsideIdentifiers(t *testing.T) {
	got := exprBoundaryValues(`tier("t", cc1h * 3 + img_o * 2)`)
	assert.NotContains(t, got, 1.0, "cc1h 里的 1 不是阈值")
	assert.Contains(t, got, 3.0)
	assert.Contains(t, got, 2.0)
}

// billing_mode 是 billing_expr 的兄弟键,此前完全没有落库前校验。
func TestValidateBillingModeJSONRejectsUnknownModes(t *testing.T) {
	require.NoError(t, ValidateBillingModeJSON(``))
	require.NoError(t, ValidateBillingModeJSON(`{"m":"ratio"}`))
	require.NoError(t, ValidateBillingModeJSON(`{"m":"tiered_expr"}`), "孤立只告警不拒,否则没有任何保存顺序能过闸")

	err := ValidateBillingModeJSON(`{"m":"totally-bogus-mode"}`)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "不是合法取值")

	err = ValidateBillingModeJSON(`not json`)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "不是合法的 JSON 对象")
}
