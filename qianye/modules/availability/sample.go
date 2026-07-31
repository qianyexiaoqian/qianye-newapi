package availability

import (
	"context"
	"time"

	"github.com/QuantumNous/new-api/common"
	perfmetrics "github.com/QuantumNous/new-api/pkg/perf_metrics"
	"github.com/QuantumNous/new-api/qianye/config"
	"github.com/QuantumNous/new-api/qianye/guard"
	relaycommon "github.com/QuantumNous/new-api/relay/common"
)

// sample 是一次采样的不可变快照。
//
// 关键约束:它只含值类型。relay 线程把 RelayInfo 里需要的东西全部取干净后
// 才交给后台 worker —— StreamStatus 在 relay 返回之后仍可能被其它协程写入,
// 把 *RelayInfo 带出本次调用就是数据竞争。
type sample struct {
	BucketTs     int64
	Group        string
	Model        string
	Outcome      Outcome
	LatencyMs    int64
	TtftMs       int64
	HasTtft      bool
	OutputTokens int64
	GenerationMs int64
}

// installHooks 注入上游 pkg/perf_metrics 的同包 hook 变量。
// 调用时机在 qianye.Init(),早于 HTTP 监听与后台协程,无并发读写窗口。
func installHooks() {
	perfmetrics.QyOnRelaySample = onRelaySample
}

// onRelaySample 跑在 relay 线程上,是整个模块唯一的热路径。
//
// 分工:分类与维度提取必须同步做完(纯内存,O(1),且必须在 info 还活着时读);
// 累加交给 guard.HotAsync 的 worker —— 它自带有界队列、panic 拦截、超时与
// 高水位背压,relay 线程不会因为扩展而多等一次调度。
//
// 开关在调用时读而不是安装期快照:config 支持热更新,快照会让改完 YAML 必须重启。
func onRelaySample(info *relaycommon.RelayInfo, success bool, outputTokens int64) {
	if !config.Get().Availability.Enabled {
		return
	}
	s, ok := buildSample(info, success, outputTokens)
	if !ok {
		return
	}
	guard.HotAsync("availability.sample", func(ctx context.Context) error {
		observe(s)
		return nil
	})
}

// buildSample 把 RelayInfo 压成一个值快照。返回 false 表示这条样本应当丢弃。
func buildSample(info *relaycommon.RelayInfo, success bool, outputTokens int64) (sample, bool) {
	if info == nil {
		return sample{}, false
	}
	model := truncateRunes(info.OriginModelName, maxModelNameRunes)
	if model == "" {
		// 没有模型名的样本无法归档到任何单元格,计数后丢弃 ——
		// 静默丢弃会让"分母对不上"变成无从排查的悬案。
		statNoModel.Add(1)
		return sample{}, false
	}

	// UsingGroup 是 auto 解析之后的真实分组(relay/helper/price.go 会改写它),
	// 与消费日志、上游 perf_metrics 同源,三方数字可对账。
	group := truncateRunes(info.UsingGroup, maxGroupNameRunes)
	if group == "" {
		group = unknownGroup
	}

	in := classifyInput{Success: success, IsChannelTest: info.IsChannelTest, Err: info.LastError}
	if info.StreamStatus != nil {
		in.EndReason = info.StreamStatus.EndReason
		in.StreamErrors = info.StreamStatus.TotalErrorCount()
	}

	now := time.Now()
	latencyMs, ttftMs, hasTtft, generationMs := timings(info, now)

	return sample{
		BucketTs:     alignBucket(now.Unix(), bucketSeconds()),
		Group:        group,
		Model:        model,
		Outcome:      classify(in),
		LatencyMs:    latencyMs,
		TtftMs:       ttftMs,
		HasTtft:      hasTtft,
		OutputTokens: outputTokens,
		GenerationMs: generationMs,
	}, true
}

// timings 复刻上游 RecordRelaySample 的耗时口径,使两套数字逐列可比。
//
// StartTime 为零值时(理论上不该发生,但 RelayInfo 有多条构造路径)全部归零,
// 否则会得到一个从 1970 年算起的天文数字延迟,把均值彻底污染。
func timings(info *relaycommon.RelayInfo, now time.Time) (latencyMs, ttftMs int64, hasTtft bool, generationMs int64) {
	if info.StartTime.IsZero() {
		return 0, 0, false, 0
	}
	latencyMs = now.Sub(info.StartTime).Milliseconds()
	if latencyMs < 0 {
		latencyMs = 0
	}
	hasTtft = info.IsStream && info.HasSendResponse()
	generationMs = latencyMs
	if hasTtft {
		ttftMs = info.FirstResponseTime.Sub(info.StartTime).Milliseconds()
		if ttftMs < 0 {
			ttftMs = 0
			hasTtft = false
		}
		generationMs = now.Sub(info.FirstResponseTime).Milliseconds()
	}
	if generationMs <= 0 {
		generationMs = latencyMs
	}
	return latencyMs, ttftMs, hasTtft, generationMs
}

// warnAttemptLevelUnsupported 在配置打开了尚未实现的 attempt 级采样时提醒一次。
//
// 静默忽略一个配置开关是最危险的失败模式:运维以为渠道级归因已经开启,
// 实际看到的仍是端到端数字。
func warnAttemptLevelUnsupported() {
	if config.Get().Availability.SampleAttemptLevel {
		common.SysError("qianye/availability: sample_attempt_level 暂未实现(需要在 controller/relay.go 的 " +
			"processChannelError 增加挂载点),当前仍按端到端口径采样")
	}
}
