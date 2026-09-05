package commission

import (
	"fmt"
	"sync/atomic"

	"github.com/QuantumNous/new-api/common"
)

// counter 是最小可用的计数器。刻意不引入指标库:
// 这些数字只服务于管理端健康面板与告警,不需要维度、直方图或采集端点。
type counter struct{ v atomic.Int64 }

func newCounter() *counter     { return &counter{} }
func (c *counter) Add(n int64) { c.v.Add(n) }
func (c *counter) Load() int64 { return c.v.Load() }

var (
	consumeEvents      = newCounter() // 进入 hook 且确有邀请人的消费事件
	accrualCreated     = newCounter()
	accrualAccumulated = newCounter()
	accrualFailed      = newCounter()
	accrualSkipped     = newCounter() // 风控/口径排除
	// accrualCapped 是"单笔封顶削掉过增量"的次数。它不为零就意味着账本里
	// 有 gross < base×rate 的行,运营需要据此判断 max_per_order_stardust 是不是配小了。
	accrualCapped = newCounter()
	topupScanned  = newCounter()
	// topupHeld 是"因为有订单计佣失败,本轮游标被钉住"的次数。
	// 持续增长意味着有一笔充值计佣卡住了,后面的订单也在排队等它。
	topupHeld = newCounter()
	// topupLateSwept 是"被前向游标越过之后才转 success、由迟付回收扫描捞回来"
	// 的订单数。它不为零说明确实有订单在窗口外才被支付。
	topupLateSwept  = newCounter()
	clawbackCreated = newCounter()
	settleRuns      = newCounter()
	settleGranted   = newCounter() // 累计发放的整数额度
	settleReclaimed = newCounter()
	settleFailed    = newCounter()
	// creditOpened / creditDone / creditFailed / creditHeld 是自动入账四个结局的计数。
	// held 不为零就意味着有一笔钱既没到账也没退回,必须有人去对账台裁决。
	creditOpened = newCounter()
	creditDone   = newCounter()
	creditFailed = newCounter()
	creditHeld   = newCounter()
)

func metricsSnapshot() map[string]any {
	return map[string]any{
		"consume_events":      consumeEvents.Load(),
		"accrual_created":     accrualCreated.Load(),
		"accrual_accumulated": accrualAccumulated.Load(),
		"accrual_failed":      accrualFailed.Load(),
		"accrual_skipped":     accrualSkipped.Load(),
		"accrual_capped":      accrualCapped.Load(),
		"topup_scanned":       topupScanned.Load(),
		"topup_cursor_held":   topupHeld.Load(),
		"topup_late_swept":    topupLateSwept.Load(),
		"clawback_created":    clawbackCreated.Load(),
		"settle_runs":         settleRuns.Load(),
		"settle_granted":      settleGranted.Load(),
		"settle_reclaimed":    settleReclaimed.Load(),
		"settle_failed":       settleFailed.Load(),
		"credit_opened":       creditOpened.Load(),
		"credit_done":         creditDone.Load(),
		"credit_failed":       creditFailed.Load(),
		"credit_held":         creditHeld.Load(),
	}
}

// warnf 是限频之外的一次性告警。用于"绝不该发生"的情形 ——
// 金额触顶、欠账产生、扫描游标倒退、入账挂起,这些必须在日志里能被 grep 到。
func warnf(format string, args ...any) {
	common.SysError("qianye/commission: " + fmt.Sprintf(format, args...))
}
