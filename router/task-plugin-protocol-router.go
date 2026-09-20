package router

import (
	"fmt"

	"github.com/QuantumNous/new-api/controller"
	"github.com/QuantumNous/new-api/middleware"
	pluginruntime "github.com/QuantumNous/new-api/pkg/jsplugin"
	"github.com/QuantumNous/new-api/relaykit/types"
	"github.com/gin-gonic/gin"
)

func SetTaskPluginProtocolRouter(router *gin.Engine) {
	for _, protocol := range pluginruntime.HostProtocols() {
		for _, operation := range protocol.Operations {
			for _, method := range operation.Methods {
				handlers, err := taskPluginProtocolHandlers(protocol.Name, operation.Name)
				if err != nil {
					panic(err)
				}
				router.Handle(method, operation.Path, handlers...)
			}
		}
	}
}

func taskPluginProtocolHandlers(protocol, operation string) ([]gin.HandlerFunc, error) {
	switch protocol + "." + operation {
	case "openai_responses.create":
		return []gin.HandlerFunc{
			middleware.RouteTag("relay"), middleware.SystemPerformanceCheck(), middleware.TokenAuth(),
			// QyTokenLiveStats 紧跟 TokenAuth、排在限流之前(被 429 挡掉的请求也要计入)。
			middleware.QyTokenLiveStats(),
			middleware.ModelRequestRateLimit(), middleware.PinTaskPluginEndpoint(), middleware.PrepareTaskPluginEndpoint(), middleware.Distribute(),
			func(c *gin.Context) {
				controller.RelayTaskPluginEndpoint(c, func(c *gin.Context) { controller.Relay(c, types.RelayFormatOpenAIResponses) })
			},
		}, nil
	case "openai_image.generate", "openai_image.edit":
		// /v1/images/generations 与 /images/edits 原来挂在 relayV1Router 的 httpRouter 下,
		// 上游 03563a4a7 把它们搬到这里时用的是上游自己的链。本仓那组上的两道闸
		// 必须跟过来:QyTokenLiveStats(「API 密钥」页的实时计数,排在限流之前)与
		// QyModelRequestConcurrencyLimit(按用户分组的在途并发上限,排在 RPM 之后)。
		return []gin.HandlerFunc{
			middleware.RouteTag("relay"), middleware.SystemPerformanceCheck(), middleware.TokenAuth(),
			middleware.QyTokenLiveStats(),
			middleware.ModelRequestRateLimit(), middleware.QyModelRequestConcurrencyLimit(),
			middleware.PinTaskPluginEndpoint(), middleware.PrepareTaskPluginEndpoint(), middleware.Distribute(),
			func(c *gin.Context) {
				controller.RelayTaskPluginEndpoint(c, func(c *gin.Context) { controller.Relay(c, types.RelayFormatOpenAIImage) })
			},
		}, nil
	case "openai_video.create":
		return []gin.HandlerFunc{
			middleware.RouteTag("relay"), middleware.TokenAuth(), middleware.QyTokenLiveStats(), middleware.SystemPerformanceCheck(),
			middleware.PinTaskPluginEndpoint(), middleware.TaskPluginEndpointOnly(middleware.ModelRequestRateLimit()), middleware.PrepareTaskPluginEndpoint(), middleware.Distribute(),
			func(c *gin.Context) { controller.RelayTaskPluginEndpoint(c, controller.RelayTask) },
		}, nil
	case "openai_responses.retrieve":
		return []gin.HandlerFunc{middleware.RouteTag("relay"), middleware.TokenAuth(), middleware.QyTokenLiveStats(), controller.RetrieveTaskPluginResponse}, nil
	case "openai_video.retrieve":
		return []gin.HandlerFunc{middleware.RouteTag("relay"), middleware.TokenAuth(), middleware.QyTokenLiveStats(), middleware.Distribute(), controller.RelayTaskFetch}, nil
	case "openai_video.content":
		return []gin.HandlerFunc{middleware.RouteTag("relay"), middleware.TokenAuth(), controller.VideoProxy}, nil
	default:
		return nil, fmt.Errorf("host protocol registry operation %s.%s has no handler", protocol, operation)
	}
}
