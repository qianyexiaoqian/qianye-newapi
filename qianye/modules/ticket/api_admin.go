package ticket

import (
	"errors"
	"strings"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/qianye/db"
	"github.com/QuantumNous/new-api/qianye/guard"
	"github.com/QuantumNous/new-api/qianye/httpq"
	qymodel "github.com/QuantumNous/new-api/qianye/model"

	"github.com/gin-gonic/gin"
	"gorm.io/gorm"
)

// handleAdminList 是客服队列。
//
// 排序:先按"等得最久的在前"(last_reply_at 升序),而不是按等级。
// 等级排前面听起来更合理,却会让一批被标成"紧急"的低价值工单永久压住
// 正常队列 —— 而等级是**用户自己填的**。等级的用处是筛选(?priority=urgent),
// 不是全局排序权重。
func handleAdminList(c *gin.Context) {
	if !guard.RequireAPI(c, guard.FlagTicket) {
		return
	}
	page, size := httpq.Paginate(c, listPaging)
	q := db.Get().Model(&Ticket{})
	q = applyStatusFilter(q, c.Query("status"))
	q = applyPriorityFilter(q, c.Query("priority"))
	if uid := httpq.Int(c, "user_id", 0); uid > 0 {
		q = q.Where("user_id = ?", uid)
	}
	if aid := httpq.Int(c, "assignee_id", 0); aid > 0 {
		q = q.Where("assignee_id = ?", aid)
	}
	// 关键词只匹配单号与标题,**不匹配正文**:正文是 text 列、没有索引,
	// 一次 LIKE '%...%' 就是全表扫,而客服队列是每分钟都在刷新的页面。
	// 想按正文找的场景应当先按用户或时间收窄。
	if kw := strings.TrimSpace(c.Query("keyword")); kw != "" {
		// LIKE 的通配符有**两个**:% 与 _。只转义 % 的话,客服照着单号搜
		// `TK_1785…`(或标题里带下划线的模型名)会把 _ 当成"任意单字符",
		// 命中一批毫不相关的单,而他会以为"就这些" —— 在争议工单上按单号
		// 定位时,这是一次会把人引向错误结论的检索。
		//
		// 转义符显式写成 '/' 而不是沿用默认:SQLite 的 LIKE 压根没有默认转义符,
		// 而 MySQL 的字符串字面量里 '\' 还要再转一次 —— 同一段 SQL 在三种数据库
		// 上的含义会不一样。ESCAPE 子句三种库都支持。
		like := "%" + strings.NewReplacer("/", "//", "%", "/%", "_", "/_").Replace(kw) + "%"
		q = q.Where(`ticket_no LIKE ? ESCAPE '/' OR title LIKE ? ESCAPE '/'`, like, like)
	}

	var total int64
	if err := q.Count(&total).Error; err != nil {
		db.MarkFailure(err)
		respondErr(c, err)
		return
	}
	rows := make([]Ticket, 0, size)
	if err := q.Order("last_reply_at asc, id asc").
		Offset(httpq.Offset(page, size)).Limit(size).Find(&rows).Error; err != nil {
		db.MarkFailure(err)
		respondErr(c, err)
		return
	}

	items := make([]*ticketView, 0, len(rows))
	for i := range rows {
		items = append(items, toAdminView(&rows[i], nil, nil))
	}
	respondOK(c, gin.H{"items": items, "total": total, "p": page, "page_size": size})
}

// handleAdminStats 返回各状态的角标数。
func handleAdminStats(c *gin.Context) {
	if !guard.RequireAPI(c, guard.FlagTicket) {
		return
	}
	type bucket struct {
		Status string `json:"status"`
		Count  int64  `json:"count"`
	}
	// 必须 make:库里一张单都没有时 Scan 不会给 rows 赋值,`var rows []bucket`
	// 会原样以 nil 下发成 JSON null,前端 `buckets.find(...)` 直接白屏。
	// 这正是 qianye/json_array_guard_test.go 记着的那个缺陷。
	rows := make([]bucket, 0, 4)
	err := db.Get().Model(&Ticket{}).
		Select("status, count(*) as count").Group("status").Scan(&rows).Error
	if err != nil {
		db.MarkFailure(err)
		respondErr(c, err)
		return
	}
	respondOK(c, gin.H{"buckets": rows})
}

// handleAdminGet 返回工单详情,含内部备注。
func handleAdminGet(c *gin.Context) {
	if !guard.RequireAPI(c, guard.FlagTicket) {
		return
	}
	id, ok := pathId(c)
	if !ok {
		respondErr(c, errInvalidParam)
		return
	}
	t, err := loadTicket(id)
	if err != nil {
		respondErr(c, err)
		return
	}
	messages, err := loadAllMessages(t.Id)
	if err != nil {
		respondErr(c, err)
		return
	}
	atts, err := loadAttachmentsOfTicket(t.Id)
	if err != nil {
		respondErr(c, err)
		return
	}
	respondOK(c, toAdminView(t, messages, atts))
}

// handleAdminReply 由管理员回复或写内部备注。
func handleAdminReply(c *gin.Context) {
	if !guard.RequireAPI(c, guard.FlagTicket) {
		return
	}
	id, ok := pathId(c)
	if !ok {
		respondErr(c, errInvalidParam)
		return
	}
	var req replyRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		respondErr(c, errInvalidParam)
		return
	}
	body, err := acceptBody(req.Body)
	if err != nil {
		respondErr(c, err)
		return
	}
	refs, err := acceptRefs(req.AttachmentRefs)
	if err != nil {
		respondErr(c, err)
		return
	}

	adminId, adminName := actorOf(c)
	t, err := loadTicket(id)
	if err != nil {
		respondErr(c, err)
		return
	}
	// 已关闭的单要先重开再回复。允许直接往终态上追加的话,用户那边会收到一条
	// 关于一张"已结束"工单的新消息,而列表里它仍然显示已关闭 —— 两边对不上。
	if t.Status == StatusClosed {
		respondErr(c, errClosed)
		return
	}

	m, err := appendMessage(t, replyInput{
		AuthorType: qymodel.ActorAdmin,
		AuthorId:   adminId,
		AuthorName: adminName,
		Body:       body,
		Internal:   req.Internal,
		IP:         c.ClientIP(),
	}, refs)
	if err != nil {
		respondErr(c, err)
		return
	}

	action := "ticket.admin.reply"
	reason := "管理员回复工单"
	if req.Internal {
		action = "ticket.admin.note"
		reason = "管理员添加内部备注"
	}
	writeTicketAudit(c, ticketAudit{
		TraceNo: t.TicketNo, Action: action, ActorType: qymodel.ActorAdmin,
		ActorId: adminId, ActorName: adminName, TargetUserId: t.UserId,
		Result: qymodel.ResultOK, Reason: reason,
		After: map[string]any{"message_id": m.Id, "images": len(refs), "internal": req.Internal},
	})
	// 内部备注不通知:用户那边什么都没发生。
	if !req.Internal {
		notify(NotifyEvent{
			Kind: NotifyReplied, TicketId: t.Id, TicketNo: t.TicketNo,
			OwnerUserId: t.UserId, Title: t.Title, Priority: t.Priority,
			ActorType: qymodel.ActorAdmin, ActorName: adminName,
		})
	}
	respondOK(c, toAdminView(t, []Message{*m}, nil))
}

// statusRequest 是管理员改状态的请求体。
type statusRequest struct {
	// Status 只接受 closed / open。中间状态(replied / user_replied)由消息驱动,
	// 手工设置它们只会让"在等谁"这件事与实际对话脱节。
	Status string `json:"status"`
	Reason string `json:"reason"`
}

// handleAdminSetStatus 关闭或重开一张工单。
func handleAdminSetStatus(c *gin.Context) {
	if !guard.RequireAPI(c, guard.FlagTicket) {
		return
	}
	id, ok := pathId(c)
	if !ok {
		respondErr(c, errInvalidParam)
		return
	}
	var req statusRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		respondErr(c, errInvalidParam)
		return
	}
	adminId, adminName := actorOf(c)
	t, err := loadTicket(id)
	if err != nil {
		respondErr(c, err)
		return
	}

	before := t.Status
	action := ""
	switch strings.TrimSpace(req.Status) {
	case StatusClosed:
		action = "ticket.admin.close"
		err = closeTicket(t, ClosedByAdmin)
	case StatusOpen:
		action = "ticket.admin.reopen"
		err = reopenTicket(t)
	default:
		respondErr(c, errIllegalTransition)
		return
	}
	if err != nil {
		writeTicketAudit(c, ticketAudit{
			TraceNo: t.TicketNo, Action: action, ActorType: qymodel.ActorAdmin,
			ActorId: adminId, ActorName: adminName, TargetUserId: t.UserId,
			Result: qymodel.ResultFail, Reason: "改状态失败: " + err.Error(),
			Before: before,
		})
		respondErr(c, err)
		return
	}

	writeTicketAudit(c, ticketAudit{
		TraceNo: t.TicketNo, Action: action, ActorType: qymodel.ActorAdmin,
		ActorId: adminId, ActorName: adminName, TargetUserId: t.UserId,
		Result: qymodel.ResultOK, Reason: truncate(strings.TrimSpace(req.Reason), 200),
		Before: before, After: t.Status,
	})
	if t.Status == StatusClosed {
		notify(NotifyEvent{
			Kind: NotifyClosed, TicketId: t.Id, TicketNo: t.TicketNo,
			OwnerUserId: t.UserId, Title: t.Title, Priority: t.Priority,
			ActorType: qymodel.ActorAdmin, ActorName: adminName,
		})
	}
	respondOK(c, toAdminView(t, nil, nil))
}

// priorityRequest 是管理员改等级的请求体。
type priorityRequest struct {
	Priority string `json:"priority"`
}

// handleAdminSetPriority 调整工单等级。
//
// 只有管理员能改:用户自己能反复调高等级的话,这一列在队列筛选里就没有任何
// 信息量了 —— 所有人都会选"紧急"。用户填的那个值仍然保留在建单审计里,
// 改动前后由本条审计回答。
func handleAdminSetPriority(c *gin.Context) {
	if !guard.RequireAPI(c, guard.FlagTicket) {
		return
	}
	id, ok := pathId(c)
	if !ok {
		respondErr(c, errInvalidParam)
		return
	}
	var req priorityRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		respondErr(c, errInvalidParam)
		return
	}
	priority := strings.TrimSpace(req.Priority)
	if !knownPriority(priority) {
		respondErr(c, errPriorityInvalid)
		return
	}
	adminId, adminName := actorOf(c)
	t, err := loadTicket(id)
	if err != nil {
		respondErr(c, err)
		return
	}

	before := t.Priority
	now := common.GetTimestamp()
	if err := db.Get().Model(&Ticket{}).Where("id = ?", t.Id).
		Updates(map[string]any{"priority": priority, "updated_at": now}).Error; err != nil {
		db.MarkFailure(err)
		writeTicketAudit(c, ticketAudit{
			TraceNo: t.TicketNo, Action: "ticket.admin.priority", ActorType: qymodel.ActorAdmin,
			ActorId: adminId, ActorName: adminName, TargetUserId: t.UserId,
			Result: qymodel.ResultFail, Reason: "改等级失败: " + err.Error(), Before: before,
		})
		respondErr(c, err)
		return
	}
	t.Priority = priority
	t.UpdatedAt = now

	writeTicketAudit(c, ticketAudit{
		TraceNo: t.TicketNo, Action: "ticket.admin.priority", ActorType: qymodel.ActorAdmin,
		ActorId: adminId, ActorName: adminName, TargetUserId: t.UserId,
		Result: qymodel.ResultOK, Reason: "调整工单等级",
		Before: before, After: priority,
	})
	respondOK(c, toAdminView(t, nil, nil))
}

// assignRequest 是指派请求体。assignee_id = 0 表示取消指派。
type assignRequest struct {
	AssigneeId int `json:"assignee_id"`
}

// handleAdminAssign 把工单指派给某个管理员。
//
// 被指派人必须**真的是管理员**,这一条要查主库而不是信前端:指派给一个普通
// 用户之后,那张单会从所有客服的"我的待办"里消失,而没有任何地方会报错 ——
// 一张被静默丢弃的工单。
func handleAdminAssign(c *gin.Context) {
	if !guard.RequireAPI(c, guard.FlagTicket) {
		return
	}
	id, ok := pathId(c)
	if !ok {
		respondErr(c, errInvalidParam)
		return
	}
	var req assignRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		respondErr(c, errInvalidParam)
		return
	}
	adminId, adminName := actorOf(c)
	t, err := loadTicket(id)
	if err != nil {
		respondErr(c, err)
		return
	}

	before := map[string]any{"assignee_id": t.AssigneeId, "assignee_name": t.AssigneeName}
	rejectAssign := func(reason string) {
		// 拒绝也要留痕:拿 assignee_id 遍历主库用户 id 去试哪些是管理员,命中与
		// 不命中分别回 200 与 400,而没有这两条审计的话,"谁在什么时候枚举过
		// 管理员名单"事后完全无法回答。DB 写失败那条分支本来就有审计,
		// 缺的从来不是能力,而是这两处 return。
		writeTicketAudit(c, ticketAudit{
			TraceNo: t.TicketNo, Action: "ticket.admin.assign", ActorType: qymodel.ActorAdmin,
			ActorId: adminId, ActorName: adminName, TargetUserId: t.UserId,
			Result: qymodel.ResultFail, Reason: reason, Before: before,
			After: map[string]any{"assignee_id": req.AssigneeId},
		})
		respondErr(c, errAssigneeInvalid)
	}

	assigneeName := ""
	switch {
	case req.AssigneeId < 0:
		// 负数原样写库的结果:这张单的 assignee_id 非 0(前端显示"已指派"、
		// 负责人名为空),而队列筛选只认 assignee_id > 0 —— 谁都搜不到它,
		// 也没人认为它还没人认领。正是本函数要防的那个后果,只是走负数这条路。
		rejectAssign("指派对象 id 非法(负数)")
		return
	case req.AssigneeId > 0:
		u, err := model.GetUserById(req.AssigneeId, false)
		if err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				rejectAssign("指派对象不存在")
				return
			}
			respondErr(c, err)
			return
		}
		if u.Role < common.RoleAdminUser {
			rejectAssign("指派对象不是管理员")
			return
		}
		assigneeName = truncate(u.Username, 64)
	}

	now := common.GetTimestamp()
	if err := db.Get().Model(&Ticket{}).Where("id = ?", t.Id).
		Updates(map[string]any{
			"assignee_id": req.AssigneeId, "assignee_name": assigneeName, "updated_at": now,
		}).Error; err != nil {
		db.MarkFailure(err)
		writeTicketAudit(c, ticketAudit{
			TraceNo: t.TicketNo, Action: "ticket.admin.assign", ActorType: qymodel.ActorAdmin,
			ActorId: adminId, ActorName: adminName, TargetUserId: t.UserId,
			Result: qymodel.ResultFail, Reason: "指派失败: " + err.Error(), Before: before,
		})
		respondErr(c, err)
		return
	}
	t.AssigneeId = req.AssigneeId
	t.AssigneeName = assigneeName
	t.UpdatedAt = now

	writeTicketAudit(c, ticketAudit{
		TraceNo: t.TicketNo, Action: "ticket.admin.assign", ActorType: qymodel.ActorAdmin,
		ActorId: adminId, ActorName: adminName, TargetUserId: t.UserId,
		Result: qymodel.ResultOK, Reason: "指派工单",
		Before: before,
		After:  map[string]any{"assignee_id": t.AssigneeId, "assignee_name": t.AssigneeName},
	})
	respondOK(c, toAdminView(t, nil, nil))
}

// handleAdminUploadImage 管理员上传图片(回复里附截图)。
func handleAdminUploadImage(c *gin.Context) {
	if !guard.RequireAPI(c, guard.FlagTicket) {
		return
	}
	adminId, adminName := actorOf(c)
	row, err := acceptImageUpload(c, adminId)
	if err != nil {
		respondErr(c, err)
		return
	}
	writeTicketAudit(c, ticketAudit{
		TraceNo: row.Ref, Action: "ticket.image.upload", ActorType: actorTypeOf(true),
		ActorId: adminId, ActorName: adminName, TargetUserId: adminId,
		Result: qymodel.ResultOK, Reason: "管理员上传工单图片",
		After: map[string]any{
			"ref": row.Ref, "mime_type": row.MimeType, "size": row.Size,
		},
	})
	respondOK(c, gin.H{
		"ref":        row.Ref,
		"mime_type":  row.MimeType,
		"size":       row.Size,
		"created_at": row.CreatedAt,
	})
}

// handleAdminGetImage 管理端下载任意一张未清理的工单图片。
//
// 不做归属判定:管理员本来就能打开任意工单的详情。多写一层"这张图属于哪张单"
// 只会给人一种"这里有权限边界"的错觉,而真正的边界在 AdminAuth 上。
func handleAdminGetImage(c *gin.Context) {
	if !guard.RequireAPI(c, guard.FlagTicket) {
		return
	}
	ref := c.Param("ref")
	if ref == "" {
		respondErr(c, errInvalidParam)
		return
	}
	a, err := loadAttachment(ref)
	if err != nil {
		respondErr(c, err)
		return
	}
	// 到期判定挪到取行之后:loadAttachment 不再自己回 410(理由见那个函数)。
	if a.PurgedAt > 0 {
		respondErr(c, errImagePurged)
		return
	}
	serveAttachment(c, a)
}
