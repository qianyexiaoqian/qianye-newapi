package middleware

import (
	"fmt"

	"github.com/QuantumNous/new-api/common"
	"github.com/gin-gonic/gin"
)

const RouteTagKey = "route_tag"

func RouteTag(tag string) gin.HandlerFunc {
	return func(c *gin.Context) {
		c.Set(RouteTagKey, tag)
		c.Next()
	}
}

func SetUpLogger(server *gin.Engine) {
	server.Use(gin.LoggerWithFormatter(func(param gin.LogFormatterParams) string {
		var requestID string
		if param.Keys != nil {
			requestID, _ = param.Keys[common.RequestIdKey].(string)
		}
		tag, _ := param.Keys[RouteTagKey].(string)
		if tag == "" {
			tag = "web"
		}
		// 访问日志里的来源 IP 必须与审计台账**逐字同源**。
		//
		// param.ClientIP 是 gin 自己的 c.ClientIP(),而它与 common.ClientIP 在四处
		// 给出不同答案(多值 XFF 取第一个、链上带端口就整条放弃、读不到
		// CLIENT_IP_HEADERS、不做 ::ffff:/大小写归一化)。访问日志是运维排查滥用
		// 时最先看的东西,两边给出两个不同的地址、而日志那一侧还是攻击者可控的,
		// 就等于把"限流按真实 IP、台账记伪造 IP"换成了"台账按真实 IP、访问日志记
		// 伪造 IP"。理由与实测分叉见 common.ClientIPForAccessLog。
		//
		// 回落逻辑刻意留在 common 里,好让这个文件里一次都不出现 gin 的那个字段名。
		return fmt.Sprintf("[GIN] %s | %s | %s | %3d | %13v | %15s | %7s %s\n",
			param.TimeStamp.Format("2006/01/02 - 15:04:05"),
			tag,
			requestID,
			param.StatusCode,
			param.Latency,
			common.ClientIPForAccessLog(param),
			param.Method,
			param.Path,
		)
	}))
}
