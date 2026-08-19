package kling

import (
	"testing"

	relaycommon "github.com/QuantumNous/new-api/relay/common"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// 快手 Kling 认的模型字段是 model_name。此前 metadata 里的 model_name 会原样
// 覆盖掉计费/鉴权认定的模型:用户按 kling-v1 的价被扣,上游收到的是
// kling-v2-1-master,渠道 models / 令牌 model_limits / 分组 abilities 三层授权
// 在这条路上全部失效。
func TestMetadataCannotChangeTheKlingModel(t *testing.T) {
	info := &relaycommon.RelayInfo{ChannelMeta: &relaycommon.ChannelMeta{UpstreamModelName: "kling-v1"}}
	req := &relaycommon.TaskSubmitReq{
		Model:  "kling-v1",
		Prompt: "a cat",
		Metadata: map[string]any{
			"model_name": "kling-v2-1-master",
			"model":      "kling-v2-1-master",
			"modelName":  "kling-v2-1-master",
			"mode":       "pro",
			"duration":   "10",
		},
	}

	payload, err := (&TaskAdaptor{}).convertToRequestPayload(req, info)

	require.NoError(t, err)
	assert.Equal(t, "kling-v1", payload.ModelName)
	assert.Equal(t, "kling-v1", payload.Model)
	// 与模型无关的 metadata 仍然照常透传 —— 这道闸只针对模型字段。
	assert.Equal(t, "pro", payload.Mode)
	assert.Equal(t, "10", payload.Duration)
	// 中心黑名单那一层:模型字段必须从 metadata 里被摘掉,而不是靠下游侥幸。
	assert.NotContains(t, req.Metadata, "model_name")
	assert.NotContains(t, req.Metadata, "model")
	assert.NotContains(t, req.Metadata, "modelName")
}

func TestKlingModelFallsBackWhenUpstreamModelIsEmpty(t *testing.T) {
	payload, err := (&TaskAdaptor{}).convertToRequestPayload(
		&relaycommon.TaskSubmitReq{Prompt: "a cat", Metadata: map[string]any{"model_name": "kling-v2-1-master"}},
		&relaycommon.RelayInfo{ChannelMeta: &relaycommon.ChannelMeta{}},
	)

	require.NoError(t, err)
	assert.Equal(t, "kling-v1", payload.ModelName)
	assert.Equal(t, "kling-v1", payload.Model)
}
