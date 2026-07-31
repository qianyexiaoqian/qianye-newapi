package availability

import (
	"testing"

	"github.com/QuantumNous/new-api/qianye/config"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// 性能指标与可用率共享同一条铁律:没有足够样本就必须是 null,绝不能是 0。
// 渲染成「延迟 0ms / 速度 0 t/s」会被读成「快得离谱 / 一个字都吐不出来」,
// 两种误读都会让人对着一个根本不存在的数字做决策。
func TestPerfOfRequiresSamplesAndNeverReturnsZero(t *testing.T) {
	cases := []struct {
		name        string
		bucket      Bucket
		wantLatency *int64
		wantTtft    *int64
		wantTps     *float64
	}{
		{
			name:   "空桶三项全空",
			bucket: Bucket{},
		},
		{
			name:   "延迟样本不足下限",
			bucket: Bucket{LatencySumMs: 4000, LatencyCount: perfMinSamples - 1},
		},
		{
			name:        "延迟样本达到下限",
			bucket:      Bucket{LatencySumMs: 1000, LatencyCount: perfMinSamples},
			wantLatency: int64Ptr(200),
		},
		{
			// 非流式请求不产生首字样本:延迟有值不代表首字有值,两个计数必须独立判定。
			name:        "首字样本独立于延迟",
			bucket:      Bucket{LatencySumMs: 2000, LatencyCount: 10, TtftSumMs: 500, TtftCount: 0},
			wantLatency: int64Ptr(200),
		},
		{
			name: "首字样本达到下限",
			bucket: Bucket{
				LatencySumMs: 2000, LatencyCount: 10,
				TtftSumMs: 600, TtftCount: 6,
			},
			wantLatency: int64Ptr(200),
			wantTtft:    int64Ptr(100),
		},
		{
			name:    "速度样本不足下限",
			bucket:  Bucket{OutputTokens: 10000, GenerationMs: 1000, SpeedCount: perfMinSamples - 1},
			wantTps: nil,
		},
		{
			name:    "速度按 token 加权",
			bucket:  Bucket{OutputTokens: 1000, GenerationMs: 2000, SpeedCount: perfMinSamples},
			wantTps: float64Ptr(500),
		},
		{
			name:    "速度保留两位小数",
			bucket:  Bucket{OutputTokens: 7, GenerationMs: 3000, SpeedCount: 5},
			wantTps: float64Ptr(2.33),
		},
		{
			// 升级前落库的历史行没有 speed_count,只有 token 与耗时总量。
			// 宁可留白也不能拿一个不知道来自几次请求的数字当结论。
			name:   "历史行缺少速度样本数",
			bucket: Bucket{OutputTokens: 5000, GenerationMs: 1000},
		},
		{
			// generation_ms 为 0 却有 token 是坏数据:除下去得到 +Inf,
			// JSON 序列化直接报错,整个看板 500。
			name:   "生成耗时为零不得除零",
			bucket: Bucket{OutputTokens: 500, GenerationMs: 0, SpeedCount: 100},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := perfOf(&tc.bucket)
			assert.Equal(t, tc.wantLatency, got.AvgLatencyMs)
			assert.Equal(t, tc.wantTtft, got.AvgTtftMs)
			assert.Equal(t, tc.wantTps, got.AvgTps)

			// 样本数必须原样下发:页面靠它解释「为什么这里是横杠」。
			assert.Equal(t, tc.bucket.LatencyCount, got.LatencySamples)
			assert.Equal(t, tc.bucket.TtftCount, got.TtftSamples)
			assert.Equal(t, tc.bucket.SpeedCount, got.SpeedSamples)
		})
	}

	assert.Equal(t, perf{}, perfOf(nil), "nil 桶不得 panic")
}

// 矩阵格子与趋势点必须走同一个 perfOf:两处各算一遍的话,点开抽屉看到的
// 延迟会和矩阵里的对不上,而那种不一致排查起来最费时。
func TestBuildCellCarriesPerfMetrics(t *testing.T) {
	d := definitionOf(config.Availability{})
	b := &Bucket{
		ReqTotal: 100, SuccessCount: 100,
		LatencySumMs: 3000, LatencyCount: 10,
		TtftSumMs: 800, TtftCount: 8,
		OutputTokens: 2000, GenerationMs: 4000, SpeedCount: 10,
	}
	got := buildCell(cellKey{group: "vip", model: "gpt-5"}, b, true, d)

	require.NotNil(t, got.AvgLatencyMs)
	assert.Equal(t, int64(300), *got.AvgLatencyMs)
	require.NotNil(t, got.AvgTtftMs)
	assert.Equal(t, int64(100), *got.AvgTtftMs)
	require.NotNil(t, got.AvgTps)
	assert.Equal(t, 500.0, *got.AvgTps)
	assert.Equal(t, perfOf(b), got.perf)

	// 没有任何样本的格子:可用率与三个性能指标必须一起是 null。
	empty := buildCell(cellKey{group: "vip", model: "gpt-5"}, nil, true, d)
	assert.Nil(t, empty.Availability)
	assert.Nil(t, empty.AvgLatencyMs)
	assert.Nil(t, empty.AvgTtftMs)
	assert.Nil(t, empty.AvgTps)
}

// 口径要能被页面解释:样本下限只写在常量里而不下发,用户看到横杠只会以为页面坏了。
func TestDefinitionExposesPerfMinSamples(t *testing.T) {
	d := definitionOf(config.Availability{})
	assert.Equal(t, perfMinSamples, d.PerfMinSamples)
	assert.NotEqual(t, 0, d.PerfMinSamples)
}

// 只有成功且同时拿到 token 与生成耗时的样本才算一次速度样本。
// 少加一边就会让样本下限失去意义:一次请求也能凑出「足够样本」。
func TestRecordCountsSpeedSamples(t *testing.T) {
	c := &counters{}
	c.record(sample{Outcome: OutcomeSuccess, LatencyMs: 100, OutputTokens: 10, GenerationMs: 500})
	c.record(sample{Outcome: OutcomeSuccess, LatencyMs: 100, OutputTokens: 0, GenerationMs: 500})
	c.record(sample{Outcome: OutcomeSuccess, LatencyMs: 100, OutputTokens: 10, GenerationMs: 0})
	c.record(sample{Outcome: OutcomeUpstream, LatencyMs: 100, OutputTokens: 10, GenerationMs: 500})

	b := c.drain()
	assert.Equal(t, int64(1), b.SpeedCount)
	assert.Equal(t, int64(10), b.OutputTokens)
	assert.Equal(t, int64(500), b.GenerationMs)
	assert.Equal(t, int64(3), b.LatencyCount, "性能量只取成功样本")
}

// 切到「延迟」主指标却仍按可用率排序,第一页看到的是一堆延迟正常的格子,
// 等于没切。无数据永远垫底:它既不是最慢也不是最快。
func TestSortCellsByPerfMetrics(t *testing.T) {
	d := definitionOf(config.Availability{})
	slow := buildCell(cellKey{group: "g", model: "slow"},
		&Bucket{ReqTotal: 50, SuccessCount: 50, LatencySumMs: 5000, LatencyCount: 10,
			TtftSumMs: 9000, TtftCount: 10,
			OutputTokens: 100, GenerationMs: 10000, SpeedCount: 10}, true, d)
	fast := buildCell(cellKey{group: "g", model: "fast"},
		&Bucket{ReqTotal: 50, SuccessCount: 50, LatencySumMs: 1000, LatencyCount: 10,
			TtftSumMs: 1000, TtftCount: 10,
			OutputTokens: 1000, GenerationMs: 10000, SpeedCount: 10}, true, d)
	idle := buildCell(cellKey{group: "g", model: "idle"}, nil, true, d)

	for _, mode := range []string{"latency_desc", "ttft_desc", "tps_asc"} {
		cells := []cell{fast, idle, slow}
		sortCells(cells, mode)
		assert.Equalf(t, []string{"slow", "fast", "idle"},
			[]string{cells[0].Model, cells[1].Model, cells[2].Model}, "排序模式 %s", mode)
	}
}

// 分页按模型切,排序必须发生在服务端:否则切到「最慢优先」时,
// 真正最慢的模型可能压根不在前端拿到的这一页里。
func TestSortModelsByPerfMetrics(t *testing.T) {
	d := definitionOf(config.Availability{})
	groups := []string{"default", "vip"}
	cells := map[cellKey]*Bucket{
		// fast 在 vip 上很慢:按最差分组取值,它必须排到 mid 前面,
		// 取平均则会被 default 的好成绩稀释掉。
		{group: "default", model: "fast"}: {ReqTotal: 50, SuccessCount: 50,
			LatencySumMs: 500, LatencyCount: 10, TtftSumMs: 500, TtftCount: 10,
			OutputTokens: 5000, GenerationMs: 5000, SpeedCount: 10},
		{group: "vip", model: "fast"}: {ReqTotal: 50, SuccessCount: 50,
			LatencySumMs: 90000, LatencyCount: 10, TtftSumMs: 90000, TtftCount: 10,
			OutputTokens: 100, GenerationMs: 50000, SpeedCount: 10},
		{group: "default", model: "mid"}: {ReqTotal: 50, SuccessCount: 50,
			LatencySumMs: 20000, LatencyCount: 10, TtftSumMs: 20000, TtftCount: 10,
			OutputTokens: 2000, GenerationMs: 5000, SpeedCount: 10},
		{group: "default", model: "idle"}: {ReqTotal: 3, SuccessCount: 3},
	}

	for _, mode := range []string{"latency_desc", "ttft_desc", "tps_asc"} {
		names := []string{"fast", "idle", "mid"}
		sortModels(names, cells, groups, d, mode)
		assert.Equalf(t, []string{"fast", "mid", "idle"}, names, "排序模式 %s", mode)
	}
}

func int64Ptr(v int64) *int64       { return &v }
func float64Ptr(v float64) *float64 { return &v }
