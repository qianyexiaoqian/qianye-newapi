package controller

import (
	"testing"

	"github.com/QuantumNous/new-api/setting"

	"github.com/stretchr/testify/require"
)

// TestResolveTokenDefaultGroup 钉住「运营配了什么」与「这个人能选什么」的求交。
//
// 这一条是本功能唯一的安全属性:预选值必须落在该用户真实可选的清单内。
// 漏掉它的表现是用户打开新建令牌就看到一个提交即被拒的分组,而他没动过那一栏。
func TestResolveTokenDefaultGroup(t *testing.T) {
	usable := func(names ...string) map[string]map[string]interface{} {
		out := make(map[string]map[string]interface{}, len(names))
		for _, n := range names {
			out[n] = map[string]interface{}{"ratio": 1, "desc": n}
		}
		return out
	}

	tests := []struct {
		name      string
		config    string
		userGroup string
		usable    map[string]map[string]interface{}
		want      string
	}{
		{
			name:      "配了且该用户可选 → 返回它",
			config:    `{"vip":"vip-pool"}`,
			userGroup: "vip",
			usable:    usable("vip-pool", "default"),
			want:      "vip-pool",
		},
		{
			name:      "配了但该用户选不了 → 空串,由前端退回原有逻辑",
			config:    `{"vip":"vip-pool"}`,
			userGroup: "vip",
			usable:    usable("default"),
			want:      "",
		},
		{
			name:      "该用户分组没配 → 空串",
			config:    `{"vip":"vip-pool"}`,
			userGroup: "default",
			usable:    usable("default", "vip-pool"),
			want:      "",
		},
		{
			name:      "auto 是合法的预选值",
			config:    `{"vip":"auto"}`,
			userGroup: "vip",
			usable:    usable("default", "auto"),
			want:      "auto",
		},
		{
			name:      "空用户分组(匿名口径)→ 空串",
			config:    `{"vip":"vip-pool"}`,
			userGroup: "",
			usable:    usable("vip-pool"),
			want:      "",
		},
		{
			name:      "整份配置为空 → 空串,逐位等于本功能上线前",
			config:    `{}`,
			userGroup: "vip",
			usable:    usable("vip-pool"),
			want:      "",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			require.NoError(t, setting.UpdateTokenDefaultGroupsByJSONString(tc.config))
			t.Cleanup(func() {
				require.NoError(t, setting.UpdateTokenDefaultGroupsByJSONString(`{}`))
			})
			require.Equal(t, tc.want, resolveTokenDefaultGroup(tc.userGroup, tc.usable))
		})
	}
}

// TestValidateTokenDefaultGroups 钉住写入期的结构校验。
//
// 空键对应不到任何用户分组,空值等价于没配 —— 两者存进库只会让运营
// 以为配过了,而界面上什么都不会发生。
func TestValidateTokenDefaultGroups(t *testing.T) {
	require.NoError(t, setting.ValidateTokenDefaultGroups(`{}`))
	require.NoError(t, setting.ValidateTokenDefaultGroups(`{"vip":"vip-pool"}`))

	require.Error(t, setting.ValidateTokenDefaultGroups(`{"":"vip-pool"}`), "空用户分组名必须被拒")
	require.Error(t, setting.ValidateTokenDefaultGroups(`{"vip":""}`), "空默认模型分组必须被拒")
	require.Error(t, setting.ValidateTokenDefaultGroups(`not json`), "非法 JSON 必须被拒")
}

// TestUpdateTokenDefaultGroupsRejectsPartialWrite 钉住「解析失败不留半张表」。
//
// 直接往共享 map 上 Unmarshal 的话,一份中途失败的 JSON 会留下半新半旧的映射,
// 而调用方拿到 error 之后通常只把它记进日志 —— 于是线上跑着一份谁都没配过的组合。
func TestUpdateTokenDefaultGroupsRejectsPartialWrite(t *testing.T) {
	require.NoError(t, setting.UpdateTokenDefaultGroupsByJSONString(`{"vip":"vip-pool"}`))
	t.Cleanup(func() {
		require.NoError(t, setting.UpdateTokenDefaultGroupsByJSONString(`{}`))
	})

	require.Error(t, setting.UpdateTokenDefaultGroupsByJSONString(`{"a":`))
	require.Equal(t, "vip-pool", setting.GetTokenDefaultGroup("vip"), "解析失败后原映射必须原样保留")
}
