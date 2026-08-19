package helper

import (
	"testing"

	"github.com/QuantumNous/new-api/common"

	"github.com/stretchr/testify/assert"
)

// The per-token pre-consume estimate has to use the same shape as settlement
// (`prompt + completion * CompletionRatio`). It used to add max_tokens raw,
// leaving the output side covered at 1/CompletionRatio — with the common
// CompletionRatio of 5 that is 20%. A single request then passed the balance
// gate and settled far above it: a 4,000-quota wallet reserved 3,900, settled
// 17,385 and ended at -13,385.
//
// Dropping the `* completionRatio` factor makes every ratio>1 row here fail.
func TestPreConsumeTokenEstimateCoversTheOutputSideAtTheCompletionRatio(t *testing.T) {
	originalPreConsumed := common.PreConsumedQuota
	common.PreConsumedQuota = 5000
	t.Cleanup(func() { common.PreConsumedQuota = originalPreConsumed })

	cases := []struct {
		name            string
		promptTokens    int
		maxTokens       int
		completionRatio float64
		want            float64
	}{
		{
			name:            "floor prompt, completion ratio 5",
			promptTokens:    39,
			maxTokens:       8000,
			completionRatio: 5,
			want:            5000 + 8000*5,
		},
		{
			name:            "measured overdraft case: max_tokens 30000",
			promptTokens:    3,
			maxTokens:       30000,
			completionRatio: 5,
			want:            5000 + 30000*5,
		},
		{
			name:            "prompt above the floor is used verbatim",
			promptTokens:    28623,
			maxTokens:       8000,
			completionRatio: 5,
			want:            28623 + 8000*5,
		},
		{
			name:            "completion ratio 1 keeps the historical value",
			promptTokens:    10,
			maxTokens:       8,
			completionRatio: 1,
			want:            5000 + 8,
		},
		{
			// 省略 max_tokens 是 OpenAI 协议的**默认用法**,不是异常输入。
			// 曾经这里对输出侧一个 token 都不预留,于是预扣只覆盖输入侧,
			// 而结算按 prompt + completion*CR 无条件扣款,差额直接把余额扣成负数:
			// 实测 gemini-3-flash(0.6/5)一次请求把 3100 额度的钱包打到 -11621,
			// 三路并发打到 -29476,与余额多少无关。阶梯计价一开始就有这条兜底。
			name:            "no max_tokens falls back to the default output cap",
			promptTokens:    100,
			maxTokens:       0,
			completionRatio: 5,
			want:            5000 + defaultPreConsumeMaxTokens*5,
		},
		{
			name:            "a negative max_tokens is treated the same as absent",
			promptTokens:    100,
			maxTokens:       -1,
			completionRatio: 5,
			want:            5000 + defaultPreConsumeMaxTokens*5,
		},
		{
			// 兜底只作用在输出侧:CompletionRatio<=0 时结算对输出也不收钱,
			// 预扣就不该凭空多留一笔。
			name:            "the fallback never applies when output is free",
			promptTokens:    100,
			maxTokens:       0,
			completionRatio: 0,
			want:            5000,
		},
		{
			name:            "zero completion ratio contributes nothing",
			promptTokens:    100,
			maxTokens:       8000,
			completionRatio: 0,
			want:            5000,
		},
		{
			name:            "a negative completion ratio never shrinks the prompt reservation",
			promptTokens:    100,
			maxTokens:       8000,
			completionRatio: -3,
			want:            5000,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := preConsumeTokenEstimate(tc.promptTokens, tc.maxTokens, tc.completionRatio)
			assert.Equal(t, tc.want, got)
		})
	}
}
