package model

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// 幂等键里客户端可控那一段的规范化规则,是全站唯一的定义。
//
// 它同时解决两件事:
//   - 大小写折叠 —— MySQL 的库默认排序规则(0900_ai_ci / general_ci)大小写
//     不敏感,PostgreSQL / SQLite 按字节比较。不折叠的话同一对请求在两种
//     官方支持的方言上给出相反的资金结果。
//   - 字符集收紧 —— 折叠救不了重音不敏感('café' 与 'cafe' 在 MySQL 上仍是
//     同一个键),只能靠不让非 ASCII 进来。
func TestNormalizeIdemClientKey(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want string
		ok   bool
	}{
		{"小写原样", "abc-123_x", "abc-123_x", true},
		{"大写折成小写", "ABC-123_X", "abc-123_x", true},
		{"大小写混排折成同一个值", "AbC-123_x", "abc-123_x", true},
		{"典型大写 UUID", "3F2504E0-4F89-11D3-9A0C-0305E82C3301", "3f2504e0-4f89-11d3-9a0c-0305e82c3301", true},
		{"首尾空白被吃掉", "  abc  ", "abc", true},
		{"空串不能当键", "", "", false},
		{"纯空白不能当键", "   ", "", false},
		{"带重音的字母被拒", "café", "", false},
		{"中文被拒", "订单一", "", false},
		{"井号被拒(它是服务端派生位的分隔符)", "abc#1", "", false},
		{"冒号被拒(它是键的分段符)", "a:b", "", false},
		{"空格在中间被拒", "a b", "", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, ok := NormalizeIdemClientKey(tc.in)
			assert.Equal(t, tc.ok, ok)
			assert.Equal(t, tc.want, got)
		})
	}
}

// 只差大小写的两个输入必须折成同一个值 —— 这一条就是 MySQL CI 排序规则的
// 语义,把它搬进代码之后 PostgreSQL 与 SQLite 才跟上。
func TestNormalizeIdemClientKeyCollapsesCaseVariantsIdentically(t *testing.T) {
	a, okA := NormalizeIdemClientKey("Tr-Case-A")
	b, okB := NormalizeIdemClientKey("TR-CASE-A")
	c, okC := NormalizeIdemClientKey("tr-case-a")
	require.True(t, okA && okB && okC)
	assert.Equal(t, a, b)
	assert.Equal(t, b, c)
}

// 规范化之后的值必须落在"三方言比较一致"的字符集里,否则折叠本身没有意义。
func TestNormalizedKeysAreCollationNeutral(t *testing.T) {
	for _, raw := range []string{"ABC", "a-b_c", "3F2504E0-4F89", "000"} {
		got, ok := NormalizeIdemClientKey(raw)
		require.True(t, ok, raw)
		assert.Truef(t, IsCollationNeutralIdemKey(got), "%q 规范化成 %q 之后仍不是中立键", raw, got)
	}
}

func TestIsCollationNeutralIdemKey(t *testing.T) {
	cases := []struct {
		in   string
		want bool
	}{
		{"lt20260826-abc:crid", true},
		{"lt20260826-abc:crid#12", true},
		{"1:abc-def", true},
		{"h:0123456789abcdef", true},
		{"", false},
		{"LT20260826:crid", false},
		{"lt:café", false},
		{"lt:a b", false},
	}
	for _, tc := range cases {
		assert.Equalf(t, tc.want, IsCollationNeutralIdemKey(tc.in), "%q", tc.in)
	}
}
