package commission

import (
	"time"

	"github.com/QuantumNous/new-api/middleware"
	"github.com/QuantumNous/new-api/qianye/config"
	"github.com/QuantumNous/new-api/qianye/module"
	"github.com/QuantumNous/new-api/qianye/service/lease"

	"github.com/gin-gonic/gin"
)

// Mod 是星辉佣金模块的注册入口。内嵌 module.Base 得到全部空实现,只覆盖用到的。
type Mod struct{ module.Base }

func (Mod) Name() string { return "commission" }

// Tables 是本包全部扩展库表。邀请关系快照(qy_invite_relation)与跨节点失效流水归 invite。
func (Mod) Tables() []any {
	return []any{
		&Accrual{},
		&Balance{},
		&Settlement{},
		&GroupRate{},
		&SettleRun{},
		&Credit{},
	}
}

// InstallHooks 接管消费 / 任务计费两个上游 hook、兑换码的第二级转发槽,以及跨节点
// 失效的本地处置。
//
// D-15 时这里还要注册一个两阶段资金单的 Resolver;D-16 佣金改记星屑之后自动入账
// 是扩展库里的本地事务,没有资金单也就没有 Resolver(autocredit.go 的文件头写了
// 那一整层为什么消失)。
func (Mod) InstallHooks() {
	installHooks()
}

func (Mod) RegisterUserRoutes(g *gin.RouterGroup) {
	// 用户端全部只读。列表接口挂搜索限流:分页 + 聚合查询比单点读贵得多。
	// 下线列表与下线日消费住在 /api/qy/invite/*(invite / stardust),这里不再有一份。
	g.GET("/commission/summary", getSummary)
	g.GET("/commission/records", middleware.SearchRateLimit(), listRecords)
	g.GET("/commission/credits", middleware.SearchRateLimit(), listCredits)
}

func (Mod) RegisterAdminRoutes(g *gin.RouterGroup) {
	g.GET("/commission/records", adminListRecords)
	g.GET("/commission/config", adminGetConfig)
	g.GET("/commission/health", adminHealth)
	g.GET("/commission/credits", middleware.SearchRateLimit(), adminListCredits)

	// 写接口一律挂关键操作限流:它们要么直接改钱,要么改决定钱的参数。
	crit := middleware.CriticalRateLimit()
	g.PUT("/commission/config", crit, adminPutConfig)
	g.PUT("/commission/group-rates", crit, adminPutGroupRate)
	g.DELETE("/commission/group-rates", crit, adminDeleteGroupRate)
	g.POST("/commission/clawback", crit, adminClawback)
	g.POST("/commission/settle", crit, adminSettle)
	// 重跑今天这一轮。与上面那条按人一条的手动结算是两件事:那条只救一个人,
	// 这条是当天那一跑挂掉之后唯一的整轮补救入口(见 rearmDailyRun)。
	g.POST("/commission/settle/rerun", crit, adminRerunDailySettle)

	// 余额总览住在 api_admin_balance.go 自己的注册函数里,免得这里的清单与那边的
	// 处理器分两处维护、加了处理器忘了挂。
	registerBalanceRoutes(g)
	// 手工增减佣金。
	registerAdjustRoutes(g, crit)
	// 以用户为中心的佣金总表(一行 = 一个人)。只读,写动作复用上面几条。
	registerUserCommissionRoutes(g)
	// 邀请关系(绑定 / 换绑 / 解绑 / 拉黑)、缓存失效与下线日消费报表在 /admin/invite/*。
}

func (Mod) StartTasks() {
	if !config.Get().Commission.Enabled {
		return
	}
	cm := config.Get().Commission

	// 结算是一日一结算,这个周期只是**心跳**:每次心跳 runSettle 只判断
	// "今天这一次跑过了没有",没跑过才抢占并排空整个队列(见 settle_daily.go)。
	settleHeartbeat := cm.SettleIntervalSecs
	if settleHeartbeat <= 0 {
		settleHeartbeat = 300
	}
	creditEvery := cm.CreditIntervalSecs
	if creditEvery <= 0 {
		creditEvery = 300
	}

	// 必须走 lease.Run:common.IsMasterNode 只是个环境变量,多节点都配成
	// master 时结算、扫描与入账会双跑,直接造成重复发钱。
	//
	// 租约只保证"同一时刻只有一个节点在跑",不保证"今天只跑一次"——
	// 后者由 qy_commission_settle_run 上的条件写承担。
	lease.Run("commission.settle", time.Duration(settleHeartbeat)*time.Second, runSettle)
	lease.Run("commission.topup_scan", time.Duration(topupScanIntervalSec)*time.Second, runTopupScan)
	lease.Run("commission.credit", time.Duration(creditEvery)*time.Second, runCredit)
	// 跨节点缓存失效通道与 logs 覆盖索引由 invite.StartSharedInfra 起(它在
	// invite.enabled || stardust.enabled 时启动);佣金依赖 invite.enabled 才有关系可用,
	// 所以这里不再单独起一份。
}

func init() { module.Register(Mod{}) }
