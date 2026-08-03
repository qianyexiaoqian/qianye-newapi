package violation

import (
	"testing"

	"github.com/QuantumNous/new-api/common"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// resetBreaker 清空进程级熔断状态。
//
// 全是包级 atomic,不清会让用例之间互相污染,断言变成看运行顺序的玄学。
// (全局影子开关的那份快照已随 settings.go 一起删除,这里不再需要清它。)
func resetBreaker() {
	winStart.Store(common.GetTimestamp())
	winScanned.Store(0)
	winBlocked.Store(0)
	banWinStart.Store(common.GetTimestamp())
	banWinCount.Store(0)
	forcedShadowUntil.Store(0)
	forcedShadowReason.Store("")
}

// TestBlockRateBreakerClampsEveryRuleToShadow 验证"一条 .* 正则拦掉全站"的自愈能力。
//
// 熔断在本轮被降级成"运行期钳位"而不是一个模式,但**能力必须一字不少地留着** ——
// 它是删掉全局开关之后唯一还能自动拦住规则事故的东西。
func TestBlockRateBreakerClampsEveryRuleToShadow(t *testing.T) {
	// enforce 规则是观察熔断的探针:规则自己说真实执行,那么 effectiveShadow
	// 返回 true 就只可能来自熔断。用 shadow 规则测这里会恒真,是个假回归。
	enforcing := &compiledRule{R: Rule{Id: 1, Mode: ModeEnforce}}

	t.Run("样本量不足时不误判", func(t *testing.T) {
		resetBreaker()
		useTestConfig(t, "  enabled: true\n  global_block_rate_limit_bps: 500\n")
		// 上线后第一个请求就被拦,拦截率是 100%。若不设最小样本量,
		// 系统会立刻自我熔断,风控形同虚设。
		for i := 0; i < blockRateMinSamples-1; i++ {
			noteScan(true)
		}
		on, _ := effectiveShadow(enforcing)
		assert.False(t, on)
	})

	t.Run("拦截率越界后把全部规则钳成影子", func(t *testing.T) {
		resetBreaker()
		useTestConfig(t, "  enabled: true\n  global_block_rate_limit_bps: 500\n")
		for i := 0; i < blockRateMinSamples+10; i++ {
			noteScan(true)
		}
		on, reason := effectiveShadow(enforcing)
		require.True(t, on, "拦截率 100% 远超 5%,必须自动钳位")
		assert.Contains(t, reason, "block_rate",
			"原因必须是熔断的触发描述,而不是 rule_mode —— 两者在事后分析里含义相反")

		// 管理员确认规则已修正后可以手动解除,规则回到它自己声明的模式。
		clearForcedShadow()
		on, _ = effectiveShadow(enforcing)
		assert.False(t, on)
	})

	t.Run("正常拦截率不触发", func(t *testing.T) {
		resetBreaker()
		useTestConfig(t, "  enabled: true\n  global_block_rate_limit_bps: 500\n")
		// 1000 次里拦 10 次 = 1%,低于 5% 阈值。
		for i := 0; i < 1000; i++ {
			noteScan(i%100 == 0)
		}
		on, _ := effectiveShadow(enforcing)
		assert.False(t, on)
	})
}

// TestBanRateBreaker 验证封号速率闸。
//
// 即使拦截率不高,批量封号也必须先停下来给人看一眼:封错号造成的信任损失
// 远大于晚封几分钟。
func TestBanRateBreaker(t *testing.T) {
	resetBreaker()
	useTestConfig(t, "  enabled: true\n  global_ban_rate_limit_per_hour: 3\n")

	assert.False(t, banRateExceeded())
	for i := 0; i < 3; i++ {
		noteBan()
	}
	assert.True(t, banRateExceeded(), "达到每小时上限后必须暂停自动封号")

	// 触发速率闸的同时也应钳成影子,防止后续继续产生新的封号认领。
	on, reason := effectiveShadow(&compiledRule{R: Rule{Id: 1, Mode: ModeEnforce}})
	assert.True(t, on)
	assert.Contains(t, reason, "ban_rate")
}

// TestBreakerStatsDropsTheDeletedGlobalModeFields 是一条断链回归。
//
// 全局模式层删掉之后,breakerStats 里那五个描述它的字段必须一起消失。留着的话
// 前端会继续渲染一个改不动任何东西的开关 —— 而"配置/接口还在、消费方已经没了"
// 正是本仓库反复出现的形状。
func TestBreakerStatsDropsTheDeletedGlobalModeFields(t *testing.T) {
	resetBreaker()
	useTestConfig(t, "  enabled: true\n")
	st := breakerStats()

	for _, gone := range []string{
		"shadow", "shadow_reason", "config_shadow", "shadow_override",
		"global_shadow", "shadow_loaded_at", "shadow_load_fails",
	} {
		assert.NotContains(t, st, gone, "全局模式层已删除,%s 不该还在下发", gone)
	}
	assert.Equal(t, false, st["forced_shadow"])
	assert.Equal(t, "", st["forced_shadow_reason"])

	tripShadow("block_rate 测试")
	st = breakerStats()
	assert.Equal(t, true, st["forced_shadow"], "熔断触发后必须能从统计里看出来")
	assert.Contains(t, st["forced_shadow_reason"], "block_rate")
	clearForcedShadow()
}
