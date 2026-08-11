package violation

import (
	"context"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/middleware"
	"github.com/QuantumNous/new-api/qianye/config"
	"github.com/QuantumNous/new-api/qianye/db"
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
		&BanPolicy{},
		&Category{},
		&CategoryCounter{},
		&AIChannel{},
		&AISetting{},
		&AIReview{},
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

	// 违规类型的种子与规则绑定迁移同样排在预热之前:预热会把类型装进快照,
	// 而没有种子时 catFallback 是零值 —— 类型计数整体跳过。结果是对的
	// (账号总量线照常工作),但管理端会先看到一张空的类型表,
	// 而用户端公示页会是空的,让人以为"这个功能没做"。
	//
	// 迁移不改变任何用户的封号判定:「未分类」出厂阈值为 0,见 migrateRuleCategory。
	runCategoryMigration()

	// 兜底策略档必须在预热之前补建:第一条违规可能在启动后的几毫秒内就到,
	// 而那时如果一行策略都没有,resolveBanPolicy 会落到 YAML 兜底 ——
	// 结果是对的(YAML 就是种子),但管理端会先看到一张空表,
	// 让人以为"策略还没配、所以现在不封号"。
	if gdb := db.Get(); gdb != nil {
		if err := ensureDefaultBanPolicy(context.Background(), gdb); err != nil {
			common.SysError("qianye/violation: 兜底封号策略补建失败(将回落到 YAML 阈值): " + err.Error())
		}
		if err := reloadBanPolicies(context.Background(), gdb); err != nil {
			common.SysError("qianye/violation: 封号策略预热失败(将回落到 YAML 阈值): " + err.Error())
		}
		// AI 审核设置行同理:出厂是**关闭 + 抽样率 0**,补建不改变任何行为,
		// 但没有它管理端会看到一张空表单,分不清"还没配"与"默认值是多少"。
		if err := ensureAISetting(context.Background(), gdb); err != nil {
			common.SysError("qianye/violation: AI 审核设置行补建失败(AI 审核将保持关闭): " + err.Error())
		}
	}

	if err := reload(true); err != nil {
		common.SysError("qianye/violation: 规则快照预热失败(热路径将放行,稍后自动重试): " + err.Error())
	}
}

func (Mod) RegisterUserRoutes(g *gin.RouterGroup) {
	// 用户端只读。让用户知道"自己为什么被扣费"是需求原文的要求,
	// 也是避免黑箱扣费引发工单与信任危机的唯一办法。
	g.GET("/violation/my-records", middleware.SearchRateLimit(), userListRecords)
	g.GET("/violation/my-summary", userSummary)
	// 违规类型公示:有哪些类型、各自多少次会被处置、以及自己当前每一类的计数。
	// 只返回 published=true 的类型,且只返回对外文案 —— 内部说明写的就是匹配细节,
	// 公示它等于把绕过方法印给用户(白名单见 userCategoryView)。
	g.GET("/violation/my-categories", userListCategories)
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
	g.GET("/violation/ban-policies", adminListBanPolicies)
	// 违规类型:列表带每一类的规则条数(归档确认弹窗要显示"这 N 条规则会改绑到 X")。
	g.GET("/violation/categories", adminListCategories)
	// 类型阈值的影响面预览。只读但要扫计数表,挂搜索限流而不是完全裸奔。
	g.GET("/violation/categories/impact", middleware.SearchRateLimit(), adminCategoryImpact)
	// 内置类型的建议阈值 + 逐类影响面。只读,同样要扫计数表。
	g.GET("/violation/categories/suggestions", middleware.SearchRateLimit(), adminSuggestedThresholds)
	// 影响面预览是只读的,但它要跨库扫描,所以挂搜索限流而不是完全裸奔。
	g.GET("/violation/ban-policies/impact", middleware.SearchRateLimit(), adminBanPolicyImpact)

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
	// 列表多选之后的批量操作。刻意是两条**动作独立**的路由,而不是一条带 op 参数的
	// 万能批量接口:批量启停与批量改作用分组的危险面完全不同(前者要过"选中里有没有
	// 真实模式规则"这道确认闸,后者要过"覆盖还是追加、哪一种名单"这道),
	// 揉进一个入口就必须在同一份校验里表达两套互不相干的前置条件。
	//
	// mode(影子 / 真实)**不在**批量里,理由见 adminBatchSetRuleEnabled 的注释。
	g.POST("/violation/rules/batch/enabled", crit, adminBatchSetRuleEnabled)
	g.POST("/violation/rules/batch/group-scope", crit, adminBatchSetRuleGroupScope)
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

	// ── AI 审核 ──
	// 读接口不挂限流(它们是列表与统计);写接口一律挂关键操作限流:
	// 渠道地址与密钥决定用户内容被发到哪里,抽样率与总开关决定花多少钱。
	g.GET("/violation/ai-review/channels", adminListAIChannels)
	g.GET("/violation/ai-review/settings", adminGetAISetting)
	g.GET("/violation/ai-review/logs", adminListAIReviews)
	g.GET("/violation/ai-review/stats", adminAIReviewStats)
	g.POST("/violation/ai-review/channels", crit, adminCreateAIChannel)
	g.PUT("/violation/ai-review/channels/:id", crit, adminUpdateAIChannel)
	g.DELETE("/violation/ai-review/channels/:id", crit, adminDeleteAIChannel)
	// 连通性试跑会发一次真实的出站调用,所以同样挂关键操作限流 ——
	// 没有它的话,这个按钮就是一个可以被反复点的、代替我们花钱的出站放大器。
	g.POST("/violation/ai-review/channels/:id/test", crit, adminTestAIChannel)
	g.PUT("/violation/ai-review/settings", crit, adminPutAISetting)
	// 兜底档与普通档是两条路由,不是一个带 is_default 参数的接口。
	// 路径决定身份:没有任何请求体能把普通档变成兜底档、或把兜底档降级 ——
	// 而"兜底档被降级"与"兜底档被删除"是同一件事(见 model.go 的三道锁)。
	g.PUT("/violation/ban-policies/default", crit, func(c *gin.Context) { adminUpsertBanPolicy(c, true) })
	g.PUT("/violation/ban-policies", crit, func(c *gin.Context) { adminUpsertBanPolicy(c, false) })
	g.DELETE("/violation/ban-policies/:id", crit, adminDeleteBanPolicy)
	// 违规类型的增改与归档。新建与编辑共用一个入口(请求体带 id 即为编辑),
	// 与策略档同形 —— 两者的校验、二次确认、审计口径完全一致,拆成两条路由
	// 就是把同一段逻辑抄两份,而漏抄的那一份大概率是二次确认。
	g.PUT("/violation/categories", crit, adminUpsertCategory)
	// 一键写入建议阈值。它只补空(当前阈值为 0 的类型),绝不覆盖手填过的线,
	// 且没有 confirm 一律 409 —— 语义与影响面口径写在 adminApplySuggestedThresholds 上。
	g.POST("/violation/categories/apply-suggested", crit, adminApplySuggestedThresholds)
	// DELETE 是**归档**,不是删除:历史违规记录一行不动,规则改绑到 reassign_to
	// (缺省「未分类」)。语义写在 adminArchiveCategory 上,界面文案必须照抄。
	g.DELETE("/violation/categories/:id", crit, adminArchiveCategory)
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
