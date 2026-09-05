package stardust

import (
	"strings"

	"github.com/QuantumNous/new-api/qianye/db"
	"github.com/QuantumNous/new-api/qianye/guard"
	"github.com/QuantumNous/new-api/qianye/httpq"
	"github.com/QuantumNous/new-api/qianye/modules/invite"

	"github.com/gin-gonic/gin"
)

// api_admin_invite.go —— 管理端看下线消费返的日结明细(qy_sd_invite_accrual)。
//
// 挂在 /api/qy/admin/invite/ 下与关系管理同一组(契约);数据住本包所以处理器也住本包。
// 只读;两侧用户名从主库按本页 id 批量取一次(与 api_admin_ledger.go 同一手法)。

func init() {
	adminRouteInstallers = append(adminRouteInstallers, func(g *gin.RouterGroup) {
		g.GET("/invite/invite-accruals", handleAdminListInviteAccruals)
	})
}

// adminInviteAccrualView 是一行日结明细 + 两侧用户名。
type adminInviteAccrualView struct {
	InviteAccrual
	InviterUsername string `json:"inviter_username"`
	InviteeUsername string `json:"invitee_username"`
}

// handleAdminListInviteAccruals 分页返回下线消费返日桶,可按 day / inviter_id / invitee_id / status 筛。
func handleAdminListInviteAccruals(c *gin.Context) {
	if !guard.RequireAPI(c, guard.FlagInvite) {
		return
	}
	ctx := c.Request.Context()
	page, size := httpq.Paginate(c, invitePaging)
	gdb := db.Get()
	if gdb == nil {
		respondErr(c, db.ErrNotReady)
		return
	}

	q := gdb.WithContext(ctx).Model(&InviteAccrual{})
	if day := strings.TrimSpace(c.Query("day")); day != "" {
		// 桶日的合法性由生成它的同一个日界包裁定,不在这里另写一份"八位数字"的形状判断。
		if _, ok := invite.DayKeyStart(day); !ok {
			respondErr(c, errBadRequest("day 必须是 YYYYMMDD"))
			return
		}
		q = q.Where("bucket_date = ?", day)
	}
	if uid := httpq.Int(c, "inviter_id", 0); uid > 0 {
		q = q.Where("inviter_id = ?", uid)
	}
	if uid := httpq.Int(c, "invitee_id", 0); uid > 0 {
		q = q.Where("invitee_id = ?", uid)
	}
	if status := strings.TrimSpace(c.Query("status")); status != "" {
		if status != AccrualComputed && status != AccrualSettled && status != AccrualHeld {
			respondErr(c, errBadRequest("未知的日桶状态: "+status))
			return
		}
		q = q.Where("status = ?", status)
	}
	var total int64
	if err := q.Count(&total).Error; err != nil {
		db.MarkFailure(err)
		respondErr(c, wrapInternal("统计下线消费返日桶", err))
		return
	}
	rows := make([]InviteAccrual, 0, size)
	if err := q.Order("bucket_date desc, inviter_id asc, id asc").
		Offset(httpq.Offset(page, size)).Limit(size).Find(&rows).Error; err != nil {
		db.MarkFailure(err)
		respondErr(c, wrapInternal("查询下线消费返日桶", err))
		return
	}
	ids := make([]int, 0, len(rows)*2)
	for _, r := range rows {
		ids = append(ids, r.InviterId, r.InviteeId)
	}
	names, err := usernamesOf(ctx, ids)
	if err != nil {
		respondErr(c, err)
		return
	}
	items := make([]adminInviteAccrualView, 0, len(rows))
	for _, r := range rows {
		items = append(items, adminInviteAccrualView{
			InviteAccrual: r, InviterUsername: names[r.InviterId], InviteeUsername: names[r.InviteeId],
		})
	}
	respondOK(c, gin.H{"items": items, "total": total, "p": page, "page_size": size})
}
