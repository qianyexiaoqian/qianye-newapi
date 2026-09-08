// Package module 提供扩展内部的模块注册表。
//
// 每个功能模块(划转、返佣、提现、可用率、违规、日志增强、分组可见性)
// 实现 Module 接口并在自己包的 init() 里注册。这样新增一个模块只需要:
//  1. 新建 qianye/modules/<name>/ 包
//  2. 在 qianye/modules.go 加一行 blank import
//
// 不需要去改 tables.go、router.go、hooks.go、bootstrap.go —— 那些共享文件
// 一旦每个模块都去改,并行开发就会不断冲突。
//
// init() 里只做注册(往切片里追加),不读配置、不连数据库,
// 因此不受 "init() 早于 godotenv.Load" 这个约束的影响。
package module

import "github.com/gin-gonic/gin"

// Module 是功能模块的统一契约。所有方法都可以是空实现。
type Module interface {
	// Name 是模块标识,用于日志与租约命名。
	Name() string

	// Tables 返回该模块需要自动迁移的 GORM 模型。
	Tables() []any

	// InstallHooks 把实现注入上游包的 hook 变量。
	// 调用时机在 Init() 内,早于 HTTP 监听与后台协程。
	InstallHooks()

	// RegisterUserRoutes 注册普通用户接口。传入的组已挂 UserAuth。
	RegisterUserRoutes(g *gin.RouterGroup)

	// RegisterAdminRoutes 注册管理端接口。传入的组已挂 AdminAuth。
	RegisterAdminRoutes(g *gin.RouterGroup)

	// StartTasks 启动该模块的后台任务。
	// 实现必须用 lease.Run 而非裸 goroutine,否则多节点会重复执行。
	StartTasks()
}

// PublicRouter 是可选接口:实现它的模块可以往 **匿名可访问**的根组挂只读路由。
//
// 传入的组只挂了 GlobalAPIRateLimit 与请求台账,没有任何认证 —— 因此实现方
// 只允许注册"公开事实"类的只读端点(抽奖的公正性证据链就是为它存在的:
// 需要注册账号才能取证的公正性不叫公正性)。
//
// 刻意做成可选接口而不是往 Module 里加方法:绝大多数模块没有任何匿名面,
// 给它们每人加一个空实现只会让"这里本来就该是空的"和"这里忘了写"长得一样。
type PublicRouter interface {
	// RegisterPublicRoutes 注册匿名可访问的只读路由。
	RegisterPublicRoutes(g *gin.RouterGroup)
}

// LogTabler 是可选接口:实现它的模块可以把**高频只读台账**声明到台账库
// (config.Config.LogDatabase)上。
//
// 判据不是"这张表很大",而是"它的行数正比于请求数、并且按保留期滚动清理"。
// qy_violation_ai_review 是目前唯一一张:开了 10% 抽样的百万级站点一天几十万行,
// 每小时还要批量删一遍 —— 那些代价没有理由落在佣金账本旁边。
//
// 台账库没配 dsn 时(默认),这些表跟着 Tables() 一起迁进主扩展库,与本接口
// 存在之前逐字节一致。也就是说实现它**不要求**任何部署去准备第二个库。
//
// 刻意做成可选接口而不是往 Module 里加方法:绝大多数模块一张台账表都没有,
// 给它们每人加一个 return nil 只会让"这里本来就该是空的"和"这里忘了写"长得一样
// (与 PublicRouter 同一条理由)。
//
// 硬约束:声明到这里的表**不得**与主库的表 JOIN,也不得参与任何跨表事务 ——
// 分家之后它们在两个连接上,那种查询会在配了 log_database 的部署上直接报错,
// 而在没配的部署上完全正常。这类分叉最难查,所以它是纪律而不是建议。
type LogTabler interface {
	// LogTables 返回该模块要迁移到台账库的 GORM 模型。
	LogTables() []any
}

var registry []Module

// Register 由各模块在 init() 中调用。
func Register(m Module) { registry = append(registry, m) }

// All 返回已注册的全部模块,顺序即注册顺序。
func All() []Module { return registry }

// Base 提供全部方法的空实现,模块内嵌它之后只需覆盖自己用到的方法。
type Base struct{}

func (Base) Tables() []any                          { return nil }
func (Base) InstallHooks()                          {}
func (Base) RegisterUserRoutes(g *gin.RouterGroup)  {}
func (Base) RegisterAdminRoutes(g *gin.RouterGroup) {}
func (Base) StartTasks()                            {}
