package ticket

import (
	"github.com/QuantumNous/new-api/common"
	qymodel "github.com/QuantumNous/new-api/qianye/model"
	"github.com/QuantumNous/new-api/qianye/service/audit"

	"github.com/gin-gonic/gin"
)

// ticketAudit 是一次工单动作的审计输入。
//
// 收成一个结构体而不是给 audit.Entry 直接填十来个字段:本模块有九个写入点
// (建单/回复/关单/重开/改等级/指派各自的成功与失败),每处各拼一遍 Entry
// 的结果必然是有的填了 TargetUserId、有的忘了 —— 而 target_user_id 正是
// "把某个用户的全部工单动作拉出来"这个查询唯一的依据。
type ticketAudit struct {
	TraceNo string
	Action  string

	ActorType string
	ActorId   int
	ActorName string

	// TargetUserId 恒为**工单所属用户**,不是操作者。管理员回复用户 A 的工单时,
	// 这里是 A —— 否则按用户查审计只能查到他自己发起的那一半。
	TargetUserId int

	Result string
	Reason string
	Before any
	After  any
}

// writeTicketAudit 落一条工单审计。
//
// 工单不动钱,为什么还要全量审计?因为它承载**承诺**:客服说过"这笔费用会退"、
// 管理员把等级从紧急改成普通、某张单被谁关掉了 —— 这些事后都要能解释。
// 消息本身是 append-only 的(见 model.go),但状态与等级的变更没有正文可查,
// 审计是它们唯一的痕迹。
func writeTicketAudit(c *gin.Context, a ticketAudit) {
	audit.Write(c, audit.Entry{
		TraceNo:      a.TraceNo,
		Category:     qymodel.AuditCategoryTicket,
		Action:       a.Action,
		ActorType:    a.ActorType,
		ActorUserId:  a.ActorId,
		ActorName:    a.ActorName,
		TargetUserId: a.TargetUserId,
		Result:       a.Result,
		Reason:       a.Reason,
		BeforeSnap:   snapshot(a.Before),
		AfterSnap:    snapshot(a.After),
	})
}

// snapshot 把快照值转成入库文本。字符串原样透传(有些快照本来就是一句人话)。
func snapshot(v any) string {
	if v == nil {
		return ""
	}
	if s, ok := v.(string); ok {
		return s
	}
	b, err := common.Marshal(v)
	if err != nil {
		return ""
	}
	return string(b)
}
