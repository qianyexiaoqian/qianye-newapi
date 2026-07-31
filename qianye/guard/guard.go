// Package guard 是扩展所有调用点的统一降级判断入口。
//
// 核心原则:扩展绝不能拖垮主业务。
//   - 热路径(relay、消费日志、采样):fail-open。扩展不可用就静默跳过,
//     绝不阻塞、绝不报错、绝不让 panic 冒泡到 relay。
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
	default:
		return false
	}
}

// ─────────────────────────── 热路径:fail-open ───────────────────────────

// Hot 同步执行热路径 hook。
//
// 保证:
//  1. 扩展禁用 / 库不可用 / 熔断打开 → 直接返回,什么都不做
//  2. panic 一律吞掉并记日志,绝不冒泡到 relay
//  3. 在 hot_path_timeout_ms 的 ctx 下执行,超时即放弃
//  4. 错误只写日志(按 name 限频防刷屏),永不返回给调用方
//
// 只适用于必须同步完成的极轻量操作。凡是要查库的,一律用 HotAsync。
func Hot(name string, fn func(ctx context.Context) error) {
	if !Available() {
		return
	}
	defer recoverHot(name)

	timeout := config.Get().Runtime.HotPathTimeoutMs
	if timeout <= 0 {
		timeout = 200
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Duration(timeout)*time.Millisecond)
	defer cancel()

	if err := fn(ctx); err != nil {
		db.MarkFailure(err)
		logThrottled(name, err)
		return
	}
	db.MarkSuccess()
}

type hotJob struct {
	name string
	fn   func(ctx context.Context) error
}

var (
	queue     chan hotJob
	queueOnce sync.Once
	dropped   atomic.Int64
	submitted atomic.Int64
)

// HotAsync 把工作丢进有界队列,由独立 worker 消费。
//
// relay 线程绝不能等待扩展库 —— 消费返佣、可用率采样这类 hook 必须走这里。
//
// 队列水位超过 80% 时降级为同步执行:宁可给 relay 增加一点延迟,
// 也不能丢掉用户该拿的佣金。只有队列真正满了才丢弃并计数。
func HotAsync(name string, fn func(ctx context.Context) error) {
	if !Available() {
		return
	}
	startWorkers()
	submitted.Add(1)

	capacity := cap(queue)
	if capacity > 0 && len(queue)*5 >= capacity*4 {
		// 高水位:同步执行,产生背压而不是丢数据。
		Hot(name, fn)
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
			gopool.Go(func() {
				for job := range queue {
					Hot(job.name, job.fn)
				}
			})
		}
	})
}

// QueueStats 暴露队列水位与丢弃数,供管理端健康面板告警。
func QueueStats() map[string]any {
	return map[string]any{
		"capacity":  cap(queue),
		"pending":   len(queue),
		"submitted": submitted.Load(),
		"dropped":   dropped.Load(),
	}
}

func recoverHot(name string) {
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
