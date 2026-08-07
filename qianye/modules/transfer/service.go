package transfer

import (
	"context"
	"errors"
	"strconv"
	"unicode/utf8"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/qianye/config"
	"github.com/QuantumNous/new-api/qianye/db"
	"github.com/QuantumNous/new-api/qianye/guard"
	qymodel "github.com/QuantumNous/new-api/qianye/model"
	"github.com/QuantumNous/new-api/qianye/service/audit"
	"github.com/QuantumNous/new-api/qianye/service/twophase"

	"github.com/gin-gonic/gin"
	"gorm.io/gorm"
)

// idemScope 与 qy_fund_orders.(idem_scope, idem_key) 双列唯一索引配套。
const idemScope = "transfer"

// create 执行一笔划转。
//
// 全流程只有一个跨库写入点(twophase.Execute),这里不自己写跨库事务:
// 资金中间态的判定依赖主库 outbox 探针,自建一套只会多一个无法对账的状态机。
func create(c *gin.Context, fromUserId int, req createRequest) (*createResponse, error) {
	// 门槛快照**在这里取唯一一次**,受理校验、主库锁内复检、扩展库锁内风控
	// 判定三处共用它。
	//
	// 分三次读是资损:门槛已经可以被管理端在线改(见 settings.go),运营在
	// 受理与落账之间保存一次,就会出现"受理时按旧门槛放行、锁内按新门槛拒绝"
	// —— 用户白吃一次冷却与风控预占,而他提交那一刻看到的门槛确实是允许的。
	// 反过来同样成立。用户提交时看到什么门槛,这一笔就按什么门槛算。
	//
	// 这一步单独用一个冷路径预算,不与下面那个共用:共用会让读配置花掉的时间
	// 从资金操作的预算里扣,一次慢查询就能让 twophase.Execute 在几乎耗尽的
	// ctx 上启动。它一读完就 cancel,拿到的是值不是句柄,没有后续依赖。
	settingsCtx, settingsCancel := guard.ColdContext(context.Background())
	settings, err := effectiveCtx(settingsCtx)
	settingsCancel()
	if err != nil {
		return nil, err
	}

	// 刻意不用 c.Request.Context():客户端断开连接不该中断一笔已经在动钱的操作。
	ctx, cancel := guard.ColdContext(context.Background())
	defer cancel()

	// ── 门槛分档:这一笔按发起方**此刻的用户分组**那一档算 ──
	//
	// 这次读必须排在 validateCreate 之前:单笔上下限本身就是可分档项,
	// 拿全站兜底去做受理校验,等于让分档在最主要的那道闸门上完全不生效。
	//
	// loadParties 随后会再读一次同一行用户。刻意不把这次的结果传下去:
	// "受理阶段以哪一行为准"是 loadParties 自己的不变量(见 enforceGroupPolicy
	// 的参数说明 —— 取值这一步留在函数外面,迟早有一处传进旧分组),而这里
	// 只需要一个分组名。两次读之间管理员改了分组的话,门槛按前一次、分组闸门
	// 按后一次;真正作数的分组判定在主库行锁内还会再做一次(applyQuotaTransfer)。
	//
	// 门槛快照本身仍然**只取这一次**,受理校验、锁内复检、扩展库锁内风控三处
	// 共用它 —— 分三次读是资损,理由见上面那段。
	senderNow, err := model.GetUserById(fromUserId, false)
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, errSenderDisabled
		}
		return nil, err
	}
	cfg, err := settings.transferFor(senderNow.Group)
	if err != nil {
		return nil, err
	}

	acc, err := validateCreate(fromUserId, req, cfg)
	if err != nil {
		return nil, err
	}

	// 分组规则在这里一次性读出并冻结,后面两处判定共用同一份快照。
	//
	// 为什么冻结:受理与主库落账之间若各读各的,运营在这中间改一次规则就会出现
	// "受理放行、锁内拒绝"—— 用户白吃一次冷却与风控预占,而他提交那一刻看到的
	// 规则确实是允许的。用户提交时看到什么规则,这一笔就按什么规则算。
	//
	// 读失败绝不回落成"不限制"(见 loadGroupRules):空表才是"不限制"。
	groupRules, err := loadGroupRules()
	if err != nil {
		return nil, err
	}

	sender, receiver, err := loadParties(acc, cfg, groupRules)
	if err != nil {
		return nil, err
	}

	// ── 收款方口径的闸门按**收款方**那一档解析 ──
	//
	// receiver_daily_max_in_count 约束的是「这个账号每天能收几笔」。按发起方那一档
	// 解析等于把闸门的上限交给被它约束的那一方去挑:200 个 default 小号往一个
	// 配了「每天 1 笔」的 vip 汇集账号转钱,每一笔都按 default 那一档(全站 20)
	// 放行,而这正是这道闸门存在的唯一理由(见 evaluateRisk)。
	//
	// 这一档配坏时 fail-closed(503):拿一份不自洽的门槛去判一笔资金操作,
	// 比让这一笔失败更糟。
	receiverCfg, err := settings.transferFor(receiver.Group)
	if err != nil {
		return nil, err
	}

	now := common.GetTimestamp()
	detail := Order{
		FromUserId:   acc.FromUserId,
		ToUserId:     acc.ToUserId,
		FromUsername: sender.Username,
		ToUsername:   receiver.Username,
		Amount:       acc.Amount,
		FeeQuota:     acc.Fee,
		Status:       statusPending,
		Remark:       acc.Remark,
		ClientIp:     truncate(c.ClientIP(), 64),
		UserAgent:    truncate(c.Request.UserAgent(), 255),
		RiskHeld:     true,
		CreatedAt:    now,
	}

	var (
		snap        quotaSnapshot
		mainApplied bool
	)

	fundReq := fundingFacts(acc)
	fundReq.LocalDetail = func(tx *gorm.DB, o *qymodel.FundOrder) error {
		// 风控与落单同事务:两者之间只要有提交间隙,
		// 并发请求就会同时读到旧计数、同时通过日限额校验。
		if err := reserveRisk(tx, acc, cfg, receiverCfg, now); err != nil {
			return err
		}
		detail.OrderNo = o.OrderNo
		return tx.Create(&detail).Error
	}
	fundReq.MainApply = func(tx *gorm.DB, o *qymodel.FundOrder) error {
		if err := applyQuotaTransfer(tx, acc, cfg, groupRules, &snap); err != nil {
			return err
		}
		mainApplied = true
		return nil
	}
	fundReq.AfterCommit = func(o *qymodel.FundOrder) {
		if !mainApplied {
			return
		}
		afterMainCommit(o.OrderNo, detail, snap)
	}
	fundReq.LocalCommit = func(tx *gorm.DB, o *qymodel.FundOrder) error {
		return settleDetailTx(tx, o.OrderNo, settlement{status: statusSuccess, snap: &snap})
	}

	order, execErr := twophase.Execute(ctx, fundReq)

	if execErr != nil {
		// 指纹冲突意味着"另一笔请求复用了同一个 client_request_id",本次与原单毫无关系。
		// 必须在 releaseOnFailure 之前拦掉:那条路径会拿本次的失败原因去结算**原单**的
		// 明细行与风控预占,等于让攻击者用一个伪造请求去污染别人已经成交的单据。
		if errors.Is(execErr, twophase.ErrIdemConflict) {
			// 只记日志不写审计:审计表是事后仲裁的唯一凭据,不能让任何人凭一个
			// 被拒绝的请求往里塞行。SysError 一样可观测,而且写不进仲裁表。
			common.SysError("qianye/transfer: 用户 " + strconv.Itoa(acc.FromUserId) +
				" 用同一个 client_request_id 提交了资金要素不同的划转,已拒绝: " + execErr.Error())
			return nil, errIdemKeyConflict
		}
		releaseOnFailure(order, execErr)
		// 被风控拒绝的划转在此之前零留痕:资金审计里只有成功的那些,
		// 于是"这个账号连续 20 次撞日限额、每次都换一个收款人"这种最典型的
		// 洗号形状,事后一行都查不到。
		//
		// 只在 order != nil 时写:资金单已经落库意味着这是一次被受理并被判定
		// 失败的真实尝试,行数与资金单一一对应,不会因为参数校验失败被灌爆
		// 仲裁表(那一类由请求台账 qy_request_audits 覆盖)。
		if order != nil {
			e := transferCreatedAudit(order, c.GetString("username"))
			e.Result = qymodel.ResultFail
			e.Reason = audit.Truncate("划转被拒: "+execErr.Error(), 500)
			audit.Write(c, e)
		}
		return nil, execErr
	}

	audit.Write(c, transferCreatedAudit(order, c.GetString("username")))
	return buildCreateResponse(order, detail, snap), nil
}

// fundingFacts 把受理后的请求翻译成 twophase 的资金要素,并算好幂等指纹。
//
// 单独成函数不是为了缩短 create:指纹是"同一个 client_request_id 到底代表哪一笔钱"
// 的唯一判据,一旦它漏算某个要素(比如漏了 PeerUserId),换收款人重放就会被当成
// 正常的幂等命中直接返回成功。这条不变量必须能脱离数据库被直接测到。
//
// 刻意不含 Remark:备注不是资金要素,改文案重试不该被判成冲突。
//
// acc.Fee 照常写进 FeeQuota(资金单要如实记账),但它**不参与指纹** —— 见
// twophase.Digest 的说明:手续费是受理时按当时的 transfer.fee_* 算出的服务端派生量,
// 把它算进指纹会让"用户重试期间运营调了费率"变成 409,而原单根本没成功。
func fundingFacts(acc acceptedRequest) twophase.Request {
	req := twophase.Request{
		Kind:        qymodel.KindTransfer,
		IdemScope:   idemScope,
		IdemKey:     acc.IdemKey,
		UserId:      acc.FromUserId,
		PeerUserId:  acc.ToUserId,
		AmountQuota: acc.Amount,
		FeeQuota:    acc.Fee,
	}
	req.Fingerprint = req.Digest()
	return req
}

// loadParties 读取并校验双方在主库中的状态。
//
// 这一轮校验会在主库事务里再做一次:两次之间用户可能被封禁、分组可能被调整,
// 锁外的结论只是为了尽早给用户明确错误,不能当作最终依据。
func loadParties(acc acceptedRequest, cfg config.Transfer, rules groupRuleSet) (*model.User, *model.User, error) {
	sender, err := model.GetUserById(acc.FromUserId, false)
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, nil, errSenderDisabled
		}
		return nil, nil, err
	}
	if sender.Status != common.UserStatusEnabled {
		return nil, nil, errSenderDisabled
	}
	// 新账号冻结期:批量注册的小号是零成本套现的入口,
	// users.created_at 现成可读,这道门槛的成本几乎为零。
	if freeze := int64(cfg.NewAccountFreezeHours) * 3600; freeze > 0 &&
		sender.CreatedAt > 0 && common.GetTimestamp()-sender.CreatedAt < freeze {
		return nil, nil, errAccountTooNew
	}

	receiver, err := model.GetUserById(acc.ToUserId, false)
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, nil, errReceiverNotFound
		}
		return nil, nil, err
	}
	if cfg.ReceiverMustBeEnabled() && receiver.Status != common.UserStatusEnabled {
		return nil, nil, errReceiverDisabled
	}
	// 分组限制的受理阶段判定,与上面的额度/状态门槛并列。
	//
	// 排在收款人存在性与状态之后是刻意的:抢在它们前面报出去,等于用一句
	// "分组不允许"回答了"这个账号存不存在",凭空多一条枚举旁路。
	//
	// 这一轮只是为了尽早给出明确错误。真正作数的是 applyQuotaTransfer 在主库
	// 行锁内的复检 —— 见那里的注释。
	if err := enforceGroupPolicy(sender, receiver, rules); err != nil {
		return nil, nil, err
	}
	return sender, receiver, nil
}

// applyQuotaTransfer 在主库事务内完成双向额度变更。
//
// 硬性约束(违反即资损):
//   - 两行按 user id 升序加锁,否则 A→B 与 B→A 并发会在主库形成死锁环
//   - 扣款必须带 WHERE quota >= ? 并检查 RowsAffected;主库可能是 SQLite,
//     那里 lockForUpdate 是空操作,CAS 条件是唯一的正确性保障
//   - 加款必须带上限条件:users.quota 是 int32,上游全无溢出校验
//   - 绝不调用 user.Update()/IncrementUserAuthVersionWithTx —— 那会吊销双方会话
func applyQuotaTransfer(tx *gorm.DB, acc acceptedRequest, cfg config.Transfer, rules groupRuleSet, snap *quotaSnapshot) error {
	first, second := acc.FromUserId, acc.ToUserId
	if first > second {
		first, second = second, first
	}
	u1, err := lockUserForUpdate(tx, first, acc.FromUserId)
	if err != nil {
		return err
	}
	u2, err := lockUserForUpdate(tx, second, acc.FromUserId)
	if err != nil {
		return err
	}
	sender, receiver := u1, u2
	if sender.Id != acc.FromUserId {
		sender, receiver = u2, u1
	}

	// 锁内复检:受理校验到加锁之间存在窗口,用户可能刚被管理员封禁。
	if sender.Status != common.UserStatusEnabled {
		return errSenderDisabled
	}
	if cfg.ReceiverMustBeEnabled() && receiver.Status != common.UserStatusEnabled {
		return errReceiverDisabled
	}
	// 分组限制的**权威判定点**:以主库行锁那一刻读到的 users.group 为准。
	//
	// 受理阶段那次(loadParties)只是为了尽早报错,两者之间存在真实窗口:
	// 管理员把用户从 vip 降到 default、或把收款方调进一个受限分组,都会落在这个
	// 窗口里。锁内的这两行是本次划转唯一"不会再变"的事实,只有基于它们的判定
	// 才拦得住"提交时合规、落账时已不合规"。
	//
	// 规则快照仍是受理时冻结的那一份(见 create):分组是"这个人现在属于谁"的
	// 事实,必须取最新;规则是"提交那一刻的约定",中途被收紧不该让一笔已经在
	// 动钱的请求拿到与用户所见不同的结论。
	if err := enforceGroupPolicy(sender, receiver, rules); err != nil {
		return err
	}
	if int64(sender.Quota) < acc.Total {
		return errInsufficientQuota
	}
	if int64(receiver.Quota) > int64(common.MaxQuota)-acc.Amount {
		return errReceiverOverflow
	}

	res := tx.Model(&model.User{}).
		Where("id = ? AND quota >= ?", acc.FromUserId, acc.Total).
		Update("quota", gorm.Expr("quota - ?", acc.Total))
	if res.Error != nil {
		return res.Error
	}
	if res.RowsAffected != 1 {
		return errInsufficientQuota
	}

	res = tx.Model(&model.User{}).
		Where("id = ? AND quota <= ?", acc.ToUserId, int64(common.MaxQuota)-acc.Amount).
		Update("quota", gorm.Expr("quota + ?", acc.Amount))
	if res.Error != nil {
		return res.Error
	}
	if res.RowsAffected != 1 {
		return errReceiverOverflow
	}

	*snap = quotaSnapshot{
		FromBefore: int64(sender.Quota),
		FromAfter:  int64(sender.Quota) - acc.Total,
		ToBefore:   int64(receiver.Quota),
		ToAfter:    int64(receiver.Quota) + acc.Amount,
	}
	return nil
}

// lockUserForUpdate 在主库事务内按 id 取用户行锁。
// senderId 只用于把"记录不存在"翻译成对应一方的业务错误。
func lockUserForUpdate(tx *gorm.DB, id, senderId int) (*model.User, error) {
	var u model.User
	// First 自带软删除过滤(deleted_at IS NULL)。绝不能加 Unscoped:
	// 给已注销账号加款等于把钱转进一个再也取不出来的账户。
	if err := model.QyLockForUpdate(tx).Where("id = ?", id).First(&u).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			if id == senderId {
				return nil, errSenderDisabled
			}
			return nil, errReceiverNotFound
		}
		return nil, err
	}
	return &u, nil
}

// afterMainCommit 处理主库提交之后的收尾。此时钱已经动了,任何失败都不能回滚,
// 只能记日志并交给对账任务。
func afterMainCommit(orderNo string, detail Order, snap quotaSnapshot) {
	// 双侧都要失效。漏掉任一侧,用户在缓存 TTL 内看到的余额就是错的;
	// 用增量更新缓存则会与并发的消费扣费互相覆盖,所以这里选择直接失效回源。
	if err := model.InvalidateUserCache(detail.FromUserId); err != nil {
		common.SysError("qianye/transfer: 失效发起方用户缓存失败: " + err.Error())
	}
	if err := model.InvalidateUserCache(detail.ToUserId); err != nil {
		common.SysError("qianye/transfer: 失效收款方用户缓存失败: " + err.Error())
	}

	if err := stampLedger(orderNo, snap); err != nil {
		if !errors.Is(err, errLedgerAlreadyStamped) {
			common.SysError("qianye/transfer: 单号 " + orderNo + " 标记账本日志失败,已交由对账任务补写: " + err.Error())
		}
		return
	}
	detail.OrderNo = orderNo
	detail.FromQuotaAfter = snap.FromAfter
	detail.ToQuotaAfter = snap.ToAfter
	writeLedgerLogs(detail, true)
}

// releaseOnFailure 在 Execute 返回错误时退还风控预占。
//
// 只处理主库明确未生效(资金单已被置为 failed)的情况:pending 与 uncertain
// 都意味着"钱可能已经动了",退还额度会造成超发,必须留给对账任务判定。
func releaseOnFailure(order *qymodel.FundOrder, cause error) {
	if order == nil || order.Status != qymodel.StatusFailed {
		return
	}
	if mainSideApplied(order.OrderNo) {
		// 资金单被判成 failed,但主库 outbox 说钱已经动了 —— 两者矛盾,只能转人工。
		markUncertainAfterConflict(order.OrderNo)
		return
	}
	code, reason := failureOf(cause)
	// settleDetail 幂等(risk_held CAS),因此重放一笔历史失败单不会二次退还。
	if err := settleDetail(order.OrderNo, settlement{
		status: statusFailed, failCode: code, failReason: reason,
	}); err != nil {
		common.SysError("qianye/transfer: 单号 " + order.OrderNo +
			" 退还风控预占失败,已交由对账任务处理: " + err.Error())
	}
}

// mainSideApplied 用主库 outbox 探针判定资金变更到底有没有真的生效。
//
// 存在理由:主库事务在 commit 阶段断连时,GORM 会返回错误,但事务其实可能已经提交。
// 仅凭错误就退还风控预占,等于"钱已经转走却把额度原样还给用户",是纯资损。
// 探针自身失败时一律按"可能已生效"处理:宁可让用户当天少一次额度,
// 也不能错退 —— 前者是体验问题,后者是可被反复利用的超发漏洞。
func mainSideApplied(orderNo string) bool {
	if !config.Get().TwoPhase.OutboxEnabled() {
		return false
	}
	applied, err := model.QyProbeFundOutbox(orderNo)
	if err != nil {
		common.SysError("qianye/transfer: 单号 " + orderNo +
			" 探测主库 outbox 失败,按可能已生效处理: " + err.Error())
		return true
	}
	return applied
}

func markUncertainAfterConflict(orderNo string) {
	common.SysError("qianye/transfer: 单号 " + orderNo +
		" 的资金单被判为失败但主库 outbox 显示已生效,已转人工裁决")
	if err := settleDetail(orderNo, settlement{
		status:     statusUncertain,
		failCode:   "qy_uncertain",
		failReason: "资金单状态与主库探针矛盾,需人工核对",
	}); err != nil {
		common.SysError("qianye/transfer: 单号 " + orderNo + " 转人工裁决失败: " + err.Error())
	}
}

func failureOf(err error) (string, string) {
	var be *bizError
	if errors.As(err, &be) {
		return be.Code, truncate(be.Msg, 255)
	}
	return "qy_transfer_failed", truncate(err.Error(), 255)
}

// transferCreatedAudit 构造"划转创建成功"的审计记录。
//
// 参数里刻意没有本次请求(acceptedRequest):金额、收款人、发起人一律取自
// **真实生效的资金单**。两者在幂等命中时可能完全不同 —— 指纹校验只能拦住
// 双方指纹都非空的情况,升级前落库的历史单指纹是空的,那类单的重放依旧会命中
// 并返回成功。拿本次请求的参数去写审计,就会在仲裁表里留下一条
// "金额 5000 万、收款人是另一个人、单号却是真实单号"的成功记录,
// 而资金侧毫无痕迹 —— 审计表是这套资金系统事后仲裁的唯一凭据,不能是可伪造的。
func transferCreatedAudit(order *qymodel.FundOrder, actorName string) audit.Entry {
	return audit.Entry{
		TraceNo:      order.OrderNo,
		Category:     qymodel.AuditCategoryTransfer,
		Action:       "transfer.create",
		ActorType:    qymodel.ActorUser,
		ActorUserId:  order.UserId,
		ActorName:    actorName,
		TargetUserId: order.PeerUserId,
		AmountQuota:  order.AmountQuota,
		Result:       qymodel.ResultOK,
		Reason:       qymodel.StatusName(order.Status),
	}
}

// buildCreateResponse 以落库的明细为准构造响应;读不到时退回内存快照。
// 幂等命中(重复提交)时闭包根本没执行过,只有回读才能拿到原单结果。
func buildCreateResponse(order *qymodel.FundOrder, detail Order, snap quotaSnapshot) *createResponse {
	row := detail
	row.OrderNo = order.OrderNo
	row.Status = qymodel.StatusName(order.Status)
	row.FromQuotaAfter = snap.FromAfter

	if gdb := db.Get(); gdb != nil {
		var stored Order
		if err := gdb.Where("order_no = ?", order.OrderNo).First(&stored).Error; err == nil {
			row = stored
		}
	}
	return &createResponse{
		OrderNo:          row.OrderNo,
		Status:           row.Status,
		Amount:           row.Amount,
		FeeQuota:         row.FeeQuota,
		ToUserId:         row.ToUserId,
		ToMaskedUsername: maskUsername(row.ToUsername),
		MyQuotaAfter:     row.FromQuotaAfter,
		CreatedAt:        row.CreatedAt,
	}
}

// truncate 按字节截断到最近的 UTF-8 边界。
// 直接切字节会把中文失败原因劈成半个字符,MySQL 会整条报错而不是截断。
func truncate(s string, max int) string {
	if max <= 0 || len(s) <= max {
		return s
	}
	for max > 0 && !utf8.RuneStart(s[max]) {
		max--
	}
	return s[:max]
}
