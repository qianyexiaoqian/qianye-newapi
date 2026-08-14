package violation

import (
	"context"
	"net/http"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// aireview_budget_test.go —— 转发前审核的延迟预算是**整次**的,不是每个渠道各一份。
//
// # 为什么这条值一个测试
//
// PreTimeoutMs 是唯一一个直接加在**真实用户请求**首字节延迟上的旋钮,而它的
// 全部意义就是"审核服务变慢时,全站最坏多等多久"。这个数字如果不准,
// 管理端那一格的说明就是假的,而运营是照着那句话填的。
//
// 修复前:每个渠道各拿一次完整的 timeoutMs,最坏 = 预算 × maxAIAttempts(2)。
// 实测预算 500ms、两个渠道都挂死 → 1.0019s。默认 1500ms 对应最坏 3 秒,
// 填到硬上限 5000 则是 10 秒 —— 而界面上写的是 5 秒。
//
// 单渠道那一条同样要有:只测两个渠道的话,把整段 WithTimeout 换成
// "只在第二个渠道之前判一次 deadline"也能过,而那是另一个错的实现。

// TestPreReviewBudgetCoversTheWholeCallNotEachChannel 钉住总预算。
func TestPreReviewBudgetCoversTheWholeCallNotEachChannel(t *testing.T) {
	// 两个都挂死的渠道:handler 睡到远超预算,永远不会给出结论。
	stall := func(w http.ResponseWriter, _ string) {
		time.Sleep(3 * time.Second)
		_, _ = w.Write([]byte(okVerdict(false, "none", 1, 1, 1)))
	}
	a := newFakeReviewServer(t, stall)
	b := newFakeReviewServer(t, stall)

	const budgetMs = 600
	// 渠道自己也填了超时,而且**比总预算大** —— 这是现网的形状(渠道 id=1 的
	// timeout_ms 是 5000,而转发前预算的硬上限同样是 5000)。
	//
	// 这一行是这条守卫的全部力气所在。夹具原先把它留在 0,于是每次尝试的预算
	// 退化到 minAITimeoutMs(200ms),两三个渠道加起来仍然远在容差之内 ——
	// 实测把整段外层 WithTimeout 删掉(`if timeoutMs > 0` 改成 `if false`,
	// 等于退回"每个渠道各拿一份完整预算"那个已修复的缺陷),这条测试与
	// aireview_failover_test.go 的墙钟那条**双双照样是绿的**。
	// 也就是说,守的不是渠道超时,守的是"渠道超时不许突破总预算"。
	const chTimeoutMs = 2500
	// 容差 = 预算 + 一份固定抖动,不再是"2 倍预算"。倍数容差与"预算被乘以
	// 渠道数"是同一个量级,天生挡不住它;而本地 httptest 的调度抖动是几十毫秒级。
	const tolerance = budgetMs*time.Millisecond + 400*time.Millisecond

	t.Run("两个渠道都挂死时,总耗时仍受单份预算约束", func(t *testing.T) {
		rt := &aiRuntime{
			Channels: []*aiChannelRT{
				{Id: 1, Name: "a", URL: chatCompletionsURL(a.URL), Model: "m", Weight: 1, TimeoutMs: chTimeoutMs},
				{Id: 2, Name: "b", URL: chatCompletionsURL(b.URL), Model: "m", Weight: 1, TimeoutMs: chTimeoutMs},
			},
			Prompt: defaultAIPrompt, MaxInputChars: defaultAIMaxInputChars,
		}
		started := time.Now()
		out := runAIReview(context.Background(), rt, nil, "待审内容", budgetMs)
		elapsed := time.Since(started)

		require.NotNil(t, out)
		assert.False(t, out.decided(), "两个渠道都没给出结论,必须放行")
		assert.Less(t, elapsed, tolerance,
			"总耗时 %v 超过了单份预算的容差 —— 预算被按渠道数乘了一遍,"+
				"而管理端文案承诺的是这一个数字", elapsed)
	})

	t.Run("单渠道同样受同一份预算约束", func(t *testing.T) {
		rt := &aiRuntime{
			Channels: []*aiChannelRT{
				{Id: 1, Name: "a", URL: chatCompletionsURL(a.URL), Model: "m", Weight: 1, TimeoutMs: chTimeoutMs},
			},
			Prompt:        defaultAIPrompt,
			MaxInputChars: defaultAIMaxInputChars,
		}
		started := time.Now()
		out := runAIReview(context.Background(), rt, nil, "待审内容", budgetMs)
		elapsed := time.Since(started)

		require.NotNil(t, out)
		assert.False(t, out.decided())
		assert.Less(t, elapsed, tolerance, "单渠道耗时 %v 也超了预算", elapsed)
	})

	t.Run("预算耗尽的结局是超时(放行),不是拦截", func(t *testing.T) {
		rt := &aiRuntime{
			Channels: []*aiChannelRT{
				{Id: 1, Name: "a", URL: chatCompletionsURL(a.URL), Model: "m", Weight: 1, TimeoutMs: chTimeoutMs},
				{Id: 2, Name: "b", URL: chatCompletionsURL(b.URL), Model: "m", Weight: 1, TimeoutMs: chTimeoutMs},
			},
			Prompt: defaultAIPrompt, MaxInputChars: defaultAIMaxInputChars,
		}
		out := runAIReview(context.Background(), rt, nil, "待审内容", budgetMs)
		require.NotNil(t, out)
		assert.Equal(t, OutcomeTimeout, out.Outcome,
			"超时必须单独成一态:与 upstream_error 合并会让运维分不清该调预算还是查网络")
		// 失败即放行的最终判据:任何 ai_review 规则都不该命中。
		rule := mustCompile(t, Rule{
			Id: 1, Name: "ai", Phase: PhasePrompt, MatchType: MatchAIReview,
			Pattern: "", Mode: ModeEnforce, Action: ActionBlock, FeeMode: FeeNone,
		})
		assert.Nil(t, matchAIRule(rule, out), "超时的结论绝不能命中任何规则")
	})
}
