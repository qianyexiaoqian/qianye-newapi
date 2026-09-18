package service

import (
	"encoding/base64"
	"fmt"
	"strings"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/constant"
	"github.com/QuantumNous/new-api/logger"
	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/pkg/billingexpr"
	relaycommon "github.com/QuantumNous/new-api/relay/common"
	"github.com/QuantumNous/new-api/relaykit/types"
	"github.com/QuantumNous/new-api/setting/ratio_setting"
	hosttypes "github.com/QuantumNous/new-api/types"

	"github.com/gin-gonic/gin"
)

// attachQuotaSaturationToOther nests a quota saturation marker under
// other.admin_info.quota_saturation. Nesting under admin_info makes it
// admin-only for free, since model.formatUserLogs strips the whole admin_info
// object for non-admin viewers. Creates admin_info if absent. No-op when the
// clamp is nil (the common case: no saturation happened).
func attachQuotaSaturationToOther(other *model.LogOther, clamp *common.QuotaClamp) {
	if clamp == nil || other == nil {
		return
	}
	other.SetAdmin("quota_saturation", clamp.AuditMap())
}

// attachGroupRatioFallbackToOther nests a "billed at the fail-open ratio 1.0"
// marker under other.admin_info.group_ratio_missing. It is the *sole* writer of
// that key, exactly as attachQuotaSaturationToOther is for quota_saturation.
//
// 它必须与 attachQuotaSaturationToOther 一样有一个不需要 *RelayInfo 的形态:
// 异步 Task 差额结算(service/task_billing.go)手上根本没有 RelayInfo,而它恰恰是
// 单笔金额最大的那条计费链路。少这一个形态的后果是运维按
// admin_info.group_ratio_missing 全量扫描补差时,会得出「只有文本/WSS 受影响、
// Task 没事」这个完全错误的结论。
func attachGroupRatioFallbackToOther(other *model.LogOther, miss *ratio_setting.GroupRatioMiss) {
	if miss == nil || other == nil {
		return
	}
	other.SetAdmin("group_ratio_missing", miss.AuditMap())
}

// attachQuotaSaturation records the request's quota clamp (if any) onto the
// consume log's other.admin_info and emits a request-correlated backend audit
// line. Called right before RecordConsumeLog on the text/audio/wss paths.
func attachQuotaSaturation(ctx *gin.Context, relayInfo *relaycommon.RelayInfo, other *model.LogOther) {
	if relayInfo == nil {
		return
	}
	clamp := relayInfo.QuotaClamp
	if clamp == nil {
		return
	}
	attachQuotaSaturationToOther(other, clamp)
	logger.LogWarn(ctx, fmt.Sprintf("quota saturation on consume log: op=%s kind=%s original=%g clamped=%d user=%d model=%s",
		clamp.Op, clamp.Kind, clamp.Original, clamp.Clamped, relayInfo.UserId, relayInfo.GetBillingModelName()))
}

// attachSettleFailure 把「这一笔结算失败了」这件事写进消费日志的
// other.admin_info.settle_failed,并留一条与请求关联的后端告警。
//
// 结算失败时代码只能继续往下走(请求已经跑完,退不回上游 token),日志照常按
// 全额真实花费记账 —— 于是 logs.quota 与实际收到的钱分家,而账面上没有任何
// 一处指出这件事。这个键就是那唯一的指路牌:按它可以把漏收的笔捞出来补。
// 与 attachQuotaSaturation 同形,嵌在 admin_info 下天然只对管理员可见。
// attachPreConsumeShortfall 把「这一笔的预扣额没兜住真实花费」写进消费日志的
// other.admin_info.pre_consume_shortfall,并留一条与请求关联的后端告警。
//
// 只在差额**相对预留额显著**时告警(否则每一笔正常请求都会响:预扣本来就是估算,
// 小幅补收是常态),但标记一律写 —— 事后统计需要的是全量,不是被阈值过滤过的样本。
func attachPreConsumeShortfall(ctx *gin.Context, relayInfo *relaycommon.RelayInfo, other *model.LogOther) {
	if relayInfo == nil || other == nil {
		return
	}
	sf := relayInfo.PreConsumeShortfall
	if sf == nil || sf.Shortfall <= 0 {
		return
	}
	other.SetAdmin("pre_consume_shortfall", sf.AuditMap())
	// 阈值 = 补收额超过预留额本身。到这一档说明预留连一半都没覆盖,
	// 而这正是"客户端没写 max_tokens + 模型输出远超 8192"的形状。
	if sf.Reserved > 0 && sf.Shortfall > sf.Reserved {
		logger.LogWarn(ctx, fmt.Sprintf(
			"pre-consume did not cover the charge: reserved=%d charged=%d shortfall=%d user=%d model=%s",
			sf.Reserved, sf.Charged, sf.Shortfall, relayInfo.UserId, relayInfo.OriginModelName))
	}
}

func attachSettleFailure(ctx *gin.Context, relayInfo *relaycommon.RelayInfo, other *model.LogOther) {
	if relayInfo == nil || other == nil {
		return
	}
	if relayInfo.SettleFailure == "" {
		return
	}
	other.SetAdmin("settle_failed", map[string]interface{}{
		"error":          relayInfo.SettleFailure,
		"billing_source": relayInfo.BillingSource,
	})
	logger.LogWarn(ctx, fmt.Sprintf(
		"settlement failed but the consume log still records the full charge: user=%d model=%s source=%s: %s",
		relayInfo.UserId, relayInfo.OriginModelName, relayInfo.BillingSource, relayInfo.SettleFailure))
}

// attachGroupRatioFallback nests a "billed at the fail-open ratio 1.0" marker
// under other.admin_info.group_ratio_missing and emits a request-correlated
// backend audit line. Shape and权限模型 copied verbatim from
// attachQuotaSaturation: nesting under admin_info makes it admin-only for free
// (model.formatUserLogs strips the whole admin_info object for non-admins).
//
// 为什么必须逐笔落地而不是只报一个计数器:今天日志里 group 字段是对的、quota 是
// 按 1.0 算出来的,"这一笔本该按 0.125 收"这个事实在事后**不存在于任何地方**,
// 补差就无从谈起。打上标记之后,这条不可逆的损失变成可逆的。
func attachGroupRatioFallback(ctx *gin.Context, relayInfo *relaycommon.RelayInfo, other *model.LogOther) {
	if relayInfo == nil || other == nil {
		return
	}
	miss := relayInfo.GroupRatioFallback
	if miss == nil {
		return
	}
	attachGroupRatioFallbackToOther(other, miss)
	logger.LogWarn(ctx, fmt.Sprintf(
		"group ratio fail-open on consume log: user_group=%s model_group=%s applied_ratio=%g user=%d model=%s",
		miss.UserGroup, miss.ModelGroup, miss.AppliedRatio, relayInfo.UserId, relayInfo.OriginModelName))
}

func appendRequestPath(ctx *gin.Context, relayInfo *relaycommon.RelayInfo, other *model.LogOther) {
	if other == nil {
		return
	}
	if ctx != nil && ctx.Request != nil && ctx.Request.URL != nil {
		if path := ctx.Request.URL.Path; path != "" {
			other.SetPublic("request_path", path)
			return
		}
	}
	if relayInfo != nil && relayInfo.RequestURLPath != "" {
		path := relayInfo.RequestURLPath
		if idx := strings.Index(path, "?"); idx != -1 {
			path = path[:idx]
		}
		other.SetPublic("request_path", path)
	}
}

// AppendRelayLogAdminInfo records relay routing and conversion diagnostics in
// the admin-only scope shared by successful and failed request logs.
func AppendRelayLogAdminInfo(ctx *gin.Context, relayInfo *relaycommon.RelayInfo, other *model.LogOther) {
	if ctx == nil || other == nil {
		return
	}
	other.SetAdmin("use_channel", ctx.GetStringSlice("use_channel"))
	if relayInfo != nil {
		if billingModel := relayInfo.GetBillingModelName(); billingModel != "" && billingModel != relayInfo.OriginModelName {
			other.SetAdmin("billing_model", billingModel)
		}
		if diagnostics := relayInfo.ConversionDiagnostics(); len(diagnostics) > 0 {
			other.SetAdmin("conversion_diagnostics", diagnostics)
		}
		if relayInfo.ConversionDiagnosticsTruncated() {
			other.SetAdmin("conversion_diagnostics_truncated", true)
		}
	}
	if common.GetContextKeyBool(ctx, constant.ContextKeyChannelIsMultiKey) {
		other.SetAdmin("is_multi_key", true)
		other.SetAdmin("multi_key_index", common.GetContextKeyInt(ctx, constant.ContextKeyChannelMultiKeyIndex))
	}
	if common.GetContextKeyBool(ctx, constant.ContextKeyLocalCountTokens) {
		other.SetAdmin("local_count_tokens", true)
	}

	AppendChannelAffinityAdminInfo(ctx, other)
}

func GenerateTextOtherInfo(ctx *gin.Context, relayInfo *relaycommon.RelayInfo, modelRatio, groupRatio, completionRatio float64,
	cacheTokens int, cacheRatio float64, modelPrice float64, userGroupRatio float64) *model.LogOther {
	other := model.NewLogOther()
	other.SetPublic("model_ratio", modelRatio)
	other.SetPublic("group_ratio", groupRatio)
	other.SetPublic("completion_ratio", completionRatio)
	other.SetPublic("cache_tokens", cacheTokens)
	other.SetPublic("cache_ratio", cacheRatio)
	other.SetPublic("model_price", modelPrice)
	other.SetPublic("user_group_ratio", userGroupRatio)
	other.SetPublic("frt", float64(relayInfo.FirstResponseTime.UnixMilli()-relayInfo.StartTime.UnixMilli()))
	if relayInfo.ReasoningEffort != "" {
		other.SetPublic("reasoning_effort", relayInfo.ReasoningEffort)
	}
	QyLogMetricsAttachReasoning(ctx, relayInfo, other)
	if relayInfo.IsModelMapped {
		other.SetPublic("is_model_mapped", true)
		other.SetPublic("upstream_model_name", relayInfo.UpstreamModelName)
	}

	isSystemPromptOverwritten := common.GetContextKeyBool(ctx, constant.ContextKeySystemPromptOverride)
	if isSystemPromptOverwritten {
		other.SetPublic("is_system_prompt_overwritten", true)
	}

	AppendRelayLogAdminInfo(ctx, relayInfo, other)
	AppendResponseModelLogInfo(relayInfo, other)
	appendRequestPath(ctx, relayInfo, other)
	appendRequestConversionChain(relayInfo, other)
	appendFinalRequestFormat(relayInfo, other)
	appendBillingInfo(relayInfo, other)
	appendParamOverrideInfo(relayInfo, other)
	appendStreamStatus(relayInfo, other)
	return other
}

func AppendResponseModelLogInfo(relayInfo *relaycommon.RelayInfo, other *model.LogOther) {
	if relayInfo == nil || relayInfo.ResponseModel == nil || other == nil {
		return
	}
	observation := relayInfo.ResponseModel
	if !observation.Mismatch &&
		observation.ReturnedModel == observation.RequestedModel &&
		(observation.UpstreamModel == "" || observation.UpstreamModel == observation.RequestedModel) &&
		(relayInfo.ChannelMeta == nil || !relayInfo.IsModelMapped) {
		return
	}
	other.SetPublic("response_model", *observation)
}

func appendParamOverrideInfo(relayInfo *relaycommon.RelayInfo, other *model.LogOther) {
	if relayInfo == nil || other == nil || len(relayInfo.ParamOverrideAudit) == 0 {
		return
	}
	other.SetPublic("po", relayInfo.ParamOverrideAudit)
}

func appendStreamStatus(relayInfo *relaycommon.RelayInfo, other *model.LogOther) {
	if relayInfo == nil || other == nil || !relayInfo.IsStream || relayInfo.StreamStatus == nil {
		return
	}
	ss := relayInfo.StreamStatus
	status := "ok"
	if !ss.IsNormalEnd() || ss.HasErrors() || ss.ResponseFailed() {
		status = "error"
	}
	streamInfo := map[string]any{
		"status":     status,
		"end_reason": string(ss.EndReason),
	}
	if outcome := ss.ResponseOutcome(); outcome != "" {
		streamInfo["response_status"] = outcome
	}
	if ss.EndError != nil {
		streamInfo["end_error"] = ss.EndError.Error()
	}
	if ss.ErrorCount > 0 {
		streamInfo["error_count"] = ss.ErrorCount
		messages := make([]string, 0, len(ss.Errors))
		for _, e := range ss.Errors {
			messages = append(messages, e.Message)
		}
		streamInfo["errors"] = messages
	}
	other.SetPublic("stream_status", streamInfo)
}

func appendBillingInfo(relayInfo *relaycommon.RelayInfo, other *model.LogOther) {
	if relayInfo == nil || other == nil {
		return
	}
	// billing_source: "wallet" or "subscription"
	if relayInfo.BillingSource != "" {
		other.SetPublic("billing_source", relayInfo.BillingSource)
	}
	// 曾经还写一个 other["billing_preference"]。扣费顺序写死之后它对每一行日志都是
	// 同一个常量,而"这一笔钱到底从哪出的"由上面的 billing_source 与下面那组
	// subscription_* 字段完整回答。历史行原样留着(不读即无害)。
	if relayInfo.BillingSource == "subscription" {
		if relayInfo.SubscriptionId != 0 {
			other.SetPublic("subscription_id", relayInfo.SubscriptionId)
		}
		if relayInfo.SubscriptionPreConsumed > 0 {
			other.SetPublic("subscription_pre_consumed", relayInfo.SubscriptionPreConsumed)
		}
		// post_delta: settlement delta applied after actual usage is known (can be negative for refund)
		if relayInfo.SubscriptionPostDelta != 0 {
			other.SetPublic("subscription_post_delta", relayInfo.SubscriptionPostDelta)
		}
		if relayInfo.SubscriptionPlanId != 0 {
			other.SetPublic("subscription_plan_id", relayInfo.SubscriptionPlanId)
		}
		if relayInfo.SubscriptionPlanTitle != "" {
			other.SetPublic("subscription_plan_title", relayInfo.SubscriptionPlanTitle)
		}
		// Compute "this request" subscription consumed + remaining
		consumed := relayInfo.SubscriptionPreConsumed + relayInfo.SubscriptionPostDelta
		usedFinal := relayInfo.SubscriptionAmountUsedAfterPreConsume + relayInfo.SubscriptionPostDelta
		if consumed < 0 {
			consumed = 0
		}
		if usedFinal < 0 {
			usedFinal = 0
		}
		if relayInfo.SubscriptionAmountTotal > 0 {
			remain := max(relayInfo.SubscriptionAmountTotal-usedFinal, 0)
			other.SetPublic("subscription_total", relayInfo.SubscriptionAmountTotal)
			other.SetPublic("subscription_used", usedFinal)
			other.SetPublic("subscription_remain", remain)
		}
		if consumed > 0 {
			other.SetPublic("subscription_consumed", consumed)
		}
		// Wallet quota is normally untouched when billed from subscription; the
		// exception is a settlement that overshot amount_total, where the
		// uncollected remainder is charged to the wallet instead of being lost.
		other.SetPublic("wallet_quota_deducted", relayInfo.SubscriptionWalletShortfall)
		// ...unless the plan that unlocked this model group forbids wallet
		// overflow, in which case nobody pays that remainder and the platform
		// absorbs it. The log's `quota` column still records the full charge, so
		// without this key the row reads as "billed N, collected less than N"
		// with no explanation. Invariant for reconciliation:
		//   quota == subscription_consumed + wallet_quota_deducted + subscription_written_off
		if relayInfo.SubscriptionWrittenOff != 0 {
			other.SetPublic("subscription_written_off", relayInfo.SubscriptionWrittenOff)
		}
	}
}

func appendRequestConversionChain(relayInfo *relaycommon.RelayInfo, other *model.LogOther) {
	if relayInfo == nil || other == nil {
		return
	}
	if len(relayInfo.RequestConversionChain) == 0 {
		return
	}
	chain := make([]string, 0, len(relayInfo.RequestConversionChain))
	for _, f := range relayInfo.RequestConversionChain {
		switch f {
		case types.RelayFormatOpenAI:
			chain = append(chain, "OpenAI Compatible")
		case types.RelayFormatClaude:
			chain = append(chain, "Claude Messages")
		case types.RelayFormatGemini:
			chain = append(chain, "Google Gemini")
		case types.RelayFormatOpenAIResponses:
			chain = append(chain, "OpenAI Responses")
		default:
			chain = append(chain, string(f))
		}
	}
	if len(chain) == 0 {
		return
	}
	other.SetPublic("request_conversion", chain)
}

func appendFinalRequestFormat(relayInfo *relaycommon.RelayInfo, other *model.LogOther) {
	if relayInfo == nil || other == nil {
		return
	}
	if relayInfo.GetFinalRequestRelayFormat() == types.RelayFormatClaude {
		// claude indicates the final upstream request format is Claude Messages.
		// Frontend log rendering uses this to keep the original Claude input display.
		other.SetPublic("claude", true)
	}
}

// GenerateWssOtherInfo / GenerateAudioOtherInfo 的四项 token 明细必须收
// **归一化之后**的值,也就是真正参与计费的那一份,而不是上游原样上报的那一份。
//
// 归一化有两处:normalizeAudioTokenDetails 在上游不报 text_tokens 时用
// 「总数 − 音频」兜底文本 token(OpenAI 兼容渠道走 /v1/chat/completions 的主路
// **一处都不补** text_tokens,所以这是默认形状而不是边角),
// reasoningTokensOutsideCompletion 把落在 completion 之外的思考 token 补进输出。
//
// 原先这两个函数直接读 usage 原值,后果是**音频消费日志用它自己记的明细复算
// 不出它自己记的金额**:实测 {p:100, c:1, audio_in:20, reasoning:53, total:154}
// 在一个 ModelRatio2/CompletionRatio3/AudioRatio10 的模型上实收 884,而同一行
// 日志里 text_input=0、text_output=0、audio_input=20、completion_tokens=1,
// 照着代回去只得 400。用户在自己的日志里看到的是「输出 1 个 token 收 884」,
// 而申诉、退款仲裁与对账脚本都答不出这 884 是怎么来的。
//
// 倍率文本路当初专门为这件事把 summary.CompletionTokens 改成了归一化值,
// 音频/实时两条路是漏改的那两处。
func GenerateWssOtherInfo(ctx *gin.Context, relayInfo *relaycommon.RelayInfo, in, out TokenDetails, modelRatio, groupRatio, completionRatio, audioRatio, audioCompletionRatio, modelPrice, userGroupRatio float64) *model.LogOther {
	info := GenerateTextOtherInfo(ctx, relayInfo, modelRatio, groupRatio, completionRatio, 0, 0.0, modelPrice, userGroupRatio)
	info.SetPublic("ws", true)
	info.SetPublic("audio_input", in.AudioTokens)
	info.SetPublic("audio_output", out.AudioTokens)
	info.SetPublic("text_input", in.TextTokens)
	info.SetPublic("text_output", out.TextTokens)
	info.SetPublic("audio_ratio", audioRatio)
	info.SetPublic("audio_completion_ratio", audioCompletionRatio)
	return info
}

func GenerateAudioOtherInfo(ctx *gin.Context, relayInfo *relaycommon.RelayInfo, in, out TokenDetails, modelRatio, groupRatio, completionRatio, audioRatio, audioCompletionRatio, modelPrice, userGroupRatio float64) *model.LogOther {
	info := GenerateTextOtherInfo(ctx, relayInfo, modelRatio, groupRatio, completionRatio, 0, 0.0, modelPrice, userGroupRatio)
	info.SetPublic("audio", true)
	info.SetPublic("audio_input", in.AudioTokens)
	info.SetPublic("audio_output", out.AudioTokens)
	info.SetPublic("text_input", in.TextTokens)
	info.SetPublic("text_output", out.TextTokens)
	info.SetPublic("audio_ratio", audioRatio)
	info.SetPublic("audio_completion_ratio", audioCompletionRatio)
	return info
}

func GenerateClaudeOtherInfo(ctx *gin.Context, relayInfo *relaycommon.RelayInfo, modelRatio, groupRatio, completionRatio float64,
	cacheTokens int, cacheRatio float64,
	cacheCreationTokens int, cacheCreationRatio float64,
	cacheCreationTokens5m int, cacheCreationRatio5m float64,
	cacheCreationTokens1h int, cacheCreationRatio1h float64,
	modelPrice float64, userGroupRatio float64) *model.LogOther {
	info := GenerateTextOtherInfo(ctx, relayInfo, modelRatio, groupRatio, completionRatio, cacheTokens, cacheRatio, modelPrice, userGroupRatio)
	info.SetPublic("claude", true)
	info.SetPublic("cache_creation_tokens", cacheCreationTokens)
	info.SetPublic("cache_creation_ratio", cacheCreationRatio)
	if cacheCreationTokens5m != 0 {
		info.SetPublic("cache_creation_tokens_5m", cacheCreationTokens5m)
		info.SetPublic("cache_creation_ratio_5m", cacheCreationRatio5m)
	}
	if cacheCreationTokens1h != 0 {
		info.SetPublic("cache_creation_tokens_1h", cacheCreationTokens1h)
		info.SetPublic("cache_creation_ratio_1h", cacheCreationRatio1h)
	}
	return info
}

func GenerateMjOtherInfo(relayInfo *relaycommon.RelayInfo, priceData hosttypes.PriceData) *model.LogOther {
	other := model.NewLogOther()
	other.SetPublic("model_price", priceData.ModelPrice)
	other.SetPublic("group_ratio", priceData.GroupRatioInfo.GroupRatio)
	if priceData.GroupRatioInfo.HasSpecialRatio {
		other.SetPublic("user_group_ratio", priceData.GroupRatioInfo.GroupSpecialRatio)
	}
	appendRequestPath(nil, relayInfo, other)
	return other
}

// InjectTieredBillingInfo overlays tiered billing fields onto an existing
// module-specific other map. Call this after GenerateTextOtherInfo /
// GenerateClaudeOtherInfo / etc. when the request used tiered_expr billing.
func InjectTieredBillingInfo(other *model.LogOther, relayInfo *relaycommon.RelayInfo, result *billingexpr.TieredResult) {
	if relayInfo == nil || other == nil {
		return
	}
	snap := relayInfo.TieredBillingSnapshot
	if snap == nil {
		return
	}
	other.SetPublic("billing_mode", "tiered_expr")
	other.SetPublic("expr_b64", base64.StdEncoding.EncodeToString([]byte(snap.ExprString)))
	if result != nil {
		other.SetPublic("matched_tier", result.MatchedTier)
		if len(result.RequestRules) > 0 {
			other.SetPublic("request_rules", result.RequestRules)
		}
	}
}
