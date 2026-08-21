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
	preConsumedQuota int  // 实际预扣额度
	tokenConsumed    int  // 令牌额度实际扣减量
	extraReserved    int  // 发送前补充预扣的额度（订阅退款时需要单独回滚）
	fundingSettled   bool // funding.Settle 已成功，资金来源已提交
	tokenSettled     bool // 令牌额度已按差额调整过一次（资金侧失败也算，不能调第二遍）
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
	// 预扣没兜住真实花费 —— 记一条可审计的标记。见 RelayInfo.PreConsumeShortfall:
	// 这条差额是在请求跑完之后强制补收的,余额闸门管不住它,而钱包因此可以被
	// 扣成负数。它不是错误(服务确实提供了),但必须留下痕迹,否则事后既看不出
	// 放大倍数,也算不出坏账。
	if delta > 0 && s.relayInfo != nil && s.relayInfo.PreConsumeShortfall == nil {
		s.relayInfo.PreConsumeShortfall = &relaycommon.ReservationShortfall{
			Reserved:  s.preConsumedQuota,
			Charged:   actualQuota,
			Shortfall: delta,
		}
	}
	if delta == 0 && !s.fundingHasPartialReserve() {
		s.settled = true
		return nil
	}
	// 1) 调整资金来源（仅在尚未提交时执行，防止重复调用）
	//
	// 资金侧失败**不再**直接 return:令牌额度与它是两笔独立的账,把令牌那一步
	// 一起跳过只会让 token.remain_quota 永久停在预扣值(正差额时用户白得一截
	// 消费上限,负差额时被多占一截)。资金侧的失败仍然原样返回给调用方,
	// 由 relayInfo.SettleFailure 落进日志的 admin_info(见 attachSettleFailure)。
	var fundingErr error
	if !s.fundingSettled {
		if err := s.funding.Settle(delta); err != nil {
			fundingErr = err
		} else {
			s.fundingSettled = true
		}
	}
	// 2) 调整令牌额度（tokenSettled 保证它至多执行一次：资金侧失败时本次不置
	//    settled，调用方若再来一次结算，令牌不能被调第二遍）
	var tokenErr error
	if !s.tokenSettled && !s.relayInfo.IsPlayground && delta != 0 {
		s.tokenSettled = true
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
		s.relayInfo.SubscriptionWrittenOff += sub.SettleWrittenOff()
	}
	if fundingErr != nil {
		// 不置 settled:资金侧确实没收到钱,这一笔不该被标成"已结清"。
		return fundingErr
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

	if s.settled || s.refunded || targetQuota <= s.preConsumedQuota {
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
// PreConsume — 统一预扣费入口
// ---------------------------------------------------------------------------

// preConsume 执行预扣费：令牌预扣 -> 资金来源预扣。
// 任一步骤失败时原子回滚已完成的步骤。
func (s *BillingSession) preConsume(c *gin.Context, quota int) *types.NewAPIError {
	effectiveQuota := quota

	// ══════════ 这里曾经有一条「信任额度旁路」,已经删除 ══════════
	//
	// 旧逻辑:余额(或令牌余额)超过 common.GetTrustQuota()(硬编码 10×QuotaPerUnit
	// = $10)时把 effectiveQuota 置 0,于是令牌预扣与资金预扣**两步一起跳过** ——
	// model.TryReserveUserQuota 那把 `WHERE quota >= ?` 的原子闩根本没被调用。
	//
	// 后果是全站唯一的额度上限在并发下彻底失效:N 路请求各自读到同一个余额快照、
	// 各自判定「这个人付得起」,中间没有任何预占把额度扣住,结算时
	// model.DecreaseUserQuota 无下限地扣。实测余额 $10.000002 的账号 50 路并发
	// 打到 -$90,200 路打到 -$328,损失随并发线性放大且无上界;令牌
	// remain_quota 这条「这把 key 最多花多少」的硬约束同样被击穿到负数。
	//
	// 旁路省下的是每请求一次原子预留的写入 —— 拿一个可被任意放大的资损换它,
	// 不是一笔划算的买卖。余额充足的用户走同一条预留路径只会成功,
	// 唯一被改变的就是「余额到底够不够」这件事从此由数据库说了算。
	if effectiveQuota > 0 {
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

// requirePartialReserveBacked 挡住「套餐只出得起一部分,而剩下那部分谁都付不起」
// 的请求。
//
// ═══════════ 它补的是哪一道被跳过的门槛 ═══════════
//
// 钱包那条路强制 userQuota >= preConsumedQuota(tryWallet 的 wallet_threshold
// 那一档)。套餐这条路只要还剩 >= 1 个 quota,pickFundingSubscription 的第二轮
// 就按剩余额度**部分预扣**并放行整个请求,而 NewBillingSession 的路由是
// 「订阅出得了资就不再看钱包」—— 于是钱包余额判据在预扣阶段一次都不会被问到。
//
// 真实花费在结算时才落地(settleSubscriptionDelta):撞到 amount_total 上限的
// 那一段无条件扣钱包(余额可为负)。实测:钱包 0 + 一张 remain=1 的套餐,
// 真打上游一条 gemini-3.1-pro-high 的长请求 → HTTP 200,users.quota 0 → −190,300。
// 同一个账号把套餐拿掉就是 403。也就是说尾数那 1 个 quota 换来了一次
// **不受任何余额判据约束**的请求。
//
// ═══════════ 判据:总可用资金必须覆盖预扣估算额 ═══════════
//
// 套餐吃下 preConsumed,剩下的 shortfall = amount − preConsumed 由钱包兜底,
// 所以要求 userQuota >= shortfall —— 与纯钱包路径「userQuota >= 预扣额」是同一
// 条规则,只是套餐先付掉了一部分。
//
// 这**不会**把「尾数必须花得掉」推翻:钱包里有钱的人照旧走部分预扣把尾数花光
// (实测的 10,000 quota 钱包对 1 quota 的尾数绰绰有余),被挡住的只有"两边都
// 付不起"的那一类 —— 而他在没有第二轮的年代本来就会被 403。
//
// ═══════════ 闸门说"钱包不得补收"时不拦 ═══════════
//
// 那一档差额按配置由平台核销(model.ClaimSubscriptionWriteOff 每个重置周期
// 只发一份名额),运营就是要让这类分组不花用户的钱;拿钱包余额去拦他等于
// 把那个配置反过来用。
func (s *BillingSession) requirePartialReserveBacked(c *gin.Context) *types.NewAPIError {
	sub, ok := s.funding.(*SubscriptionFunding)
	if !ok {
		return nil
	}
	shortfall := sub.amount - sub.preConsumed
	if shortfall <= 0 {
		return nil
	}
	info := s.relayInfo
	if !QyWalletMayCoverSubscriptionShortfall(info.UserId, info.UserGroup, info.UsingGroup, shortfall) {
		return nil
	}
	userQuota, err := model.GetUserQuota(info.UserId, false)
	if err != nil {
		return types.NewError(err, types.ErrorCodeQueryDataError, types.ErrOptionWithSkipRetry())
	}
	if int64(userQuota) >= shortfall {
		info.UserQuota = userQuota
		return nil
	}
	// 拒绝之前必须把已经预扣掉的套餐额度与令牌额度退回去 —— 这一笔请求根本没跑。
	//
	// 同步退,不走 Refund 的异步 gopool:这条路上会话还没有交给任何人,退款
	// 落库之前就返回 403 的话,用户紧接着的下一次请求会读到一个仍然被占着的
	// 尾数,表现成"明明有余额却一直被拒"。s.refunded 顺手置上,杜绝二次退款。
	s.refunded = true
	if err := s.funding.Refund(); err != nil {
		common.SysLog("error refunding subscription pre-consume after partial reserve was rejected: " + err.Error())
	}
	if s.tokenConsumed > 0 && !info.IsPlayground {
		if err := model.IncreaseTokenQuota(info.TokenId, info.TokenKey, s.tokenConsumed); err != nil {
			common.SysLog("error rolling back token quota after partial reserve was rejected: " + err.Error())
		}
	}
	s.tokenConsumed = 0
	// 额度列是 32 位;走 quota_math 的饱和转换而不是裸 int(),越界会被夹住并留痕。
	rejectErr := fmt.Errorf("预扣费额度失败, 套餐余额仅够 %s, 钱包剩余额度 %s 不足以补足差额 %s",
		logger.FormatQuota(common.QuotaFromFloat(float64(sub.preConsumed))),
		logger.FormatQuota(userQuota),
		logger.FormatQuota(common.QuotaFromFloat(float64(shortfall))))
	logPreConsumeRejected(c, info, "subscription_partial_uncovered", s.preConsumedQuota, rejectErr)
	return types.NewErrorWithStatusCode(
		rejectErr, types.ErrorCodeInsufficientUserQuota, http.StatusForbidden,
		types.ErrOptionWithSkipRetry(), types.ErrOptionWithNoRecordErrorLog())
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
			// 「余额用完了」与「你已经欠费了」对用户是两件事,文案必须分开。
			//
			// 本站刻意接受结算把余额扣成负数(拍板与代价见
			// qianye/docs/decisions.md 的 D-01),所以 userQuota < 0 是一个
			// **正常会发生**的状态,不是脏数据。此前两档共用一句
			// 「用户额度不足, 剩余额度: -$1.23」——用户看到一个带负号的余额,
			// 既不知道那是欠款,也不知道充值 1 块钱为什么还是被拒
			// (充完仍然是负的)。欠费档必须自己说清楚:欠多少、要补多少。
			//
			// 错误码仍然是 InsufficientUserQuota:客户端的重试/降级策略按码走,
			// 为了分辨文案而换码会让所有既有集成在这一档上改变行为。
			// 区分只落在人读的 message 与审计用的 reason 上。
			if userQuota < 0 {
				err := fmt.Errorf("账户已透支, 当前欠费 %s, 请充值补足欠款后再继续调用",
					logger.FormatQuota(-userQuota))
				logPreConsumeRejected(c, relayInfo, "wallet_overdrawn", preConsumedQuota, err)
				return nil, types.NewErrorWithStatusCode(
					err, types.ErrorCodeInsufficientUserQuota, http.StatusForbidden,
					types.ErrOptionWithSkipRetry(), types.ErrOptionWithNoRecordErrorLog())
			}
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
				// 结算差额撞上限时要问「用户分组含不含 usingGroup」,零值会被
				// 闸门读成"不含",所以这里必须给真值。
				userGroup: relayInfo.UserGroup,
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
		if apiErr := session.requirePartialReserveBacked(c); apiErr != nil {
			return nil, apiErr
		}
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
