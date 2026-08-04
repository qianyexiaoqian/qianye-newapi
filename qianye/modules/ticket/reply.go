package ticket

import (
	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/qianye/config"
	"github.com/QuantumNous/new-api/qianye/db"
	qymodel "github.com/QuantumNous/new-api/qianye/model"

	"gorm.io/gorm"
)

// replyInput 是一次追加回复的归一化输入。
type replyInput struct {
	AuthorType string
	AuthorId   int
	AuthorName string
	Body       string
	Refs       []string
	// Internal 只对管理员有效,由调用方保证(用户端 handler 不读这个字段)。
	Internal bool
	IP       string
}

// nextStatus 回答"这条消息发出去之后,这张单在等谁"。
//
// 只有**跨方向**才改状态:同一方连续追加不改变"在等谁",因此返回空串,
// 调用方跳过状态更新。把它写成一条 open→open 的自环边只会让"状态没变"和
// "状态被非法地改成了自己"长得一样(见 status.go 的说明)。
//
// 内部备注永远不改状态:它不是给用户看的,用户那边什么都没发生 ——
// 把 open 改成 replied 会让这张单从"还没有人回应"的队列里消失,
// 而实际上用户一个字都没收到。这是本函数最重要的一条。
func nextStatus(current string, in replyInput) string {
	if in.Internal {
		return ""
	}
	if in.AuthorType == qymodel.ActorUser {
		if current == StatusReplied {
			return StatusUserReplied
		}
		return ""
	}
	// 管理员的对外回复:open / user_replied 都变成"在等用户"。
	if current == StatusOpen || current == StatusUserReplied {
		return StatusReplied
	}
	return ""
}

// appendMessage 往工单里追加一条消息,并按需推进状态。
//
// 整个动作在一个事务里:消息插入、条数计数、状态跃迁、图片绑定四者要么都成立
// 要么都不成立。拆开做的话,中途失败会留下"消息在、状态没跟上"的单 ——
// 客服队列因此漏掉一张用户正在等的工单,而没有任何地方会报错。
//
// 状态跃迁用带条件的 UPDATE(`WHERE id=? AND status=?`)判定并发,绝不"先读
// 后写":两个管理员同时回复同一张单时,先读后写会让其中一次的状态更新静默丢失。
func appendMessage(t *Ticket, in replyInput, refs []string) (*Message, error) {
	cfg := config.Get().Ticket
	now := common.GetTimestamp()
	m := &Message{
		TicketId:   t.Id,
		TicketNo:   t.TicketNo,
		AuthorType: in.AuthorType,
		AuthorId:   in.AuthorId,
		AuthorName: truncate(in.AuthorName, 64),
		Body:       in.Body,
		Internal:   in.Internal,
		ClientIp:   truncate(in.IP, 64),
		CreatedAt:  now,
	}
	to := nextStatus(t.Status, in)

	err := db.Get().Transaction(func(tx *gorm.DB) error {
		// 条数上限在事务里重查,不信任外面读到的 MessageCount:两条并发回复
		// 会双双读到同一个旧计数一起通过。上限本身不是安全边界,但它守的是
		// "详情接口还打得开"这条底线,越界一次就再也回不去。
		var cnt int64
		if err := tx.Model(&Message{}).Where("ticket_id = ?", t.Id).Count(&cnt).Error; err != nil {
			return err
		}
		if cnt >= int64(cfg.MaxMessagesPerTicket) {
			return errMessageLimit
		}
		if err := tx.Create(m).Error; err != nil {
			return err
		}

		updates := map[string]any{
			"updated_at":    now,
			"message_count": gorm.Expr("message_count + 1"),
		}
		// 内部备注不动 last_reply_*:那两列回答的是"这张单最后一次**对用户可见的**
		// 动作是什么时候",自动关闭与客服队列排序都按它算。让一条用户看不见的
		// 备注去刷新它,等于把一张真的没人管的单伪装成刚刚处理过。
		if !in.Internal {
			updates["last_reply_at"] = now
			updates["last_reply_by"] = in.AuthorType
			if in.AuthorType == qymodel.ActorAdmin && t.FirstRepliedAt == 0 {
				// 首响时刻只写一次:它是"首次人工响应有多快"的原始数据,
				// 被后续回复覆盖之后这个指标就永远算不出来了。
				updates["first_replied_at"] = now
			}
		}
		if to != "" {
			if !canTransit(t.Status, to) {
				return errIllegalTransition
			}
			updates["status"] = to
		}

		res := tx.Model(&Ticket{}).Where("id = ? AND status = ?", t.Id, t.Status).Updates(updates)
		if res.Error != nil {
			return res.Error
		}
		if res.RowsAffected == 0 {
			// 状态在我们读它与写它之间被别人改了。可能是对方刚回复、
			// 也可能是这张单刚被关掉 —— 让调用方刷新重试,不要猜。
			return errStatusConflict
		}
		return bindAttachments(tx, t, m, refs)
	})
	if err != nil {
		db.MarkFailure(err)
		return nil, err
	}

	t.MessageCount++
	t.UpdatedAt = now
	if !in.Internal {
		t.LastReplyAt = now
		t.LastReplyBy = in.AuthorType
		if in.AuthorType == qymodel.ActorAdmin && t.FirstRepliedAt == 0 {
			t.FirstRepliedAt = now
		}
	}
	if to != "" {
		t.Status = to
	}
	return m, nil
}

// closeTicket 关闭一张工单。by ∈ user|admin|system。
//
// 关闭不写消息:它不是一句话,而是一次状态决定。谁在什么时候关的由
// closed_at / closed_by 回答,为什么关的由审计回答(system 那条由扫描任务
// 自己解释)。硬要塞一条"工单已关闭"的系统消息,只会让每张单的正文里都多
// 一条谁都不会读的噪声,还要给它编一个 author_name。
func closeTicket(t *Ticket, by string) error {
	if t.Status == StatusClosed {
		// 幂等:已经关了就不是错误。两个标签页各点一次"关闭"是常态,
		// 第二次报"状态冲突"只会让用户以为出了问题。
		return nil
	}
	if !canTransit(t.Status, StatusClosed) {
		return errIllegalTransition
	}
	now := common.GetTimestamp()
	res := db.Get().Model(&Ticket{}).
		Where("id = ? AND status = ?", t.Id, t.Status).
		Updates(map[string]any{
			"status": StatusClosed, "closed_at": now, "closed_by": by, "updated_at": now,
		})
	if res.Error != nil {
		db.MarkFailure(res.Error)
		return res.Error
	}
	if res.RowsAffected == 0 {
		return errStatusConflict
	}
	t.Status = StatusClosed
	t.ClosedAt = now
	t.ClosedBy = by
	t.UpdatedAt = now
	return nil
}

// reopenTicket 重开一张已关闭的工单。**只有管理员能调**(路由已保证)。
//
// 用户侧刻意不给这条出边:给了的话,"未关闭工单数上限"这道闸就能被
// "关掉再开回来"绕过,而那是本模块最重要的一道闸。用户要继续问就新开一张单,
// 那才是客服队列想要的粒度。
//
// 回到 open 而不是 replied:重开的语义是"这件事还没完,重新排进待处理队列",
// 而 replied 表示"在等用户回话" —— 管理员刚刚决定它没解决,不该由用户先开口。
func reopenTicket(t *Ticket) error {
	if !canTransit(t.Status, StatusOpen) {
		return errIllegalTransition
	}
	now := common.GetTimestamp()
	res := db.Get().Model(&Ticket{}).
		Where("id = ? AND status = ?", t.Id, StatusClosed).
		Updates(map[string]any{
			"status": StatusOpen, "closed_at": 0, "closed_by": "", "updated_at": now,
		})
	if res.Error != nil {
		db.MarkFailure(res.Error)
		return res.Error
	}
	if res.RowsAffected == 0 {
		return errStatusConflict
	}
	t.Status = StatusOpen
	t.ClosedAt = 0
	t.ClosedBy = ""
	t.UpdatedAt = now
	return nil
}

// checkReplyCooldown 判断用户两次追加回复之间的间隔。
//
// 只对**用户**生效:管理员批量处理队列时本来就该连着回,用同一道闸卡他们
// 等于给客服效率设上限。而这道闸要拦的是"用一张工单当聊天室刷屏"。
//
// 按"这张单里该用户的上一条消息"算而不是全局最后一条:两张不同工单各回一句
// 是正常操作,合起来算会让人在第二张单里莫名被拒。
func checkReplyCooldown(ticketId int64, userId int, now int64) error {
	secs := config.Get().Ticket.ReplyCooldownSecs
	if secs <= 0 {
		return nil
	}
	var last Message
	err := db.Get().Select("created_at").
		Where("ticket_id = ? AND author_type = ? AND author_id = ?", ticketId, qymodel.ActorUser, userId).
		Order("id desc").Limit(1).Take(&last).Error
	if err == gorm.ErrRecordNotFound {
		return nil
	}
	if err != nil {
		db.MarkFailure(err)
		return err
	}
	if now-last.CreatedAt < int64(secs) {
		return errReplyCooldown
	}
	return nil
}
