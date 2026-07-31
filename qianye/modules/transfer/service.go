package transfer

import (
	"context"
	"errors"
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
	cfg := config.Get().Transfer
	acc, err := validateCreate(fromUserId, req, cfg)
	if err != nil {
		return nil, err
	}

	sender, receiver, err := loadParties(acc, cfg)
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

	// 刻意不用 c.Request.Context():客户端断开连接不该中断一笔已经在动钱的操作。
	ctx, cancel := guard.ColdContext(context.Background())
	defer cancel()

	order, execErr := twophase.Execute(ctx, twophase.Request{
		Kind:        qymodel.KindTransfer,
		IdemScope:   idemScope,
		IdemKey:     acc.IdemKey,
		UserId:      acc.FromUserId,
		PeerUserId:  acc.ToUserId,
		AmountQuota: acc.Amount,
		FeeQuota:    acc.Fee,

		LocalDetail: func(tx *gorm.DB, o *qymodel.FundOrder) error {
			// 风控与落单同事务:两者之间只要有提交间隙,
			// 并发请求就会同时读到旧计数、同时通过日限额校验。
			if err := reserveRisk(tx, acc, now); err != nil {
				return err
			}
			detail.OrderNo = o.OrderNo
			return tx.Create(&detail).Error
		},

		MainApply: func(tx *gorm.DB, o *qymodel.FundOrder) error {
			if err := applyQuotaTransfer(tx, acc, cfg, &snap); err != nil {
				return err
			}
			mainApplied = true
			return nil
		},

		AfterCommit: func(o *qymodel.FundOrder) {
			if !mainApplied {
				return
			}
			afterMainCommit(o.OrderNo, detail, snap)
		},

		LocalCommit: func(tx *gorm.DB, o *qymodel.FundOrder) error {
			return settleDetailTx(tx, o.OrderNo, settlement{status: statusSuccess, snap: &snap})
		},
	})

	if execErr != nil {
		releaseOnFailure(order, execErr)
		return nil, execErr
	}

	writeCreateAudit(c, order, acc)
	return buildCreateResponse(order, detail, snap), nil
}

// loadParties 读取并校验双方在主库中的状态。
//
// 这一轮校验会在主库事务里再做一次:两次之间用户可能被封禁,
// 锁外的结论只是为了尽早给用户明确错误,不能当作最终依据。
func loadParties(acc acceptedRequest, cfg config.Transfer) (*model.User, *model.User, error) {
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
func applyQuotaTransfer(tx *gorm.DB, acc acceptedRequest, cfg config.Transfer, snap *quotaSnapshot) error {
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

func writeCreateAudit(c *gin.Context, order *qymodel.FundOrder, acc acceptedRequest) {
	audit.Write(c, audit.Entry{
		TraceNo:      order.OrderNo,
		Category:     qymodel.AuditCategoryTransfer,
		Action:       "transfer.create",
		ActorType:    qymodel.ActorUser,
		ActorUserId:  acc.FromUserId,
		ActorName:    c.GetString("username"),
		TargetUserId: acc.ToUserId,
		AmountQuota:  acc.Amount,
		Result:       qymodel.ResultOK,
		Reason:       qymodel.StatusName(order.Status),
	})
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
