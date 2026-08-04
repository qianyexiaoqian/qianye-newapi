package apiaddr

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// normalizeURL 是本模块唯一的对外契约面:它产出的字符串会被原样拼进剪贴板里的
// 连接信息 JSON,再被用户粘进桌面客户端。三类输入必须被挡死:
//
//   - 非 http/https 的 scheme —— 这个值在管理端列表里会被渲染成可点链接;
//   - URL 里的账号密码 —— 存进来就是把明文凭据放进一张会完整下发给所有登录
//     用户的表;
//   - 查询串 / 片段 —— `?key=sk-…` 这种误粘贴同样是凭据泄漏,而且客户端拼
//     `/v1/...` 时会拼出坏地址。
func TestNormalizeURLAcceptsOnlyPlainHTTPEndpoints(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want string
		err  error
	}{
		{"https 原样", "https://api.example.com", "https://api.example.com", nil},
		{"http 也放行", "http://10.0.0.2:3000", "http://10.0.0.2:3000", nil},
		{"去掉尾部斜杠", "https://api.example.com/", "https://api.example.com", nil},
		{"去掉多个尾部斜杠", "https://api.example.com/base//", "https://api.example.com/base", nil},
		{"保留子路径", "https://api.example.com/gw", "https://api.example.com/gw", nil},
		{"两端空白", "  https://api.example.com  ", "https://api.example.com", nil},

		{"空", "   ", "", errURLRequired},
		{"没有 scheme", "api.example.com", "", errURLScheme},
		{"协议相对", "//api.example.com", "", errURLScheme},
		{"javascript 伪协议", "javascript:alert(1)", "", errURLScheme},
		{"data 伪协议", "data:text/html,x", "", errURLScheme},
		{"ftp", "ftp://api.example.com", "", errURLScheme},
		{"带凭据", "https://user:pass@api.example.com", "", errURLCredentials},
		{"带查询串", "https://api.example.com?key=sk-1", "", errURLExtra},
		{"带空查询串", "https://api.example.com?", "", errURLExtra},
		{"带片段", "https://api.example.com#top", "", errURLExtra},
		{"没有 host", "https://", "", errURLInvalid},
		{"超长", "https://" + strings.Repeat("a", maxURLLen) + ".com", "", errURLTooLong},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := normalizeURL(tc.in)
			if tc.err != nil {
				require.ErrorIs(t, err, tc.err)
				assert.Empty(t, got)
				return
			}
			require.NoError(t, err)
			assert.Equal(t, tc.want, got)
		})
	}
}

// 名称与备注的边界:名称必填、两者都按 rune 计长。
//
// 按 rune 而不是 byte 是必须的:32 个中文名在 byte 口径下是 96,直接被判超长,
// 而列宽 varchar(64) 在 utf8mb4 下量的也是字符数,真正会溢出的是别的东西。
func TestNormalizeNameAndRemarkCountRunesNotBytes(t *testing.T) {
	name, err := normalizeName("  主线路  ")
	require.NoError(t, err)
	assert.Equal(t, "主线路", name)

	_, err = normalizeName("   ")
	require.ErrorIs(t, err, errNameRequired)

	_, err = normalizeName(strings.Repeat("线", maxNameRunes))
	require.NoError(t, err, "%d 个中文必须在上限之内 —— 按字节算就会在这里误报", maxNameRunes)

	_, err = normalizeName(strings.Repeat("线", maxNameRunes+1))
	require.ErrorIs(t, err, errNameTooLong)

	remark, err := normalizeRemark("")
	require.NoError(t, err, "备注是选填的")
	assert.Equal(t, "", remark)

	_, err = normalizeRemark(strings.Repeat("注", maxRemarkRune+1))
	require.ErrorIs(t, err, errRemarkTooLong)
}

// 重排入参的自身合法性:空、超上限、重复 id、非正 id 一律拒。
//
// 重复 id 必须挡在这里:漏掉的话 applyOrder 的"长度相等 + 全集包含"两条判定
// 会被一份 [1,1] 骗过(库里恰好有两行时),结果是一行被写两次序号、另一行原地不动。
func TestNormalizeOrderIdsRejectsMalformedInput(t *testing.T) {
	got, err := normalizeOrderIds([]int{3, 1, 2})
	require.NoError(t, err)
	assert.Equal(t, []int{3, 1, 2}, got)

	for _, bad := range [][]int{
		nil,
		{},
		{1, 1},
		{0, 2},
		{-1},
		make([]int, maxAddresses+1),
	} {
		_, err := normalizeOrderIds(bad)
		assert.ErrorIs(t, err, errInvalidParam, "输入 %v 应当被拒", bad)
	}
}
