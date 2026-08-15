package dto

// reasoning_reconcile_test.go —— 推理 token 的对账。
//
// ═══════════════════════ 这条测试守的是一个实测到的资损 ═══════════════════════
//
// 2026-08-14 上线前测试的一次真实调用:
//
//	{"prompt_tokens":8, "completion_tokens":42, "total_tokens":521,
//	 "completion_tokens_details":{"reasoning_tokens":471}}
//
// 网关按 prompt+completion=50 记账,实收 65 quota;若按上游真实消耗的 521 个
// token 计,应收 772 quota。**实收是应收的 8.4%**,而站点是按 521 向上游付的钱。
//
// 修法必须对两类上游都正确,所以这里的每一条用例都是一个方向的守卫:
// 少收(本站上游)要补齐,多收(标准上游)一个字节都不许动。

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestReconcileReasoningTokens(t *testing.T) {
	tests := []struct {
		name      string
		usage     Usage
		wantCompl int
		explain   string
	}{
		{
			name: "本站上游:reasoning 只进 total,不进 completion → 必须补齐",
			usage: Usage{
				PromptTokens:     8,
				CompletionTokens: 42,
				TotalTokens:      521,
			},
			wantCompl: 513, // 521 - 8
			explain:   "这正是实测到那一笔;不补齐则 471 个已付费 token 完全免费",
		},
		{
			name: "标准 OpenAI:reasoning 已并进 completion → 恒等,零变化",
			usage: Usage{
				PromptTokens:     8,
				CompletionTokens: 513,
				TotalTokens:      521,
			},
			wantCompl: 513,
			explain:   "直接 completion+=reasoning 会在这里重复计一次,把少收变成多收",
		},
		{
			name: "普通无推理响应 → 恒等",
			usage: Usage{
				PromptTokens:     100,
				CompletionTokens: 50,
				TotalTokens:      150,
			},
			wantCompl: 50,
			explain:   "绝大多数请求走这条路径,必须逐位不变",
		},
		{
			name: "上游没给 total → 不动",
			usage: Usage{
				PromptTokens:     8,
				CompletionTokens: 42,
				TotalTokens:      0,
			},
			wantCompl: 42,
			explain:   "拿一个缺失的字段去调高收费,是拿用户的钱赌上游的正确性",
		},
		{
			name: "total 小于 prompt(上游错乱)→ 不动",
			usage: Usage{
				PromptTokens:     100,
				CompletionTokens: 50,
				TotalTokens:      30,
			},
			wantCompl: 50,
			explain:   "这种响应本来就不可信,不能据此改动计费",
		},
		{
			name: "上游只给 total、completion 留空 → 补出来",
			usage: Usage{
				PromptTokens:     10,
				CompletionTokens: 0,
				TotalTokens:      90,
			},
			wantCompl: 80,
			explain:   "残缺响应此前会被按 0 个输出 token 计费",
		},
		{
			name: "total 恰好等于 prompt(纯输入、无输出)→ 不变",
			usage: Usage{
				PromptTokens:     40,
				CompletionTokens: 0,
				TotalTokens:      40,
			},
			wantCompl: 0,
			explain:   "derived 为 0,不大于现值,不改",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			u := tc.usage
			promptBefore, totalBefore := u.PromptTokens, u.TotalTokens

			u.ReconcileReasoningTokens()

			assert.Equal(t, tc.wantCompl, u.CompletionTokens, tc.explain)
			assert.Equal(t, promptBefore, u.PromptTokens, "绝不能改动 prompt_tokens")
			assert.Equal(t, totalBefore, u.TotalTokens, "绝不能改动 total_tokens —— 它是上游的原始事实")
		})
	}
}

// TestReconcileReasoningTokensIsIdempotent 重复调用必须收敛。
//
// 流式与非流式各调一次、将来可能还有第三处调用点 —— 一旦它不幂等,
// 多调一次就多收一次钱,而那种缺陷只会在特定链路上出现。
func TestReconcileReasoningTokensIsIdempotent(t *testing.T) {
	u := Usage{PromptTokens: 8, CompletionTokens: 42, TotalTokens: 521}
	u.ReconcileReasoningTokens()
	first := u.CompletionTokens
	u.ReconcileReasoningTokens()
	u.ReconcileReasoningTokens()
	assert.Equal(t, first, u.CompletionTokens, "重复对账必须收敛,否则多调一次就多收一次钱")
}

// TestReconcileReasoningTokensNilSafe 空指针不得 panic。
//
// 它跑在 relay 的返回路径上,一次 panic 就是一个 500 —— 而这条路径上
// usage 为 nil 在上游异常时是可能的。
func TestReconcileReasoningTokensNilSafe(t *testing.T) {
	var u *Usage
	assert.NotPanics(t, func() { u.ReconcileReasoningTokens() })
}
