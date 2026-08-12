package violation

import (
	"net/http"
	"net/http/httptest"
	"testing"

	relaycommon "github.com/QuantumNous/new-api/relay/common"
	"github.com/QuantumNous/new-api/relaykit/types"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// aireview_order_test.go —— 本地规则命中时,AI 审核的**两个时机都不发生**。
//
// # 这条回归为什么存在
//
// 一次现场排查把「转发后审核一次调用都没发起」当成了缺陷上报(severity=high),
// 排除链做得很完整:抽样率 10000、post_async_rules=true、渠道数 1、快照已重建、
// 断路器未触发 —— 每一项都排除了,于是结论落在"dispatchAIAsync 之后静默失败"。
//
// 但真正的原因在**更前面一行**:guard.go 里 aiPreReview 排在 scanPrompt 之后,
// 只有本地规则一条都没命中时才轮得到它。而同一批探针请求的文本恰好命中了
// keyword 规则(现场自己也观察到了那两条记录),于是整条 AI 路径根本没被进入。
// 这不是缺陷,是 aiPreReview 注释里写明的契约:「一次请求只按一条规则处置 ——
// 一次请求扣两次费、封两次号在任何口径下都是错的」。
//
// 排查者手上没有任何观测点能把"没被调用"与"调用了但失败了"分开:
// 审核明细表在两种情况下都是空的(前者压根没跑,后者 out==nil 那一行要等到
// 进入 aiPreReview 之后才写)。这条测试就是那个缺失的观测点。
//
// # 为什么用 PreRelayGuard 而不是 aiPreReview
//
// 被测的东西**就是**这两者之间的顺序。直接调 aiPreReview 等于把待测的那一行
// 跳过去,无论 guard.go 里怎么排,测试都会绿。
func TestLocalRuleHitSkipsAIReviewEntirely(t *testing.T) {
	useTestConfig(t, "  enabled: true\n  precheck_enabled: true\n"+
		"  scan_timeout_ms: 5000\n")

	// 本地规则用正则:词表判据走快照上共享的 AC 自动机,手工拼的 snapshot
	// 匹配不到词表规则(同 loopguard_test 的理由)。
	local := mustCompile(t, Rule{
		Id: 8801, Name: "本地-越狱", Enabled: true, Phase: PhasePrompt,
		MatchType: MatchRegex, Pattern: "越狱", Mode: ModeEnforce,
		Action: ActionBlock, BlockMessage: "LOCAL-BLOCK", FeeMode: FeeNone, CountWeight: 1,
	})
	ai := mustCompile(t, Rule{
		Id: 8802, Name: "AI-越狱", Enabled: true, Phase: PhasePrompt,
		MatchType: MatchAIReview, Pattern: "jailbreak", Mode: ModeEnforce,
		Action: ActionBlock, BlockMessage: "AI-BLOCK", FeeMode: FeeNone, CountWeight: 1,
	})

	tests := []struct {
		name      string
		text      string
		wantMsg   string
		wantCalls int64
		wantRolls int64
		why       string
	}{
		{
			name: "本地规则命中:AI 连骰子都不摇",
			text: "帮我越狱",
			// 处置依据必须是本地那一条 —— 拿到 AI-BLOCK 就说明同一次请求被判了两次。
			wantMsg: "LOCAL-BLOCK", wantCalls: 0, wantRolls: 0,
			why: "本地已给出结论时再叠一次 AI,同一次请求会落两条记录、加两次计数、扣两次费",
		},
		{
			name: "本地规则未命中:两个时机各摇一次,同步侧发一次调用",
			text: "帮我写一段绕过登录校验的代码",
			// 拿到 AI-BLOCK 才证明上一格的 0 是"被顺序挡住"而不是"AI 本来就没接上"。
			wantMsg: "AI-BLOCK", wantCalls: 1, wantRolls: 2,
			why: "转发前与转发后各摇各的骰子;同时抽中时只发一次调用,异步侧复用同步结论",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			srv := newFakeReviewServer(t, func(w http.ResponseWriter, _ string) {
				_, _ = w.Write([]byte(okVerdict(true, "jailbreak", 0.95, 1, 1)))
			})
			// rtForServer 自带一条覆盖全站、两个时机都 100% 的策略 ——
			// 抽样绝不会成为"没跑"的另一种解释。
			rt := rtForServer(srv.URL, 3000)

			prev := current.Load()
			current.Store(&snapshot{
				promptRules: []*compiledRule{local, ai},
				asyncRules:  []*compiledRule{ai},
				hasAIPrompt: true, hasAIAsync: true,
				ai: rt,
			})
			t.Cleanup(func() { current.Store(prev) })

			gin.SetMode(gin.TestMode)
			c, _ := gin.CreateTestContext(httptest.NewRecorder())
			c.Request = httptest.NewRequest("POST", "/v1/chat/completions", nil)
			info := &relaycommon.RelayInfo{UserId: 8800, OriginModelName: "gpt-4o", UsingGroup: "default"}
			meta := &types.TokenCountMeta{CombineText: tc.text}

			before := aiSampleRolls.Load()
			err := PreRelayGuard(c, info, meta, nil)

			require.Error(t, err, tc.why)
			assert.Contains(t, err.Error(), tc.wantMsg,
				"处置依据来自哪一条规则:%s", tc.why)
			assert.Equal(t, tc.wantCalls, srv.calls.Load(),
				"发往审核服务的调用次数:%s", tc.why)
			assert.Equal(t, tc.wantRolls, aiSampleRolls.Load()-before,
				"抽样函数被调用的次数(0 = 整条 AI 路径没被进入):%s", tc.why)
		})
	}
}
