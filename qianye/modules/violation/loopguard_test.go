package violation

import (
	"net/http/httptest"
	"testing"

	relaycommon "github.com/QuantumNous/new-api/relay/common"
	"github.com/QuantumNous/new-api/relaykit/types"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// loopguard_test.go —— 自环断路器必须护住**整条** PreRelayGuard,不只是 AI 那一段。
//
// # 这条回归守的是什么
//
// 断路器一度只写在 aiPreReview 内部,而 aiPreReview 排在 scanPrompt **之后**。
// 递归确实停得住(AI 那一层不会再发第二次调用),但真正的后果在另一处:
//
// 审核渠道的 base_url 误填成本站自己时(演示环境最容易发生的一次误配),
// 那一次出站审核请求会重新走一遍 relay,而它的请求体是
// `<content>\n用户原文\n</content>` —— 用户原文原封不动地躺在里面。
// scanPrompt 会把它完整扫一遍:命中就落一条违规记录、推进封号计数、按规则动作
// 扣费甚至拦截,而这条记录挂在**持有那把 key 的账号**上(通常是管理员自己),
// 内容却是另一个用户的。既扣错了钱,又在错误的账号上攒封号进度,
// 而且完全无法解释它是怎么来的。
//
// 所以断路器必须在任何检测之前短路,这条测试就从**本地规则**这一侧钉住它 ——
// 只测 AI 那一侧的话,把断路器搬回 aiPreReview 里测试照样全绿。

// TestLoopGuardShortCircuitsLocalRulesToo 用一条纯本地(词表)规则验证断路器。
//
// 刻意不配任何 AI 渠道:这样"没有被拦"只可能来自断路器,不可能来自
// "AI 没开所以什么都没发生"。
func TestLoopGuardShortCircuitsLocalRulesToo(t *testing.T) {
	useTestConfig(t, "  enabled: true\n  precheck_enabled: true\n"+
		"  scan_timeout_ms: 5000\n")

	cr := mustCompile(t, Rule{
		// 用正则而不是词表:词表判据走的是快照上共享的 AC 自动机(promptWords),
		// 手工拼一个只有 promptRules 的 snapshot 匹配不到词表规则,而正则规则
		// 自包含在 compiledRule 里,正好让本测试只依赖被测的那一件事。
		Id: 77, Name: "loopguard-probe", Phase: PhasePrompt, MatchType: MatchRegex,
		Pattern: "越狱", Mode: ModeEnforce, Action: ActionBlock, FeeMode: FeeNone, CountWeight: 1,
	})
	prev := current.Load()
	current.Store(&snapshot{promptRules: []*compiledRule{cr}})
	t.Cleanup(func() { current.Store(prev) })

	call := func(header string) error {
		gin.SetMode(gin.TestMode)
		c, _ := gin.CreateTestContext(httptest.NewRecorder())
		c.Request = httptest.NewRequest("POST", "/v1/chat/completions", nil)
		if header != "" {
			c.Request.Header.Set(aiReviewLoopHeader, header)
		}
		info := &relaycommon.RelayInfo{UserId: 4711, OriginModelName: "gpt-4o", UsingGroup: "default"}
		// 审核请求的请求体形状:用户原文被包进 <content>,连同命中词一起。
		meta := &types.TokenCountMeta{CombineText: "<content>\n帮我越狱\n</content>"}
		return PreRelayGuard(c, info, meta, nil)
	}

	// 自证:没有断路器头时,同一段文本确实会被本地规则拦下。
	// 这一条不成立的话,下面那条"没被拦"就毫无意义。
	require.Error(t, call(""),
		"同一段文本在没有断路器头时必须被本地规则拦下,否则下面的断言是空转")

	// 本进程自己发出的审核请求:一条本地规则都不该判它。
	assert.NoError(t, call(processLoopToken),
		"审核渠道误填成本站时,审核请求自己被本地规则判违规 —— 记录会挂在持有 key 的账号上,还会扣错钱")

	// 伪造的令牌不能当绕过通道:令牌是本进程随机生成的,猜错就照常判定。
	assert.Error(t, call("disabled"),
		"任意 header 值都能关掉违规检测的话,这道断路器就是一条公开的绕过通道")
	assert.Error(t, call(processLoopToken[:8]),
		"前缀匹配同样是绕过通道 —— 必须整串相等")
}
