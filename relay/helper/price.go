package helper

import (
	"fmt"
	"strings"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/logger"
	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/pkg/billingexpr"
	relaycommon "github.com/QuantumNous/new-api/relay/common"
	"github.com/QuantumNous/new-api/relaykit/types"
	"github.com/QuantumNous/new-api/setting/billing_setting"
	"github.com/QuantumNous/new-api/setting/operation_setting"
	"github.com/QuantumNous/new-api/setting/ratio_setting"
	hosttypes "github.com/QuantumNous/new-api/types"

	"github.com/gin-gonic/gin"
)

func modelPriceNotConfiguredError(modelName string, userId int) error {
	if model.IsAdmin(userId) {
		return fmt.Errorf(
			"模型 %s 的价格未配置。请前往「系统设置 → 运营设置」开启自用模式，或在「系统设置 → 分组与模型定价设置」中为该模型配置价格；"+
				"Model %s price not configured. Go to System Settings → Operation Settings to enable self-use mode, or configure the model price in System Settings → Group & Model Pricing.",
			modelName, modelName,
		)
	}
	return fmt.Errorf(
		"模型 %s 的价格尚未由管理员配置，暂时无法使用，请联系站点管理员开启该模型；"+
			"Model %s has not been priced by the administrator yet. Please contact the site administrator to enable this model.",
		modelName, modelName,
	)
}

// https://docs.claude.com/en/docs/build-with-claude/prompt-caching#1-hour-cache-duration
const claudeCacheCreation1hMultiplier = 6 / 3.75

// defaultPreConsumeMaxTokens 是客户端省略 max_tokens 时,预扣费对输出侧使用的
// 兜底上限。
//
// OpenAI 协议里 max_tokens 是可选的,省略是**默认用法**而不是异常输入。省略时
// 若对输出一个 token 都不预留,预扣额就只覆盖输入侧,而结算按
// `prompt + completion * CompletionRatio` 无条件扣款(见 service/text_quota.go
// 与 service/funding_source.go 的正差额分支),差额直接把余额扣成负数:实测
// gemini-3-flash(ModelRatio 0.6 / CompletionRatio 5)一次请求就能把 3100 额度
// 的钱包打到 -11621,三路并发打到 -29476,而这一切与余额多少无关。
//
// 阶梯计价(tiered_expr)一开始就有这条兜底,倍率计价没有 —— 两条路径同一件事
// 用同一个数,不再各留各的缺口。
const defaultPreConsumeMaxTokens = 8192

// HandleGroupRatio checks for "auto_group" in the context and updates the group ratio and relayInfo.UsingGroup if present
func HandleGroupRatio(ctx *gin.Context, relayInfo *relaycommon.RelayInfo) hosttypes.GroupRatioInfo {
	groupRatioInfo := hosttypes.GroupRatioInfo{
		GroupRatio:        1.0, // default ratio
		GroupSpecialRatio: -1,
	}

	// check auto group
	autoGroup, exists := ctx.Get("auto_group")
	if exists {
		logger.LogDebug(ctx, "final group: %s", autoGroup)
		relayInfo.UsingGroup = autoGroup.(string)
	}

	// 分组倍率解析走全仓唯一的 ratio_setting.ResolveGroupRatio,不再在这里
	// 重复一遍 if:同一段判据原本有三份拷贝(本处 / service/quota.go /
	// service/task_billing.go),任何新增判据写三遍必然漂移。
	res := ratio_setting.ResolveGroupRatio(relayInfo.UserGroup, relayInfo.UsingGroup)
	groupRatioInfo.GroupRatio = res.Ratio
	if res.Source == ratio_setting.GroupRatioSourceOverride {
		groupRatioInfo.GroupSpecialRatio = res.Ratio
		groupRatioInfo.HasSpecialRatio = true
	}
	relayInfo.NoteGroupRatioFallback(res)

	return groupRatioInfo
}

// preConsumeTokenEstimate returns the token-equivalent basis for a per-token
// pre-consume, in the same units the settlement formula uses.
//
// Settlement charges `prompt + completion * CompletionRatio` (see
// service/text_quota.go). The pre-consume estimate must use the *same* shape,
// otherwise the reservation systematically under-covers the request and the
// difference is force-collected at settle time, driving the balance negative
// on a single request. With CompletionRatio = 5 the output side used to be
// covered at 20%: a wallet holding 4,000 quota passed a 3,900 reservation and
// settled at 17,385, ending at -13,385.
//
// A non-positive CompletionRatio contributes nothing (settlement charges
// nothing for output in that case either); it is never allowed to shrink the
// prompt-side reservation.
//
// ═════════════════ 拍过板的取舍:预扣是估算,结算无下限 ═════════════════
//
// **这个函数只负责"估得像不像",不负责"扣得住扣不住"。** 估不足的部分会在结算时
// 无条件补收,而补收**允许把余额扣成负数** —— 这不是漏洞,是项目方两次拍过板的
// 取舍。原话:
//
//	「我看很多 AI 平台都不会有上限的,高并发的情况下都会有透支的吧。
//	  性能和准确,高并发环境下只能先考虑性能。」(2026-08-19)
//
// 更早的一次(2026-08-10)撤回过一整轮"把预扣改成同步条件原子写"的改动,
// 因为那让有余额的用户被 403 误拒;上游 issue #5690 报过同一批问题,维护者关成
// NOT_PLANNED,理由是「这是高并发下必须做的取舍」。
//
// 代价是明确的、已被量化的:8 路并发打一个只够 1 次的钱包 → 8/8 全过、余额 −140000;
// 弃号跑掉的欠款是净损失。运营侧的兜底不是代码,是
// GET /api/qy/admin/overdraft(谁欠、欠多少、合计多少)。
//
// **下一个想"修"它的人,先读 qianye/docs/decisions.md 的 D-01。**
// 本仓已经有过一次"后来的人不知道这是拍过板的、又改了一遍"的教训
// (预扣 TOCTOU + 批量队列,改完又整条撤回)。可以做的是让估算更准
// (本函数)、让透支更可见(那个端点、admin_info.pre_consume_shortfall);
// 不可以做的是在结算侧加下限、把补收改成会失败的操作、或者把批量更新队列关掉。
func preConsumeTokenEstimate(promptTokens int, maxTokens int, completionRatio float64) float64 {
	estimate := float64(common.Max(promptTokens, common.PreConsumedQuota))
	// max_tokens 缺省时用兜底上限,不能按 0 处理:见 defaultPreConsumeMaxTokens。
	if maxTokens <= 0 {
		maxTokens = defaultPreConsumeMaxTokens
	}
	if completionRatio > 0 {
		estimate += float64(maxTokens) * completionRatio
	}
	return estimate
}

func ModelPriceHelper(c *gin.Context, info *relaycommon.RelayInfo, promptTokens int, meta *types.TokenCountMeta) (hosttypes.PriceData, error) {
	modelPrice, usePrice := ratio_setting.GetModelPrice(info.OriginModelName, false)

	groupRatioInfo := HandleGroupRatio(c, info)
	modelPrice, usePrice = QyGroupModelPrice(info, modelPrice, usePrice)

	// Check if this model uses tiered_expr billing
	if billing_setting.GetBillingMode(info.OriginModelName) == billing_setting.BillingModeTieredExpr {
		return modelPriceHelperTiered(c, info, promptTokens, meta, groupRatioInfo)
	}

	var preConsumedQuota int
	var modelRatio float64
	var completionRatio float64
	var cacheRatio float64
	var imageRatio float64
	var cacheCreationRatio float64
	var cacheCreationRatio5m float64
	var cacheCreationRatio1h float64
	var audioRatio float64
	var audioCompletionRatio float64
	var freeModel bool
	if !usePrice {
		var success bool
		var matchName string
		modelRatio, success, matchName = ratio_setting.GetModelRatio(info.OriginModelName)
		modelRatio, success = QyGroupModelRatio(info, modelRatio, success)
		if !success {
			acceptUnsetRatio := false
			if info.UserSetting.AcceptUnsetRatioModel {
				acceptUnsetRatio = true
			}
			if !acceptUnsetRatio {
				return hosttypes.PriceData{}, modelPriceNotConfiguredError(matchName, info.UserId)
			}
		}
		completionRatio = ratio_setting.GetCompletionRatio(info.OriginModelName)
		cacheRatio, _ = ratio_setting.GetCacheRatio(info.OriginModelName)
		cacheCreationRatio, _ = ratio_setting.GetCreateCacheRatio(info.OriginModelName)
		cacheCreationRatio5m = cacheCreationRatio
		// 固定1h和5min缓存写入价格的比例
		cacheCreationRatio1h = cacheCreationRatio * claudeCacheCreation1hMultiplier
		imageRatio, _ = ratio_setting.GetImageRatio(info.OriginModelName)
		audioRatio = ratio_setting.GetAudioRatio(info.OriginModelName)
		audioCompletionRatio = ratio_setting.GetAudioCompletionRatio(info.OriginModelName)
		ratio := modelRatio * groupRatioInfo.GroupRatio
		quota, err := common.QuotaFromFloatStrict(preConsumeTokenEstimate(promptTokens, meta.MaxTokens, completionRatio) * ratio)
		if err != nil {
			return hosttypes.PriceData{}, err
		}
		preConsumedQuota = quota
	} else {
		if meta.ImagePriceRatio != 0 {
			modelPrice = modelPrice * meta.ImagePriceRatio
		}
	}

	// check if free model pre-consume is disabled
	if !operation_setting.GetQuotaSetting().EnableFreeModelPreConsume {
		// if model price or ratio is 0, do not pre-consume quota
		if groupRatioInfo.GroupRatio == 0 {
			preConsumedQuota = 0
			freeModel = true
		} else if usePrice {
			if modelPrice == 0 {
				preConsumedQuota = 0
				freeModel = true
			}
		} else {
			if modelRatio == 0 {
				preConsumedQuota = 0
				freeModel = true
			}
		}
	}

	priceData := hosttypes.PriceData{
		FreeModel:            freeModel,
		ModelPrice:           modelPrice,
		ModelRatio:           modelRatio,
		CompletionRatio:      completionRatio,
		GroupRatioInfo:       groupRatioInfo,
		UsePrice:             usePrice,
		CacheRatio:           cacheRatio,
		ImageRatio:           imageRatio,
		AudioRatio:           audioRatio,
		AudioCompletionRatio: audioCompletionRatio,
		CacheCreationRatio:   cacheCreationRatio,
		CacheCreation5mRatio: cacheCreationRatio5m,
		CacheCreation1hRatio: cacheCreationRatio1h,
		QuotaToPreConsume:    preConsumedQuota,
	}
	// 请求形状决定的计费倍率（图片张数 n、dall-e 的 size×quality）与“按次还是
	// 按量”无关：一次 n=10 的请求就是真的生了十张图。它们原先整段写在
	// `if usePrice` 里，于是把图片模型按 ModelRatio（开箱默认就把 gpt-image-1
	// 放在这条路上：defaultModelRatio 有它、defaultModelPrice 没有）定价时，n 在
	// 预扣与结算两侧都被丢掉 —— 再叠上图片处理器在上游不回 usage 时把
	// TotalTokens 强设为 1，一次 n=128 的请求与 n=1 收同样的钱，而那点钱等于
	// 一个 token（实测：同一个 mock 上游、同样 128 张图，按次 16,000,000 vs
	// 按倍率 3）。
	//
	// 结算侧本来就两条路都调 ApplyOtherRatios（service/text_quota.go:487/499），
	// 真正的断点只是“n 从来没被放进去”。
	for name, ratio := range meta.BillingRatios {
		priceData.AddOtherRatio(name, ratio)
	}
	if usePrice {
		quotaToPreConsume := priceData.ApplyOtherRatiosToFloat(modelPrice * common.QuotaPerUnit * groupRatioInfo.GroupRatio)
		quota, err := common.QuotaFromFloatStrict(quotaToPreConsume)
		if err != nil {
			return hosttypes.PriceData{}, err
		}
		priceData.QuotaToPreConsume = quota
	} else {
		// 按量路径上 size×quality 无处可放（它在按次路径上是乘进 modelPrice 的），
		// 放进 OtherRatios 才能同时到达预扣与结算两侧。名字与 n 分开，否则图片
		// 处理器事后按真实张数覆盖 n 时会把尺寸倍率一并抹掉。
		if meta.ImagePriceRatio != 0 {
			priceData.AddOtherRatio("image_size_quality", meta.ImagePriceRatio)
		}
		if len(priceData.OtherRatios()) > 0 {
			quota, err := common.QuotaFromFloatStrict(
				priceData.ApplyOtherRatiosToFloat(float64(priceData.QuotaToPreConsume)))
			if err != nil {
				return hosttypes.PriceData{}, err
			}
			priceData.QuotaToPreConsume = quota
		}
	}

	if common.DebugEnabled {
		logger.LogDebug(c, "model_price_helper result: %s", priceData.ToSetting())
	}
	info.PriceData = priceData
	return priceData, nil
}

// ModelPriceHelperPerCall 按次/按量计费的 PriceHelper (MJ、Task)
func ModelPriceHelperPerCall(c *gin.Context, info *relaycommon.RelayInfo) (hosttypes.PriceData, error) {
	groupRatioInfo := HandleGroupRatio(c, info)

	modelPrice, success := ratio_setting.GetModelPrice(info.OriginModelName, true)
	modelPrice, success = QyGroupModelPrice(info, modelPrice, success)
	usePrice := success
	var modelRatio float64

	if !success {
		defaultPrice, ok := ratio_setting.GetDefaultModelPriceMap()[info.OriginModelName]
		if ok {
			modelPrice = defaultPrice
			usePrice = true
		} else {
			var ratioSuccess bool
			var matchName string
			modelRatio, ratioSuccess, matchName = ratio_setting.GetModelRatio(info.OriginModelName)
			modelRatio, ratioSuccess = QyGroupModelRatio(info, modelRatio, ratioSuccess)
			acceptUnsetRatio := false
			if info.UserSetting.AcceptUnsetRatioModel {
				acceptUnsetRatio = true
			}
			if !ratioSuccess && !acceptUnsetRatio {
				return hosttypes.PriceData{}, modelPriceNotConfiguredError(matchName, info.UserId)
			}
		}
	}

	var quota int
	freeModel := false

	if usePrice {
		var err error
		quota, err = common.QuotaFromFloatStrict(modelPrice * common.QuotaPerUnit * groupRatioInfo.GroupRatio)
		if err != nil {
			return hosttypes.PriceData{}, err
		}
		if !operation_setting.GetQuotaSetting().EnableFreeModelPreConsume {
			if groupRatioInfo.GroupRatio == 0 || modelPrice == 0 {
				quota = 0
				freeModel = true
			}
		}
	} else {
		// 按量计费：以模型倍率的一半作为预扣额度
		var err error
		quota, err = common.QuotaFromFloatStrict(modelRatio / 2 * common.QuotaPerUnit * groupRatioInfo.GroupRatio)
		if err != nil {
			return hosttypes.PriceData{}, err
		}
		modelPrice = -1
		if !operation_setting.GetQuotaSetting().EnableFreeModelPreConsume {
			if groupRatioInfo.GroupRatio == 0 || modelRatio == 0 {
				quota = 0
				freeModel = true
			}
		}
	}

	priceData := hosttypes.PriceData{
		FreeModel:      freeModel,
		ModelPrice:     modelPrice,
		ModelRatio:     modelRatio,
		UsePrice:       usePrice,
		Quota:          quota,
		GroupRatioInfo: groupRatioInfo,
	}
	return priceData, nil
}

func HasModelBillingConfig(modelName string) bool {
	if _, ok := ratio_setting.GetModelPrice(modelName, false); ok {
		return true
	}
	if _, ok, _ := ratio_setting.GetModelRatio(modelName); ok {
		return true
	}
	if billing_setting.GetBillingMode(modelName) != billing_setting.BillingModeTieredExpr {
		return false
	}
	expr, ok := billing_setting.GetBillingExpr(modelName)
	return ok && strings.TrimSpace(expr) != ""
}

func modelPriceHelperTiered(c *gin.Context, info *relaycommon.RelayInfo, promptTokens int, meta *types.TokenCountMeta, groupRatioInfo hosttypes.GroupRatioInfo) (hosttypes.PriceData, error) {
	exprStr, ok := billing_setting.GetBillingExpr(info.OriginModelName)
	if !ok {
		return hosttypes.PriceData{}, fmt.Errorf("model %s is configured as tiered_expr but has no billing expression", info.OriginModelName)
	}

	estimatedCompletionTokens := meta.MaxTokens
	if estimatedCompletionTokens == 0 && groupRatioInfo.GroupRatio != 0 {
		estimatedCompletionTokens = defaultPreConsumeMaxTokens
	}

	requestInput, err := ResolveIncomingBillingExprRequestInput(c, info)
	if err != nil {
		return hosttypes.PriceData{}, err
	}

	rawCost, trace, err := billingexpr.RunExprWithRequest(exprStr, billingexpr.TokenParams{
		P:   float64(promptTokens),
		C:   float64(estimatedCompletionTokens),
		Len: float64(promptTokens),
	}, requestInput)
	if err != nil {
		return hosttypes.PriceData{}, fmt.Errorf("model %s tiered expr run failed: %w", info.OriginModelName, err)
	}

	// Expression coefficients are $/1M tokens prices; convert to quota the same way per-call billing does.
	quotaBeforeGroup := rawCost / 1_000_000 * common.QuotaPerUnit
	// 分组级乘数是**当前分组**的属性,和分组倍率一样会随 auto 重试切组而变,
	// 因此它只作用在本次预扣的金额上,绝不能写进快照:
	// service.refreshTieredBillingGroup 会拿快照里的 before-group 值重算预留额,
	// 快照里若存的是已乘乘数的值,切到新分组后原分组的乘数会被带进新分组的预留额。
	// 快照存未乘任何分组因子的原始表达式结果,两侧各按当前分组现算乘数。
	quotaWithGroupMultiplier := QyGroupTieredQuota(info, quotaBeforeGroup)
	preConsumedQuota, err := billingexpr.QuotaRoundStrict(quotaWithGroupMultiplier * groupRatioInfo.GroupRatio)
	if err != nil {
		return hosttypes.PriceData{}, err
	}

	freeModel := false
	if !operation_setting.GetQuotaSetting().EnableFreeModelPreConsume {
		if groupRatioInfo.GroupRatio == 0 {
			preConsumedQuota = 0
			freeModel = true
		}
	}

	exprHash := billingexpr.ExprHashString(exprStr)
	snapshot := &billingexpr.BillingSnapshot{
		BillingMode:               billing_setting.BillingModeTieredExpr,
		ModelName:                 info.OriginModelName,
		ExprString:                exprStr,
		ExprHash:                  exprHash,
		GroupRatio:                groupRatioInfo.GroupRatio,
		EstimatedPromptTokens:     promptTokens,
		EstimatedCompletionTokens: estimatedCompletionTokens,
		EstimatedQuotaBeforeGroup: quotaBeforeGroup,
		EstimatedQuotaAfterGroup:  preConsumedQuota,
		EstimatedTier:             trace.MatchedTier,
		QuotaPerUnit:              common.QuotaPerUnit,
		ExprVersion:               billingexpr.ExprVersion(exprStr),
	}
	info.TieredBillingSnapshot = snapshot
	info.BillingRequestInput = &requestInput

	priceData := hosttypes.PriceData{
		FreeModel:         freeModel,
		GroupRatioInfo:    groupRatioInfo,
		QuotaToPreConsume: preConsumedQuota,
	}

	logger.LogDebug(c, "model_price_helper_tiered result: model=%s preConsume=%d quotaBeforeGroup=%.2f groupMultiplied=%.2f groupRatio=%.2f tier=%s", info.OriginModelName, preConsumedQuota, quotaBeforeGroup, quotaWithGroupMultiplier, groupRatioInfo.GroupRatio, trace.MatchedTier)

	info.PriceData = priceData
	return priceData, nil
}
