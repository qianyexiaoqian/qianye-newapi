// Package guard 是扩展所有调用点的统一降级判断入口。
//
// 核心原则:扩展绝不能拖垮主业务。
//   - 热路径(relay、消费日志、采样):fail-open。扩展不可用就跳过(计入 skipped
//     并限频告警),绝不阻塞、绝不报错、绝不让 panic 冒泡到 relay。
//     "跳过"只发生在入队之前;已经进了队列的作业出队后一律执行 —— 那些事件
//     只存在于内存闭包里,没有任何补偿路径,丢一条就是永久丢一条。
//   - 非热路径(划转、佣金、提现的 HTTP 接口):fail-closed。不可用时返回 503,
//     宁可让用户重试,也不能在账目不确定的情况下动钱。
package guard

import (
	"context"
	"fmt"
	"net/http"
	"runtime/debug"
	"sync"
	"sync/atomic"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/qianye/config"
	"github.com/QuantumNous/new-api/qianye/db"

	"github.com/bytedance/gopkg/util/gopool"
	"github.com/gin-gonic/gin"
)

// Flag 标识单个功能模块。
type Flag string

const (
	FlagCore         Flag = "core"
	FlagTransfer     Flag = "transfer"
	FlagCommission   Flag = "commission"
	FlagWithdraw     Flag = "withdraw"
	FlagLogMetrics   Flag = "log_metrics"
	FlagAvailability Flag = "availability"
	FlagViolation    Flag = "violation"
	FlagGroupPricing Flag = "group_pricing"
	FlagGroupMatrix  Flag = "group_matrix"
	FlagLottery      Flag = "lottery"
	FlagTicket       Flag = "ticket"
)

// 响应 code。前端按 code 映射到 i18n 文案,message 只作兜底,
// 避免每个模块各写一套中文硬编码。
const (
	CodeDisabled    = "qy_disabled"
	CodeFeatureOff  = "qy_feature_off"
	CodeUnavailable = "qy_unavailable"
)

// Enabled 表示扩展已配置且启用。
func Enabled() bool { return config.Enabled() }

// Available 表示扩展启用且扩展库当前可用(含熔断状态)。
func Available() bool { return Enabled() && db.Available() }

// Feature 判断单个功能是否可用(功能开关 ∧ Available)。
func Feature(f Flag) bool {
	if !Available() {
		return false
	}
	return featureOn(f)
}

func featureOn(f Flag) bool {
	c := config.Get()
	switch f {
	case FlagCore:
		return true
	case FlagTransfer:
		return c.Transfer.Enabled
	case FlagCommission:
		return c.Commission.Enabled
	case FlagWithdraw:
		return c.Withdraw.Enabled
	case FlagLogMetrics:
		return c.LogMetrics.ReasoningColumn() || c.LogMetrics.CacheRatioColumn()
	case FlagAvailability:
		return c.Availability.Enabled
	case FlagViolation:
		return c.Violation.Enabled
	case FlagGroupPricing:
		return c.GroupPricing.Enabled
	case FlagGroupMatrix:
		return c.GroupMatrix.Enabled
	case FlagLottery:
		return c.Lottery.Enabled
	case FlagTicket:
		return c.Ticket.Enabled
	default:
		return false
	}
}

// ─────────────────────────── 热路径:fail-open ───────────────────────────

// Hot 同步执行热路径 hook。
//
// 保证:
//  1. 扩展禁用 / 库不可用 / 熔断打开 → 直接返回并计入 skipped(不会静默消失)
//  2. panic 一律吞掉并记日志,绝不冒泡到 relay
//  3. 在 hot_path_timeout_ms 的 ctx 下执行,超时即放弃
//  4. 错误只写日志(按 name 限频防刷屏),永不返回给调用方
//
// 只适用于必须同步完成的极轻量操作。凡是要查库的,一律用 HotAsync。
func Hot(name string, fn func(ctx context.Context) error) {
	if !Available() {
		recordSkip(name)
		return
	}
	hotRun(name, fn)
}

// hotRun 是不做可用性判断的执行体。
//
// 与 Hot 分开是 A3 的修复:worker 从队列取出作业后必须无条件执行。
// 原先 worker 直接复用 Hot,入队时可用、出队时刚好探测失败(单次 3 秒 ping
// 超时即置 healthy=false)就会让整个积压队列被逐条静默丢弃 —— 既不计数
// 也不告警,而消费返佣没有任何 outbox 补偿路径,那些佣金就是永久丢失。
// 执行失败至少会走 MarkFailure + 限频日志,是可观测的。
func hotRun(name string, fn func(ctx context.Context) error) {
	hotRunWithBudget(name, fn, syncBudget())
}

// syncBudget 是真正跑在 relay 线程上的作业的预算,必须很短。
func syncBudget() time.Duration {
	ms := config.Get().Runtime.HotPathTimeoutMs
	if ms <= 0 {
		ms = 200
	}
	return time.Duration(ms) * time.Millisecond
}

// asyncBudget 是队列 worker 的预算。
//
// 它不能沿用同步路径的 200ms:worker 跑在自己的 goroutine 上,不占 relay 线程,
// 而返佣的一次冷缓存轮要依次做 resolveInviter(回主库)、ensureRelation、
// blockedInvitees、writeAccrual 共五六次往返,200ms 根本跑不完。
// 超时之后没有重试也没有 outbox 补偿,那笔佣金就是永久丢失 —— 用一个为
// "别拖住 relay"设计的预算去卡一条本来就不在 relay 线程上的链路,是把
// 保护措施用成了资损来源。
func asyncBudget() time.Duration {
	ms := config.Get().Runtime.HotAsyncTimeoutMs
	if ms <= 0 {
		ms = 3000
	}
	return time.Duration(ms) * time.Millisecond
}

func hotRunWithBudget(name string, fn func(ctx context.Context) error, budget time.Duration) {
	defer RecoverHot(name)

	ctx, cancel := context.WithTimeout(context.Background(), budget)
	defer cancel()
	ctx, touchedDB := db.WithOpProbe(ctx)

	if err := fn(ctx); err != nil {
		db.MarkFailure(err)
		logThrottled(name, err)
		return
	}
	// 只有真正跑过扩展库语句的 hook 才有资格清零熔断的失败计数(C4)。
	// 纯内存 hook(可用率采样)必然返回 nil 且频率远高于失败频率,
	// 无条件 MarkSuccess 会让熔断在"库可达但查询变慢"时永远打不开。
	if touchedDB() {
		db.MarkSuccess()
	}
}

type hotJob struct {
	name string
	fn   func(ctx context.Context) error
}

var (
	queue     chan hotJob
	queueOnce sync.Once
	dropped   atomic.Int64
	skipped   atomic.Int64
	submitted atomic.Int64
)

// syncSafeJobs 列出允许在队列高水位时同步执行(直接跑在 relay 线程上)的作业。
//
// 默认是"不允许"。要进这张表,作业必须被证明是纯内存、O(1)、无任何 I/O:
// 一旦作业会碰扩展库或主库,同步执行就等于把不可控的等待搬回 relay 结算线程,
// hot_path_timeout_ms 只能让 Go 侧放弃等待,却拦不住底层连接继续被占着,
// 而封号链路里还有主库事务 + Redis + 逐批会话撤销这类没有上界的 I/O(C3)。
// 宁可在队列真正满时丢弃并告警,也不接受 relay 被拖住。
var syncSafeJobs = map[string]bool{
	"availability.sample": true, // 纯内存 sync.Map + atomic 累加,见 availability/aggregate.go
	// 影子差额只做一次 map 查找 + atomic 累加(grouppricing/shadow.go 的 observe),
	// 没有任何 I/O。它必须进这张表:影子模式的全部意义就是让运营算清"切换后
	// 会多收/少收多少",高水位时静默丢样本会让那个数字系统性偏小,
	// 而偏小的对账结论恰恰会让人放心地关掉影子模式。
	"grouppricing.shadow": true,
}

// inlineAtHighWater 判断队列高水位时,该作业能否直接跑在调用方(relay)线程上。
//
// 80% 水位是背压信号。对纯内存作业,同步执行的代价是几十纳秒,换来"绝不丢数据";
// 对会碰数据库的作业,同步执行的代价没有上界,必须拒绝 —— 队列没满就继续排队,
// 满了就按 dropped 计数并告警。
func inlineAtHighWater(name string, pending, capacity int) bool {
	if capacity <= 0 || pending*5 < capacity*4 {
		return false
	}
	return syncSafeJobs[name]
}

// HotAsync 把工作丢进有界队列,由独立 worker 消费。
//
// relay 线程绝不能等待扩展库 —— 消费返佣、可用率采样这类 hook 必须走这里。
//
// 队列高水位时只有 syncSafeJobs 里的纯内存作业会降级为同步执行(产生背压而
// 不丢数据);会碰数据库的作业照常尝试入队,只有队列真正满了才丢弃并告警。
func HotAsync(name string, fn func(ctx context.Context) error) {
	if !Available() {
		recordSkip(name)
		return
	}
	startWorkers()
	submitted.Add(1)

	if inlineAtHighWater(name, len(queue), cap(queue)) {
		hotRun(name, fn)
		return
	}

	select {
	case queue <- hotJob{name: name, fn: fn}:
	default:
		n := dropped.Add(1)
		// 丢弃是"用户该拿的钱没拿到"的唯一路径,必须显式告警而不是静默。
		if n == 1 || n%100 == 0 {
			common.SysError(fmt.Sprintf(
				"qianye: 热路径队列已满,累计丢弃 %d 个事件(最近: %s)—— "+
					"请检查扩展库性能或调大 runtime.hot_hook_queue_size", n, name))
		}
	}
}

func startWorkers() {
	queueOnce.Do(func() {
		c := config.Get().Runtime
		size := c.HotHookQueueSize
		if size <= 0 {
			size = 4096
		}
		workers := c.HotHookWorkers
		if workers <= 0 {
			workers = 2
		}
		queue = make(chan hotJob, size)
		for i := 0; i < workers; i++ {
			gopool.Go(func() { drainQueue(queue) })
		}
	})
}

// drainQueue 是 worker 的循环体:出队即执行,不再判可用性(见 hotRun)。
//
// 用异步预算而非同步预算:worker 不占 relay 线程,没有理由套用为
// "别拖住 relay"设计的 200ms(见 asyncBudget)。
func drainQueue(ch <-chan hotJob) {
	for job := range ch {
		hotRunWithBudget(job.name, job.fn, asyncBudget())
	}
}

// recordSkip 记录"因扩展不可用而未执行"的作业。
//
// 与 dropped 同级别的告警:入队前被挡掉同样意味着用户该拿的佣金没有落账,
// 静默返回会让这类丢失完全不可观测。
func recordSkip(name string) {
	n := skipped.Add(1)
	if n == 1 || n%1000 == 0 {
		common.SysError(fmt.Sprintf(
			"qianye: 扩展不可用,累计跳过 %d 个热路径事件(最近: %s)—— "+
				"这些事件没有补偿路径,请检查扩展库健康状态", n, name))
	}
}

// QueueStats 暴露队列水位、丢弃数与跳过数,供管理端健康面板告警。
//
// 开头的 startWorkers() 不能省:queue 只在 queueOnce 里赋值,绕过它直接读
// cap/len 与首个 HotAsync 的写构成数据竞争(进程刚起、还没有 relay 流量时
// 打开健康面板即可命中),按 Go 内存模型是未定义行为(D2)。
func QueueStats() map[string]any {
	startWorkers()
	return map[string]any{
		"capacity":  cap(queue),
		"pending":   len(queue),
		"submitted": submitted.Load(),
		"dropped":   dropped.Load(),
		"skipped":   skipped.Load(),
	}
}

// RecoverHot 是热路径 hook 的 panic 拦截器,用法固定为函数首行 defer。
//
// 导出是因为不是所有热路径都经过 HotAsync:hook 的**同步段**(把上游的结构体
// 压成值快照那一步)必须在调用方自己的栈上跑,那一段的 panic 会一路冒泡进
// relay,把"扩展功能出错"变成"用户请求 500"。扩展模块相对主业务永远是附加物,
// 任何一处都不该有能力打死它。
func RecoverHot(name string) {
	if r := recover(); r != nil {
		common.SysError(fmt.Sprintf("qianye: 热路径 hook %s 发生 panic(已拦截,不影响主流程): %v\n%s",
			name, r, debug.Stack()))
	}
}

var (
	logMu    sync.Mutex
	lastLog  = map[string]int64{}
	logEvery = int64(60) // 同名错误最多每 60 秒记一次
)

func logThrottled(name string, err error) {
	now := common.GetTimestamp()
	logMu.Lock()
	last := lastLog[name]
	if now-last < logEvery {
		logMu.Unlock()
		return
	}
	lastLog[name] = now
	logMu.Unlock()
	common.SysError(fmt.Sprintf("qianye: 热路径 hook %s 执行失败(已忽略): %v", name, err))
}

// ─────────────────────────── 非热路径:fail-closed ───────────────────────────

// RequireAPI 是所有扩展 HTTP handler 的第一行。不可用时写响应并 Abort,返回 false。
//
// 语义严格区分(前端据此决定是隐藏入口还是提示重试):
//   - 扩展未启用 / 功能关闭 → 404,前端直接隐藏入口,不显示任何报错
//   - 扩展库不可用 → 503 + Retry-After,前端提示稍后重试
//   - 权限不足 → 403,由上游 AdminAuth 中间件负责,不在这里处理
func RequireAPI(c *gin.Context, f Flag) bool {
	if !Enabled() {
		abort(c, http.StatusNotFound, CodeDisabled, "扩展功能未启用")
		return false
	}
	if !featureOn(f) {
		abort(c, http.StatusNotFound, CodeFeatureOff, "该功能未启用")
		return false
	}
	if !db.Available() {
		c.Header("Retry-After", "30")
		abort(c, http.StatusServiceUnavailable, CodeUnavailable, "扩展服务暂时不可用,请稍后再试")
		return false
	}
	return true
}

func abort(c *gin.Context, status int, code, msg string) {
	c.AbortWithStatusJSON(status, gin.H{
		"success": false,
		"code":    code,
		"message": msg,
	})
}

// ColdContext 返回非热路径操作应使用的 ctx(带 cold_path_timeout_ms 超时)。
func ColdContext(parent context.Context) (context.Context, context.CancelFunc) {
	timeout := config.Get().Runtime.ColdPathTimeoutMs
	if timeout <= 0 {
		timeout = 3000
	}
	return context.WithTimeout(parent, time.Duration(timeout)*time.Millisecond)
}
