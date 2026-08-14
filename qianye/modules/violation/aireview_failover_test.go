package violation

import (
	"context"
	"net/http"
	"testing"
	"time"

	"github.com/shopspring/decimal"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// aireview_failover_test.go —— 审核渠道的故障转移。
//
// 项目方原话:「加一下故障转移吧。」这一整个文件钉的是那句话的四个边界:
//
//  1. **退到哪、退不退**由这一档自己说了算(ChannelFailover),而不是全站一刀切。
//     默认关 —— 打开它会改变用户内容的出境目的地,那不该由一次二进制升级决定。
//  2. **重试在同一份时间预算里切分**,不是每重试一次就重新计一遍。上一轮修过一个
//     同形缺陷(预算 × 渠道数),这里不许重演。
//  3. **什么算"失败"**分得清:一次正常的「未违规」判定、一个 400,都不该被
//     重试成 N 次付费调用。
//  4. **重试链跑完仍然全败 ⇒ 放行。** 六种失败一律放行这条不变量不因为多了
//     几次尝试就松动。
//
// 假审核服务全部是本地的 httptest —— 绝不用任何真实密钥。

// deadReviewServer 是一个"活着但永远 5xx"的假渠道。
//
// 用 500 而不是一个关掉的端口:两者对被测代码是同一类失败(都可重试),
// 但只有前者数得出**它到底被调了几次** —— 而"指定渠道没开转移时一次都不该
// 落到别人身上"这条断言,判据正是那个次数。
func deadReviewServer(t *testing.T) *fakeReviewServer {
	t.Helper()
	return newFakeReviewServer(t, func(w http.ResponseWriter, _ string) {
		w.WriteHeader(http.StatusBadGateway)
		_, _ = w.Write([]byte(`{"error":"upstream down"}`))
	})
}

// chRT 是渠道的测试夹具。单价固定 $1/$2 每百万,让花费断言能独立算出来。
func chRT(id int64, name, url string, weight int) *aiChannelRT {
	return &aiChannelRT{
		Id: id, Name: name, URL: chatCompletionsURL(url), Model: "m", Weight: weight,
		PriceInPerM: decimal.NewFromFloat(1), PriceOutPerM: decimal.NewFromFloat(2),
	}
}

func rtWith(channels ...*aiChannelRT) *aiRuntime {
	return &aiRuntime{
		MaxInputChars: defaultAIMaxInputChars,
		Prompt:        defaultAIPrompt,
		Vocab:         seedAIVocabulary(),
		Channels:      channels,
	}
}

// ─────────────── 一、重试链是怎么排出来的 ───────────────

// TestPickAIChannelsFailoverChain 钉住三种链的形状。
//
// 断言的是**链本身**而不是最终结局:结局还要过一遍网络与解析,而"指定的渠道
// 有没有被排进链、池子有没有被排进来"是一个纯函数的问题,在这里断言最直接,
// 也最能挡住"加一行回落"这种改动。
func TestPickAIChannelsFailoverChain(t *testing.T) {
	// 四个渠道:池子够大,maxAIAttempts 的上界才是真的被测到了。
	rt := rtWith(
		chRT(1, "指定的", "https://a.invalid/v1", 1),
		chRT(2, "池子甲", "https://b.invalid/v1", 1),
		chRT(3, "池子乙", "https://c.invalid/v1", 1),
		chRT(4, "池子丙", "https://d.invalid/v1", 1),
	)

	tests := []struct {
		name      string
		scope     *aiScopeRT
		wantLen   int
		wantFirst string // 空 = 不要求第一个是谁(加权随机)
		wantNever string // 这个名字一次都不该出现在链上
		why       string
	}{
		{
			name:  "没指定渠道:按权重随机排,取到上界为止",
			scope: &aiScopeRT{ChannelId: 0}, wantLen: maxAIAttempts,
			why: "权重是运营表达主备的唯一方式;恒定顺序会让备用渠道永远不被验证",
		},
		{
			name:  "指定了 + 转移关着:只有它一个,链长恒为 1",
			scope: &aiScopeRT{ChannelId: 1}, wantLen: 1, wantFirst: "指定的",
			why: "这是出厂行为,升级上来的站点必须逐字节不变 —— " +
				"指定渠道往往表达的是数据流向约束,默认回落等于把它悄悄关掉",
		},
		{
			name:  "指定了 + 转移开着:它排第一,后面从其余渠道里补位",
			scope: &aiScopeRT{ChannelId: 1, ChannelFailover: true},
			// 指定的那个占一格,池子补到上界。
			wantLen: maxAIAttempts, wantFirst: "指定的",
			why: "「指定」的含义仍然是优先它,而不是与池子平起平坐地随机",
		},
		{
			name:  "指定的渠道不在快照里 + 转移关着:一个都不返回",
			scope: &aiScopeRT{ChannelId: 404}, wantLen: 0,
			why: "回落会把用户内容发去一个运营明确没有选的端点 —— " +
				"而「只能发给这一个」往往正是指定它的全部理由",
		},
		{
			name:    "指定的渠道不在快照里 + 转移开着:整条链都是池子",
			scope:   &aiScopeRT{ChannelId: 404, ChannelFailover: true},
			wantLen: maxAIAttempts,
			why: "开关的字面意思就是「这一档可以用别的渠道」," +
				"而「已经被停掉」与「刚刚开始超时」对这一档的用户是同一件事",
		},
		{
			name:  "sc 为 nil(没有任何策略命中)也要安全",
			scope: nil, wantLen: maxAIAttempts,
			why: "热路径上一次 nil 解引用就是一次 500,而这条路是 relay 主干",
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := pickAIChannels(rt, tc.scope)
			require.Len(t, got, tc.wantLen, tc.why)
			if tc.wantFirst != "" {
				assert.Equal(t, tc.wantFirst, got[0].Name, tc.why)
			}
			seen := map[int64]bool{}
			for _, ch := range got {
				assert.False(t, seen[ch.Id],
					"同一个渠道不能在链上出现两次 —— 重试到同一个坏掉的端点是纯浪费")
				seen[ch.Id] = true
				if tc.wantNever != "" {
					assert.NotEqual(t, tc.wantNever, ch.Name, tc.why)
				}
			}
		})
	}

	t.Run("指定的渠道不会在后半段被再抽一次", func(t *testing.T) {
		// 补位是从**其余**渠道里抽的。抽回自己等于把一次故障转移浪费在
		// 刚刚失败的那个端点上,而链长是有上界的 —— 浪费掉的就是少一次机会。
		// 摇 64 次:这一条是概率性的,单次通过说明不了任何事。
		for i := 0; i < 64; i++ {
			got := pickAIChannels(rt, &aiScopeRT{ChannelId: 1, ChannelFailover: true})
			require.Len(t, got, maxAIAttempts)
			for _, ch := range got[1:] {
				assert.NotEqual(t, int64(1), ch.Id, "第 %d 次:指定的渠道被重复排进了链", i)
			}
		}
	})

	t.Run("池子只有指定的那一个时,链长就是 1", func(t *testing.T) {
		only := rtWith(chRT(1, "唯一", "https://a.invalid/v1", 1))
		got := pickAIChannels(only, &aiScopeRT{ChannelId: 1, ChannelFailover: true})
		require.Len(t, got, 1, "开了转移也变不出第二个渠道来")
		assert.Equal(t, int64(1), got[0].Id)
	})
}

// ─────────────── 二、端到端:真的换了一个渠道 ───────────────

// TestAIReviewFailoverEndToEnd 是这次改动的主用例。
//
// 每一格都用**真的会失败的本地服务**跑完整条链,断言两件事:最终结局对不对,
// 以及每个假服务各被调了几次 —— 后者才是"到底有没有转移"的判据。
// 只断言结局的话,一个"指定渠道挂了就整条放弃"的实现在「全挂 ⇒ 放行」那一格
// 上同样是绿的。
func TestAIReviewFailoverEndToEnd(t *testing.T) {
	tests := []struct {
		name        string
		failover    bool
		pinGood     bool // 指定的是好渠道还是坏渠道
		wantOutcome string
		wantGood    int64 // 好渠道被调了几次
		wantDead    int64 // 坏渠道被调了几次
		wantAttempt int
		why         string
	}{
		{
			name:     "指定的渠道好使:一次成功,池子一次都不碰",
			failover: true, pinGood: true,
			wantOutcome: OutcomeClean, wantGood: 1, wantDead: 0, wantAttempt: 1,
			why: "开着转移不代表每次都要多打几个 —— 第一个给出结论就收工",
		},
		{
			name:     "指定的渠道挂了 + 转移关着:就此放弃,绝不落到别人身上",
			failover: false, pinGood: false,
			wantOutcome: OutcomeUpstreamError, wantGood: 0, wantDead: 1, wantAttempt: 1,
			why: "这是出厂行为。好渠道一次都不该被调到 —— " +
				"哪怕一次,内容就已经出境到运营没有选的端点了",
		},
		{
			name:     "指定的渠道挂了 + 转移开着:退到池子并拿到结论",
			failover: true, pinGood: false,
			wantOutcome: OutcomeClean, wantGood: 1, wantDead: 1, wantAttempt: 2,
			why: "这就是项目方要的那句「加一下故障转移」",
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			dead := deadReviewServer(t)
			good := newFakeReviewServer(t, func(w http.ResponseWriter, _ string) {
				_, _ = w.Write([]byte(okVerdict(false, "none", 0.1, 100, 20)))
			})
			pin, other := dead, good
			if tc.pinGood {
				pin, other = good, dead
			}
			rt := rtWith(
				chRT(1, "指定的", pin.URL, 1),
				chRT(2, "池子里的", other.URL, 1),
			)
			out := runAIReview(context.Background(), rt,
				&aiScopeRT{ChannelId: 1, ChannelFailover: tc.failover}, "内容", 2000)

			require.NotNil(t, out)
			assert.Equal(t, tc.wantOutcome, out.Outcome, tc.why)
			assert.Equal(t, tc.wantGood, good.calls.Load(), "好渠道的调用次数:%s", tc.why)
			assert.Equal(t, tc.wantDead, dead.calls.Load(), "坏渠道的调用次数:%s", tc.why)
			assert.Equal(t, tc.wantAttempt, out.Attempts,
				"attempts 要如实记下发了几次调用 —— 它是花费那几列的分母")
		})
	}

	t.Run("池子里第一个挂了会试第二个,与随机顺序无关", func(t *testing.T) {
		// 不靠权重去"几乎必然"地排出某个顺序:链长上界是 3、池子只有 2 个,
		// 所以**两个都会被试到**,先后无所谓 —— 结论必须是 clean。
		// 这样这条用例是确定性的,而不是 999:1 那种会偶尔翻车的写法。
		dead := deadReviewServer(t)
		good := newFakeReviewServer(t, func(w http.ResponseWriter, _ string) {
			_, _ = w.Write([]byte(okVerdict(true, "jailbreak", 0.9, 10, 5)))
		})
		rt := rtWith(chRT(1, "坏", dead.URL, 1), chRT(2, "好", good.URL, 1))

		out := runAIReview(context.Background(), rt, nil, "内容", 2000)
		require.NotNil(t, out)
		assert.Equal(t, OutcomeViolation, out.Outcome, "池子里有一个好的就必须拿到结论")
		assert.Equal(t, int64(2), out.ChannelId, "明细上要留下真正给出结论的那个渠道")
		assert.Equal(t, int64(1), good.calls.Load())
	})
}

// ─────────────── 三、重试链跑完仍然全败 ⇒ 放行 ───────────────

// TestFailoverExhaustedStillFailsOpen 是"六种失败一律放行"在有了重试之后的形态。
//
// 加重试最容易破坏的正是这条:多试几次之后,"最后还是没结论"这件事很容易被
// 写成一个 error 往上抛,而上面唯一的处置方式是拦。所以这里断言的不是"没报错",
// 是**最宽的规则也一条都不命中** —— 那才是"请求照常发往上游"的可执行形态。
func TestFailoverExhaustedStillFailsOpen(t *testing.T) {
	// 一条什么都不挑的规则:不限类型、不设置信度下限、真实执行、动作是拦截。
	// 它是最容易命中的形状,所以它不命中就等于谁都不命中。
	widest := mustCompile(t, Rule{
		Id: 1, Name: "ai", Phase: PhasePrompt, MatchType: MatchAIReview,
		Pattern: "", Mode: ModeEnforce, Action: ActionBlock, FeeMode: FeeNone,
	})

	tests := []struct {
		name        string
		handler     func(w http.ResponseWriter, body string)
		scope       *aiScopeRT
		wantOutcome string
	}{
		{
			name: "链上每一个都 5xx",
			handler: func(w http.ResponseWriter, _ string) {
				w.WriteHeader(http.StatusBadGateway)
			},
			scope: nil, wantOutcome: OutcomeUpstreamError,
		},
		{
			name: "链上每一个都 401(密钥全过期)",
			handler: func(w http.ResponseWriter, _ string) {
				w.WriteHeader(http.StatusUnauthorized)
			},
			scope: nil, wantOutcome: OutcomeUpstreamError,
		},
		{
			name: "链上每一个都吐不出结构化结论",
			handler: func(w http.ResponseWriter, _ string) {
				_, _ = w.Write([]byte(`{"choices":[{"message":{"content":"我觉得没问题"}}],` +
					`"usage":{"prompt_tokens":10,"completion_tokens":5,"total_tokens":15}}`))
			},
			scope: nil, wantOutcome: OutcomeBadJSON,
		},
		{
			name: "指定的渠道挂了、开了转移、退过去的也挂了",
			handler: func(w http.ResponseWriter, _ string) {
				w.WriteHeader(http.StatusServiceUnavailable)
			},
			scope:       &aiScopeRT{ChannelId: 1, ChannelFailover: true},
			wantOutcome: OutcomeUpstreamError,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			a := newFakeReviewServer(t, tc.handler)
			b := newFakeReviewServer(t, tc.handler)
			rt := rtWith(chRT(1, "甲", a.URL, 1), chRT(2, "乙", b.URL, 1))

			out := runAIReview(context.Background(), rt, tc.scope, "内容", 2000)
			require.NotNil(t, out, "runAIReview 永不返回 nil —— 调用方没有 nil 分支")
			assert.Equal(t, tc.wantOutcome, out.Outcome)
			assert.False(t, out.decided(),
				"重试链跑完还是没结论 = 未判定 = 放行,绝不是「试了这么多次所以一定有问题」")
			assert.Nil(t, matchAIRule(widest, out),
				"最宽的规则都不该命中 —— 否则一次审核服务全挂就是一次全站 400")
			assert.Equal(t, 2, out.Attempts, "两个渠道都试过了,而这一点要留在明细上")
		})
	}
}

// ─────────────── 四、什么不算"失败":成本不能被重试乘上去 ───────────────

// TestAIReviewRetryClassification 钉住"哪几种结局不该触发下一次调用"。
//
// 分错的代价是不对称的:该重试却没重试,只是漏掉一条被抽中的请求(fail-open
// 本来就接受这个代价);不该重试却重试了,是同一个请求被送去 N 个渠道、
// 每一个都付一次钱 —— 界面上那个"抽样率 10%"对应的账单变成 N 倍,而那个数字
// 没有任何变化。所以这一条用例数的是**假服务被调了几次**,不是结局。
func TestAIReviewRetryClassification(t *testing.T) {
	tests := []struct {
		name        string
		handler     func(w http.ResponseWriter, body string)
		wantOutcome string
		wantCalls   int64
		why         string
	}{
		{
			name: "判定「未违规」:这是成功,不是失败",
			handler: func(w http.ResponseWriter, _ string) {
				_, _ = w.Write([]byte(okVerdict(false, "none", 0.05, 900, 30)))
			},
			wantOutcome: OutcomeClean, wantCalls: 1,
			why: "这是本条最要紧的一格:把一次正常的「没问题」重试成 N 次调用," +
				"等于给绝大多数请求(它们本来就没问题)乘上 N 倍成本",
		},
		{
			name: "判定「违规」同样只调一次",
			handler: func(w http.ResponseWriter, _ string) {
				_, _ = w.Write([]byte(okVerdict(true, "jailbreak", 0.9, 900, 30)))
			},
			wantOutcome: OutcomeViolation, wantCalls: 1,
			why: "有结论就是有结论,再问一个渠道只会让两个答案打架",
		},
		{
			name: "400:请求本身被拒,换个渠道是同一个答案",
			handler: func(w http.ResponseWriter, _ string) {
				w.WriteHeader(http.StatusBadRequest)
				_, _ = w.Write([]byte(`{"error":"unsupported parameter"}`))
			},
			wantOutcome: OutcomeUpstreamError, wantCalls: 1,
			why: "我们发给每个渠道的是同一个请求体 —— 重试是把同一个拒绝买 N 遍",
		},
		{
			name: "413:体积超限,换个渠道也一样超",
			handler: func(w http.ResponseWriter, _ string) {
				w.WriteHeader(http.StatusRequestEntityTooLarge)
			},
			wantOutcome: OutcomeUpstreamError, wantCalls: 1,
			why: "该调的是 max_input_chars,不是再买一次同样的拒绝",
		},
		{
			name: "401:这把钥匙的问题,别的渠道用的是别的钥匙",
			handler: func(w http.ResponseWriter, _ string) {
				w.WriteHeader(http.StatusUnauthorized)
			},
			wantOutcome: OutcomeUpstreamError, wantCalls: 2,
			why: "一把密钥过期不该让整档审核停摆,而这正是故障转移最实的用途之一",
		},
		{
			name: "429:这一家在限流",
			handler: func(w http.ResponseWriter, _ string) {
				w.WriteHeader(http.StatusTooManyRequests)
			},
			wantOutcome: OutcomeUpstreamError, wantCalls: 2,
			why: "限流是渠道侧的状态,换一家就绕开了",
		},
		{
			name: "404:这个渠道的地址填错了(本页最常见的误配)",
			handler: func(w http.ResponseWriter, _ string) {
				w.WriteHeader(http.StatusNotFound)
			},
			wantOutcome: OutcomeUpstreamError, wantCalls: 2,
			why: "别的渠道地址不同,值得试",
		},
		{
			name: "5xx:这一家在抖",
			handler: func(w http.ResponseWriter, _ string) {
				w.WriteHeader(http.StatusInternalServerError)
			},
			wantOutcome: OutcomeUpstreamError, wantCalls: 2,
			why: "换一家有用,而且不产生 token —— 边际成本是零",
		},
		{
			name: "非法 JSON:这个模型不肯按格式回答,换一个模型值得试",
			handler: func(w http.ResponseWriter, _ string) {
				_, _ = w.Write([]byte(`{"choices":[{"message":{"content":"随便说点什么"}}]}`))
			},
			wantOutcome: OutcomeBadJSON, wantCalls: 2,
			why: "它是唯一一种**已经付过钱**的可重试失败 —— " +
				"所以链长上界(maxAIAttempts)真正防的就是它",
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			// 两个渠道都用同一个 handler:这样"调了几次"就是"重试了几次",
			// 与哪个渠道被先抽到无关 —— 用例因此是确定性的。
			a := newFakeReviewServer(t, tc.handler)
			b := newFakeReviewServer(t, tc.handler)
			rt := rtWith(chRT(1, "甲", a.URL, 1), chRT(2, "乙", b.URL, 1))

			out := runAIReview(context.Background(), rt, nil, "内容", 2000)
			require.NotNil(t, out)
			assert.Equal(t, tc.wantOutcome, out.Outcome)
			assert.Equal(t, tc.wantCalls, a.calls.Load()+b.calls.Load(),
				"一共发出去的调用次数:%s", tc.why)
			assert.Equal(t, int(tc.wantCalls), out.Attempts,
				"attempts 必须与真实调用次数一致,否则成本页上的分母是假的")
		})
	}
}

// TestAIStatusRetryable 是上面那张表的纯函数形态。
//
// 单独列一遍是因为端到端只覆盖得了几个代表值,而这个函数的判据是"这一次失败
// 属于渠道还是属于请求"—— 边界(499/500、400/401)必须逐个钉住,
// 否则某天有人顺手把 `>= 500` 改成 `> 500`,502 照样重试、500 悄悄不重试了。
func TestAIStatusRetryable(t *testing.T) {
	tests := []struct {
		status int
		want   bool
		why    string
	}{
		{http.StatusBadRequest, false, "请求本身被拒,换渠道是同一个答案"},
		{http.StatusRequestEntityTooLarge, false, "体积超限,换渠道也一样超"},
		{http.StatusUnprocessableEntity, false, "参数语义不合法,与渠道无关"},
		{http.StatusConflict, false, "其余 4xx 一律按「请求的问题」处理"},
		{http.StatusUnauthorized, true, "这把钥匙的问题"},
		{http.StatusPaymentRequired, true, "这个账号欠费,别的渠道是别的账号"},
		{http.StatusForbidden, true, "这把钥匙没有权限"},
		{http.StatusNotFound, true, "这个渠道的 base_url 填错了"},
		{http.StatusRequestTimeout, true, "这一家慢"},
		{http.StatusTooManyRequests, true, "这一家在限流"},
		{499, false, "4xx 的上沿仍然是「请求的问题」"},
		{http.StatusInternalServerError, true, "5xx 的下沿必须可重试"},
		{http.StatusBadGateway, true, ""},
		{http.StatusServiceUnavailable, true, ""},
		{599, true, "5xx 的上沿"},
	}
	for _, tc := range tests {
		t.Run(http.StatusText(tc.status)+"/"+decimal.NewFromInt(int64(tc.status)).String(), func(t *testing.T) {
			assert.Equal(t, tc.want, aiStatusRetryable(tc.status), tc.why)
		})
	}
}

// ─────────────── 五、重试的花费必须记进去 ───────────────

// TestChainCostCountsEveryAttempt 钉住"抽样率 10% 对应的花费对得上"。
//
// 唯一一种**已经付过钱**的可重试失败是 bad_json:响应回来了、usage 也回来了,
// 只是内容不是我们要的形状。只把最后一次成功的调用写进 qy_violation_ai_review,
// 成本页上的数字就会系统性偏低 —— 而偏低的方向恰好最没人会去核对
// ("这个月比预算省了 40%"没人会来查)。
//
// 期望值是独立算出来的,不是抄实现:
//
//	第一次(bad_json) 输入 900、输出 100 → 900×$1/M + 100×$2/M = $0.0011
//	第二次(clean)    输入 800、输出 200 → 800×$1/M + 200×$2/M = $0.0012
//	合计 1700 输入 / 300 输出 / $0.0023
func TestChainCostCountsEveryAttempt(t *testing.T) {
	badJSON := newFakeReviewServer(t, func(w http.ResponseWriter, _ string) {
		_, _ = w.Write([]byte(`{"choices":[{"message":{"content":"这段内容我觉得还行"}}],` +
			`"usage":{"prompt_tokens":900,"completion_tokens":100,"total_tokens":1000}}`))
	})
	good := newFakeReviewServer(t, func(w http.ResponseWriter, _ string) {
		_, _ = w.Write([]byte(okVerdict(false, "none", 0.1, 800, 200)))
	})
	// 指定 + 转移:链的顺序因此是确定的(先坏后好),花费的期望值才算得死。
	rt := rtWith(chRT(1, "话多的", badJSON.URL, 1), chRT(2, "听话的", good.URL, 1))
	out := runAIReview(context.Background(), rt,
		&aiScopeRT{ChannelId: 1, ChannelFailover: true}, "内容", 3000)

	require.NotNil(t, out)
	require.Equal(t, OutcomeClean, out.Outcome)
	assert.Equal(t, 2, out.Attempts)
	assert.Equal(t, 1700, out.PromptTokens, "两次调用的输入 token 要合计")
	assert.Equal(t, 300, out.CompletionTokens)
	assert.Equal(t, 2000, out.TotalTokens)
	assert.Equal(t, "0.0023", out.CostUsd.String(),
		"$0.0011(那次白花的)+ $0.0012(有结论的)—— 少记前者就是系统性低估")
	assert.True(t, out.Priced)

	t.Run("链上有一个渠道没配单价 ⇒ 合计是下界,priced 必须为假", func(t *testing.T) {
		rt := rtWith(chRT(1, "话多的", badJSON.URL, 1), chRT(2, "听话的", good.URL, 1))
		// 第一个渠道没填单价:它那 1000 token 算不出钱,于是整条链的合计不准。
		rt.Channels[0].PriceInPerM = decimal.Zero
		rt.Channels[0].PriceOutPerM = decimal.Zero

		out := runAIReview(context.Background(), rt,
			&aiScopeRT{ChannelId: 1, ChannelFailover: true}, "内容", 3000)
		require.NotNil(t, out)
		require.Equal(t, OutcomeClean, out.Outcome)
		assert.Equal(t, 2000, out.TotalTokens, "token 照记 —— 那两次调用确实发生了")
		assert.Equal(t, "0.0012", out.CostUsd.String(), "算得出的那一半照算")
		assert.False(t, out.Priced,
			"链上有一次产生了 token 却没单价 ⇒ 这个合计是下界,"+
				"界面必须显示成「单价未配」而不是「一共只花了这么多」")
	})

	t.Run("一次调用都没发出去时 attempts 是 0", func(t *testing.T) {
		out := runAIReview(context.Background(), rtWith(), nil, "内容", 1000)
		require.NotNil(t, out)
		assert.Equal(t, OutcomeNoChannel, out.Outcome)
		assert.Zero(t, out.Attempts, "no_channel 的花费是 0,而且那个 0 是准的")
	})
}

// ─────────────── 六、总预算:重试在里面切,不是每次重新计 ───────────────

// TestAIAttemptBudget 是切分规则的纯函数用例。
//
// 上一轮修过一个同形缺陷:预算曾经是"每个渠道各一份",填 5000 实际最坏 10 秒。
// 加了故障转移之后这条更容易重演 —— 链更长了。所以切分规则本身要有直接的
// 表驱动,而不是只靠端到端的墙钟去兜。
func TestAIAttemptBudget(t *testing.T) {
	tests := []struct {
		name         string
		remaining    int
		attemptsLeft int
		chTimeout    int
		want         int
		why          string
	}{
		{
			name: "只剩一次尝试:剩下的全给它", remaining: 1500, attemptsLeft: 1, want: 1500,
			why: "没有后续尝试要留底,留底就是白扔",
		},
		{
			name: "两次尝试:第一次拿「剩下的减一个最小片」", remaining: 1500, attemptsLeft: 2,
			want: 1500 - minAITimeoutMs,
			why: "留底而不是平分 —— 平分会把一个平时 900ms 出结论的健康渠道" +
				"砍在 750ms 上,拿常态路径的成功率去换异常路径的一次补位",
		},
		{
			name: "三次尝试:留两个最小片", remaining: 1500, attemptsLeft: 3,
			want: 1500 - 2*minAITimeoutMs,
		},
		{
			name: "前一次秒失败之后,后一次几乎拿到全部预算", remaining: 1495, attemptsLeft: 2,
			want: 1495 - minAITimeoutMs,
			why: "值得重试的失败(连不上 / 5xx / 401)都是毫秒级的," +
				"所以常态下这条留底一点都没少给",
		},
		{
			name:      "剩下的还不够留底:夹到最小片,由外层 deadline 兜住上界",
			remaining: 300, attemptsLeft: 3, want: minAITimeoutMs,
			why: "这里只是切分,不是上界 —— callAIChannel 的子 ctx 活不过父 ctx",
		},
		{
			name: "渠道自己的超时更小时取小者", remaining: 1500, attemptsLeft: 1, chTimeout: 50,
			want: 50,
			why:  "那一格的存在理由是「本地小模型 50ms、云端大模型 2s,一个数字卡不住两者」",
		},
		{
			name: "渠道自己的超时更大时不放大预算", remaining: 600, attemptsLeft: 1, chTimeout: 30000,
			want: 600,
			why:  "渠道级超时只能收紧,不能突破总预算 —— 否则它就成了绕过上限的旋钮",
		},
		{
			name:      "attemptsLeft 传了 0(不该发生)按 1 处理,不要算出负数",
			remaining: 800, attemptsLeft: 0, want: 800,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			assert.Equal(t, tc.want,
				aiAttemptBudget(tc.remaining, tc.attemptsLeft, tc.chTimeout), tc.why)
		})
	}
}

// TestFailoverWorstCaseLatencyStaysInBudget 是上面那条规则的墙钟证据。
//
// 三个全部挂死的渠道 + 一份预算。修复前的那种写法(每次尝试各拿一份完整预算)
// 在这里会跑出 3 倍;而**这一条才是运营真正关心的数**,因为 PreTimeoutMs
// 那一格的说明写着"它直接加在被抽中请求的响应延迟上",给的是一个数字。
func TestFailoverWorstCaseLatencyStaysInBudget(t *testing.T) {
	stall := func(w http.ResponseWriter, _ string) {
		time.Sleep(5 * time.Second)
		_, _ = w.Write([]byte(okVerdict(false, "none", 1, 1, 1)))
	}
	a := newFakeReviewServer(t, stall)
	b := newFakeReviewServer(t, stall)
	c := newFakeReviewServer(t, stall)

	const budgetMs = 600
	// 渠道自己也填了超时,而且**比总预算大**。现网就是这个形状(渠道 id=1 的
	// timeout_ms 是 5000),而夹具原先把它留在 0 —— 那样每次尝试都退化到
	// minAITimeoutMs(200ms),三次加起来 600ms,与正确实现的耗时**恰好一样**。
	// 于是这条墙钟守卫在"外层总预算被拆掉"面前是绿的(实测)。
	// 渠道超时大于总预算,才是"总预算能不能压住渠道超时"这个问题的形状。
	const chTimeoutMs = 2500
	// 容差 = 预算 + 一份固定抖动。倍数容差(原先的 2 倍)与"预算被按尝试次数
	// 乘一遍"是同一个量级,挡不住它;本地 httptest 的抖动只有几十毫秒。
	const tolerance = budgetMs*time.Millisecond + 400*time.Millisecond

	rt := rtWith(chRT(1, "甲", a.URL, 1), chRT(2, "乙", b.URL, 1), chRT(3, "丙", c.URL, 1))
	for _, ch := range rt.Channels {
		ch.TimeoutMs = chTimeoutMs
	}

	t.Run("三个渠道全挂死,总耗时仍受同一份预算约束", func(t *testing.T) {
		started := time.Now()
		out := runAIReview(context.Background(), rt, nil, "内容", budgetMs)
		elapsed := time.Since(started)

		require.NotNil(t, out)
		assert.Equal(t, OutcomeTimeout, out.Outcome)
		assert.False(t, out.decided(), "预算耗尽 = 未判定 = 放行")
		assert.Less(t, elapsed, tolerance,
			"总耗时 %v —— 预算被按尝试次数乘了一遍,而管理端文案承诺的是这一个数字", elapsed)
		assert.LessOrEqual(t, out.LatencyMs, int(tolerance/time.Millisecond),
			"落库的延迟也要是真实的整次耗时,否则成本页看不出审核在拖慢谁")
	})

	t.Run("指定 + 转移,链更长了也不放大预算", func(t *testing.T) {
		started := time.Now()
		out := runAIReview(context.Background(), rt,
			&aiScopeRT{ChannelId: 1, ChannelFailover: true}, "内容", budgetMs)
		elapsed := time.Since(started)

		require.NotNil(t, out)
		assert.False(t, out.decided())
		assert.Less(t, elapsed, tolerance,
			"故障转移把链拉长到 %d 个渠道,而这个上界一毫秒都不该动", maxAIAttempts)
	})

	t.Run("挂死的第一个之后,第二个仍然拿得到时间去成功", func(t *testing.T) {
		// 留底那一条的实际用途:第一个渠道**挂起**(不是秒失败)时,
		// 后面的尝试仍然有一个最小可用片。平分或者"全给第一个"都会让这一格红。
		good := newFakeReviewServer(t, func(w http.ResponseWriter, _ string) {
			_, _ = w.Write([]byte(okVerdict(false, "none", 0.1, 10, 5)))
		})
		rt := rtWith(chRT(1, "挂死的", a.URL, 1), chRT(2, "好的", good.URL, 1))
		for _, ch := range rt.Channels {
			ch.TimeoutMs = chTimeoutMs
		}
		out := runAIReview(context.Background(), rt,
			&aiScopeRT{ChannelId: 1, ChannelFailover: true}, "内容", 1200)

		require.NotNil(t, out)
		assert.Equal(t, OutcomeClean, out.Outcome,
			"第一个渠道吃光预算的实现会在这里超时 —— 那时故障转移在最需要它的"+
				"那一种故障(上游挂起)上恰好不发生")
		assert.Equal(t, int64(2), out.ChannelId)
	})
}

// ─────────────── 七、这一位真的从库里走到了热路径 ───────────────

// TestBuildAIScopesCarriesChannelFailover 钉住这一列进了快照。
//
// 少了这一步:库里存着"开",汇总表显示"开",而热路径读到的是零值 ——
// 于是故障转移在界面上是开着的、线上一次都不发生。这正是本模块反复出现的
// "保存成功、界面正常、线上不是那么回事",而它的症状只是"审核又放行了"。
func TestBuildAIScopesCarriesChannelFailover(t *testing.T) {
	gdb := newAIWiringDB(t)
	require.NoError(t, gdb.Create(&AIScope{
		Id: 1, Name: "自助注册", Enabled: true, Priority: 10,
		GroupScope: "selfserve", GroupScopeMode: GroupScopeInclude,
		AsyncSampleRateBps: 5000, ChannelId: 3, ChannelFailover: true,
	}).Error)
	scopes, err := buildAIScopes(gdb)
	require.NoError(t, err)
	require.Len(t, scopes, 1)
	assert.Equal(t, int64(3), scopes[0].ChannelId)
	assert.True(t, scopes[0].ChannelFailover)
}

// TestValidateAIScopeChannelFailoverNeedsAChannel 钉住"没指定渠道时这一位归零"。
//
// 归零而不是报错:没指定渠道时本来就走加权随机池,这一位开着与关着的**运行期
// 行为逐字节相同**,归零改的只是显示形态。留着一个悬空的 true,列表上会出现
// 「按权重随机 · 故障转移: 开」这种自相矛盾的一格,而运营会据此以为自己配了
// 点什么。报错则更糟:表单上这一格在"不指定"时是隐藏的,一次「把指定渠道改回
// 不指定」的正常保存会莫名其妙 400。
func TestValidateAIScopeChannelFailoverNeedsAChannel(t *testing.T) {
	tests := []struct {
		name         string
		channelId    int64
		failover     bool
		wantFailover bool
	}{
		{"指定了渠道 + 开转移:原样留着", 7, true, true},
		{"指定了渠道 + 不开转移:原样留着", 7, false, false},
		{"没指定渠道 + 开转移:归零(它没有任何含义)", 0, true, false},
		{"没指定渠道 + 不开转移:本来就是零", 0, false, false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			row := AIScope{
				Name: "自助注册", GroupScope: "selfserve",
				GroupScopeMode: GroupScopeInclude, AsyncSampleRateBps: 1000,
				ChannelId: tc.channelId, ChannelFailover: tc.failover,
			}
			require.NoError(t, validateAIScope(&row))
			assert.Equal(t, tc.wantFailover, row.ChannelFailover)
		})
	}
}

// TestSummarizeAIScopesShowsFailover 钉住这一位出现在管理端列表上。
//
// 它改变的是**用户内容会被发到哪些第三方端点**:运营看到「审核渠道: 内部自建」
// 时的默认理解是"只有它",而开着这一位时那句话是假的 —— 一次超时就足以让
// 内容去到别处。这个预期不能只在编辑弹窗里才纠正得过来。
func TestSummarizeAIScopesShowsFailover(t *testing.T) {
	rows := []AIScope{
		{Id: 1, Name: "指定+转移", Enabled: true, GroupScope: "a",
			GroupScopeMode: GroupScopeInclude, ChannelId: 5, ChannelFailover: true},
		{Id: 2, Name: "指定不转移", Enabled: true, GroupScope: "b",
			GroupScopeMode: GroupScopeInclude, ChannelId: 5},
		// 存量里可能躺着这一行:channel_id=0 而 channel_failover=1
		// (这一列刚加、写入闸之前存下来的)。照原样下发会让列表画出一个
		// 不存在的状态,所以汇总要与 validateAIScope 的归一同口径。
		{Id: 3, Name: "没指定却开着", Enabled: true, GroupScope: "c",
			GroupScopeMode: GroupScopeInclude, ChannelId: 0, ChannelFailover: true},
	}
	got := summarizeAIScopes(rows)
	require.Len(t, got, 3)
	assert.True(t, got[0].ChannelFailover)
	assert.False(t, got[1].ChannelFailover)
	assert.False(t, got[2].ChannelFailover,
		"没指定渠道时恒假 —— 「按权重随机 · 故障转移: 开」不是一个存在的状态")
}
