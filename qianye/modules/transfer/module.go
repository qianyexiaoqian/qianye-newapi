package transfer

import (
	"time"

	"github.com/QuantumNous/new-api/middleware"
	qymodel "github.com/QuantumNous/new-api/qianye/model"
	"github.com/QuantumNous/new-api/qianye/module"
	"github.com/QuantumNous/new-api/qianye/service/lease"
	"github.com/QuantumNous/new-api/qianye/service/twophase"

	"github.com/gin-gonic/gin"
)

// Mod 把划转接进扩展的模块注册表。
// 本模块对原项目后端的改动为 0 行:它既不需要 hook,也不需要新的路由文件。
type Mod struct{ module.Base }

func (Mod) Name() string { return "transfer" }

func (Mod) Tables() []any { return []any{&Order{}, &UserState{}, &LookupLog{}} }

// InstallHooks 注册补偿回调。
// 这里没有上游 hook 要注入,只有 twophase 需要知道"确认主库生效之后找谁收尾"。
func (Mod) InstallHooks() {
	twophase.RegisterResolver(qymodel.KindTransfer, resolveAfterCompensation)
}

// RegisterUserRoutes 挂载普通用户接口。传入的组已挂 UserAuth。
func (Mod) RegisterUserRoutes(g *gin.RouterGroup) {
	// 唯一会动钱的入口,挂关键操作限流:幂等键只能防住"同一次点击的重试",
	// 防不住脚本用不同 request_id 连续发起。
	g.POST("/transfer", middleware.CriticalRateLimit(), handleCreate)
	// 解析接口天然可被用来枚举,SearchRateLimit 按用户 ID 限流,比 IP 限流抗代理轮换。
	g.POST("/transfer/preview", middleware.SearchRateLimit(), handlePreview)
	g.GET("/transfer/records", handleListRecords)
	g.GET("/transfer/limits", handleGetLimits)
}

// RegisterAdminRoutes 挂载管理端接口。传入的组已挂 AdminAuth(自带操作审计)。
func (Mod) RegisterAdminRoutes(g *gin.RouterGroup) {
	g.GET("/transfer/records", handleAdminListRecords)
}

// StartTasks 启动对账任务。
//
// 周期取两阶段补偿间隔的两倍:本任务只负责收拾补偿任务推进完终态之后的尾巴,
// 跑得比它还快没有意义,只会空扫。
func (Mod) StartTasks() {
	interval := 2 * twophase.Interval()
	if interval < time.Minute {
		interval = time.Minute
	}
	lease.Run("transfer.reconcile", interval, reconcile)
}

func init() { module.Register(Mod{}) }
