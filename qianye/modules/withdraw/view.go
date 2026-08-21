package withdraw

import (
	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/qianye/config"
	qymodel "github.com/QuantumNous/new-api/qianye/model"
	"github.com/QuantumNous/new-api/qianye/modules/commission"
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
	// 什么时候拒绝的 → ReviewedAt;拒绝理由 → RejectReason;什么时候发的钱 → PaidAt。
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
	UserId             int    `json:"user_id"`
	Username           string `json:"username"`
	RiskFlags          string `json:"risk_flags"`
	ReviewerId         int    `json:"reviewer_id"`
	ReviewerName       string `json:"reviewer_name"`
	PayoutOperatorId   int    `json:"payout_operator_id"`
	PayoutOperatorName string `json:"payout_operator_name"`
	PayoutNote         string `json:"payout_note"`
	ClientIp           string `json:"client_ip"`

	// DebtBlocked / UnsettledAmount 是收款人当下的冲正欠账状态，实时读佣金余额行。
	//
	// 它们不是建单时的快照（RiskFlags 才是）：冲正欠账只在【提交提现】那一刻
	// 拦一次，而冲正按设计只吃 available、吃不到已经冻住的 frozen。于是“先提现
	// 冻住 → 下线退款触发冲正 → 管理员照常审批放款”是一条完整的、无告警的通路，
	// 而 approve / mark-paid 正是这笔钱最后一次还能被拦回来的地方。信号在系统里
	// 存在（佣金余额页有 debt_blocked 徽标与筛选），只是不在审核人正在看的那
	// 张单上。
	DebtBlocked bool `json:"debt_blocked"`
	// UnsettledAmount 为负即挂着没收回来的冲正差额。与其他 decimal 一样下发 string。
	UnsettledAmount string `json:"unsettled_amount"`

	// SLA 字段让前端不必自己算截止时间,也就不会因为客户端时钟偏差而误标红。
	//
	// 一张单在任一时刻只处在一道时限里(pending 归审核时限、approved 归发放时限),
	// 所以两者共用同一对字段,由 slaOf 按状态选判据。拆成四个字段只会让调用方
	// 每次都要先判状态再挑字段读,而挑错的那次不会有任何信号。
	SLADeadline int64 `json:"sla_deadline"`
	SLABreached bool  `json:"sla_breached"`
	// SLAKind ∈ ""|review|payout,说明上面那对字段说的是哪一道时限。
	SLAKind string `json:"sla_kind"`
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
		UserId:             w.UserId,
		Username:           w.Username,
		RiskFlags:          w.RiskFlags,
		ReviewerId:         w.ReviewerId,
		ReviewerName:       w.ReviewerName,
		PayoutOperatorId:   w.PayoutOperatorId,
		PayoutOperatorName: w.PayoutOperatorName,
		PayoutNote:         w.PayoutNote,
		ClientIp:           w.ClientIp,
	}
	v.SLADeadline, v.SLABreached, v.SLAKind = slaOf(w)
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

// fillDebtStatus 把收款人的冲正欠账状态填进管理端视图。
//
// 审核人就是在这一屏上决定要不要把钱发出去的，而欠账判据只在提交提现那
// 一刻拦过一次 —— 详见 commission.LoadDebtStatuses。读不到时保守地留空：它是一个
// 展示维度，不能把整个审核页卡掉（那一页还要用来驳回与排障），错误已由
// db.MarkFailure 记下。
func fillDebtStatus(views []*adminOrderView) {
	if len(views) == 0 {
		return
	}
	ids := make([]int, 0, len(views))
	for _, v := range views {
		ids = append(ids, v.UserId)
	}
	statuses, err := commission.LoadDebtStatuses(ids)
	if err != nil {
		return
	}
	for _, v := range views {
		st, ok := statuses[v.UserId]
		if !ok {
			continue
		}
		v.DebtBlocked = st.Blocked
		v.UnsettledAmount = st.Unsettled.String()
	}
}

// SLA 种类。空串表示这张单当前不在任何一道时限里(终态)。
const (
	SLAKindReview = "review"
	SLAKindPayout = "payout"
)

// slaOf 计算这张单当前所处那道时限的截止点。
//
// 两道时限、两个起点,刻意不共用 created_at:
//
//	pending  —— 审核时限,从提交(created_at)起算,归审核人;
//	approved —— 发放时限,从审核通过(reviewed_at)起算,归发放人。
//
// 用同一个起点的话,一张审核拖了三天的单在通过的那一刻就已经"发放超时"了,
// 而发放的人一分钟都还没耽误 —— 两道时限就都不再指向任何具体的人。
//
// 终态单一律不标:已经处理完的单再标红只会污染队列的告警信号。
func slaOf(w *Withdrawal) (deadline int64, breached bool, kind string) {
	cfg := config.Get().Withdraw
	switch w.Status {
	case StatusPending:
		if cfg.ReviewSLAHours <= 0 {
			return 0, false, ""
		}
		deadline = w.CreatedAt + int64(cfg.ReviewSLAHours)*3600
		return deadline, common.GetTimestamp() > deadline, SLAKindReview
	case StatusApproved:
		// reviewed_at 为 0 只可能出现在本改造之前落库的存量 approved 单上。
		// 回落到 updated_at 而不是当成"不计时":那批单正是最该被看见的积压。
		start := w.ReviewedAt
		if start <= 0 {
			start = w.UpdatedAt
		}
		if cfg.PayoutSLAHours <= 0 || start <= 0 {
			return 0, false, ""
		}
		deadline = start + int64(cfg.PayoutSLAHours)*3600
		return deadline, common.GetTimestamp() > deadline, SLAKindPayout
	default:
		return 0, false, ""
	}
}
