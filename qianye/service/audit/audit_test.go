package audit

import (
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// B6:审计截断必须落在 rune 边界上。
//
// 扩展库 DSN 强制 charset=utf8mb4,MySQL 在 STRICT_TRANS_TABLES 下会以 1366
// 拒绝含非法 UTF-8 的整行;而 audit.Write 是 fail-open 的(只 SysError),
// 于是丢的不是理由的尾巴,而是"谁在什么时候拒了这笔提现"这条记录本身。
//
// 用例特意构造切点落在多字节字符中间的输入:纯中文串在 512 字节上限下,
// 旧实现的切点 512-14=498 不是 3 的倍数,必然切开一个汉字。
func TestTruncate_KeepsValidUTF8(t *testing.T) {
	const mark = "...[truncated]"
	cases := []struct {
		name string
		in   string
		max  int
	}{
		{"纯中文超长(切点落在字符中间)", strings.Repeat("拒", 400), 512},
		{"中英混排超长", strings.Repeat("拒绝理由reason", 100), 512},
		{"emoji 超长(4 字节字符)", strings.Repeat("🚫", 200), 512},
		{"上限小于标记长度", strings.Repeat("拒", 10), 5},
		{"上限刚好等于标记长度", strings.Repeat("拒", 10), len(mark)},
		{"上限比标记多一个字节", strings.Repeat("拒", 10), len(mark) + 1},
		{"操作人名超长", strings.Repeat("管理员", 50), 64},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := Truncate(tc.in, tc.max)
			assert.True(t, utf8.ValidString(got),
				"截断结果必须是合法 UTF-8,否则整条审计行会被 utf8mb4 列拒绝")
			assert.LessOrEqual(t, len(got), tc.max,
				"截断结果不得超过列宽,否则 MySQL 严格模式下报 1406")
		})
	}
}

// 不需要截断时必须原样返回:审计理由被无谓改写会让复盘时对不上原文。
func TestTruncate_PassesThroughWhenShortEnough(t *testing.T) {
	cases := []struct {
		name string
		in   string
		max  int
	}{
		{"空串", "", 512},
		{"上限为零表示不限制", strings.Repeat("拒", 400), 0},
		{"上限为负表示不限制", strings.Repeat("拒", 400), -1},
		{"长度正好等于上限", "abcdef", 6},
		{"中文未超上限", "余额不足,拒绝提现", 512},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			assert.Equal(t, tc.in, Truncate(tc.in, tc.max))
		})
	}
}

// 截断必须留下可识别的标记,否则事后无法区分"理由本来就这么短"与"被切了"。
func TestTruncate_MarksTruncation(t *testing.T) {
	got := Truncate(strings.Repeat("拒", 400), 512)
	require.NotEmpty(t, got)
	assert.True(t, strings.HasSuffix(got, "...[truncated]"))
	assert.True(t, strings.HasPrefix(got, "拒拒拒"))
}
