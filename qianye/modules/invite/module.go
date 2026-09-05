package invite

import (
	"github.com/QuantumNous/new-api/middleware"
	"github.com/QuantumNous/new-api/qianye/module"

	"github.com/gin-gonic/gin"
)

// Mod 是邀请关系模块的注册入口。内嵌 module.Base 得到全部空实现,只覆盖用到的。
type Mod struct{ module.Base }

func (Mod) Name() string { return "invite" }

func (Mod) Tables() []any {
	return []any{&InviteRelation{}, &CacheInvalidation{}}
}

func (Mod) InstallHooks() { installHooks() }

// 用户端接口(/api/qy/invite/summary 等)不在这里:它们读的全是星屑账本
// (流水、日结明细、分组档),由 stardust 模块挂在同一个 /invite 前缀下。
// 本包只管关系本身。

func (Mod) RegisterAdminRoutes(g *gin.RouterGroup) {
	g.GET("/invite/health", adminHealth)
	// 下线日消费明细。三条都是只读,但都扫主库 logs 的一段区间,比这里其它
	// GET 贵一个量级,所以挂搜索限流。
	g.GET("/invite/daily-consume", middleware.SearchRateLimit(), adminListDailyConsume)
	g.GET("/invite/daily-consume/export", middleware.SearchRateLimit(), adminExportDailyConsume)
	// 主表某一行的按天下钻。单人 + 至多 31 天,由 idx_qy_logs_token_daily 收窄,
	// 但仍然扫主库,所以与上面两条同一档限流。
	g.GET("/invite/daily-consume/by-day", middleware.SearchRateLimit(), adminUserDailyConsume)

	// 写接口一律挂关键操作限流:它们改的是"未来的邀请返归谁"。
	crit := middleware.CriticalRateLimit()
	g.POST("/invite/relations/block", crit, adminBlockRelation)
	g.POST("/invite/cache/invalidate", crit, adminInvalidateCache)
	// 关系列表与手工绑定/换绑/解绑住在 api_admin_relation.go 自己的注册函数里,
	// 免得这里的清单与那边的处理器分两处维护、加了处理器忘了挂。
	registerRelationRoutes(g, crit)
}

func (Mod) StartTasks() {
	// 跨节点失效通道与 logs 覆盖索引是本包与星屑**共用**的设施,谁开着都得起
	// (见 export.go StartSharedInfra)。本包自己没有别的后台任务。
	if SharedInfraWanted() {
		StartSharedInfra()
	}
}

func init() { module.Register(Mod{}) }
