package taskcommon

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// UnmarshalMetadata 是 7 个适配器里 6 个共用的 metadata 合并入口。
// 它此前只删 "model" 一个键,kling 的 model_name 与 jimeng 的 req_key 直接穿过去。
func TestUnmarshalMetadataDropsEveryModelSelectionKey(t *testing.T) {
	metadata := map[string]any{
		"model":      "expensive-model",
		"model_name": "expensive-model",
		"req_key":    "expensive-model",
		"MODEL_NAME": "expensive-model",
		"mode":       "pro",
	}

	var target struct {
		Model     string `json:"model"`
		ModelName string `json:"model_name"`
		ReqKey    string `json:"req_key"`
		Mode      string `json:"mode"`
	}
	target.Model = "cheap-model"
	target.ModelName = "cheap-model"
	target.ReqKey = "cheap-model"

	require.NoError(t, UnmarshalMetadata(metadata, &target))

	assert.Equal(t, "cheap-model", target.Model)
	assert.Equal(t, "cheap-model", target.ModelName)
	assert.Equal(t, "cheap-model", target.ReqKey)
	assert.Equal(t, "pro", target.Mode, "普通透传字段必须照常合并")
	assert.Equal(t, map[string]any{"mode": "pro"}, metadata, "模型字段必须从 metadata 里被摘掉")
}
