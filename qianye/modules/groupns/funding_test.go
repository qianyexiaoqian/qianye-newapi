package groupns

import (
	"testing"

	"github.com/QuantumNous/new-api/qianye/config"

	"github.com/stretchr/testify/assert"
)

// funding_test.go —— 套餐耗尽闸门。
//
// 这一组测试的存在理由是一个**具体的反例**:如果闸门用「纯用户分组口径」判定
// (contains(U,M) 成立才放行),那么
//
//	用户 U 的 users.group = default,套餐 P 解锁模型分组 G(default 不含 G),
//	P 余额充足,U 的计费偏好是 wallet_first
//
// 会被直接 403 —— 而 P 还有满额余额,用户拿不到自己付过钱的分组;更糟的是那个 403
// 不是 ErrorCodeInsufficientUserQuota,连 wallet_first 的 trySubscription 回落
// 都触发不了,没有任何补救路径。TestFundingGateAllowsWalletFirstWhilePlanStillFunded
// 就是那个反例本身。

func TestFundingGateIsNoopUntilEnforced(t *testing.T) {
	newTestDB(t)
	useUpstreamGroups(t, map[string]string{"default": "默认"}, map[string]float64{"default": 1})
	// 套餐解锁了 G、且**已经耗尽** —— 这是唯一一档 enforce 会拒绝的输入。
	usePlanUnlock(t, func(int, string) (bool, bool) { return true, false })

	for _, tc := range []struct {
		name    string
		enabled bool
		mode    string
	}{
		{"模块关闭", false, config.FundingGateEnforce},
		{"闸门 off(默认)", true, config.FundingGateOff},
		{"闸门 shadow", true, config.FundingGateShadow},
	} {
		t.Run(tc.name, func(t *testing.T) {
			nsConfig(t, tc.enabled, config.MissingRatioPolicyLegacyOne, tc.mode)
			allowed, reason := ModelGroupFundingAllowed(7, "default", "反重力的哈基米", true)
			assert.True(t, allowed, "非 enforce 档必须逐位等于上游:放行")
			assert.Empty(t, reason)
		})
	}
}

// TestFundingGateAllowsWalletFirstWhilePlanStillFunded 是那个反例。
//
// 它是这个设计相对于「在 tryWallet 第一行写一个纯用户分组判据」的**全部价值**:
// 那种写法在这里会返回 false,把一个套餐余额充足的付费用户挡死。
func TestFundingGateAllowsWalletFirstWhilePlanStillFunded(t *testing.T) {
	newTestDB(t)
	nsConfig(t, true, config.MissingRatioPolicyLegacyOne, config.FundingGateEnforce)
	// 全局白名单里只有 default,所以 contains("default", "反重力的哈基米") = false。
	useUpstreamGroups(t, map[string]string{"default": "默认"}, map[string]float64{"default": 1})
	usePlanUnlock(t, func(_ int, mg string) (bool, bool) {
		return mg == "反重力的哈基米", true // 已解锁,且**仍有余额**
	})

	allowed, reason := ModelGroupFundingAllowed(7, "default", "反重力的哈基米", true)
	assert.True(t, allowed,
		"套餐仍有余额时钱包出资必须放行 —— wallet_first 会在套餐没用尽时就先走钱包,"+
			"在这里拒绝等于让付费用户拿不到自己解锁的分组,而且拿不到额度不足的错误码、"+
			"连订阅回落都触发不了")
	assert.Empty(t, reason)
}

// TestFundingGateRejectsOnlyWhenPlanExhaustedAndGroupNotOwned 是唯一一档拒绝。
func TestFundingGateRejectsOnlyWhenPlanExhaustedAndGroupNotOwned(t *testing.T) {
	newTestDB(t)
	nsConfig(t, true, config.MissingRatioPolicyLegacyOne, config.FundingGateEnforce)
	useUpstreamGroups(t, map[string]string{"default": "默认"}, map[string]float64{"default": 1})
	usePlanUnlock(t, func(_ int, mg string) (bool, bool) {
		return mg == "反重力的哈基米", false // 已解锁,但**额度用尽**
	})

	allowed, reason := ModelGroupFundingAllowed(7, "default", "反重力的哈基米", true)
	assert.False(t, allowed)
	assert.Contains(t, reason, "额度已用尽",
		"文案必须能自解释为什么昨天还能用 —— 「无权访问」会把排查方向从第一分钟就带偏")

	// 同一时刻的**权限层**必须仍然放行:分组照常出现在界面上、令牌照常钉得住,
	// 用户看到的是一句付费提示,而不是一个凭空消失的分组。
	allowed, _ = ModelGroupFundingAllowed(7, "default", "反重力的哈基米", false)
	assert.True(t, allowed,
		"requireFunding=false 是权限层,它只问「有没有解锁」,不问余额")
}

// TestFundingGateMembershipAgreesAcrossBothCallSites 是「一个函数、两个调用点、
// 只差一个布尔」这条结构性保证的直接断言。
//
// 遍历 requireFunding 的两个取值,断言**除了余额那一项之外**两者结论完全一致。
// 权限层与出资层对成员资格产生分歧,表现是「界面上能选、一发请求就被拒」或者反过来,
// 而两者各写一份判据时这种分歧迟早会发生。
func TestFundingGateMembershipAgreesAcrossBothCallSites(t *testing.T) {
	newTestDB(t)
	nsConfig(t, true, config.MissingRatioPolicyLegacyOne, config.FundingGateEnforce)
	useUpstreamGroups(t,
		map[string]string{"default": "默认", "共享池": "共享"},
		map[string]float64{"default": 1, "共享池": 1})

	cases := []struct {
		name       string
		modelGroup string
		unlocked   bool
		funded     bool
		// wantBoth 为 true 表示两个调用点都必须放行。
		wantBoth bool
		// wantFundingOnly 为 true 表示只有 requireFunding=true 那一侧拒绝。
		wantFundingOnly bool
	}{
		{"用户分组本来就含它", "共享池", false, false, true, false},
		{"用户分组含它 + 套餐也耗尽了", "共享池", true, false, true, false}, // contains 先命中,余额与它无关 —— 这一半一行代码都不用写。

		{"只靠套餐解锁 + 仍有余额", "反重力的哈基米", true, true, true, false},
		{"只靠套餐解锁 + 已耗尽", "反重力的哈基米", true, false, false, true},
		{"两项都不成立(交回上游判定)", "陌生分组", false, false, true, false},
		{"auto 伪分组永不参与", "auto", true, false, true, false},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			usePlanUnlock(t, func(_ int, mg string) (bool, bool) {
				if mg != tc.modelGroup {
					return false, false
				}
				return tc.unlocked, tc.funded
			})
			permit, _ := ModelGroupFundingAllowed(7, "default", tc.modelGroup, false)
			fund, _ := ModelGroupFundingAllowed(7, "default", tc.modelGroup, true)

			assert.True(t, permit,
				"权限层永远不该由本闸门拒绝 —— 它只回答「套餐耗尽后还能不能出资」")
			if tc.wantFundingOnly {
				assert.False(t, fund, "只有「仅靠套餐解锁 + 已耗尽」这一档拒绝")
				return
			}
			assert.True(t, fund, "其余各档两个调用点结论必须一致")
			assert.Equal(t, tc.wantBoth, permit && fund)
		})
	}
}
