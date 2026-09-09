package riskwatch

import (
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/middleware"
	"github.com/QuantumNous/new-api/qianye/config"
	"github.com/QuantumNous/new-api/qianye/module"
	"github.com/QuantumNous/new-api/qianye/relayguard"
	"github.com/QuantumNous/new-api/qianye/service/lease"

	"github.com/gin-gonic/gin"
)

// Mod 把风控预警接进扩展的模块注册表。
//
// 对原项目后端的改动为 0 行:热路径挂钩走 qianye/relayguard 的分发器
// (上游那一行 service.QyPreRelayGuard 早已存在,是违规检测当年加的)。
type Mod struct{ module.Base }

func (Mod) Name() string { return "riskwatch" }

// Tables 返回空:本模块**一张表都不进主扩展库**。
//
// 这不是"暂时没有",而是这个功能的定义(见 qianye/db/watchdb.go):监听记录的
// 体量由管理员在页面上临时决定,把任何一部分放进主库,都会让一次监管操作有
// 机会写满佣金账本所在的那块盘。任务表也一样 —— 它与记录表在同一个事务里被
// 更新(名额预留,见 store.persist),分家就得跨库两阶段,而这里完全没有必要。
func (Mod) Tables() []any { return nil }

// WatchTables 把两张表声明到风控预警存储节点上(见 module.WatchTabler)。
func (Mod) WatchTables() []any { return []any{&Task{}, &Capture{}} }

// InstallHooks 登记热路径观察者并预热一次任务快照。
//
// 预热是必要的:第一个请求到来时快照如果还是空的,那一次就抓不到 ——
// 而重启网关恰恰常常发生在"刚建完监听任务"之后。失败不阻塞启动:存储节点
// 连不上时热路径必须继续放行(maybeRefresh 会在有流量之后自己重试)。
func (Mod) InstallHooks() {
	if !config.Get().RiskWatch.On() {
		return
	}
	// 登记成**观察者**而不是闸门:这个类型没有返回值,所以本模块在结构上
	// 不可能拦住任何一次请求。观察者整体排在闸门之前,因此被违规规则拒掉的
	// 那一次同样会被抓到(理由见 qianye/relayguard 的包注释)。
	relayguard.RegisterObserver("riskwatch", observe)

	if err := Reload(); err != nil {
		common.SysError("qianye/riskwatch: 监听任务快照预热失败(有流量后会自动重试): " + err.Error())
	}
}

// RegisterAdminRoutes 挂载管理端接口。传入的组已挂 AdminAuth(自带操作审计)。
//
// 没有用户端路由是刻意的:被监听的人不该知道自己在被监听,否则这个功能唯一的
// 用途就没了。
//
// 五个写接口都**不挂** CriticalRateLimit:它们是管理员在一张编辑页上的连续
// 操作(建一个任务、调两次概率、停掉再启动),关键操作限流会把正常编辑挡成 429。
// 这里既不动钱也不发信,AdminAuth 已经是足够的闸门。
func (Mod) RegisterAdminRoutes(g *gin.RouterGroup) {
	g.GET("/risk-watch/tasks", adminListTasks)
	g.POST("/risk-watch/tasks", adminCreateTask)
	g.PUT("/risk-watch/tasks/:id", adminUpdateTask)
	// 停止/启动走 POST 子路径而不是 PUT 上的一个 status 字段:那样"改参数"与
	// "开始盯人"会落进同一条审计动作,而这两件事在事后必须分得开。
	g.POST("/risk-watch/tasks/:id/stop", adminStopTask)
	g.POST("/risk-watch/tasks/:id/start", adminStartTask)
	// 删除会连带删掉这个任务的全部记录 —— 那是不可逆的取证材料销毁,
	// 因此这一条(且只有这一条)挂关键操作限流。
	g.DELETE("/risk-watch/tasks/:id", middleware.CriticalRateLimit(), adminDeleteTask)
	g.GET("/risk-watch/captures", adminListCaptures)
	g.GET("/risk-watch/captures/:id", adminGetCapture)
	g.GET("/risk-watch/stats", adminStats)
}

// StartTasks 启动清理与到期巡检。
//
// 两条都用 lease.Run:租约保证多节点部署下同一时刻只有一个节点在跑。
// 裸 goroutine 在这里的后果是每个节点各删各的,批次互相打架。
func (Mod) StartTasks() {
	if !config.Get().RiskWatch.On() {
		return
	}
	every := config.Get().RiskWatch.GCIntervalMinutes
	if every <= 0 {
		every = 60
	}
	lease.Run("riskwatch.prune", time.Duration(every)*time.Minute, PruneCaptures)
	// 到期巡检跑得比清理密得多,因为它解决的是**状态可见性**:一个昨天到期的
	// 任务在管理端一直显示"运行中",而管理员据此以为还在抓。一分钟一次足够,
	// 它是一条带索引的 UPDATE,零流量时影响不到任何东西。
	lease.Run("riskwatch.sweep", time.Minute, SweepWindows)
}

func init() { module.Register(Mod{}) }
