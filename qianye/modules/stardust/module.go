package stardust

import (
	"github.com/QuantumNous/new-api/qianye/config"
	qyctl "github.com/QuantumNous/new-api/qianye/controller"
	"github.com/QuantumNous/new-api/qianye/module"

	"github.com/gin-gonic/gin"
)

// Mod 把星屑接进扩展的模块注册表。
//
// 本模块对原项目后端的改动为 0 行:表全在扩展库,hook 只接扩展自己的引导端点。
type Mod struct{ module.Base }

// Name 必须与目录名一致(qianye/modules_test.go)。
func (Mod) Name() string { return "stardust" }

func (Mod) Tables() []any { return Tables() }

// InstallHooks 接管引导端点的星屑段。
//
// 单位名与入口开关都是运营在管理端改的(qy_settings),而 qyctl 的默认实现只读
// YAML —— 不接管就会出现"运营改了名,前台照旧显示旧名"。匿名可访问,
// 因此只有名字与开关,没有比例、没有余额。
func (Mod) InstallHooks() {
	qyctl.QyStardustSection = func() map[string]any {
		s := effective()
		return map[string]any{"show_entry": s.ShowEntry, "name": s.Name}
	}
	// 上游事件的接管(invite.AfterRedeemSuccess / model.QyOnUserRegistered /
	// model.QyOnSubscriptionGranted)由 hooks.go 登记进 hookInstallers,这里只遍历。
	for _, install := range hookInstallers {
		install()
	}
}

// 路由与后台任务的注册表。
//
// 用户端 / 管理端接口与后台任务分别由 api_user.go / api_admin*.go / settle.go 等文件
// 在各自的 init() 里追加:
//
//	func init() { userRouteInstallers = append(userRouteInstallers, func(g *gin.RouterGroup) { ... }) }
//
// 做成注册表而不是在这里逐条写路由:这些文件是并行开发的,共享一份 module.go
// 意味着每一条路由都要改同一个文件、不断冲突。init() 只往切片里追加,不读配置、
// 不连数据库,因此顺序与时机都无关紧要。
var (
	userRouteInstallers  []func(*gin.RouterGroup)
	adminRouteInstallers []func(*gin.RouterGroup)
	taskStarters         []func()
	// hookInstallers 是接管上游 hook 变量的登记表(hooks.go 追加),理由与路由登记表相同。
	hookInstallers []func()
)

// RegisterUserRoutes 挂载普通用户接口。传入的组已挂 UserAuth。
// 刻意只挂 UserAuth 不挂 TokenAuth:API Key 是给机器用的、可批量分发。
func (Mod) RegisterUserRoutes(g *gin.RouterGroup) {
	for _, install := range userRouteInstallers {
		install(g)
	}
}

// RegisterAdminRoutes 挂载管理端接口。传入的组已挂 AdminAuth(自带上游操作审计)。
func (Mod) RegisterAdminRoutes(g *gin.RouterGroup) {
	for _, install := range adminRouteInstallers {
		install(g)
	}
}

// StartTasks 启动后台任务(日结、充值扫描等,由各文件登记进 taskStarters)。
//
// 总闸在这里判一次:stardust.enabled=false 时一个任务都不起,与 sections.go 登记的
// 效果("消费返 / 邀请返 / 套餐返全部停止")一致。各任务实现必须用 lease.Run
// 而非裸 goroutine,否则多节点会重复执行。
func (Mod) StartTasks() {
	if !config.Get().Stardust.Enabled {
		return
	}
	for _, start := range taskStarters {
		start()
	}
}

func init() { module.Register(Mod{}) }
