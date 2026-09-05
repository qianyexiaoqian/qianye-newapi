package invite

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

// warnf 是限频之外的一次性告警。用于"绝不该发生"的情形 ——
// 失效广播投不出去、索引补建失败,这些必须在日志里能被 grep 到。
func warnf(format string, args ...any) {
	common.SysError("qianye/invite: " + fmt.Sprintf(format, args...))
}
