package ticket

import (
	qymodel "github.com/QuantumNous/new-api/qianye/model"
)

// attachmentView 是一张图片的对外投影。
//
// 只给 ref,不给落盘文件名、不给 sha256、不给上传者 id:下载走鉴权接口
// (GET /ticket/attachments/:ref),ref 是唯一需要对外存在的标识。
type attachmentView struct {
	Ref      string `json:"ref"`
	MimeType string `json:"mime_type"`
	Size     int64  `json:"size"`
}

// messageView 是一条消息的对外投影。
type messageView struct {
	Id         int64  `json:"id"`
	AuthorType string `json:"author_type"`
	// AuthorName 在用户端恒为空串(见 toUserView),前端按 author_type 渲染
	// "我" / "客服"。管理端给真名 —— 内部要能回答"这句话是谁说的"。
	AuthorName  string           `json:"author_name"`
	Body        string           `json:"body"`
	Internal    bool             `json:"internal"`
	Attachments []attachmentView `json:"attachments"`
	CreatedAt   int64            `json:"created_at"`
}

// ticketView 是工单的对外投影(用户端与管理端共用结构,字段按端裁剪)。
type ticketView struct {
	TicketNo string `json:"ticket_no"`
	// Id 只在管理端有意义(管理端按 id 寻址);用户端一律用 ticket_no。
	Id       int64  `json:"id"`
	UserId   int    `json:"user_id"`
	Username string `json:"username"`

	Title        string `json:"title"`
	Priority     string `json:"priority"`
	Status       string `json:"status"`
	MessageCount int    `json:"message_count"`

	AssigneeId   int    `json:"assignee_id"`
	AssigneeName string `json:"assignee_name"`

	LastReplyAt    int64 `json:"last_reply_at"`
	FirstRepliedAt int64 `json:"first_replied_at"`
	ClosedAt       int64 `json:"closed_at"`

	ClosedBy  string `json:"closed_by"`
	CreatedAt int64  `json:"created_at"`
	UpdatedAt int64  `json:"updated_at"`

	// Messages 只在详情接口非空。列表接口留空切片而不是 null ——
	// nil 切片序列化成 JSON null,前端对着它调 .map 会整页白屏
	// (判据见 qianye/json_array_guard_test.go)。
	Messages []messageView `json:"messages"`
}

// toUserView 构造用户端视图。
//
// 三处收窄,每一处都对应一个具体的泄漏:
//
//  1. **内部备注不下发**。调用方必须已经在 SQL 的 WHERE 里滤掉了它们
//     (见 loadUserMessages);这里的 `if m.Internal { continue }` 是第二道 ——
//     少写一个条件的后果是把客服的内部判断原样发给被投诉的那个用户。
//  2. **管理员姓名不下发**。给了的话,任何一个用户发一张工单就能拿到一个
//     真实管理员账号名,那是撞库与钓鱼的起点。前端按 author_type 渲染"客服"。
//  3. **指派信息不下发**。它是内部排班,用户不需要也不该知道。
func toUserView(t *Ticket, messages []Message, byMessage map[int64][]Attachment) *ticketView {
	v := baseView(t)
	v.Id = 0
	v.UserId = 0
	v.Username = ""
	v.AssigneeId = 0
	v.AssigneeName = ""

	v.Messages = make([]messageView, 0, len(messages))
	for i := range messages {
		m := &messages[i]
		if m.Internal {
			continue
		}
		mv := toMessageView(m, byMessage)
		if m.AuthorType != qymodel.ActorUser {
			mv.AuthorName = ""
		}
		v.Messages = append(v.Messages, mv)
	}
	return v
}

// toAdminView 构造管理端视图:全字段,含内部备注与指派。
func toAdminView(t *Ticket, messages []Message, byMessage map[int64][]Attachment) *ticketView {
	v := baseView(t)
	v.Messages = make([]messageView, 0, len(messages))
	for i := range messages {
		v.Messages = append(v.Messages, toMessageView(&messages[i], byMessage))
	}
	return v
}

func baseView(t *Ticket) *ticketView {
	return &ticketView{
		TicketNo:       t.TicketNo,
		Id:             t.Id,
		UserId:         t.UserId,
		Username:       t.Username,
		Title:          t.Title,
		Priority:       t.Priority,
		Status:         t.Status,
		MessageCount:   t.MessageCount,
		AssigneeId:     t.AssigneeId,
		AssigneeName:   t.AssigneeName,
		LastReplyAt:    t.LastReplyAt,
		FirstRepliedAt: t.FirstRepliedAt,
		ClosedAt:       t.ClosedAt,
		ClosedBy:       t.ClosedBy,
		CreatedAt:      t.CreatedAt,
		UpdatedAt:      t.UpdatedAt,
		// 列表接口不带消息,但这个字段仍必须是空切片而不是 nil。
		Messages: make([]messageView, 0),
	}
}

func toMessageView(m *Message, byMessage map[int64][]Attachment) messageView {
	rows := byMessage[m.Id]
	atts := make([]attachmentView, 0, len(rows))
	for i := range rows {
		atts = append(atts, attachmentView{
			Ref:      rows[i].Ref,
			MimeType: rows[i].MimeType,
			Size:     rows[i].Size,
		})
	}
	return messageView{
		Id:          m.Id,
		AuthorType:  m.AuthorType,
		AuthorName:  m.AuthorName,
		Body:        m.Body,
		Internal:    m.Internal,
		Attachments: atts,
		CreatedAt:   m.CreatedAt,
	}
}
