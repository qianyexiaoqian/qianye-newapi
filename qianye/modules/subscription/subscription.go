// Package subscription 给上游的订阅套餐补两件运营一直缺的能力:
//
//  1. 删除套餐          —— 上游连未挂路由的删除函数都没有,套餐只能停用不能删。
//  2. 全站总名额(限购人数) —— 上游只有「每人限购次数」,没有「全站最多几个人在用」。
//
// # 与上游的耦合面
//
// 上游改动一共 5 处、每处 1 行,全部是同一个 hook 变量的调用:
//
//	model/subscription.go          CreateUserSubscriptionFromPlanTx  强一致闸门(事务内)
//	controller/subscription_payment_epay.go           下单前置预检(体验层)
//	controller/subscription_payment_stripe.go         同上
//	controller/subscription_payment_creem.go          同上
//	controller/subscription_payment_waffo_pancake.go  同上
//
// hook 声明在纯新增文件 model/qy_subscription_export.go,五个调用点都不改 import。
//
// 删除套餐**不走 hook**:它是一个纯管理端动作,没有任何上游代码需要感知它,
// 所以整条链路都住在本模块里,路由挂在扩展自己的 /api/qy/admin 组下。
//
// # 数据分布(这是本模块最需要想清楚的一件事)
//
//	名额上限 capacity   → 扩展库 qy_subscription_plan_seats(运营配置,极低频变更)
//	名额占用 used       → 主库 user_subscriptions(每次购买都变,必须强一致)
//
// 把「变的那一半」留在主库是刻意的:占用数要在购买事务内数,才能看见同事务里
// 尚未提交的那一行。跨库的只剩一个慢变的配置数字,可以安全地缓存。
// 完整的取舍与残余风险写在 gate.go 的 gateSeat 上,不要绕过它看。
package subscription

import (
	"github.com/QuantumNous/new-api/middleware"
	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/qianye/module"

	"github.com/gin-gonic/gin"
)

// Mod 是订阅套餐增强模块的注册入口。
type Mod struct{ module.Base }

func (Mod) Name() string { return "subscription" }

func (Mod) Tables() []any { return []any{&PlanSeat{}} }

// InstallHooks 无条件注入名额闸门,并预热一次配置缓存。
//
// 无条件注入是安全的:没有任何套餐配了名额时 gateSeat 原样返回入参。反过来,
// 把注入包在「已配置」判断里会引入一个更糟的状态 —— 运营在管理端配好名额之后
// 必须重启进程才生效,而重启前后行为不同这件事在日志里完全看不出来。
//
// 预热的目的是让进程起来后的**第一次**购买就走在正确的名额上:缓存冷的时候
// gateSeat 会 fail-open 放行,那一次购买就绕过了名额。预热失败不阻塞启动 ——
// 扩展库连不上时订阅购买必须照常进行。
func (Mod) InstallHooks() {
	model.QyGateSubscriptionSeat = gateSeat
	warmCapacities()
}

// RegisterAdminRoutes 挂管理端接口。
//
// 不新增 guard.Flag:本功能没有 YAML 开关(「是否启用」等价于「有没有给某个套餐
// 配名额」),用 FlagCore 即「扩展已启用且扩展库可用」正是这里需要的语义。
// 多定义一个恒为 true 的开关只会多出一个没人消费的配置项 —— 与 usergroup 同口径。
//
// 删除用 POST /…/delete 而不是 DELETE /…:删除必须带 body(force 与必填的
// reason),而 DELETE 携带请求体在中间的反向代理、CDN、以及部分 HTTP 客户端上
// 属于"允许但没人保证"的灰色地带,丢 body 的表现是 reason 变空 → 400,
// 排查起来完全指错方向。这里宁可牺牲一点 REST 洁癖。
func (Mod) RegisterAdminRoutes(g *gin.RouterGroup) {
	// 三条路由全部挂在 /subscription/plans/:plan_id 之下。前缀统一不是洁癖:
	// 前端只需要记住一个基址,路径拼错在前端是 404、在 qy 客户端里又会被归类成
	// "扩展未启用"从而**静默隐藏入口**,排查方向直接指反。
	g.GET("/subscription/plans/:plan_id/usage", adminPlanUsage)
	// 改总名额会立刻决定还能不能有人买这个套餐,按关键操作限流。
	g.PUT("/subscription/plans/:plan_id/seat-limit", middleware.CriticalRateLimit(), adminPutSeat)
	// 删除会级联失效用户订阅与待处理订单,是本模块破坏力最大的一个接口。
	g.POST("/subscription/plans/:plan_id/delete", middleware.CriticalRateLimit(), adminDeletePlan)
}

func init() { module.Register(Mod{}) }
