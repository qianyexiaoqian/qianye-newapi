package qianye

// 功能模块通过 blank import 触发各自的 init() 完成自注册。
//
// 这是新增一个功能模块唯一需要改动的共享文件 —— 表定义、路由、hook、后台任务
// 全部由模块自己在 module.Module 接口里声明,不必再去改 tables.go / router.go /
// bootstrap.go。这样并行开发多个模块时不会互相冲突。
//
// import 顺序不影响正确性:init() 只往注册表追加,不读配置也不连数据库。
import (
	_ "github.com/QuantumNous/new-api/qianye/modules/apiaddr"
	_ "github.com/QuantumNous/new-api/qianye/modules/availability"
	_ "github.com/QuantumNous/new-api/qianye/modules/channelops"
	_ "github.com/QuantumNous/new-api/qianye/modules/commission"
	_ "github.com/QuantumNous/new-api/qianye/modules/groupmatrix"
	_ "github.com/QuantumNous/new-api/qianye/modules/groupns"
	_ "github.com/QuantumNous/new-api/qianye/modules/groupvis"
	_ "github.com/QuantumNous/new-api/qianye/modules/logmetrics"
	_ "github.com/QuantumNous/new-api/qianye/modules/lottery"
	_ "github.com/QuantumNous/new-api/qianye/modules/paypass"
	_ "github.com/QuantumNous/new-api/qianye/modules/planentitlement"
	_ "github.com/QuantumNous/new-api/qianye/modules/subscription"
	_ "github.com/QuantumNous/new-api/qianye/modules/ticket"
	_ "github.com/QuantumNous/new-api/qianye/modules/transfer"
	_ "github.com/QuantumNous/new-api/qianye/modules/usergroup"
	_ "github.com/QuantumNous/new-api/qianye/modules/violation"
	_ "github.com/QuantumNous/new-api/qianye/modules/withdraw"
)
