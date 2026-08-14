package violation

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/QuantumNous/new-api/qianye/config"

	"github.com/shopspring/decimal"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// aireview_test.go —— AI 审核的边界与失败方向。
//
// 每一条断言对应项目方点名要回答的一个问题:超时怎么办、非法 JSON 怎么办、
// 渠道全挂怎么办、概率 0/100 分别怎么走、命中之后落不落到违规类型上。
//
// 这里**不做**假 fuzz、不 sleep 等待、不断言日志。超时用的是一个真的会卡住的
// 本地服务器加一个 200ms 的预算 —— 那是被测行为本身,不是为了"让测试慢一点"。

// fakeReviewServer 是一个本地的假审核服务。
//
// 它存在的全部理由是:**绝不能用真实的 DeepSeek key 跑测试**。
// 同时它让"上游返回了什么"成为测试的输入,而那正是失败方向要覆盖的维度。
type fakeReviewServer struct {
	*httptest.Server
	calls    atomic.Int64
	lastBody atomic.Pointer[string]
	lastAuth atomic.Pointer[string]
	lastLoop atomic.Pointer[string]
}

// newFakeReviewServer 起一个按 handler 决定响应的假审核服务。
func newFakeReviewServer(t *testing.T, handler func(w http.ResponseWriter, body string)) *fakeReviewServer {
	t.Helper()
	f := &fakeReviewServer{}
	f.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		f.calls.Add(1)
		raw, _ := io.ReadAll(r.Body)
		body := string(raw)
		auth := r.Header.Get("Authorization")
		loop := r.Header.Get(aiReviewLoopHeader)
		f.lastBody.Store(&body)
		f.lastAuth.Store(&auth)
		f.lastLoop.Store(&loop)
		handler(w, body)
	}))
	t.Cleanup(f.Close)
	return f
}

// okVerdict 生成一条格式正确的审核回复。
func okVerdict(violation bool, category string, confidence float64, promptTok, compTok int) string {
	content := fmt.Sprintf(`{"violation":%t,"category":%q,"confidence":%v,"reason":"测试"}`,
		violation, category, confidence)
	return fmt.Sprintf(`{"choices":[{"message":{"content":%q}}],"usage":{"prompt_tokens":%d,"completion_tokens":%d,"total_tokens":%d}}`,
		content, promptTok, compTok, promptTok+compTok)
}

func rtForServer(url string, timeoutMs int) *aiRuntime {
	return &aiRuntime{
		PreTimeoutMs: timeoutMs, AsyncTimeoutMs: timeoutMs,
		MaxInputChars: defaultAIMaxInputChars,
		// 一条覆盖全部分组、两个时机都 100% 的策略。
		//
		// 全局抽样率与兜底档已经下线,"送不送审"只由策略表回答,所以这个夹具
		// 必须自带一条 —— 否则每一条用例都会在作用域闸那里就返回,
		// 断言"发了几次调用"全都变成恒真的 0。
		Scopes: []*aiScopeRT{scopeRT("全站", "", "", GroupScopeInclude, 10000, 10000)},
		// 闭集必须是**出厂那一份**,不能留零值:零值闭集会把每一个类型都判成
		// "清单外"并折进兜底,于是这一批用例断言的类型全都变成 uncategorized ——
		// 测出来的是归一兜底,不是它们本来要测的那件事。
		Vocab: seedAIVocabulary(),
		Channels: []*aiChannelRT{{
			Id: 1, Name: "fake", URL: chatCompletionsURL(url), Model: "fake-model",
			APIKey: "test-not-a-real-key", Weight: 1,
			PriceInPerM: decimal.NewFromFloat(1), PriceOutPerM: decimal.NewFromFloat(2),
		}},
	}
}

// ─────────────────────── 抽样:0 与 100 两个边界 ───────────────────────

// TestSampleAIBoundaries 钉住抽样率的两端。
//
// 0 必须**永不**抽中(项目方要求"概率为 0 时整条路径零开销"),
// 10000 必须**永远**抽中 —— 中间值是随机的、不该在测试里断言具体比例
// (那会变成一个时灵时不灵的用例),只断言它落在开区间里。
func TestSampleAIBoundaries(t *testing.T) {
	tests := []struct {
		name string
		bps  int
		want *bool
	}{
		{"零抽样率永不送审", 0, boolPtr(false)},
		{"负数按零处理", -1, boolPtr(false)},
		{"满抽样率必定送审", 10000, boolPtr(true)},
		{"超过满值仍按满值", 20000, boolPtr(true)},
		{"中间值不做比例断言", 5000, nil},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if tc.want == nil {
				// 只验证它不会 panic、两种结果都合法。
				_ = sampleAI(tc.bps)
				return
			}
			// 多摇几次:边界值的语义是"确定性",一次通过可能是巧合。
			for i := 0; i < 64; i++ {
				assert.Equal(t, *tc.want, sampleAI(tc.bps), "第 %d 次抽样与边界语义不符", i)
			}
		})
	}
}

func boolPtr(b bool) *bool { return &b }

// ─────────────────────── 失败方向:每一种都必须放行 ───────────────────────

// TestAIReviewFailureDirections 是本文件最重要的一条。
//
// 项目方要求逐条回答"超时 / 非法 JSON / 5xx / 渠道全挂各自怎么办"。答案统一是
// **放行**,而"放行"在代码里的表达是 `decided() == false` —— 只要它为真,
// matchAIRule 就永远返回 nil,规则永远不命中,请求照常发往上游。
//
// 所以这里同时断言两件事:结局分类对不对(排障要用),以及 decided 是不是 false。
func TestAIReviewFailureDirections(t *testing.T) {
	tests := []struct {
		name        string
		handler     func(w http.ResponseWriter, body string)
		timeoutMs   int
		wantOutcome string
	}{
		{
			name: "上游 500 → upstream_error,放行",
			handler: func(w http.ResponseWriter, _ string) {
				w.WriteHeader(http.StatusInternalServerError)
				_, _ = w.Write([]byte(`{"error":"boom"}`))
			},
			timeoutMs: 2000, wantOutcome: OutcomeUpstreamError,
		},
		{
			name: "上游 401 → upstream_error,放行(密钥过期不该拦住全站)",
			handler: func(w http.ResponseWriter, _ string) {
				w.WriteHeader(http.StatusUnauthorized)
			},
			timeoutMs: 2000, wantOutcome: OutcomeUpstreamError,
		},
		{
			name: "响应不是 JSON → bad_json,放行",
			handler: func(w http.ResponseWriter, _ string) {
				_, _ = w.Write([]byte(`<html>502 Bad Gateway</html>`))
			},
			timeoutMs: 2000, wantOutcome: OutcomeBadJSON,
		},
		{
			name: "结论不是 JSON → bad_json,放行",
			handler: func(w http.ResponseWriter, _ string) {
				_, _ = w.Write([]byte(`{"choices":[{"message":{"content":"我觉得这段内容是违规的"}}]}`))
			},
			timeoutMs: 2000, wantOutcome: OutcomeBadJSON,
		},
		{
			name: "结论缺 violation 字段 → bad_json,放行",
			handler: func(w http.ResponseWriter, _ string) {
				_, _ = w.Write([]byte(`{"choices":[{"message":{"content":"{\"category\":\"sexual\"}"}}]}`))
			},
			timeoutMs: 2000, wantOutcome: OutcomeBadJSON,
		},
		{
			name: "没有 choices → bad_json,放行",
			handler: func(w http.ResponseWriter, _ string) {
				_, _ = w.Write([]byte(`{"choices":[]}`))
			},
			timeoutMs: 2000, wantOutcome: OutcomeBadJSON,
		},
		{
			name: "上游卡住超过预算 → timeout,放行",
			handler: func(w http.ResponseWriter, _ string) {
				// 预算是 200ms,这里睡 2s:被测的就是"预算到了就走人"。
				time.Sleep(2 * time.Second)
				_, _ = w.Write([]byte(okVerdict(true, "sexual", 0.99, 10, 5)))
			},
			timeoutMs: minAITimeoutMs, wantOutcome: OutcomeTimeout,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			srv := newFakeReviewServer(t, tc.handler)
			out := runAIReview(context.Background(), rtForServer(srv.URL, tc.timeoutMs), nil, "任意内容", tc.timeoutMs)

			require.NotNil(t, out, "runAIReview 永不返回 nil —— 调用方没有 nil 分支")
			assert.Equal(t, tc.wantOutcome, out.Outcome)
			assert.False(t, out.decided(), "失败结局必须让 decided 为 false,否则规则可能命中并拦住请求")

			// 变异验证的落点:哪怕规则配得再宽(不限类型、不设置信度下限),
			// 失败结局也一条都不该命中。
			cr := &compiledRule{R: Rule{MatchType: MatchAIReview}}
			assert.Nil(t, matchAIRule(cr, out), "失败结局绝不能命中任何 ai_review 规则")
		})
	}
}

// TestAIReviewAllChannelsDown 回答"抽到了但所有审核渠道都不可用 → 放行还是拒绝"。
//
// 答案是**放行**。两种"都不可用"要分开:一个渠道都没配(no_channel),
// 与配了但全都连不上(upstream_error)。两者的处置人不同,所以结局不能合并,
// 但方向必须一致 —— 都不拦。
func TestAIReviewAllChannelsDown(t *testing.T) {
	t.Run("一个渠道都没有", func(t *testing.T) {
		out := runAIReview(context.Background(), &aiRuntime{}, nil, "内容", 1000)
		require.NotNil(t, out)
		assert.Equal(t, OutcomeNoChannel, out.Outcome)
		assert.False(t, out.decided())
	})

	t.Run("配了两个渠道但全都连不上", func(t *testing.T) {
		// 起一个立刻关掉的服务器,拿到一个确定不通的地址 —— 比硬编码端口可靠。
		dead := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
		deadURL := dead.URL
		dead.Close()

		rt := rtForServer(deadURL, 1000)
		rt.Channels = append(rt.Channels, &aiChannelRT{
			Id: 2, Name: "fake2", URL: chatCompletionsURL(deadURL), Model: "m", Weight: 1,
		})

		out := runAIReview(context.Background(), rt, nil, "内容", 1000)
		require.NotNil(t, out)
		assert.Equal(t, OutcomeUpstreamError, out.Outcome)
		assert.False(t, out.decided(), "渠道全挂必须放行 —— 审核服务的可用性一定低于网关自身")
	})

	t.Run("第一个渠道挂了会自动落到第二个", func(t *testing.T) {
		dead := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
		deadURL := dead.URL
		dead.Close()
		good := newFakeReviewServer(t, func(w http.ResponseWriter, _ string) {
			_, _ = w.Write([]byte(okVerdict(false, "none", 0.1, 20, 6)))
		})

		// 权重全压在死渠道上,让它几乎必然被先抽中。
		rt := &aiRuntime{
			MaxInputChars: defaultAIMaxInputChars,
			Channels: []*aiChannelRT{
				{Id: 1, Name: "dead", URL: chatCompletionsURL(deadURL), Model: "m", Weight: 999},
				{Id: 2, Name: "good", URL: chatCompletionsURL(good.URL), Model: "m", Weight: 1},
			},
		}
		out := runAIReview(context.Background(), rt, nil, "内容", 2000)
		require.NotNil(t, out)
		assert.Equal(t, OutcomeClean, out.Outcome, "第一个渠道失败后必须落到第二个")
		assert.True(t, out.decided())
	})
}

// ─────────────────────── 成功路径与成本 ───────────────────────

// TestAIReviewSuccessAndCost 断言结构化结论被正确解析,并且 token 与花费算得准。
//
// 花费是独立算出来的期望值,不是抄实现:
// 输入 1000 token × $1/M = $0.001,输出 500 token × $2/M = $0.001,合计 $0.002。
func TestAIReviewSuccessAndCost(t *testing.T) {
	srv := newFakeReviewServer(t, func(w http.ResponseWriter, _ string) {
		_, _ = w.Write([]byte(okVerdict(true, "jailbreak", 0.93, 1000, 500)))
	})
	out := runAIReview(context.Background(), rtForServer(srv.URL, 2000), nil, "越狱样本", 2000)

	require.NotNil(t, out)
	assert.Equal(t, OutcomeViolation, out.Outcome)
	assert.True(t, out.Violated)
	assert.Equal(t, "jailbreak", out.Category)
	assert.Equal(t, "0.93", out.Confidence.String())
	assert.Equal(t, 1000, out.PromptTokens)
	assert.Equal(t, 500, out.CompletionTokens)
	assert.Equal(t, 1500, out.TotalTokens)
	assert.True(t, out.Priced)
	assert.Equal(t, "0.002", out.CostUsd.String(), "1000×$1/M + 500×$2/M = $0.002")

	t.Run("密钥进 Authorization 头而不是请求体", func(t *testing.T) {
		auth := srv.lastAuth.Load()
		require.NotNil(t, auth)
		assert.Equal(t, "Bearer test-not-a-real-key", *auth)
		body := srv.lastBody.Load()
		require.NotNil(t, body)
		assert.NotContains(t, *body, "test-not-a-real-key", "密钥绝不能出现在请求体里")
	})

	t.Run("出站请求带自环断路器令牌", func(t *testing.T) {
		loop := srv.lastLoop.Load()
		require.NotNil(t, loop)
		assert.Equal(t, processLoopToken, *loop,
			"没有这个头,把审核地址填成本站就会无限递归 —— 每一层都在花钱")
	})

	t.Run("待审内容被包进 content 标签", func(t *testing.T) {
		body := srv.lastBody.Load()
		require.NotNil(t, body)
		// 断言解析后的形态,不是字节形态:JSON 编码会把 `<` 转义成 <
		// (两者对接收方等价)。按字节断言等于把序列化器的转义策略也钉死,
		// 换一个 JSON 库就会红,而那与被测行为无关。
		var parsed chatRequest
		require.NoError(t, json.Unmarshal([]byte(*body), &parsed))
		require.Len(t, parsed.Messages, 2)
		assert.Equal(t, "system", parsed.Messages[0].Role)
		assert.Contains(t, parsed.Messages[1].Content, "<content>")
		assert.Contains(t, parsed.Messages[1].Content, "越狱样本")

		// temperature 必须真的发出去且为 0:漏掉它(非指针 + omitempty 的经典坑)
		// 会换来一个每次结论都可能不同的审核员。
		require.NotNil(t, parsed.Temperature)
		assert.Equal(t, 0.0, *parsed.Temperature)
		require.NotNil(t, parsed.ResponseFormat)
		assert.Equal(t, "json_object", parsed.ResponseFormat.Type)
	})
}

// TestAICostUnpricedChannelStaysZero 钉住"没填单价 = 算不出花费",而不是"没花钱"。
func TestAICostUnpricedChannelStaysZero(t *testing.T) {
	srv := newFakeReviewServer(t, func(w http.ResponseWriter, _ string) {
		_, _ = w.Write([]byte(okVerdict(false, "none", 0.05, 900, 100)))
	})
	rt := rtForServer(srv.URL, 2000)
	rt.Channels[0].PriceInPerM = decimal.Zero
	rt.Channels[0].PriceOutPerM = decimal.Zero

	out := runAIReview(context.Background(), rt, nil, "正常内容", 2000)
	require.NotNil(t, out)
	assert.Equal(t, 1000, out.TotalTokens, "token 必须照记 —— 那次调用确实花了钱")
	assert.Equal(t, "0", out.CostUsd.String())
	assert.False(t, out.Priced, "priced=false 才能让界面把 $0 显示成「单价未配」而不是「没花钱」")
}

// TestParseAIVerdictTolerance 覆盖模型不听话的几种常见吐法。
//
// 容错的边界也要钉住:能救的救回来,救不回来的必须报 bad_json 而不是猜一个
// 「未违规」—— 猜出来的结论会被当成真的,而它的方向恰好是"这次没事"。
func TestParseAIVerdictTolerance(t *testing.T) {
	tests := []struct {
		name     string
		content  string
		wantErr  bool
		wantViol bool
		wantCat  string
	}{
		{"裸 JSON", `{"violation":true,"category":"hate","confidence":0.8}`, false, true, "hate"},
		{"带 json 代码块围栏", "```json\n{\"violation\":false,\"category\":\"none\"}\n```", false, false, "none"},
		{"带无语言代码块围栏", "```\n{\"violation\":true,\"category\":\"spam\"}\n```", false, true, "spam"},
		{"JSON 前后有废话", `好的,结论是:{"violation":true,"category":"malware"} 以上。`, false, true, "malware"},
		{"空回复", "   ", true, false, ""},
		{"纯文本", "这段内容看起来没有问题", true, false, ""},
		{"缺 violation 字段", `{"category":"sexual","confidence":0.9}`, true, false, ""},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			v, err := parseAIVerdict(tc.content)
			if tc.wantErr {
				require.Error(t, err, "解析不出结构化结论时必须报错,绝不能猜一个「未违规」")
				return
			}
			require.NoError(t, err)
			require.NotNil(t, v.Violation)
			assert.Equal(t, tc.wantViol, *v.Violation)
			// 断言的是**解析出来的原值**,不再过一遍归一:归一现在要一份
			// 类型闭集(见 aiVocabulary.resolveCategory,它有自己的表驱动用例),
			// 在这里顺带测它只会让"解析容错"与"闭集归一"两件事纠缠在一起。
			assert.Equal(t, tc.wantCat, v.Category)
		})
	}
}

// TestClampConfidence 钉住置信度归一。85 是模型把单位当成百分比时的典型输出。
func TestClampConfidence(t *testing.T) {
	tests := []struct {
		in   float64
		want string
	}{
		{0, "0"}, {0.5, "0.5"}, {1, "1"},
		{85, "1"},           // 模型误用百分比,夹到上界
		{-3, "0"},           // 负数夹到下界
		{0.12345, "0.1235"}, // 四位小数,与 decimal(5,4) 列宽一致
	}
	for _, tc := range tests {
		t.Run(fmt.Sprint(tc.in), func(t *testing.T) {
			assert.Equal(t, tc.want, clampConfidence(tc.in).String())
		})
	}
}

// TestChatCompletionsURL 钉住三种 base_url 写法。
//
// 这一格是本页最容易填错的地方,而填错的表现是 404 → fail-open →
// 「审核开着但一次都没生效」。三种写法在各家文档里都出现过。
func TestChatCompletionsURL(t *testing.T) {
	tests := []struct{ in, want string }{
		{"https://api.deepseek.com", "https://api.deepseek.com/chat/completions"},
		{"https://api.deepseek.com/v1", "https://api.deepseek.com/v1/chat/completions"},
		{"https://api.deepseek.com/v1/", "https://api.deepseek.com/v1/chat/completions"},
		{"https://api.deepseek.com/v1/chat/completions", "https://api.deepseek.com/v1/chat/completions"},
		{"  ", ""},
	}
	for _, tc := range tests {
		t.Run(tc.in, func(t *testing.T) {
			assert.Equal(t, tc.want, chatCompletionsURL(tc.in))
		})
	}
}

// ─────────────────────── 规则匹配:类型与置信度 ───────────────────────

// TestMatchAIRule 是"命中之后落到违规类型上"那一半的判据。
//
// 每一行的失败方向都是不命中(放行),这与 matchAIRule 的注释是同一份契约。
func TestMatchAIRule(t *testing.T) {
	tests := []struct {
		name    string
		pattern string
		minConf string
		out     *aiOutcome
		wantHit bool
	}{
		{
			name:    "不限类型 + 判违规 → 命中",
			out:     &aiOutcome{Outcome: OutcomeViolation, Violated: true, Category: "sexual", Confidence: decimal.NewFromFloat(0.9)},
			wantHit: true,
		},
		{
			name:    "判未违规 → 不命中",
			out:     &aiOutcome{Outcome: OutcomeClean, Violated: false, Category: "none"},
			wantHit: false,
		},
		{
			name:    "类型在白名单里 → 命中",
			pattern: "sexual,jailbreak",
			out:     &aiOutcome{Outcome: OutcomeViolation, Violated: true, Category: "jailbreak", Confidence: decimal.NewFromFloat(0.7)},
			wantHit: true,
		},
		{
			name:    "类型不在白名单里 → 不命中(这条规则管的不是这一类)",
			pattern: "sexual",
			out:     &aiOutcome{Outcome: OutcomeViolation, Violated: true, Category: "spam", Confidence: decimal.NewFromFloat(0.99)},
			wantHit: false,
		},
		{
			name:    "置信度刚好等于下限 → 命中(下限是「已达」不是「超过」)",
			minConf: "0.8",
			out:     &aiOutcome{Outcome: OutcomeViolation, Violated: true, Category: "hate", Confidence: decimal.NewFromFloat(0.8)},
			wantHit: true,
		},
		{
			name:    "置信度低于下限 → 不命中",
			minConf: "0.8",
			out:     &aiOutcome{Outcome: OutcomeViolation, Violated: true, Category: "hate", Confidence: decimal.NewFromFloat(0.79)},
			wantHit: false,
		},
		{
			name:    "下限为 0 表示不设限,低置信度也命中",
			minConf: "0",
			out:     &aiOutcome{Outcome: OutcomeViolation, Violated: true, Category: "hate", Confidence: decimal.NewFromFloat(0.01)},
			wantHit: true,
		},
		{
			name:    "超时的结论 → 不命中",
			out:     &aiOutcome{Outcome: OutcomeTimeout, Violated: true, Category: "sexual", Confidence: decimal.NewFromFloat(1)},
			wantHit: false,
		},
		{
			name:    "没有结论(没抽中)→ 不命中",
			out:     nil,
			wantHit: false,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			minConf := decimal.Zero
			if tc.minConf != "" {
				var err error
				minConf, err = decimal.NewFromString(tc.minConf)
				require.NoError(t, err)
			}
			cr, err := compile(Rule{
				MatchType: MatchAIReview, Pattern: tc.pattern, AIMinConfidence: minConf,
			})
			require.NoError(t, err)

			got := matchAIRule(cr, tc.out)
			assert.Equal(t, tc.wantHit, len(got) > 0)
			if tc.wantHit {
				assert.Contains(t, got[0], tc.out.Category,
					"命中词必须带上类型与置信度,否则每条记录长得一样、分不出「刚过线」和「非常确定」")
			}
		})
	}
}

// TestAICategoryFilterIsCaseInsensitiveOnBothSides 是"保存成功却永不命中"那一类
// 缺陷的直接回归。本仓已经在 groupname 上栽过两次同形的坑。
func TestAICategoryFilterIsCaseInsensitiveOnBothSides(t *testing.T) {
	cr, err := compile(Rule{MatchType: MatchAIReview, Pattern: "SEXUAL, JailBreak"})
	require.NoError(t, err)

	vocab := seedAIVocabulary()
	out := &aiOutcome{Outcome: OutcomeViolation, Violated: true,
		Category: vocab.resolveCategory("JAILBREAK", true).Key, Confidence: decimal.NewFromFloat(0.9)}
	assert.NotEmpty(t, matchAIRule(cr, out),
		"管理端填大写、模型回大写,两侧都要归一 —— 否则这条规则保存成功、界面正常、线上永不命中")
}

// ─────────────────────── 校验:每一条都挡一种静默失效 ───────────────────────

func TestValidateAIRule(t *testing.T) {
	tests := []struct {
		name    string
		rule    Rule
		wantErr string
	}{
		{
			name: "转发前 + ai_review + block 合法",
			rule: Rule{Phase: PhasePrompt, MatchType: MatchAIReview, Action: ActionBlock},
		},
		{
			name: "转发后异步 + 只记录 合法",
			rule: Rule{Phase: PhasePostAsync, MatchType: MatchAIReview, Action: ActionRecord},
		},
		{
			name:    "转发后异步 + 扣费 被拒(异步拿不到计费上下文)",
			rule:    Rule{Phase: PhasePostAsync, MatchType: MatchAIReview, Action: ActionCharge},
			wantErr: "只支持 action",
		},
		{
			name:    "转发后异步 + 关键词 被拒(本地就能算,推迟只会失去拦截能力)",
			rule:    Rule{Phase: PhasePostAsync, MatchType: MatchKeyword, Action: ActionRecord},
			wantErr: "只支持 match_type",
		},
		{
			name:    "ai_review 挂在上游错误阶段 被拒",
			rule:    Rule{Phase: PhaseUpstreamErr, MatchType: MatchAIReview, Action: ActionRecord},
			wantErr: "只能用于",
		},
		{
			name:    "置信度下限超过 1 被拒",
			rule:    Rule{Phase: PhasePrompt, MatchType: MatchAIReview, Action: ActionRecord, AIMinConfidence: decimal.NewFromInt(2)},
			wantErr: "0..1",
		},
		{
			name:    "非 AI 规则填置信度下限 被拒",
			rule:    Rule{Phase: PhasePrompt, MatchType: MatchKeyword, Action: ActionRecord, AIMinConfidence: decimal.NewFromFloat(0.5)},
			wantErr: "只对 match_type",
		},
		{
			name:    "类型名带空格 被拒(它会让规则永不命中)",
			rule:    Rule{Phase: PhasePrompt, MatchType: MatchAIReview, Action: ActionRecord, Pattern: "self harm"},
			wantErr: "非法字符",
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			r := tc.rule
			err := validateAIRule(&r)
			if tc.wantErr == "" {
				assert.NoError(t, err)
				return
			}
			require.Error(t, err)
			assert.Contains(t, err.Error(), tc.wantErr)
		})
	}
}

func TestValidateAISetting(t *testing.T) {
	base := AISetting{
		PreTimeoutMs: 1500, AsyncTimeoutMs: 8000,
		MaxInputChars: defaultAIMaxInputChars,
	}
	tests := []struct {
		name    string
		mutate  func(*AISetting)
		wantErr string
	}{
		{name: "默认值合法", mutate: func(*AISetting) {}},
		{
			name:    "转发前超时超过硬上限被拒(它直接加在用户的延迟上)",
			mutate:  func(s *AISetting) { s.PreTimeoutMs = maxPreTimeoutMs + 1 },
			wantErr: "转发前审核超时",
		},
		{
			name:    "转发前超时低于下限被拒(填 10ms 等于永远超时、永远放行)",
			mutate:  func(s *AISetting) { s.PreTimeoutMs = 10 },
			wantErr: "转发前审核超时",
		},
		{
			name:    "开启但未确认内容出境 被拒",
			mutate:  func(s *AISetting) { s.Enabled = true },
			wantErr: "第三方审核服务",
		},
		{
			name:   "开启且已确认 合法",
			mutate: func(s *AISetting) { s.Enabled = true; s.ThirdPartyNoticeAck = true },
		},
		{
			name:    "提示词过长被拒",
			mutate:  func(s *AISetting) { s.Prompt = strings.Repeat("字", maxAIPromptRunes+1) },
			wantErr: "提示词过长",
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			s := base
			tc.mutate(&s)
			err := validateAISetting(&s)
			if tc.wantErr == "" {
				assert.NoError(t, err)
				return
			}
			require.Error(t, err)
			assert.Contains(t, err.Error(), tc.wantErr)
		})
	}
}

func TestValidateAIChannel(t *testing.T) {
	base := AIChannel{Name: "deepseek", BaseUrl: "https://api.deepseek.com/v1",
		Model: "deepseek-v4-flash", Weight: 1}
	tests := []struct {
		name    string
		mutate  func(*AIChannel)
		wantErr string
	}{
		{name: "基本配置合法", mutate: func(*AIChannel) {}},
		{name: "地址不是 http(s) 被拒", mutate: func(c *AIChannel) { c.BaseUrl = "file:///etc/passwd" }, wantErr: "http"},
		{name: "地址为空被拒", mutate: func(c *AIChannel) { c.BaseUrl = "" }, wantErr: "http"},
		{name: "模型名为空被拒", mutate: func(c *AIChannel) { c.Model = "" }, wantErr: "模型名"},
		{name: "名称为空被拒", mutate: func(c *AIChannel) { c.Name = "" }, wantErr: "名称"},
		{name: "权重 0 被拒(它会让渠道永远抽不中却仍显示已启用)", mutate: func(c *AIChannel) { c.Weight = 0 }, wantErr: "权重"},
		{name: "负单价被拒", mutate: func(c *AIChannel) { c.PriceInPerM = decimal.NewFromInt(-1) }, wantErr: "单价"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			ch := base
			tc.mutate(&ch)
			err := validateAIChannel(&ch)
			if tc.wantErr == "" {
				assert.NoError(t, err)
				return
			}
			require.Error(t, err)
			assert.Contains(t, err.Error(), tc.wantErr)
		})
	}
}

// ─────────────────────── 密钥:加密、掩码、绝不回显 ───────────────────────

// useAIReviewKey 给本次测试装一把 AES 密钥。
//
// 这里用的是一段**测试专用的常量**,不是任何真实密钥,也不会被写进任何
// 示例配置或部署文件。
func useAIReviewKey(t *testing.T) {
	t.Helper()
	key := base64.StdEncoding.EncodeToString([]byte("0123456789abcdef0123456789abcdef"))
	path := filepath.Join(t.TempDir(), "qianye.yaml")
	yaml := "enabled: true\n" +
		"database:\n  dsn: \"u:p@tcp(127.0.0.1:3306)/qy_test\"\n" +
		"violation:\n  enabled: true\n  scan_timeout_ms: 5000\n" +
		"  ai_review_key: \"" + key + "\"\n  ai_review_key_version: 2\n"
	require.NoError(t, os.WriteFile(path, []byte(yaml), 0o600))
	t.Setenv(config.EnvConfigPath, path)
	require.NoError(t, config.Load())
}

// TestAIKeySealOpenRoundTrip 钉住密钥的加解密与 AAD 绑定。
func TestAIKeySealOpenRoundTrip(t *testing.T) {
	useAIReviewKey(t)
	require.True(t, aiKeyConfigured())

	const plain = "sk-this-is-only-a-test-value"
	nonce, cipher, version, err := sealAIKey(plain, aiChannelAAD(7))
	require.NoError(t, err)
	assert.Equal(t, 2, version, "版本号必须由 seal 返回,两次分开读配置会把 v1 密文标成 v2")
	assert.NotContains(t, string(cipher), plain, "密文里不能出现明文")

	got, err := openAIKey(nonce, cipher, aiChannelAAD(7), version)
	require.NoError(t, err)
	assert.Equal(t, plain, got)

	t.Run("密文被搬到另一个渠道上会解密失败", func(t *testing.T) {
		_, err := openAIKey(nonce, cipher, aiChannelAAD(8), version)
		require.ErrorIs(t, err, errAIKeyUndecryptable,
			"AAD 绑定渠道 id 就是为了挡住这一步 —— 否则 A 渠道的密钥会被 B 渠道拿去用")
	})

	t.Run("没有密文表示这个渠道本来就不需要鉴权", func(t *testing.T) {
		got, err := openAIKey(nil, nil, aiChannelAAD(7), 0)
		require.NoError(t, err)
		assert.Empty(t, got)
	})

	t.Run("轮换后旧版本查不到密钥时不猜、直接报错", func(t *testing.T) {
		_, err := openAIKey(nonce, cipher, aiChannelAAD(7), 99)
		require.ErrorIs(t, err, errAIKeyMissingVersion)
	})
}

// TestAIKeyRefusedWithoutConfiguredKey 钉住"没配密钥就存不下密钥"。
//
// 方向必须是拒绝而不是明文兜底:"配置少一行就静默降级成明文"是凭证最常见的
// 泄漏路径,而它在界面上完全看不出来。
func TestAIKeyRefusedWithoutConfiguredKey(t *testing.T) {
	useGenerousScanBudget(t) // 这份配置里没有 ai_review_key
	assert.False(t, aiKeyConfigured())
	_, _, _, err := sealAIKey("sk-whatever", aiChannelAAD(1))
	require.ErrorIs(t, err, errAIKeyUnavailable)
}

// TestMaskAIKeyNeverLeaksEnough 钉住掩码只露尾 4 位,短密钥一个字符都不露。
func TestMaskAIKeyNeverLeaksEnough(t *testing.T) {
	tests := []struct{ in, want string }{
		{"", ""},
		{"short", "****"},
		{"sk-abcdefghijkl", "****ijkl"},
	}
	for _, tc := range tests {
		t.Run(tc.in, func(t *testing.T) {
			got := maskAIKey(tc.in)
			assert.Equal(t, tc.want, got)
			if len(tc.in) >= 8 {
				assert.NotContains(t, got, tc.in[:len(tc.in)-4], "掩码里不能出现密钥的前缀")
			}
		})
	}
}

// TestAIChannelViewCarriesNoSecret 是接口回显那一半的锁。
//
// 白名单视图结构体的字段是逐个列出来的,这条测试保证它不会在下一次改动里
// 悄悄变成 AIChannel 的别名(那样密文与 nonce 会跟着一起下发)。
func TestAIChannelViewCarriesNoSecret(t *testing.T) {
	useAIReviewKey(t)
	nonce, cipher, version, err := sealAIKey("sk-secret-value-here", aiChannelAAD(3))
	require.NoError(t, err)

	view := toAIChannelView(AIChannel{
		Id: 3, Name: "n", BaseUrl: "https://x/v1", Model: "m",
		KeyNonce: nonce, KeyCipher: cipher, KeyVersion: version,
		KeyHint: maskAIKey("sk-secret-value-here"),
	})
	assert.True(t, view.HasKey)
	assert.Equal(t, "****here", view.KeyHint)

	// 序列化之后逐字节检查:结构体字段看着没问题,序列化出来多一段才是真事故。
	blob := fmt.Sprintf("%+v", view)
	assert.NotContains(t, blob, "sk-secret-value-here")
	assert.NotContains(t, blob, string(cipher))
}

// TestAIChannelAuditSnapCarriesNoSecret 同上,针对审计快照。
// 审计表会被导出、会被管理端整行下发,密文一旦离开服务器就再也收不回来。
func TestAIChannelAuditSnapCarriesNoSecret(t *testing.T) {
	useAIReviewKey(t)
	nonce, cipher, version, err := sealAIKey("sk-audit-secret", aiChannelAAD(4))
	require.NoError(t, err)

	snap := aiChannelAuditSnap(&AIChannel{
		Id: 4, Name: "n", KeyNonce: nonce, KeyCipher: cipher, KeyVersion: version,
		KeyHint: maskAIKey("sk-audit-secret"),
	})
	blob := fmt.Sprintf("%v", snap)
	assert.NotContains(t, blob, "sk-audit-secret")
	assert.NotContains(t, blob, string(cipher))
	assert.Equal(t, true, snap["has_key"])
}

// ─────────────────────── 送审内容的裁剪 ───────────────────────

// TestReviewTextClipsHeadAndTail 钉住"取头尾"而不是"只取头"。
//
// 只取头等于给出一条"前面垫 5000 个字就不会被审"的绕过路径。
func TestReviewTextClipsHeadAndTail(t *testing.T) {
	head := strings.Repeat("头", 300)
	tail := strings.Repeat("尾", 300)
	got := reviewText(head+strings.Repeat("中", 5000)+tail, 400)

	assert.Contains(t, got, "头")
	assert.Contains(t, got, "尾", "尾部必须保留 —— 违规内容常被刷子塞在长 padding 之后")
	assert.Contains(t, got, "truncated")
	assert.Less(t, len([]rune(got)), 500)

	t.Run("短内容原样送审", func(t *testing.T) {
		assert.Equal(t, "很短", reviewText("很短", 400))
	})
}
