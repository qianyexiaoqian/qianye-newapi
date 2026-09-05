package jsplugin

import (
	"testing"

	"github.com/QuantumNous/new-api/common"
	pluginruntime "github.com/QuantumNous/new-api/pkg/jsplugin"
	_ "github.com/QuantumNous/new-api/plugins" // 注册内置任务插件(init)
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// hostileModelName 是探针里那个「客户端想偷偷换上去」的模型名。它不在任何插件的
// models 里,所以只要它出现在上游报文里,就一定是从请求体透传过去的。
const hostileModelName = "qy-free-model-that-must-never-reach-upstream"

// TestBuiltInTaskPluginsNeverTakeTheModelFromTheRequestBody 把「模型由鉴权与计费
// 认定,不由请求体说了算」这条不变量钉在**新的边界**上。
//
// ── 这条不变量搬了家 ──
//
// 它原先由 relay/channel/task/taskcommon.UnmarshalMetadata 在每个 Go 适配器入口
// 执行(删掉 metadata 里的 model / model_name / req_key)。上游 rc.33 把那一整层
// 适配器换成了 JS 插件,那道闸随之消失:插件拿到的是完整请求体,而内置插件普遍把
// metadata **整包并进上游报文**(kling 并进 body、google 并进 parameters、
// doubao / jimeng 直接透传)。于是
//
//	{"model":"便宜模型","metadata":{"model_name":"贵模型"}}
//
// 就能做到「按便宜的付费、按贵的出货」:渠道 models、令牌 model_limits、分组
// abilities 三层授权同时失效,而且全链路不报错 —— 消费日志记的是便宜那个,
// 事后对账查不出来。
//
// 现在的闸在宿主侧(stripModelSelectionFromRequestBody,submitContext 里调用),
// 所以这条守卫也跑宿主 + 插件的组合,而不是单独跑插件:换插件、上游改插件、
// 装第三方插件,都不该让这条不变量失效。
func TestBuiltInTaskPluginsNeverTakeTheModelFromTheRequestBody(t *testing.T) {
	generation := pluginruntime.DefaultRegistry.Generation()
	require.NotNil(t, generation)
	plugins := generation.Plugins()
	require.NotEmpty(t, plugins, "内置任务插件一个都没注册,这条守卫什么也没验到")

	checked := 0
	for _, plugin := range plugins {
		if len(plugin.Meta.Models) == 0 {
			continue
		}
		hasSubmit, err := plugin.Engine.HasExport(t.Context(), "buildSubmitRequest")
		require.NoError(t, err)
		if !hasSubmit {
			continue
		}
		billedModel := plugin.Meta.Models[0]
		t.Run(plugin.Meta.Key, func(t *testing.T) {
			// 三个键都试:上游对「模型」的字段名不止一个(kling=model_name、
			// jimeng=req_key),只试 model 等于只挡住一部分插件。
			metadata := map[string]any{}
			for _, key := range []string{"model", "model_name", "req_key"} {
				metadata[key] = hostileModelName
			}
			body := map[string]any{
				"model":    hostileModelName, // 客户端自己填的那个,同样不作数
				"prompt":   "guard",
				"metadata": metadata,
			}
			// 这一行就是生产路径上 submitContext 做的事。
			stripModelSelectionFromRequestBody(body)

			value, callErr := plugin.Engine.Call(t.Context(), "buildSubmitRequest", map[string]any{
				"model":         billedModel,
				"upstreamModel": billedModel,
				"action":        "text_to_video",
				"baseUrl":       "https://upstream.example",
				"apiKey":        "sk-guard",
				"requestBody":   body,
			})
			if callErr != nil {
				// 插件可以因为别的原因拒绝这份探针请求体(缺必填字段等)。
				// 它没产出上游报文,也就没有「按别的模型出货」这条路径。
				t.Skipf("plugin rejected the probe body: %v", callErr)
			}
			encoded, marshalErr := common.Marshal(value)
			require.NoError(t, marshalErr)
			assert.NotContainsf(t, string(encoded), hostileModelName,
				"%s 装配出来的上游报文里带着请求体给的模型名。\n"+
					"模型只能取 ctx.model / ctx.upstreamModel —— 那是鉴权与计费认定的那一个。\n"+
					"要么这个插件从 req 里取了模型(改插件),要么它用的字段名不在\n"+
					"relaycommon 的模型选择名单里(往那份名单里加)。\n"+
					"实际报文:%s", plugin.Meta.Key, string(encoded))
			checked++
		})
	}
	assert.Positive(t, checked, "没有一个内置插件被真正验到,守卫失效了")
}
