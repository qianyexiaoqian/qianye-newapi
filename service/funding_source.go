package service

import (
	"errors"
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

// ErrInsufficientWalletQuota 钱包原子预扣失败（余额不足），未发生任何扣减。
// BillingSession 据此映射为 ErrorCodeInsufficientUserQuota，
// 使 wallet_first 等计费偏好可以回退到订阅。
var ErrInsufficientWalletQuota = errors.New("wallet quota insufficient")

type WalletFunding struct {
	userId   int
	consumed int // 实际预扣的用户额度
}

func (w *WalletFunding) Source() string { return BillingSourceWallet }

func (w *WalletFunding) PreConsume(amount int) error {
	if amount <= 0 {
		return nil
	}
	reserved, err := model.TryReserveUserQuota(w.userId, amount)
	if err != nil {
		return err
	}
	if !reserved {
		return ErrInsufficientWalletQuota
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

// subscriptionSettleDelta 把"按请求预扣额算出来的差额"换算成"套餐这一侧真正
// 该增减多少"。
//
// requested 是想扣的额度(令牌那一侧就是按它预扣、按它退的),preConsumed 是
// 套餐余额不够时**实际**扣到的那一部分。两者相等时这里恒等变换。
//
//	想扣 3048 / 只扣到 100 / 真实花费 28   → delta −3020 → subDelta −72(退回 28)
//	想扣 3048 / 只扣到 100 / 真实花费 5000 → delta  1952 → subDelta 4900(撞上限后落钱包)
func subscriptionSettleDelta(delta int, requested, preConsumed int64) int64 {
	return int64(delta) + (requested - preConsumed)
}

func (s *SubscriptionFunding) Settle(delta int) error {
	s.settleApplied = 0
	s.settleWalletShortfall = 0
	// 套餐可能只按**剩余额度**部分预扣(见 model.PreConsumeUserSubscription):
	// 想扣 s.amount,实际只扣到了 s.preConsumed。调用方给的 delta 是按"想扣多少"
	// 算的(令牌那一侧确实是按它预扣的,退款也按它退),套餐这一侧必须把这段差
	// 补回来,否则退款方向会连着把套餐里**别的**消费一起退掉,补扣方向又会少收。
	//
	// 举例:想扣 3048、套餐只剩 100、真实花费 28 —— delta = 28−3048 = −3020,
	// 补回 3048−100 = 2948 之后 subDelta = −72,套餐从 100 退回 28,分毫不差;
	// 若真实花费 5000,subDelta = 4900,套餐撞上限只吃下 0,4900 落钱包。
	subDelta := subscriptionSettleDelta(delta, s.amount, s.preConsumed)
	if subDelta == 0 {
		return nil
	}
	applied, err := model.SettleUserSubscriptionDelta(s.subscriptionId, subDelta)
	if err != nil {
		return err
	}
	s.settleApplied = applied
	shortfall := subDelta - applied
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
