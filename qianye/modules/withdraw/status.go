package withdraw

import (
	"github.com/QuantumNous/new-api/common"
	qymodel "github.com/QuantumNous/new-api/qianye/model"

	"gorm.io/gorm"
)

// 提现单状态。
//
//	pending   待审核    —— 佣金已冻结
//	approved  已通过    —— quota 方式等待兑现;fiat 方式等待人工打款
//	paying    兑现中    —— 跨库执行窗口,只属于 quota 方式
//	paid      已到账/已打款(终态,佣金核销)
//	rejected  已拒绝    (终态,佣金退回)
//	cancelled 已撤销    (终态,佣金退回)
//	failed    失败      (终态,佣金退回)
const (
	StatusPending   = "pending"
	StatusApproved  = "approved"
	StatusPaying    = "paying"
	StatusPaid      = "paid"
	StatusRejected  = "rejected"
	StatusCancelled = "cancelled"
	StatusFailed    = "failed"
)

// ReconcileHold 是"主库结果不可判定,已转人工"的队列标记。
//
// 它附着在 paying 上而不是做成独立状态:独立状态会让状态机凭空多出一整套
// 与"钱到底动没动"无关的转移边。裁决完成后标记清空,历史留在事件流水里 ——
// 标记是实时队列,事件才是账。
const ReconcileHold = "hold"

// 事件动作。稳定的英文标识,前端按 key 做 i18n,不存自然语言。
const (
	ActionSubmit      = "submit"
	ActionCancel      = "cancel"
	ActionApprove     = "approve"
	ActionReject      = "reject"
	ActionSettleStart = "settle_start"
	ActionSettleDone  = "settle_done"
	ActionPay         = "pay"
	ActionFail        = "fail"
	ActionHold        = "hold"
	ActionResolve     = "resolve"
)

// allowedTransitions 是状态机的唯一真相。
//
// 缺席即非法:任何不在表里的跃迁都会被 canTransit 拒绝,不需要在每个 handler
// 里各写一遍 if。终态(paid/rejected/cancelled/failed)没有出边 ——
// 提现单一旦终结就不再自动变化,误打款的冲正走人工而不是反向接口。
var allowedTransitions = map[string]map[string]bool{
	StatusPending: {
		StatusApproved:  true, // 管理员审核通过
		StatusRejected:  true, // 管理员拒绝(理由必填)
		StatusCancelled: true, // 用户本人撤销
	},
	StatusApproved: {
		StatusPaying: true, // quota 方式进入跨库兑现
		StatusPaid:   true, // fiat 方式管理员标记已打款
		StatusFailed: true, // fiat 方式管理员标记打款失败
	},
	StatusPaying: {
		StatusPaid:   true, // 主库确认已加额度
		StatusFailed: true, // 主库确定性失败(用户禁用/额度溢出)
	},
}

// canTransit 判断一次状态跃迁是否合法。
func canTransit(from, to string) bool {
	return allowedTransitions[from][to]
}

// activeStatuses / terminalStatuses 是同一套状态的切片投影,供 SQL 的 `status IN ?` 使用。
//
// isTerminal 由 terminalStatuses 派生而不是各写一份 switch:新增状态却忘了登记,
// 一边会让"未终态单上限"漏掉它,另一边会让保留期清理提前抹掉还要打款的单据密文。
// 两处必须由同一个真相派生。
var (
	activeStatuses   = []string{StatusPending, StatusApproved, StatusPaying}
	terminalStatuses = []string{StatusPaid, StatusRejected, StatusCancelled, StatusFailed}
)

// isTerminal 表示该状态不会再变化,佣金已经落定(核销或退回)。
func isTerminal(s string) bool {
	for _, t := range terminalStatuses {
		if s == t {
			return true
		}
	}
	return false
}

// transition 描述一次状态跃迁及其副作用。
type transition struct {
	From   string
	To     string
	Action string

	ActorType string
	ActorId   int
	ActorName string

	Reason string
	Detail string
	IP     string

	// Updates 是随状态一起写入的业务列(审核人、打款单号等)。
	// 与状态在同一条 UPDATE 里落库,避免"状态变了但审核人没记上"的半截数据。
	Updates map[string]any
}

// applyTransition 用带状态条件的 UPDATE 完成跃迁,并在同一事务内写事件。
//
// 非法跃迁与并发冲突一律靠 `WHERE id=? AND status=?` 的 RowsAffected 判定,
// 绝不"先读后写" —— 多节点下先读后写必然出现两个管理员同时把一张单处理两次。
//
// 返回 errStatusConflict 表示单据已被别人处理,调用方应把当前状态回给前端,
// 而不是重试。
func applyTransition(tx *gorm.DB, w *Withdrawal, t transition) error {
	if !canTransit(t.From, t.To) {
		return errIllegalTransition
	}
	now := common.GetTimestamp()
	updates := map[string]any{"status": t.To, "updated_at": now}
	for k, v := range t.Updates {
		updates[k] = v
	}

	res := tx.Model(&Withdrawal{}).
		Where("id = ? AND status = ?", w.Id, t.From).
		Updates(updates)
	if res.Error != nil {
		return res.Error
	}
	if res.RowsAffected == 0 {
		return errStatusConflict
	}

	w.Status = t.To
	w.UpdatedAt = now
	return writeEvent(tx, w, t)
}

// writeEvent 追加一条状态机流水。事件只增不改,因此天然免并发。
func writeEvent(tx *gorm.DB, w *Withdrawal, t transition) error {
	actor := t.ActorType
	if actor == "" {
		actor = qymodel.ActorSystem
	}
	return tx.Create(&Event{
		WithdrawalId: w.Id,
		WithdrawNo:   w.WithdrawNo,
		FromStatus:   t.From,
		ToStatus:     t.To,
		Action:       t.Action,
		ActorType:    actor,
		ActorId:      t.ActorId,
		ActorName:    truncate(t.ActorName, 64),
		Reason:       truncate(t.Reason, 512),
		Detail:       truncate(t.Detail, 1024),
		Ip:           truncate(t.IP, 64),
		CreatedAt:    common.GetTimestamp(),
	}).Error
}
