package violation

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

// TestCrossedThresholdFiresExactlyOnce 是自动封号并发正确性的核心不变量。
//
// 场景:用户并发打 100 个违规请求,多个节点的 worker 同时推进计数。
// bumpCounter 用"单条原子 upsert + 同事务读回"保证每个 worker 拿到的
// 推进后计数互不相同且连续,本用例在这个前提上验证:无论并发多高、
// 权重多大,"跨越阈值"这件事全局只会被观察到一次。
//
// 若判据写成 after >= threshold,阈值之后的每一次违规都会去认领封号;
// 虽然唯一索引兜得住,但会产生大量无效认领与告警,并让 ban_cycle 的语义失真。
func TestCrossedThresholdFiresExactlyOnce(t *testing.T) {
	const threshold = 5

	t.Run("权重为 1 时恰好在第 5 次跨越", func(t *testing.T) {
		crossings := 0
		for after := 1; after <= 50; after++ {
			if crossedThreshold(after, 1, threshold) {
				crossings++
				assert.Equal(t, threshold, after, "跨越必须发生在恰好到达阈值的那一次")
			}
		}
		assert.Equal(t, 1, crossings)
	})

	t.Run("权重大于 1 时一次跳过阈值也只算一次跨越", func(t *testing.T) {
		// 一条 count_weight=3 的高危规则:计数序列是 3, 6, 9…,
		// 6 那一次直接跨过了阈值 5,必须被识别为唯一的跨越点。
		crossings := 0
		for i := 1; i <= 20; i++ {
			if crossedThreshold(i*3, 3, threshold) {
				crossings++
				assert.Equal(t, 6, i*3)
			}
		}
		assert.Equal(t, 1, crossings)
	})

	t.Run("阈值为 0 表示关闭自动封号", func(t *testing.T) {
		for after := 1; after <= 100; after++ {
			assert.False(t, crossedThreshold(after, 1, 0))
		}
	})

	t.Run("权重为 0 的规则只记录不计数", func(t *testing.T) {
		assert.False(t, crossedThreshold(threshold, 0, threshold))
	})

	// 解封后计数被清零、周期 +1,下一轮必须能重新触发跨越 ——
	// 否则该用户的自动封号就永久失效了。
	t.Run("解封清零后能再次跨越", func(t *testing.T) {
		second := 0
		for after := 1; after <= 10; after++ {
			if crossedThreshold(after, 1, threshold) {
				second++
			}
		}
		assert.Equal(t, 1, second)
	})
}
