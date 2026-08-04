package ticket

import (
	"context"
	"fmt"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/qianye/config"
	"github.com/QuantumNous/new-api/qianye/db"

	"gorm.io/gorm"
)

// 自动关闭走的是 closed_by = system,没有对应的 gin.Context,因此**不写审计**:
// 审计表回答的是"谁做了这件事",而这里没有"谁"。痕迹留在 closed_by 那一列上,
// 加上 SysLog 里的一行汇总。下一个人打开 audit_coverage_guard_test.go 会发现
// 本文件不在登记表里 —— 那是刻意的,不是漏了。

// maintenanceBatch 是每一轮扫描的处理条数上限。
//
// 固化在代码里:它只影响"一轮干多少活",而任务每小时跑一次、积压会在下一轮
// 继续消化。把它做成配置项等于多一个没人会调的旋钮。
const maintenanceBatch = 200

// maintain 是本模块唯一的后台任务:自动关闭 + 图片保留期清理。
//
// 合成一个而不是两个 lease.Run:它们的周期相同(都是"每小时看一眼"级别),
// 而两个租约意味着两条独立的接管路径、两处要读 ctx.Err()、健康面板上两行。
// 真正需要分开的理由(一个是分钟级、另一个是天级)在这里并不存在。
func maintain(ctx context.Context) {
	handle := db.Get()
	if handle == nil {
		return
	}
	// 句柄必须接上租约给的 ctx:租约到期或进程收到关停信号时,正在跑的那条
	// UPDATE 要跟着被取消,否则一个没有上界的清理批次会把关停拖到超时。
	gdb := handle.WithContext(ctx)
	autoCloseStale(ctx, gdb)
	pruneAttachments(ctx, gdb)
}

// autoCloseStale 关闭"管理员已回复、用户超过 auto_close_days 没再回应"的工单。
//
// 三条约束,每一条都对应一个具体的坏结局:
//
//  1. **只扫 replied**。open 与 user_replied 在等的是客服,自动关掉等于把
//     没人处理的工单悄悄抹掉 —— 那是这个功能最容易变成的样子。
//  2. **按 last_reply_at 计时**,不是 updated_at:后者会被改等级、改指派这类
//     内部动作刷新,于是一张真的没人回的单可以靠客服自己调一次等级永远不过期。
//  3. **判定写进 UPDATE 的 WHERE**,不是取回来再判:多节点的租约交接窗口里
//     可能重叠执行一轮,条件写在 UPDATE 上才不会把 closed_at 刷成第二个时间戳。
func autoCloseStale(ctx context.Context, gdb *gorm.DB) {
	if ctx.Err() != nil {
		return
	}
	days := config.Get().Ticket.AutoCloseDays
	if days <= 0 {
		return
	}
	now := common.GetTimestamp()
	deadline := now - int64(days)*86400

	// 先取一批 id 再按 id 更新,而不是直接 UPDATE ... LIMIT:
	// MySQL 的 UPDATE 支持 LIMIT,PostgreSQL 不支持,而这段代码三种库通吃。
	ids := make([]int64, 0, maintenanceBatch)
	err := gdb.Model(&Ticket{}).
		Where("status = ? AND last_reply_at > 0 AND last_reply_at < ?", StatusReplied, deadline).
		Order("id asc").Limit(maintenanceBatch).Pluck("id", &ids).Error
	if err != nil {
		db.MarkFailure(err)
		common.SysError("qianye/ticket: 扫描待自动关闭工单失败: " + err.Error())
		return
	}
	if len(ids) == 0 {
		return
	}

	res := gdb.Model(&Ticket{}).
		Where("id IN ? AND status = ? AND last_reply_at < ?", ids, StatusReplied, deadline).
		Updates(map[string]any{
			"status":     StatusClosed,
			"closed_at":  now,
			"closed_by":  ClosedBySystem,
			"updated_at": now,
		})
	if res.Error != nil {
		db.MarkFailure(res.Error)
		common.SysError("qianye/ticket: 自动关闭工单失败: " + res.Error.Error())
		return
	}
	if res.RowsAffected > 0 {
		common.SysLog(fmt.Sprintf(
			"qianye/ticket: 已自动关闭 %d 张超过 %d 天无用户回应的工单", res.RowsAffected, days))
	}
}

// pruneAttachments 清理不该再留在磁盘上的图片。
//
// 两条到期口径:
//
//	A. 孤儿      —— 传了但从没随任何消息提交,超过 attachmentOrphanSeconds
//	B. 保留期到  —— 所属工单已关闭,且关闭时间超过 image_retention_days
//
// A 不受 image_retention_days 控制:那个键回答的是"工单关闭后图片留多久",
// 而孤儿文件从来不是任何一条消息的一部分。把它们挂在同一个开关上,
// 运维填 0 关掉保留期的同时会打开一个磁盘泄漏。
//
// B 从**关闭时刻**起算而不是上传时刻:一张挂了半年还在争议中的工单,它的截图
// 正是争议的物证,按上传时间清掉等于在还需要它的时候销毁证据。
//
// 到期判定一律写进【扫描条件】而不是取回来再过滤 —— 一批永远不可清的行
// (比如挂了半年的未关闭工单的图片)会把每一轮 batch 占满,后面所有到期行
// 从此再也轮不到。
func pruneAttachments(ctx context.Context, gdb *gorm.DB) {
	if ctx.Err() != nil {
		return
	}
	now := common.GetTimestamp()

	purgeBatch(ctx, gdb, gdb.Model(&Attachment{}).
		Where("purged_at = 0 AND ticket_id = 0 AND created_at > 0 AND created_at < ?",
			now-attachmentOrphanSeconds), "孤儿图片")

	days := config.Get().Ticket.ImageRetentionDays
	if days <= 0 {
		return
	}
	// 子查询而不是 JOIN:GORM 的 Pluck 走 JOIN 时列名歧义要靠手写别名,
	// 而子查询在三种数据库上的行为完全一致。
	closed := gdb.Model(&Ticket{}).Select("id").
		Where("status = ? AND closed_at > 0 AND closed_at < ?", StatusClosed, now-int64(days)*86400)
	purgeBatch(ctx, gdb, gdb.Model(&Attachment{}).
		Where("purged_at = 0 AND ticket_id > 0 AND ticket_id IN (?)", closed), "到期图片")
}

// purgeBatch 执行一轮"取一批 → 删文件 → 标记已清"。
//
// 先删文件再标记,不是反过来:标记完再删,中途崩溃会留下一个 purged_at 已置位
// 却仍躺在磁盘上的文件 —— 它从此不会再被任何一轮扫到,清理任务在日志里一切
// 正常,而图片永远留在盘上。删文件失败的那一行则原样留着,下一轮再试。
func purgeBatch(ctx context.Context, gdb *gorm.DB, scope *gorm.DB, what string) {
	if ctx.Err() != nil {
		return
	}
	rows := make([]Attachment, 0, maintenanceBatch)
	if err := scope.Order("id asc").Limit(maintenanceBatch).Find(&rows).Error; err != nil {
		db.MarkFailure(err)
		common.SysError("qianye/ticket: 扫描" + what + "失败: " + err.Error())
		return
	}
	if len(rows) == 0 {
		return
	}

	done := make([]int64, 0, len(rows))
	for i := range rows {
		if ctx.Err() != nil {
			return
		}
		if err := attachmentStore.Remove(rows[i].StoredName); err != nil {
			common.SysError("qianye/ticket: 删除" + what + "文件失败(下一轮重试): " + err.Error())
			continue
		}
		done = append(done, rows[i].Id)
	}
	if len(done) == 0 {
		return
	}

	// 再带一次 purged_at = 0:两个节点的租约交接窗口里可能重叠执行一轮,
	// 条件写在 UPDATE 上才不会把 purged_at 刷成第二个时间戳。
	res := gdb.Model(&Attachment{}).Where("id IN ? AND purged_at = 0", done).
		Updates(map[string]any{"purged_at": common.GetTimestamp()})
	if res.Error != nil {
		db.MarkFailure(res.Error)
		common.SysError("qianye/ticket: 标记" + what + "已清理失败: " + res.Error.Error())
		return
	}
	if res.RowsAffected > 0 {
		common.SysLog(fmt.Sprintf("qianye/ticket: 已清除 %d 张%s", res.RowsAffected, what))
	}
}
