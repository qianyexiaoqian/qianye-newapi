package withdraw

import (
	"errors"
	"fmt"
	"strconv"
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

	w, payee, err := buildWithdrawal(c, user, acc, cfg)
	if err != nil {
		return nil, err
	}

	// 顺序不变量(与 submitInTx 里那条同源,只是这里是它的上一层):
	// **幂等重放的判定必须排在任何余额/风控判据之前**。
	//
	// 申请一旦落库，佣金就已经离开 available 进了 frozen。此后同一个
	// client_request_id 的重放(客户端超时后在同一个弹窗里再点一次，裁定 C10
	// 要求沿用同一个键)如果先撞上锁外余额预检，就会因为“剩余可提余额已经少了
	// 这一笔”而被判成 qy_wd_insufficient_commission —— 只要申请额度超过剩余可提
	// 余额的一半就必然发生，而“提全部佣金”正是本页最常见的意图。用户看到的是
	// “佣金不足”，同时他的佣金确实被冻住、账户页可提余额确实变少了，三者互相
	// 矛盾；而设计稿写明这里应当返回 200 + 原单。
	//
	// 同一道错序还会把 ensureReplayMatches 的 409 qy_wd_idem_conflict 按金额大小
	// 随机遮成 400，并让 debt_blocked 用户永远看不到为他定义的 qy_wd_debt_blocked
	// (Withdrawable 对欠账账号返回 0，预检先短路)。
	if existing, ferr := findByIdemKey(db.Get(), user.Id, acc.IdemKey); ferr != nil {
		db.MarkFailure(ferr)
		return nil, ferr
	} else if existing != nil {
		if err := ensureReplayMatches(existing, w); err != nil {
			return nil, err
		}
		return toUserView(existing, nil), nil
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
			return loadByIdemKey(w, acc)
		}
		db.MarkFailure(err)
		// 被风控闸门(冷却、日限额、频次)挡下的申请在此之前零留痕:
		// 审计里只有成功提交的那些,于是"这个账号在被封之前连续尝试提现 30 次"
		// 事后完全查不到。单号已经生成,失败那条与将来可能成功的那条共用
		// 同一个 trace_no,时间线能串起来。
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
			Result:       qymodel.ResultFail,
			Reason:       audit.Truncate("提现申请被拒: "+err.Error(), 500),
		})
		return nil, err
	}
	if replay != nil {
		// 幂等命中:本次没有产生任何副作用,因此也不写 submit 审计 ——
		// 审计表是事后仲裁的唯一凭据,一次点击重试三次不该看起来像申请了三笔。
		if err := ensureReplayMatches(replay, w); err != nil {
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
// # 顺序不变量一:佣金余额行锁必须是本事务的第一条语句
//
// 风控闸门(冷却 / 日笔数 / 日额度 / 未终态单数)全是"先 COUNT 再 INSERT"。
// 此前第一把行锁在 FreezeForWithdraw 里,也就是排在闸门【之后】:并发的 N 个
// 事务在 MySQL 的 REPEATABLE READ 快照里各读各的旧计数,一起通过闸门,再被
// 余额行锁排成一队先后落单 —— 实测 8 并发全过,daily_max_count=3 与
// max_pending_orders=3 双双失效。余额行锁本身仍然兜住资损(佣金不会超发),
// 但日额度、冷却、反刷号这三道闸是空的。
//
// 把同一把锁提到最前面,闸门就与落单共享了这把锁。它必须在 findByIdemKey
// 之前:那是一次普通查询,MySQL 的快照建立在事务的第一次一致性读那一刻,
// 让它跑在加锁前,后面的计数读到的仍是旧快照,提锁就白提了(见
// commission.LockBalance)。
//
// # 顺序不变量二:幂等重放判定必须排在风控闸门之前
//
// 同一个 client_request_id 的重试是原单的重放,不是第二笔申请。放在闸门之后的话,
// 原单自己占掉的冷却窗口与日限额会把它自己的重试判成违规 —— 用户看到的是
// "提现失败",而单其实已经落库、佣金已经冻结。这条顺序写反了只在冷却窗口内
// 复现,人工测试极难碰上,所以必须由回归测试钉住。
//
// 两条不变量不冲突:加锁不是闸门,重放判定仍然排在所有闸门之前。
//
// 返回非 nil 的 *Withdrawal 表示本次是重放,调用方应原样回原单且不写审计。
func submitInTx(tx *gorm.DB, w *Withdrawal, payee *Payee, acc acceptedRequest,
	cfg config.Withdraw, actorName string) (*Withdrawal, error) {

	if err := commission.LockBalance(tx, w.UserId); err != nil {
		return nil, err
	}
	replay, err := findByIdemKey(tx, w.UserId, acc.IdemKey)
	if err != nil || replay != nil {
		return replay, err
	}
	if err := enforceCreateLimits(tx, w.UserId, w.Quota, cfg, w.CreatedAt); err != nil {
		return nil, err
	}
	// 定价必须在落单之前、且在同一把余额行锁之内:金额取自账本的
	// available_fiat,锁外算出来的数与稍后 FreezeForWithdraw 真正削走的数
	// 可能已经被一次并发结算改开。
	if err := priceFiatInTx(tx, w, cfg); err != nil {
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
	// 共用收款账号的标记必须在本单落库之后重算:算在落库之前的话,
	// 同一张卡上时间最早的那张单永远数不到别人,于是永不带标记(见 markSharedPayee)。
	if err := markSharedPayee(tx, w); err != nil {
		return nil, err
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

	perUnit, err := frozenQuotaPerUnit()
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
		FrozenQuotaPerUnit: perUnit,
		Remark:             acc.Remark,
		HasProof:           acc.ProofRef != "",
		ClientIp:           truncate(c.ClientIP(), 64),
		CreatedAt:          now,
		UpdatedAt:          now,
	}

	if acc.Method != config.WithdrawMethodFiat {
		return w, nil, nil
	}

	// 金额三段与币种不在这里定 —— 它们只能由佣金账本在持锁事务内给出,
	// 见 priceFiatInTx。这里只先把与账本无关的部分组装好。
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
	// RiskFlags 由 markSharedPayee 在落单之后算(见那里的说明):
	// 在这里算的话,同一张卡上时间最早的那张单永远数不到别人。
	return w, payee, nil
}

// priceFiatInTx 按佣金账本给这张法币单定金额与币种。
//
// # 为什么金额只能来自账本,而且只能在事务里定
//
// 用户在「我的推广」页看到的 available_fiat,是按**计佣当刻**的三层折算比例
// (分组档 → 兜底档 → 全站汇率)一笔笔攒起来的绝对值;冻结时按额度比例等比
// 削走。提现单要付的就是被削走的那一笔 —— 两侧同源,"这笔提现打多少钱"
// 全站才只有一个答案。
//
// 此前这里是另一套独立计价(quota / QuotaPerUnit × 充值页汇率),与账本毫无
// 关系:配一个 8.5 的分组结汇档,账面冻走 850 CNY 而单据只开 100 CNY;
// 把充值汇率从 1 改到 7.3,同一笔按比例 1 冻结的余额开出 7.3 倍的单。
// 默认配置下两者恰好相等,所以它是"配置即触发"的潜伏错价,而不是装完就错。
//
// 事务内定价还有第二个理由:锁外读到的余额与 FreezeForWithdraw 真正削走的数
// 之间隔着一次可能的并发结算/冲正。同一把行锁之内两次读同一行,两个数才必然相等。
//
// method != fiat 的单没有法币侧,直接跳过。
func priceFiatInTx(tx *gorm.DB, w *Withdrawal, cfg config.Withdraw) error {
	if w.Method != config.WithdrawMethodFiat {
		return nil
	}
	quote, err := commission.QuoteWithdrawFiat(tx, w.UserId, w.Quota)
	if err != nil {
		return err
	}
	amounts, err := splitFiat(quote.Amount, cfg.FiatFeeBps)
	if err != nil {
		return err
	}
	minAmount, err := minFiatAmount(cfg.MinFiatAmount)
	if err != nil {
		return err
	}
	if amounts.Gross.LessThan(minAmount) {
		return errFiatBelowMin
	}
	w.Currency = quote.Currency
	w.GrossAmount, w.FeeAmount, w.NetAmount = amounts.Gross, amounts.Fee, amounts.Net
	w.FeeBps = cfg.FiatFeeBps
	w.FrozenFxRate = impliedFxRate(amounts.Gross, w.Quota, w.FrozenQuotaPerUnit)
	return nil
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
// 调用约定:必须与落单在【同一个扩展库事务】内,且调用点已经持有该用户的佣金
// 余额行锁(submitInTx 的第一条语句)。两个条件缺一不可 ——
//
//	只同事务不加锁:并发申请各在各的 REPEATABLE READ 快照里读到旧计数,
//	                一起通过闸门,实测 8 并发全过;
//	只加锁不同事务:判定与落单之间存在提交间隙,别人可以插进来。
//
// 早先这里写着"同事务不等于串行化,但多放行一笔不构成资损,故按当前形态收敛"。
// 前半句是对的,后半句站不住:资损确实由 FreezeForWithdraw 的
// `WHERE available_quota >= ?` 兜住,但日额度、冷却与反刷号这三道闸要拦的
// 本来就不是资损,而是"一个账号一天能发多少笔"。它们被绕过时,余额闸一无所知。
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

// loadByIdemKey 在唯一索引冲突后回读已有单。incoming 是本次组装好的单,
// 只用来判定"重放的是不是同一笔申请"。
func loadByIdemKey(incoming *Withdrawal, acc acceptedRequest) (*orderView, error) {
	w, err := findByIdemKey(db.Get(), incoming.UserId, acc.IdemKey)
	if err != nil {
		db.MarkFailure(err)
		return nil, fmt.Errorf("qianye/withdraw: 幂等冲突但无法回读原单: %w", err)
	}
	if w == nil {
		// 唯一索引刚刚拦下、回读却查不到,只能是原单在这两步之间被删掉了。
		// 这是数据异常,不能静默当成"没申请过"再放一次。
		return nil, fmt.Errorf("qianye/withdraw: 幂等冲突但原单不存在(用户 %d)", incoming.UserId)
	}
	if err := ensureReplayMatches(w, incoming); err != nil {
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
//
// 收款方式同理,而且更要命:金额改错了用户至少看得见数字不对,收款人改错了
// 界面上只有一串脱敏值,而钱会照着**原来那张卡**打出去。同一个弹窗里换一张
// 收款账号再提交,client_request_id 一个字都没变。此前这里只比金额与方式,
// 漏掉的正是这条链路上唯一决定"钱去哪"的字段。
//
// 比的是指纹而不是 payee_ref:同一张卡存两次会得到两个 ref,但指纹相同 ——
// 那本来就是同一个收款目的地,不该判成冲突。指纹的口径是"钱去哪"(该渠道的
// 账号字段,见 payeeDigest),所以只把户名写法改了一下的重放同样不算冲突:
// 钱去的仍是同一个账号,而这里要挡的是"钱去了另一个账号"。
func ensureReplayMatches(replay, incoming *Withdrawal) error {
	if replay.Quota != incoming.Quota ||
		replay.Method != incoming.Method ||
		replay.PayeeDigest != incoming.PayeeDigest {
		return errIdemConflict
	}
	return nil
}

// isDuplicateKey 判断错误是否为唯一索引冲突。
//
// 判据统一收在 db.IsDuplicateKey:三家方言的报错文本互不相同,而这里把"撞键"
// 当作幂等重放(errIdemReplay),漏判一家就会让重放变成建单失败。
func isDuplicateKey(err error) bool { return db.IsDuplicateKey(err) }
