package stardust

import (
	"fmt"
	"strings"

	"github.com/QuantumNous/new-api/middleware"
	"github.com/QuantumNous/new-api/qianye/guard"
	qymodel "github.com/QuantumNous/new-api/qianye/model"
	"github.com/QuantumNous/new-api/qianye/service/audit"

	"github.com/gin-gonic/gin"
)

// api_admin_settle.go —— 管理端的结算状态与重跑(契约 §3)。
//
// 两条路由都在 /api/qy/admin 前缀下,受限账号守卫自动覆盖;重跑挂 CriticalRateLimit。

func init() {
	adminRouteInstallers = append(adminRouteInstallers, func(g *gin.RouterGroup) {
		g.GET("/stardust/settle/status", handleAdminSettleStatus)
		// 重跑会真的发星屑(对 computed / held 桶),是写接口:关键操作限流 + 审计。
		g.POST("/stardust/settle/rerun", middleware.CriticalRateLimit(), handleAdminSettleRerun)
	})
}

// handleAdminSettleStatus 是 GET /stardust/settle/status:
// {last_run|null, next_settle_at, target_day, ready}。
func handleAdminSettleStatus(c *gin.Context) {
	if !guard.RequireAPI(c, guard.FlagStardust) {
		return
	}
	respondOK(c, settleStatus(c.Request.Context()))
}

// handleAdminSettleRerun 是 POST /stardust/settle/rerun {day}:同步重跑桶日 D,
// 只对 computed / held 桶生效。成功与失败各写一条审计 —— 它决定"那一天欠着的
// 星屑还发不发",而且只在出过故障的那一天才会被按下,事后复盘要能看见是谁按的。
//
// 请求体解析失败不单独报错:day 留空会在 rerunDay 的日期校验里被拒,并走失败审计。
func handleAdminSettleRerun(c *gin.Context) {
	if !guard.RequireAPI(c, guard.FlagStardust) {
		return
	}
	var req struct {
		Day string `json:"day"`
	}
	_ = c.ShouldBindJSON(&req)
	day := strings.TrimSpace(req.Day)

	stats, err := rerunDay(c.Request.Context(), day)
	if err != nil {
		audit.Write(c, audit.Entry{
			Category:    qymodel.AuditCategoryStardust,
			Action:      "stardust.settle.rerun",
			ActorType:   qymodel.ActorAdmin,
			ActorUserId: c.GetInt("id"),
			ActorName:   c.GetString("username"),
			Result:      qymodel.ResultFail,
			Reason:      audit.Truncate("重跑 "+day+" 失败: "+err.Error(), 255),
			AfterSnap:   rerunSnapshot(day, stats),
		})
		respondErr(c, err)
		return
	}
	audit.Write(c, audit.Entry{
		Category:    qymodel.AuditCategoryStardust,
		Action:      "stardust.settle.rerun",
		ActorType:   qymodel.ActorAdmin,
		ActorUserId: c.GetInt("id"),
		ActorName:   c.GetString("username"),
		AmountQuota: stats.Granted,
		Result:      qymodel.ResultOK,
		Reason:      audit.Truncate("重跑 "+day+" 的消费返结算", 255),
		AfterSnap:   rerunSnapshot(day, stats),
	})
	respondOK(c, gin.H{
		"day":        day,
		"recomputed": stats.Recomputed,
		"settled":    stats.Settled,
		"held":       stats.Held,
		"skipped":    stats.Skipped,
		"failed":     stats.Failed,
		"granted":    stats.Granted,
	})
}

// rerunSnapshot 是审计 after 快照:一行人话,复盘时不用再去翻运行记录。
func rerunSnapshot(day string, s RerunStats) string {
	return fmt.Sprintf("day=%s recomputed=%d settled=%d held=%d skipped=%d failed=%d granted=%d",
		day, s.Recomputed, s.Settled, s.Held, s.Skipped, s.Failed, s.Granted)
}
