package apiaddr

import (
	"strconv"
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

// 颜色是白名单:写入侧折叠大小写与空白,不在调色板里的值直接拒收 ——
// 这个值会被前端原样拿去查样式表,也会进审计快照。空 = 用前端默认色,
// 也是存量行补列后的值。
func TestNormalizeColorAcceptsOnlyPaletteNames(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want string
		err  error
	}{
		{"空", "", "", nil},
		{"全空白", "  ", "", nil},
		{"调色板原样", "orange", "orange", nil},
		{"折叠大小写", " Blue ", "blue", nil},
		{"不在调色板", "magenta", "", errColorInvalid},
		{"自由串", "bg-red-500", "", errColorInvalid},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := normalizeColor(tc.in)
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

// 展示位置是白名单:折叠大小写与空白、去重,不认识的位置名直接拒收 ——
// 写错的位置名不会报错,只会让这条地址在过滤时去错地方,而那查不出来。
// 空 = 所有位置可见,与 UserGroups 的空名单同一套口径。
func TestNormalizeSurfacesAcceptsOnlyKnownSurfaces(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want string
		err  error
	}{
		{"空", "", "", nil},
		{"全空白", "  ", "", nil},
		{"单个位置", "picker", "picker", nil},
		{"折叠大小写与空白", " Console , PICKER ", "console,picker", nil},
		{"去重", "picker,picker", "picker", nil},
		{"空 token 跳过", "console,,", "console", nil},
		{"未知位置", "sidebar", "", errSurfaceInvalid},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := normalizeSurfaces(tc.in)
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

// 「适用分组」写入侧的归一化:折叠大小写与空白、跳过空 token、去重,
// 以及三道上限与两类非法字符。
//
// 折叠必须与判定侧(visibleToUserGroup)同口径 —— 存了 "VIP" 而判定拿 "vip"
// 来比,这条地址就对谁都不可见,管理端却看着配得明明白白。这张表就是两侧
// 口径的对账单。
func TestNormalizeUserGroupsFoldsDedupesAndBounds(t *testing.T) {
	tooMany := make([]string, maxUserGroupEntries+1)
	for i := range tooMany {
		tooMany[i] = "g" + strconv.Itoa(i)
	}
	// 20 条 × 60 rune ≈ 1219 rune:条数没超但总长超 —— 两道上限必须各自成立。
	tooLong := make([]string, 20)
	for i := range tooLong {
		tooLong[i] = strings.Repeat("a", 58) + "-" + strconv.Itoa(i)
	}

	cases := []struct {
		name string
		in   string
		want string
		err  error
	}{
		{"空串 = 所有分组", "   ", "", nil},
		{"单个原样", "vip", "vip", nil},
		{"折叠大小写与空白", " VIP , svip ", "vip,svip", nil},
		{"空 token 跳过", "vip,,svip,", "vip,svip", nil},
		{"折叠后去重", "vip,VIP", "vip", nil},
		{"中文分组放行", "内测用户", "内测用户", nil},

		{"内部空白拒收", "vip 2", "", errGroupInvalid},
		{"分号拒收", "vip;svip", "", errGroupInvalid},
		{"单名超长", strings.Repeat("组", maxUserGroupRunes+1), "", errGroupTooLong},
		{"条数超限", strings.Join(tooMany, ","), "", errGroupsTooMany},
		{"总长超限", strings.Join(tooLong, ","), "", errGroupsTooLong},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := normalizeUserGroups(tc.in)
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

// 判定侧的可见性口径:空名单对谁都可见(默认兜底线路),用户分组走 Effective
// —— 历史行的空分组在业务上就是 default 用户,漏掉这一折叠,挂在 default 上的
// 专属线路对这批账号恰好不生效。
func TestVisibleToUserGroupMatchesEffectiveGroup(t *testing.T) {
	cases := []struct {
		name       string
		userGroups string
		userGroup  string
		want       bool
	}{
		{"空名单对谁都可见", "", "vip", true},
		{"命中", "vip,svip", "vip", true},
		{"未命中", "vip,svip", "default", false},
		{"用户分组折叠后命中", "vip", " VIP ", true},
		{"历史空分组按 default 算", "default", "", true},
		{"历史空分组不在名单则不可见", "vip", "", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			assert.Equal(t, tc.want, visibleToUserGroup(tc.userGroups, tc.userGroup))
		})
	}
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
