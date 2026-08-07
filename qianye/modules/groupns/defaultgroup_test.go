package groupns

import (
	"testing"

	"github.com/QuantumNous/new-api/qianye/config"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestResolveDefaultModelGroupIsIdentityUntilConfigured 是「上线当天零变化」的证明。
//
// 每一条 case 都必须逐位返回入参那个用户分组 + inherit —— 也就是上游
// middleware/auth.go 里 `userGroup = userCache.Group` 那一行的行为。
//
// 它覆盖的是全部**退化路径**:关模块、关子开关、匿名口径、快照没加载、名字没登记、
// mode 是错字、pin 却没填值。任何一条退化成"拒绝"或"改写",线上就是一整组用户
// 发不出请求 —— 而那正是这个功能要修的东西,方向写反等于把病治成了两倍。
func TestResolveDefaultModelGroupIsIdentityUntilConfigured(t *testing.T) {
	gdb := newTestDB(t)
	syncHotAsync(t)

	t.Run("模块关闭", func(t *testing.T) {
		nsConfig(t, false, config.MissingRatioPolicyLegacyOne, config.FundingGateOff)
		g, mode := ResolveDefaultModelGroup(1, "default")
		assert.Equal(t, "default", g)
		assert.Equal(t, DefaultModeInherit, mode)
	})

	t.Run("快照从未加载", func(t *testing.T) {
		nsConfig(t, true, config.MissingRatioPolicyLegacyOne, config.FundingGateOff)
		ResetForTest()
		// maybeRefresh 会同步跑一次(syncHotAsync),但库里此刻没有任何行,
		// 于是快照虽然加载成功却是空的 —— 空快照同样必须是 inherit。
		g, mode := ResolveDefaultModelGroup(1, "浅夜の梦专属号池")
		assert.Equal(t, "浅夜の梦专属号池", g)
		assert.Equal(t, DefaultModeInherit, mode)
	})

	t.Run("匿名口径", func(t *testing.T) {
		nsConfig(t, true, config.MissingRatioPolicyLegacyOne, config.FundingGateOff)
		g, mode := ResolveDefaultModelGroup(0, "")
		assert.Equal(t, "", g)
		assert.Equal(t, DefaultModeInherit, mode)
	})

	t.Run("名字没登记(fail-open)", func(t *testing.T) {
		nsConfig(t, true, config.MissingRatioPolicyLegacyOne, config.FundingGateOff)
		seedUserGroup(t, gdb, "已登记", DefaultModePin, "免费の渠道")
		require.NoError(t, InvalidateAndReload())
		g, mode := ResolveDefaultModelGroup(1, "没登记过的分组")
		assert.Equal(t, "没登记过的分组", g,
			"未登记必须表现为「和今天一样」—— 登记表是给人看的表,不是准入闸门")
		assert.Equal(t, DefaultModeInherit, mode)
	})

	t.Run("mode 是错字", func(t *testing.T) {
		nsConfig(t, true, config.MissingRatioPolicyLegacyOne, config.FundingGateOff)
		require.NoError(t, gdb.Model(&UserGroup{}).Where("name = ?", "已登记").
			Update("default_mode", "PIN").Error)
		require.NoError(t, InvalidateAndReload())
		g, mode := ResolveDefaultModelGroup(1, "已登记")
		assert.Equal(t, "已登记", g, "未知配置必须退化成今天的行为,不是把整组用户挡在门外")
		assert.Equal(t, DefaultModeInherit, mode)
	})

	t.Run("pin 却没填值", func(t *testing.T) {
		nsConfig(t, true, config.MissingRatioPolicyLegacyOne, config.FundingGateOff)
		require.NoError(t, gdb.Model(&UserGroup{}).Where("name = ?", "已登记").
			Updates(map[string]any{"default_mode": DefaultModePin, "default_model_group": ""}).Error)
		require.NoError(t, InvalidateAndReload())
		g, mode := ResolveDefaultModelGroup(1, "已登记")
		assert.Equal(t, "已登记", g, "pin 到空串等于把 UsingGroup 清空,会直接 503")
		assert.Equal(t, DefaultModeInherit, mode)
	})
}

// TestResolveDefaultModelGroupThreeStates 是三态本身的断言。
//
// 三态互斥且缺一不可 —— 而这正是本方案与「用空串当哨兵」那种两态方案的分歧点:
// 登记表对每个观测到的用户分组自动回填一行,行存在性不再携带信息,
// 于是空串会被迫同时表达「还没配」与「就是不给兜底」。
func TestResolveDefaultModelGroupThreeStates(t *testing.T) {
	gdb := newTestDB(t)
	syncHotAsync(t)
	nsConfig(t, true, config.MissingRatioPolicyLegacyOne, config.FundingGateOff)

	seedUserGroup(t, gdb, "普通组", DefaultModeInherit, "")
	seedUserGroup(t, gdb, "空令牌组", DefaultModePin, "免费の渠道")
	seedUserGroup(t, gdb, "隔离组", DefaultModeDeny, "")
	require.NoError(t, InvalidateAndReload())

	cases := []struct {
		userGroup string
		wantGroup string
		wantMode  string
		why       string
	}{
		{"普通组", "普通组", DefaultModeInherit, "inherit 必须逐位等于上游"},
		{"空令牌组", "免费の渠道", DefaultModePin, "pin 必须把 UsingGroup 换成配好的模型分组"},
		{"隔离组", "", DefaultModeDeny, "deny 必须让调用方 403,而不是悄悄回落身份"},
	}
	for _, tc := range cases {
		t.Run(tc.userGroup, func(t *testing.T) {
			g, mode := ResolveDefaultModelGroup(7, tc.userGroup)
			assert.Equal(t, tc.wantGroup, g, tc.why)
			assert.Equal(t, tc.wantMode, mode, tc.why)
		})
	}

	snap, loaded := SnapshotView()
	require.True(t, loaded)
	assert.Equal(t, 1, snap.PinnedGroups)
	assert.Equal(t, 1, snap.DenyGroups)
}

// TestGroupRatioMissingDeniedDefaultsToLegacy 钉死严格模式的默认档。
//
// 默认必须是 legacy_one(恒 false ⇒ 上游 fail-open 原样保留)。翻成 deny 之前
// 全站会有多少请求被 403,只有失配登记簿的计数能回答;默认打开等于一次全站 403。
func TestGroupRatioMissingDeniedDefaultsToLegacy(t *testing.T) {
	newTestDB(t)

	nsConfig(t, true, config.MissingRatioPolicyLegacyOne, config.FundingGateOff)
	assert.False(t, GroupRatioMissingDenied("任何分组"),
		"legacy_one 下必须恒 false —— 上游 fail-open 原样保留")

	nsConfig(t, true, config.MissingRatioPolicyDeny, config.FundingGateOff)
	assert.True(t, GroupRatioMissingDenied("任何分组"))
	assert.False(t, GroupRatioMissingDenied(""), "空分组名不是一个模型分组,不参与判定")

	nsConfig(t, false, config.MissingRatioPolicyDeny, config.FundingGateOff)
	assert.False(t, GroupRatioMissingDenied("任何分组"),
		"模块关闭时严格模式必须一起失效 —— kill switch 不能只关一半")
}
