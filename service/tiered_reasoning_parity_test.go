package service

import (
	"testing"

	"github.com/QuantumNous/new-api/relaykit/dto"

	"github.com/stretchr/testify/assert"
)

// 阶梯计价(tiered_expr)与倍率计价必须对「上游把思考 token 放在
// completion_tokens 之外」这件事用同一条归一化。
//
// 缺了它的形状是:同一条上游响应,倍率路径按 c=54 收 524,阶梯路径按 c=1 只收
// 158(诚实价 555),而消费日志里 completion_tokens 记的是已归一化的 54 ——
// 账单与自身对不上,把日志里的三项代回日志里那条表达式得不到日志里的金额。
// 本站挂 tiered_expr 的 gemini 一族把 c 的系数定在 50~120,输出侧是主要收入项。
func TestTieredTokenParamsNormalizesReasoningLikeTheRatioPath(t *testing.T) {
	cases := []struct {
		name       string
		prompt     int
		completion int
		total      int
		reasoning  int
		semantic   string
		claude     bool
		wantC      float64
	}{
		{"上游把 reasoning 放在 completion 之外:必须补进 c", 100, 1, 154, 53, "", false, 54},
		{"真实调用 gemini-3-flash", 3, 1, 57, 53, "", false, 54},
		{"completion 为 0 的思考模型", 3, 0, 102, 99, "", false, 99},
		{"上游规范(reasoning 已含在 completion 里)不重复补", 3, 50, 53, 20, "", false, 50},
		{"三个数对不上就不猜", 3, 1, 100, 53, "", false, 1},
		{"Claude 语义:prompt 不含缓存段,total 天然更大,一个都不补", 100, 30, 500, 20, "anthropic", true, 30},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			usage := &dto.Usage{
				PromptTokens:     tc.prompt,
				CompletionTokens: tc.completion,
				TotalTokens:      tc.total,
				UsageSemantic:    tc.semantic,
			}
			usage.CompletionTokenDetails.ReasoningTokens = tc.reasoning

			params := BuildTieredTokenParams(usage, tc.claude, map[string]bool{})
			assert.Equal(t, tc.wantC, params.C,
				"阶梯计价的 c 必须与 calculateTextQuotaSummary 的 completion 口径逐值一致")

			// 与倍率路径逐值对照:两条路径对同一条 usage 必须算出同一个输出侧数量。
			ratioCompletion := float64(usage.CompletionTokens + reasoningTokensOutsideCompletion(usage))
			assert.Equal(t, ratioCompletion, params.C,
				"倍率路径与阶梯路径对同一条上游响应算出了不同的输出 token 数")
		})
	}
}
