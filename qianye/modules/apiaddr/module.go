package apiaddr

import (
	"github.com/QuantumNous/new-api/qianye/module"

	"github.com/gin-gonic/gin"
)

// Mod 把 API 地址簿接进扩展的模块注册表。
//
// 对原项目后端的改动为 0 行:没有 hook、没有后台任务,只有自己的表与路由。
type Mod struct{ module.Base }

func (Mod) Name() string { return "apiaddr" }

func (Mod) Tables() []any { return []any{&Address{}} }

// RegisterUserRoutes 挂载普通用户接口。传入的组已挂 UserAuth。
//
// 只有一个只读端点。刻意要求登录:地址簿里可能有"仅内网可达""内测线路"这类
// 只该给已注册用户看的入口,而这个接口的唯一消费方(密钥列表)本来就在登录态里。
func (Mod) RegisterUserRoutes(g *gin.RouterGroup) {
	g.GET("/api-addresses", handleUserList)
}

// RegisterAdminRoutes 挂载管理端接口。传入的组已挂 AdminAuth(自带操作审计)。
//
// 重排走 POST /api-addresses/reorder 而不是 PUT /api-addresses/order:
// 同一 HTTP 方法下的静态段与 `:id` 通配段在 gin 的路由树里是相邻分支,
// 换个方法就彻底不存在这个歧义,也省得后来者去记"这个版本的 gin 支不支持"。
//
// 四个写接口都**不挂** CriticalRateLimit:它们是管理员在一张编辑页上的连续
// 操作(拖三下顺序、连改两条备注),关键操作限流会把正常编辑挡成 429,
// 而这里既不动钱也不发信,没有被刷的价值 —— AdminAuth 已经是足够的闸门。
func (Mod) RegisterAdminRoutes(g *gin.RouterGroup) {
	g.GET("/api-addresses", adminList)
	g.POST("/api-addresses", adminCreate)
	g.POST("/api-addresses/reorder", adminReorder)
	g.PUT("/api-addresses/:id", adminUpdate)
	g.DELETE("/api-addresses/:id", adminDelete)
}

func init() { module.Register(Mod{}) }
