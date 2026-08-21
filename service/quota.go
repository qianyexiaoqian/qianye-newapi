package service

import (
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/constant"
	"github.com/QuantumNous/new-api/logger"
	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/pkg/billingexpr"
	perfmetrics "github.com/QuantumNous/new-api/pkg/perf_metrics"
	relaycommon "github.com/QuantumNous/new-api/relay/common"
	"github.com/QuantumNous/new-api/relaykit/dto"
	"github.com/QuantumNous/new-api/setting/ratio_setting"
	"github.com/QuantumNous/new-api/types"

	"github.com/bytedance/gopkg/util/gopool"

	"github.com/gin-gonic/gin"
	"github.com/shopspring/decimal"
)

type TokenDetails struct {
	TextTokens  int
	AudioTokens int
}

type QuotaInfo struct {
	InputDetails  TokenDetails
	OutputDetails TokenDetails
	ModelName     string
	UsePrice      bool
	ModelPrice    float64
	ModelRatio    float64
	GroupRatio    float64
}

func hasCustomModelRatio(modelName string, currentRatio float64) bool {
	defaultRatio, exists := ratio_setting.GetDefaultModelRatioMap()[modelName]
	if !exists {
		return true
	}
	return currentRatio != defaultRatio
}

// normalizeAudioTokenDetails 把上游只报了一半的 token 明细补齐成「一个 token 都不白送」。
//
// 音频计价只按 TextTokens/AudioTokens 逐项累加,而这两个数**完全由上游决定**:
// 40+ 家上游、二手中转站、自建 OpenAI 兼容服务里,只要有一家不报 text_tokens,
// 该渠道的音频模型就整体退化成「只收音频那几个 token」,除音频以外的输入输出全部免费。
// /v1/responses 更狠 —— 那条协议本来就没有 text_tokens 字段,于是一次纯文本对话
// 只要模型名撞上 gpt-4o-audio* 前缀,四项明细全是 0,金额被地板托成 1(实测 3750 → 1)。
//
// 倍率的文本路径在同样情形下是从 prompt 里逐项扣减子类、**剩下的按基础价收**
// (service/text_quota.go 的 baseTokens),两条路径口径不能相反。
// 这里取 max(上游报的, 总数−音频):
//   - 上游报得对时(text+audio==total)是恒等变换,一分钱不多收;
//   - 上游没报 text 时补成 total−audio,与 relay/channel/openai/audio.go 给
//     /v1/audio/* 打的那块补丁同向;
//   - 上游报的 text 偏小(reasoning_tokens 不算在 text 里)时按总数兜底。
//
// 单调不减:任何输入下结果都不低于改动前,不存在「原本收得到、改完反而少收」。
func normalizeAudioTokenDetails(totalTokens int, d TokenDetails) TokenDetails {
	if d.AudioTokens < 0 {
		d.AudioTokens = 0
	}
	if d.TextTokens < 0 {
		d.TextTokens = 0
	}
	if remainder := totalTokens - d.AudioTokens; remainder > d.TextTokens {
		d.TextTokens = remainder
	}
	return d
}

func calculateAudioQuota(info QuotaInfo) (int, *common.QuotaClamp) {
	if info.UsePrice {
		modelPrice := decimal.NewFromFloat(info.ModelPrice)
		quotaPerUnit := decimal.NewFromFloat(common.QuotaPerUnit)
		groupRatio := decimal.NewFromFloat(info.GroupRatio)

		quota := modelPrice.Mul(quotaPerUnit).Mul(groupRatio)
		return common.QuotaFromDecimalChecked(quota)
	}

	completionRatio := decimal.NewFromFloat(ratio_setting.GetCompletionRatio(info.ModelName))
	audioRatio := decimal.NewFromFloat(ratio_setting.GetAudioRatio(info.ModelName))
	audioCompletionRatio := decimal.NewFromFloat(ratio_setting.GetAudioCompletionRatio(info.ModelName))

	groupRatio := decimal.NewFromFloat(info.GroupRatio)
	modelRatio := decimal.NewFromFloat(info.ModelRatio)
	ratio := groupRatio.Mul(modelRatio)

	inputTextTokens := decimal.NewFromInt(int64(info.InputDetails.TextTokens))
	outputTextTokens := decimal.NewFromInt(int64(info.OutputDetails.TextTokens))
	inputAudioTokens := decimal.NewFromInt(int64(info.InputDetails.AudioTokens))
	outputAudioTokens := decimal.NewFromInt(int64(info.OutputDetails.AudioTokens))

	quota := decimal.Zero
	quota = quota.Add(inputTextTokens)
	quota = quota.Add(outputTextTokens.Mul(completionRatio))
	quota = quota.Add(inputAudioTokens.Mul(audioRatio))
	quota = quota.Add(outputAudioTokens.Mul(audioRatio).Mul(audioCompletionRatio))

	quota = quota.Mul(ratio)

	// If ratio is not zero and quota is less than or equal to zero, set quota to 1
	if !ratio.IsZero() && quota.LessThanOrEqual(decimal.Zero) {
		quota = decimal.NewFromInt(1)
	}

	return common.QuotaFromDecimalChecked(quota)
}

func PreWssConsumeQuota(ctx *gin.Context, relayInfo *relaycommon.RelayInfo, usage *dto.RealtimeUsage) error {
	if relayInfo.UsePrice {
		return nil
	}
	userQuota, err := model.GetUserQuota(relayInfo.UserId, false)
	if err != nil {
		return err
	}

	token, err := model.GetTokenByKey(strings.TrimPrefix(relayInfo.TokenKey, "sk-"), false)
	if err != nil {
		return err
	}

	modelName := relayInfo.OriginModelName
	textInputTokens := usage.InputTokenDetails.TextTokens
	textOutTokens := usage.OutputTokenDetails.TextTokens
	audioInputTokens := usage.InputTokenDetails.AudioTokens
	audioOutTokens := usage.OutputTokenDetails.AudioTokens
	modelRatio, _, _ := ratio_setting.GetModelRatio(modelName)

	autoGroup, exists := common.GetContextKey(ctx, constant.ContextKeyAutoGroup)
	if exists {
		relayInfo.UsingGroup = autoGroup.(string)
	}

	// 分组倍率解析走全仓唯一的 ratio_setting.ResolveGroupRatio(见
	// setting/ratio_setting/qy_ratio_export.go),不再在这里重复第二份 if。
	// 顺序与改动前一致:先应用 auto 改写,再按 (用户分组, 最终模型分组) 解析。
	//
	// **会话内只解析一次**:本函数在一次实时会话里会被调用很多次,每一次都真的
	// 扣一笔钱。现场重算意味着运营中途改一次倍率,同一次会话的前后半段按两个价
	// 收费(见 relay/common 的 ResolveWssGroupRatio)。
	res := relayInfo.ResolveWssGroupRatio()
	actualGroupRatio := res.Ratio
	logger.LogDebug(ctx, "final group ratio: %f", actualGroupRatio)
	relayInfo.NoteGroupRatioFallback(res)

	quotaInfo := QuotaInfo{
		InputDetails: TokenDetails{
			TextTokens:  textInputTokens,
			AudioTokens: audioInputTokens,
		},
		OutputDetails: TokenDetails{
			TextTokens:  textOutTokens,
			AudioTokens: audioOutTokens,
		},
		ModelName:  modelName,
		UsePrice:   relayInfo.UsePrice,
		ModelRatio: modelRatio,
		GroupRatio: actualGroupRatio,
	}
	quotaInfo.InputDetails = normalizeAudioTokenDetails(usage.InputTokens, quotaInfo.InputDetails)
	quotaInfo.OutputDetails = normalizeAudioTokenDetails(usage.OutputTokens, quotaInfo.OutputDetails)

	quota, clamp := calculateAudioQuota(quotaInfo)
	noteQuotaClamp(relayInfo, clamp)

	if userQuota < quota {
		return fmt.Errorf("user quota is not enough, user quota: %s, need quota: %s", logger.FormatQuota(userQuota), logger.FormatQuota(quota))
	}

	if !token.UnlimitedQuota && token.RemainQuota < quota {
		return fmt.Errorf("token quota is not enough, token remain quota: %s, need quota: %s", logger.FormatQuota(token.RemainQuota), logger.FormatQuota(quota))
	}

	// 实时会话的增量必须走**预留**，不能直接扣费。
	//
	// 会话结束时 PostWssConsumeQuota 会用**累计** usage 重算整场金额再
	// SettleBilling，而 SettleBilling 的差额只认 BillingSession 里的预扣额。
	// 增量若走 PostConsumeQuota，这两笔钱互不相识：实收 = Σ增量 + 整场金额
	// ≈ 2 倍。改成把累计额度补进预扣，最终 delta = 整场 − 已预留，一场只收一次。
	relayInfo.WssReservedQuota += quota
	if relayInfo.Billing != nil {
		if err := relayInfo.Billing.Reserve(relayInfo.WssReservedQuota); err != nil {
			return err
		}
		relayInfo.FinalPreConsumedQuota = relayInfo.Billing.GetPreConsumedQuota()
	} else {
		// 无计费会话时退回旧路径，并把已扣额度计入 FinalPreConsumedQuota，
		// 否则 SettleBilling 的回退分支会按整场金额再扣一次。
		if err := PostConsumeQuota(relayInfo, quota, 0, false); err != nil {
			return err
		}
		relayInfo.FinalPreConsumedQuota += quota
	}
	logger.LogInfo(ctx, "realtime streaming reserve quota success, quota: "+fmt.Sprintf("%d", quota))
	return nil
}

func PostWssConsumeQuota(ctx *gin.Context, relayInfo *relaycommon.RelayInfo, modelName string,
	usage *dto.RealtimeUsage, extraContent string) {

	var tieredResult *billingexpr.TieredResult
	tieredOk, tieredQuota, tieredRes := TryTieredSettle(relayInfo, billingexpr.TokenParams{
		P:   float64(usage.InputTokens),
		C:   float64(usage.OutputTokens),
		Len: float64(usage.InputTokens),
	})
	if tieredOk {
		tieredResult = tieredRes
	}

	useTimeSeconds := time.Now().Unix() - relayInfo.StartTime.Unix()
	textInputTokens := usage.InputTokenDetails.TextTokens
	textOutTokens := usage.OutputTokenDetails.TextTokens

	audioInputTokens := usage.InputTokenDetails.AudioTokens
	audioOutTokens := usage.OutputTokenDetails.AudioTokens

	tokenName := ctx.GetString("token_name")
	completionRatio := decimal.NewFromFloat(ratio_setting.GetCompletionRatio(modelName))
	audioRatio := decimal.NewFromFloat(ratio_setting.GetAudioRatio(relayInfo.OriginModelName))
	audioCompletionRatio := decimal.NewFromFloat(ratio_setting.GetAudioCompletionRatio(modelName))

	modelRatio := relayInfo.PriceData.ModelRatio
	groupRatio := relayInfo.PriceData.GroupRatioInfo.GroupRatio
	modelPrice := relayInfo.PriceData.ModelPrice
	usePrice := relayInfo.PriceData.UsePrice

	quotaInfo := QuotaInfo{
		InputDetails: TokenDetails{
			TextTokens:  textInputTokens,
			AudioTokens: audioInputTokens,
		},
		OutputDetails: TokenDetails{
			TextTokens:  textOutTokens,
			AudioTokens: audioOutTokens,
		},
		ModelName: modelName,
		UsePrice:  usePrice,
		// ModelPrice 必须给:calculateAudioQuota 的 UsePrice 分支算的是
		// ModelPrice × QuotaPerUnit × GroupRatio。三个 QuotaInfo 字面量原先都漏了
		// 这一位,于是按次定价的音频/实时模型无条件算出 0 ——
		// 预扣了 $0.50、结算全额退回,一分钱收不到,而日志上还写着
		// 「模型价格 0.50」。这条路在 /v1/chat/completions(compatible_handler,
		// 只需 AudioTokens>0)、/v1/responses(gpt-4o-audio* 前缀,连 token 门都没有)
		// 与实时会话收尾三处都可达。
		ModelPrice: modelPrice,
		ModelRatio: modelRatio,
		GroupRatio: groupRatio,
	}
	quotaInfo.InputDetails = normalizeAudioTokenDetails(usage.InputTokens, quotaInfo.InputDetails)
	quotaInfo.OutputDetails = normalizeAudioTokenDetails(usage.OutputTokens, quotaInfo.OutputDetails)

	quota, clamp := calculateAudioQuota(quotaInfo)
	noteQuotaClamp(relayInfo, clamp)
	if tieredOk {
		quota = tieredQuota
	}

	totalTokens := usage.TotalTokens
	var logContent string
	if !usePrice {
		logContent = fmt.Sprintf("模型倍率 %.2f，补全倍率 %.2f，音频倍率 %.2f，音频补全倍率 %.2f，分组倍率 %.2f",
			modelRatio, completionRatio.InexactFloat64(), audioRatio.InexactFloat64(), audioCompletionRatio.InexactFloat64(), groupRatio)
	} else {
		logContent = fmt.Sprintf("模型价格 %.2f，分组倍率 %.2f", modelPrice, groupRatio)
	}

	// record all the consume log even if quota is 0
	if totalTokens == 0 {
		// in this case, must be some error happened
		// we cannot just return, because we may have to return the pre-consumed quota
		quota = 0
		logContent += "（可能是上游超时）"
		logger.LogError(ctx, fmt.Sprintf("total tokens is 0, cannot consume quota, userId %d, channelId %d, "+
			"tokenId %d, model %s， pre-consumed quota %d", relayInfo.UserId, relayInfo.ChannelId, relayInfo.TokenId, modelName, relayInfo.FinalPreConsumedQuota))
	} else {
		model.UpdateUserUsedQuotaAndRequestCount(relayInfo.UserId, quota)
		model.UpdateChannelUsedQuota(relayInfo.ChannelId, quota)
	}

	if err := SettleBilling(ctx, relayInfo, quota); err != nil {
		// 结算失败 ≠ 不收钱:请求已经跑完,只能继续往下把日志写完。但日志记的是
		// 全额真实花费,而这笔钱实际没收到 —— 标记必须落在这一行上,否则事后
		// 无从分辨"已收讫"与"记了没收到"。
		relayInfo.SettleFailure = err.Error()
		logger.LogError(ctx, "error settling billing: "+err.Error())
	}

	logModel := modelName
	if extraContent != "" {
		logContent += ", " + extraContent
	}
	other := GenerateWssOtherInfo(ctx, relayInfo, usage, modelRatio, groupRatio,
		completionRatio.InexactFloat64(), audioRatio.InexactFloat64(), audioCompletionRatio.InexactFloat64(), modelPrice, relayInfo.PriceData.GroupRatioInfo.GroupSpecialRatio)
	if tieredResult != nil {
		InjectTieredBillingInfo(other, relayInfo, tieredResult)
	}
	attachQuotaSaturation(ctx, relayInfo, other)
	attachSettleFailure(ctx, relayInfo, other)
	attachPreConsumeShortfall(ctx, relayInfo, other)
	attachGroupRatioFallback(ctx, relayInfo, other)
	model.RecordConsumeLog(ctx, relayInfo.UserId, model.RecordConsumeLogParams{
		ChannelId:        relayInfo.ChannelId,
		PromptTokens:     usage.InputTokens,
		CompletionTokens: usage.OutputTokens,
		ModelName:        logModel,
		TokenName:        tokenName,
		Quota:            quota,
		Content:          logContent,
		TokenId:          relayInfo.TokenId,
		UseTimeSeconds:   int(useTimeSeconds),
		IsStream:         relayInfo.IsStream,
		Group:            relayInfo.UsingGroup,
		Other:            other,
	})
}

func CalcOpenRouterCacheCreateTokens(usage dto.Usage, priceData types.PriceData) int {
	if priceData.CacheCreationRatio == 1 {
		return 0
	}
	quotaPrice := priceData.ModelRatio / common.QuotaPerUnit
	promptCacheCreatePrice := quotaPrice * priceData.CacheCreationRatio
	promptCacheReadPrice := quotaPrice * priceData.CacheRatio
	completionPrice := quotaPrice * priceData.CompletionRatio

	// 除数为 0 时表达式产出 ±Inf / NaN,裸 int() 在 amd64 上会得到 INT64_MIN。
	// ModelRatio 配成 0(免费模型)且 CacheCreationRatio != 1 就是这个组合。
	denominator := promptCacheCreatePrice - quotaPrice
	if denominator == 0 {
		return 0
	}

	cost, _ := usage.Cost.(float64)
	totalPromptTokens := float64(usage.PromptTokens)
	completionTokens := float64(usage.CompletionTokens)
	promptCacheReadTokens := float64(usage.PromptTokensDetails.CachedTokens)

	// usage.Cost 是**上游可控**的:一个畸形 cost 足以把这个商推到 ±Inf。
	// 按 AGENTS.md 的计费安全不变量,无界输入一律走 common/quota_math.go 的
	// 饱和转换,不得用裸 int(math.Round(...)) —— 调用方现在恰好夹住了负的巨值,
	// 但那道夹子在调用方而不在转换处,新增第二个调用点就会漏。
	return common.QuotaRound((cost -
		totalPromptTokens*quotaPrice +
		promptCacheReadTokens*(quotaPrice-promptCacheReadPrice) -
		completionTokens*completionPrice) /
		denominator)
}

func PostAudioConsumeQuota(ctx *gin.Context, relayInfo *relaycommon.RelayInfo, usage *dto.Usage, extraContent string) {

	var tieredUsedVars map[string]bool
	if snap := relayInfo.TieredBillingSnapshot; snap != nil {
		tieredUsedVars = billingexpr.UsedVars(snap.ExprString)
	}
	var tieredResult *billingexpr.TieredResult
	tieredOk, tieredQuota, tieredRes := TryTieredSettle(relayInfo, BuildTieredTokenParams(usage, false, tieredUsedVars))
	if tieredOk {
		tieredResult = tieredRes
	}

	useTimeSeconds := time.Now().Unix() - relayInfo.StartTime.Unix()
	textInputTokens := usage.PromptTokensDetails.TextTokens
	textOutTokens := usage.CompletionTokenDetails.TextTokens

	audioInputTokens := usage.PromptTokensDetails.AudioTokens
	audioOutTokens := usage.CompletionTokenDetails.AudioTokens

	tokenName := ctx.GetString("token_name")
	completionRatio := decimal.NewFromFloat(ratio_setting.GetCompletionRatio(relayInfo.OriginModelName))
	audioRatio := decimal.NewFromFloat(ratio_setting.GetAudioRatio(relayInfo.OriginModelName))
	audioCompletionRatio := decimal.NewFromFloat(ratio_setting.GetAudioCompletionRatio(relayInfo.OriginModelName))

	modelRatio := relayInfo.PriceData.ModelRatio
	groupRatio := relayInfo.PriceData.GroupRatioInfo.GroupRatio
	modelPrice := relayInfo.PriceData.ModelPrice
	usePrice := relayInfo.PriceData.UsePrice

	quotaInfo := QuotaInfo{
		InputDetails: TokenDetails{
			TextTokens:  textInputTokens,
			AudioTokens: audioInputTokens,
		},
		OutputDetails: TokenDetails{
			TextTokens:  textOutTokens,
			AudioTokens: audioOutTokens,
		},
		ModelName: relayInfo.OriginModelName,
		UsePrice:  usePrice,
		// 与 PostWssConsumeQuota 同理:漏了 ModelPrice 就是按次定价恒收 0。
		ModelPrice: modelPrice,
		ModelRatio: modelRatio,
		GroupRatio: groupRatio,
	}
	quotaInfo.InputDetails = normalizeAudioTokenDetails(usage.PromptTokens, quotaInfo.InputDetails)
	quotaInfo.OutputDetails = normalizeAudioTokenDetails(usage.CompletionTokens, quotaInfo.OutputDetails)

	quota, clamp := calculateAudioQuota(quotaInfo)
	noteQuotaClamp(relayInfo, clamp)
	if tieredOk {
		quota = tieredQuota
	}

	totalTokens := usage.TotalTokens
	var logContent string
	if !usePrice {
		logContent = fmt.Sprintf("模型倍率 %.2f，补全倍率 %.2f，音频倍率 %.2f，音频补全倍率 %.2f，分组倍率 %.2f",
			modelRatio, completionRatio.InexactFloat64(), audioRatio.InexactFloat64(), audioCompletionRatio.InexactFloat64(), groupRatio)
	} else {
		logContent = fmt.Sprintf("模型价格 %.2f，分组倍率 %.2f", modelPrice, groupRatio)
	}

	// record all the consume log even if quota is 0
	if totalTokens == 0 {
		// in this case, must be some error happened
		// we cannot just return, because we may have to return the pre-consumed quota
		quota = 0
		logContent += "（可能是上游超时）"
		logger.LogError(ctx, fmt.Sprintf("total tokens is 0, cannot consume quota, userId %d, channelId %d, "+
			"tokenId %d, model %s， pre-consumed quota %d", relayInfo.UserId, relayInfo.ChannelId, relayInfo.TokenId, relayInfo.OriginModelName, relayInfo.FinalPreConsumedQuota))
	} else {
		model.UpdateUserUsedQuotaAndRequestCount(relayInfo.UserId, quota)
		model.UpdateChannelUsedQuota(relayInfo.ChannelId, quota)
	}

	if err := SettleBilling(ctx, relayInfo, quota); err != nil {
		// 结算失败 ≠ 不收钱:请求已经跑完,只能继续往下把日志写完。但日志记的是
		// 全额真实花费,而这笔钱实际没收到 —— 标记必须落在这一行上,否则事后
		// 无从分辨"已收讫"与"记了没收到"。
		relayInfo.SettleFailure = err.Error()
		logger.LogError(ctx, "error settling billing: "+err.Error())
	}

	logModel := relayInfo.OriginModelName
	if extraContent != "" {
		logContent += ", " + extraContent
	}
	other := GenerateAudioOtherInfo(ctx, relayInfo, usage, modelRatio, groupRatio,
		completionRatio.InexactFloat64(), audioRatio.InexactFloat64(), audioCompletionRatio.InexactFloat64(), modelPrice, relayInfo.PriceData.GroupRatioInfo.GroupSpecialRatio)
	if tieredResult != nil {
		InjectTieredBillingInfo(other, relayInfo, tieredResult)
	}
	attachQuotaSaturation(ctx, relayInfo, other)
	attachSettleFailure(ctx, relayInfo, other)
	attachPreConsumeShortfall(ctx, relayInfo, other)
	attachGroupRatioFallback(ctx, relayInfo, other)
	model.RecordConsumeLog(ctx, relayInfo.UserId, model.RecordConsumeLogParams{
		ChannelId:        relayInfo.ChannelId,
		PromptTokens:     usage.PromptTokens,
		CompletionTokens: usage.CompletionTokens,
		ModelName:        logModel,
		TokenName:        tokenName,
		Quota:            quota,
		Content:          logContent,
		TokenId:          relayInfo.TokenId,
		UseTimeSeconds:   int(useTimeSeconds),
		IsStream:         relayInfo.IsStream,
		Group:            relayInfo.UsingGroup,
		Other:            other,
	})
	gopool.Go(func() {
		perfmetrics.RecordRelaySample(relayInfo, true, int64(usage.CompletionTokens))
	})
}

func PreConsumeTokenQuota(relayInfo *relaycommon.RelayInfo, quota int) error {
	if quota < 0 {
		return errors.New("quota 不能为负数！")
	}
	if relayInfo.IsPlayground {
		return nil
	}
	// 原子预扣：检查与扣减在同一操作中完成，并发请求不可能同时通过检查后超扣。
	reserved, err := model.TryReserveTokenQuota(relayInfo.TokenId, relayInfo.TokenKey, quota, relayInfo.TokenUnlimited)
	if err != nil {
		return err
	}
	if !reserved {
		remainQuota := 0
		if token, tokenErr := model.GetTokenByKey(relayInfo.TokenKey, false); tokenErr == nil && token != nil {
			remainQuota = token.RemainQuota
		}
		return fmt.Errorf("token quota is not enough, token remain quota: %s, need quota: %s", logger.FormatQuota(remainQuota), logger.FormatQuota(quota))
	}
	return nil
}

func PostConsumeQuota(relayInfo *relaycommon.RelayInfo, quota int, preConsumedQuota int, sendEmail bool) (err error) {

	// 1) Consume from wallet quota OR subscription item
	if relayInfo != nil && relayInfo.BillingSource == BillingSourceSubscription {
		if relayInfo.SubscriptionId == 0 {
			return errors.New("subscription id is missing")
		}
		delta := int64(quota)
		if delta != 0 {
			// 与 SubscriptionFunding.Settle 同一份实现：套餐撞到 amount_total
			// 上限时只吃下能吃的部分，剩余差额按钱包出资闸门的裁决落到钱包或
			// 被核销，绝不静默丢弃。
			split, err := settleSubscriptionDelta(
				relayInfo.UserId, relayInfo.UserGroup, relayInfo.UsingGroup,
				relayInfo.SubscriptionId, delta)
			if err != nil {
				return err
			}
			relayInfo.SubscriptionPostDelta += split.Applied
			relayInfo.SubscriptionWalletShortfall += split.WalletCharged
			relayInfo.SubscriptionWrittenOff += split.WrittenOff
		}
	} else {
		// Wallet
		if quota > 0 {
			err = model.DecreaseUserQuota(relayInfo.UserId, quota, false)
		} else {
			err = model.IncreaseUserQuota(relayInfo.UserId, -quota, false)
		}
		if err != nil {
			return err
		}
	}

	if !relayInfo.IsPlayground {
		if quota > 0 {
			err = model.DecreaseTokenQuota(relayInfo.TokenId, relayInfo.TokenKey, quota)
		} else {
			err = model.IncreaseTokenQuota(relayInfo.TokenId, relayInfo.TokenKey, -quota)
		}
		if err != nil {
			return err
		}
	}

	if sendEmail {
		if (quota + preConsumedQuota) != 0 {
			checkAndSendQuotaNotify(relayInfo, quota, preConsumedQuota)
		}
	}

	return nil
}

func checkAndSendQuotaNotify(relayInfo *relaycommon.RelayInfo, quota int, preConsumedQuota int) {
	gopool.Go(func() {
		userSetting := relayInfo.UserSetting
		threshold := common.QuotaRemindThreshold
		if userSetting.QuotaWarningThreshold != 0 {
			threshold = int(userSetting.QuotaWarningThreshold)
		}

		//noMoreQuota := userCache.Quota-(quota+preConsumedQuota) <= 0
		quotaTooLow := false
		consumeQuota := quota + preConsumedQuota
		if relayInfo.UserQuota-consumeQuota < threshold {
			quotaTooLow = true
		}
		if quotaTooLow {
			prompt := "您的额度即将用尽"
			topUpLink := PaymentReturnURL("/wallet")

			// 根据通知方式生成不同的内容格式
			var content string
			var values []interface{}

			notifyType := userSetting.NotifyType
			if notifyType == "" {
				notifyType = dto.NotifyTypeEmail
			}

			if notifyType == dto.NotifyTypeBark {
				// Bark推送使用简短文本，不支持HTML
				content = "{{value}}，剩余额度：{{value}}，请及时充值"
				values = []interface{}{prompt, logger.FormatQuota(relayInfo.UserQuota)}
			} else if notifyType == dto.NotifyTypeGotify {
				content = "{{value}}，当前剩余额度为 {{value}}，请及时充值。"
				values = []interface{}{prompt, logger.FormatQuota(relayInfo.UserQuota)}
			} else {
				// 默认内容格式，适用于Email和Webhook（支持HTML）
				content = "{{value}}，当前剩余额度为 {{value}}，为了不影响您的使用，请及时充值。<br/>充值链接：<a href='{{value}}'>{{value}}</a>"
				values = []interface{}{prompt, logger.FormatQuota(relayInfo.UserQuota), topUpLink, topUpLink}
			}

			err := NotifyUser(relayInfo.UserId, relayInfo.UserEmail, relayInfo.UserSetting, dto.NewNotify(dto.NotifyTypeQuotaExceed, prompt, content, values))
			if err != nil {
				common.SysError(fmt.Sprintf("failed to send quota notify to user %d: %s", relayInfo.UserId, err.Error()))
			}
		}
	})
}

func checkAndSendSubscriptionQuotaNotify(relayInfo *relaycommon.RelayInfo) {
	gopool.Go(func() {
		if relayInfo == nil {
			return
		}
		if relayInfo.SubscriptionId == 0 || relayInfo.SubscriptionAmountTotal <= 0 {
			return
		}

		userSetting := relayInfo.UserSetting
		threshold := common.QuotaRemindThreshold
		if userSetting.QuotaWarningThreshold != 0 {
			threshold = int(userSetting.QuotaWarningThreshold)
		}

		usedAfter := relayInfo.SubscriptionAmountUsedAfterPreConsume + relayInfo.SubscriptionPostDelta
		remaining := relayInfo.SubscriptionAmountTotal - usedAfter
		if remaining >= int64(threshold) {
			return
		}

		prompt := "您的订阅额度即将用尽"
		topUpLink := PaymentReturnURL("/wallet")

		var content string
		var values []interface{}
		notifyType := userSetting.NotifyType
		if notifyType == "" {
			notifyType = dto.NotifyTypeEmail
		}

		if notifyType == dto.NotifyTypeBark {
			content = "{{value}}，剩余额度：{{value}}，请及时充值"
			values = []interface{}{prompt, logger.FormatQuota(int(remaining))}
		} else if notifyType == dto.NotifyTypeGotify {
			content = "{{value}}，当前剩余额度为 {{value}}，请及时充值。"
			values = []interface{}{prompt, logger.FormatQuota(int(remaining))}
		} else {
			content = "{{value}}，当前剩余额度为 {{value}}，为了不影响您的使用，请及时充值。<br/>充值链接：<a href='{{value}}'>{{value}}</a>"
			values = []interface{}{prompt, logger.FormatQuota(int(remaining)), topUpLink, topUpLink}
		}

		if err := NotifyUser(relayInfo.UserId, relayInfo.UserEmail, relayInfo.UserSetting, dto.NewNotify(dto.NotifyTypeQuotaExceed, prompt, content, values)); err != nil {
			common.SysError(fmt.Sprintf("failed to send subscription quota notify to user %d: %s", relayInfo.UserId, err.Error()))
		}
	})
}
