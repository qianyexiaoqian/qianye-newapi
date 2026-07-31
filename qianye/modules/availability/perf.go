package availability

import "math"

// ─────────────────────────── 性能维度(延迟 / 首字 / 速度) ───────────────────────────
//
// 可用率回答「能不能用」,这三个指标回答「用起来什么体验」。用户要的
// 「按分组看延迟、速度」就落在这里:同一个模型在 default 与 svip 背后往往
// 是不同渠道,全站汇总的延迟对选分组这件事毫无指导意义。
//
// 三个指标都由预聚合列直接算出,不再回表:
//
//	平均延迟 = latency_sum_ms / latency_count   (端到端,只累加成功样本)
//	首字延迟 = ttft_sum_ms   / ttft_count       (仅流式且已下发首帧的样本)
//	生成速度 = output_tokens / (generation_ms / 1000)
//
// 速度刻意用「总 token ÷ 总生成毫秒」而不是「每次请求速度的算术平均」:
// 后者会让一次 3 token 的短回复与一次 3000 token 的长回复等权,而用户关心的
// 是这个时段实际吐字有多快。token 加权正好是这个语义。

// perfMinSamples 是性能均值的样本下限。
//
// 刻意与可用率的 MinSamples(20)分开,不是偷懒:可用率是比例,20 条以下的
// 比值噪声大到能把 0/1 显示成 0%;而延迟与速度是数值均值,几条样本就足以反映
// 量级。沿用 20 的结果是中小分组的延迟列整片空白 —— 那不叫严谨,那叫把
// 用户真正想看的信息删掉。
//
// 它随 Definition 一并下发给前端,页面据此解释「为什么这里是横杠」。
const perfMinSamples = 5

// perf 是一个格子的性能指标。
//
// 三个均值都是指针:nil 表示「样本不足以下结论」,与 availability 的规则完全
// 一致 —— 渲染成 0 会让人以为「延迟 0 毫秒 / 一个字都吐不出来」,而真相是
// 这里根本没有数据。三个样本数一并下发,让「为什么没有数字」当场可解释。
//
// 内嵌进 cell 与 seriesPoint,JSON 会被摊平成同级字段;两处共用同一份口径,
// 不会出现矩阵与趋势图对同一个格子给出两个延迟的情况。
type perf struct {
	AvgLatencyMs   *int64   `json:"avg_latency_ms"`
	AvgTtftMs      *int64   `json:"avg_ttft_ms"`
	AvgTps         *float64 `json:"avg_tps"`
	LatencySamples int64    `json:"latency_samples"`
	TtftSamples    int64    `json:"ttft_samples"`
	SpeedSamples   int64    `json:"speed_samples"`
}

// perfOf 由预聚合计数推导性能指标。纯函数,无 IO。
func perfOf(b *Bucket) perf {
	if b == nil {
		return perf{}
	}
	return perf{
		AvgLatencyMs:   avgMs(b.LatencySumMs, b.LatencyCount),
		AvgTtftMs:      avgMs(b.TtftSumMs, b.TtftCount),
		AvgTps:         tpsOf(b),
		LatencySamples: b.LatencyCount,
		TtftSamples:    b.TtftCount,
		SpeedSamples:   b.SpeedCount,
	}
}

// avgMs 求毫秒均值。样本不足、计数为 0(除零)、总和非正一律返回 nil。
func avgMs(sum, count int64) *int64 {
	if count < perfMinSamples || count <= 0 || sum <= 0 {
		return nil
	}
	v := sum / count
	return &v
}

// tpsOf 求生成速度(tokens/秒),保留两位小数。
//
// generation_ms 为 0 的行必须挡在除法之前:那是「有 token 却没有耗时」的
// 坏数据,除下去会得到 +Inf,JSON 序列化直接报错,整个看板 500。
func tpsOf(b *Bucket) *float64 {
	if b.SpeedCount < perfMinSamples || b.OutputTokens <= 0 || b.GenerationMs <= 0 {
		return nil
	}
	v := math.Round(float64(b.OutputTokens)/(float64(b.GenerationMs)/1000)*100) / 100
	return &v
}
