package ticket

import (
	"crypto/rand"
	"encoding/hex"
	"strconv"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/qianye/config"
	"github.com/QuantumNous/new-api/qianye/db"
	qymodel "github.com/QuantumNous/new-api/qianye/model"
	"github.com/QuantumNous/new-api/qianye/service/audit"

	"github.com/gin-gonic/gin"
	"gorm.io/gorm"
)

// newTicketNo 生成对外单号。
//
// 用 crypto/rand 而不是 common.GetRandomString(math/rand):单号可预测意味着
// 可以被枚举。工单里有用户的联系方式、报错日志、账号截图,越权判定一旦哪天
// 被写错,可预测的单号就是直接的批量拖库入口 —— 与提现单号同一条理由。
//
// 前缀带日期只是为了人念得出来("TK2601..."),不承载任何判定。
func newTicketNo() string {
	buf := make([]byte, 8)
	if _, err := rand.Read(buf); err != nil {
		// crypto/rand 失败在实践中只发生在系统熵源坏掉时。不静默回落到
		// math/rand —— 那会让"单号不可预测"这条性质在最需要它的时候失效,
		// 且没有任何迹象。让调用方看到空串并失败,比悄悄降级安全。
		common.SysError("qianye/ticket: 生成工单号失败: " + err.Error())
		return ""
	}
	return "TK" + strconv.FormatInt(common.GetTimestamp(), 10) + hex.EncodeToString(buf)
}

// create 新建一张工单。
//
// 全部副作用都在一个扩展库事务里:工单头、首条消息、图片绑定三者要么都成立
// 要么都不成立。拆开做的话,中途失败会留下一张没有正文的空工单 ——
// 客服打开它只能看到一个标题,而用户以为自己已经把问题说清楚了。
//
// 四道防滥用闸门(冷却 / 日限 / 未关闭数 / 图片数)都在这个事务里判,
// 而不是事务外:并发提交会双双读到旧计数一起通过,闸门形同虚设。
func create(c *gin.Context, userId int, username string, req createRequest) (*ticketView, error) {
	acc, err := acceptCreate(req)
	if err != nil {
		return nil, err
	}

	no := newTicketNo()
	if no == "" {
		// crypto/rand 取不到熵。绝不回落到可预测的单号 —— 那会让"单号不可枚举"
		// 这条性质在最需要它的时候静默失效(理由见 newTicketNo)。
		return nil, errInternal
	}
	now := common.GetTimestamp()
	t := &Ticket{
		TicketNo:     no,
		UserId:       userId,
		Username:     truncate(username, 64),
		Title:        acc.Title,
		Priority:     acc.Priority,
		Status:       StatusOpen,
		MessageCount: 1,
		LastReplyAt:  now,
		LastReplyBy:  qymodel.ActorUser,
		ClientIp:     truncate(c.ClientIP(), 64),
		CreatedAt:    now,
		UpdatedAt:    now,
	}
	m := &Message{
		TicketNo:   no,
		AuthorType: qymodel.ActorUser,
		AuthorId:   userId,
		AuthorName: truncate(username, 64),
		Body:       acc.Body,
		ClientIp:   t.ClientIp,
		CreatedAt:  now,
	}

	err = db.Get().Transaction(func(tx *gorm.DB) error {
		if err := checkCreateLimits(tx, userId, now); err != nil {
			return err
		}
		if err := tx.Create(t).Error; err != nil {
			return err
		}
		m.TicketId = t.Id
		if err := tx.Create(m).Error; err != nil {
			return err
		}
		return bindAttachments(tx, t, m, acc.Refs)
	})
	if err != nil {
		db.MarkFailure(err)
		// 被闸门挡下的建单尝试必须留痕:审计里只有成功的那些,
		// "这个账号一分钟内被冷却拦了 40 次"事后就完全查不到,
		// 而那恰恰是最需要查的形状。单号已经生成,失败那条与
		// 将来可能成功的那条共用同一个 trace_no,时间线能串起来。
		writeTicketAudit(c, ticketAudit{
			TraceNo: no, Action: "ticket.create", ActorType: qymodel.ActorUser,
			ActorId: userId, ActorName: username, TargetUserId: userId,
			Result: qymodel.ResultFail,
			Reason: audit.Truncate("工单提交被拒: "+err.Error(), 500),
		})
		return nil, err
	}

	writeTicketAudit(c, ticketAudit{
		TraceNo: no, Action: "ticket.create", ActorType: qymodel.ActorUser,
		ActorId: userId, ActorName: username, TargetUserId: userId,
		Result: qymodel.ResultOK, Reason: "提交工单",
		After: map[string]any{"title": t.Title, "priority": t.Priority, "images": len(acc.Refs)},
	})
	notify(NotifyEvent{
		Kind: NotifyCreated, TicketId: t.Id, TicketNo: t.TicketNo,
		OwnerUserId: t.UserId, Title: t.Title, Priority: t.Priority,
		ActorType: qymodel.ActorUser, ActorName: username,
	})

	return toUserView(t, []Message{*m}, nil), nil
}

// checkCreateLimits 是建单的四道闸,全部在建单事务内执行。
//
// 顺序是刻意的:先判最便宜、最可能命中的(冷却只看一行的时间戳),
// 再判要 count 的两条。反过来的话,一个循环提交的脚本会让每一次被拒
// 都先付两次全表条件计数的代价。
//
// 每一条的 0 语义是"不限制",而不是"上限为 0"—— 这是配置注释里承诺的,
// 在这里必须逐条兑现:写成 `if cnt >= cfg.X` 而漏掉 `cfg.X > 0`,
// 会让填 0 的站点一张单都建不出来。
func checkCreateLimits(tx *gorm.DB, userId int, now int64) error {
	cfg := config.Get().Ticket

	if cfg.CooldownSecs > 0 {
		var last Ticket
		err := tx.Select("created_at").Where("user_id = ?", userId).
			Order("id desc").Limit(1).Take(&last).Error
		if err != nil && err != gorm.ErrRecordNotFound {
			return err
		}
		if err == nil && now-last.CreatedAt < int64(cfg.CooldownSecs) {
			return errCooldown
		}
	}

	if cfg.MaxOpenPerUser > 0 {
		var open int64
		if err := tx.Model(&Ticket{}).
			Where("user_id = ? AND status IN ?", userId, openStatuses).
			Count(&open).Error; err != nil {
			return err
		}
		if open >= int64(cfg.MaxOpenPerUser) {
			return errOpenLimit
		}
	}

	if cfg.DailyMaxCount > 0 {
		// 按"最近 24 小时"而不是自然日:自然日要在 SQL 里做时区换算,
		// 三种数据库的函数各不相同,而滑动窗口只是一次整数比较。
		// 代价是跨零点时窗口不重置 —— 对一道防滥用闸门来说这是更严的方向。
		var today int64
		if err := tx.Model(&Ticket{}).
			Where("user_id = ? AND created_at >= ?", userId, now-86400).
			Count(&today).Error; err != nil {
			return err
		}
		if today >= int64(cfg.DailyMaxCount) {
			return errDailyLimit
		}
	}
	return nil
}
