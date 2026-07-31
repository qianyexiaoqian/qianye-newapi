package guard

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/QuantumNous/new-api/qianye/db"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// 测试进程里扩展库从未初始化,因此 Available() 恒为 false ——
// 这正好是 A3 复现所需要的"worker 出队那一刻扩展不可用"的状态。
func requireExtensionUnavailable(t *testing.T) {
	t.Helper()
	require.False(t, Available(), "测试前提:扩展库未初始化")
}

func failStreakNow(t *testing.T) int32 {
	t.Helper()
	v, ok := db.Stats()["fail_streak"].(int32)
	require.True(t, ok, "db.Stats() 必须暴露 fail_streak")
	return v
}

// TestWorkerRunsDequeuedJobEvenWhenUnavailable 固化 A3。
//
// 修复前 worker 的循环体直接复用 Hot,而 Hot 开头就判 Available();入队时可用、
// 出队时刚好一次 3 秒 ping 超时把 healthy 置 false,整条积压队列就被逐条静默
// 丢弃 —— 既不计入 dropped 也没有任何日志,而消费返佣没有 outbox 补偿路径,
// 那些佣金是永久丢失。
func TestWorkerRunsDequeuedJobEvenWhenUnavailable(t *testing.T) {
	requireExtensionUnavailable(t)

	ch := make(chan hotJob, 2)
	ran := make([]string, 0, 2)
	ch <- hotJob{name: "commission.consume", fn: func(ctx context.Context) error {
		ran = append(ran, "commission.consume")
		return nil
	}}
	ch <- hotJob{name: "violation.persist", fn: func(ctx context.Context) error {
		ran = append(ran, "violation.persist")
		return nil
	}}
	close(ch)

	drainQueue(ch)

	assert.Equal(t, []string{"commission.consume", "violation.persist"}, ran,
		"已入队的作业出队后必须执行,不得因为此刻扩展不可用而静默丢弃")
}

// TestSkippedJobsAreCounted 保证"没执行"这件事永远可观测。
//
// 入队前被 Available() 挡掉同样意味着用户该拿的佣金没有落账,必须与 dropped
// 同级别地计数并告警,而不是静默 return。
func TestSkippedJobsAreCounted(t *testing.T) {
	requireExtensionUnavailable(t)

	before := skipped.Load()
	Hot("commission.consume", func(ctx context.Context) error {
		t.Fatal("扩展不可用时不应执行")
		return nil
	})
	HotAsync("violation.persist", func(ctx context.Context) error {
		t.Fatal("扩展不可用时不应执行")
		return nil
	})

	assert.EqualValues(t, before+2, skipped.Load())
	assert.EqualValues(t, skipped.Load(), QueueStats()["skipped"],
		"健康面板必须能看到跳过数")
}

// TestHotRunOnlyMarksSuccessWhenDBWasTouched 固化 C4。
//
// 修复前 Hot 对任何返回 nil 的 hook 都调 db.MarkSuccess()。availability.sample
// 是纯内存 O(1) 累加、必然返回 nil,且频率远高于失败频率,于是失败计数被反复
// 清零,熔断在"扩展库可达但查询变慢"这唯一重要的场景下永远打不开。
func TestHotRunOnlyMarksSuccessWhenDBWasTouched(t *testing.T) {
	t.Run("纯内存 hook 不得清零失败计数", func(t *testing.T) {
		db.MarkSuccess()
		db.MarkFailure(errors.New("invalid connection"))
		require.EqualValues(t, 1, failStreakNow(t), "驱动层连接错误应计入熔断")

		hotRun("availability.sample", func(ctx context.Context) error { return nil })

		assert.EqualValues(t, 1, failStreakNow(t),
			"没访问过扩展库的 hook 无权给熔断投健康票")
	})

	t.Run("hook 返回连接级错误时计入失败", func(t *testing.T) {
		db.MarkSuccess()
		hotRun("commission.consume", func(ctx context.Context) error {
			return errors.New("driver: bad connection")
		})
		assert.EqualValues(t, 1, failStreakNow(t))
	})

	// 超时是我们自己设的预算到期,不是数据库的健康信号。
	//
	// 把它计入熔断会形成一条放大链:热路径预算只有几百毫秒,一次尾延迟抖动
	// 就能连续攒够阈值,把整个扩展熔断数十秒,期间所有事件被直接丢弃 ——
	// 用一次抖动换来数十秒的全站丢弃,远比慢查询本身严重。
	//
	// 真正的"库坏了"由驱动层错误识别(含 readTimeout 触发的 i/o timeout),
	// 那条路径不受影响,上面两个子用例就是它的护栏。
	t.Run("超时不得计入熔断", func(t *testing.T) {
		db.MarkSuccess()
		hotRun("commission.consume", func(ctx context.Context) error {
			return context.DeadlineExceeded
		})
		assert.EqualValues(t, 0, failStreakNow(t),
			"预算到期不是数据库故障,计入熔断会让一次抖动放大成全站丢弃")

		// 租约丢失导致的 ctx 取消同理 —— 那是别的节点接管了,与库的健康无关。
		hotRun("commission.settle", func(ctx context.Context) error {
			return context.Canceled
		})
		assert.EqualValues(t, 0, failStreakNow(t),
			"租约易主导致的取消更不该被当成数据库故障")
	})

	t.Run("业务错误不计入熔断", func(t *testing.T) {
		db.MarkSuccess()
		hotRun("commission.consume", func(ctx context.Context) error {
			return errors.New("Duplicate entry '1' for key 'uk_qy_commission_idem'")
		})
		assert.EqualValues(t, 0, failStreakNow(t), "幂等冲突是正常现象,不能打开熔断")
	})

	db.MarkSuccess()
}

// TestHotRunCarriesTimeoutAndSurvivesPanic 固化 guard.Hot 对外承诺的两条:
// hook 拿到的 ctx 必须是有限期限的,panic 绝不能冒泡到 relay。
func TestHotRunCarriesTimeoutAndSurvivesPanic(t *testing.T) {
	var hasDeadline bool
	hotRun("commission.consume", func(ctx context.Context) error {
		_, hasDeadline = ctx.Deadline()
		return nil
	})
	assert.True(t, hasDeadline, "hook 必须在带超时的 ctx 下执行")

	assert.NotPanics(t, func() {
		hotRun("violation.persist", func(ctx context.Context) error {
			panic("boom")
		})
	})
}

// TestInlineAtHighWaterRejectsCrossDBJobs 固化 C3(c)。
//
// 修复前队列过 80% 水位就把作业同步执行在调用方线程上。commission.consume /
// violation.persist 会碰扩展库与主库(封号链路里还有主库事务 + Redis + 逐批
// 会话撤销),同步执行等于把没有上界的 IO 搬回 relay 结算线程 ——
// hot_path_timeout_ms 只能让 Go 侧放弃等待,拦不住底层连接继续被占着。
func TestInlineAtHighWaterRejectsCrossDBJobs(t *testing.T) {
	cases := []struct {
		name     string
		job      string
		pending  int
		capacity int
		want     bool
	}{
		{"低水位:纯内存作业照常入队", "availability.sample", 100, 4096, false},
		{"高水位:纯内存作业同步执行以产生背压", "availability.sample", 3277, 4096, true},
		{"队列已满:纯内存作业同步执行", "availability.sample", 4096, 4096, true},
		{"高水位:消费返佣绝不同步执行", "commission.consume", 3277, 4096, false},
		{"队列已满:消费返佣绝不同步执行", "commission.consume", 4096, 4096, false},
		{"高水位:违规归档(含封号链路)绝不同步执行", "violation.persist", 4000, 4096, false},
		{"高水位:充值返佣绝不同步执行", "commission.redeem", 4000, 4096, false},
		{"高水位:任务退款冲正绝不同步执行", "commission.task_refund", 4000, 4096, false},
		{"未登记的新作业默认不允许同步执行", "future.unknown_job", 4096, 4096, false},
		{"队列尚未创建时不同步执行", "availability.sample", 0, 0, false},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			assert.Equal(t, tc.want, inlineAtHighWater(tc.job, tc.pending, tc.capacity))
		})
	}
}

// TestQueueStatsInitializesQueue 固化 D2。
//
// queue 只在 startWorkers 的 queueOnce 里赋值,而 startWorkers 只由 HotAsync 调用。
// QueueStats 绕开这个 once 直接读 cap/len,与首个 relay 请求的写构成数据竞争
// (进程刚起、还没有流量时打开健康面板即可命中),按 Go 内存模型是未定义行为。
func TestQueueStatsInitializesQueue(t *testing.T) {
	stats := QueueStats()

	capacity, ok := stats["capacity"].(int)
	require.True(t, ok)
	assert.Positive(t, capacity, "面板不得在未跑过 HotAsync 时把容量显示成 0")
	assert.NotNil(t, queue, "读取队列水位之前必须先完成队列初始化")
}

// 异步 worker 不能沿用同步热路径的预算。
//
// worker 跑在自己的 goroutine 上,不占 relay 线程。返佣的一次冷缓存轮要做
// 五六次数据库往返,200ms 根本跑不完;超时后既无重试也无 outbox 补偿,
// 那笔佣金就是永久丢失 —— 等于把"别拖住 relay"的保护措施用成了资损来源。
func TestAsyncBudgetIsLargerThanSyncBudget(t *testing.T) {
	sync := syncBudget()
	async := asyncBudget()

	assert.Equal(t, 200*time.Millisecond, sync,
		"同步路径跑在 relay 线程上,预算必须很短")
	assert.GreaterOrEqual(t, async, time.Second,
		"异步预算至少要能容纳一次冷缓存的多次往返")
	assert.Greater(t, async, sync,
		"异步预算必须大于同步预算,否则区分两者就没有意义")
}

// 队列 worker 实际拿到的必须是异步预算,而不是同步预算。
//
// 这一条盯的是"改了函数却没接上调用链"——单测 asyncBudget() 返回值是不够的,
// 必须验证 drainQueue 真的用了它。
func TestDrainQueueUsesAsyncBudget(t *testing.T) {
	// drainQueue 刻意不判可用性(A3 的修复),因此无需初始化扩展库。
	got := make(chan time.Duration, 1)
	ch := make(chan hotJob, 1)
	ch <- hotJob{
		name: "test.deadline",
		fn: func(ctx context.Context) error {
			dl, ok := ctx.Deadline()
			require.True(t, ok, "worker 必须给作业带上 deadline")
			got <- time.Until(dl)
			return nil
		},
	}
	close(ch)
	drainQueue(ch)

	select {
	case d := <-got:
		// 留出执行抖动的余量,只断言量级:必须明显超过同步预算。
		assert.Greater(t, d, syncBudget(),
			"worker 拿到的预算不能是同步路径的 200ms —— 那正是返佣丢失的成因")
	default:
		t.Fatal("作业未被执行")
	}
}
