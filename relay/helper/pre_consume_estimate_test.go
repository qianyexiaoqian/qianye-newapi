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
			name:            "no max_tokens means prompt side only",
			promptTokens:    100,
			maxTokens:       0,
			completionRatio: 5,
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
