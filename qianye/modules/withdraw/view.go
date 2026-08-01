package withdraw

import (
	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/qianye/config"
	qymodel "github.com/QuantumNous/new-api/qianye/model"
)

// 对外视图。
//
// 所有 decimal 一律以 string 下发,禁止 JSON number:JS 的 Number 只有 2^53 的
// 精度,decimal(18,6) 与 decimal(18,8) 会在前端悄悄丢位,变成对不上的金额。
//
// 明文收款信息永远不出现在任何视图里,只能通过带审计的专用接口获取。

type eventView struct {
	Action     string `json:"action"`
	FromStatus string `json:"from_status"`
	ToStatus   string `json:"to_status"`
	ActorType  string `json:"actor_type"`
	ActorName  string `json:"actor_name"`
	Reason     string `json:"reason"`
	Detail     string `json:"detail,omitempty"`
	CreatedAt  int64  `json:"created_at"`
}

type orderView struct {
	Id         int64  `json:"id"`
	WithdrawNo string `json:"withdraw_no"`
	Method     string `json:"method"`
	Status     string `json:"status"`
	Quota      int64  `json:"quota"`

	Currency           string `json:"currency"`
	FrozenQuotaPerUnit string `json:"frozen_quota_per_unit"`
	FrozenFxRate       string `json:"frozen_fx_rate"`
	GrossAmount        string `json:"gross_amount"`
	FeeAmount          string `json:"fee_amount"`
	NetAmount          string `json:"net_amount"`
	FeeBps             int    `json:"fee_bps"`

	PayeeChannel string `json:"payee_channel"`
	PayeeMasked  string `json:"payee_masked"`
	Remark       string `json:"remark"`
	// HasProof 只说明"这张单附过凭证",不保证现在还下载得到 ——
	// 保留期到期或单据被拒绝之后图片会被清掉,那时下载接口回 qy_wd_proof_purged。
	HasProof bool `json:"has_proof"`

	// 需求原文的三个问题在这里闭环:
	// 什么时候拒绝的 → ReviewedAt;拒绝理由 → RejectReason;什么时候打的款 → PaidAt。
	ReviewedAt   int64  `json:"reviewed_at"`
	RejectReason string `json:"reject_reason"`
	PaidAt       int64  `json:"paid_at"`
	PayoutRef    string `json:"payout_ref"`
	FailReason   string `json:"fail_reason"`

	CreatedAt int64 `json:"created_at"`
	UpdatedAt int64 `json:"updated_at"`

	Events []eventView `json:"events,omitempty"`
}

// adminOrderView 在用户视图之上补齐排障与风控字段。
type adminOrderView struct {
	orderView
	OrderNo            string `json:"order_no"`
	UserId             int    `json:"user_id"`
	Username           string `json:"username"`
	RiskFlags          string `json:"risk_flags"`
	ReviewerId         int    `json:"reviewer_id"`
	ReviewerName       string `json:"reviewer_name"`
	PayoutOperatorId   int    `json:"payout_operator_id"`
	PayoutOperatorName string `json:"payout_operator_name"`
	PayoutNote         string `json:"payout_note"`
	ReconcileState     string `json:"reconcile_state"`
	ClientIp           string `json:"client_ip"`

	// SLA 字段让前端不必自己算截止时间,也就不会因为客户端时钟偏差而误标红。
	SLADeadline int64 `json:"sla_deadline"`
	SLABreached bool  `json:"sla_breached"`
}

func baseView(w *Withdrawal) orderView {
	return orderView{
		Id:                 w.Id,
		WithdrawNo:         w.WithdrawNo,
		Method:             w.Method,
		Status:             w.Status,
		Quota:              w.Quota,
		Currency:           w.Currency,
		FrozenQuotaPerUnit: w.FrozenQuotaPerUnit.String(),
		FrozenFxRate:       w.FrozenFxRate.String(),
		GrossAmount:        w.GrossAmount.String(),
		FeeAmount:          w.FeeAmount.String(),
		NetAmount:          w.NetAmount.String(),
		FeeBps:             w.FeeBps,
		PayeeChannel:       w.PayeeChannel,
		PayeeMasked:        w.PayeeMasked,
		Remark:             w.Remark,
		HasProof:           w.HasProof,
		ReviewedAt:         w.ReviewedAt,
		RejectReason:       w.RejectReason,
		PaidAt:             w.PaidAt,
		PayoutRef:          w.PayoutRef,
		FailReason:         w.FailReason,
		CreatedAt:          w.CreatedAt,
		UpdatedAt:          w.UpdatedAt,
	}
}

// toUserView 构造普通用户可见的单据视图。
func toUserView(w *Withdrawal, events []Event) *orderView {
	v := baseView(w)
	for _, e := range events {
		v.Events = append(v.Events, eventView{
			Action:     e.Action,
			FromStatus: e.FromStatus,
			ToStatus:   e.ToStatus,
			ActorType:  e.ActorType,
			// 对普通用户隐去具体管理员姓名,否则用户可以把全站管理员账号枚举出来。
			// Detail 同理不下发:里面有内部单号与错误码。
			ActorName: publicActorName(e),
			Reason:    e.Reason,
			CreatedAt: e.CreatedAt,
		})
	}
	return &v
}

func publicActorName(e Event) string {
	if e.ActorType == qymodel.ActorUser {
		return e.ActorName
	}
	return ""
}

// toAdminView 构造管理端视图,含全部排障字段与 SLA 标记。
func toAdminView(w *Withdrawal, events []Event) *adminOrderView {
	v := &adminOrderView{
		orderView:          baseView(w),
		OrderNo:            w.OrderNo,
		UserId:             w.UserId,
		Username:           w.Username,
		RiskFlags:          w.RiskFlags,
		ReviewerId:         w.ReviewerId,
		ReviewerName:       w.ReviewerName,
		PayoutOperatorId:   w.PayoutOperatorId,
		PayoutOperatorName: w.PayoutOperatorName,
		PayoutNote:         w.PayoutNote,
		ReconcileState:     w.ReconcileState,
		ClientIp:           w.ClientIp,
	}
	v.SLADeadline, v.SLABreached = slaOf(w)
	for _, e := range events {
		v.Events = append(v.Events, eventView{
			Action:     e.Action,
			FromStatus: e.FromStatus,
			ToStatus:   e.ToStatus,
			ActorType:  e.ActorType,
			ActorName:  e.ActorName,
			Reason:     e.Reason,
			Detail:     e.Detail,
			CreatedAt:  e.CreatedAt,
		})
	}
	return v
}

// slaOf 计算审核时效截止点。
//
// 只对还没审完的单有意义:已经处理过的单再标红只会污染队列的告警信号。
func slaOf(w *Withdrawal) (deadline int64, breached bool) {
	hours := config.Get().Withdraw.ReviewSLAHours
	if hours <= 0 || w.Status != StatusPending {
		return 0, false
	}
	deadline = w.CreatedAt + int64(hours)*3600
	return deadline, common.GetTimestamp() > deadline
}
