package withdraw

import (
	"context"
	"errors"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/qianye/config"
	"github.com/QuantumNous/new-api/qianye/db"
	"github.com/QuantumNous/new-api/qianye/guard"
	qymodel "github.com/QuantumNous/new-api/qianye/model"
	"github.com/QuantumNous/new-api/qianye/modules/commission"
	"github.com/QuantumNous/new-api/qianye/service/audit"

	"github.com/gin-gonic/gin"
	"gorm.io/gorm"
)

// 人工裁决的决定值。
const (
	DecisionPaid   = "paid"
	DecisionFailed = "failed"
)

// maxReasonRunes 是拒绝理由 / 失败原因 / 裁决依据的长度上限。
//
// 刻意与用户说明的 remark_max_runes 解耦:后者是运营可调的展示型配置(上限 2000),
// 而这几个字段的列宽是固定的。跟着配置走的话,运营把说明上限调大之后,
// 管理员写一条长理由就会直接写库失败 —— 而那一刻用户的佣金正卡在冻结里。
const maxReasonRunes = 200

// maxPayoutRefRunes 与 payout_ref 的列宽一致。
const maxPayoutRefRunes = 128

// actor 是一次人工操作的操作者快照。
type actor struct {
	Id   int
	Name string
	IP   string
}

func actorOf(c *gin.Context) actor {
	return actor{Id: c.GetInt("id"), Name: c.GetString("username"), IP: c.ClientIP()}
}

// cancelByUser 撤销申请。只有 pending 单可撤销,佣金原路退回。
//
// 与管理员审核是同一把锁:两侧都用 `WHERE status='pending'` 的 CAS,
// 先到者胜,后到者拿到 409 并看到当前状态。
func cancelByUser(c *gin.Context, userId int, id int64) (*orderView, error) {
	w, err := loadUserWithdrawal(userId, id)
	if err != nil {
		return nil, err
	}
	a := actorOf(c)
	err = db.Get().Transaction(func(tx *gorm.DB) error {
		if err := applyTransition(tx, w, transition{
			From: StatusPending, To: StatusCancelled, Action: ActionCancel,
			ActorType: qymodel.ActorUser, ActorId: userId, ActorName: a.Name, IP: a.IP,
		}); err != nil {
			return err
		}
		return commission.UnfreezeForWithdraw(tx, w.UserId, w.Quota, w.WithdrawNo)
	})
	if err != nil {
		return nil, err
	}
	writeDecisionAudit(c, w, "withdraw.cancel", qymodel.ActorUser, a, "", qymodel.ResultOK)
	return toUserView(w, nil), nil
}

// approve 审核通过。
//
// quota 方式且开启了自动到账时,通过之后立即触发跨库兑现。兑现的错误不会让
// 审核动作回滚 —— 审核已经是既成事实,兑现的中间态由对账任务收敛。
func approve(c *gin.Context, id int64) (*adminOrderView, error) {
	w, err := loadWithdrawal(id)
	if err != nil {
		return nil, err
	}
	a := actorOf(c)
	now := common.GetTimestamp()

	err = db.Get().Transaction(func(tx *gorm.DB) error {
		return applyTransition(tx, w, transition{
			From: StatusPending, To: StatusApproved, Action: ActionApprove,
			ActorType: qymodel.ActorAdmin, ActorId: a.Id, ActorName: a.Name, IP: a.IP,
			Updates: map[string]any{
				"reviewer_id": a.Id, "reviewer_name": truncate(a.Name, 64), "reviewed_at": now,
			},
		})
	})
	if err != nil {
		return nil, err
	}
	w.ReviewerId, w.ReviewerName, w.ReviewedAt = a.Id, a.Name, now
	writeDecisionAudit(c, w, "withdraw.approve", qymodel.ActorAdmin, a, "", qymodel.ResultOK)

	if shouldAutoCredit(w) {
		// 刻意不用 c.Request.Context():管理员的浏览器断开不该中断一笔已经在动钱的操作。
		ctx, cancel := guard.ColdContext(context.Background())
		defer cancel()
		if err := creditQuota(ctx, w); err != nil {
			// 兑现失败不改变"已审核通过"这个事实,单据的真实状态已写在库里,
			// 直接把它回给管理员即可 —— 让他看到 failed/paying 比看到一个
			// 语焉不详的错误更有用。
			common.SysError("qianye/withdraw: 单号 " + w.WithdrawNo + " 兑现未完成: " + err.Error())
		}
	}
	return toAdminView(w, nil), nil
}

// creditNow 由管理员对一张 approved 的 quota 单显式触发兑现。
//
// 存在的理由是 withdraw.auto_credit_on_approve 这个开关的 false 分支:
// approved → paying 是 quota 单走到 paid 的唯一入边,而它只由 startPaying 写、
// startPaying 只在 creditQuota 里被调用。审核后自动兑现与对账重试这两个
// creditQuota 调用点都被 AutoCredit() 门住,所以开关置 false 后 quota 单会
// 无限期停在 approved:用户既拿不到额度,管理端也只剩下"标记打款失败"
// (退回佣金、用户白等)与 markPaid(核销佣金、一分钱不付)两条错路。
// 本接口把 false 的语义从"quota 提现静默失效"变成"兑现改由管理员手动确认"。
//
// 幂等由 startPaying 的 approved → paying CAS 保证 —— 与自动到账、对账重试
// 共用同一道全集群准入闸门,连点两次不会加两次额度。因此这里的状态与方式
// 判断只是给管理员一个明确的拒绝理由,不是防重的依据。
func creditNow(c *gin.Context, id int64) (*adminOrderView, error) {
	w, err := loadWithdrawal(id)
	if err != nil {
		return nil, err
	}
	if w.Method != config.WithdrawMethodQuota {
		return nil, errNotQuotaOrder
	}
	if w.Status != StatusApproved {
		return nil, errIllegalTransition
	}
	a := actorOf(c)

	// 与审核通过同一口径:管理员的浏览器断开不该中断一笔已经在动钱的操作。
	ctx, cancel := guard.ColdContext(context.Background())
	defer cancel()
	creditErr := creditQuota(ctx, w)

	// 成败都要落审计:这是一次人工的动钱决策,"谁在什么时候按了这个按钮"
	// 不能只在成功时才留痕。
	result, reason := qymodel.ResultOK, ""
	if creditErr != nil {
		result, reason = qymodel.ResultFail, creditErr.Error()
	}
	writeDecisionAudit(c, w, "withdraw.credit", qymodel.ActorAdmin, a, reason, result)
	if creditErr != nil {
		common.SysError("qianye/withdraw: 单号 " + w.WithdrawNo + " 手动兑现未完成: " + creditErr.Error())
		return nil, creditErr
	}
	return toAdminView(w, nil), nil
}

// reject 拒绝申请。理由必填 —— 需求明确要求用户能看到"为什么被拒"。
func reject(c *gin.Context, id int64, rawReason string) (*adminOrderView, error) {
	reason, err := checkRunes(rawReason, maxReasonRunes)
	if err != nil || reason == "" {
		return nil, errReasonRequired
	}
	w, err := loadWithdrawal(id)
	if err != nil {
		return nil, err
	}
	a := actorOf(c)
	now := common.GetTimestamp()

	err = db.Get().Transaction(func(tx *gorm.DB) error {
		if err := applyTransition(tx, w, transition{
			From: StatusPending, To: StatusRejected, Action: ActionReject,
			ActorType: qymodel.ActorAdmin, ActorId: a.Id, ActorName: a.Name, IP: a.IP,
			Reason: reason,
			Updates: map[string]any{
				"reviewer_id": a.Id, "reviewer_name": truncate(a.Name, 64),
				"reviewed_at": now, "reject_reason": reason,
			},
		}); err != nil {
			return err
		}
		return commission.UnfreezeForWithdraw(tx, w.UserId, w.Quota, w.WithdrawNo)
	})
	if err != nil {
		return nil, err
	}
	w.ReviewerId, w.ReviewerName, w.ReviewedAt, w.RejectReason = a.Id, a.Name, now, reason
	writeDecisionAudit(c, w, "withdraw.reject", qymodel.ActorAdmin, a, reason, qymodel.ResultOK)
	return toAdminView(w, nil), nil
}

// markPaid 标记线下法币已打款。
//
// 法币路径完全不触碰主库,因此不存在两阶段问题:一个扩展库事务里
// 改状态 + 核销佣金即可。
//
// 【方式闸门是资金安全边界,不是参数校验】本函数会把佣金从 frozen 直接转成
// withdrawn(SettleFrozen),而它全程不碰主库 users.quota。对 method=quota 的单
// 执行一次,结果就是"佣金被永久核销、用户一分钱没拿到":paid 是终态、
// allowedTransitions 没有出边、没有反向接口、order_no 为空的 paid 单也不会再被
// 对账扫到 —— 静默、不可逆、无检测。
// 前端按 method === 'fiat' 隐藏按钮不算防线(见 acceptCreate 的注释:绕过前端
// 只需要一个 curl),批量"标记已打款"的运维脚本更是一发就中。
// quota 单的正确正向出口只有 creditQuota(自动到账或管理员手动兑现),
// 反向出口是 markFailed(退回佣金)。
func markPaid(c *gin.Context, id int64, payoutRef string, paidAt int64, note string) (*adminOrderView, error) {
	ref, err := checkRunes(payoutRef, maxPayoutRefRunes)
	if err != nil || ref == "" {
		return nil, errPayoutRefMissing
	}
	w, err := loadWithdrawal(id)
	if err != nil {
		return nil, err
	}
	if w.Method != config.WithdrawMethodFiat {
		return nil, errNotFiatOrder
	}

	now := common.GetTimestamp()
	// 允许补录历史打款时间,但不接受未来时间:那要么是客户端时钟错了,
	// 要么是有人想把打款记录做到对账窗口之外。留 5 分钟容忍时钟漂移。
	if paidAt <= 0 || paidAt > now+300 {
		paidAt = now
	}
	a := actorOf(c)

	err = db.Get().Transaction(func(tx *gorm.DB) error {
		if err := applyTransition(tx, w, transition{
			From: StatusApproved, To: StatusPaid, Action: ActionPay,
			ActorType: qymodel.ActorAdmin, ActorId: a.Id, ActorName: a.Name, IP: a.IP,
			Detail: common.MapToJsonStr(map[string]any{"payout_ref": ref, "paid_at": paidAt}),
			Updates: map[string]any{
				"payout_operator_id":   a.Id,
				"payout_operator_name": truncate(a.Name, 64),
				"paid_at":              paidAt,
				"payout_ref":           ref,
				"payout_note":          truncate(note, 255),
			},
		}); err != nil {
			return err
		}
		return commission.SettleFrozen(tx, w.UserId, w.Quota, w.WithdrawNo)
	})
	if err != nil {
		return nil, err
	}
	w.PaidAt, w.PayoutRef = paidAt, ref
	writeDecisionAudit(c, w, "withdraw.pay", qymodel.ActorAdmin, a, ref, qymodel.ResultOK)
	return toAdminView(w, nil), nil
}

// markFailed 标记线下打款失败,佣金退回。理由必填。
func markFailed(c *gin.Context, id int64, rawReason string) (*adminOrderView, error) {
	reason, err := checkRunes(rawReason, maxReasonRunes)
	if err != nil || reason == "" {
		return nil, errReasonRequired
	}
	w, err := loadWithdrawal(id)
	if err != nil {
		return nil, err
	}
	a := actorOf(c)

	err = db.Get().Transaction(func(tx *gorm.DB) error {
		if err := applyTransition(tx, w, transition{
			From: StatusApproved, To: StatusFailed, Action: ActionFail,
			ActorType: qymodel.ActorAdmin, ActorId: a.Id, ActorName: a.Name, IP: a.IP,
			Reason:  reason,
			Updates: map[string]any{"fail_reason": reason},
		}); err != nil {
			return err
		}
		return commission.UnfreezeForWithdraw(tx, w.UserId, w.Quota, w.WithdrawNo)
	})
	if err != nil {
		return nil, err
	}
	w.FailReason = reason
	writeDecisionAudit(c, w, "withdraw.fail", qymodel.ActorAdmin, a, reason, qymodel.ResultOK)
	return toAdminView(w, nil), nil
}

// resolveHold 对"对账异常"单做人工裁决。
//
// 只有人能回答"主库到底加没加额度"这个问题时才走这里 —— 管理员对照主库用户
// 额度变动与 logs 判定后二选一。裁决依据必填并写进事件的 Reason,
// 事后复盘要能看到"当时凭什么这么判"。
func resolveHold(c *gin.Context, id int64, decision, rawEvidence string) (*adminOrderView, error) {
	evidence, err := checkRunes(rawEvidence, maxReasonRunes)
	if err != nil || evidence == "" {
		return nil, errReasonRequired
	}
	if decision != DecisionPaid && decision != DecisionFailed {
		return nil, errInvalidParam
	}
	w, err := loadWithdrawal(id)
	if err != nil {
		return nil, err
	}
	if w.Status != StatusPaying {
		return nil, errIllegalTransition
	}
	a := actorOf(c)

	err = db.Get().Transaction(func(tx *gorm.DB) error {
		// 先留裁决痕迹再改状态:状态变更后 from 就丢了,事件里的
		// "从什么状态被人工推走的"是复盘时最关键的一条信息。
		if err := writeEvent(tx, w, transition{
			From: StatusPaying, To: StatusPaying, Action: ActionResolve,
			ActorType: qymodel.ActorAdmin, ActorId: a.Id, ActorName: a.Name, IP: a.IP,
			Reason: evidence,
			Detail: common.MapToJsonStr(map[string]any{"decision": decision, "order_no": w.OrderNo}),
		}); err != nil {
			return err
		}
		if decision == DecisionPaid {
			return finishPaid(tx, w, w.OrderNo)
		}
		if err := applyTransition(tx, w, transition{
			From: StatusPaying, To: StatusFailed, Action: ActionFail,
			ActorType: qymodel.ActorAdmin, ActorId: a.Id, ActorName: a.Name, IP: a.IP,
			Reason: evidence,
			Updates: map[string]any{
				"fail_reason":     evidence,
				"reconcile_state": "",
			},
		}); err != nil {
			return err
		}
		return commission.UnfreezeForWithdraw(tx, w.UserId, w.Quota, w.WithdrawNo)
	})
	if err != nil {
		return nil, err
	}
	writeDecisionAudit(c, w, "withdraw.resolve."+decision, qymodel.ActorAdmin, a, evidence, qymodel.ResultOK)
	return toAdminView(w, nil), nil
}

// writeDecisionAudit 记录一次人工决策。
//
// 每一次人工决策都必须落审计:没有它,"这笔为什么被拒""谁批准的"事后无法自证。
//
// result 由调用方给出而不是写死 ResultOK:动钱的人工决策(手动兑现)可能失败,
// 把失败的尝试记成成功,事后复盘会得出"管理员兑现过一次"的错误结论。
func writeDecisionAudit(c *gin.Context, w *Withdrawal, action, actorType string, a actor, reason, result string) {
	audit.Write(c, audit.Entry{
		TraceNo:      w.WithdrawNo,
		Category:     qymodel.AuditCategoryWithdraw,
		Action:       action,
		ActorType:    actorType,
		ActorUserId:  a.Id,
		ActorName:    a.Name,
		TargetUserId: w.UserId,
		AmountQuota:  w.Quota,
		AmountFiat:   w.NetAmount,
		Currency:     w.Currency,
		FrozenRate:   w.FrozenFxRate,
		Result:       result,
		Reason:       reason,
		AfterSnap:    common.MapToJsonStr(map[string]any{"status": w.Status, "method": w.Method}),
	})
}

func loadWithdrawal(id int64) (*Withdrawal, error) {
	var w Withdrawal
	err := db.Get().Where("id = ?", id).Take(&w).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, errNotFound
	}
	if err != nil {
		db.MarkFailure(err)
		return nil, err
	}
	return &w, nil
}

// loadUserWithdrawal 按用户维度取单。user_id 必须进 WHERE 条件而不是取回来再比 ——
// 后者只要有人忘写一次判断就是越权。
func loadUserWithdrawal(userId int, id int64) (*Withdrawal, error) {
	var w Withdrawal
	err := db.Get().Where("id = ? AND user_id = ?", id, userId).Take(&w).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, errNotFound
	}
	if err != nil {
		db.MarkFailure(err)
		return nil, err
	}
	return &w, nil
}

func loadEvents(withdrawalId int64) ([]Event, error) {
	var rows []Event
	// 上限兜底:事件是 append-only,理论上不会太多,但对账异常单被反复裁决时
	// 可能积累一批,不能让详情接口把它们全拉进内存。
	err := db.Get().Where("withdrawal_id = ?", withdrawalId).
		Order("id asc").Limit(200).Find(&rows).Error
	if err != nil {
		db.MarkFailure(err)
		return nil, err
	}
	return rows, nil
}
