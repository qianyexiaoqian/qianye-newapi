package groupmatrix

import (
	"reflect"
	"testing"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/service"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

// bitexact_test.go —— 「未配置时与上游逐位一致」的证明。
//
// # 为什么这是本模块最重要的一组测试
//
// 本模块的回退能力完全建立在一条承诺上:没有 scope 行的用户分组,行为与改动前
// **一个字节都不差**。这条承诺一旦悄悄失效,表现是「某些用户莫名其妙少了几个
// 可选分组」或者「莫名其妙多了几个」—— 前者是断服,后者是资金泄漏
// (本站有三个 ratio=0 的模型分组),而两者都不会有任何报错。
//
// # 为什么不重新实现一遍上游逻辑当 golden
//
// 那份复制品会自己漂移,而漂移之后它仍然与自己一致,测试照常全绿。
// 这里的做法是:**在同一个进程、同一份 setting 状态下跑两次** ——
// 先把 hook 置回恒等函数拿到上游真值,再装上真实实现拿到结果,两者比对。

// upstreamBaseline 在 hook 恒等的前提下取一次上游真值。
func upstreamBaseline(t *testing.T, userGroup string) map[string]string {
	t.Helper()
	prev := service.QyResolveUsableGroups
	service.QyResolveUsableGroups = func(_ string, upstream map[string]string) map[string]string {
		return upstream
	}
	defer func() { service.QyResolveUsableGroups = prev }()
	return service.GetUserUsableGroups(userGroup)
}

// withRealHook 装上真实实现跑一次。
func withRealHook(t *testing.T, userGroup string) map[string]string {
	t.Helper()
	prev := service.QyResolveUsableGroups
	service.QyResolveUsableGroups = Resolve
	defer func() { service.QyResolveUsableGroups = prev }()
	return service.GetUserUsableGroups(userGroup)
}

// TestUnmanagedGroupsAreBitExactWithUpstream 是笛卡尔积对账。
//
// 覆盖必须包含两格「一眼看不出为什么重要」的:
//   - `-:<userGroup 自己>`:本站实测存在的那条**从未生效过**的规则。
//     上游最后一步无条件补自己,所以它一直是空转的;接管之后它第一次真的生效。
//     未接管时必须仍然空转 —— 否则这次改动就悄悄改变了一批用户的可选范围。
//   - userGroup 不在白名单里:这是触发上游那段自我补入的唯一路径。
func TestUnmanagedGroupsAreBitExactWithUpstream(t *testing.T) {
	newTestDB(t)
	useConfig(t, true)
	syncHotAsync(t)

	whitelists := map[string]map[string]string{
		"空":           {},
		"仅 default":   {"default": "默认分组"},
		"default+vip": {"default": "默认分组", "vip": "vip分组"},
	}
	specialSets := map[string]map[string]string{
		"无":         nil,
		"加一个":       {"+:extra": "额外分组"},
		"删 default": {"-:default": ""},
		"删自己":       {"-:vip": ""},
		"裸键":        {"bare": "裸键分组"},
	}
	userGroups := []string{"", "default", "vip", "不在白名单的分组"}

	groupRatio := map[string]float64{
		"default": 1, "vip": 0.5, "extra": 0.2, "bare": 1, "不在白名单的分组": 1,
	}

	for wlName, wl := range whitelists {
		for spName, sp := range specialSets {
			for _, ug := range userGroups {
				t.Run(wlName+"/"+spName+"/"+ug, func(t *testing.T) {
					specials := map[string]map[string]string{}
					if sp != nil && ug != "" {
						specials[ug] = sp
					}
					useUpstreamGroups(t, wl, specials, groupRatio)
					// 库里一条 scope 行都没有 → 全部 unmanaged。
					require.NoError(t, reload())

					want := upstreamBaseline(t, ug)
					got := withRealHook(t, ug)
					assert.Equal(t, want, got,
						"未接管的用户分组必须与上游逐位一致 —— 差一个 key 就是一批用户断服或一次资金泄漏")
				})
			}
		}
	}
}

// TestResolveReturnsUpstreamPointerWhenNotManaged 是**指针恒等**断言。
//
// 值相等在功能上够用,但指针相等才能证明"hook 确实没碰它"。
// 有人哪天在恒等分支里顺手加一次 maps.Clone,值断言照常通过,而
// controller/pricing.go 依赖的指针语义已经变了。
func TestResolveReturnsUpstreamPointerWhenNotManaged(t *testing.T) {
	gdb := newTestDB(t)
	syncHotAsync(t)
	useUpstreamGroups(t,
		map[string]string{"default": "默认分组"},
		map[string]map[string]string{},
		map[string]float64{"default": 1, "vip": 1})

	cases := []struct {
		name    string
		enabled bool
		setup   func(t *testing.T)
		group   string
	}{
		{"(a) 功能开关关闭", false, func(t *testing.T) {
			require.NoError(t, reload())
		}, "vip"},
		{"(b) 匿名口径", true, func(t *testing.T) {
			require.NoError(t, reload())
		}, ""},
		{"(c) 未接管", true, func(t *testing.T) {
			require.NoError(t, reload())
		}, "vip"},
		{"(d) 快照从未加载", true, func(t *testing.T) {
			current.Store(nil)
		}, "vip"},
		{"(e) 已接管但 shadow", true, func(t *testing.T) {
			seedScope(t, gdb, "vip", ModeShadow, false, "default")
		}, "vip"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			useConfig(t, tc.enabled)
			// 每个用例独立清库,避免 (e) 的 scope 行渗进后续用例。
			require.NoError(t, gdb.Where("1 = 1").Delete(&Scope{}).Error)
			require.NoError(t, gdb.Where("1 = 1").Delete(&Grant{}).Error)
			tc.setup(t)

			in := map[string]string{"default": "默认分组", "vip": "vip分组"}
			got := Resolve(tc.group, in)
			assert.Equal(t, reflect.ValueOf(in).Pointer(), reflect.ValueOf(got).Pointer(),
				"这一档必须原样返回入参那一张 map,不得复制、不得增删")
		})
	}
}

// TestEnforceReturnsFreshMapNotSnapshotInternals 守的是"调用方 delete 一个 key
// 不会污染全站鉴权"。
//
// 快照是进程内共享的只读结构;把它内部的 map 直接交出去,任何一个调用方的
// 就地修改都会影响此后**所有**用户的鉴权,而且无法复现。
func TestEnforceReturnsFreshMapNotSnapshotInternals(t *testing.T) {
	gdb := newTestDB(t)
	useConfig(t, true)
	syncHotAsync(t)
	useUpstreamGroups(t,
		map[string]string{"default": "默认分组", "vip": "vip分组"},
		map[string]map[string]string{},
		map[string]float64{"default": 1, "vip": 1})
	seedScope(t, gdb, "vip", ModeEnforce, false, "default")

	in := map[string]string{"default": "默认分组", "vip": "vip分组"}
	got := Resolve("vip", in)
	require.Equal(t, map[string]string{"default": "默认分组"}, got)

	delete(got, "default")
	again := Resolve("vip", in)
	assert.Equal(t, map[string]string{"default": "默认分组"}, again,
		"调用方改了返回值之后,下一次解析必须仍然正确 —— 返回快照内部的 map 会让这条失败")
}

// TestEnforceAuthoritativeListOverridesUpstreamEntirely 是本轮**核心行为变更**的断言。
//
// 权威清单必须是权威的,不是差分:全局白名单、+:/-: 规则、以及上游那步
// 无条件的自我补入,三者在接管之后全部失效。
//
// 「用户分组可以不包含它自己」是项目方点名要的能力,而它只有在 hook 挂在
// GetUserUsableGroups 的**最后一条语句**上时才做得到 —— 挂早一步,
// 上游会在其后把自己补回去,这条断言就是它的守卫。
func TestEnforceAuthoritativeListOverridesUpstreamEntirely(t *testing.T) {
	gdb := newTestDB(t)
	useConfig(t, true)
	syncHotAsync(t)
	useUpstreamGroups(t,
		map[string]string{"default": "默认分组", "vip": "vip分组", "svip": "svip分组"},
		map[string]map[string]string{"vip": {"+:extra": "额外分组"}},
		map[string]float64{"default": 1, "vip": 1, "svip": 1, "extra": 1, "only": 1})
	seedScope(t, gdb, "vip", ModeEnforce, false, "only")

	prev := service.QyResolveUsableGroups
	service.QyResolveUsableGroups = Resolve
	t.Cleanup(func() { service.QyResolveUsableGroups = prev })

	got := service.GetUserUsableGroups("vip")
	assert.Equal(t, map[string]string{"only": "only"}, got,
		"接管之后清单必须是权威的:全局白名单、+: 规则、以及上游无条件补自己全部失效")
	assert.NotContains(t, got, "vip",
		"「用户分组可以不包含它自己」是项目方点名要的能力 —— 出现 vip 说明 hook 挂早了,"+
			"上游在它之后又把自己补了回去")
}

// TestSwitchingToManagedWithPrefillIsBehaviourNeutral 是「开箱零行为变更」的断言(L4)。
//
// 首次接管时 grants 预填 = 切换**之前**的实际可选集合,因此切完之后立刻再读一次,
// 输出必须完全相同 —— 包括描述文案。运营看到的第一屏与现状一致,
// 所以不需要任何迁移工具。
//
// 顺带断言三个派生函数也不变:它们全都经过 GetUserUsableGroups,
// 但"经过"这件事是上游的实现细节,上游哪天把某一个改成直接读 setting,
// 这条会立刻红。
func TestSwitchingToManagedWithPrefillIsBehaviourNeutral(t *testing.T) {
	gdb := newTestDB(t)
	useConfig(t, true)
	syncHotAsync(t)
	useUpstreamGroups(t,
		map[string]string{"default": "默认分组", "vip": "vip分组"},
		map[string]map[string]string{"vip": {"+:extra": "额外分组"}},
		map[string]float64{"default": 1, "vip": 1, "extra": 1})

	prev := service.QyResolveUsableGroups
	service.QyResolveUsableGroups = Resolve
	t.Cleanup(func() { service.QyResolveUsableGroups = prev })

	require.NoError(t, reload())
	before := service.GetUserUsableGroups("vip")
	beforeSelectable := service.IsUserSelectableGroup("vip", "extra")

	// 预填 = 切换之前的实际结果。这一步必须在写 scope 行**之前**算好。
	prefill := make([]string, 0, len(before))
	for name := range before {
		prefill = append(prefill, name)
	}
	seedScope(t, gdb, "vip", ModeEnforce, false, prefill...)

	after := service.GetUserUsableGroups("vip")
	assert.Equal(t, before, after,
		"预填之后立刻再读,输出必须逐位相同(含描述文案)—— 否则接管本身就是一次静默的行为变更")
	assert.Equal(t, beforeSelectable, service.IsUserSelectableGroup("vip", "extra"),
		"派生函数也必须不变:它们经过 GetUserUsableGroups,自动跟随 hook")
}

// TestEmptyGrantListIsExpressible 守的是"零值与未配置必须可区分"。
//
// 空清单(一个模型分组都不许用)是合法且危险的配置(隔离组、封禁组)。
// 靠 grants 行数推断接管与否,就是本仓刚修过的那个缺陷的第二次发作,
// 而这一次的后果是整组用户 403 —— 或者反过来,本该被隔离的组照常畅通。
func TestEmptyGrantListIsExpressible(t *testing.T) {
	gdb := newTestDB(t)
	useConfig(t, true)
	syncHotAsync(t)
	useUpstreamGroups(t,
		map[string]string{"default": "默认分组", "vip": "vip分组"},
		map[string]map[string]string{},
		map[string]float64{"default": 1, "vip": 1})
	seedScope(t, gdb, "vip", ModeEnforce, false)

	in := map[string]string{"default": "默认分组", "vip": "vip分组"}
	got := Resolve("vip", in)
	assert.Empty(t, got, "空清单必须真的解析成空集合,而不是被当成「没配过」回落上游")
	assert.NotEqual(t, reflect.ValueOf(in).Pointer(), reflect.ValueOf(got).Pointer(),
		"空清单绝不能返回上游那张 map —— 那正是「零值与未配置不可区分」的失败形状")
}

// TestGrantsAreDroppedWhenModelGroupLeavesRatioTable 守的是编译期的第二道校验。
//
// 分组可能在保存之后被从倍率表里删掉,只在保存时把关拦不住这种漂移。
// 留着它的后果不是"多给一点权限",而是矩阵页上那一格看起来是通的、
// 用户却被上游用「分组已被弃用」挡掉 —— 又一个界面骗人的数字。
func TestGrantsAreDroppedWhenModelGroupLeavesRatioTable(t *testing.T) {
	gdb := newTestDB(t)
	useConfig(t, true)
	syncHotAsync(t)
	useUpstreamGroups(t,
		map[string]string{"default": "默认分组"},
		map[string]map[string]string{},
		map[string]float64{"default": 1, "vip": 1, "gone": 1})
	seedScope(t, gdb, "vip", ModeEnforce, false, "default", "gone")

	in := map[string]string{"default": "默认分组"}
	require.Equal(t, map[string]string{"default": "默认分组", "gone": "gone"}, Resolve("vip", in))

	// 运营在上游把 gone 从分组倍率表里删了。
	useUpstreamGroups(t,
		map[string]string{"default": "默认分组"},
		map[string]map[string]string{},
		map[string]float64{"default": 1, "vip": 1})
	require.NoError(t, reload())

	assert.Equal(t, map[string]string{"default": "默认分组"}, Resolve("vip", in),
		"已从倍率表消失的模型分组必须在编译期被剔除")
	snap, ok := SnapshotView()
	require.True(t, ok)
	assert.Equal(t, []string{"vip/gone"}, snap.DroppedGrants,
		"剔除必须留下痕迹,否则运营在矩阵页上永远看不出这一格是死的")
}

// TestSnapshotIsKeptWhenStale 守的是与 grouppricing **相反**的降级方向。
//
// 陈旧的钱丢弃更安全,陈旧的可见性保留更安全:丢弃只能回落到上游宽松白名单,
// 而本站有三个 ratio=0 的模型分组,回落意味着被收紧的用户重新可以选中它们。
// 那是真实的资金泄漏,不是"短暂宽松"。
func TestSnapshotIsKeptWhenStale(t *testing.T) {
	gdb := newTestDB(t)
	useConfig(t, true)
	syncHotAsync(t)
	useUpstreamGroups(t,
		map[string]string{"default": "默认分组", "free": "免费分组"},
		map[string]map[string]string{},
		map[string]float64{"default": 1, "vip": 1, "free": 0})
	seedScope(t, gdb, "vip", ModeEnforce, false, "default")

	// 把加载时间推到远古:快照已远超 max_stale_seconds。
	// 同时把下次刷新推到未来 —— 否则 maybeRefresh 会在断言之前把它刷新掉,
	// 这条测试就变成了永真(它测的是刷新成功之后的快照)。
	loadedAt.Store(1)
	nextRefreshAt.Store(common.GetTimestamp() + 3600)

	in := map[string]string{"default": "默认分组", "free": "免费分组"}
	got := Resolve("vip", in)
	assert.Equal(t, map[string]string{"default": "默认分组"}, got,
		"陈旧的快照必须继续生效 —— 回落上游会让被收紧的用户重新选到 ratio=0 的免费分组")
	assert.Positive(t, staleWarns.Load(), "陈旧必须告警:回落可以接受,回落无声不行")
}

// TestRetakeoverPrefillIsIdempotent 守**回退能力本身**。
//
// 撤销接管刻意只删 scope 行、保留 grants。于是「接管 → 撤销接管 → 再接管」
// 会让预填对已存在的行再 Create 一次,撞唯一键 uk_qy_ggrant_pair、整个事务回滚、
// 接口 500 —— 该用户分组从此再也接管不了,除非有人手工去库里删行。
//
// 回退一次就再也走不回来,那不叫回退。
func TestRetakeoverPrefillIsIdempotent(t *testing.T) {
	gdb := newTestDB(t)
	prefill := []string{"default", "vip", autoGroup, ""}

	require.NoError(t, gdb.Transaction(func(tx *gorm.DB) error {
		return ensureGrants(tx, "default", prefill, 1, 100)
	}))
	// 撤销接管:只删 scope 行,grants 原样留着(这就是缺陷的前提条件)。
	require.NoError(t, gdb.Where("user_group = ?", "default").Delete(&Scope{}).Error)

	require.NoError(t, gdb.Transaction(func(tx *gorm.DB) error {
		return ensureGrants(tx, "default", prefill, 1, 200)
	}), "重新接管必须成功 —— 撞唯一键会让这个用户分组永远接管不了")

	grants, err := loadGrants(gdb)
	require.NoError(t, err)
	assert.Len(t, grants["default"], 2, "重复预填不得产生重复行")
	assert.NotContains(t, grants["default"], autoGroup,
		"auto 是伪分组,可选性由 scope.allow_auto 控制,不进 grants")
}

// TestPreviewPrefillsUnmanagedTargets 守「首次接管 + 直接 enforce」那条路上的影响面。
//
// 写入侧首次接管会用上游的实际可选清单预填 grants(零行为变更是硬要求)。
// 预览侧若按"没有 grant 行 = 什么都不许"算,块 A 会把该分组**当前能用的每一个
// 模型分组**全部列成「本次新增的失效」—— 那道专门为切 enforce 而设的闸门,
// 在它唯一必需的场景下系统性地报假警。运营要么被吓退,要么对块 A 脱敏,
// 而块 A 是撤销操作唯一的护栏。
func TestPreviewPrefillsUnmanagedTargets(t *testing.T) {
	newTestDB(t)
	useConfig(t, true)
	syncHotAsync(t)
	useUpstreamGroups(t,
		map[string]string{"default": "默认分组", "vip": "会员分组"},
		map[string]map[string]string{},
		map[string]float64{"default": 1, "vip": 0.5, "paid": 1})

	res, err := runPreview(previewReq{UserGroups: []string{"paid"}})
	require.NoError(t, err)
	assert.Empty(t, res.NewlyBroken,
		"尚未接管的用户分组:首次接管会按当前可选清单预填,零行为变更,块 A 必须是空的")
	assert.Empty(t, res.NewlyAllowed,
		"预填同样不该被算成「本次新放开」")
}
