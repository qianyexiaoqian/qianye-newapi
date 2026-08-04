package violation

import (
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/middleware"
	"github.com/QuantumNous/new-api/qianye/config"
	qymodel "github.com/QuantumNous/new-api/qianye/model"
	"github.com/QuantumNous/new-api/qianye/module"
	"github.com/QuantumNous/new-api/qianye/service/lease"
	"github.com/QuantumNous/new-api/qianye/service/twophase"
	"github.com/QuantumNous/new-api/service"

	"github.com/gin-gonic/gin"
)

// Mod 是违规检测模块的注册入口。
type Mod struct{ module.Base }

func (Mod) Name() string { return "violation" }

func (Mod) Tables() []any {
	return []any{
		&Rule{},
		&RuleVersion{},
		&Record{},
		&Payload{},
		&Counter{},
		&Ban{},
		&Appeal{},
	}
}

// InstallHooks 注册补偿回调、注入上游 service 包的两个挂载点,并预热一次规则快照。
//
// 预热是必要的:第一个请求到来时如果快照还是空的,那次请求就检查不到任何规则。
// 失败不阻塞启动 —— 扩展库连不上时热路径必须 fail-open。
func (Mod) InstallHooks() {
	// Resolver 必须在 enabled 判断之前注册:关掉违规检测不会让历史退款单消失,
	// 停在 pending 的那些单仍要被补偿任务推进,而补偿任务找不到 Resolver 时会
	// 直接把资金单标成 success,把 fee_status 永久留在 charged。
	twophase.RegisterResolver(qymodel.KindViolationFee, resolveAfterCompensation)

	if !config.Get().Violation.Enabled {
		return
	}
	service.QyPreRelayGuard = PreRelayGuard
	service.QyPostRelayGuard = PostRelayGuard

	// 迁移必须排在预热之前:预热会把规则编译进快照,而未迁移的行 mode 是空串,
	// 编译出来一律按影子跑 —— 结果是对的,但管理端列表会显示一个渲染不出来的
	// 第三种模式,运营看到的就是"我这条规则既不是影子也不是真实"。
	runRuleModeMigration()

	if err := reload(true); err != nil {
		common.SysError("qianye/violation: 规则快照预热失败(热路径将放行,稍后自动重试): " + err.Error())
	}
}

func (Mod) RegisterUserRoutes(g *gin.RouterGroup) {
	// 用户端只读。让用户知道"自己为什么被扣费"是需求原文的要求,
	// 也是避免黑箱扣费引发工单与信任危机的唯一办法。
	g.GET("/violation/my-records", middleware.SearchRateLimit(), userListRecords)
	g.GET("/violation/my-summary", userSummary)
	g.POST("/violation/appeals", middleware.CriticalRateLimit(), userCreateAppeal)
}

func (Mod) RegisterAdminRoutes(g *gin.RouterGroup) {
	g.GET("/violation/rules", adminListRules)
	// 内置防护规则包的目录。刻意放在 /rules/builtin 而不是 /rules?source=builtin:
	// 它返回的是**代码里的模板 + 站点上的导入状态**,与规则列表不是同一种东西。
	g.GET("/violation/rules/builtin", adminListBuiltinRules)
	g.GET("/violation/records", adminListRecords)
	// 导出与列表共用 recordQuery,筛选参数完全一致。挂在 records 下面而不是单独
	// 一个 /export:导出的语义就是"当前这张列表,不分页"。
	g.GET("/violation/records/export", adminExportRecords)
	g.GET("/violation/records/:id/evidence", adminGetEvidence)
	g.GET("/violation/bans", adminListBans)
	g.GET("/violation/appeals", adminListAppeals)
	g.GET("/violation/stats", adminStats)
	g.GET("/violation/counters", adminListCounters)

	// 写接口一律挂关键操作限流:它们要么直接改钱/改账号状态,
	// 要么改的是决定这两者的规则。
	crit := middleware.CriticalRateLimit()
	g.POST("/violation/rules", crit, adminCreateRule)
	g.PUT("/violation/rules/:id", crit, adminUpdateRule)
	// 列表行内的快速启停。刻意是一条**只写 enabled 一列**的独立路由,而不是
	// 让前端拿整体更新接口传一份改了 enabled 的完整规则:那份规则是列表页
	// 十几秒前拉下来的拷贝,整体写回去会把这期间别人对 pattern / mode / 作用域
	// 的改动一起静默回滚 —— 而回滚的正是决定谁被扣钱、谁被封号的那几列。
	g.PATCH("/violation/rules/:id/enabled", crit, adminSetRuleEnabled)
	g.DELETE("/violation/rules/:id", crit, adminDeleteRule)
	g.POST("/violation/rules/test", adminTestRule)
	// 一键导入内置防护规则包。导入出来一律是影子模式的普通规则行,
	// 可编辑、可删除、可单独开关。
	g.POST("/violation/rules/import-builtin", crit, adminImportBuiltinRules)
	g.POST("/violation/records/:id/revoke", crit, adminRevokeRecord)
	g.POST("/violation/bans/:userId/unban", crit, adminUnban)
	g.POST("/violation/appeals/:id/review", crit, adminReviewAppeal)
	// 解除熔断。全局影子开关(PUT /violation/mode)已删除 —— 模式绑在规则上,
	// 改模式就是改那条规则,不再有第二个入口。
	g.POST("/violation/breaker/reset", crit, adminResetBreaker)
	g.POST("/violation/counters/:userId/reset", crit, adminResetCounter)
}

func (Mod) StartTasks() {
	// 与 Resolver 注册同理,退款状态收敛必须排在 enabled 判断之前:关掉违规检测
	// 不会让历史退款单消失,而"资金单已成功、记录仍写着已扣费"的错位行只要不收敛,
	// 用户端就一直把这笔已退的钱算作违规扣费。
	lease.Run("violation.refund_reconcile", 10*time.Minute, runRefundStateReconcile)

	if !config.Get().Violation.Enabled {
		return
	}
	// 必须走 lease.Run:common.IsMasterNode 只是个环境变量,多节点都配成 master
	// 时清理任务会并发删同一批行,补偿任务会重复执行封号。
	lease.Run("violation.retention_gc", time.Hour, runRetentionGC)
	lease.Run("violation.ban_compensate", 5*time.Minute, runBanCompensate)
}

func init() { module.Register(Mod{}) }
