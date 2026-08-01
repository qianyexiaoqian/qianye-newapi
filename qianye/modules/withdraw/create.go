package withdraw

import (
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/qianye/config"
	"github.com/QuantumNous/new-api/qianye/db"
	qymodel "github.com/QuantumNous/new-api/qianye/model"
	"github.com/QuantumNous/new-api/qianye/modules/commission"
	"github.com/QuantumNous/new-api/qianye/service/audit"
	"github.com/QuantumNous/new-api/qianye/service/twophase"

	"github.com/gin-gonic/gin"
	"gorm.io/gorm"
)

// idemScope 与 qy_withdrawals.(idem_scope, idem_key) 的双列唯一索引配套(裁定 C11)。
const idemScope = "withdraw"

// dailySubmitFactor 是"提交总数"相对 daily_max_count 的放宽倍数。
//
// 已撤销的单刻意不计入 daily_max_count —— 填错收款信息撤销重填是正常操作,
// 把它算进次数等于惩罚一次手滑。但完全不计会让"申请-撤销"变成一个无上限循环:
// 每一轮都写单据、写事件、冻结再解冻佣金,而次数闸门永远看不见它。
// 因此再加一道只看提交总数(含已撤销)的宽松闸门:允许手滑,不允许刷。
//
// 固化在代码里而不是新增配置项:它的语义完全依附于 daily_max_count,
// 单独放出来会变成又一个"定义了却没人调"的旋钮。
const dailySubmitFactor = 4

// create 提交一笔提现申请。
//
// 关键设计:申请阶段【完全不碰主库】,全部副作用都在一个扩展库事务里完成。
// 佣金在这一刻就离开 available,之后无论兑现阶段发生什么,用户都不可能拿同一笔
// 佣金再发一单。最坏情况只是佣金卡在 frozen 需要人工处理,永远不会变成
// "佣金可无限重复领取"。
func create(c *gin.Context, userId int, req createRequest) (*orderView, error) {
	cfg := config.Get().Withdraw
	acc, err := acceptCreate(req, cfg)
	if err != nil {
		return nil, err
	}

	user, err := model.GetUserById(userId, false)
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, errUserUnavailable
		}
		return nil, err
	}
	if user.Status != common.UserStatusEnabled {
		return nil, errUserUnavailable
	}

	// 锁外余额预检只为尽早给出明确错误。真正的准入判定是 FreezeForWithdraw
	// 内部的 `WHERE available_quota >= ?` CAS —— 两次读之间余额可能被并发改动。
	available, err := commission.Withdrawable(userId)
	if err != nil {
		return nil, err
	}
	if available < acc.Quota {
		return nil, errInsufficient
	}

	w, payee, err := buildWithdrawal(c, user, acc, cfg)
	if err != nil {
		return nil, err
	}

	var replay *Withdrawal
	err = db.Get().Transaction(func(tx *gorm.DB) error {
		var txErr error
		replay, txErr = submitInTx(tx, w, payee, acc, cfg, user.Username)
		return txErr
	})
	if err != nil {
		if isDuplicateKey(err) {
			// 预读没看到而唯一索引挡下了:两个完全相同的请求同时进来。
			// 返回原单而不是报错 —— 这是双击、多标签、客户端超时重试的正常结果。
			return loadByIdemKey(userId, acc)
		}
		db.MarkFailure(err)
		return nil, err
	}
	if replay != nil {
		// 幂等命中:本次没有产生任何副作用,因此也不写 submit 审计 ——
		// 审计表是事后仲裁的唯一凭据,一次点击重试三次不该看起来像申请了三笔。
		if err := ensureReplayMatches(replay, acc); err != nil {
			return nil, err
		}
		return toUserView(replay, nil), nil
	}

	audit.Write(c, audit.Entry{
		TraceNo:      w.WithdrawNo,
		Category:     qymodel.AuditCategoryWithdraw,
		Action:       "withdraw.submit",
		ActorType:    qymodel.ActorUser,
		ActorUserId:  userId,
		ActorName:    user.Username,
		TargetUserId: userId,
		AmountQuota:  w.Quota,
		AmountFiat:   w.NetAmount,
		Currency:     w.Currency,
		FrozenRate:   w.FrozenFxRate,
		Result:       qymodel.ResultOK,
	})
	return toUserView(w, nil), nil
}

// submitInTx 是提交申请的全部扩展库副作用,跑在同一个事务里。
//
// 顺序不变量:【幂等重放判定必须排在风控闸门之前】。
// 同一个 client_request_id 的重试是原单的重放,不是第二笔申请。放在闸门之后的话,
// 原单自己占掉的冷却窗口与日限额会把它自己的重试判成违规 —— 用户看到的是
// "提现失败",而单其实已经落库、佣金已经冻结。这条顺序写反了只在冷却窗口内
// 复现,人工测试极难碰上,所以必须由回归测试钉住。
//
// 返回非 nil 的 *Withdrawal 表示本次是重放,调用方应原样回原单且不写审计。
func submitInTx(tx *gorm.DB, w *Withdrawal, payee *Payee, acc acceptedRequest,
	cfg config.Withdraw, actorName string) (*Withdrawal, error) {

	replay, err := findByIdemKey(tx, w.UserId, acc.IdemKey)
	if err != nil || replay != nil {
		return replay, err
	}
	if err := enforceCreateLimits(tx, w.UserId, w.Quota, cfg, w.CreatedAt); err != nil {
		return nil, err
	}
	// 先落单再冻结:单据的唯一索引冲突要在动佣金之前就把事务打断,
	// 否则一次重复提交会白白走一遍冻结再回滚。
	if err := tx.Create(w).Error; err != nil {
		return nil, err
	}
	if payee != nil {
		payee.WithdrawalId = w.Id
		if err := tx.Create(payee).Error; err != nil {
			return nil, err
		}
	}
	// 凭证绑定要在同一个事务里,且必须在 Create 之后 —— 它认领的是 w.Id。
	// CAS 失败(凭证不存在 / 不是本人的 / 已被别的单用掉)会让整笔申请回滚,
	// 于是 w.HasProof 这份冗余永远不会与凭证表脱节。
	if acc.ProofRef != "" {
		if err := bindProof(tx, w, acc.ProofRef); err != nil {
			return nil, err
		}
	}
	if err := commission.FreezeForWithdraw(tx, w.UserId, w.Quota, w.WithdrawNo); err != nil {
		return nil, err
	}
	return nil, writeEvent(tx, w, transition{
		To:        StatusPending,
		Action:    ActionSubmit,
		ActorType: qymodel.ActorUser,
		ActorId:   w.UserId,
		ActorName: actorName,
		IP:        w.ClientIp,
		Detail:    submitDetail(w),
	})
}

// buildWithdrawal 组装待落库的提现单与收款信息快照。
func buildWithdrawal(c *gin.Context, user *model.User, acc acceptedRequest, cfg config.Withdraw) (
	*Withdrawal, *Payee, error) {

	rates, err := freezeRates()
	if err != nil {
		return nil, nil, err
	}

	kind := qymodel.KindWithdrawQuota
	if acc.Method == config.WithdrawMethodFiat {
		kind = qymodel.KindWithdrawFiat
	}
	now := common.GetTimestamp()
	w := &Withdrawal{
		// 单号用 crypto/rand 派生(裁定 C9),禁止 common.GetRandomString ——
		// math/rand 可预测,资金单号可预测意味着可以被枚举和伪造。
		WithdrawNo:         twophase.NewOrderNo(kind),
		IdemScope:          idemScope,
		IdemKey:            idemKeyOf(user.Id, acc.IdemKey),
		UserId:             user.Id,
		Username:           truncate(user.Username, 64),
		Method:             acc.Method,
		Status:             StatusPending,
		Quota:              acc.Quota,
		FrozenQuotaPerUnit: rates.QuotaPerUnit,
		FrozenFxRate:       rates.FxRate,
		Remark:             acc.Remark,
		HasProof:           acc.ProofRef != "",
		ClientIp:           truncate(c.ClientIP(), 64),
		CreatedAt:          now,
		UpdatedAt:          now,
	}

	if acc.Method != config.WithdrawMethodFiat {
		return w, nil, nil
	}

	amounts, err := computeFiat(acc.Quota, rates, cfg.FiatFeeBps)
	if err != nil {
		return nil, nil, err
	}
	minAmount, err := minFiatAmount()
	if err != nil {
		return nil, nil, err
	}
	if amounts.Gross.LessThan(minAmount) {
		return nil, nil, errFiatBelowMin
	}
	w.Currency = cfg.FiatCurrency
	w.GrossAmount, w.FeeAmount, w.NetAmount = amounts.Gross, amounts.Fee, amounts.Net
	w.FeeBps = cfg.FiatFeeBps

	channel, data, err := resolvePayee(user.Id, acc)
	if err != nil {
		return nil, nil, err
	}
	payee, err := buildPayeeSnapshot(w.WithdrawNo, user.Id, channel, data)
	if err != nil {
		return nil, nil, err
	}
	w.PayeeChannel = channel
	w.PayeeMasked = payee.Masked
	w.PayeeDigest = payee.Digest
	w.RiskFlags = digestRiskFlag(user.Id, payee.Digest)
	return w, payee, nil
}

// dailyUsage 是用户当日的提现用量快照。
//
// 三个口径一次查出来:分成三条 COUNT/SUM 只是把同一张表按同一个 WHERE 扫三遍,
// 而且更容易出现"这条加了排除撤销、那条忘了"的口径漂移。
type dailyUsage struct {
	// Submitted 是今日提交的全部申请数,含已撤销 —— 用来堵"申请-撤销"循环。
	Submitted int64
	// Active 不含已撤销,对应 daily_max_count 的口径。
	Active int64
	// Quota 是不含已撤销的额度合计,对应 daily_max_quota 的口径。
	Quota int64
}

func loadDailyUsage(tx *gorm.DB, userId int) (dailyUsage, error) {
	var u dailyUsage
	err := tx.Model(&Withdrawal{}).
		Select("COUNT(*) AS submitted, "+
			"COALESCE(SUM(CASE WHEN status <> ? THEN 1 ELSE 0 END), 0) AS active, "+
			"COALESCE(SUM(CASE WHEN status <> ? THEN quota ELSE 0 END), 0) AS quota",
			StatusCancelled, StatusCancelled).
		Where("user_id = ? AND created_at >= ?", userId, dayStart()).
		Scan(&u).Error
	return u, err
}

// enforceCreateLimits 是申请阶段的额度与频率闸门。
//
// 必须与落单在【同一个扩展库事务】内:判定与写入之间只要存在提交间隙,
// 并发请求就会同时读到旧计数、同时通过校验 —— 这与 checkDailyCount 一直以来
// 待在事务里的理由完全一样。
//
// 诚实说明它挡不住什么:同事务不等于串行化。两笔并发申请可能都在各自的快照里
// 读到旧计数,随后被 FreezeForWithdraw 的余额行锁排成一队先后提交,于是限额被
// 多放行一笔。要根治得在判定之前就锁住该用户的余额行,而那把锁属于 commission
// 模块。频率闸门多放行一笔不构成资损(佣金冻结才是准入判定),因此按当前形态
// 收敛;真正的硬闸门是 FreezeForWithdraw 的 `WHERE available_quota >= ?`。
//
// 这四项(max_quota_per_order 在 acceptCreate、其余三项在这里)此前定义了、
// 校验了、赋了默认值,却没有任何消费方。运维看着一份写满上限的 YAML,
// 实际上一道闸门都没有关。
func enforceCreateLimits(tx *gorm.DB, userId int, quota int64, cfg config.Withdraw, now int64) error {
	usage, err := loadDailyUsage(tx, userId)
	if err != nil {
		return err
	}
	if cfg.DailyMaxCount > 0 {
		if usage.Active >= int64(cfg.DailyMaxCount) {
			return errDailyCountReached
		}
		if usage.Submitted >= int64(cfg.DailyMaxCount)*dailySubmitFactor {
			return errDailySubmitReached
		}
	}
	if cfg.DailyMaxQuota > 0 && usage.Quota+quota > cfg.DailyMaxQuota {
		return errDailyQuotaReached
	}

	if cfg.CooldownSecs > 0 {
		var recent int64
		// 冷却窗口刻意把已撤销的单也算进来:"撤销后立刻重发"正是刷单循环的驱动力,
		// 按 status 过滤等于专门给这个循环留一道门。
		if err := tx.Model(&Withdrawal{}).
			Where("user_id = ? AND created_at > ?", userId, now-int64(cfg.CooldownSecs)).
			Count(&recent).Error; err != nil {
			return err
		}
		if recent > 0 {
			return errCooldown
		}
	}

	if cfg.MaxPendingOrders > 0 {
		var pending int64
		if err := tx.Model(&Withdrawal{}).
			Where("user_id = ? AND status IN ?", userId, activeStatuses).
			Count(&pending).Error; err != nil {
			return err
		}
		if pending >= int64(cfg.MaxPendingOrders) {
			return errPendingLimit
		}
	}
	return nil
}

// dayStart 返回服务器本地时区今日 0 点的 unix 秒。
//
// 用本地时区而不是 UTC:"今天最多提 3 次"是给运营和用户看的口径,
// 跟着服务器所在地走才不会出现"晚上八点就换天了"的困惑。
func dayStart() int64 {
	now := time.Now()
	return time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, now.Location()).Unix()
}

func submitDetail(w *Withdrawal) string {
	return common.MapToJsonStr(map[string]any{
		"method":                w.Method,
		"quota":                 w.Quota,
		"currency":              w.Currency,
		"frozen_fx_rate":        w.FrozenFxRate.String(),
		"frozen_quota_per_unit": w.FrozenQuotaPerUnit.String(),
		"gross_amount":          w.GrossAmount.String(),
		"fee_amount":            w.FeeAmount.String(),
		"net_amount":            w.NetAmount.String(),
		"fee_bps":               w.FeeBps,
	})
}

// idemKeyOf 拼出落库的幂等键。用户 id 必须是前缀:client_request_id 由前端生成,
// 不带 user 前缀的话,两个用户碰巧用了同一个 UUID 就会互相顶掉对方的申请。
func idemKeyOf(userId int, clientKey string) string {
	return strconv.Itoa(userId) + ":" + clientKey
}

// findByIdemKey 按幂等键取原单。没有原单时返回 (nil, nil)。
func findByIdemKey(tx *gorm.DB, userId int, clientKey string) (*Withdrawal, error) {
	var w Withdrawal
	err := tx.Where("idem_scope = ? AND idem_key = ?", idemScope, idemKeyOf(userId, clientKey)).
		Take(&w).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &w, nil
}

// loadByIdemKey 在唯一索引冲突后回读已有单。
func loadByIdemKey(userId int, acc acceptedRequest) (*orderView, error) {
	w, err := findByIdemKey(db.Get(), userId, acc.IdemKey)
	if err != nil {
		db.MarkFailure(err)
		return nil, fmt.Errorf("qianye/withdraw: 幂等冲突但无法回读原单: %w", err)
	}
	if w == nil {
		// 唯一索引刚刚拦下、回读却查不到,只能是原单在这两步之间被删掉了。
		// 这是数据异常,不能静默当成"没申请过"再放一次。
		return nil, fmt.Errorf("qianye/withdraw: 幂等冲突但原单不存在(用户 %d)", userId)
	}
	if err := ensureReplayMatches(w, acc); err != nil {
		return nil, err
	}
	return toUserView(w, nil), nil
}

// ensureReplayMatches 校验这次重放确实是同一笔申请。
//
// 唯一索引只保证"不重复执行",保证不了"重放的是同一个请求":client_request_id
// 由前端在打开弹窗时生成并缓存(裁定 C10),用户在同一个弹窗里改完金额再提交
// 仍然沿用它。此时把原单当成本次结果返回,用户会把"那笔 300 的申请"读成
// "我这笔 500 成功了" —— 必须 409 让前端刷新。
func ensureReplayMatches(w *Withdrawal, acc acceptedRequest) error {
	if w.Quota != acc.Quota || w.Method != acc.Method {
		return errIdemConflict
	}
	return nil
}

// isDuplicateKey 判断错误是否为唯一索引冲突。
// MySQL 驱动不导出结构化错误码,只能按文本匹配 —— 与地基 twophase 保持一致。
func isDuplicateKey(err error) bool {
	if err == nil {
		return false
	}
	msg := strings.ToLower(err.Error())
	return strings.Contains(msg, "duplicate entry") ||
		strings.Contains(msg, "error 1062") ||
		strings.Contains(msg, "duplicate key")
}
