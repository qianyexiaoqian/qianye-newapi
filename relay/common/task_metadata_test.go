package common

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestStripModelSelectionMetadata(t *testing.T) {
	cases := []struct {
		name string
		in   map[string]any
		want map[string]any
	}{
		{
			name: "kling 的 model_name 必须被删掉",
			in:   map[string]any{"model_name": "kling-v2-1-master", "mode": "pro"},
			want: map[string]any{"mode": "pro"},
		},
		{
			name: "jimeng 的 req_key 必须被删掉",
			in:   map[string]any{"req_key": "jimeng_ti2v_v30_pro", "seed": 1},
			want: map[string]any{"seed": 1},
		},
		{
			name: "model 仍然被删掉",
			in:   map[string]any{"model": "MiniMax-Hailuo-02", "duration": 10},
			want: map[string]any{"duration": 10},
		},
		{
			name: "camelCase 别名也删",
			in:   map[string]any{"modelName": "x", "reqKey": "y", "prompt": "a cat"},
			want: map[string]any{"prompt": "a cat"},
		},
		{
			name: "大小写变形也删(encoding/json 的字段匹配是大小写不敏感的)",
			in:   map[string]any{"MODEL_NAME": "kling-v2-1-master", "Model": "x", "ReQ_KeY": "y", "mode": "pro"},
			want: map[string]any{"mode": "pro"},
		},
		{
			name: "与模型无关的键一个都不动",
			in:   map[string]any{"mode": "pro", "duration": "10", "aspect_ratio": "16:9"},
			want: map[string]any{"mode": "pro", "duration": "10", "aspect_ratio": "16:9"},
		},
		{
			name: "空 metadata 不炸",
			in:   nil,
			want: nil,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			StripModelSelectionMetadata(tc.in)
			assert.Equal(t, tc.want, tc.in)
		})
	}
}

// hailuo 走的是 TaskSubmitReq.UnmarshalMetadata 这一条,它此前一行删除都没有。
func TestTaskSubmitReqUnmarshalMetadataDropsModelSelectionKeys(t *testing.T) {
	req := &TaskSubmitReq{
		Metadata: map[string]any{
			"model":      "MiniMax-Hailuo-02",
			"model_name": "kling-v2-1-master",
			"req_key":    "jimeng_ti2v_v30_pro",
			"resolution": "1080P",
		},
	}

	var target struct {
		Model      string `json:"model"`
		ModelName  string `json:"model_name"`
		ReqKey     string `json:"req_key"`
		Resolution string `json:"resolution"`
	}
	target.Model = "T2V-01"

	require.NoError(t, req.UnmarshalMetadata(&target))

	assert.Equal(t, "T2V-01", target.Model, "metadata 不得改写模型")
	assert.Empty(t, target.ModelName)
	assert.Empty(t, target.ReqKey)
	assert.Equal(t, "1080P", target.Resolution, "普通透传字段必须照常合并")
}
