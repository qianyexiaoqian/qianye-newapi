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
		&FiatRate{},
		&SettleRun{},
		&CacheInvalidation{},
	}
}

func (Mod) InstallHooks() { installHooks() }

func (Mod) RegisterUserRoutes(g *gin.RouterGroup) {
	// 用户端全部只读。列表接口挂搜索限流:分页 + 聚合查询比单点读贵得多。
	g.GET("/commission/summary", getSummary)
	g.GET("/commission/invitees", middleware.SearchRateLimit(), listInvitees)
	g.GET("/commission/records", middleware.SearchRateLimit(), listRecords)
	// 「我的下线在这段时间里贡献了多少」。口径是计佣表的 base_quota,
	// **不是** logs 的真实消费额 —— 为什么两边刻意不同,见
	// api_daily_consume.go 上 listMyInviteeDailyConsume 的文件内注释。
	g.GET("/commission/invitee-daily", middleware.SearchRateLimit(), listMyInviteeDailyConsume)
}

func (Mod) RegisterAdminRoutes(g *gin.RouterGroup) {
	g.GET("/commission/records", adminListRecords)
	g.GET("/commission/config", adminGetConfig)
	g.GET("/commission/health", adminHealth)
	// 日消费明细。两条都是只读,但都扫主库 logs 的一段区间,比这里其它
	// GET 贵一个量级,所以跟用户端列表一样挂搜索限流。
	g.GET("/commission/daily-consume", middleware.SearchRateLimit(), adminListDailyConsume)
	g.GET("/commission/daily-consume/export", middleware.SearchRateLimit(), adminExportDailyConsume)
	// 主表某一行的按天下钻。单人 + 至多 31 天,由 idx_qy_logs_user_daily 收窄,
	// 但仍然扫主库,所以与上面两条同一档限流。
	g.GET("/commission/daily-consume/by-day", middleware.SearchRateLimit(), adminUserDailyConsume)

	// 写接口一律挂关键操作限流:它们要么直接改钱,要么改决定钱的参数。
	crit := middleware.CriticalRateLimit()
	g.PUT("/commission/config", crit, adminPutConfig)
	g.PUT("/commission/group-rates", crit, adminPutGroupRate)
	g.DELETE("/commission/group-rates", crit, adminDeleteGroupRate)
	// 分组法币折算比例。与费率分开两条路由而不是塞进 group-rates 的报文:
	// 两张表现在都按**上线**分组判定(见 pricing.go),但它们是两件不同的
	// 运营决策 —— 费率是招募政策(返几个点),比例是结汇价格(一点值多少钱),
	// 调整节奏与审批人都不同。合成一次 upsert 会强迫运营改一个的时候连带
	// 填另一个,而两张表各自的层级与回落规则也会互相绊住(见 fiatrate.go)。
	g.PUT("/commission/fiat-rates", crit, adminPutFiatRate)
	g.DELETE("/commission/fiat-rates", crit, adminDeleteFiatRate)
	g.POST("/commission/clawback", crit, adminClawback)
	g.POST("/commission/settle", crit, adminSettle)
	// 重跑今天这一轮。与上面那条按人一条的手动结算是两件事:那条只救一个人,
	// 这条是当天那一跑挂掉之后唯一的整轮补救入口(见 rearmDailyRun)。
	g.POST("/commission/settle/rerun", crit, adminRerunDailySettle)
	g.POST("/commission/relations/block", crit, adminBlockRelation)
	g.POST("/commission/cache/invalidate", crit, adminInvalidateCache)

	// 余额总览与「已提现」迁移编辑。两条路由住在 api_admin_balance.go 自己的
	// 注册函数里,免得这里的清单与那边的处理器分两处维护、加了处理器忘了挂。
	registerBalanceRoutes(g, crit)
	// AFF 关系列表与手工绑定/换绑/解绑,同上。
	registerRelationRoutes(g, crit)
	// 手工增减佣金。
	registerAdjustRoutes(g, crit)
	// 以用户为中心的佣金总表(一行 = 一个人)。只读,写动作复用上面几条。
	registerUserCommissionRoutes(g)
}

func (Mod) StartTasks() {
	if !config.Get().Commission.Enabled {
		return
	}
	cm := config.Get().Commission

	// 结算已改成一日一结算,这个周期只是**心跳**:每次心跳 runSettle 只判断
	// "今天这一次跑过了没有",没跑过才抢占并排空整个队列(见 settle_daily.go)。
	//
	// 所以默认值刻意不动:存量部署重启之后,心跳还是 300 秒,变的只是每次心跳
	// 干什么。演示站那份 20 秒的 YAML 也不必改 —— 它现在只意味着"日界过后
	// 最多 20 秒开跑",不再是 15 倍的结算表行数。
	settleHeartbeat := cm.SettleIntervalSecs
	if settleHeartbeat <= 0 {
		settleHeartbeat = 300
	}
	scanEvery := cm.TopupScanIntervalSec
	if scanEvery <= 0 {
		scanEvery = 60
	}

	// 必须走 lease.Run:common.IsMasterNode 只是个环境变量,多节点都配成
	// master 时结算与扫描会双跑,直接造成重复返佣。
	//
	// 租约只保证"同一时刻只有一个节点在跑",不保证"今天只跑一次"——
	// 后者由 qy_commission_settle_run 上的条件写承担。
	// 跨节点缓存失效通道。刻意**不走 lease**:租约保证的是"只有一个节点跑",
	// 而这里要的恰恰是"每一个节点都在听" —— 少一个节点听,它就是那个继续
	// 按旧费率发钱的节点(见 cachesync.go)。
	startCacheSync()

	lease.Run("commission.settle", time.Duration(settleHeartbeat)*time.Second, runSettle)
	lease.Run("commission.topup_scan", time.Duration(scanEvery)*time.Second, runTopupScan)
	// 日消费明细那条覆盖索引的补建。挂在这里而不是启动路径上:
	// 备份库实测建一次要 68 秒,而它对启动没有任何必要。
	startLogsIndexMaintenance()
}

func init() { module.Register(Mod{}) }
