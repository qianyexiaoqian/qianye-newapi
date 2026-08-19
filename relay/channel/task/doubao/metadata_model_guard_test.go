package doubao

import (
	"testing"

	relaycommon "github.com/QuantumNous/new-api/relay/common"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// doubao 的模型字段就叫 model,原来那条一键黑名单挡得住;这条用例把它钉死,
// 顺带覆盖 BuildRequestBody 未做模型映射时会把 body.Model 回写进
// info.UpstreamModelName 的那条路 —— 那条回写绝不能被 metadata 污染。
func TestMetadataCannotChangeTheDoubaoModel(t *testing.T) {
	payload, err := (&TaskAdaptor{}).convertToRequestPayload(&relaycommon.TaskSubmitReq{
		Model:  "doubao-seedance-1-0-lite-t2v",
		Prompt: "a cat",
		Metadata: map[string]any{
			"model":      "doubao-seedance-1-0-pro",
			"model_name": "doubao-seedance-1-0-pro",
		},
	})

	require.NoError(t, err)
	assert.Equal(t, "doubao-seedance-1-0-lite-t2v", payload.Model)
}
