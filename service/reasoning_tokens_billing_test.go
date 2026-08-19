package service

import (
	"net/http/httptest"
	"testing"
	"time"

	relaycommon "github.com/QuantumNous/new-api/relay/common"
	"github.com/QuantumNous/new-api/relaykit/dto"
	"github.com/QuantumNous/new-api/relaykit/types"
	hosttypes "github.com/QuantumNous/new-api/types"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// 上游把 reasoning 放在 completion_tokens **之外**时,本站按 completion_tokens
// 计费就等于把绝大部分输出按 0 收。这些数字全部来自备份库上的真实调用:
//
//	gemini-3-flash      {pt 3,  ct 1,   total 57,   reasoning 53}   少收 50 倍
//	gemini-3-flash 流式 {pt 38, ct 5,   total 2328, reasoning 2285} 少收 181 倍
//	gemini-3.1-pro-high {pt 3,  ct 0,   total 102,  reasoning 99}   完全按输入收费
//
// 反方向同样必须钉住:Claude 语义下 prompt_tokens 不含缓存读写,total 本来
// 就大于 prompt+completion,拿差额当思考 token 会凭空多收钱。所以补偿只在
// 三个数字互相印证(prompt+completion+reasoning == total)时才发生。
func TestReasoningTokensOutsideCompletionOnlyFiresOnAnExactIdentity(t *testing.T) {
	cases := []struct {
		name       string
		prompt     int
		completion int
		total      int
		reasoning  int
		want       int
	}{
		{"真实调用:非流式 gemini-3-flash", 3, 1, 57, 53, 53},
		{"真实调用:流式长输出", 38, 5, 2328, 2285, 2285},
		{"真实调用:completion 为 0 的思考模型", 3, 0, 102, 99, 99},
		{"上游规范(reasoning 已含在 completion 里)不动", 3, 50, 53, 20, 0},
		{"没有 reasoning 字段就不猜", 3, 1, 57, 0, 0},
		{"上游没报 total 就不猜", 3, 1, 0, 53, 0},
		{"三个数对不上就不猜(Claude 语义下 total 天然更大)", 3, 1, 100, 53, 0},
		{"prompt+completion 已等于 total 时不重复补", 10, 40, 50, 40, 0},
		{"负的 reasoning 不允许降低收费", 3, 1, 57, -53, 0},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			usage := &dto.Usage{
				PromptTokens:     tc.prompt,
				CompletionTokens: tc.completion,
				TotalTokens:      tc.total,
			}
			usage.CompletionTokenDetails.ReasoningTokens = tc.reasoning
			assert.Equal(t, tc.want, reasoningTokensOutsideCompletion(usage))
		})
	}
	assert.Equal(t, 0, reasoningTokensOutsideCompletion(nil))
}

// 光有换算还不够:必须真的进到计费与日志里。这条走 calculateTextQuotaSummary,
// 用真实调用的那组数字(gemini-3-flash,ModelRatio 0.6 / CompletionRatio 5)。
func TestTextQuotaSummaryChargesForReasoningTokens(t *testing.T) {
	gin.SetMode(gin.TestMode)
	ctx, _ := gin.CreateTestContext(httptest.NewRecorder())

	relayInfo := &relaycommon.RelayInfo{
		RelayFormat:             types.RelayFormatOpenAI,
		FinalRequestRelayFormat: types.RelayFormatOpenAI,
		OriginModelName:         "gemini-3-flash",
		PriceData: hosttypes.PriceData{
			ModelRatio:      0.6,
			CompletionRatio: 5,
			GroupRatioInfo:  hosttypes.GroupRatioInfo{GroupRatio: 1},
		},
		StartTime: time.Now(),
	}

	usage := &dto.Usage{PromptTokens: 3, CompletionTokens: 1, TotalTokens: 57}
	usage.CompletionTokenDetails.ReasoningTokens = 53

	summary := calculateTextQuotaSummary(ctx, relayInfo, usage)
	require.Equal(t, 54, summary.CompletionTokens,
		"上游把 53 个思考 token 放在 completion_tokens 之外,不补回来就是按 1 个输出 token 收费")
	assert.Equal(t, 57, summary.TotalTokens)
	// (3 + 54×5) × 0.6 = 163.8 → 164;不补的话是 (3 + 1×5) × 0.6 = 4.8 → 5。
	assert.Equal(t, 164, summary.Quota)
}
