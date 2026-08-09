package service

import (
	"time"

	"github.com/QuantumNous/new-api/model"
)

// ---------------------------------------------------------------------------
// FundingSource — 资金来源接口（钱包 or 订阅）
// ---------------------------------------------------------------------------

// FundingSource 抽象了预扣费的资金来源。
type FundingSource interface {
	// Source 返回资金来源标识："wallet" 或 "subscription"
	Source() string
	// PreConsume 从该资金来源预扣 amount 额度
	PreConsume(amount int) error
	// Settle 根据差额调整资金来源（正数补扣，负数退还）
	Settle(delta int) error
	// Refund 退还所有预扣费
	Refund() error
}

// ---------------------------------------------------------------------------
// WalletFunding — 钱包资金来源实现
// ---------------------------------------------------------------------------

type WalletFunding struct {
	userId   int
	consumed int // 实际预扣的用户额度
}

func (w *WalletFunding) Source() string { return BillingSourceWallet }

func (w *WalletFunding) PreConsume(amount int) error {
	if amount <= 0 {
		return nil
	}
	// 条件原子扣减:余额判据必须与扣减在同一条 UPDATE 里。NewBillingSession
	// 里那次 GetUserQuota 只是为了给出可读的 403 文案与快速失败,它和这次扣减
	// 之间存在 TOCTOU 窗口,并发请求会全部通过那个判据。
	if err := model.PreConsumeUserQuota(w.userId, amount); err != nil {
		return err
	}
	w.consumed = amount
	return nil
}

func (w *WalletFunding) Settle(delta int) error {
	if delta == 0 {
		return nil
	}
	if delta > 0 {
		return model.DecreaseUserQuota(w.userId, delta, false)
	}
	return model.IncreaseUserQuota(w.userId, -delta, false)
}

func (w *WalletFunding) Refund() error {
	if w.consumed <= 0 {
		return nil
	}
	// IncreaseUserQuota 是 quota += N 的非幂等操作，不能重试，否则会多退额度。
	// 订阅的 RefundSubscriptionPreConsume 有 requestId 幂等保护所以可以重试。
	return model.IncreaseUserQuota(w.userId, w.consumed, false)
}

// ---------------------------------------------------------------------------
// SubscriptionFunding — 订阅资金来源实现
// ---------------------------------------------------------------------------

type SubscriptionFunding struct {
	requestId      string
	userId         int
	modelName      string
	amount         int64 // 预扣的订阅额度（subConsume）
	subscriptionId int
	preConsumed    int64

	// usingGroup 是本次请求的模型分组：「余额仅限绑定分组」的套餐据此被跳过。
	usingGroup string

	// settleApplied / settleWalletShortfall 记录最近一次 Settle 的落点：
	// 套餐真正吃下的部分，以及因撞到 amount_total 上限而改由钱包补收的部分。
	settleApplied         int64
	settleWalletShortfall int64
	// 以下字段在 PreConsume 成功后填充，供 RelayInfo 同步使用
	AmountTotal     int64
	AmountUsedAfter int64
	PlanId          int
	PlanTitle       string
}

func (s *SubscriptionFunding) Source() string { return BillingSourceSubscription }

func (s *SubscriptionFunding) PreConsume(_ int) error {
	// amount 参数被忽略，使用内部 s.amount（已在构造时根据 preConsumedQuota 计算）
	res, err := model.PreConsumeUserSubscription(s.requestId, s.userId, s.modelName, 0, s.amount, s.usingGroup)
	if err != nil {
		return err
	}
	s.subscriptionId = res.UserSubscriptionId
	s.preConsumed = res.PreConsumed
	s.AmountTotal = res.AmountTotal
	s.AmountUsedAfter = res.AmountUsedAfter
	// 获取订阅计划信息
	if planInfo, err := model.GetSubscriptionPlanInfoByUserSubscriptionId(res.UserSubscriptionId); err == nil && planInfo != nil {
		s.PlanId = planInfo.PlanId
		s.PlanTitle = planInfo.PlanTitle
	}
	return nil
}

func (s *SubscriptionFunding) Settle(delta int) error {
	s.settleApplied = 0
	s.settleWalletShortfall = 0
	if delta == 0 {
		return nil
	}
	applied, err := model.SettleUserSubscriptionDelta(s.subscriptionId, int64(delta))
	if err != nil {
		return err
	}
	s.settleApplied = applied
	shortfall := int64(delta) - applied
	if shortfall <= 0 {
		return nil
	}
	// 套餐撞到 amount_total 上限，剩下的差额必须落到钱包上，否则这笔已经服务完成
	// 的请求就白送了。方向与 WalletFunding.Settle 的正差额一致：无条件扣，
	// 余额不足的部分记为欠费，保证「日志记多少 == 实际收多少」。
	if err := model.DecreaseUserQuota(s.userId, int(shortfall), false); err != nil {
		return err
	}
	s.settleWalletShortfall = shortfall
	return nil
}

// SettleApplied 返回最近一次 Settle 中套餐实际吃下的额度。
func (s *SubscriptionFunding) SettleApplied() int64 { return s.settleApplied }

// SettleWalletShortfall 返回最近一次 Settle 中改由钱包补收的额度。
func (s *SubscriptionFunding) SettleWalletShortfall() int64 { return s.settleWalletShortfall }

func (s *SubscriptionFunding) Refund() error {
	if s.preConsumed <= 0 {
		return nil
	}
	return refundWithRetry(func() error {
		return model.RefundSubscriptionPreConsume(s.requestId)
	})
}

// refundWithRetry 尝试多次执行退款操作以提高成功率，只能用于基于事务的退款函数！！！！！！
// try to refund with retries, only for refund functions based on transactions!!!
func refundWithRetry(fn func() error) error {
	if fn == nil {
		return nil
	}
	const maxAttempts = 3
	var lastErr error
	for i := 0; i < maxAttempts; i++ {
		if err := fn(); err == nil {
			return nil
		} else {
			lastErr = err
		}
		if i < maxAttempts-1 {
			time.Sleep(time.Duration(200*(i+1)) * time.Millisecond)
		}
	}
	return lastErr
}
