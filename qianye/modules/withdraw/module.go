package withdraw

import (
	"time"

	"github.com/QuantumNous/new-api/middleware"
	"github.com/QuantumNous/new-api/qianye/config"
	qymodel "github.com/QuantumNous/new-api/qianye/model"
	"github.com/QuantumNous/new-api/qianye/module"
	"github.com/QuantumNous/new-api/qianye/service/lease"
	"github.com/QuantumNous/new-api/qianye/service/twophase"

	"github.com/gin-gonic/gin"
)

// Mod 把提现接进扩展的模块注册表。
// 本模块对原项目后端的改动为 0 行:既不需要 hook,也不需要新的上游文件。
type Mod struct{ module.Base }

func (Mod) Name() string { return "withdraw" }

func (Mod) Tables() []any {
	return []any{&Withdrawal{}, &Payee{}, &PayeeAccount{}, &Event{}, &PiiAudit{}}
}

// InstallHooks 注册补偿回调。
//
// 这里没有上游 hook 要注入,只有 twophase 需要知道"确认主库已生效之后找谁收尾"。
func (Mod) InstallHooks() {
	twophase.RegisterResolver(qymodel.KindWithdrawQuota, resolveAfterCompensation)
}

// RegisterUserRoutes 挂载普通用户接口。传入的组已挂 UserAuth。
func (Mod) RegisterUserRoutes(g *gin.RouterGroup) {
	g.GET("/withdraw/config", handleGetConfig)
	// 唯一会动佣金的入口。幂等键只防得住"同一次点击的重试",防不住脚本用不同
	// client_request_id 连续发单,所以还要挂关键操作限流。
	g.POST("/withdraw", middleware.CriticalRateLimit(), handleCreate)
	g.GET("/withdraw/records", middleware.SearchRateLimit(), handleListRecords)
	g.GET("/withdraw/payees", handleListPayees)
	g.POST("/withdraw/payees", middleware.CriticalRateLimit(), handleCreatePayee)
	g.DELETE("/withdraw/payees/:ref", handleDeletePayee)
	g.GET("/withdraw/:id", handleGetRecord)
	g.POST("/withdraw/:id/cancel", middleware.CriticalRateLimit(), handleCancel)
}

// RegisterAdminRoutes 挂载管理端接口。传入的组已挂 AdminAuth(自带上游操作审计)。
func (Mod) RegisterAdminRoutes(g *gin.RouterGroup) {
	g.GET("/withdraw", handleAdminList)
	g.GET("/withdraw/stats", handleAdminStats)
	g.GET("/withdraw/pii-audits", handleAdminPiiAudits)
	g.GET("/withdraw/:id", handleAdminGet)
	// 查看明文是被审计的高敏操作,挂关键操作限流可以让"批量扒库"变得昂贵。
	g.GET("/withdraw/:id/payee", middleware.CriticalRateLimit(), handleAdminRevealPayee)

	// 人工决策一律挂关键操作限流:它们要么改钱,要么决定钱去哪。
	crit := middleware.CriticalRateLimit()
	g.POST("/withdraw/:id/approve", crit, handleAdminApprove)
	g.POST("/withdraw/:id/reject", crit, handleAdminReject)
	g.POST("/withdraw/:id/mark-paid", crit, handleAdminMarkPaid)
	// quota 单的手动兑现入口。auto_credit_on_approve = false 时它是这类单据
	// 唯一的正向终态出口,缺了它这个开关就是一个吃佣金的稳态。
	g.POST("/withdraw/:id/credit", crit, handleAdminCreditNow)
	g.POST("/withdraw/:id/fail", crit, handleAdminFail)
	g.POST("/withdraw/:id/resolve", crit, handleAdminResolve)
}

// StartTasks 启动对账任务。
//
// 周期取两阶段补偿间隔的两倍:本任务只负责收拾补偿任务推进完终态之后的尾巴,
// 跑得比它还快没有意义,只会空扫。必须走 lease.Run —— common.IsMasterNode
// 只是个环境变量,多节点都配成 master 时对账会双跑。
func (Mod) StartTasks() {
	if !config.Get().Withdraw.Enabled {
		return
	}
	interval := 2 * twophase.Interval()
	if interval < time.Minute {
		interval = time.Minute
	}
	lease.Run("withdraw.reconcile", interval, reconcile)
}

func init() { module.Register(Mod{}) }
