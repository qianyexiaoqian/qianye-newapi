package ticket

import (
	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/qianye/config"
	"github.com/QuantumNous/new-api/qianye/db"
	"github.com/QuantumNous/new-api/qianye/guard"
	"github.com/QuantumNous/new-api/qianye/httpq"
	qymodel "github.com/QuantumNous/new-api/qianye/model"

	"github.com/gin-gonic/gin"
)

// handleGetConfig 下发工单的限额与开关。
//
// 五项上限必须下发:后端拦得住不代表用户知道为什么被拦。不下发的话,用户只会
// 在写完一整篇正文之后收到一句"已达上限",而"上限是多少、我还剩几张"全靠猜。
func handleGetConfig(c *gin.Context) {
	if !guard.RequireAPI(c, guard.FlagTicket) {
		return
	}
	cfg := config.Get().Ticket
	userId := c.GetInt("id")

	var open int64
	if err := db.Get().Model(&Ticket{}).
		Where("user_id = ? AND status IN ?", userId, openStatuses).
		Count(&open).Error; err != nil {
		db.MarkFailure(err)
		respondErr(c, err)
		return
	}

	respondOK(c, gin.H{
		"priorities":              Priorities(),
		"title_max_runes":         cfg.TitleMaxRunes,
		"body_max_runes":          cfg.BodyMaxRunes,
		"max_open_per_user":       cfg.MaxOpenPerUser,
		"daily_max_count":         cfg.DailyMaxCount,
		"cooldown_seconds":        cfg.CooldownSecs,
		"reply_cooldown_seconds":  cfg.ReplyCooldownSecs,
		"max_messages_per_ticket": cfg.MaxMessagesPerTicket,
		"auto_close_days":         cfg.AutoCloseDays,
		"open_count":              open,
		// 图片三项决定前端渲不渲染上传控件、accept 填什么、以及在【选文件的
		// 那一刻】就告诉用户"这张 9 MB 的图不行",而不是等一次完整上传之后
		// 才收到 413。
		"image_enabled":         cfg.ImageOn(),
		"image_max_bytes":       cfg.ImageMaxBytes,
		"image_max_per_message": cfg.ImageMaxPerMessage,
		"image_accept":          ImageAcceptMimes(),
	})
}

// handleCreate 新建工单。已挂 CriticalRateLimit。
func handleCreate(c *gin.Context) {
	if !guard.RequireAPI(c, guard.FlagTicket) {
		return
	}
	var req createRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		respondErr(c, errInvalidParam)
		return
	}
	userId, username := actorOf(c)
	view, err := create(c, userId, username, req)
	if err != nil {
		respondErr(c, err)
		return
	}
	respondOK(c, view)
}

// handleList 返回当前用户的工单列表。
func handleList(c *gin.Context) {
	if !guard.RequireAPI(c, guard.FlagTicket) {
		return
	}
	page, size := httpq.Paginate(c, listPaging)
	q := db.Get().Model(&Ticket{}).Where("user_id = ?", c.GetInt("id"))
	q = applyStatusFilter(q, c.Query("status"))

	var total int64
	if err := q.Count(&total).Error; err != nil {
		db.MarkFailure(err)
		respondErr(c, err)
		return
	}
	rows := make([]Ticket, 0, size)
	// 按 updated_at 倒序而不是 id:用户关心的是"哪张单刚有动静",
	// 而不是"哪张单建得晚"。id 做次序键打破并列,免得同秒的两行顺序抖动。
	if err := q.Order("updated_at desc, id desc").
		Offset(httpq.Offset(page, size)).Limit(size).Find(&rows).Error; err != nil {
		db.MarkFailure(err)
		respondErr(c, err)
		return
	}

	items := make([]*ticketView, 0, len(rows))
	for i := range rows {
		items = append(items, toUserView(&rows[i], nil, nil))
	}
	respondOK(c, gin.H{"items": items, "total": total, "p": page, "page_size": size})
}

// handleGet 返回工单详情与全部对用户可见的消息。
func handleGet(c *gin.Context) {
	if !guard.RequireAPI(c, guard.FlagTicket) {
		return
	}
	no, ok := pathTicketNo(c)
	if !ok {
		respondErr(c, errInvalidParam)
		return
	}
	t, err := loadUserTicketByNo(c.GetInt("id"), no)
	if err != nil {
		respondErr(c, err)
		return
	}
	messages, err := loadUserMessages(t.Id)
	if err != nil {
		respondErr(c, err)
		return
	}
	atts, err := loadAttachmentsOfTicket(t.Id)
	if err != nil {
		respondErr(c, err)
		return
	}
	respondOK(c, toUserView(t, messages, atts))
}

// handleReply 追加一条回复。已挂 CriticalRateLimit。
//
// **刻意不读请求体里的 internal**:内部备注是管理端概念,用户端连读都不读它,
// 而不是读了之后判 `if !isAdmin { internal = false }` —— 后者多一处可能被
// 重构掉的判断,而漏掉它的后果是用户能往客服的内部备注里写东西。
func handleReply(c *gin.Context) {
	if !guard.RequireAPI(c, guard.FlagTicket) {
		return
	}
	no, ok := pathTicketNo(c)
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

	userId, username := actorOf(c)
	t, err := loadUserTicketByNo(userId, no)
	if err != nil {
		respondErr(c, err)
		return
	}
	// 关闭是终态:允许单方面在终态上继续追加,等于让关单按钮什么都没关掉。
	if t.Status == StatusClosed {
		respondErr(c, errClosed)
		return
	}
	// 回复冷却【不在这里判】:它是一道"先查再插"的闸门,只有和消息插入待在
	// 同一个事务、同一把工单行锁下才成立。这里再补一次判定不会更安全,
	// 只会制造"看起来判过了"的错觉(见 appendMessage)。
	m, err := appendMessage(t, replyInput{
		AuthorType: qymodel.ActorUser,
		AuthorId:   userId,
		AuthorName: username,
		Body:       body,
		IP:         common.ClientIP(c),
	}, refs)
	if err != nil {
		respondErr(c, err)
		return
	}

	writeTicketAudit(c, ticketAudit{
		TraceNo: t.TicketNo, Action: "ticket.reply", ActorType: qymodel.ActorUser,
		ActorId: userId, ActorName: username, TargetUserId: t.UserId,
		Result: qymodel.ResultOK, Reason: "用户追加工单回复",
		After: map[string]any{"message_id": m.Id, "images": len(refs)},
	})
	notify(NotifyEvent{
		Kind: NotifyReplied, TicketId: t.Id, TicketNo: t.TicketNo,
		OwnerUserId: t.UserId, Title: t.Title, Priority: t.Priority,
		ActorType: qymodel.ActorUser, ActorName: username,
	})
	respondOK(c, toUserView(t, []Message{*m}, nil))
}

// handleClose 由用户关闭自己的工单。
func handleClose(c *gin.Context) {
	if !guard.RequireAPI(c, guard.FlagTicket) {
		return
	}
	no, ok := pathTicketNo(c)
	if !ok {
		respondErr(c, errInvalidParam)
		return
	}
	userId, username := actorOf(c)
	t, err := loadUserTicketByNo(userId, no)
	if err != nil {
		respondErr(c, err)
		return
	}
	if err := closeTicket(t, ClosedByUser); err != nil {
		respondErr(c, err)
		return
	}
	writeTicketAudit(c, ticketAudit{
		TraceNo: t.TicketNo, Action: "ticket.close", ActorType: qymodel.ActorUser,
		ActorId: userId, ActorName: username, TargetUserId: t.UserId,
		Result: qymodel.ResultOK, Reason: "用户关闭工单",
	})
	notify(NotifyEvent{
		Kind: NotifyClosed, TicketId: t.Id, TicketNo: t.TicketNo,
		OwnerUserId: t.UserId, Title: t.Title, Priority: t.Priority,
		ActorType: qymodel.ActorUser, ActorName: username,
	})
	respondOK(c, toUserView(t, nil, nil))
}

// handleUploadImage 接收一张图片,返回可用于建单/回复的 ref。已挂 CriticalRateLimit。
func handleUploadImage(c *gin.Context) {
	if !guard.RequireAPI(c, guard.FlagTicket) {
		return
	}
	userId, username := actorOf(c)
	row, err := acceptImageUpload(c, userId)
	if err != nil {
		respondErr(c, err)
		return
	}
	// 只在成功后写审计:失败的上传(格式不对、超限)没有产生任何存储副作用,
	// 它的调用事实由请求台账(qy_request_audits)覆盖。
	writeTicketAudit(c, ticketAudit{
		TraceNo: row.Ref, Action: "ticket.image.upload", ActorType: actorTypeOf(false),
		ActorId: userId, ActorName: username, TargetUserId: userId,
		Result: qymodel.ResultOK, Reason: "上传工单图片",
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

// handleGetImage 回给用户一张他有权看的图片。
//
// 授权口径在 userCanSeeAttachment 里(是我传的 / 挂在我的工单上的一条对我可见的
// 消息里)。不成立一律回"图片不存在"而不是"无权访问":后者会把"这个 ref 确实
// 存在"泄漏给一个正在枚举的人。
//
// **顺序是先鉴权再判到期**。反过来的话,一个已按保留期清理的 ref 会在任何归属
// 判定之前就回 410,而不存在的 ref 回 400 —— 两者可区分,于是任意登录用户都能
// 拿别人工单里的旧 ref 确认"它确实存在过"。
func handleGetImage(c *gin.Context) {
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
	ok, err := userCanSeeAttachment(c.GetInt("id"), a)
	if err != nil {
		respondErr(c, err)
		return
	}
	if !ok {
		respondErr(c, errImageNotFound)
		return
	}
	if a.PurgedAt > 0 {
		respondErr(c, errImagePurged)
		return
	}
	serveAttachment(c, a)
}

// handleDiscardImage 丢弃一张自己上传、尚未随任何消息提交的图片。
//
// 用户端与管理端共用。存在的理由是 pendingUploadMax 那道闸没有出口:前端"移除
// 一张图"只删本地那一项,服务端那条未绑定的行会一直占着名额,两次"选了又不发"
// 之后用户在 24 小时孤儿窗口到期前再也传不了图,而提示语让他"先完成当前这条
// 消息"—— 一个他根本无法执行的动作。
func handleDiscardImage(c *gin.Context) {
	if !guard.RequireAPI(c, guard.FlagTicket) {
		return
	}
	ref := c.Param("ref")
	if ref == "" {
		respondErr(c, errInvalidParam)
		return
	}
	userId, username := actorOf(c)
	if err := discardPendingUpload(userId, ref); err != nil {
		respondErr(c, err)
		return
	}
	// 与上传那条成对:一张图从磁盘上出现与消失都要能对上,否则"这个账号传了
	// 多少、现在还剩多少"在事后只剩一半答案。
	writeTicketAudit(c, ticketAudit{
		TraceNo: ref, Action: "ticket.image.discard", ActorType: actorTypeOf(false),
		ActorId: userId, ActorName: username, TargetUserId: userId,
		Result: qymodel.ResultOK, Reason: "丢弃未提交的工单图片",
		Before: map[string]any{"ref": ref},
	})
	respondOK(c, gin.H{"ref": ref})
}
