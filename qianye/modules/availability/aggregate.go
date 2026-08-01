package availability

import (
	"fmt"
	"sync"
	"sync/atomic"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/qianye/config"
)

// hotSeriesLimit 是内存里 (bucket, group, model) 键的硬上限。
//
// 分组名与模型名都来自渠道配置的自由文本,一次手滑的多行粘贴就能造出上千个
// "分组";模型名更是客户端可控。没有上限,一个构造出来的请求流就能把内存打爆。
// 超限后新键一律丢弃并计数,已有键继续正常累加 —— 保住存量数据的正确性。
const hotSeriesLimit = 20000

const (
	minBucketSeconds     = 30
	maxBucketSeconds     = 3600
	defaultBucketSeconds = 300
	maxModelNameRunes    = 128
	maxGroupNameRunes    = 64
)

// unknownGroup 用于 UsingGroup 为空的样本。
//
// 刻意不兜底成 "default":上游 pkg/perf_metrics 就是这么干的,结果是所有
// 分组解析失败的请求都被记到 default 头上,把该分组的可用率无端拉低 ——
// 这正是用户抱怨"整体很不准确"的成因之一,不能继承。
const unknownGroup = "unknown"

// dimKey 是内存热桶的键。
type dimKey struct {
	bucketTs int64
	group    string
	model    string
}

// counters 是一个热桶的原子计数器组。
//
// 全程 sync.Map + atomic,没有任何互斥锁:采样跑在 relay 全量流量上,
// 一把全局锁就是全站吞吐的瓶颈。
type counters struct {
	reqTotal     atomic.Int64
	success      atomic.Int64
	softFail     atomic.Int64
	noChannel    atomic.Int64
	timeout      atomic.Int64
	upstream     atomic.Int64
	rateLimit    atomic.Int64
	clientError  atomic.Int64
	internalErr  atomic.Int64
	quota        atomic.Int64
	violation    atomic.Int64
	clientGone   atomic.Int64
	channelTest  atomic.Int64
	latencySum   atomic.Int64
	latencyCount atomic.Int64
	ttftSum      atomic.Int64
	ttftCount    atomic.Int64
	outputTokens atomic.Int64
	generationMs atomic.Int64
	speedCount   atomic.Int64
}

var (
	hotBuckets sync.Map // dimKey → *counters
	hotSeries  atomic.Int64

	// 采样与落库的可观测指标。
	//
	// 注意:**读侧已随管理页移除** —— 原先唯一的消费方是
	// api.go 的 adminStats(/admin/availability/stats),该页面按项目方要求整体删除,
	// 因此下面这批计数器目前只写不读。刻意保留而不是一并删掉:它们是采样链路的
	// 唯一自检信号(区分「没人用这个模型」与「hook 没生效」),将来接告警或
	// 运维自检端点时直接读即可,删了就得把 flush/rollup/cleanup 三处再改一遍。
	statObserved   atomic.Int64
	statNoModel    atomic.Int64
	statSeriesFull atomic.Int64
	statTruncated  atomic.Int64
	statFlushRuns  atomic.Int64
	statFlushRows  atomic.Int64
	statFlushFail  atomic.Int64
	statFlushAt    atomic.Int64
	statRollupAt   atomic.Int64
	statRollupRows atomic.Int64
	statCleanupAt  atomic.Int64
	statCleanupRow atomic.Int64
)

// observe 把一次分类完成的样本累加进内存热桶。纯内存 O(1),无锁无 IO。
func observe(s sample) {
	c, ok := loadOrCreate(dimKey{bucketTs: s.BucketTs, group: s.Group, model: s.Model})
	if !ok {
		return
	}
	statObserved.Add(1)
	c.record(s)
}

// record 把样本累加进一组计数器。Outcome 与列的对应关系全部集中在这里,
// 落库、查询、口径三处都依赖它不出错。
func (c *counters) record(s sample) {
	c.reqTotal.Add(1)
	switch s.Outcome {
	case OutcomeSuccess:
		c.success.Add(1)
	case OutcomeSoftFail:
		c.softFail.Add(1)
	case OutcomeNoChannel:
		c.noChannel.Add(1)
	case OutcomeTimeout:
		c.timeout.Add(1)
	case OutcomeUpstream:
		c.upstream.Add(1)
	case OutcomeRateLimit:
		c.rateLimit.Add(1)
	case OutcomeClientError:
		c.clientError.Add(1)
	case OutcomeInternal:
		c.internalErr.Add(1)
	case OutcomeQuota:
		c.quota.Add(1)
	case OutcomeViolation:
		c.violation.Add(1)
	case OutcomeClientGone:
		c.clientGone.Add(1)
	case OutcomeChannelTest:
		c.channelTest.Add(1)
	}

	// 性能量只取成功样本:一次 600 秒超时就能把均值拉飞,
	// 而运营看均值是为了判断"正常时候多快",不是"最坏能多慢"。
	if s.Outcome != OutcomeSuccess {
		return
	}
	if s.LatencyMs > 0 {
		c.latencySum.Add(s.LatencyMs)
		c.latencyCount.Add(1)
	}
	if s.HasTtft && s.TtftMs > 0 {
		c.ttftSum.Add(s.TtftMs)
		c.ttftCount.Add(1)
	}
	// 三个量必须同进同出:少加 speedCount 会让样本下限凭空放宽,
	// 少加另两个则会让下限卡住一个其实算得出来的速度。
	if s.OutputTokens > 0 && s.GenerationMs > 0 {
		c.outputTokens.Add(s.OutputTokens)
		c.generationMs.Add(s.GenerationMs)
		c.speedCount.Add(1)
	}
}

// loadOrCreate 取或建热桶,并在基数触顶时拒绝新键。
func loadOrCreate(k dimKey) (*counters, bool) {
	if c, ok := hotBuckets.Load(k); ok {
		return c.(*counters), true
	}
	if hotSeries.Load() >= hotSeriesLimit {
		n := statSeriesFull.Add(1)
		if n == 1 || n%10000 == 0 {
			common.SysError(fmt.Sprintf(
				"qianye/availability: 内存序列数已达上限 %d,累计丢弃 %d 个新序列 —— "+
					"通常意味着分组名或模型名被污染", hotSeriesLimit, n))
		}
		return nil, false
	}
	actual, loaded := hotBuckets.LoadOrStore(k, &counters{})
	if !loaded {
		hotSeries.Add(1)
	}
	return actual.(*counters), true
}

// drain 原子地取走全部计数并清零,返回值即将写入 DB 的一行。
//
// 逐字段 Swap 存在一个极短窗口:并发采样的某个样本可能一半字段落在旧批、
// 一半落在新批。两批最终都会累加进同一 DB 行,总量不丢;查询侧用
// availabilityOf 的夹紧兜住瞬时的 success > counted。
func (c *counters) drain() Bucket {
	return Bucket{
		ReqTotal:        c.reqTotal.Swap(0),
		SuccessCount:    c.success.Swap(0),
		FailSoftStream:  c.softFail.Swap(0),
		FailNoChannel:   c.noChannel.Swap(0),
		FailTimeout:     c.timeout.Swap(0),
		FailUpstream:    c.upstream.Swap(0),
		FailRateLimit:   c.rateLimit.Swap(0),
		FailClientError: c.clientError.Swap(0),
		FailInternal:    c.internalErr.Swap(0),
		ExcQuota:        c.quota.Swap(0),
		ExcViolation:    c.violation.Swap(0),
		ExcClientGone:   c.clientGone.Swap(0),
		ExcChannelTest:  c.channelTest.Swap(0),
		LatencySumMs:    c.latencySum.Swap(0),
		LatencyCount:    c.latencyCount.Swap(0),
		TtftSumMs:       c.ttftSum.Swap(0),
		TtftCount:       c.ttftCount.Swap(0),
		OutputTokens:    c.outputTokens.Swap(0),
		GenerationMs:    c.generationMs.Swap(0),
		SpeedCount:      c.speedCount.Swap(0),
	}
}

// snapshot 只读取不清零,供查询侧叠加本节点尚未 flush 的数据。
func (c *counters) snapshot() Bucket {
	return Bucket{
		ReqTotal:        c.reqTotal.Load(),
		SuccessCount:    c.success.Load(),
		FailSoftStream:  c.softFail.Load(),
		FailNoChannel:   c.noChannel.Load(),
		FailTimeout:     c.timeout.Load(),
		FailUpstream:    c.upstream.Load(),
		FailRateLimit:   c.rateLimit.Load(),
		FailClientError: c.clientError.Load(),
		FailInternal:    c.internalErr.Load(),
		ExcQuota:        c.quota.Load(),
		ExcViolation:    c.violation.Load(),
		ExcClientGone:   c.clientGone.Load(),
		ExcChannelTest:  c.channelTest.Load(),
		LatencySumMs:    c.latencySum.Load(),
		LatencyCount:    c.latencyCount.Load(),
		TtftSumMs:       c.ttftSum.Load(),
		TtftCount:       c.ttftCount.Load(),
		OutputTokens:    c.outputTokens.Load(),
		GenerationMs:    c.generationMs.Load(),
		SpeedCount:      c.speedCount.Load(),
	}
}

// restore 把一批 flush 失败的数据加回内存,等下一轮重试。
func (c *counters) restore(b *Bucket) {
	c.reqTotal.Add(b.ReqTotal)
	c.success.Add(b.SuccessCount)
	c.softFail.Add(b.FailSoftStream)
	c.noChannel.Add(b.FailNoChannel)
	c.timeout.Add(b.FailTimeout)
	c.upstream.Add(b.FailUpstream)
	c.rateLimit.Add(b.FailRateLimit)
	c.clientError.Add(b.FailClientError)
	c.internalErr.Add(b.FailInternal)
	c.quota.Add(b.ExcQuota)
	c.violation.Add(b.ExcViolation)
	c.clientGone.Add(b.ExcClientGone)
	c.channelTest.Add(b.ExcChannelTest)
	c.latencySum.Add(b.LatencySumMs)
	c.latencyCount.Add(b.LatencyCount)
	c.ttftSum.Add(b.TtftSumMs)
	c.ttftCount.Add(b.TtftCount)
	c.outputTokens.Add(b.OutputTokens)
	c.generationMs.Add(b.GenerationMs)
	c.speedCount.Add(b.SpeedCount)
}

// bucketSeconds 返回生效的桶粒度。
func bucketSeconds() int64 {
	return clampBucketSeconds(int64(config.Get().Availability.BucketSeconds))
}

// clampBucketSeconds 把配置值夹进可落库的区间。
//
// 小于 30 秒会让行数暴涨且没有统计意义(单桶样本太少,可用率全是噪声);
// 大于 1 小时则与小时级 rollup 表语义重叠,两套数据会互相矛盾。
func clampBucketSeconds(n int64) int64 {
	if n <= 0 {
		return defaultBucketSeconds
	}
	if n < minBucketSeconds {
		return minBucketSeconds
	}
	if n > maxBucketSeconds {
		return maxBucketSeconds
	}
	return n
}

// alignBucket 把 unix 秒对齐到桶起点。
//
// 用向下取整而非四舍五入:桶代表的是 [ts, ts+size) 这个区间,
// 取整方向一旦不一致,同一次请求在采样端和查询端会落进不同的桶。
func alignBucket(ts, size int64) int64 {
	if size <= 0 {
		return ts
	}
	rem := ts % size
	// Go 的取模对负数保留符号,1970 年前的时间戳会被对齐到未来的桶。
	if rem < 0 {
		rem += size
	}
	return ts - rem
}

func alignHour(ts int64) int64 { return alignBucket(ts, 3600) }

// truncateRunes 按字符数截断,保证不超出 varchar 的定义长度。
// 按字节截断会把多字节字符切成半个,MySQL 侧直接报编码错误。
func truncateRunes(s string, limit int) string {
	r := []rune(s)
	if len(r) <= limit {
		return s
	}
	statTruncated.Add(1)
	return string(r[:limit])
}
