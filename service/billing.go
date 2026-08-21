package service

import (
	"fmt"
	"net/http"

	"github.com/QuantumNous/new-api/logger"
	"github.com/QuantumNous/new-api/model"
	relaycommon "github.com/QuantumNous/new-api/relay/common"
	"github.com/QuantumNous/new-api/relaykit/types"
	"github.com/gin-gonic/gin"
)

const (
	BillingSourceWallet       = "wallet"
	BillingSourceSubscription = "subscription"
)

// RejectOverdrawnFreeModelCall 是免费模型路径上那道“欠费了就不许再调”的闸。
//
// 关掉「免费模型预消耗」之后，价格/倍率为 0 的模型会整段跳过
// PreConsumeBilling，relayInfo.Billing 为 nil，于是既不过 tryWallet 的
// wallet_overdrawn / wallet_empty 三道判据，也不过 model.TryReserveUserQuota 的
// 原子预留；而跑完之后 SettleBilling 的回退分支直接 PostConsumeQuota
// 裸扣钱包。“免费模型”并不等于“这次调用不产生金额”：内置工具调用的
// 附加费与 ModelRatio / ModelPrice 完全解耦（service/text_quota.go 的
// calculateTextToolCallSurcharge），web_search_preview 单次就是 5000 quota。于是一个
// 已经欠费的账号可以无上界地调用并无上界地记账，全站唯一那道余额闸一次
// 都不会被问到。
//
// 这里只拦 userQuota < 0（已欠费），不拦 == 0：余额刚好用完的人还能用免费
// 模型，那正是这个开关的本意；而“余额可为负”本身是拍板接受的结算形态
// （qianye/docs/decisions.md D-01），接受的是“结算可以扣成负”，不是“欠着钱还能
// 接着刷”。
//
// 错误码、文案、审计 reason 与 tryWallet 的 wallet_overdrawn 那一档逐字相同：
// 客户端不应该因为管理员拨了一个开关就看到另一种拒绝。
func RejectOverdrawnFreeModelCall(c *gin.Context, relayInfo *relaycommon.RelayInfo) *types.NewAPIError {
	if relayInfo == nil {
		return nil
	}
	userQuota, err := model.GetUserQuota(relayInfo.UserId, false)
	if err != nil {
		return types.NewError(err, types.ErrorCodeQueryDataError, types.ErrOptionWithSkipRetry())
	}
	if userQuota >= 0 {
		return nil
	}
	quotaErr := fmt.Errorf("账户已透支, 当前欠费 %s, 请充值补足欠款后再继续调用",
		logger.FormatQuota(-userQuota))
	logPreConsumeRejected(c, relayInfo, "wallet_overdrawn", 0, quotaErr)
	return types.NewErrorWithStatusCode(
		quotaErr, types.ErrorCodeInsufficientUserQuota, http.StatusForbidden,
		types.ErrOptionWithSkipRetry(), types.ErrOptionWithNoRecordErrorLog())
}

// PreConsumeBilling 创建 BillingSession(套餐优先,套餐出不了资才走钱包)并执行预扣费。
// 会话存储在 relayInfo.Billing 上，供后续 Settle / Refund 使用。
func PreConsumeBilling(c *gin.Context, preConsumedQuota int, relayInfo *relaycommon.RelayInfo) *types.NewAPIError {
	if relayInfo != nil && relayInfo.QuotaClamp != nil {
		return types.NewErrorWithStatusCode(
			relayInfo.QuotaClamp,
			types.ErrorCodeModelPriceError,
			http.StatusBadRequest,
			types.ErrOptionWithSkipRetry(),
		)
	}
	if preConsumedQuota < 0 {
		return types.NewErrorWithStatusCode(
			fmt.Errorf("pre-consume quota cannot be negative: %d", preConsumedQuota),
			types.ErrorCodeModelPriceError,
			http.StatusBadRequest,
			types.ErrOptionWithSkipRetry(),
		)
	}
	session, apiErr := NewBillingSession(c, relayInfo, preConsumedQuota)
	if apiErr != nil {
		return apiErr
	}
	relayInfo.Billing = session
	return nil
}

// ---------------------------------------------------------------------------
// SettleBilling — 后结算辅助函数
// ---------------------------------------------------------------------------

// SettleBilling 执行计费结算。如果 RelayInfo 上有 BillingSession 则通过 session 结算，
// 否则回退到旧的 PostConsumeQuota 路径（兼容按次计费等场景）。
func SettleBilling(ctx *gin.Context, relayInfo *relaycommon.RelayInfo, actualQuota int) error {
	if relayInfo.Billing != nil {
		preConsumed := relayInfo.Billing.GetPreConsumedQuota()
		delta := actualQuota - preConsumed

		if delta > 0 {
			logger.LogInfo(ctx, fmt.Sprintf("预扣费后补扣费：%s（实际消耗：%s，预扣费：%s）",
				logger.FormatQuota(delta),
				logger.FormatQuota(actualQuota),
				logger.FormatQuota(preConsumed),
			))
		} else if delta < 0 {
			logger.LogInfo(ctx, fmt.Sprintf("预扣费后返还扣费：%s（实际消耗：%s，预扣费：%s）",
				logger.FormatQuota(-delta),
				logger.FormatQuota(actualQuota),
				logger.FormatQuota(preConsumed),
			))
		} else {
			logger.LogInfo(ctx, fmt.Sprintf("预扣费与实际消耗一致，无需调整：%s（按次计费）",
				logger.FormatQuota(actualQuota),
			))
		}

		if err := relayInfo.Billing.Settle(actualQuota); err != nil {
			return err
		}

		// 发送额度通知（订阅计费使用订阅剩余额度）
		if actualQuota != 0 {
			if relayInfo.BillingSource == BillingSourceSubscription {
				checkAndSendSubscriptionQuotaNotify(relayInfo)
			} else {
				checkAndSendQuotaNotify(relayInfo, actualQuota-preConsumed, preConsumed)
			}
		}
		return nil
	}

	// 回退：无 BillingSession 时使用旧路径
	quotaDelta := actualQuota - relayInfo.FinalPreConsumedQuota
	if quotaDelta != 0 {
		return PostConsumeQuota(relayInfo, quotaDelta, relayInfo.FinalPreConsumedQuota, true)
	}
	return nil
}
