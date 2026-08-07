package service

import (
	"context"
	"fmt"
	"strings"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/constant"
	"github.com/QuantumNous/new-api/logger"
	"github.com/QuantumNous/new-api/model"
	relaycommon "github.com/QuantumNous/new-api/relay/common"
	"github.com/QuantumNous/new-api/setting/ratio_setting"
	"github.com/QuantumNous/new-api/types"
	"github.com/gin-gonic/gin"
)

// LogTaskConsumption 记录任务消费日志和统计信息（仅记录，不涉及实际扣费）。
// 实际扣费已由 BillingSession（PreConsumeBilling + SettleBilling）完成。
func LogTaskConsumption(c *gin.Context, info *relaycommon.RelayInfo) {
	tokenName := c.GetString("token_name")
	logContent := fmt.Sprintf("操作 %s", info.Action)
	// 支持任务仅按次计费
	if common.StringsContains(constant.TaskPricePatches, info.OriginModelName) {
		logContent = fmt.Sprintf("%s，按次计费", logContent)
	} else {
		if otherRatios := info.PriceData.OtherRatios(); len(otherRatios) > 0 {
			var contents []string
			for key, ra := range otherRatios {
				if 1.0 != ra {
					contents = append(contents, fmt.Sprintf("%s: %.2f", key, ra))
				}
			}
			if len(contents) > 0 {
				logContent = fmt.Sprintf("%s, 计算参数：%s", logContent, strings.Join(contents, ", "))
			}
		}
	}
	other := make(map[string]interface{})
	other["is_task"] = true
	other["request_path"] = c.Request.URL.Path
	other["model_price"] = info.PriceData.ModelPrice
	if info.PriceData.ModelRatio > 0 {
		other["model_ratio"] = info.PriceData.ModelRatio
	}
	other["group_ratio"] = info.PriceData.GroupRatioInfo.GroupRatio
	if info.PriceData.GroupRatioInfo.HasSpecialRatio {
		other["user_group_ratio"] = info.PriceData.GroupRatioInfo.GroupSpecialRatio
	}
	if info.IsModelMapped {
		other["is_model_mapped"] = true
		other["upstream_model_name"] = info.UpstreamModelName
	}
	attachQuotaSaturation(c, info, other)
	attachGroupRatioFallback(c, info, other)
	model.RecordConsumeLog(c, info.UserId, model.RecordConsumeLogParams{
		ChannelId: info.ChannelId,
		ModelName: info.OriginModelName,
		TokenName: tokenName,
		Quota:     info.PriceData.Quota,
		Content:   logContent,
		TokenId:   info.TokenId,
		Group:     info.UsingGroup,
		Other:     other,
	})
	model.UpdateUserUsedQuotaAndRequestCount(info.UserId, info.PriceData.Quota)
	model.UpdateChannelUsedQuota(info.ChannelId, info.PriceData.Quota)
}

// ---------------------------------------------------------------------------
// 异步任务计费辅助函数
// ---------------------------------------------------------------------------

// resolveTokenKey 通过 TokenId 运行时获取令牌 Key（用于 Redis 缓存操作）。
// 如果令牌已被删除或查询失败，返回空字符串。
func resolveTokenKey(ctx context.Context, tokenId int, taskID string) string {
	token, err := model.GetTokenById(tokenId)
	if err != nil {
		logger.LogWarn(ctx, fmt.Sprintf("获取令牌 key 失败 (tokenId=%d, task=%s): %s", tokenId, taskID, err.Error()))
		return ""
	}
	return token.Key
}

// taskIsSubscription 判断任务是否通过订阅计费。
func taskIsSubscription(task *model.Task) bool {
	return task.PrivateData.BillingSource == BillingSourceSubscription && task.PrivateData.SubscriptionId > 0
}

// taskAdjustFunding 调整任务的资金来源（钱包或订阅），delta > 0 表示扣费，delta < 0 表示退还。
func taskAdjustFunding(task *model.Task, delta int) error {
	if taskIsSubscription(task) {
		return model.PostConsumeUserSubscriptionDelta(task.PrivateData.SubscriptionId, int64(delta))
	}
	if delta > 0 {
		return model.DecreaseUserQuota(task.UserId, delta, false)
	}
	return model.IncreaseUserQuota(task.UserId, -delta, false)
}

// taskAdjustTokenQuota 调整任务的令牌额度，delta > 0 表示扣费，delta < 0 表示退还。
// 需要通过 resolveTokenKey 运行时获取 key（不从 PrivateData 中读取）。
func taskAdjustTokenQuota(ctx context.Context, task *model.Task, delta int) {
	if task.PrivateData.TokenId <= 0 || delta == 0 {
		return
	}
	tokenKey := resolveTokenKey(ctx, task.PrivateData.TokenId, task.TaskID)
	if tokenKey == "" {
		return
	}
	var err error
	if delta > 0 {
		err = model.DecreaseTokenQuota(task.PrivateData.TokenId, tokenKey, delta)
	} else {
		err = model.IncreaseTokenQuota(task.PrivateData.TokenId, tokenKey, -delta)
	}
	if err != nil {
		logger.LogWarn(ctx, fmt.Sprintf("调整令牌额度失败 (delta=%d, task=%s): %s", delta, task.TaskID, err.Error()))
	}
}

// taskBillingOther 从 task 的 BillingContext 构建日志 Other 字段。
func taskBillingOther(task *model.Task) map[string]interface{} {
	other := make(map[string]interface{})
	if bc := task.PrivateData.BillingContext; bc != nil {
		other["model_price"] = bc.ModelPrice
		if bc.ModelRatio > 0 {
			other["model_ratio"] = bc.ModelRatio
		}
		other["group_ratio"] = bc.GroupRatio
		if priceData := taskBillingContextPriceData(bc); priceData != nil {
			for k, v := range priceData.OtherRatios() {
				other[k] = v
			}
		}
	}
	props := task.Properties
	if props.UpstreamModelName != "" && props.UpstreamModelName != props.OriginModelName {
		other["is_model_mapped"] = true
		other["upstream_model_name"] = props.UpstreamModelName
	}
	return other
}

func taskBillingContextPriceData(bc *model.TaskBillingContext) *types.PriceData {
	if bc == nil || len(bc.OtherRatios) == 0 {
		return nil
	}
	priceData := &types.PriceData{}
	if !priceData.ReplaceOtherRatios(bc.OtherRatios) {
		return nil
	}
	return priceData
}

// taskModelName 从 BillingContext 或 Properties 中获取模型名称。
func taskModelName(task *model.Task) string {
	if bc := task.PrivateData.BillingContext; bc != nil && bc.OriginModelName != "" {
		return bc.OriginModelName
	}
	return task.Properties.OriginModelName
}

// RefundTaskQuota 统一的任务失败退款逻辑。
// 当异步任务失败时，将预扣的 quota 退还给用户（支持钱包和订阅），并退还令牌额度。
// 返回资金来源是否已成功退还；失败时保留 quota，供显式重试或人工对账。
func RefundTaskQuota(ctx context.Context, task *model.Task, reason string) bool {
	quota := task.Quota
	if quota == 0 {
		return true
	}

	// 1. 退还资金来源（钱包或订阅）
	if err := taskAdjustFunding(task, -quota); err != nil {
		logger.LogWarn(ctx, fmt.Sprintf("退还资金来源失败 task %s: %s", task.TaskID, err.Error()))
		return false
	}

	// 2. 退还令牌额度
	taskAdjustTokenQuota(ctx, task, -quota)

	// 3. 记录日志
	other := taskBillingOther(task)
	other["task_id"] = task.TaskID
	other["reason"] = reason
	model.RecordTaskBillingLog(model.RecordTaskBillingLogParams{
		UserId:    task.UserId,
		LogType:   model.LogTypeRefund,
		Content:   "",
		ChannelId: task.ChannelId,
		ModelName: taskModelName(task),
		Quota:     quota,
		TokenId:   task.PrivateData.TokenId,
		Group:     task.Group,
		Other:     other,
	})

	// 4. 资金退款完成后再清除持久化标记。
	// 回写失败必须显式告警，避免漏掉潜在的重复退款风险。
	task.Quota = 0
	if err := task.UpdateQuota(); err != nil {
		logger.LogError(ctx, fmt.Sprintf("退款成功但清除 task quota 失败 task %s: %s", task.TaskID, err.Error()))
	}
	return true
}

// RecalculateTaskQuota 通用的异步差额结算。
// actualQuota 是任务完成后的实际应扣额度，与预扣额度 (task.Quota) 做差额结算。
// reason 用于日志记录（例如 "token重算" 或 "adaptor调整"）。
// clamps 可选：若计算 actualQuota 时发生额度饱和，将其记入日志 admin_info（仅管理员可见）。
func RecalculateTaskQuota(ctx context.Context, task *model.Task, actualQuota int, reason string, clamps ...*common.QuotaClamp) {
	// applied 传 nil:adaptor 调整路径的 actualQuota 由适配器自己算出来,
	// 本函数不知道它用了哪些倍率,日志沿用提交时刻的 pin(那是这条路径唯一
	// 已知为真的倍率)。
	settleTaskQuotaDelta(ctx, task, actualQuota, reason, nil, nil, clamps...)
}

// taskAppliedRatios 是这一次差额结算**真正乘进金额**的那两个倍率。
//
// 它存在的唯一理由是让日志里的结构化字段与实收金额对得上:taskBillingOther
// 写的是提交时刻的 pin,而 token 重算路径的模型倍率会再过一次 QyGroupTaskRatio。
// 两者只在倍率被改过时不同 —— 而那正是唯一需要事后对账的时刻。少了它,
// 审计员用 tokens × model_ratio × group_ratio 复算出来的数与实收差好几倍,
// 却没有任何字段指向这一笔。
type taskAppliedRatios struct {
	ModelRatio float64
	GroupRatio float64
}

// settleTaskQuotaDelta 是差额结算的唯一函数体。
//
// 相对 RecalculateTaskQuota 多一个 groupRatioMiss:异步 Task 是**单笔金额最大**的
// 计费链路,它同样会走 ResolveGroupRatio 的 fail-open(模型分组被从 GroupRatio
// 删掉,而 abilities 里仍有 enabled 行 —— 545 条孤儿令牌正是这么来的),这一笔
// 差额就按凭空的 1.0 真金白银扣掉了。文本/WSS 侧把这件事写进
// other.admin_info.group_ratio_missing,Task 侧不写的话,运维按那个键全量扫描
// 补差会得出「Task 没事」这个正好相反的结论。
func settleTaskQuotaDelta(
	ctx context.Context, task *model.Task, actualQuota int, reason string,
	applied *taskAppliedRatios, groupRatioMiss *ratio_setting.GroupRatioMiss,
	clamps ...*common.QuotaClamp,
) {
	if actualQuota <= 0 {
		return
	}
	preConsumedQuota := task.Quota
	quotaDelta := actualQuota - preConsumedQuota

	if quotaDelta == 0 {
		logger.LogInfo(ctx, fmt.Sprintf("任务 %s 预扣费准确（%s，%s）",
			task.TaskID, logger.LogQuota(actualQuota), reason))
		return
	}

	logger.LogInfo(ctx, fmt.Sprintf("任务 %s 差额结算：delta=%s（实际：%s，预扣：%s，%s）",
		task.TaskID,
		logger.LogQuota(quotaDelta),
		logger.LogQuota(actualQuota),
		logger.LogQuota(preConsumedQuota),
		reason,
	))

	// 调整资金来源
	if err := taskAdjustFunding(task, quotaDelta); err != nil {
		logger.LogError(ctx, fmt.Sprintf("差额结算资金调整失败 task %s: %s", task.TaskID, err.Error()))
		return
	}

	// 调整令牌额度
	taskAdjustTokenQuota(ctx, task, quotaDelta)

	task.Quota = actualQuota
	if err := task.UpdateQuota(); err != nil {
		logger.LogError(ctx, fmt.Sprintf("差额结算回写 quota 失败 task %s: %s", task.TaskID, err.Error()))
	}

	var logType int
	var logQuota int
	if quotaDelta > 0 {
		logType = model.LogTypeConsume
		logQuota = quotaDelta
		model.UpdateUserUsedQuotaAndRequestCount(task.UserId, quotaDelta)
		model.UpdateChannelUsedQuota(task.ChannelId, quotaDelta)
	} else {
		logType = model.LogTypeRefund
		logQuota = -quotaDelta
	}
	other := taskBillingOther(task)
	// 日志里的倍率必须是**这一笔真正乘进金额**的那两个,不是提交时刻的 pin。
	// 两者只在倍率被改过时不同,而那正是唯一需要事后对账的时刻。
	if applied != nil {
		if applied.ModelRatio > 0 {
			other["model_ratio"] = applied.ModelRatio
		}
		other["group_ratio"] = applied.GroupRatio
	}
	other["task_id"] = task.TaskID
	other["pre_consumed_quota"] = preConsumedQuota
	other["actual_quota"] = actualQuota
	for _, clamp := range clamps {
		attachQuotaSaturationToOther(other, clamp)
	}
	attachGroupRatioFallbackToOther(other, groupRatioMiss)
	model.RecordTaskBillingLog(model.RecordTaskBillingLogParams{
		UserId:    task.UserId,
		LogType:   logType,
		Content:   reason,
		ChannelId: task.ChannelId,
		ModelName: taskModelName(task),
		Quota:     logQuota,
		TokenId:   task.PrivateData.TokenId,
		Group:     task.Group,
		Other:     other,
		NodeName:  task.PrivateData.NodeName,
	})
}

// RecalculateTaskQuotaByTokens 根据实际 token 消耗重新计费（异步差额结算）。
// 当任务成功且返回了 totalTokens 时，根据模型倍率和分组倍率重新计算实际扣费额度，
// 与预扣费的差额进行补扣或退还。支持钱包和订阅计费来源。
func RecalculateTaskQuotaByTokens(ctx context.Context, task *model.Task, totalTokens int) {
	if totalTokens <= 0 {
		return
	}

	modelName := taskModelName(task)

	// 获取模型价格和倍率
	modelRatio, hasRatioSetting, _ := ratio_setting.GetModelRatio(modelName)
	// 只有配置了倍率(非固定价格)时才按 token 重新计费
	if !hasRatioSetting || modelRatio <= 0 {
		return
	}

	// 获取用户和组的倍率信息。
	//
	// **优先读提交时刻落库的用户分组**:交叉倍率是 (用户分组, 模型分组) 这一对,
	// 只 pin 模型分组等于只 pin 了一半 —— 任务运行期间用户被降级/升级,预扣与结算
	// 就落在矩阵的两个不同格子上(详见 model.TaskBillingContext.UserGroup)。
	// 历史行没有这个字段,回落到现读,逐位等于改动前。
	userGroup := ""
	if bc := task.PrivateData.BillingContext; bc != nil {
		userGroup = bc.UserGroup
	}
	if userGroup == "" {
		if user, err := model.GetUserById(task.UserId, false); err == nil {
			userGroup = user.Group
		}
	}
	group := task.Group
	if group == "" {
		group = userGroup
	}
	if group == "" {
		return
	}
	modelRatio = QyGroupTaskRatio(group, modelName, modelRatio)

	// ── 分组倍率:结算沿用**提交那一刻钉下的那个值**,不重算 ──
	//
	// 预扣按提交时刻的倍率算。结算若重新解析,倍率表在任务运行期间被改过一次
	// (运营改矩阵、或用户分组改名/删除时 groupns 的 dropUserGroupOptions 删掉
	// GroupGroupRatio 的外层键)就会拿另一个价去算差额,而差额是以**追扣**落到
	// 用户头上的:vip 谈好的 0.2 变回兜底的 2,同一笔任务从预扣 20000 变成实扣
	// 200000。更糟的是这条路径**零告警**:兜底价本身存在 ⇒ BaseMissing=false ⇒
	// SilentFallback() 为假 ⇒ 不写 admin_info、不 LogWarn,事后对账查不出来。
	//
	// 同步的文本/音频结算读的是 PriceData 的 pin,异步 Task 必须同口径:
	// 用户提交时看到什么价,这一笔就按什么价结算。
	//
	// **group 恒取 task.Group,绝不在结算这一刻重新解析「用户分组的默认模型分组」。**
	// task.Group 在提交时就由 relayInfo.UsingGroup 落库(model/task.go),它是
	// 这条异步链路的另一半 pin;上面那句 `if group == "" { group = userGroup }`
	// 只保留给历史空值行,不得扩大成一条新的解析路径。
	//
	// 历史行没有 GroupRatioPinned 这一位,回落现算(走全仓唯一的
	// ratio_setting.ResolveGroupRatio),逐位等于改动前。
	var finalGroupRatio float64
	var groupRatioMiss *ratio_setting.GroupRatioMiss
	if bc := task.PrivateData.BillingContext; bc != nil && bc.GroupRatioPinned {
		finalGroupRatio = bc.GroupRatio
		// pin 下来的那个 1.0 若本来就是 fail-open 编出来的,标记必须跟着 pin 一起
		// 传到结算这一笔上,否则运维按 group_ratio_missing 全量扫描补差时,
		// 会漏掉差额那半边(预扣那半边由提交时刻的消费日志覆盖)。
		if bc.GroupRatioFailOpen {
			groupRatioMiss = &ratio_setting.GroupRatioMiss{
				UserGroup: userGroup, ModelGroup: group, AppliedRatio: finalGroupRatio,
			}
			logger.LogWarn(ctx, fmt.Sprintf(
				"group ratio fail-open carried into task settlement: user_group=%s model_group=%s applied_ratio=%g task=%s",
				userGroup, group, finalGroupRatio, task.TaskID))
		}
	} else {
		groupRatioRes := ratio_setting.ResolveGroupRatio(userGroup, group)
		finalGroupRatio = groupRatioRes.Ratio
		// 静默 fail-open 必须逐笔落地,判据完全委托给 SilentFallback()(与
		// relay/common 的 NoteGroupRatioFallback 是同一个判据,不在这里重写)。
		if groupRatioRes.SilentFallback() {
			groupRatioMiss = &ratio_setting.GroupRatioMiss{
				UserGroup: userGroup, ModelGroup: group, AppliedRatio: finalGroupRatio,
			}
			logger.LogWarn(ctx, fmt.Sprintf(
				"group ratio fail-open on task settlement: user_group=%s model_group=%s applied_ratio=%g task=%s",
				userGroup, group, finalGroupRatio, task.TaskID))
		}
	}

	// 计算 OtherRatios 乘积（视频折扣、时长等）
	otherMultiplier := 1.0
	if priceData := taskBillingContextPriceData(task.PrivateData.BillingContext); priceData != nil {
		otherMultiplier = priceData.OtherRatioMultiplier()
	}

	// 计算实际应扣费额度: totalTokens * modelRatio * groupRatio * otherMultiplier（饱和转换，防止溢出成负数）
	actualQuota, clamp := common.QuotaFromFloatChecked(float64(totalTokens) * modelRatio * finalGroupRatio * otherMultiplier)

	reason := fmt.Sprintf("token重算：tokens=%d, modelRatio=%.2f, groupRatio=%.2f, otherMultiplier=%.4f", totalTokens, modelRatio, finalGroupRatio, otherMultiplier)
	settleTaskQuotaDelta(ctx, task, actualQuota, reason,
		&taskAppliedRatios{ModelRatio: modelRatio, GroupRatio: finalGroupRatio},
		groupRatioMiss, clamp)
}
