package service

import (
	"reflect"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestQyResolveUsableGroupsDefaultIsIdentity 守的是默认实现的**指针恒等**。
//
// 扩展未安装时 GetUserUsableGroups 必须与上游逐位一致。等值拷贝在功能上也一致,
// 但它会掩盖「hook 到底跑没跑」,而 controller/pricing.go 会把这张 map 继续
// 往下传 —— 指针语义本来就是调用方依赖的。
//
// 这条抓的是「把 return upstream 改成 return maps.Clone(upstream)」这类
// 看起来无害的重构:它会让扩展侧那份 bit-exact 断言从此永远失效。
func TestQyResolveUsableGroupsDefaultIsIdentity(t *testing.T) {
	cases := []struct {
		name      string
		userGroup string
		in        map[string]string
	}{
		{"匿名 + nil", "", nil},
		{"匿名 + 空 map", "", map[string]string{}},
		{"具名分组", "vip", map[string]string{"default": "默认分组", "vip": "vip分组"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := QyResolveUsableGroups(tc.userGroup, tc.in)
			assert.Equal(t, reflect.ValueOf(tc.in).Pointer(), reflect.ValueOf(got).Pointer(),
				"默认实现必须原样返回入参那一张 map,不得复制")
		})
	}
}

// TestQyCheckTokenGroupChangeDefaultAllows 守默认实现放行一切。
//
// 上游单独发行时这个变量永远是 no-op,任何一次「顺手加个默认校验」都会让
// 不带扩展的部署突然开始拒绝合法的令牌分组。
func TestQyCheckTokenGroupChangeDefaultAllows(t *testing.T) {
	require.NoError(t, QyCheckTokenGroupChange(nil, "", "vip"))
	require.NoError(t, QyCheckTokenGroupChange(nil, "vip", ""))
	require.NoError(t, QyCheckTokenGroupChange(nil, "vip", "auto"))
}

// TestGetUserUsableGroupsRoutesThroughHook 断言 hook 真的接在返回值上。
//
// 它与扩展侧的 AST 守卫互补:那条守「源码里那一行还在」,这条守
// 「那一行的返回值真的是函数的返回值」—— 有人把它写成
// `_ = QyResolveUsableGroups(...)` 也能骗过 AST。
func TestGetUserUsableGroupsRoutesThroughHook(t *testing.T) {
	prev := QyResolveUsableGroups
	t.Cleanup(func() { QyResolveUsableGroups = prev })

	sentinel := map[string]string{"__qy_sentinel__": "1"}
	var sawUserGroup string
	QyResolveUsableGroups = func(userGroup string, upstream map[string]string) map[string]string {
		sawUserGroup = userGroup
		return sentinel
	}

	got := GetUserUsableGroups("vip")
	assert.Equal(t, "vip", sawUserGroup, "hook 必须拿到被查询的用户分组")
	assert.Equal(t, reflect.ValueOf(sentinel).Pointer(), reflect.ValueOf(got).Pointer(),
		"GetUserUsableGroups 必须返回 hook 的返回值,而不是丢弃它")
}
