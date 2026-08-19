package jimeng

import (
	"testing"

	relaycommon "github.com/QuantumNous/new-api/relay/common"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// 即梦用 req_key 选模型。此前 metadata.req_key 会原样覆盖计费认定的模型。
func TestMetadataCannotChangeTheJimengReqKey(t *testing.T) {
	payload, err := (&TaskAdaptor{}).convertToRequestPayload(
		&relaycommon.TaskSubmitReq{
			Model:  "jimeng_vgfm_t2v_l20",
			Prompt: "a cat",
			Metadata: map[string]any{
				"req_key": "jimeng_ti2v_v30_pro",
				"reqKey":  "jimeng_ti2v_v30_pro",
				"model":   "jimeng_ti2v_v30_pro",
				"seed":    7,
			},
		},
		&relaycommon.RelayInfo{ChannelMeta: &relaycommon.ChannelMeta{UpstreamModelName: "jimeng_vgfm_t2v_l20"}},
	)

	require.NoError(t, err)
	assert.Equal(t, "jimeng_vgfm_t2v_l20", payload.ReqKey)
	assert.Equal(t, int64(7), payload.Seed, "与模型无关的 metadata 仍然照常透传")
}

// 3.0 的 ReqKey 转换必须仍然基于**计费认定的**模型来推导。
func TestJimengV30ReqKeyConversionStillRunsOnTheBilledModel(t *testing.T) {
	payload, err := (&TaskAdaptor{}).convertToRequestPayload(
		&relaycommon.TaskSubmitReq{
			Model:    "jimeng_v30_pro",
			Prompt:   "a cat",
			Metadata: map[string]any{"req_key": "jimeng_vgfm_t2v_l20"},
		},
		&relaycommon.RelayInfo{ChannelMeta: &relaycommon.ChannelMeta{UpstreamModelName: "jimeng_v30_pro"}},
	)

	require.NoError(t, err)
	assert.Equal(t, "jimeng_ti2v_v30_pro", payload.ReqKey)
}
