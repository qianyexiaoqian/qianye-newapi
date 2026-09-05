package mall

import (
	"time"

	"github.com/QuantumNous/new-api/middleware"
	"github.com/QuantumNous/new-api/qianye/config"
	qyctl "github.com/QuantumNous/new-api/qianye/controller"
	"github.com/QuantumNous/new-api/qianye/module"
	"github.com/QuantumNous/new-api/qianye/modules/paypass"
	"github.com/QuantumNous/new-api/qianye/service/lease"

	"github.com/gin-gonic/gin"
)

// Mod 把星屑商城接进扩展的模块注册表。
//
// 本模块对原项目后端的改动为 0 行:表全在扩展库;主库只在套餐订单的 twophase
// 主库事务里被写(user_subscriptions / subscription_orders / qy_fund_outbox),
// 走的全是上游已导出的函数。
type Mod struct{ module.Base }

// Name 必须与目录名一致(qianye/modules_test.go)。
func (Mod) Name() string { return "mall" }

func (Mod) Tables() []any { return Tables() }

// InstallHooks 注册 twophase 的收尾回调,并接管引导端点的商城入口开关。
//
// 收尾回调缺了的后果在本仓的 violation 上真实发生过:补偿任务把资金单推成 success,
// 业务侧永远停在中间态。入口开关必须接管:运营在管理端(qy_settings scope=mall)关掉
// 入口之后,引导端点默认只读 YAML —— 不接管就会出现"运营关掉了入口,前台照旧显示"。
func (Mod) InstallHooks() {
	InstallResolvers()
	qyctl.QyMallEntryShown = entryShown
}

// RegisterPublicRoutes 挂载**匿名可访问**的封面端点。
//
// `<img src>` 由浏览器发出、不带 Authorization 头;只回已经绑到某件商品上的封面,
// 理由见 cover.go 文件头。
func (Mod) RegisterPublicRoutes(g *gin.RouterGroup) {
	g.GET("/mall/covers/:ref", handleGetCover)
}

// RegisterUserRoutes 挂载普通用户接口。传入的组已挂 UserAuth。
//
// 刻意只挂 UserAuth 不挂 TokenAuth:API Key 天然是给机器用的、可批量分发,
// 允许它调用等于允许脚本化下单。
func (Mod) RegisterUserRoutes(g *gin.RouterGroup) {
	g.GET("/mall/products", handleListProducts)
	g.GET("/mall/products/:no", handleGetProduct)
	// 唯一会动钱的入口。幂等键只防得住"同一次点击的重试",防不住脚本用不同
	// client_request_id 连续发单,所以两把限流桶都要挂:CriticalRateLimit 按 IP,
	// UserCriticalRateLimit 按账号 —— 一个被盗的会话换出口 IP 就换了前一个桶。
	// 支付密码(code / physical 强制验密)在 handler 里,限流是节流不是授权。
	g.POST("/mall/orders",
		middleware.CriticalRateLimit(),
		middleware.UserCriticalRateLimit("mall_order"),
		handleCreateOrder)
	g.GET("/mall/orders", handleListMyOrders)
	g.GET("/mall/orders/:no", handleGetMyOrder)
	// 码揭示挂验密中间件(请求头 X-Qy-Pay-Password,D-12):兑换码可以是任意第三方卡密,
	// 一次揭示即离开平台。**逐条**拉取,刻意不做批量列表:一次越权 bug 就是全量泄漏。
	g.GET("/mall/orders/:no/code", paypass.Middleware(), handleRevealCode)
	// 取消是退款动作,挂关键操作限流。
	g.POST("/mall/orders/:no/cancel", middleware.CriticalRateLimit(), handleCancelOrder)
	// 抽奖所得的实物单在中奖那一刻没有地址,中奖者事后补填(只允许一次)。
	g.POST("/mall/orders/:no/address", middleware.CriticalRateLimit(), handleSetOrderAddress)
}

// RegisterAdminRoutes 挂载管理端接口。传入的组已挂 AdminAuth(自带上游操作审计)。
func (Mod) RegisterAdminRoutes(g *gin.RouterGroup) {
	g.GET("/mall/products", handleAdminListProducts)
	g.GET("/mall/orders", handleAdminListOrders)
	// 地址明文是 PII 的唯一出口:登记 sensitiveReads(请求台账)+ 业务审计。
	g.GET("/mall/orders/:no/address", handleAdminRevealAddress)

	// 全部写接口都挂关键操作限流:它们要么决定平台会发出去多少东西,要么会退星屑。
	crit := middleware.CriticalRateLimit()
	g.POST("/mall/products", crit, handleAdminCreateProduct)
	g.PUT("/mall/products/:no", crit, handleAdminUpdateProduct)
	g.DELETE("/mall/products/:no", crit, handleAdminDeleteProduct)
	// 兑换码批量上传:整个 body 由凭证构成,登记 credentialBodyRoutes。
	g.POST("/mall/products/:no/codes", crit, handleAdminUploadCodes)
	// 封面。上传要落磁盘,是本模块唯一一条能消耗宿主机存储的入口。
	g.POST("/mall/covers", crit, handleAdminUploadCover)
	g.DELETE("/mall/covers/:ref", crit, handleAdminDiscardCover)
	g.POST("/mall/orders/:no/ship", crit, handleAdminShipOrder)
	g.POST("/mall/orders/:no/fail", crit, handleAdminFailOrder)
	g.POST("/mall/orders/:no/revoke-code", crit, handleAdminRevokeCode)
	// 人工裁决是本模块唯一提到超级管理员的动作:它推翻的是资金单已经给过的 failed
	// 结论,applied 一支把一笔"星屑已扣、订阅可能没发"的单在账上宣布为已发放。
	// 闸门排在 crit **之前**:限流桶按客户端 IP + 路由计,被拒的越权尝试不该消耗它
	// (否则一个 role=10 连点就能把这条路由对同一来源 IP 上的所有人锁死 20 分钟)。
	g.POST("/mall/orders/:no/adjudicate",
		middleware.RootActionGate(middleware.RootActionMallAdjudicate),
		crit, handleAdminAdjudicate)
}

// StartTasks 启动后台任务。全部走 lease.Run 而非裸 goroutine,多节点不双跑。
//
// 总闸在这里判一次:mall.enabled=false 时一个任务都不起(sections.go 登记的效果:
// "已下单的实物 / 套餐订单不再推进")。
func (Mod) StartTasks() {
	cfg := config.Get().Mall
	if !cfg.Enabled {
		return
	}
	// 对账按 pending_grace_seconds 的节奏跑:它收拾的是过了宽限期仍停在 paid / held
	// 的套餐订单,跑得比宽限期还快只会空扫。
	grace := cfg.PendingGraceSeconds
	if grace <= 0 {
		grace = 60
	}
	lease.Run("mall.reconcile", time.Duration(grace)*time.Second, runReconcile)
	// 地址保留期与封面回收的粒度都是"天 / 小时"级,一小时一轮足够。
	lease.Run("mall.prune", time.Hour, runPrune)
	lease.Run("mall.cover_prune", time.Hour, pruneCovers)
}

func init() { module.Register(Mod{}) }
