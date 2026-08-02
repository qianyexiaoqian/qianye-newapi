package audit

// request.go —— 请求台账(qy_request_audits)的异步批量写入。
//
// # 为什么这一张表可以异步,而 qy_audit_logs 不行
//
// 资金审计是仲裁凭据:一笔提现批了却查不到"谁批的",纠纷就无法收场,
// 所以它同步写、失败即告警,甚至在 WriteTx 里与业务同生共死。
// 请求台账的价值在覆盖率而不在单行完整性 —— 每一个写请求都要留痕,
// 而任何一条的丢失都不会让某笔钱失去解释。让它同步写反而危险:
// 扩展库抖动会把每一个管理端请求都拖慢一个来回。
//
// # 但"可以丢"不等于"可以静默丢"
//
// 参照实现(sub2api)的队列满即丢弃、没有任何计数。那等于给自己留了一个
// 无法察觉的盲区:队列一旦长期打满,台账会系统性缺失最繁忙那段时间的记录,
// 而那恰恰是最需要它的时候。这里沿用本扩展 guard 的既有做法 ——
// dropped / failed 都是计数器,并从 RequestQueueStats 暴露到管理端健康接口。
//
// # 刻意没有 TruncateAll
//
// 参照实现提供了带 TOTP 的"一键清空全部审计"。这里不抄:
// 能被一键清空的审计不是审计,而做坏事的人恰恰最有动机去按那个按钮。
// 过期数据由 audit.retention_days 那套已有机制按天清理(见 retention.go)。

import (
	"context"
	"fmt"
	"sync"
	"sync/atomic"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/qianye/config"
	"github.com/QuantumNous/new-api/qianye/db"
	qymodel "github.com/QuantumNous/new-api/qianye/model"

	"github.com/bytedance/gopkg/util/gopool"
)

const (
	// requestQueueCapacity 是内存队列容量。管理端写请求的量级是每分钟个位数,
	// 4096 意味着扩展库要连续不可用很久才会开始丢。
	requestQueueCapacity = 4096
	// requestBatchSize 是单次 INSERT 的行数上限。
	requestBatchSize = 100
	// requestFlushInterval 是最长滞留时间:低流量时一条记录也不该在内存里过夜。
	requestFlushInterval = time.Second
	// requestFlushTimeout 是一次批量写的语句预算。
	requestFlushTimeout = 5 * time.Second
)

var (
	requestQueue     chan *qymodel.RequestAudit
	requestQueueOnce sync.Once

	requestSubmitted atomic.Int64
	requestDropped   atomic.Int64
	requestWritten   atomic.Int64
	requestFailed    atomic.Int64
)

// Record 把一条请求台账丢进队列。永不阻塞、永不返回错误。
//
// 队列满时丢弃并计数:调用方是 HTTP 中间件,跑在用户请求的线程上,
// 让它等一个已经写不动的数据库就是把台账的故障传染给业务本身。
func Record(row *qymodel.RequestAudit) {
	if row == nil || !config.Get().Audit.RequestOn() || !db.Available() {
		return
	}
	startRequestWriter()
	requestSubmitted.Add(1)
	select {
	case requestQueue <- row:
	default:
		n := requestDropped.Add(1)
		if n == 1 || n%100 == 0 {
			common.SysError(fmt.Sprintf(
				"qianye: 请求台账队列已满,累计丢弃 %d 条(最近: %s %s)—— "+
					"这段时间的写请求将没有 HTTP 留痕,请检查扩展库写入能力",
				n, row.Method, row.Path))
		}
	}
}

// RequestQueueStats 暴露队列水位与丢弃/失败计数,供管理端健康面板告警。
//
// 开头的 startRequestWriter() 不能省:requestQueue 只在 Once 里赋值,
// 绕过它直接读 cap/len 与首个 Record 的写构成数据竞争 —— 进程刚起、
// 还没有任何写请求时打开健康面板即可命中。这与 guard.QueueStats 是同一个坑。
func RequestQueueStats() map[string]any {
	startRequestWriter()
	return map[string]any{
		"capacity":  cap(requestQueue),
		"pending":   len(requestQueue),
		"submitted": requestSubmitted.Load(),
		"written":   requestWritten.Load(),
		"dropped":   requestDropped.Load(),
		"failed":    requestFailed.Load(),
	}
}

func startRequestWriter() {
	requestQueueOnce.Do(func() {
		requestQueue = make(chan *qymodel.RequestAudit, requestQueueCapacity)
		gopool.Go(func() { drainRequestQueue(requestQueue) })
	})
}

// drainRequestQueue 攒批落库:攒够 requestBatchSize 或到点即写。
//
// 单 worker 而不是多 worker:批量 INSERT 本身就是把并发压成一条语句,
// 多个 worker 只会让同一张表上出现无谓的并发写。
func drainRequestQueue(ch <-chan *qymodel.RequestAudit) {
	ticker := time.NewTicker(requestFlushInterval)
	defer ticker.Stop()

	batch := make([]*qymodel.RequestAudit, 0, requestBatchSize)
	for {
		select {
		case row := <-ch:
			if row == nil {
				continue
			}
			batch = append(batch, row)
			if len(batch) >= requestBatchSize {
				batch = flushRequestBatch(batch)
			}
		case <-ticker.C:
			batch = flushRequestBatch(batch)
		}
	}
}

// flushRequestBatch 写一批并返回清空后的切片(复用底层数组)。
//
// 失败不重试:重试意味着要把这一批一直攥在内存里,而它前面还有一个满不了的
// 队列在持续入队 —— 那是把"丢一批台账"换成"OOM 打死整个网关"。
// 失败计数与告警是这里唯一正确的收尾。
func flushRequestBatch(batch []*qymodel.RequestAudit) []*qymodel.RequestAudit {
	if len(batch) == 0 {
		return batch
	}
	gdb := db.Get()
	if gdb == nil {
		requestFailed.Add(int64(len(batch)))
		return batch[:0]
	}
	ctx, cancel := context.WithTimeout(context.Background(), requestFlushTimeout)
	defer cancel()

	if err := gdb.WithContext(ctx).CreateInBatches(batch, requestBatchSize).Error; err != nil {
		db.MarkFailure(err)
		n := requestFailed.Add(int64(len(batch)))
		common.SysError(fmt.Sprintf(
			"qianye: 请求台账写入失败,累计 %d 条未落库(业务未受影响): %s", n, err.Error()))
	} else {
		requestWritten.Add(int64(len(batch)))
	}
	return batch[:0]
}
