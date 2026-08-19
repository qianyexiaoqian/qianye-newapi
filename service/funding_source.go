package service

import (
	"errors"
	"fmt"
	"time"

	"github.com/QuantumNous/new-api/common"
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
// 套餐结算差额的落点 — relay / 任务 / 补记三条链路共用
// ---------------------------------------------------------------------------

// subscriptionSettleSplit 记录一次套餐结算差额**真正落在哪三处**。
type subscriptionSettleSplit struct {
	// Applied 是套餐真正吃下的部分(负数 = 退回套餐)。
	Applied int64
	// WalletCharged 是撞到 amount_total 上限、改由钱包补收的部分。
	WalletCharged int64
	// WrittenOff 是撞到上限、而这个模型分组**不许**由钱包出资时,平台自己吃下的部分。
	WrittenOff int64
}

// settleSubscriptionDelta 把一次结算差额落到套餐上,并决定撞到 amount_total
// 上限的那一段由谁承担。三条链路(relay 的 SubscriptionFunding.Settle、
// PostConsumeQuota 的补记、异步任务的 taskAdjustFunding)共用同一份实现 ——
// 分开写过一次,结果是同一条规则在三处各有一个版本。
//
// ══════════════ 差额为什么会存在,以及为什么不能拒绝它 ══════════════
//
// 套餐余额不够一次预扣额时按剩余额度**部分预扣**(model.pickFundingSubscription
// 的第二轮),这是「套餐尾数花不掉」的解法:预扣额是真实花费的几十到上百倍,
// 只按整额筛候选会让每张套餐都留下"一次预扣额 − 1"的死残额。
// 绝大多数请求的真实花费远小于那点余额,结算时退回去,一分钱不欠。
//
// 少数请求的真实花费超出了那点余额:套餐扣到 amount_total 为止(
// SettleUserSubscriptionDelta 钳位并回报落点),剩下 shortfall 无处可去。
// 此刻请求**已经跑完**,上游 token 已经烧掉 —— 返回错误换不回那些 token,
// 只会让调用方连令牌扣减和账单日志一起跳过,账面上没有任何一行指向这笔钱
// (实测过的形态:计费 14,566、实收 4,200)。
//
// ══════════════ 于是只剩两个去处,由钱包出资闸门二选一 ══════════════
//
//	可以补收 → 钱包扣掉 shortfall(此前**无条件**走这条,套餐上那个
//	           allow_wallet_overflow 从来没被问过 —— 这正是本次修的缺陷)
//	不得补收 → 核销:套餐扣到上限,钱包一分不动,这一段进日志的
//	           subscription_written_off 与 groupns 的核销计数
//
// 判据交给 QyWalletMayCoverSubscriptionShortfall,与请求前那道
// QyModelGroupFundingAllowed **同一个实现**:用户分组本身含这个模型分组的人
// 照常由钱包补收(他不靠套餐也能用),只有"纯靠套餐解锁"的分组才轮到套餐上那个
// 开关说话。默认实现恒 true ⇒ 逐位等于扩展未启用时的行为。
//
// 时序不可换:必须先提交套餐那一笔,闸门才看得到"这张套餐已经到顶了"。
//
// 核销量由 model.ClaimSubscriptionWriteOff 封顶:每张套餐每个重置周期只发一份
// 核销名额(随 amount_used 一起归零)。此前这条上界是靠"核销之后 amount_used
// == amount_total,pickFundingSubscription 就不再派预扣"推出来的,而那条推理
// 在并发下不成立 —— 多路请求可以各自先拿到预扣、再各自超支。
func settleSubscriptionDelta(userId int, userGroup, modelGroup string, subscriptionId int, delta int64) (subscriptionSettleSplit, error) {
	var split subscriptionSettleSplit
	if delta == 0 {
		return split, nil
	}
	applied, err := model.SettleUserSubscriptionDelta(subscriptionId, delta)
	if err != nil {
		return split, err
	}
	split.Applied = applied
	shortfall := delta - applied
	// 只补收正差额。负差额(退款没能全额退进套餐,例如套餐已被续期清零)绝不
	// 往钱包里补:那笔钱当初未必是从钱包出的,补进去就是凭空发钱。
	if shortfall <= 0 {
		return split, nil
	}
	if !QyWalletMayCoverSubscriptionShortfall(userId, userGroup, modelGroup, shortfall) {
		// 「每张套餐每个重置周期至多核销一次」以前是靠推理成立的:核销之后
		// amount_used == amount_total,pickFundingSubscription 就再也不给这张
		// 套餐派预扣了。推理漏掉了并发 —— N 路请求各自先拿到预扣、各自超支、
		// 各自走到这里,核销笔数 = 在飞路数,平台白送的额度随并发线性放大且
		// 没有上界(实测 10 路把一张面值 10000 的套餐核销掉 40000)。
		//
		// 现在名额由 model.ClaimSubscriptionWriteOff 在行锁里发,一个周期一份。
		// 拿到名额的那一笔照旧核销(= 上一轮定下的口径,开关说不许动钱包就一分
		// 不动);名额已经被用掉的后续并发只能回落钱包 —— 在「平台无上限地送」
		// 与「按实际用量向用户收」之间,后者是唯一还能收敛的选项。这条路一定
		// 留痕:SysError + 日志里的 wallet_quota_deducted。
		claimed, claimErr := model.ClaimSubscriptionWriteOff(subscriptionId)
		if claimErr != nil {
			return split, claimErr
		}
		if claimed {
			split.WrittenOff = shortfall
			// 登记必须在**领到名额之后**:闸门只表达了规则,"这一段最后由谁出"
			// 要等这一行才定下来。写在闸门里的话,名额用尽后照样扣了钱包的那些笔
			// 也会被计进免单量。
			QyNoteSubscriptionWriteOff(userId, userGroup, modelGroup, shortfall)
			return split, nil
		}
		common.SysError(fmt.Sprintf(
			"subscription %d (user %d, group %s, model group %s) already used its write-off allowance this reset period; charging the %d shortfall to the wallet instead",
			subscriptionId, userId, userGroup, modelGroup, shortfall))
	}
	// 额度列是 32 位;这里走 quota_math 的饱和转换而不是裸 int(),
	// 越界会被夹住并留下 SysError,而不是静默回绕成一笔负扣款(= 凭空发钱)。
	if err := model.DecreaseUserQuota(userId, common.QuotaFromFloat(float64(shortfall)), false); err != nil {
		return split, err
	}
	split.WalletCharged = shortfall
	return split, nil
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

	// userGroup 是这个人的 users.group。只有结算差额撞到 amount_total 上限时才
	// 用得上：钱包能不能补收那一段，第一判据就是「用户分组本身含不含 usingGroup」。
	// 空串在闸门里等价于"不含"，所以构造时必须给真值，不能靠零值。
	userGroup string

	// settleApplied / settleWalletShortfall / settleWrittenOff 记录最近一次
	// Settle 的三个落点：套餐真正吃下的部分、撞到 amount_total 上限后由钱包
	// 补收的部分、以及不许钱包补收时由平台核销的部分。
	settleApplied         int64
	settleWalletShortfall int64
	settleWrittenOff      int64
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
	s.settleWrittenOff = 0
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
	// 撞到 amount_total 上限的那一段由 settleSubscriptionDelta 分派：能补收就落
	// 钱包（方向与 WalletFunding.Settle 的正差额一致：无条件扣，余额不足记为欠费），
	// 不能补收就核销。绝不静默丢弃 —— 那会让「日志记多少」与「实际收多少」分家。
	split, err := settleSubscriptionDelta(s.userId, s.userGroup, s.usingGroup, s.subscriptionId, subDelta)
	if err != nil {
		return err
	}
	s.settleApplied = split.Applied
	s.settleWalletShortfall = split.WalletCharged
	s.settleWrittenOff = split.WrittenOff
	return nil
}

// SettleApplied 返回最近一次 Settle 中套餐实际吃下的额度。
func (s *SubscriptionFunding) SettleApplied() int64 { return s.settleApplied }

// SettleWalletShortfall 返回最近一次 Settle 中改由钱包补收的额度。
func (s *SubscriptionFunding) SettleWalletShortfall() int64 { return s.settleWalletShortfall }

// SettleWrittenOff 返回最近一次 Settle 中由平台核销(谁都没出)的额度。
func (s *SubscriptionFunding) SettleWrittenOff() int64 { return s.settleWrittenOff }

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
