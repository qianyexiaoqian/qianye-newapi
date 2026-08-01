package paypass

import (
	"context"

	"github.com/QuantumNous/new-api/qianye/guard"

	"github.com/gin-gonic/gin"
)

// HeaderPayPassword 是 Middleware 读取支付密码的请求头。
//
// 用请求头而不是请求体,是为了让中间件形态不必读掉 c.Request.Body ——
// 读掉之后下游 handler 的 ShouldBindJSON 会拿到空 body,那是一个只在
// "同时用了中间件和 body 绑定"时才现形的、极难定位的故障。
const HeaderPayPassword = "X-Qy-Pay-Password"

// Require 是划转执行入口的验密闸门:通过返回 true;不通过时已写好响应并 Abort,
// 调用方直接 return 即可。
//
// 形状刻意与同一个 handler 里已有的 `guard.RequireAPI(c, guard.FlagTransfer)` 一致,
// 集成者只需在它下面再加一行。
//
// # 签名里为什么没有"是不是联系人"这类参数
//
// 裁决 1:「联系人只是方便快速填写表,不是因为是联系人就可以绕过支付密码的验证。」
// 联系人只影响前端把收款人字段填成什么,与"该不该验密"毫无关系。
// 本函数因此**没有任何**可以表达豁免的入参 —— 想加豁免的人必须先改这个签名,
// 那是一次看得见的动作,而不是在调用点悄悄包一个 if。
// no_exemption_test.go 另有一条 AST 断言在全仓禁掉 `if isContact { skip }` 的形状。
//
// # 为什么没有"关闭支付密码校验"的开关
//
// 一个能关掉验密的配置项本身就是那条绕过路径。功能总闸是 transfer.enabled ——
// 划转整体关掉时这条路径根本不会被执行到,不需要第二个开关。
//
// # ctx 为什么脱离请求
//
// 见 verify 的说明:用请求 ctx 会让客户端主动断连取消掉错误计数的写入,
// 等于把锁定策略变成可选项。
func Require(c *gin.Context, userId int, password string) bool {
	ctx, cancel := guard.ColdContext(context.Background())
	defer cancel()

	if err := verify(ctx, userId, password); err != nil {
		respondErr(c, err)
		return false
	}
	return true
}

// Middleware 是 Require 的中间件形态,从 HeaderPayPassword 取密码、
// 从上游 UserAuth 写进上下文的 "id" 取用户。
//
// 它与 Require 走的是同一个 verify,不存在第二套判定 —— 两种形态只是取参方式不同。
//
// 用途:给将来要接入支付密码的其他出钱路径(例如提现)一个零改动的挂载方式。
// 划转本轮走的是 Require 那条函数调用,因为它的密码是随请求体一起提交的。
func Middleware() gin.HandlerFunc {
	return func(c *gin.Context) {
		if !Require(c, c.GetInt("id"), c.GetHeader(HeaderPayPassword)) {
			return
		}
		c.Next()
	}
}
