package qianye

import (
	"github.com/QuantumNous/new-api/middleware"
	"github.com/QuantumNous/new-api/qianye/config"
	qyctl "github.com/QuantumNous/new-api/qianye/controller"
	"github.com/QuantumNous/new-api/qianye/module"

	"github.com/gin-contrib/gzip"
	"github.com/gin-gonic/gin"
)

// RegisterRoutes 注册扩展的全部 HTTP 路由。
//
// 挂载点:main.go 中 router.SetRouter(server, ...) 调用之前。
//
// 为什么必须在 SetRouter 之前:SetWebRouter 用 engine 级的 router.Use() 注册了
// gzip、GlobalWebRateLimit、Cache 和 static.Serve。Gin 的 Use() 只影响其后注册的
// 路由,因此在 SetRouter 之后注册会被这些中间件全部套上 —— 静态文件服务会拦截
// 未匹配的路径,缓存中间件会缓存动态响应,SSE 也会被 gzip 破坏。
// 上游自己也是靠"先注册 API/Relay,最后注册 Web"来规避的。
//
// 只有这个函数能调用 engine.Group:各业务模块通过 registerXxx(user, admin) 挂子路由,
// 避免每个模块各建一个组、各挂一套中间件。
func RegisterRoutes(engine *gin.Engine) {
	if !config.Enabled() {
		return
	}

	root := engine.Group("/api/qy")
	root.Use(middleware.RouteTag("api"))
	root.Use(gzip.Gzip(gzip.DefaultCompression))
	root.Use(middleware.GlobalAPIRateLimit())

	// 引导端点:匿名可访问且永远返回 200。前端据此决定是渲染入口还是完全隐藏。
	root.GET("/config", qyctl.GetConfig)

	user := root.Group("")
	user.Use(middleware.UserAuth())
	registerUserRoutes(user)

	admin := root.Group("/admin")
	// AdminAuth 自带上游的管理操作审计,无需在每个 handler 里重复埋点。
	admin.Use(middleware.AdminAuth())
	registerAdminRoutes(admin)
}

// registerUserRoutes 挂载普通用户接口。各业务模块通过注册表自行挂载。
func registerUserRoutes(g *gin.RouterGroup) {
	for _, m := range module.All() {
		m.RegisterUserRoutes(g)
	}
}

// registerAdminRoutes 挂载管理端接口。
func registerAdminRoutes(g *gin.RouterGroup) {
	for _, m := range module.All() {
		m.RegisterAdminRoutes(g)
	}
	g.GET("/health", qyctl.AdminHealth)
	g.GET("/fund-orders", qyctl.AdminListFundOrders)
	g.POST("/fund-orders/:order_no/reprobe", qyctl.AdminReprobeFundOrder)
	// 人工裁决会直接改写资金状态,挂关键操作限流。
	g.POST("/fund-orders/:order_no/resolve", middleware.CriticalRateLimit(), qyctl.AdminResolveFundOrder)
	g.GET("/audit-logs", qyctl.AdminListAuditLogs)
	g.GET("/leases", qyctl.AdminListLeases)
	g.POST("/config/reload", middleware.CriticalRateLimit(), qyctl.AdminReloadConfig)
}
