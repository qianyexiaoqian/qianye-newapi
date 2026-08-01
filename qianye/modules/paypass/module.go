package paypass

import (
	"github.com/QuantumNous/new-api/middleware"
	"github.com/QuantumNous/new-api/qianye/module"

	"github.com/gin-gonic/gin"
)

// Mod 把支付密码接进扩展的模块注册表。
//
// 对原项目后端的改动为 0 行:没有 hook、没有后台任务,只有自己的表与路由。
type Mod struct{ module.Base }

func (Mod) Name() string { return "paypass" }

func (Mod) Tables() []any { return []any{&PayPassword{}} }

// RegisterUserRoutes 挂载普通用户接口。传入的组已挂 UserAuth。
//
// # 为什么写接口全部挂 CriticalRateLimit
//
// 这些端点各自都能被拿来做密码试探或验证码轰炸:
//   - POST   /pay-password              首次设置(并发抢占的入口)
//   - PUT    /pay-password              改密,内部会走一次完整的验密
//   - POST   /pay-password/recover/code 发验证码,不限流就是一个免费的发信机
//   - POST   /pay-password/recover/reset 拿验证码换密码,不限流就是在线爆破验证码
//
// 只有 GET /pay-password(状态查询)不挂:它是纯读、且只读自己的状态,
// 前端每次打开划转页都要调,挂关键操作限流会把正常使用挡掉。
func (Mod) RegisterUserRoutes(g *gin.RouterGroup) {
	g.GET("/pay-password", handleGetStatus)
	g.POST("/pay-password", middleware.CriticalRateLimit(), handleSet)
	g.PUT("/pay-password", middleware.CriticalRateLimit(), handleChange)
	g.POST("/pay-password/recover/code", middleware.CriticalRateLimit(), handleRecoverCode)
	g.POST("/pay-password/recover/reset", middleware.CriticalRateLimit(), handleRecoverReset)
}

// RegisterAdminRoutes 挂载管理端接口。传入的组已挂 AdminAuth(自带操作审计)。
//
// 两个写接口都挂关键操作限流,并且都在 adminMutate 里写 qianye/service/audit。
func (Mod) RegisterAdminRoutes(g *gin.RouterGroup) {
	g.GET("/pay-password/:user_id", handleAdminGetStatus)
	g.POST("/pay-password/:user_id/unlock", middleware.CriticalRateLimit(), handleAdminUnlock)
	g.POST("/pay-password/:user_id/reset", middleware.CriticalRateLimit(), handleAdminReset)
}

func init() { module.Register(Mod{}) }
