package groupmatrix

import (
	"reflect"
	"testing"

	"github.com/QuantumNous/new-api/service"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// rollback_proof_test.go —— L3 回退能力与「未配置逐位一致」的**可执行证明**。
//
// 两者都必须是测试而不是一段说明:说明会过期,测试不会。
//
//	回退      = 把三个 hook 变量恢复成上游那份 no-op 默认实现之后,行为回到改动前。
//	未配置    = hook 仍然接着本模块的实现,但没有任何 scope 行时行为与上游逐位一致。
//
// 第二条比第一条更重要:它才是这套方案敢于全站上线的前提。

// resetHooksToUpstreamDefaults 把三个 hook 恢复成 service 包里那份默认实现。
//
// 刻意用与 service/qy_usablegroup_export.go 里逐字相同的函数体,而不是
// 「把 InstallHooks 之前的旧值存起来再还原」—— 后者只能证明"还原成了刚才那个",
// 证明不了"刚才那个就是上游默认"。
func resetHooksToUpstreamDefaults(t *testing.T) {
	t.Helper()
	prevResolve := service.QyResolveUsableGroups
	prevCheck := service.QyCheckTokenGroupChange
	prevPlayground := service.QyPlaygroundGroupAllowed
	t.Cleanup(func() {
		service.QyResolveUsableGroups = prevResolve
		service.QyCheckTokenGroupChange = prevCheck
		service.QyPlaygroundGroupAllowed = prevPlayground
	})

	service.QyResolveUsableGroups = func(_ string, upstream map[string]string) map[string]string {
		return upstream
	}
	service.QyCheckTokenGroupChange = func(*gin.Context, string, string) error { return nil }
	service.QyPlaygroundGroupAllowed = func(_ *gin.Context, usingGroup, requestedGroup string) bool {
		return service.GroupInUserUsableGroups(usingGroup, requestedGroup) ||
			requestedGroup == usingGroup
	}
}

// TestRollbackToUpstreamDefaultsRestoresUpstreamBehaviour 证明:
// 即使扩展**已经配了一份最激进的权威清单**(enforce + 空清单),
// 把三个 hook 置回恒等实现之后,三条链路全部回到上游行为。
func TestRollbackToUpstreamDefaultsRestoresUpstreamBehaviour(t *testing.T) {
	gdb := newTestDB(t)
	useConfig(t, true)
	syncHotAsync(t)
	useUpstreamGroups(t,
		map[string]string{"default": "默认分组", "vip": "会员分组"},
		map[string]float64{"default": 1, "vip": 0.5, "paid": 1})

	// 最激进的配置:paid 被接管、enforce、清单为空(一个模型分组都不许用)。
	seedScope(t, gdb, "paid", ModeEnforce, false)
	useOwnerGroup(t, "paid")

	// ── 前置条件:接管确实在生效(否则下面的"回退"什么都没证明)─────────────
	Mod{}.InstallHooks()
	require.Empty(t, service.GetUserUsableGroups("paid"),
		"前置条件:enforce + 空清单必须真的把可选集合清空,否则这条回退证明是空转")
	require.Error(t, service.QyCheckTokenGroupChange(ctxWithUser(1), "", "vip"),
		"前置条件:写入侧此刻必须真的会拒绝")

	// ── 回退 ────────────────────────────────────────────────────────────
	resetHooksToUpstreamDefaults(t)

	// 上游语义(瘦身之后):全局白名单 + 「自己在倍率表里就补自己」。
	// `paid` 不在白名单、但在 GroupRatio 里,所以自我补入这一支必须命中 ——
	// 它正是那 5 个 legacy_dual 名字赖以拿到可选性的那条路径。
	assert.Equal(t, map[string]string{
		"default": "默认分组",
		"vip":     "会员分组",
		"paid":    service.SelfUsableGroupDescription,
	}, service.GetUserUsableGroups("paid"),
		"回退之后必须逐位回到上游行为:全局白名单 + 自我补入")

	assert.NoError(t, service.QyCheckTokenGroupChange(ctxWithUser(1), "", "vip"),
		"回退之后写入侧必须完全放行 —— 上游本来就不校验 token.Group")

	assert.True(t, service.QyPlaygroundGroupAllowed(ctxWithUser(1), "paid", "vip"),
		"回退之后 playground 判定必须回到上游那条 GroupInUserUsableGroups 语义")
}

// TestUnconfiguredIsBitExactWithUpstream 是「未配置 = 上游」的证明。
//
// 与上一条的区别:这里 hook **仍然接着本模块的实现**,只是库里没有任何 scope 行。
// 断言取到最强的两级:值逐位相等 + **同一个 map 指针**。
//
// 指针那一级抓的是「有人在恒等分支里顺手加一次 maps.Clone」——
// 值断言照常通过,而 controller/pricing.go 依赖的共享语义已经悄悄变了。
func TestUnconfiguredIsBitExactWithUpstream(t *testing.T) {
	newTestDB(t)
	useConfig(t, true)
	syncHotAsync(t)
	useUpstreamGroups(t,
		map[string]string{"default": "默认分组", "vip": "会员分组"},
		map[string]float64{"default": 1, "vip": 0.5, "paid": 1})
	require.NoError(t, reload()) // 快照加载成功,但一条 scope 行都没有

	for _, ug := range []string{"", "default", "vip", "paid", "从没见过的分组"} {
		t.Run("用户分组="+ug, func(t *testing.T) {
			want := upstreamBaseline(t, ug)
			got := withRealHook(t, ug)
			assert.Equal(t, want, got,
				"未配置权威清单时,GetUserUsableGroups 的结果必须与上游原实现逐位一致")

			probe := map[string]string{"探针": "只出现在这一张 map 里"}
			assert.Equal(t,
				reflect.ValueOf(probe).Pointer(),
				reflect.ValueOf(Resolve(ug, probe)).Pointer(),
				"未配置时 Resolve 必须原样返回入参那一张 map 的指针,不复制、不增删、不重排")
		})
	}
}
