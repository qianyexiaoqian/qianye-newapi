package grouppricing

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// hook_test.go —— 三个计价挂载点的行为。
//
// 这里的每一条都对应一个"改坏了会真的多扣/少扣钱"的场景,
// 而不是"覆盖一下这行代码"。

// TestNoRuleKeepsUpstreamValuesBitIdentical 是最重要的一条:向后兼容。
//
// 没有配置分组价时,三个 hook 必须**原样**返回入参 —— 不是"约等于",
// 是逐位相同。恒等函数不做任何浮点运算,连一次舍入都不会引入。
// 这条一旦破了,全站每一笔账单都会在升级瞬间偏移,而且偏移量小到没人会发现。
func TestNoRuleKeepsUpstreamValuesBitIdentical(t *testing.T) {
	useConfig(t, true, true)
	gdb := newTestDB(t)
	// 有规则,但既不是这个分组也不是这个模型 —— 覆盖"表非空但不命中"这一路。
	seedRule(t, gdb, "vip", "gpt-4o", ModeRatio, "0.5")

	info := relayInfo("default", "default", "claude-3-5-sonnet")
	for _, v := range []float64{0, 1, 2.5, 0.000001, 37.5, 1e18} {
		price, usePrice := applyModelPrice(info, v, true)
		assert.Equal(t, v, price)
		assert.True(t, usePrice)

		price, usePrice = applyModelPrice(info, v, false)
		assert.Equal(t, v, price)
		assert.False(t, usePrice)

		ratio, ok := applyModelRatio(info, v, true)
		assert.Equal(t, v, ratio)
		assert.True(t, ok)

		ratio, ok = applyModelRatio(info, v, false)
		assert.Equal(t, v, ratio)
		assert.False(t, ok)

		assert.Equal(t, v, applyTieredQuota(info, v))
	}
}

// TestDisabledModuleIsIdentity:总开关关掉时,即使规则表里有命中的规则,
// 也必须完全不生效。开关必须真的接着代码,而不是一个空转的配置项。
func TestDisabledModuleIsIdentity(t *testing.T) {
	useConfig(t, true, false) // shadow=false,也就是"真实模式"
	gdb := newTestDB(t)
	seedRule(t, gdb, "vip", "gpt-4o", ModeRatio, "0.5")

	cfg := *qyConfig.Load()
	cfg.GroupPricing.Enabled = false
	qyConfig.Store(&cfg)

	info := relayInfo("vip", "vip", "gpt-4o")
	ratio, ok := applyModelRatio(info, 2, true)
	assert.Equal(t, float64(2), ratio)
	assert.True(t, ok)
}

// TestShadowModeKeepsChargeUnchangedButRecordsDiff 锁定影子模式的两半。
//
// 缺任何一半这个功能都失去意义:只不改扣费而不记录 = 什么都没做;
// 只记录而改了扣费 = 影子模式形同虚设,而这正是"钱已经扣走了"的那个场景。
func TestShadowModeKeepsChargeUnchangedButRecordsDiff(t *testing.T) {
	useConfig(t, true, true)
	syncHotAsync(t)
	gdb := newTestDB(t)
	seedRule(t, gdb, "vip", "gpt-4o", ModeRatio, "0.5")

	info := relayInfo("vip", "vip", "gpt-4o")
	ratio, ok := applyModelRatio(info, 2, true)
	assert.Equal(t, float64(2), ratio, "影子模式下实际扣费必须一分不变")
	assert.True(t, ok)

	buckets := drainToBuckets(t)
	require.Len(t, buckets, 1)
	b := buckets[0]
	assert.Equal(t, "vip", b.GroupName)
	assert.Equal(t, "gpt-4o", b.ModelName)
	assert.Equal(t, ModeRatio, b.Mode)
	assert.Equal(t, "2", b.OldValue)
	assert.Equal(t, "0.5", b.NewValue)
	assert.True(t, b.Exact)
	assert.Equal(t, int64(1), b.Requests)
	assert.Equal(t, "req-test-1", b.SampleRequestId, "必须留下请求标识供抽查")
}

// TestRealModeAppliesOverride 锁定关掉影子模式之后覆盖真实生效。
func TestRealModeAppliesOverride(t *testing.T) {
	useConfig(t, true, false)
	syncHotAsync(t)
	gdb := newTestDB(t)
	seedRule(t, gdb, "vip", "gpt-4o", ModeRatio, "0.5")

	info := relayInfo("vip", "vip", "gpt-4o")
	ratio, ok := applyModelRatio(info, 2, true)
	assert.Equal(t, 0.5, ratio)
	assert.True(t, ok)

	// 真实模式下不再写影子差额:主库日志里的金额已经是按新价扣的,
	// 再按系数折算一次就是二次应用,得到的是一个错的差额。
	assert.Empty(t, drainToBuckets(t), "真实模式下不应继续写影子差额")
}

// TestAllThreeModesApply 三种口径各自只对自己的挂载点生效。
//
// 口径串了会很难发现:给按 token 的模型配了 price 规则,ratio 挂载点静默不生效,
// 表面上就是"配了没用"。
func TestAllThreeModesApply(t *testing.T) {
	useConfig(t, true, false)
	syncHotAsync(t)
	gdb := newTestDB(t)

	t.Run("price 只作用于按次价挂载点", func(t *testing.T) {
		resetCaches()
		require.NoError(t, gdb.Where("1 = 1").Delete(&Rule{}).Error)
		seedRule(t, gdb, "vip", "mj", ModePrice, "0.02")
		info := relayInfo("vip", "vip", "mj")

		price, usePrice := applyModelPrice(info, 0.1, true)
		assert.Equal(t, 0.02, price)
		assert.True(t, usePrice)

		ratio, ok := applyModelRatio(info, 3, true)
		assert.Equal(t, float64(3), ratio, "price 规则不得改动模型倍率")
		assert.True(t, ok)

		assert.Equal(t, float64(9), applyTieredQuota(info, 9))
	})

	t.Run("price 可以把按 token 的模型切成按次", func(t *testing.T) {
		resetCaches()
		require.NoError(t, gdb.Where("1 = 1").Delete(&Rule{}).Error)
		seedRule(t, gdb, "vip", "gpt-4o", ModePrice, "0.02")
		info := relayInfo("vip", "vip", "gpt-4o")

		// 上游在没有按次价时返回 (-1, false)。
		price, usePrice := applyModelPrice(info, -1, false)
		assert.Equal(t, 0.02, price)
		assert.True(t, usePrice, "覆盖必须能把计费口径切成按次")
	})

	t.Run("ratio 可以给未配置倍率的模型定价", func(t *testing.T) {
		resetCaches()
		require.NoError(t, gdb.Where("1 = 1").Delete(&Rule{}).Error)
		seedRule(t, gdb, "vip", "new-model", ModeRatio, "1.25")
		info := relayInfo("vip", "vip", "new-model")

		ratio, ok := applyModelRatio(info, 37.5, false)
		assert.Equal(t, 1.25, ratio)
		assert.True(t, ok, "分组级倍率必须能让上游不再报「价格未配置」")
	})

	t.Run("tiered 是乘数", func(t *testing.T) {
		resetCaches()
		require.NoError(t, gdb.Where("1 = 1").Delete(&Rule{}).Error)
		seedRule(t, gdb, "vip", "tiered-model", ModeTiered, "0.5")
		info := relayInfo("vip", "vip", "tiered-model")

		assert.Equal(t, float64(50), applyTieredQuota(info, 100))
		price, usePrice := applyModelPrice(info, 0.1, true)
		assert.Equal(t, 0.1, price, "tiered 规则不得改动按次价")
		assert.True(t, usePrice)
	})
}

// TestResolveUsesUsingGroupNotUserGroup 锁定分组取值口径。
//
// UsingGroup 是本次请求实际使用的分组(auto 分组重试时 HandleGroupRatio 会改写它),
// UserGroup 是用户所属分组。取错不会报任何错,只会安静地按错误的分组扣钱 ——
// 而且与分组价相乘的分组倍率取的是 UsingGroup,两者来自不同分组时,
// 相乘出来的价格不对应任何真实定价。
func TestResolveUsesUsingGroupNotUserGroup(t *testing.T) {
	useConfig(t, true, false)
	syncHotAsync(t)
	gdb := newTestDB(t)
	seedRule(t, gdb, "auto-vip", "gpt-4o", ModeRatio, "0.5")

	// 用户属于 default,本次请求被 auto 分组路由到了 auto-vip。
	ratio, _ := applyModelRatio(relayInfo("default", "auto-vip", "gpt-4o"), 2, true)
	assert.Equal(t, 0.5, ratio, "必须按 UsingGroup(本次实际使用的分组)取价")

	// 反过来:规则只挂在用户所属分组上时,不得对本次请求生效。
	resetCaches()
	require.NoError(t, gdb.Where("1 = 1").Delete(&Rule{}).Error)
	seedRule(t, gdb, "default", "gpt-4o", ModeRatio, "0.5")
	ratio, _ = applyModelRatio(relayInfo("default", "auto-vip", "gpt-4o"), 2, true)
	assert.Equal(t, float64(2), ratio, "UserGroup 上的规则不得作用于另一个 UsingGroup")
}

// TestZeroAndBoundaryOverrides 覆盖 0 与极大值两端。
//
// 0 是合法的("这个分组免费用这个模型"),必须真的传下去 —— 上游据此走
// freeModel 分支。极大值必须在编译期就被挡掉,回落成无覆盖,
// 绝不能带着一个会把额度换算撑爆的数字进入计费。
func TestZeroAndBoundaryOverrides(t *testing.T) {
	useConfig(t, true, false)
	syncHotAsync(t)
	gdb := newTestDB(t)

	t.Run("零价必须生效", func(t *testing.T) {
		resetCaches()
		require.NoError(t, gdb.Where("1 = 1").Delete(&Rule{}).Error)
		seedRule(t, gdb, "free", "gpt-4o", ModeRatio, "0")
		ratio, ok := applyModelRatio(relayInfo("free", "free", "gpt-4o"), 2, true)
		assert.Equal(t, float64(0), ratio)
		assert.True(t, ok)
	})

	t.Run("越界值回落成无覆盖", func(t *testing.T) {
		resetCaches()
		require.NoError(t, gdb.Where("1 = 1").Delete(&Rule{}).Error)
		// 绕过接口直接写一行越界规则,模拟手改数据库。
		require.NoError(t, gdb.Create(&Rule{
			GroupName: "vip", ModelName: "gpt-4o", Mode: ModeRatio,
			Value: dec("99999999"), Enabled: true,
		}).Error)
		require.NoError(t, reload(true))

		ratio, ok := applyModelRatio(relayInfo("vip", "vip", "gpt-4o"), 2, true)
		assert.Equal(t, float64(2), ratio, "越界规则必须按无覆盖处理,而不是勉强用一下")
		assert.True(t, ok)
	})

	t.Run("负价回落成无覆盖", func(t *testing.T) {
		resetCaches()
		require.NoError(t, gdb.Where("1 = 1").Delete(&Rule{}).Error)
		require.NoError(t, gdb.Create(&Rule{
			GroupName: "vip", ModelName: "gpt-4o", Mode: ModePrice,
			Value: dec("-1"), Enabled: true,
		}).Error)
		require.NoError(t, reload(true))

		price, usePrice := applyModelPrice(relayInfo("vip", "vip", "gpt-4o"), 0.1, true)
		assert.Equal(t, 0.1, price, "负价会把扣费变成给用户充值,必须被拒")
		assert.True(t, usePrice)
	})
}

// TestNilInfoIsSafe:RelayInfo 有多条构造路径,hook 绝不能因为一个 nil 让 relay 崩。
func TestNilInfoIsSafe(t *testing.T) {
	useConfig(t, true, false)
	price, usePrice := applyModelPrice(nil, 1.5, true)
	assert.Equal(t, 1.5, price)
	assert.True(t, usePrice)
	ratio, ok := applyModelRatio(nil, 2.5, false)
	assert.Equal(t, 2.5, ratio)
	assert.False(t, ok)
	assert.Equal(t, 3.5, applyTieredQuota(nil, 3.5))
}
