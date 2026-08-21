package withdraw

import (
	"errors"
	"strings"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/qianye/config"
	"github.com/QuantumNous/new-api/qianye/db"
	"github.com/QuantumNous/new-api/qianye/guard"
	qymodel "github.com/QuantumNous/new-api/qianye/model"
	"github.com/QuantumNous/new-api/qianye/modules/commission"
	"github.com/QuantumNous/new-api/qianye/service/audit"

	"github.com/gin-gonic/gin"
	"github.com/shopspring/decimal"
	"gorm.io/gorm"
)

// maxReasonRunes 是驳回理由 / 发放失败原因的长度上限。
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

// ensureNoOutstandingDebt 是放款侧的欠账闸门(见 errDebtBlockedPayout)。
//
// 只在 approve / mark-paid 两步调用 —— 那是仅有的两个「把钱往外推」的动作。
// 读不出账本状态时按拦下处理:这是一道资金闸门,「读不到」不等于「没有欠账」。
func ensureNoOutstandingDebt(w *Withdrawal) error {
	if w == nil || w.UserId <= 0 {
		return errDebtStatusUnknown
	}
	statuses, err := commission.LoadDebtStatuses([]int{w.UserId})
	if err != nil {
		return errDebtStatusUnknown
	}
	st, ok := statuses[w.UserId]
	if !ok {
		// 没有余额行 = 从没计过佣。既然如此这张单也不该存在,但它不是欠账,
		// 放行交给后面的 SettleFrozen 去报真正的原因(frozen 不足)。
		return nil
	}
	if st.Blocked {
		return errDebtBlockedPayout
	}
	return nil
}

// approve 审核通过,把单据放进「待发放」队列。
//
// 这里**不再有任何出钱动作**。审核通过只表示"这笔可以发",钱由管理员自己去
// 加额度或线下打款,再回来调 markPayout 登记。佣金在申请时就已经离开可用池,
// 审核通过不改变佣金账本的任何一列。
func approve(c *gin.Context, id int64) (*adminOrderView, error) {
	w, err := loadDecidableWithdrawal(c, id)
	if err != nil {
		return nil, err
	}
	a := actorOf(c)
	if err := ensureNoOutstandingDebt(w); err != nil {
		writeDecisionAudit(c, w, "withdraw.approve", qymodel.ActorAdmin, a, err.Error(), qymodel.ResultFail)
		return nil, err
	}
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
	v := toAdminView(w, nil)
	// 决策接口的响应与详情接口共用同一份视图，欠账状态也必须一致：
	// 否则审批完的那一屏会把已经存在的欠账标记“洗”成空。
	fillDebtStatus([]*adminOrderView{v})
	return v, nil
}

// reject 驳回申请,佣金退回可用池。理由必填 —— 需求明确要求用户能看到"为什么被拒"。
func reject(c *gin.Context, id int64, rawReason string) (*adminOrderView, error) {
	reason, err := checkRunes(rawReason, maxReasonRunes)
	if err != nil || reason == "" {
		return nil, errReasonRequired
	}
	w, err := loadDecidableWithdrawal(c, id)
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
	v := toAdminView(w, nil)
	// 决策接口的响应与详情接口共用同一份视图，欠账状态也必须一致：
	// 否则审批完的那一屏会把已经存在的欠账标记“洗”成空。
	fillDebtStatus([]*adminOrderView{v})
	return v, nil
}

// payoutInput 是一次「标记已发放」的入参。
//
// ConfirmQuota / ConfirmAmount 是管理员对"我实际发了多少"的复述,必填且必须与
// 单据金额逐值相等(见 checkPayoutConfirm)。
type payoutInput struct {
	PayoutRef string
	// ConfirmQuota 用于 method=quota:管理员手工加进主库的额度。
	ConfirmQuota int64
	// ConfirmAmount 用于 method=fiat:线下实际打出去的法币金额,decimal 字符串。
	ConfirmAmount string
	PaidAt        int64
	Note          string
}

// markPayout 登记「管理员已经把钱发出去了」,佣金随之核销(frozen → withdrawn)。
//
// # 系统不动钱,所以这一步全靠人的声明
//
// quota 单由管理员在主库那边手工加额度,fiat 单由管理员线下打款 —— 两条路本模块
// 都不参与。本函数唯一的副作用是在扩展库一个事务里改状态 + 核销佣金,
// 它**从不触碰主库 users.quota**。
//
// # 为什么两个方式共用这一个出口
//
// 这里以前有一道 method 闸门:markPaid 只接受 fiat 单,quota 单必须走
// creditQuota(自动到账)。那道闸门的全部理由是"quota 单有一条会真的加额度的
// 正向出口,别让人误用只核销不给钱的这一条"。自动到账下线之后,两种方式的
// 正向出口本来就是同一件事 —— 人发钱、系统记账 —— 再留一道闸门就只是在挡
// 一条并不存在的错路。
//
// 但那道闸门原本挡住的风险(paid 是终态、不可逆、误标记等于佣金被吃掉)一分没少,
// 所以换成了两道**更贴切**的闸门:
//
//	① payout_ref 必填 —— 没有凭证的发放记录在争议时等于没发;
//	② 实发金额必填且必须与单据金额相等 —— 让"发多少"成为这个终态动作的
//	   显式入参,而不是点一下按钮的隐式后果。一个对着 approved 队列无差别
//	   POST 的运维脚本,过不了这一关。
//
// 实发金额刻意**不落新列**:它按定义恒等于单据上已有的 Quota / NetAmount,
// 多存一份就是又一处会漂移的拷贝(而单据金额的单一真相源正是上一轮刚统一的
// 东西)。它作为确认入参进事件流水的 Detail,那里是 append-only 的审计面。
func markPayout(c *gin.Context, id int64, in payoutInput) (*adminOrderView, error) {
	ref, err := checkRunes(in.PayoutRef, maxPayoutRefRunes)
	if err != nil || ref == "" {
		return nil, errPayoutRefMissing
	}
	w, err := loadDecidableWithdrawal(c, id)
	if err != nil {
		return nil, err
	}
	if err := checkPayoutConfirm(w, in); err != nil {
		return nil, err
	}
	if err := ensureNoOutstandingDebt(w); err != nil {
		writeDecisionAudit(c, w, "withdraw.payout", qymodel.ActorAdmin, actorOf(c), err.Error(), qymodel.ResultFail)
		return nil, err
	}

	now := common.GetTimestamp()
	// 允许补录历史发放时间,但不接受未来时间:那要么是客户端时钟错了,
	// 要么是有人想把发放记录做到对账窗口之外。留 5 分钟容忍时钟漂移。
	paidAt := in.PaidAt
	if paidAt <= 0 || paidAt > now+300 {
		paidAt = now
	}
	a := actorOf(c)

	err = db.Get().Transaction(func(tx *gorm.DB) error {
		if err := applyTransition(tx, w, transition{
			From: StatusApproved, To: StatusPaid, Action: ActionPay,
			ActorType: qymodel.ActorAdmin, ActorId: a.Id, ActorName: a.Name, IP: a.IP,
			Detail: common.MapToJsonStr(map[string]any{
				"payout_ref":     ref,
				"paid_at":        paidAt,
				"method":         w.Method,
				"confirm_quota":  in.ConfirmQuota,
				"confirm_amount": strings.TrimSpace(in.ConfirmAmount),
			}),
			Updates: map[string]any{
				"payout_operator_id":   a.Id,
				"payout_operator_name": truncate(a.Name, 64),
				"paid_at":              paidAt,
				"payout_ref":           ref,
				"payout_note":          truncate(in.Note, 255),
			},
		}); err != nil {
			return err
		}
		return commission.SettleFrozen(tx, w.UserId, w.Quota, w.WithdrawNo)
	})
	if err != nil {
		return nil, err
	}
	// applyTransition 只把 status / updated_at 写回内存对象，Updates 里的其余键一概
	// 不回写，所以每个调用点都要自己补齐 —— 这里原先补了四个、漏了 payout_note，
	// 于是 mark-paid 的**响应**里 payout_note 恒为空串而库里是对的。同一个
	// adminOrderView 结构，读接口给真值、写接口给空串，按写接口响应记账或回显的
	// 集成方会拿到一个假的空备注。
	w.PaidAt, w.PayoutRef = paidAt, ref
	w.PayoutOperatorId, w.PayoutOperatorName = a.Id, a.Name
	w.PayoutNote = truncate(in.Note, 255)
	writeDecisionAudit(c, w, "withdraw.payout", qymodel.ActorAdmin, a, ref, qymodel.ResultOK)
	v := toAdminView(w, nil)
	// 决策接口的响应与详情接口共用同一份视图，欠账状态也必须一致：
	// 否则审批完的那一屏会把已经存在的欠账标记“洗”成空。
	fillDebtStatus([]*adminOrderView{v})
	return v, nil
}

// checkPayoutConfirm 校验管理员复述的实发金额与单据金额一致。
//
// 按方式取不同的判据,是因为两种单据的"该发多少"根本是两个量纲:
// quota 单发的是站内额度(整数),fiat 单发的是实付法币(decimal,已扣手续费)。
// 拿错一个来比等于没比。
func checkPayoutConfirm(w *Withdrawal, in payoutInput) error {
	if w.Method == config.WithdrawMethodFiat {
		raw := strings.TrimSpace(in.ConfirmAmount)
		if raw == "" {
			return errPayoutAmountRequired
		}
		got, err := decimal.NewFromString(raw)
		if err != nil {
			return errPayoutAmountRequired
		}
		// Equal 比的是数值不是字面量:界面上回显的 "850.00" 与库里的
		// "850.000000" 是同一个数,不该被判成不一致。
		if !got.Equal(w.NetAmount) {
			return errPayoutAmountMismatch
		}
		return nil
	}
	if in.ConfirmQuota <= 0 {
		return errPayoutAmountRequired
	}
	if in.ConfirmQuota != w.Quota {
		return errPayoutAmountMismatch
	}
	return nil
}

// markFailed 标记发放失败,佣金退回可用池。理由必填。
//
// 这是「扣了佣金但发不出去」的唯一出口,两种方式都适用:线下打款被银行退回、
// 管理员发现收款信息有误、或者单纯是队列里积压太久决定退回让用户重提。
func markFailed(c *gin.Context, id int64, rawReason string) (*adminOrderView, error) {
	reason, err := checkRunes(rawReason, maxReasonRunes)
	if err != nil || reason == "" {
		return nil, errReasonRequired
	}
	w, err := loadDecidableWithdrawal(c, id)
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
	v := toAdminView(w, nil)
	// 决策接口的响应与详情接口共用同一份视图，欠账状态也必须一致：
	// 否则审批完的那一屏会把已经存在的欠账标记“洗”成空。
	fillDebtStatus([]*adminOrderView{v})
	return v, nil
}

// writeDecisionAudit 记录一次人工决策。
//
// 每一次人工决策都必须落审计:没有它,"这笔为什么被拒""谁批准的""谁说发过了"
// 事后无法自证。人工发放模型下这一条比以前更重要 —— 系统不动钱,审计表就是
// 争议时唯一能拿出来的东西。
//
// result 由调用方给出而不是写死 ResultOK:被自审自批闸门挡下的尝试要记成
// ResultFail,把它记成成功事后复盘会得出"管理员处理过一次"的错误结论。
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

// loadDecidableWithdrawal 取一张即将被人工下结论的单,并挡住自审自批。
//
// 四个管理端决定(通过 / 驳回 / 标记已发放 / 标记发放失败)全部从这里取单,
// 是刻意的单一入口:审计发现的那条链
// 「自己给自己记佣金 → 自己发起提现 → **自己批准** → 自己标记已发放」
// 在这里断掉,而只要有人再加第五个决定,他要么走这个入口、要么在
// review_self_approval_test.go 的 AST 守卫下变红。
//
// 只读的 handleAdminGet 刻意不走这里:管理员能看到自己那张单的详情不构成问题,
// 而把它一起挡掉只会让人以为单据丢了。
func loadDecidableWithdrawal(c *gin.Context, id int64) (*Withdrawal, error) {
	w, err := loadWithdrawal(id)
	if err != nil {
		return nil, err
	}
	// 自营与越级两条判据都在 guard.ActorMayActOn,与佣金侧共用一份实现 ——
	// 只挡"同一个人"的话,两个 role=10 管理员互相批对方的单,闸门一次都不会响,
	// 整条链只是从一个人变成两个人。
	//
	// 两种被拒都留痕:一次被拒的审核不是手滑,它是这条链上最容易被反复尝试的
	// 一步,而事后仲裁只认审计表。写成 ResultFail 是如实的 —— 单据没动。
	switch err := guard.ActorMayActOnCtx(c, w.UserId); {
	case err == nil:
	case errors.Is(err, guard.ErrActorIsTarget):
		writeDecisionAudit(c, w, "withdraw.self_review_denied",
			qymodel.ActorAdmin, actorOf(c), errSelfReview.Msg, qymodel.ResultFail)
		return nil, errSelfReview
	case errors.Is(err, guard.ErrTargetNotLower), errors.Is(err, guard.ErrTargetMissing):
		// 目标查不到时同样拒绝:放行等于对一个连角色都读不出来的 id 下资金结论。
		writeDecisionAudit(c, w, "withdraw.peer_review_denied",
			qymodel.ActorAdmin, actorOf(c), errPeerReview.Msg, qymodel.ResultFail)
		return nil, errPeerReview
	default:
		return nil, err
	}
	return w, nil
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
	// 上限兜底:事件是 append-only,理论上不会太多,但不能让详情接口
	// 把一张被反复操作过的单的全部事件拉进内存。
	err := db.Get().Where("withdrawal_id = ?", withdrawalId).
		Order("id asc").Limit(200).Find(&rows).Error
	if err != nil {
		db.MarkFailure(err)
		return nil, err
	}
	return rows, nil
}
