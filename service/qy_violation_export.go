package service

import (
	relaycommon "github.com/QuantumNous/new-api/relay/common"
	"github.com/QuantumNous/new-api/relaykit/types"

	"github.com/gin-gonic/gin"
)

// 千夜扩展 · 违规检测的挂载点。
//
// 声明在 service 包内而不是新建一个叶子包:controller/relay.go 已经 import 了
// service,因此调用点连 import 都不用改,上游 diff 只剩两行纯新增。
//
// 默认实现是 no-op,扩展未启用时行为与上游逐字节一致;调用点也因此不需要 nil 判断。
// 真正的实现由 qianye/modules/violation 在 Init 阶段(早于任何 HTTP 请求与后台
// 协程)一次性赋值,不存在并发读写窗口。
var (
	// QyPreRelayGuard 是转发上游之前的提示词拦截。
	//
	// 参数里带着 upstreamErr 并原样返回,是为了让调用点复用上游已有的
	// `if err != nil { ... return }` 分支 —— relay.go 因此只需要一行新增。
	// 返回的错误若是 *types.NewAPIError,上游的 types.NewError 会用 errors.As
	// 原样保留它(错误码、skip-retry 等选项都不丢),再由 relay.go 的 defer
	// 按 relayFormat 序列化成 OpenAI / Claude / Realtime 三种格式。
	QyPreRelayGuard = func(c *gin.Context, info *relaycommon.RelayInfo, meta *types.TokenCountMeta, upstreamErr error) error {
		return upstreamErr
	}

	// QyPostRelayGuard 是上游返回错误之后的违规检测与扣费。
	//
	// 挂在退款(Billing.Refund)与内置 Grok 违规扣费之后,不修改 apiErr:
	// 上游错误原样回给用户,只是额外扣了钱。
	QyPostRelayGuard = func(c *gin.Context, info *relaycommon.RelayInfo, apiErr *types.NewAPIError) {}
)
