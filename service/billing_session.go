package service

import (
	"errors"
	"fmt"
	"net/http"
	"strings"
	"sync"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/logger"
	"github.com/QuantumNous/new-api/model"
	relaycommon "github.com/QuantumNous/new-api/relay/common"
	"github.com/QuantumNous/new-api/relaykit/types"

	"github.com/bytedance/gopkg/util/gopool"
	"github.com/gin-gonic/gin"
)

// ---------------------------------------------------------------------------
// BillingSession — 统一计费会话
// ---------------------------------------------------------------------------

// BillingSession 封装单次请求的预扣费/结算/退款生命周期。
// 实现 relaycommon.BillingSettler 接口。
type BillingSession struct {
	relayInfo        *relaycommon.RelayInfo
	funding          FundingSource
	preConsumedQuota int  // 实际预扣额度（信任用户可能为 0）
	tokenConsumed    int  // 令牌额度实际扣减量
	extraReserved    int  // 发送前补充预扣的额度（订阅退款时需要单独回滚）
	trusted          bool // 是否命中信任额度旁路
	fundingSettled   bool // funding.Settle 已成功，资金来源已提交
	settled          bool // Settle 全部完成（资金 + 令牌）
	refunded         bool // Refund 已调用
	mu               sync.Mutex
}

// Settle 根据实际消耗额度进行结算。
// 资金来源和令牌额度分两步提交：若资金来源已提交但令牌调整失败，
// 会标记 fundingSettled 防止 Refund 对已提交的资金来源执行退款。
func (s *BillingSession) Settle(actualQuota int) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.settled {
		return nil
	}
	delta := actualQuota - s.preConsumedQuota
	if delta == 0 && !s.fundingHasPartialReserve() {
		s.settled = true
		return nil
	}
	// 1) 调整资金来源（仅在尚未提交时执行，防止重复调用）
	if !s.fundingSettled {
		if err := s.funding.Settle(delta); err != nil {
			return err
		}
		s.fundingSettled = true
	}
	// 2) 调整令牌额度
	var tokenErr error
	if !s.relayInfo.IsPlayground && delta != 0 {
		if delta > 0 {
			tokenErr = model.DecreaseTokenQuota(s.relayInfo.TokenId, s.relayInfo.TokenKey, delta)
		} else {
			tokenErr = model.IncreaseTokenQuota(s.relayInfo.TokenId, s.relayInfo.TokenKey, -delta)
		}
		if tokenErr != nil {
			// 资金来源已提交，令牌调整失败只能记录日志；标记 settled 防止 Refund 误退资金
			common.SysLog(fmt.Sprintf("error adjusting token quota after funding settled (userId=%d, tokenId=%d, delta=%d): %s",
				s.relayInfo.UserId, s.relayInfo.TokenId, delta, tokenErr.Error()))
		}
	}
	// 3) 更新 relayInfo 上的订阅 PostDelta（用于日志）
	//    只记套餐**真正吃下**的部分：撞到 amount_total 上限时差额已由钱包补收，
	//    把全额记进 PostDelta 会让日志算出的套餐已用量超过总额。
	if sub, ok := s.funding.(*SubscriptionFunding); ok {
		s.relayInfo.SubscriptionPostDelta += sub.SettleApplied()
		s.relayInfo.SubscriptionWalletShortfall += sub.SettleWalletShortfall()
	}
	s.settled = true
	return tokenErr
}

// fundingHasPartialReserve 报告资金来源是不是"只预扣到了一部分"。
//
// 套餐余额不足一次预扣额时按剩余额度部分预扣,于是即便差额 delta 恰好为 0
// (真实花费等于预扣估算额),套餐那一侧仍然欠着 amount − preConsumed 没收。
// 差额为 0 就早退会把这段钱永久漏掉。
func (s *BillingSession) fundingHasPartialReserve() bool {
	sub, ok := s.funding.(*SubscriptionFunding)
	return ok && sub.amount != sub.preConsumed
}

// Refund 退还所有预扣费，幂等安全，异步执行。
func (s *BillingSession) Refund(c *gin.Context) {
	s.mu.Lock()
	if s.settled || s.refunded || !s.needsRefundLocked() {
		s.mu.Unlock()
		return
	}
	s.refunded = true
	s.mu.Unlock()

	logger.LogInfo(c, fmt.Sprintf("用户 %d 请求失败, 返还预扣费（token_quota=%s, funding=%s）",
		s.relayInfo.UserId,
		logger.FormatQuota(s.tokenConsumed),
		s.funding.Source(),
	))

	// 复制需要的值到闭包中
	tokenId := s.relayInfo.TokenId
	tokenKey := s.relayInfo.TokenKey
	isPlayground := s.relayInfo.IsPlayground
	tokenConsumed := s.tokenConsumed
	extraReserved := s.extraReserved
	subscriptionId := s.relayInfo.SubscriptionId
	funding := s.funding

	gopool.Go(func() {
		// 1) 退还资金来源
		if err := funding.Refund(); err != nil {
			common.SysLog("error refunding billing source: " + err.Error())
		}
		if extraReserved > 0 && funding.Source() == BillingSourceSubscription && subscriptionId > 0 {
			if err := model.PostConsumeUserSubscriptionDelta(subscriptionId, -int64(extraReserved)); err != nil {
				common.SysLog("error refunding subscription extra reserved quota: " + err.Error())
			}
		}
		// 2) 退还令牌额度
		if tokenConsumed > 0 && !isPlayground {
			if err := model.IncreaseTokenQuota(tokenId, tokenKey, tokenConsumed); err != nil {
				common.SysLog("error refunding token quota: " + err.Error())
			}
		}
	})
}

// NeedsRefund 返回是否存在需要退还的预扣状态。
func (s *BillingSession) NeedsRefund() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.needsRefundLocked()
}

func (s *BillingSession) needsRefundLocked() bool {
	if s.settled || s.refunded || s.fundingSettled {
		// fundingSettled 时资金来源已提交结算，不能再退预扣费
		return false
	}
	if s.tokenConsumed > 0 {
		return true
	}
	// 订阅可能在 tokenConsumed=0 时仍预扣了额度
	if sub, ok := s.funding.(*SubscriptionFunding); ok && sub.preConsumed > 0 {
		return true
	}
	return false
}

// GetPreConsumedQuota 返回实际预扣的额度。
func (s *BillingSession) GetPreConsumedQuota() int {
	return s.preConsumedQuota
}

func (s *BillingSession) Reserve(targetQuota int) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	if s.settled || s.refunded || s.trusted || targetQuota <= s.preConsumedQuota {
		return nil
	}

	delta := targetQuota - s.preConsumedQuota
	if delta <= 0 {
		return nil
	}

	if err := s.reserveFunding(delta); err != nil {
		return err
	}
	if err := s.reserveToken(delta); err != nil {
		s.rollbackFundingReserve(delta)
		return err
	}

	s.preConsumedQuota += delta
	s.tokenConsumed += delta
	s.extraReserved += delta
	s.syncRelayInfo()
	return nil
}

// ---------------------------------------------------------------------------
// PreConsume — 统一预扣费入口（含信任额度旁路）
// ---------------------------------------------------------------------------

// preConsume 执行预扣费：信任检查 -> 令牌预扣 -> 资金来源预扣。
// 任一步骤失败时原子回滚已完成的步骤。
func (s *BillingSession) preConsume(c *gin.Context, quota int) *types.NewAPIError {
	effectiveQuota := quota

	// ---- 信任额度旁路 ----
	if s.shouldTrust(c) {
		s.trusted = true
		effectiveQuota = 0
		logger.LogInfo(c, fmt.Sprintf("用户 %d 额度充足, 信任且不需要预扣费 (funding=%s)", s.relayInfo.UserId, s.funding.Source()))
	} else if effectiveQuota > 0 {
		logger.LogInfo(c, fmt.Sprintf("用户 %d 需要预扣费 %s (funding=%s)", s.relayInfo.UserId, logger.FormatQuota(effectiveQuota), s.funding.Source()))
	}

	// ---- 1) 预扣令牌额度 ----
	if effectiveQuota > 0 {
		if err := PreConsumeTokenQuota(s.relayInfo, effectiveQuota); err != nil {
			logPreConsumeRejected(c, s.relayInfo, "token", effectiveQuota, err)
			return types.NewErrorWithStatusCode(err, types.ErrorCodePreConsumeTokenQuotaFailed, http.StatusForbidden, types.ErrOptionWithSkipRetry(), types.ErrOptionWithNoRecordErrorLog())
		}
		s.tokenConsumed = effectiveQuota
	}

	// ---- 2) 预扣资金来源 ----
	if err := s.funding.PreConsume(effectiveQuota); err != nil {
		// 预扣费失败，回滚令牌额度
		if s.tokenConsumed > 0 && !s.relayInfo.IsPlayground {
			if rollbackErr := model.IncreaseTokenQuota(s.relayInfo.TokenId, s.relayInfo.TokenKey, s.tokenConsumed); rollbackErr != nil {
				common.SysLog(fmt.Sprintf("error rolling back token quota (userId=%d, tokenId=%d, amount=%d, fundingErr=%s): %s",
					s.relayInfo.UserId, s.relayInfo.TokenId, s.tokenConsumed, err.Error(), rollbackErr.Error()))
			}
			s.tokenConsumed = 0
		}
		// TODO: model 层应定义哨兵错误（如 ErrNoActiveSubscription），用 errors.Is 替代字符串匹配
		if errors.Is(err, ErrInsufficientWalletQuota) {
			userQuota, quotaErr := model.GetUserQuota(s.relayInfo.UserId, false)
			if quotaErr != nil {
				userQuota = 0
			}
			// 合并上游原子预扣后**新增**的一条拒绝路径,必须和其它五条一样被记下来,
			// 否则它就是这份计数里唯一的盲区(而且是最常见的那一类:余额判据)。
			//
			// reason 刻意不复用 wallet_empty:走到这里说明 NewBillingSession 的
			// 事前余额检查**通过了**,是并发的另一路请求先把余额拿走,原子预扣才
			// 失败的。它与「进来时余额就不够」是两种现象;分开计数既能让
			// wallet_empty 的同比口径保持不变(那是 CompletionRatio 杀伤面的度量),
			// 又能让"到底有多少请求真的在抢余额"变得可观测。
			logPreConsumeRejected(c, s.relayInfo, "wallet_race", effectiveQuota, err)
			return types.NewErrorWithStatusCode(
				fmt.Errorf("用户额度不足, 剩余额度: %s", logger.FormatQuota(userQuota)),
				types.ErrorCodeInsufficientUserQuota, http.StatusForbidden,
				types.ErrOptionWithSkipRetry(), types.ErrOptionWithNoRecordErrorLog())
		}
		errMsg := err.Error()
		if strings.Contains(errMsg, "no active subscription") || strings.Contains(errMsg, "subscription quota insufficient") {
			logPreConsumeRejected(c, s.relayInfo, "subscription", effectiveQuota, err)
			return types.NewErrorWithStatusCode(fmt.Errorf("订阅额度不足或未配置订阅: %s", errMsg), types.ErrorCodeInsufficientUserQuota, http.StatusForbidden, types.ErrOptionWithSkipRetry(), types.ErrOptionWithNoRecordErrorLog())
		}
		logPreConsumeRejected(c, s.relayInfo, "funding_error", effectiveQuota, err)
		return types.NewError(err, types.ErrorCodeUpdateDataError, types.ErrOptionWithSkipRetry())
	}

	s.preConsumedQuota = effectiveQuota

	// ---- 同步 RelayInfo 兼容字段 ----
	s.syncRelayInfo()

	return nil
}

func (s *BillingSession) reserveFunding(delta int) error {
	switch funding := s.funding.(type) {
	case *WalletFunding:
		// 与结算补扣（SettleBilling 正差额 → WalletFunding.Settle）语义一致：
		// 全额无条件扣减，余额不足的部分记为欠费（余额可为负），不中断请求，
		// 保证日志记录的预扣额度与用户余额的实际变动始终对账一致。
		// DecreaseUserQuota 仅在数据库错误时失败。
		if err := model.DecreaseUserQuota(funding.userId, delta, false); err != nil {
			return types.NewError(err, types.ErrorCodeUpdateDataError, types.ErrOptionWithSkipRetry())
		}
		funding.consumed += delta
		return nil
	case *SubscriptionFunding:
		if err := model.PostConsumeUserSubscriptionDelta(funding.subscriptionId, int64(delta)); err != nil {
			return types.NewErrorWithStatusCode(
				fmt.Errorf("订阅额度不足或未配置订阅: %s", err.Error()),
				types.ErrorCodeInsufficientUserQuota,
				http.StatusForbidden,
				types.ErrOptionWithSkipRetry(),
				types.ErrOptionWithNoRecordErrorLog(),
			)
		}
		return nil
	default:
		return types.NewError(fmt.Errorf("unsupported funding source: %s", s.funding.Source()), types.ErrorCodeUpdateDataError, types.ErrOptionWithSkipRetry())
	}
}

func (s *BillingSession) rollbackFundingReserve(delta int) {
	switch funding := s.funding.(type) {
	case *WalletFunding:
		if err := model.IncreaseUserQuota(funding.userId, delta, false); err != nil {
			common.SysLog("error rolling back wallet funding reserve: " + err.Error())
		} else {
			funding.consumed -= delta
		}
	case *SubscriptionFunding:
		if err := model.PostConsumeUserSubscriptionDelta(funding.subscriptionId, -int64(delta)); err != nil {
			common.SysLog("error rolling back subscription funding reserve: " + err.Error())
		}
	}
}

func (s *BillingSession) reserveToken(delta int) error {
	if delta <= 0 || s.relayInfo.IsPlayground {
		return nil
	}
	if err := PreConsumeTokenQuota(s.relayInfo, delta); err != nil {
		return types.NewErrorWithStatusCode(err, types.ErrorCodePreConsumeTokenQuotaFailed, http.StatusForbidden, types.ErrOptionWithSkipRetry(), types.ErrOptionWithNoRecordErrorLog())
	}
	return nil
}

// logPreConsumeRejected 记下一次被预扣挡掉的请求。
//
// 预扣 403 的三个分支都带 ErrOptionWithNoRecordErrorLog（不进 error log 表），
// 而被拒的请求也不写消费日志 —— 也就是说它在数据库里**一行痕迹都没有**。
// 这件事本身是对的（用户自己的余额不足不该刷屏管理端的错误日志），但它让预扣
// 门槛的杀伤面变得不可测量：
//
//	reason=wallet_empty / wallet_threshold / subscription / token 都是余额判据
//	  本身不足。预扣公式补乘 CompletionRatio 之后门槛整体抬高了最多
//	  CompletionRatio 倍，这个计数的同比变化就是那次改动对真人的杀伤面。
//
// 落在后端日志（请求 id 已由 logger 关联），不进 logs 表，不改任何对外报文。
func logPreConsumeRejected(c *gin.Context, info *relaycommon.RelayInfo, reason string, quota int, err error) {
	logger.LogWarn(c, fmt.Sprintf(
		"预扣费被拒 reason=%s user=%d token=%d model=%s group=%s need=%d: %s",
		reason, info.UserId, info.TokenId, info.OriginModelName, info.UsingGroup, quota, err.Error()))
}

// shouldTrust 统一信任额度检查，适用于钱包和订阅。
func (s *BillingSession) shouldTrust(c *gin.Context) bool {
	// 异步任务（ForcePreConsume=true）必须预扣全额，不允许信任旁路
	if s.relayInfo.ForcePreConsume {
		return false
	}

	trustQuota := common.GetTrustQuota()
	if trustQuota <= 0 {
		return false
	}

	// 检查令牌是否充足
	tokenTrusted := s.relayInfo.TokenUnlimited
	if !tokenTrusted {
		tokenQuota := c.GetInt("token_quota")
		tokenTrusted = tokenQuota > trustQuota
	}
	if !tokenTrusted {
		return false
	}

	switch s.funding.Source() {
	case BillingSourceWallet:
		return s.relayInfo.UserQuota > trustQuota
	case BillingSourceSubscription:
		// 订阅不能启用信任旁路。原因：
		// 1. PreConsumeUserSubscription 要求 amount>0 来创建预扣记录并锁定订阅
		// 2. SubscriptionFunding.PreConsume 忽略参数，始终用 s.amount 预扣
		// 3. 若信任旁路将 effectiveQuota 设为 0，会导致 preConsumedQuota 与实际订阅预扣不一致
		return false
	default:
		return false
	}
}

// syncRelayInfo 将 BillingSession 的状态同步到 RelayInfo 的兼容字段上。
func (s *BillingSession) syncRelayInfo() {
	info := s.relayInfo
	info.FinalPreConsumedQuota = s.preConsumedQuota
	info.BillingSource = s.funding.Source()

	if sub, ok := s.funding.(*SubscriptionFunding); ok {
		info.SubscriptionId = sub.subscriptionId
		info.SubscriptionPreConsumed = sub.preConsumed + int64(s.extraReserved)
		info.SubscriptionPostDelta = 0
		info.SubscriptionAmountTotal = sub.AmountTotal
		info.SubscriptionAmountUsedAfterPreConsume = sub.AmountUsedAfter + int64(s.extraReserved)
		info.SubscriptionPlanId = sub.PlanId
		info.SubscriptionPlanTitle = sub.PlanTitle
	} else {
		info.SubscriptionId = 0
		info.SubscriptionPreConsumed = 0
	}
}

// ---------------------------------------------------------------------------
// NewBillingSession 工厂 — 套餐优先,套餐出不了资才走钱包
// ---------------------------------------------------------------------------

// NewBillingSession 创建 BillingSession。
//
// ═════════════ 扣费顺序是**写死**的,不再是每用户的一个设置 ═════════════
//
// 曾经有一个 UserSetting.BillingPreference(subscription_first / wallet_first /
// subscription_only / wallet_only),用户自己能在钱包页改。它被整个去掉了:
// 扣费顺序恒为「套餐有余额且本次用得上 → 扣套餐;否则 → 扣钱包」。
//
// relaykit/dto.UserSetting 里那个字段与库里已有的 JSON 键**刻意保留但不再读取**
// (理由见该字段的注释:侧边栏与语言两条保存路径是 read-modify-write,删字段会让
// 它们把这个键从存量用户的 setting 里静默抹掉;而 relaykit 删公开字段属于 API 破坏)。
//
// 「本次用得上」由 model.PreConsumeUserSubscription 的候选循环回答(范围不匹配、
// 余额不够本次预扣的候选一律跳过),这里只负责在它出不了资时接到钱包上。
func NewBillingSession(c *gin.Context, relayInfo *relaycommon.RelayInfo, preConsumedQuota int) (*BillingSession, *types.NewAPIError) {
	if relayInfo == nil {
		return nil, types.NewError(fmt.Errorf("relayInfo is nil"), types.ErrorCodeInvalidRequest, types.ErrOptionWithSkipRetry())
	}

	// 钱包路径需要先检查用户额度
	tryWallet := func() (*BillingSession, *types.NewAPIError) {
		// 钱包出资闸门:这个模型分组还有没有资格由**钱包**出资。
		//
		// 落点必须是这里而不是别处:钱包出资只有这一个入口(订阅出不了资之后的
		// 唯一去处),而闭包内第一行是唯一没有函数边界可挂的位置。
		//
		// 判据的第一项是**用户分组含不含这个模型分组**:含 → 钱包永远可用,
		// 连 allow_wallet_overflow 都不看。这个人本来就不需要套餐就能用这个分组,
		// 拿"套餐额度用完"去拦他没有道理。
		// 只有「纯靠套餐解锁」的模型分组才轮到 allow_wallet_overflow 说话 ——
		// 那个开关现在住在闸门**内部**(见 qianye/modules/groupns.ModelGroupFundingAllowed),
		// 不再是一个能在外面把闸门结论整段吞掉的覆盖项。
		//
		// 默认实现恒返回 (true, "") ⇒ 逐位等于上游。
		if allowed, reason := QyModelGroupFundingAllowed(
			relayInfo.UserId, relayInfo.UserGroup, relayInfo.UsingGroup); !allowed {
			// **不得**用 ErrorCodeInsufficientUserQuota:「你没有这个分组的出资资格」
			// 与「你余额不足」是两件事,客户端要能分辨 —— 前者续费钱包没有用。
			logPreConsumeRejected(c, relayInfo, "wallet_gate", preConsumedQuota, fmt.Errorf("%s", reason))
			return nil, types.NewErrorWithStatusCode(
				fmt.Errorf("%s", reason), types.ErrorCodeAccessDenied, http.StatusForbidden,
				types.ErrOptionWithSkipRetry(), types.ErrOptionWithNoRecordErrorLog())
		}
		userQuota, err := model.GetUserQuota(relayInfo.UserId, false)
		if err != nil {
			return nil, types.NewError(err, types.ErrorCodeQueryDataError, types.ErrOptionWithSkipRetry())
		}
		if userQuota <= 0 {
			err := fmt.Errorf("用户额度不足, 剩余额度: %s", logger.FormatQuota(userQuota))
			logPreConsumeRejected(c, relayInfo, "wallet_empty", preConsumedQuota, err)
			return nil, types.NewErrorWithStatusCode(
				err, types.ErrorCodeInsufficientUserQuota, http.StatusForbidden,
				types.ErrOptionWithSkipRetry(), types.ErrOptionWithNoRecordErrorLog())
		}
		if userQuota-preConsumedQuota < 0 {
			// 这一档就是预扣**门槛**本身把人挡住。补乘 CompletionRatio 之后门槛
			// 抬高了最多 CompletionRatio 倍,这条计数的同比变化即那次改动的杀伤面。
			err := fmt.Errorf("预扣费额度失败, 用户剩余额度: %s, 需要预扣费额度: %s", logger.FormatQuota(userQuota), logger.FormatQuota(preConsumedQuota))
			logPreConsumeRejected(c, relayInfo, "wallet_threshold", preConsumedQuota, err)
			return nil, types.NewErrorWithStatusCode(
				err, types.ErrorCodeInsufficientUserQuota, http.StatusForbidden,
				types.ErrOptionWithSkipRetry(), types.ErrOptionWithNoRecordErrorLog())
		}
		relayInfo.UserQuota = userQuota

		session := &BillingSession{
			relayInfo: relayInfo,
			funding:   &WalletFunding{userId: relayInfo.UserId},
		}
		if apiErr := session.preConsume(c, preConsumedQuota); apiErr != nil {
			return nil, apiErr
		}
		return session, nil
	}

	trySubscription := func() (*BillingSession, *types.NewAPIError) {
		subConsume := int64(preConsumedQuota)
		if subConsume <= 0 {
			subConsume = 1
		}
		session := &BillingSession{
			relayInfo: relayInfo,
			funding: &SubscriptionFunding{
				requestId: relayInfo.RequestId,
				userId:    relayInfo.UserId,
				modelName: relayInfo.OriginModelName,
				amount:    subConsume,

				usingGroup: relayInfo.UsingGroup,
			},
		}
		// 必须传 subConsume 而非 preConsumedQuota，保证 SubscriptionFunding.amount、
		// preConsume 参数和 FinalPreConsumedQuota 三者一致，避免订阅多扣费。
		if apiErr := session.preConsume(c, int(subConsume)); apiErr != nil {
			return nil, apiErr
		}
		return session, nil
	}

	// ── 写死的扣费顺序 ──────────────────────────────────────────────
	//
	// HasActiveUserSubscription 这次轻量存在性检查**不是**多余的一步:没有它,
	// 一个没有任何订阅的用户(站内绝大多数)每次请求都要先做一次令牌预扣 +
	// 一次带 FOR UPDATE 的订阅事务 + 一次令牌回滚,然后才轮到钱包。
	hasSub, subCheckErr := model.HasActiveUserSubscription(relayInfo.UserId)
	if subCheckErr != nil {
		return nil, types.NewError(subCheckErr, types.ErrorCodeQueryDataError, types.ErrOptionWithSkipRetry())
	}
	if !hasSub {
		return tryWallet()
	}
	session, apiErr := trySubscription()
	if apiErr == nil {
		return session, nil
	}
	// 只有「订阅这一笔出不了资」才回落到钱包。这一档由 preConsume 统一映射成
	// InsufficientUserQuota:no active subscription(竞态到期)、subscription quota
	// insufficient(余额不够本次预扣 / 全部候选被范围跳过)。
	//
	// 其余错误码不回落:令牌额度不足(PreConsumeTokenQuotaFailed)换钱包一样过不去,
	// 数据库错误换一条路只会把同一个故障再犯一次。
	if apiErr.GetErrorCode() != types.ErrorCodeInsufficientUserQuota {
		return nil, apiErr
	}
	return tryWallet()
}
