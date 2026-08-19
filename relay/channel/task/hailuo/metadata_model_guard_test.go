package hailuo

import (
	"testing"

	relaycommon "github.com/QuantumNous/new-api/relay/common"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// hailuo 走的是 TaskSubmitReq.UnmarshalMetadata,它此前**一行删除都没有**,
// metadata.model 直接把唯一的模型字段整体顶掉。
func TestMetadataCannotChangeTheHailuoModel(t *testing.T) {
	payload, err := (&TaskAdaptor{}).convertToRequestPayload(
		&relaycommon.TaskSubmitReq{
			Model:  "T2V-01",
			Prompt: "a cat",
			Metadata: map[string]any{
				"model":      "MiniMax-Hailuo-02",
				"model_name": "MiniMax-Hailuo-02",
				"resolution": "1080P",
			},
		},
		&relaycommon.RelayInfo{ChannelMeta: &relaycommon.ChannelMeta{UpstreamModelName: "T2V-01"}},
	)

	require.NoError(t, err)
	assert.Equal(t, "T2V-01", payload.Model)
	assert.Equal(t, "1080P", payload.Resolution, "与模型无关的 metadata 仍然照常透传")
}
