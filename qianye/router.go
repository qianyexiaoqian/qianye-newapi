package qianye

import (
	"github.com/QuantumNous/new-api/middleware"
	"github.com/QuantumNous/new-api/qianye/config"
	qyctl "github.com/QuantumNous/new-api/qianye/controller"
	"github.com/QuantumNous/new-api/qianye/module"
	"github.com/QuantumNous/new-api/qianye/service/audit"

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
	// 写请求台账。必须挂在 UserAuth/AdminAuth **之前**:c.Next() 之后一样读得到
	// 认证写进 context 的身份,却额外记下被认证挡掉的 401/403 —— 那正是越权探测
	// 的形状,挂在认证之后会把这类请求整个漏掉。
	root.Use(audit.Middleware())

	// 引导端点:匿名可访问且永远返回 200。前端据此决定是渲染入口还是完全隐藏。
	root.GET("/config", qyctl.GetConfig)

	// 匿名只读面。目前只有抽奖的公正性证据链在用:验证者必须先能**拿到**
	// proof 才谈得上自己复算,要求登录等于把"历史公正查询"锁在注册墙后面。
	// 挂在 UserAuth 之前,与 /config 同一档(限流 + 请求台账都已生效)。
	for _, m := range module.All() {
		if pr, ok := m.(module.PublicRouter); ok {
			pr.RegisterPublicRoutes(root)
		}
	}

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
	// 登录会话分档计数。不属于任何业务模块 —— 它读的是上游主库的 user_sessions,
	// 存在的唯一理由是上游列表接口结构性地不下发已到期会话(理由见 UserSessionStats)。
	// 为一个只读计数新建一个模块,只会让 modules_test.go 的目录/注册表一致性
	// 多守一个空壳。与 /health、/version 同一档:直接挂在这里。
	g.GET("/session-stats", qyctl.UserSessionStats)
	// 受限账号公告。与 /session-stats 同一档:「受限账号」是会话鉴权链上的一档
	// 身份(middleware/restricted_user.go),不属于任何业务模块 —— 管理员在上游
	// 用户管理页上禁用任何一个账号都会产生这个状态,与 violation / ticket 开没开
	// 无关。挂进某个模块的话,那个模块被关掉时这段解释就连配都配不了。
	//
	// **它必须同时出现在 middleware/restricted_user.go 的白名单里**:漏了的话
	// 这条路径对受限账号 403,而受限落地页上那块公告位会一直空着 ——
	// 恰好是它要解决的那个问题的加强版。
	g.GET("/restricted-notice", qyctl.UserRestrictedNotice)
	// API 密钥页的「今日消耗」列。与 /session-stats 同一档:它读的是上游主库的
	// logs,不属于任何业务模块 —— 每一个有令牌的账号都会用到它,与返佣、
	// 划转、订阅开没开无关。
	//
	// 挂在扩展这一侧而不是上游 /api/token/* 的理由是**日界**:「今日」必须与
	// 日消费明细同一个口径,而那个口径(commission.day_offset_minutes)住在
	// 扩展配置里,上游 controller 拿不到也不该拿。
	g.GET("/token-usage/today", qyctl.UserTokenTodayUsage)
}

// registerAdminRoutes 挂载管理端接口。
func registerAdminRoutes(g *gin.RouterGroup) {
	for _, m := range module.All() {
		m.RegisterAdminRoutes(g)
	}
	g.GET("/health", qyctl.AdminHealth)
	// 版本端点刻意与 /health 分开:/health 走 requireCore,扩展库不可用时 503,
	// 而那正是最需要先确认"跑的是哪个版本"的时刻。理由详见 AdminVersion 的注释。
	g.GET("/version", qyctl.AdminVersion)
	// 二开的检查更新。与 /version 分成两条,因为它们是**两件性质不同的事**:
	// /version 读的是编译进二进制的常量,必然成功、零副作用、role=10 可读;
	// 这一条会让服务端向 github.com 开一次出站连接,那是站点行为。
	//
	// 因此这一条:提到超管(RootActionUpdateCheck)、挂关键操作限流。
	// 闸门排在限流之前:被拒的越权尝试不该消耗限流桶,否则一个 role=10
	// 反复点就能把超管自己挡在门外(这条口径现由
	// qianye/root_action_guard_test.go 的 TestRootGateRunsBeforeCriticalRateLimit
	// 对全部 15 条这类路由统一钉住)。
	//
	// 这道限流挡的是**单个客户端的瞬时突发**,不是"把 GitHub 额度点光" ——
	// 它的桶键是 mark + 客户端 IP + 路由,而那份额度按**服务端出站 IP** 计,
	// 两者不是同一个量:单 IP 的稳态预算恰好 20 次/1200 秒 = 60 次/小时
	// (100% 吃满、零余量),不同来源的管理员各占一桶、线性叠加。
	// 真正对得上那份额度的闸在 handler 里(updateCheckBudget,进程级 30 次/小时)。
	//
	// 只查二开,不查上游内核:上游那颗按钮是上游自己的代码(浏览器直连
	// Calcium-Ion/new-api),本仓不碰。
	g.GET("/version/check-update",
		middleware.RootActionGate(middleware.RootActionUpdateCheck),
		middleware.CriticalRateLimit(),
		qyctl.AdminCheckUpdate)
	g.GET("/fund-orders", qyctl.AdminListFundOrders)
	g.POST("/fund-orders/:order_no/reprobe", qyctl.AdminReprobeFundOrder)
	// 人工裁决会直接改写资金状态,挂关键操作限流。
	g.POST("/fund-orders/:order_no/resolve", middleware.CriticalRateLimit(), qyctl.AdminResolveFundOrder)
	// 分组倍率失配的诊断出口。与 /health 同一档:它读的是上游主库的 users/tokens
	// 与内存里的分组倍率表,不属于任何业务模块,存在的唯一理由是上游
	// GetGroupRatio 找不到分组时会静默按 1.0 倍扣费(理由见 AdminGroupRatioOrphans)。
	g.GET("/group-ratio/orphans", qyctl.AdminGroupRatioOrphans)
	g.GET("/restricted-notice", qyctl.AdminGetRestrictedNotice)
	// 这段文案会渲染在**每一个**受限账号的首屏上,写它等于改一段对外承诺
	// (申诉渠道、联系方式、封禁政策)。挂关键操作限流,并且成功与失败都写审计。
	g.PUT("/restricted-notice", middleware.CriticalRateLimit(), qyctl.AdminPutRestrictedNotice)
	// 受限账号总览:当前有多少个受限账号 + 受限账号还能到达哪几档接口。
	// 只读,不碰扩展库(计数查主库 users,分档表是进程内常量),因此不走 requireCore。
	// 没有对应的写端点是刻意的:白名单是代码里的显式清单,做成可配等于把
	// 「新增接口默认对受限账号开放」这个失败模式请回来。
	g.GET("/restricted-accounts", qyctl.AdminRestrictedAccountsOverview)
	// 客户端 IP 识别诊断。与 /restricted-accounts 同一档:只读进程内的取值策略
	// 与本次请求自身,既不碰扩展库也不碰主库,因此不走 requireCore ——
	// 而「客户端 IP 被识别成了什么」恰恰是扩展库挂了时也必须答得出的问题
	// (令牌 allow_ips 与限流不依赖扩展库,它们照样在按这个值放行/拒绝)。
	//
	// 只读、没有写端点:受信网段是环境变量,改它必须重启进程。做成可热改的
	// 接口等于给「运行中的进程被远程放宽信任面」开一扇门,而这个信任面正是
	// 令牌 IP 白名单的地基。
	g.GET("/client-ip", qyctl.AdminClientIP)
	// 负余额(透支)账号总览。与 /restricted-accounts 同一档:只读主库 users,
	// 不碰扩展库,因此不走 requireCore。
	//
	// 它存在的理由不是"又一个报表",而是本站**刻意接受预扣/结算无下限**这条取舍
	// 的配套条件(拍板与代价见 qianye/docs/decisions.md 的 D-01):接受透支的前提
	// 是透支可被看见、可被处置。没有写端点是刻意的 —— 追欠费/封号/清零都是上游
	// 用户管理页已有的动作,这里只负责让人知道要去处置谁。
	g.GET("/overdraft", qyctl.AdminOverdraftOverview)
	g.GET("/audit-logs", qyctl.AdminListAuditLogs)
	g.GET("/request-audits", qyctl.AdminListRequestAudits)
	g.GET("/leases", qyctl.AdminListLeases)
	g.POST("/config/reload", middleware.CriticalRateLimit(), qyctl.AdminReloadConfig)
}
