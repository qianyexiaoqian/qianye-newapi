package commission

import (
	"time"

	"github.com/QuantumNous/new-api/middleware"
	"github.com/QuantumNous/new-api/qianye/config"
	"github.com/QuantumNous/new-api/qianye/module"
	"github.com/QuantumNous/new-api/qianye/service/lease"

	"github.com/gin-gonic/gin"
)

// Mod 是返佣模块的注册入口。内嵌 module.Base 得到全部空实现,只覆盖用到的。
type Mod struct{ module.Base }

func (Mod) Name() string { return "commission" }

func (Mod) Tables() []any {
	return []any{
		&Accrual{},
		&Balance{},
		&Settlement{},
		&InviteRelation{},
		&FreezeRecord{},
		&GroupRate{},
	}
}

func (Mod) InstallHooks() { installHooks() }

func (Mod) RegisterUserRoutes(g *gin.RouterGroup) {
	// 用户端全部只读。列表接口挂搜索限流:分页 + 聚合查询比单点读贵得多。
	g.GET("/commission/summary", getSummary)
	g.GET("/commission/invitees", middleware.SearchRateLimit(), listInvitees)
	g.GET("/commission/records", middleware.SearchRateLimit(), listRecords)
}

func (Mod) RegisterAdminRoutes(g *gin.RouterGroup) {
	g.GET("/commission/records", adminListRecords)
	g.GET("/commission/config", adminGetConfig)
	g.GET("/commission/health", adminHealth)

	// 写接口一律挂关键操作限流:它们要么直接改钱,要么改决定钱的参数。
	crit := middleware.CriticalRateLimit()
	g.PUT("/commission/config", crit, adminPutConfig)
	g.PUT("/commission/group-rates", crit, adminPutGroupRate)
	g.DELETE("/commission/group-rates", crit, adminDeleteGroupRate)
	g.POST("/commission/clawback", crit, adminClawback)
	g.POST("/commission/settle", crit, adminSettle)
	g.POST("/commission/relations/block", crit, adminBlockRelation)
	g.POST("/commission/cache/invalidate", crit, adminInvalidateCache)
}

func (Mod) StartTasks() {
	if !config.Get().Commission.Enabled {
		return
	}
	cm := config.Get().Commission

	settleEvery := cm.SettleIntervalSecs
	if settleEvery <= 0 {
		settleEvery = 300
	}
	scanEvery := cm.TopupScanIntervalSec
	if scanEvery <= 0 {
		scanEvery = 60
	}

	// 必须走 lease.Run:common.IsMasterNode 只是个环境变量,多节点都配成
	// master 时结算与扫描会双跑,直接造成重复返佣。
	lease.Run("commission.settle", time.Duration(settleEvery)*time.Second, runSettle)
	lease.Run("commission.topup_scan", time.Duration(scanEvery)*time.Second, runTopupScan)
}

func init() { module.Register(Mod{}) }
