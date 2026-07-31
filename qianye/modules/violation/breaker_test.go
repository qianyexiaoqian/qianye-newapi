package violation

import (
	"testing"

	"github.com/QuantumNous/new-api/common"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// resetBreaker 清空进程级熔断状态。熔断计数是包级 atomic,
// 不清会让用例之间互相污染,断言变成看运行顺序的玄学。
func resetBreaker() {
	winStart.Store(common.GetTimestamp())
	winScanned.Store(0)
	winBlocked.Store(0)
	banWinStart.Store(common.GetTimestamp())
	banWinCount.Store(0)
	forcedShadowUntil.Store(0)
	forcedShadowReason.Store("")
}

// TestShadowModeIsDefaultOn 固化"影子模式是默认状态"这条安全阀。
//
// 配置里 shadow_mode 是 *bool,不写就是 nil。若把它当普通 bool,零值 false
// 会让全新部署直接进入真实扣费与封号模式 —— 这正是必须避免的事故。
func TestShadowModeIsDefaultOn(t *testing.T) {
	resetBreaker()
	useTestConfig(t, "  enabled: true\n")
	on, reason := shadowActive()
	assert.True(t, on, "未显式关闭时必须处于影子模式")
	assert.Equal(t, "config", reason)
}

// TestBlockRateBreakerFallsBackToShadow 验证"一条 .* 正则拦掉全站"的自愈能力。
func TestBlockRateBreakerFallsBackToShadow(t *testing.T) {
	t.Run("样本量不足时不误判", func(t *testing.T) {
		resetBreaker()
		useTestConfig(t, "  enabled: true\n  shadow_mode: false\n  global_block_rate_limit_bps: 500\n")
		// 上线后第一个请求就被拦,拦截率是 100%。若不设最小样本量,
		// 系统会立刻自我熔断,风控形同虚设。
		for i := 0; i < blockRateMinSamples-1; i++ {
			noteScan(true)
		}
		on, _ := shadowActive()
		assert.False(t, on)
	})

	t.Run("拦截率越界后自动回落影子模式", func(t *testing.T) {
		resetBreaker()
		useTestConfig(t, "  enabled: true\n  shadow_mode: false\n  global_block_rate_limit_bps: 500\n")
		for i := 0; i < blockRateMinSamples+10; i++ {
			noteScan(true)
		}
		on, reason := shadowActive()
		require.True(t, on, "拦截率 100% 远超 5%,必须自动回落")
		assert.Contains(t, reason, "block_rate")

		// 管理员确认规则已修正后可以手动解除。
		clearForcedShadow()
		on, _ = shadowActive()
		assert.False(t, on)
	})

	t.Run("正常拦截率不触发", func(t *testing.T) {
		resetBreaker()
		useTestConfig(t, "  enabled: true\n  shadow_mode: false\n  global_block_rate_limit_bps: 500\n")
		// 1000 次里拦 10 次 = 1%,低于 5% 阈值。
		for i := 0; i < 1000; i++ {
			noteScan(i%100 == 0)
		}
		on, _ := shadowActive()
		assert.False(t, on)
	})
}

// TestBanRateBreaker 验证封号速率闸。
//
// 即使拦截率不高,批量封号也必须先停下来给人看一眼:封错号造成的信任损失
// 远大于晚封几分钟。
func TestBanRateBreaker(t *testing.T) {
	resetBreaker()
	useTestConfig(t, "  enabled: true\n  shadow_mode: false\n  global_ban_rate_limit_per_hour: 3\n")

	assert.False(t, banRateExceeded())
	for i := 0; i < 3; i++ {
		noteBan()
	}
	assert.True(t, banRateExceeded(), "达到每小时上限后必须暂停自动封号")

	// 触发速率闸的同时也应回落影子模式,防止后续继续产生新的封号认领。
	on, reason := shadowActive()
	assert.True(t, on)
	assert.Contains(t, reason, "ban_rate")
}
