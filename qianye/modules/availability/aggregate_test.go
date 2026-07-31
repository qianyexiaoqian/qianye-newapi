package availability

import (
	"runtime"
	"strings"
	"sync"
	"testing"

	"github.com/QuantumNous/new-api/qianye/config"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// 采样端与查询端必须用同一个对齐规则,否则同一次请求会落进不同的桶,
// 表现为曲线上凭空多出半个桶的毛刺。
func TestAlignBucket(t *testing.T) {
	cases := []struct {
		ts, size, want int64
	}{
		{1753800000, 300, 1753800000}, // 恰好在边界上
		{1753800001, 300, 1753800000},
		{1753800299, 300, 1753800000},
		{1753800300, 300, 1753800300},
		{1753800000, 3600, 1753797600},
		{0, 300, 0},
		{100, 0, 100},   // size 非法时原样返回,不制造 panic
		{-1, 300, -300}, // Go 的取模保留负号,必须补偿,否则会对齐到未来的桶
	}
	for _, tc := range cases {
		assert.Equal(t, tc.want, alignBucket(tc.ts, tc.size),
			"alignBucket(%d, %d)", tc.ts, tc.size)
	}
}

func TestAlignHour(t *testing.T) {
	assert.Equal(t, int64(1753797600), alignHour(1753800000))
	assert.Equal(t, int64(1753797600), alignHour(1753797600))
	assert.Equal(t, int64(1753797600), alignHour(1753801199))
}

// 桶粒度必须被夹在可落库的范围内:配置里写 1 秒会让行数暴涨,
// 写 1 天则与小时汇总表语义重叠。
func TestClampBucketSeconds(t *testing.T) {
	cases := map[int64]int64{
		0:     defaultBucketSeconds,
		-1:    defaultBucketSeconds,
		1:     minBucketSeconds,
		300:   300,
		3600:  3600,
		86400: maxBucketSeconds,
	}
	for in, want := range cases {
		assert.Equalf(t, want, clampBucketSeconds(in), "clampBucketSeconds(%d)", in)
	}
	// 未加载配置时 config.Get() 返回零值,应退回默认粒度。
	assert.Equal(t, int64(defaultBucketSeconds), bucketSeconds())
}

// 模型名进的是 varchar(128),按字节截断会把多字节字符切成半个,MySQL 直接报编码错误。
func TestTruncateRunesKeepsCharactersIntact(t *testing.T) {
	assert.Equal(t, "gpt-5", truncateRunes("gpt-5", maxModelNameRunes))

	long := strings.Repeat("模", 200)
	got := truncateRunes(long, maxModelNameRunes)
	assert.Equal(t, maxModelNameRunes, len([]rune(got)))
	assert.True(t, strings.HasPrefix(long, got))
}

// record → drain → 口径求和的完整链路:分类结果落到了正确的列上。
// 这是「口径开关生效」这个不变量真正被验证的地方。
func TestRecordDrainCounted(t *testing.T) {
	c := &counters{}
	outcomes := []Outcome{
		OutcomeSuccess, OutcomeSuccess, OutcomeSuccess,
		OutcomeUpstream,
		OutcomeTimeout,
		OutcomeRateLimit, OutcomeRateLimit,
		OutcomeClientError,
		OutcomeQuota,
		OutcomeViolation,
		OutcomeChannelTest,
		OutcomeClientGone,
	}
	for _, o := range outcomes {
		c.record(sample{Outcome: o, LatencyMs: 100, OutputTokens: 10, GenerationMs: 1000})
	}

	b := c.drain()
	assert.Equal(t, int64(len(outcomes)), b.ReqTotal)
	assert.Equal(t, int64(3), b.SuccessCount)
	assert.Equal(t, int64(1), b.FailUpstream)
	assert.Equal(t, int64(1), b.FailTimeout)
	assert.Equal(t, int64(2), b.FailRateLimit)
	assert.Equal(t, int64(1), b.FailClientError)
	assert.Equal(t, int64(4), b.excludedTotal())

	// 性能量只统计成功样本,否则一次超时就能把均值拉飞。
	assert.Equal(t, int64(3), b.LatencyCount)
	assert.Equal(t, int64(300), b.LatencySumMs)

	def := definitionOf(config.Availability{})
	assert.Equal(t, int64(5), counted(&b, def), "默认口径:成功 3 + 上游 1 + 超时 1")
	av := availabilityOf(b.SuccessCount, counted(&b, def))
	require.NotNil(t, av)
	assert.Equal(t, 60.0, *av)

	withAll := definitionOf(config.Availability{CountRateLimited: true, CountClientErrors: true})
	assert.Equal(t, int64(8), counted(&b, withAll))

	// drain 之后必须清零,否则下一轮 flush 会把同一批数据再写一次。
	empty := c.drain()
	assert.Equal(t, int64(0), empty.ReqTotal)
	assert.Equal(t, int64(0), empty.SuccessCount)
}

// flush 失败要把数据原样退回内存等下一轮,一列都不能丢。
func TestRestoreRoundTrip(t *testing.T) {
	c := &counters{}
	for _, o := range []Outcome{OutcomeSuccess, OutcomeUpstream, OutcomeQuota} {
		c.record(sample{Outcome: o, LatencyMs: 50, HasTtft: true, TtftMs: 20,
			OutputTokens: 7, GenerationMs: 700})
	}
	drained := c.drain()
	c.restore(&drained)
	again := c.drain()
	assert.Equal(t, drained, again)
}

// 采样与 flush 并发时总量不能丢:drain 走 Swap,累加走 Add,两者必须互补。
func TestConcurrentRecordAndDrainPreservesTotal(t *testing.T) {
	c := &counters{}
	const writers, perWriter = 8, 500

	var writersWg, drainWg sync.WaitGroup
	var drainedTotal, drainedSuccess int64
	stop := make(chan struct{})

	drainWg.Add(1)
	go func() {
		defer drainWg.Done()
		for {
			select {
			case <-stop:
				return
			default:
			}
			b := c.drain()
			drainedTotal += b.ReqTotal
			drainedSuccess += b.SuccessCount
			runtime.Gosched()
		}
	}()

	for i := 0; i < writers; i++ {
		writersWg.Add(1)
		go func() {
			defer writersWg.Done()
			for j := 0; j < perWriter; j++ {
				c.record(sample{Outcome: OutcomeSuccess})
			}
		}()
	}

	// 先等写入者结束,再停 drain 协程,最后收尾一次,确保没有残留。
	writersWg.Wait()
	close(stop)
	drainWg.Wait()

	final := c.drain()
	assert.Equal(t, int64(writers*perWriter), drainedTotal+final.ReqTotal)
	assert.Equal(t, int64(writers*perWriter), drainedSuccess+final.SuccessCount)
}
