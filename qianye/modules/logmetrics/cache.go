package logmetrics

// CacheBasis 是缓存百分比的分子与权威分母。
//
// 后端只固化分子分母、不算百分比:前端可以按需展示「12.3%」或「1,024 / 8,320」,
// 未来做聚合(平均命中率)也能正确加权求和。存结果会丢信息且无法二次利用。
type CacheBasis struct {
	// InputTotal 是权威分母 —— 本次请求的输入 token 总量,已按计费语义修正。
	InputTotal int
	// CacheRead 是分子(缓存命中读取)。与上游的 cache_tokens 同值,
	// 独立存放是为了让 qy_* 自成闭包:上游若改 cache_tokens 语义,
	// 不会静默污染本模块的计算结果。
	CacheRead int
	// CacheWrite 是缓存写入合计,参与 Anthropic 语义下的分母,也用于详情弹窗。
	CacheWrite int
	// Anomaly 标记上游 usage 自相矛盾(负数、分子大于分母、超出 int32)。
	// 前端据此加警示图标 —— 直接钳到 100% 而不留痕会掩盖上游 bug。
	Anomaly bool
}

// computeCacheBasis 按计费语义还原输入总量。
//
// 语义差异是本模块最核心的正确性来源:
//
//   - OpenAI / Gemini 语义:prompt_tokens 已经包含 cached_tokens 与 cache write。
//     证据见 service/text_quota.go —— 只有在非 Claude 语义下才会
//     baseTokens.Sub(dCacheTokens) 和 Sub(dCachedCreationTokens),
//     说明基数里本来就含这两部分。
//
//   - Anthropic 语义:prompt_tokens 等于 Claude 的 input_tokens,
//     不含 cache read 也不含 cache creation,必须加回去才是真实输入总量。
//     OpenRouter + Claude 计费路径显式做了 PromptTokens -= CacheTokens,
//     同属「不含缓存」,且 IsClaudeUsageSemantic 为 true,判别一致。
//
// 用错公式的后果不是报错而是静默出错:对一条 Claude 日志套 OpenAI 公式,
// 当 input_tokens 恰好大于 cache_read 时(长 system prompt + 小缓存),
// 结果落在 0-100% 区间,看起来完全合理,永远不会被发现。
func computeCacheBasis(promptTokens, cacheReadTokens, cacheWriteTokens int, isClaudeUsageSemantic bool) CacheBasis {
	var basis CacheBasis

	// usage 由上游服务提供,负数只可能来自畸形响应或减法下溢
	// (OpenRouter+Claude 路径会对 PromptTokens 连续做减法)。
	// 归零并标记异常,绝不让负数进入分母。
	prompt, negPrompt := nonNegative(promptTokens)
	read, negRead := nonNegative(cacheReadTokens)
	write, negWrite := nonNegative(cacheWriteTokens)
	basis.Anomaly = negPrompt || negRead || negWrite

	// 全程 int64 累加:三项各自可达 int32 上限,int 在 32 位平台上会溢出成负数。
	total := prompt
	if isClaudeUsageSemantic {
		total = prompt + read + write
	}

	// 分子大于分母只可能是上游数据错误。抬高分母保证百分比不超过 100%,
	// 同时打上 anomaly —— 静默钳制会把上游 bug 藏起来。
	if read > total {
		total = read
		basis.Anomaly = true
	}

	total, satTotal := saturate(total)
	readOut, satRead := saturate(read)
	writeOut, satWrite := saturate(write)
	if satTotal || satRead || satWrite {
		basis.Anomaly = true
	}

	basis.InputTotal = int(total)
	basis.CacheRead = int(readOut)
	basis.CacheWrite = int(writeOut)
	return basis
}

func nonNegative(v int) (int64, bool) {
	if v < 0 {
		return 0, true
	}
	return int64(v), false
}

func saturate(v int64) (int64, bool) {
	if v > maxTokenCount {
		return maxTokenCount, true
	}
	return v, false
}
