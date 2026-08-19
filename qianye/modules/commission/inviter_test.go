package commission

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// resetUnknownInviterWarn 把限频状态清回进程刚起来的样子。
//
// 这是包级可变状态,不清的话测试之间会互相看到对方留下的窗口。
func resetUnknownInviterWarn(t *testing.T) {
	t.Helper()
	unknownInviterMu.Lock()
	prevLast, prevSkipped := unknownInviterLastLog, unknownInviterSkipped
	unknownInviterLastLog, unknownInviterSkipped = 0, 0
	unknownInviterMu.Unlock()
	t.Cleanup(func() {
		unknownInviterMu.Lock()
		unknownInviterLastLog, unknownInviterSkipped = prevLast, prevSkipped
		unknownInviterMu.Unlock()
	})
}

// TestUnknownInviterWarnIsThrottled 守"返佣模块唯一一个按 relay QPS 增长的
// 写入点"必须有上限。
//
// resolveInviter 只缓存 gorm.ErrRecordNotFound,其余错误(主库超时、连接打满、
// 慢查询堆积)一律不进缓存并直接返回,所以主库一抖动,同一个用户的每一次 relay
// 请求都会重查一次主库并走到 warnUnknownInviter。旧代码那里是一行裸的
// common.SysError:恰好在主库已经不健康的那一刻,再按 QPS 给它加一份日志量。
//
// 断言落在限频判定本体上,不去断言日志内容 —— 要守的性质是"窗口内放行几次"
// 与"压掉的条数一条不丢",而不是那句话怎么写的。
func TestUnknownInviterWarnIsThrottled(t *testing.T) {
	t.Run("窗口内只放行一次,其余全部压掉", func(t *testing.T) {
		resetUnknownInviterWarn(t)
		const t0 = int64(1_700_000_000)

		ok, skipped := takeUnknownInviterWarnSlot(t0)
		require.True(t, ok, "第一条必须放行:完全静默会让主库故障没有任何信号")
		assert.Zero(t, skipped)

		// 一秒钟 5000 次请求,只有第一条已经出去了。
		for i := 0; i < 5000; i++ {
			ok, _ = takeUnknownInviterWarnSlot(t0)
			require.False(t, ok, "第 %d 条没有被限频压住", i+2)
		}
		// 窗口最后一秒仍在窗口内。
		ok, _ = takeUnknownInviterWarnSlot(t0 + unknownInviterWarnEvery - 1)
		assert.False(t, ok, "窗口边界前一秒被放行 = 窗口短了一秒")
	})

	t.Run("窗口过去之后放行,并交代压掉了多少条", func(t *testing.T) {
		resetUnknownInviterWarn(t)
		const t0 = int64(1_700_000_000)

		ok, _ := takeUnknownInviterWarnSlot(t0)
		require.True(t, ok)
		for i := 0; i < 3; i++ {
			takeUnknownInviterWarnSlot(t0 + 1)
		}

		ok, skipped := takeUnknownInviterWarnSlot(t0 + unknownInviterWarnEvery)
		require.True(t, ok, "窗口过去之后必须重新放行,否则限频变成永久静默")
		assert.EqualValues(t, 3, skipped,
			"压掉的条数没交代 = 看日志的人分不出偶发一次还是持续一分钟的风暴")

		// 交代过一次就要清零,否则下一条会把同一批重复计一遍。
		ok, skipped = takeUnknownInviterWarnSlot(t0 + 2*unknownInviterWarnEvery)
		require.True(t, ok)
		assert.Zero(t, skipped, "压掉的条数被重复计了一遍")
	})

	t.Run("进程刚起来的第一条不受零值窗口影响", func(t *testing.T) {
		resetUnknownInviterWarn(t)
		// lastLog 的零值是 0。如果判定不排除零值,那么在 now < 60 的时钟下
		// (单元测试、容器里被改过的时钟)第一条会被自己的零值窗口吞掉。
		ok, _ := takeUnknownInviterWarnSlot(5)
		assert.True(t, ok, "第一条被零值窗口吞掉了")
	})
}
