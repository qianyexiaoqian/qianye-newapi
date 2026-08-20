package withdraw

import (
	"github.com/QuantumNous/new-api/common"
	qymodel "github.com/QuantumNous/new-api/qianye/model"

	"gorm.io/gorm"
)

// 提现单状态。
//
//	pending   待审核    —— 佣金已从可用池扣除(转入 frozen)
//	approved  待发放    —— 审核通过,等待管理员人工发放(佣金仍在 frozen)
//	paid      已发放    (终态,佣金核销:frozen → withdrawn)
//	rejected  已驳回    (终态,佣金退回 available)
//	cancelled 已撤销    (终态,佣金退回 available)
//	failed    发放失败  (终态,佣金退回 available)
//
// # 这里曾经还有一个 paying
//
// 它是 quota 方式「审核通过 → 跨库给主库 users.quota 加额度」那条自动到账链路
// 的执行窗口。产品口径已经改成:**提现只做佣金扣除,金额由管理员手动发放**
// (加站内额度或线下打款),系统不再自己动任何一分钱。
//
// 于是 paying 连同它背后整套东西 —— 两阶段资金单、主库 outbox 探针、
// reconcile_state=hold 的人工裁决、approved → paying 的全集群准入 CAS ——
// 一起没有了存在理由:它们解决的全部问题都是「跨库写主库时钱到底动没动」,
// 而现在这条链路上没有任何跨库写入。留着一个永远不会被进入的状态,
// 只会让下一个读状态机的人以为还有一条自动出钱的路。
const (
	StatusPending   = "pending"
	StatusApproved  = "approved"
	StatusPaid      = "paid"
	StatusRejected  = "rejected"
	StatusCancelled = "cancelled"
	StatusFailed    = "failed"
)

// 事件动作。稳定的英文标识,前端按 key 做 i18n,不存自然语言。
const (
	ActionSubmit  = "submit"
	ActionCancel  = "cancel"
	ActionApprove = "approve"
	ActionReject  = "reject"
	ActionPay     = "pay"
	ActionFail    = "fail"
)

// allowedTransitions 是状态机的唯一真相。
//
// 缺席即非法:任何不在表里的跃迁都会被 canTransit 拒绝,不需要在每个 handler
// 里各写一遍 if。终态(paid/rejected/cancelled/failed)没有出边 ——
// 提现单一旦终结就不再变化,误标记已发放的冲正走人工补偿(佣金手工调整),
// 而不是给状态机加一条反向边:那条边会立刻变成"把已核销的佣金再退一次"的入口。
var allowedTransitions = map[string]map[string]bool{
	StatusPending: {
		StatusApproved:  true, // 管理员审核通过,进入待发放队列
		StatusRejected:  true, // 管理员驳回(理由必填,佣金退回)
		StatusCancelled: true, // 用户本人撤销(佣金退回)
	},
	StatusApproved: {
		StatusPaid:   true, // 管理员标记已发放(佣金核销)
		StatusFailed: true, // 管理员标记发放失败(理由必填,佣金退回)
	},
}

// canTransit 判断一次状态跃迁是否合法。
func canTransit(from, to string) bool {
	return allowedTransitions[from][to]
}

// activeStatuses / terminalStatuses 是同一套状态的切片投影,供 SQL 的 `status IN ?` 使用。
//
// isTerminal 由 terminalStatuses 派生而不是各写一份 switch:新增状态却忘了登记,
// 一边会让"未终态单上限"漏掉它,另一边会让保留期清理提前抹掉还要发放的单据密文。
// 两处必须由同一个真相派生。
var (
	activeStatuses   = []string{StatusPending, StatusApproved}
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

	// Updates 是随状态一起写入的业务列(审核人、发放凭证等)。
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
