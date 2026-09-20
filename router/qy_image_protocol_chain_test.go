package router

import (
	"reflect"
	"runtime"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

// TestImageProtocolEntriesKeepQyRelayGates 守 /v1/images/generations 与 /images/edits
// 搬进宿主协议路由(上游 03563a4a7)之后,本仓 relayV1Router 上那两道闸仍在链上。
//
// qy_token_live_coverage_test.go 只沿 .Use(...) 追链,看不见
// taskPluginProtocolHandlers 返回的切片,所以这里单独钉住:漏掉的表现是
// 「API 密钥」页图片流量消失、分组并发上限对图片不生效,而全程不报错。
func TestImageProtocolEntriesKeepQyRelayGates(t *testing.T) {
	for _, operation := range []string{"generate", "edit"} {
		handlers, err := taskPluginProtocolHandlers("openai_image", operation)
		require.NoError(t, err)
		names := make([]string, 0, len(handlers))
		for _, handler := range handlers {
			names = append(names, runtime.FuncForPC(reflect.ValueOf(handler).Pointer()).Name())
		}
		// 构造函数会被内联,名字形如 router.taskPluginProtocolHandlers.QyTokenLiveStats.func15,
		// 所以按 ".名字." 匹配,不带包名。
		index := func(fragment string) int {
			for i, name := range names {
				if strings.Contains(name, fragment) {
					return i
				}
			}
			return -1
		}
		tokenAuth, liveStats := index(".TokenAuth."), index(".QyTokenLiveStats.")
		rateLimit, concurrency := index(".ModelRequestRateLimit."), index(".QyModelRequestConcurrencyLimit.")
		distribute := index(".Distribute.")
		require.GreaterOrEqual(t, liveStats, 0, "openai_image.%s 缺 QyTokenLiveStats: %v", operation, names)
		require.GreaterOrEqual(t, concurrency, 0, "openai_image.%s 缺 QyModelRequestConcurrencyLimit: %v", operation, names)
		require.Less(t, tokenAuth, liveStats, "QyTokenLiveStats 要在 TokenAuth 之后")
		require.Less(t, liveStats, rateLimit, "QyTokenLiveStats 要排在限流之前,被 429 挡掉的也计数")
		require.Less(t, rateLimit, concurrency, "并发上限排在 RPM 之后,与 relayV1Router 同序")
		require.Less(t, concurrency, distribute, "两道闸都要在选渠道之前")
	}
}
