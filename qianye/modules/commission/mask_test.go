package commission

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestMaskUsername 锁定脱敏契约。
//
// 这是一条隐私边界:邀请人只被允许看到脱敏名。规则一旦被"优化"成
// 保留更多字符,泄漏就发生了,而且没人会立刻发现。
func TestMaskUsername(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want string
	}{
		{"空串", "", "***"},
		{"仅空白", "   ", "***"},
		// 单字符必须整个遮掉:保留首字符等于原样回显,一个字都没遮住。
		{"单个中文", "王", "**"},
		{"两个中文", "张三", "张**"},
		{"单个字母", "a", "**"},
		{"三字符", "abc", "a**c"},
		{"四字符", "abcd", "a**d"},
		{"长英文名", "zhangsan", "zh***an"},
		{"长中文名", "欧阳娜娜娜", "欧阳***娜娜"},
		{"邮箱", "zhangsan@example.com", "zh***an@***.com"},
		{"短本地部分邮箱", "ab@example.com", "a**@***.com"},
		{"单字符本地部分邮箱", "a@example.com", "**@***.com"},
		{"多级域名邮箱", "user123@mail.corp.co.uk", "us***23@***.uk"},
		{"无点域名邮箱", "user123@localhost", "us***23@***.localhost"},
		{"以 @ 开头不视为邮箱", "@handle", "@h***le"},
		{"以 @ 结尾不视为邮箱", "handle@", "ha***e@"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			assert.Equal(t, tc.want, MaskUsername(tc.in))
		})
	}
}

// TestMaskUsernameNeverLeaksMiddle 是一条通用不变量:
// 长度 >= 3 的名字,中间部分必须被遮掉。
func TestMaskUsernameNeverLeaksMiddle(t *testing.T) {
	for _, raw := range []string{"abcdefgh", "用户名测试账号", "a1b2c3d4e5"} {
		masked := MaskUsername(raw)
		assert.NotEqual(t, raw, masked)
		assert.Contains(t, masked, "*")
		assert.True(t, len([]rune(masked)) < len([]rune(raw))+4,
			"脱敏结果不应比原文长太多: %s -> %s", raw, masked)
	}
}

func TestMaskRef(t *testing.T) {
	assert.Equal(t, "", maskRef(""))
	assert.Equal(t, "****", maskRef("123"))
	assert.Equal(t, "****", maskRef("1234"))
	assert.Equal(t, "****4821", maskRef("TX20260730114821"))
}

// TestInviteeRefIsStableAndSalted 确认对外标识既稳定又不可枚举。
//
// 用裸 sha256(user_id) 的话,任何人都能在毫秒内枚举出 ref → user_id 的
// 完整映射,脱敏就白做了。
func TestInviteeRefIsStableAndSalted(t *testing.T) {
	const salt = "deployment-specific-salt"
	first := inviteeRef(42, salt)
	require.Len(t, first, 12)
	assert.Equal(t, first, inviteeRef(42, salt), "同一用户同一盐必须稳定")
	assert.NotEqual(t, first, inviteeRef(43, salt))
	assert.NotEqual(t, first, inviteeRef(42, "another-salt"), "换盐必须换值")
	assert.False(t, strings.Contains(first, "42"))
}
