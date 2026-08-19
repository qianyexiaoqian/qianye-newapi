package vidu

import (
	"testing"

	relaycommon "github.com/QuantumNous/new-api/relay/common"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestMetadataCannotChangeTheViduModel(t *testing.T) {
	payload, err := (&TaskAdaptor{}).convertToRequestPayload(
		&relaycommon.TaskSubmitReq{
			Model:  "viduq1",
			Prompt: "a cat",
			Metadata: map[string]any{
				"model":      "viduq2-pro",
				"model_name": "viduq2-pro",
				"bgm":        true,
			},
		},
		&relaycommon.RelayInfo{ChannelMeta: &relaycommon.ChannelMeta{UpstreamModelName: "viduq1"}},
	)

	require.NoError(t, err)
	assert.Equal(t, "viduq1", payload.Model)
	assert.True(t, payload.Bgm, "与模型无关的 metadata 仍然照常透传")
}
