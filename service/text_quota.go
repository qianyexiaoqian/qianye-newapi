package service

import (
	"fmt"
	"math"
	"sort"
	"strings"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/constant"
	"github.com/QuantumNous/new-api/logger"
	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/pkg/billingexpr"
	perfmetrics "github.com/QuantumNous/new-api/pkg/perf_metrics"
	relaycommon "github.com/QuantumNous/new-api/relay/common"
	relayconstant "github.com/QuantumNous/new-api/relay/constant"
	"github.com/QuantumNous/new-api/relaykit/dto"
	"github.com/QuantumNous/new-api/relaykit/types"
	"github.com/QuantumNous/new-api/setting/operation_setting"

	"github.com/bytedance/gopkg/util/gopool"
	"github.com/gin-gonic/gin"
	"github.com/shopspring/decimal"
)

// ToolSurchargeItem is one billable tool-call line for consume logs.
type ToolSurchargeItem struct {
	Name  string  `json:"name"`
	Count int     `json:"count"`
	Price float64 `json:"price"`
}

func appendToolSurchargeLogInfo(other map[string]interface{}, items []ToolSurchargeItem) {
	if len(items) == 0 {
		return
	}
	other["tool_surcharges"] = items
}

type textQuotaSummary struct {
	PromptTokens           int
	CompletionTokens       int
	TotalTokens            int
	CacheTokens            int
	CacheCreationTokens    int
	CacheCreationTokens5m  int
	CacheCreationTokens1h  int
	ImageTokens            int
	AudioTokens            int
	ModelName              string
	TokenName              string
	UseTimeSeconds         int64
	CompletionRatio        float64
	CacheRatio             float64
	ImageRatio             float64
	ModelRatio             float64
	GroupRatio             float64
	ModelPrice             float64
	CacheCreationRatio     float64
	CacheCreationRatio5m   float64
	CacheCreationRatio1h   float64
	Quota                  int
	IsClaudeUsageSemantic  bool
	UsageSemantic          string
	AudioInputPrice        float64
	ToolSurchargeItems     []ToolSurchargeItem
	ToolCallSurchargeQuota decimal.Decimal
}

// hasBillableUsage reports whether this request should incur any charge.
// A request can carry zero tokens yet still be billable via a tool-call
// surcharge (e.g. /v1/alpha/search returns no usage but bills one web_search
// call), so token count alone is not sufficient to decide.
func (s *textQuotaSummary) hasBillableUsage() bool {
	return s.TotalTokens > 0 || !s.ToolCallSurchargeQuota.IsZero()
}

// maxUpstreamTokenCount 是单个上游自报 token 分量的上界。
//
// 取 MaxInt32 的理由:额度列本身就是 32 位,common/quota_math.go 的饱和转换最终
// 也停在这里,所以更大的 token 数在金额上没有任何新含义,只会把中间量推进溢出区。
// 现实里没有任何一次请求接近这个量级,所以这道夹取对真实流量是恒等的。
const maxUpstreamTokenCount = math.MaxInt32

// clampUpstreamTokenCount 把上游自报的 token 数夹进 [0, maxUpstreamTokenCount]。
// 负数只可能来自坏掉的、或被中间人改写过的上游,而一个负分量会把同一笔里真实
// 发生的另一个分量一起抵消掉,表现为整笔免单。上界同理:prompt 与 completion
// 各报 MaxInt64 时,两者相加会**回绕成 -2**,hasBillableUsage() 判成「这笔没有
// 可计费用量」,整笔免单 —— 溢出发生在判据上,金额侧的饱和保护一点都用不上。
// 夹住之后仍留一条 SysError,让「某个渠道在报离谱数字」这件事仍然可被发现,
// 而不是变成一行看不出异常的 quota=0。
//
// 形参取 int64 而不是 int 是刻意的:调用方常常要先把两个上游自报的分量加起来
// (completion + 推理 token),那次加法必须在比 int 更宽的类型里做,否则夹取本身
// 会被加法的回绕绕过去。
func clampUpstreamTokenCount(relayInfo *relaycommon.RelayInfo, field string, count int64) int {
	if count >= 0 && count <= maxUpstreamTokenCount {
		return int(count)
	}
	clamped := 0
	if count > 0 {
		clamped = maxUpstreamTokenCount
	}
	// 只取 OriginModelName:ChannelId 挂在嵌入的 *ChannelMeta 上,而它在
	// InitChannelMeta 之前是 nil,读它会在这条纯粹的告警路径上把请求打挂。
	modelName := ""
	if relayInfo != nil {
		modelName = relayInfo.OriginModelName
	}
	common.SysError(fmt.Sprintf(
		"upstream reported an out-of-range %s (%d) for model %s; clamped to %d",
		field, count, modelName, clamped))
	return clamped
}

// capUpstreamSubsetTokens 把「按协议是 prompt_tokens 子集」的那些明细夹回 prompt。
// 超出即上游自报数据自相矛盾,记一条 SysError 让它可被发现。
func capUpstreamSubsetTokens(relayInfo *relaycommon.RelayInfo, field string, count, promptTokens int) int {
	if count <= promptTokens {
		return count
	}
	modelName := ""
	if relayInfo != nil {
		modelName = relayInfo.OriginModelName
	}
	common.SysError(fmt.Sprintf(
		"upstream reported %s (%d) larger than prompt_tokens (%d) for model %s; capped at prompt_tokens",
		field, count, promptTokens, modelName))
	return promptTokens
}

func cacheWriteTokensTotal(summary textQuotaSummary) int {
	if summary.CacheCreationTokens5m > 0 || summary.CacheCreationTokens1h > 0 {
		splitCacheWriteTokens := summary.CacheCreationTokens5m + summary.CacheCreationTokens1h
		if summary.CacheCreationTokens > splitCacheWriteTokens {
			return summary.CacheCreationTokens
		}
		return splitCacheWriteTokens
	}
	return summary.CacheCreationTokens
}

func isLegacyClaudeDerivedOpenAIUsage(relayInfo *relaycommon.RelayInfo, usage *dto.Usage) bool {
	if relayInfo == nil || usage == nil {
		return false
	}
	if relayInfo.GetFinalRequestRelayFormat() == types.RelayFormatClaude {
		return false
	}
	if usage.UsageSource != "" || usage.UsageSemantic != "" {
		return false
	}
	return usage.ClaudeCacheCreation5mTokens > 0 || usage.ClaudeCacheCreation1hTokens > 0
}

func collectToolSurchargeItem(items []ToolSurchargeItem, name string, count int, modelName string) []ToolSurchargeItem {
	if count <= 0 {
		return items
	}
	price := operation_setting.GetToolPriceForModel(name, modelName)
	if price <= 0 || math.IsNaN(price) || math.IsInf(price, 0) {
		return items
	}
	return append(items, ToolSurchargeItem{
		Name:  name,
		Count: count,
		Price: price,
	})
}

func mergeToolSurchargeItems(items []ToolSurchargeItem) []ToolSurchargeItem {
	if len(items) == 0 {
		return nil
	}
	sort.Slice(items, func(i, j int) bool {
		if items[i].Name == items[j].Name {
			return items[i].Price < items[j].Price
		}
		return items[i].Name < items[j].Name
	})

	merged := items[:0]
	for _, item := range items {
		lastIndex := len(merged) - 1
		if lastIndex >= 0 &&
			merged[lastIndex].Name == item.Name &&
			merged[lastIndex].Price == item.Price {
			if item.Count > math.MaxInt-merged[lastIndex].Count {
				common.SysError("tool surcharge call count overflow for " + item.Name)
				merged[lastIndex].Count = math.MaxInt
			} else {
				merged[lastIndex].Count += item.Count
			}
			continue
		}
		merged = append(merged, item)
	}
	return merged
}

func calculateTextToolCallSurcharge(ctx *gin.Context, relayInfo *relaycommon.RelayInfo, summary *textQuotaSummary) decimal.Decimal {
	dGroupRatio := decimal.NewFromFloat(summary.GroupRatio)
	dQuotaPerUnit := decimal.NewFromFloat(common.QuotaPerUnit)

	var items []ToolSurchargeItem

	if relayInfo.ResponsesUsageInfo != nil {
		for name, tool := range relayInfo.ResponsesUsageInfo.BuiltInTools {
			if tool == nil {
				continue
			}
			items = collectToolSurchargeItem(items, name, tool.CallCount, summary.ModelName)
		}
	}
	if relayInfo.RelayMode != relayconstant.RelayModeResponses &&
		strings.HasSuffix(summary.ModelName, "search-preview") {
		items = collectToolSurchargeItem(items, dto.BuildInToolWebSearchPreview, 1, summary.ModelName)
	}

	items = collectToolSurchargeItem(
		items,
		dto.BuildInToolWebSearch,
		ctx.GetInt("claude_web_search_requests"),
		summary.ModelName,
	)

	if ctx.GetBool("gemini_google_search_call") {
		items = collectToolSurchargeItem(items, dto.BuildInToolGoogleSearch, 1, summary.ModelName)
	}

	summary.ToolSurchargeItems = mergeToolSurchargeItems(items)
	var surcharge decimal.Decimal
	for _, item := range summary.ToolSurchargeItems {
		surcharge = surcharge.Add(decimal.NewFromFloat(item.Price).
			Mul(decimal.NewFromInt(int64(item.Count))).
			Div(decimal.NewFromInt(1000)).
			Mul(dGroupRatio).
			Mul(dQuotaPerUnit))
	}

	return surcharge
}

// noteQuotaClamp records the first quota saturation event onto relayInfo so it
// can later be attached to the consume/task log for admin auditing. First
// non-nil clamp wins (a single request may hit multiple conversions).
func noteQuotaClamp(relayInfo *relaycommon.RelayInfo, clamp *common.QuotaClamp) {
	if clamp == nil || relayInfo == nil {
		return
	}
	if relayInfo.QuotaClamp == nil {
		relayInfo.QuotaClamp = clamp
	}
}

func composeTieredTextQuota(relayInfo *relaycommon.RelayInfo, summary textQuotaSummary, tieredQuota int, tieredResult *billingexpr.TieredResult) int {
	if summary.ToolCallSurchargeQuota.IsZero() {
		return tieredQuota
	}

	if tieredResult != nil {
		if snap := relayInfo.TieredBillingSnapshot; snap != nil {
			// 这一支刻意**不用** TryTieredSettle 已经取整过的 tieredQuota,而是从
			// before-group 重算,避免引入第二次舍入。代价是 TryTieredSettle 在
			// 返回前打的那道非负地板(tiered_settle.go)也一并被绕开了:
			// ActualQuotaBeforeGroup 是表达式的原始结果,运营写的
			// `p*3 + c*15 - 20000` 这类促销式在小 token 数上就是负的。
			//
			// 负值经 SettleBilling 变成负 delta → 资金来源执行 Increase,扣费变成
			// 给用户充值,而工具附加费(web_search 默认单价硬编码 10.0)只需要一次
			// 调用就能把这条路走通 —— 实测每请求凭空生成 4850 额度,循环无上限。
			// 所以地板必须在这里再走一遍,和 tiered_settle.go 那道逐字同义。
			base := decimal.NewFromFloat(tieredResult.ActualQuotaBeforeGroup).
				Mul(decimal.NewFromFloat(snap.GroupRatio))
			if base.IsNegative() {
				common.SysError(fmt.Sprintf(
					"tiered billing expression produced a negative quota (%s) for model %s while composing tool-call surcharges; clamped to 0",
					base.String(), relayInfo.OriginModelName))
				base = decimal.Zero
			}
			quota, clamp := common.QuotaFromDecimalChecked(base.Add(summary.ToolCallSurchargeQuota))
			noteQuotaClamp(relayInfo, clamp)
			return quota
		}
	}

	// Saturate the final sum, not just the surcharge: tieredQuota can be near
	// MaxQuota and adding the surcharge could push the total past the int32
	// quota policy bound (persisted quota columns are 32-bit).
	total, clamp := common.QuotaFromDecimalChecked(
		decimal.NewFromInt(int64(tieredQuota)).Add(summary.ToolCallSurchargeQuota),
	)
	noteQuotaClamp(relayInfo, clamp)
	return total
}

// calculateTextQuotaSummary expects a usage already remapped by
// effectiveBillingUsage; PostTextConsumeQuota performs that remap once and shares
// the result with tiered billing, affinity observation and logging.
// reasoningTokensOutsideCompletion 返回上游放在 completion_tokens **之外**的
// 思考 token 数。
//
// 「思考 token 要计费」是本仓既定口径:原生 Gemini 路径把 ThoughtsTokenCount
// 加进 CompletionTokens(service/billing_usage.go),Gemini→OpenAI 转换用
// TotalTokens − PromptTokens 兜底(relaykit/relayconvert),本地兜底计数也把
// GetReasoningContent() 算进去(relay/channel/openai)。唯独 OpenAI 兼容渠道
// (type=1)这条主路直接采信上游的 completion_tokens,而实测中的上游把 reasoning
// 放在它**之外**:gemini-3-flash 一次 {prompt 3, completion 1, total 57,
// reasoning 53} 的真实调用只收到了 1 个输出 token 的钱,少收 50 倍;
// gemini-3.1-pro-high 的 {completion 0, reasoning 99} 更是按纯输入收费。
//
// 只认一种确凿形状:上游自己报了 reasoning_tokens,且
// prompt + completion + reasoning 恰好等于上游自己报的 total,而
// prompt + completion 不等于 total。三个数字互相印证时才补,任何一处对不上
// 就原样不动 —— Claude 语义下 prompt_tokens 不含缓存读写,total 本来就大于
// prompt + completion,拿差额当思考 token 会凭空多收钱。宁可继续少收,
// 也不能靠猜多收。
func reasoningTokensOutsideCompletion(usage *dto.Usage) int {
	if usage == nil {
		return 0
	}
	reasoning := usage.CompletionTokenDetails.ReasoningTokens
	if reasoning <= 0 || usage.TotalTokens <= 0 {
		return 0
	}
	if usage.PromptTokens+usage.CompletionTokens == usage.TotalTokens {
		return 0
	}
	if usage.PromptTokens+usage.CompletionTokens+reasoning != usage.TotalTokens {
		return 0
	}
	return reasoning
}

func calculateTextQuotaSummary(ctx *gin.Context, relayInfo *relaycommon.RelayInfo, usage *dto.Usage) textQuotaSummary {
	summary := textQuotaSummary{
		ModelName:            relayInfo.OriginModelName,
		TokenName:            ctx.GetString("token_name"),
		UseTimeSeconds:       time.Now().Unix() - relayInfo.StartTime.Unix(),
		CompletionRatio:      relayInfo.PriceData.CompletionRatio,
		CacheRatio:           relayInfo.PriceData.CacheRatio,
		ImageRatio:           relayInfo.PriceData.ImageRatio,
		ModelRatio:           relayInfo.PriceData.ModelRatio,
		GroupRatio:           relayInfo.PriceData.GroupRatioInfo.GroupRatio,
		ModelPrice:           relayInfo.PriceData.ModelPrice,
		CacheCreationRatio:   relayInfo.PriceData.CacheCreationRatio,
		CacheCreationRatio5m: relayInfo.PriceData.CacheCreation5mRatio,
		CacheCreationRatio1h: relayInfo.PriceData.CacheCreation1hRatio,
		UsageSemantic:        usageSemanticFromUsage(relayInfo, usage),
	}
	summary.IsClaudeUsageSemantic = summary.UsageSemantic == "anthropic"

	if usage == nil {
		usage = &dto.Usage{
			PromptTokens:     relayInfo.GetEstimatePromptTokens(),
			CompletionTokens: 0,
			TotalTokens:      relayInfo.GetEstimatePromptTokens(),
		}
	}

	// 上游报回来的 token 数是**外部输入**,协议上不该为负,但坏掉的/被改写的上游
	// 会给出负数。此前这里原样相加:prompt=1000、completion=-1000000 让
	// TotalTokens 变成负数,hasBillableUsage() 判成「这笔没有可计费用量」,整笔
	// (含真实发生的 prompt 侧)免单。往上有饱和转换兜着,往下一道闸都没有。
	//
	// 逐分量夹到 0 而不是只夹合计:合计非负但某一路为负同样会把另一路吃掉。
	summary.PromptTokens = clampUpstreamTokenCount(relayInfo, "prompt_tokens", int64(usage.PromptTokens))
	summary.CompletionTokens = clampUpstreamTokenCount(relayInfo, "completion_tokens",
		int64(usage.CompletionTokens)+int64(reasoningTokensOutsideCompletion(usage)))
	// 合计同样走 int64:两个分量各自已经合法(≤ MaxInt32),但在 32 位构建上相加
	// 仍会溢出;两者都已非负,所以这里只可能撞上界,不再重复告警。
	summary.TotalTokens = int(min(int64(summary.PromptTokens)+int64(summary.CompletionTokens), int64(maxUpstreamTokenCount)))
	summary.CacheTokens = clampUpstreamTokenCount(relayInfo, "cached_tokens", int64(usage.PromptTokensDetails.CachedTokens))
	summary.CacheCreationTokens = clampUpstreamTokenCount(relayInfo, "cache_creation_tokens", int64(usage.PromptTokensDetails.CacheCreationTokensTotal()))
	summary.CacheCreationTokens5m = clampUpstreamTokenCount(relayInfo, "cache_creation_5m_tokens", int64(usage.ClaudeCacheCreation5mTokens))
	summary.CacheCreationTokens1h = clampUpstreamTokenCount(relayInfo, "cache_creation_1h_tokens", int64(usage.ClaudeCacheCreation1hTokens))
	summary.ImageTokens = clampUpstreamTokenCount(relayInfo, "image_tokens", int64(usage.PromptTokensDetails.ImageTokens))
	summary.AudioTokens = clampUpstreamTokenCount(relayInfo, "audio_tokens", int64(usage.PromptTokensDetails.AudioTokens))
	legacyClaudeDerived := isLegacyClaudeDerivedOpenAIUsage(relayInfo, usage)
	// OpenAI 语义下 prompt_tokens_details 的各路明细按协议都是 prompt_tokens 的
	// **子集**(下面 baseTokens 正是逐项从 prompt 里减掉它们)。上游报一个大于
	// prompt_tokens 的 cached_tokens 时,那一段是净加上去的:扣费与真实 prompt
	// 规模彻底脱钩,单次请求即可把用户余额打到 int32 饱和(实测 −21 亿)。
	// 按子集语义夹回 prompt 是这里唯一站得住的上界。
	//
	// Claude 语义(以及由 Claude 派生的旧 OpenAI 形状)下 prompt_tokens 本就不含
	// 缓存段,不存在这层子集关系,所以那两条路不夹 —— 夹了才是错的。
	if !summary.IsClaudeUsageSemantic && !legacyClaudeDerived {
		summary.CacheTokens = capUpstreamSubsetTokens(relayInfo, "cached_tokens", summary.CacheTokens, summary.PromptTokens)
		summary.CacheCreationTokens = capUpstreamSubsetTokens(relayInfo, "cache_creation_tokens", summary.CacheCreationTokens, summary.PromptTokens)
		summary.ImageTokens = capUpstreamSubsetTokens(relayInfo, "image_tokens", summary.ImageTokens, summary.PromptTokens)
		summary.AudioTokens = capUpstreamSubsetTokens(relayInfo, "audio_tokens", summary.AudioTokens, summary.PromptTokens)
	}
	isOpenRouterClaudeBilling := relayInfo.ChannelMeta != nil &&
		relayInfo.ChannelType == constant.ChannelTypeOpenRouter &&
		summary.IsClaudeUsageSemantic

	if isOpenRouterClaudeBilling {
		summary.PromptTokens -= summary.CacheTokens
		isUsingCustomSettings := relayInfo.PriceData.UsePrice || hasCustomModelRatio(summary.ModelName, relayInfo.PriceData.ModelRatio)
		if summary.CacheCreationTokens == 0 && relayInfo.PriceData.CacheCreationRatio != 1 && usage.Cost != 0 && !isUsingCustomSettings {
			maybeCacheCreationTokens := CalcOpenRouterCacheCreateTokens(*usage, relayInfo.PriceData)
			if maybeCacheCreationTokens >= 0 && summary.PromptTokens >= maybeCacheCreationTokens {
				summary.CacheCreationTokens = maybeCacheCreationTokens
			}
		}
		summary.PromptTokens -= summary.CacheCreationTokens
	}

	dPromptTokens := decimal.NewFromInt(int64(summary.PromptTokens))
	dCacheTokens := decimal.NewFromInt(int64(summary.CacheTokens))
	dImageTokens := decimal.NewFromInt(int64(summary.ImageTokens))
	dAudioTokens := decimal.NewFromInt(int64(summary.AudioTokens))
	dCompletionTokens := decimal.NewFromInt(int64(summary.CompletionTokens))
	dCachedCreationTokens := decimal.NewFromInt(int64(summary.CacheCreationTokens))
	dCompletionRatio := decimal.NewFromFloat(summary.CompletionRatio)
	dCacheRatio := decimal.NewFromFloat(summary.CacheRatio)
	dImageRatio := decimal.NewFromFloat(summary.ImageRatio)
	dModelRatio := decimal.NewFromFloat(summary.ModelRatio)
	dGroupRatio := decimal.NewFromFloat(summary.GroupRatio)
	dModelPrice := decimal.NewFromFloat(summary.ModelPrice)
	dCacheCreationRatio := decimal.NewFromFloat(summary.CacheCreationRatio)
	dCacheCreationRatio5m := decimal.NewFromFloat(summary.CacheCreationRatio5m)
	dCacheCreationRatio1h := decimal.NewFromFloat(summary.CacheCreationRatio1h)
	dQuotaPerUnit := decimal.NewFromFloat(common.QuotaPerUnit)

	ratio := dModelRatio.Mul(dGroupRatio)
	summary.ToolCallSurchargeQuota = calculateTextToolCallSurcharge(ctx, relayInfo, &summary)

	var audioInputQuota decimal.Decimal
	if !relayInfo.PriceData.UsePrice {
		baseTokens := dPromptTokens

		var cachedTokensWithRatio decimal.Decimal
		if !dCacheTokens.IsZero() {
			if !summary.IsClaudeUsageSemantic && !legacyClaudeDerived {
				baseTokens = baseTokens.Sub(dCacheTokens)
			}
			cachedTokensWithRatio = dCacheTokens.Mul(dCacheRatio)
		}

		var cachedCreationTokensWithRatio decimal.Decimal
		hasSplitCacheCreationTokens := summary.CacheCreationTokens5m > 0 || summary.CacheCreationTokens1h > 0
		if !dCachedCreationTokens.IsZero() || hasSplitCacheCreationTokens {
			if !summary.IsClaudeUsageSemantic && !legacyClaudeDerived {
				baseTokens = baseTokens.Sub(dCachedCreationTokens)
				cachedCreationTokensWithRatio = dCachedCreationTokens.Mul(dCacheCreationRatio)
			} else {
				remaining := summary.CacheCreationTokens - summary.CacheCreationTokens5m - summary.CacheCreationTokens1h
				if remaining < 0 {
					remaining = 0
				}
				cachedCreationTokensWithRatio = decimal.NewFromInt(int64(remaining)).Mul(dCacheCreationRatio)
				cachedCreationTokensWithRatio = cachedCreationTokensWithRatio.Add(decimal.NewFromInt(int64(summary.CacheCreationTokens5m)).Mul(dCacheCreationRatio5m))
				cachedCreationTokensWithRatio = cachedCreationTokensWithRatio.Add(decimal.NewFromInt(int64(summary.CacheCreationTokens1h)).Mul(dCacheCreationRatio1h))
			}
		}

		var imageTokensWithRatio decimal.Decimal
		if !dImageTokens.IsZero() {
			baseTokens = baseTokens.Sub(dImageTokens)
			imageTokensWithRatio = dImageTokens.Mul(dImageRatio)
		}

		if !dAudioTokens.IsZero() {
			summary.AudioInputPrice = operation_setting.GetGeminiInputAudioPricePerMillionTokens(summary.ModelName)
			if summary.AudioInputPrice > 0 {
				baseTokens = baseTokens.Sub(dAudioTokens)
				audioInputQuota = decimal.NewFromFloat(summary.AudioInputPrice).
					Div(decimal.NewFromInt(1000000)).Mul(dAudioTokens).Mul(dGroupRatio).Mul(dQuotaPerUnit)
			}
		}

		// OpenAI cache-write usage reports unadjusted prefix counts, so
		// cached_tokens + cache_write_tokens can exceed prompt_tokens and the
		// remainder can go negative. Clamp at zero so overlap never turns into
		// a negative base charge.
		if baseTokens.IsNegative() {
			baseTokens = decimal.Zero
		}

		promptQuota := baseTokens.Add(cachedTokensWithRatio).Add(imageTokensWithRatio).Add(cachedCreationTokensWithRatio)
		completionQuota := dCompletionTokens.Mul(dCompletionRatio)
		quotaCalculateDecimal := promptQuota.Add(completionQuota).Mul(ratio)
		quotaCalculateDecimal = quotaCalculateDecimal.Add(audioInputQuota)
		quotaCalculateDecimal = relayInfo.PriceData.ApplyOtherRatiosToDecimal(quotaCalculateDecimal)
		quotaCalculateDecimal = quotaCalculateDecimal.Add(summary.ToolCallSurchargeQuota)

		if !ratio.IsZero() && quotaCalculateDecimal.LessThanOrEqual(decimal.Zero) {
			quotaCalculateDecimal = decimal.NewFromInt(1)
		}
		quota, clamp := common.QuotaFromDecimalChecked(quotaCalculateDecimal)
		summary.Quota = quota
		noteQuotaClamp(relayInfo, clamp)
	} else {
		quotaCalculateDecimal := dModelPrice.Mul(dQuotaPerUnit).Mul(dGroupRatio)
		quotaCalculateDecimal = quotaCalculateDecimal.Add(audioInputQuota)
		quotaCalculateDecimal = relayInfo.PriceData.ApplyOtherRatiosToDecimal(quotaCalculateDecimal)
		quotaCalculateDecimal = quotaCalculateDecimal.Add(summary.ToolCallSurchargeQuota)
		quota, clamp := common.QuotaFromDecimalChecked(quotaCalculateDecimal)
		summary.Quota = quota
		noteQuotaClamp(relayInfo, clamp)
	}

	// 「上游没返回计费信息就不收钱」这条兜底只对**按量**计费成立：那时没有 token
	// 数就确实算不出金额。按次计费的金额与 token 数严格无关（同一模型 prompt=2 与
	// prompt=7702 都收 20000），用 TotalTokens 当开关等于把一次已经完成的调用
	// 免单。所以按次分支只保留 tool 附加费这条判据之外的原样金额。
	if !relayInfo.PriceData.UsePrice {
		if !summary.hasBillableUsage() {
			summary.Quota = 0
		} else if !ratio.IsZero() && summary.Quota == 0 {
			summary.Quota = 1
		}
	}

	return summary
}

func usageSemanticFromUsage(relayInfo *relaycommon.RelayInfo, usage *dto.Usage) string {
	if usage != nil && usage.UsageSemantic != "" {
		return usage.UsageSemantic
	}
	if relayInfo != nil && relayInfo.GetFinalRequestRelayFormat() == types.RelayFormatClaude {
		return "anthropic"
	}
	return "openai"
}

func PostTextConsumeQuota(ctx *gin.Context, relayInfo *relaycommon.RelayInfo, usage *dto.Usage, extraContent []string) {
	originUsage := usage
	billingUsage := effectiveBillingUsage(usage)
	if usage == nil {
		extraContent = append(extraContent, "上游无计费信息")
	}
	if originUsage != nil {
		ObserveChannelAffinityUsageCacheByRelayFormat(ctx, billingUsage, relayInfo.GetFinalRequestRelayFormat())
	}

	adminRejectReason := common.GetContextKeyString(ctx, constant.ContextKeyAdminRejectReason)
	summary := calculateTextQuotaSummary(ctx, relayInfo, billingUsage)

	var tieredResult *billingexpr.TieredResult
	tieredBillingApplied := false
	if originUsage != nil {
		var tieredUsedVars map[string]bool
		if snap := relayInfo.TieredBillingSnapshot; snap != nil {
			tieredUsedVars = billingexpr.UsedVars(snap.ExprString)
		}
		tieredOk, tieredQuota, tieredRes := TryTieredSettle(relayInfo, BuildTieredTokenParams(billingUsage, summary.IsClaudeUsageSemantic, tieredUsedVars))
		if tieredOk {
			tieredBillingApplied = true
			tieredResult = tieredRes
			summary.Quota = composeTieredTextQuota(relayInfo, summary, tieredQuota, tieredRes)
		}
	}

	for _, item := range summary.ToolSurchargeItems {
		q := decimal.NewFromFloat(item.Price).
			Mul(decimal.NewFromInt(int64(item.Count))).
			Div(decimal.NewFromInt(1000)).
			Mul(decimal.NewFromFloat(summary.GroupRatio)).
			Mul(decimal.NewFromFloat(common.QuotaPerUnit))
		extraContent = append(extraContent, fmt.Sprintf(
			"%s 调用 %d 次，调用花费 %s",
			item.Name,
			item.Count,
			logger.LogQuota(common.QuotaFromDecimal(q)),
		))
	}
	if summary.AudioInputPrice > 0 && summary.AudioTokens > 0 {
		q := decimal.NewFromFloat(summary.AudioInputPrice).Div(decimal.NewFromInt(1000000)).Mul(decimal.NewFromInt(int64(summary.AudioTokens))).Mul(decimal.NewFromFloat(summary.GroupRatio)).Mul(decimal.NewFromFloat(common.QuotaPerUnit))
		extraContent = append(extraContent, fmt.Sprintf("Audio Input 花费 %s", logger.LogQuota(common.QuotaFromDecimal(q))))
	}

	if !summary.hasBillableUsage() {
		extraContent = append(extraContent, "上游没有返回计费信息，无法扣费（可能是上游超时）")
		logger.LogError(ctx, fmt.Sprintf("total tokens is 0, cannot consume quota, userId %d, channelId %d, tokenId %d, model %s， pre-consumed quota %d", relayInfo.UserId, relayInfo.ChannelId, relayInfo.TokenId, summary.ModelName, relayInfo.FinalPreConsumedQuota))
	}
	// 判据是"有 token **或** 有金额",不是只看 token。
	//
	// 按次计价(UsePrice)时金额与 token 数无关:上面第 506 行那段刻意只在按量
	// 计费下把 Quota 清零,按次计费不能因为 token 数为 0 就免单。于是存在
	// 一条真实可达的路 —— 上游返回一份合法但**不带 usage** 的响应
	// (/v1/responses 非流式就没有任何兜底,OaiResponsesHandler 对 Usage==nil
	// 原样放行),或上游自报负 token 被 clampUpstreamTokenCount 夹成 0 ——
	// 这一次 SettleBilling 照样扣了全额,而 users.used_quota、users.request_count
	// 与 channels.used_quota 一个都不加。后果是用户面板上的"已用额度"与实际
	// 扣掉的余额长期对不上、且不可事后自愈,渠道用量也永久少记。
	//
	// 下面的 SettleBilling 是**无条件**执行的,所以这两行必须与它同一个判据。
	// 音频/实时两条路(service/quota.go)早就是这个写法,文本路是漏改的那一处。
	if summary.hasBillableUsage() || summary.Quota > 0 {
		model.UpdateUserUsedQuotaAndRequestCount(relayInfo.UserId, summary.Quota)
		model.UpdateChannelUsedQuota(relayInfo.ChannelId, summary.Quota)
	}

	if err := SettleBilling(ctx, relayInfo, summary.Quota); err != nil {
		// 见 attachSettleFailure:结算失败但日志照记全额,差额必须留下指路牌。
		relayInfo.SettleFailure = err.Error()
		logger.LogError(ctx, "error settling billing: "+err.Error())
	}

	logModel := summary.ModelName
	if strings.HasPrefix(logModel, "gpt-4-gizmo") {
		logModel = "gpt-4-gizmo-*"
		extraContent = append(extraContent, fmt.Sprintf("模型 %s", summary.ModelName))
	}
	if strings.HasPrefix(logModel, "gpt-4o-gizmo") {
		logModel = "gpt-4o-gizmo-*"
		extraContent = append(extraContent, fmt.Sprintf("模型 %s", summary.ModelName))
	}

	logContent := strings.Join(extraContent, ", ")
	var other map[string]interface{}
	if summary.IsClaudeUsageSemantic {
		other = GenerateClaudeOtherInfo(ctx, relayInfo,
			summary.ModelRatio, summary.GroupRatio, summary.CompletionRatio,
			summary.CacheTokens, summary.CacheRatio,
			summary.CacheCreationTokens, summary.CacheCreationRatio,
			summary.CacheCreationTokens5m, summary.CacheCreationRatio5m,
			summary.CacheCreationTokens1h, summary.CacheCreationRatio1h,
			summary.ModelPrice, relayInfo.PriceData.GroupRatioInfo.GroupSpecialRatio)
		other["usage_semantic"] = "anthropic"
	} else {
		other = GenerateTextOtherInfo(ctx, relayInfo, summary.ModelRatio, summary.GroupRatio, summary.CompletionRatio, summary.CacheTokens, summary.CacheRatio, summary.ModelPrice, relayInfo.PriceData.GroupRatioInfo.GroupSpecialRatio)
	}
	appendUsageBillingPathForLog(other, common.GetContextKeyBool(ctx, constant.ContextKeyLocalCountTokens), originUsage)
	if adminRejectReason != "" {
		other["reject_reason"] = adminRejectReason
	}
	if summary.ImageTokens != 0 {
		other["image"] = true
		other["image_ratio"] = summary.ImageRatio
		other["image_output"] = summary.ImageTokens
	}
	appendToolSurchargeLogInfo(other, summary.ToolSurchargeItems)
	if summary.AudioInputPrice > 0 && summary.AudioTokens > 0 {
		other["audio_input_seperate_price"] = true
		other["audio_input_token_count"] = summary.AudioTokens
		other["audio_input_price"] = summary.AudioInputPrice
	}
	if summary.CacheCreationTokens > 0 {
		other["cache_creation_tokens"] = summary.CacheCreationTokens
		other["cache_creation_ratio"] = summary.CacheCreationRatio
	}
	if summary.CacheCreationTokens5m > 0 {
		other["cache_creation_tokens_5m"] = summary.CacheCreationTokens5m
		other["cache_creation_ratio_5m"] = summary.CacheCreationRatio5m
	}
	if summary.CacheCreationTokens1h > 0 {
		other["cache_creation_tokens_1h"] = summary.CacheCreationTokens1h
		other["cache_creation_ratio_1h"] = summary.CacheCreationRatio1h
	}
	cacheWriteTokens := cacheWriteTokensTotal(summary)
	if cacheWriteTokens > 0 {
		// cache_write_tokens: normalized cache creation total for UI display.
		// If split 5m/1h values are present, this is their sum; otherwise it falls back
		// to cache_creation_tokens.
		other["cache_write_tokens"] = cacheWriteTokens
	}
	if relayInfo.GetFinalRequestRelayFormat() != types.RelayFormatClaude && billingUsage != nil && billingUsage.UsageSource != "" && billingUsage.InputTokens > 0 {
		// input_tokens_total: explicit normalized total input used by the usage log UI.
		// Only write this field when upstream/current conversion has already provided a
		// reliable total input value and tagged the usage source. Do not infer it from
		// prompt/cache fields here, otherwise old upstream payloads may be double-counted.
		other["input_tokens_total"] = billingUsage.InputTokens
	}
	if tieredBillingApplied {
		InjectTieredBillingInfo(other, relayInfo, tieredResult)
	}

	attachQuotaSaturation(ctx, relayInfo, other)
	attachSettleFailure(ctx, relayInfo, other)
	attachPreConsumeShortfall(ctx, relayInfo, other)
	attachGroupRatioFallback(ctx, relayInfo, other)
	QyLogMetricsAttachCacheBasis(other, summary.PromptTokens, summary.CacheTokens, cacheWriteTokens, summary.IsClaudeUsageSemantic)

	model.RecordConsumeLog(ctx, relayInfo.UserId, model.RecordConsumeLogParams{
		ChannelId:        relayInfo.ChannelId,
		PromptTokens:     summary.PromptTokens,
		CompletionTokens: summary.CompletionTokens,
		ModelName:        logModel,
		TokenName:        summary.TokenName,
		Quota:            summary.Quota,
		Content:          logContent,
		TokenId:          relayInfo.TokenId,
		UseTimeSeconds:   int(summary.UseTimeSeconds),
		IsStream:         relayInfo.IsStream,
		Group:            relayInfo.UsingGroup,
		Other:            other,
	})
	gopool.Go(func() {
		perfmetrics.RecordRelaySample(relayInfo, true, int64(summary.CompletionTokens))
	})
}
