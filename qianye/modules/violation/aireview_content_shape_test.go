package violation

import (
	"context"
	"fmt"
	"net/http"
	"testing"

	"github.com/shopspring/decimal"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// OpenAI 兼容响应里的 content 有两种真实形态:一个字符串,或者一个 parts 数组
// ([{"type":"text","text":"..."}])。参考实现(sub2api 的 extractOpenAIContent)
// 两种都认;本仓的 relaykit 在**转发**方向上也认(Message.StringContent 带
// []any 分支),所以审核渠道指向本网关自身、或指向同类中转站、或指向 AWS
// Bedrock 上的 Claude 时都会收到数组形态。
//
// 把 content 声明成 string 的后果有两条,而且都不响:
//
//	① 整个响应体 Unmarshal 报错 → 该渠道永远给不出结论、永远 bad_json、
//	   永远 fail-open;
//	② 报错发生在记账那几行**之前**,于是响应体里明明带着 usage,token 与花费
//	   却被整笔丢弃。cost_unknown = TotalTokens>0 && !Priced 归零后恒为 false,
//	   这一行被记成"花费 0 且确定",而管理端低估告警的 SQL 前提是
//	   total_tokens > 0 —— 对它完全隐形。
//
// 两条分别由下面两组子用例钉住。
func TestCallAIChannelHandlesBothContentShapesAndAlwaysBills(t *testing.T) {
	useTestConfig(t, "  enabled: true\n")

	const (
		promptTok = 40
		compTok   = 8
	)
	verdictJSON := `{"violation":true,"category":"jailbreak","confidence":0.9,"reason":"测试"}`

	cases := []struct {
		name        string
		body        string
		wantOutcome string
		wantViolate bool
		// 无论结论解析成不成功,用量都必须记下来 —— 这次调用是真花了钱的。
		wantTokens int
		why        string
	}{
		{
			name: "字符串形态(对照组)",
			body: fmt.Sprintf(`{"choices":[{"message":{"content":%q}}],`+
				`"usage":{"prompt_tokens":%d,"completion_tokens":%d,"total_tokens":%d}}`,
				verdictJSON, promptTok, compTok, promptTok+compTok),
			wantOutcome: OutcomeViolation, wantViolate: true, wantTokens: promptTok + compTok,
			why: "这一档本来就是对的,改动不许让它回归",
		},
		{
			name: "parts 数组形态:必须读得出结论",
			body: fmt.Sprintf(`{"choices":[{"message":{"content":[{"type":"text","text":%q}]}}],`+
				`"usage":{"prompt_tokens":%d,"completion_tokens":%d,"total_tokens":%d}}`,
				verdictJSON, promptTok, compTok, promptTok+compTok),
			wantOutcome: OutcomeViolation, wantViolate: true, wantTokens: promptTok + compTok,
			why: "参考实现两种都认;声明成 string 会让这个渠道永远 bad_json、永远放行",
		},
		{
			name: "parts 数组拼接多段",
			body: fmt.Sprintf(`{"choices":[{"message":{"content":[`+
				`{"type":"text","text":%q},{"type":"text","text":%q}]}}],`+
				`"usage":{"prompt_tokens":%d,"completion_tokens":%d,"total_tokens":%d}}`,
				verdictJSON[:20], verdictJSON[20:], promptTok, compTok, promptTok+compTok),
			wantOutcome: OutcomeViolation, wantViolate: true, wantTokens: promptTok + compTok,
			why: "分片是这个形态的常态,只取第一片会解析失败",
		},
		{
			name: "空 choices 但带着 usage:结论读不出,钱照记",
			body: fmt.Sprintf(`{"choices":[],"usage":{"prompt_tokens":%d,"completion_tokens":%d,"total_tokens":%d}}`,
				promptTok, compTok, promptTok+compTok),
			wantOutcome: OutcomeBadJSON, wantViolate: false, wantTokens: promptTok + compTok,
			why: "这一档自己标了可重试 —— 也就是承认调用已完成、已付费,那用量就必须记下来",
		},
		{
			name:        "网关门户页:本来就没有 usage 可记",
			body:        `<html><body>403 Forbidden</body></html>`,
			wantOutcome: OutcomeBadJSON, wantViolate: false, wantTokens: 0,
			why: "反向对照:提前记账不许凭空造出用量",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			srv := newFakeReviewServer(t, func(w http.ResponseWriter, _ string) {
				w.Header().Set("Content-Type", "application/json")
				_, _ = w.Write([]byte(tc.body))
			})
			ch := &aiChannelRT{
				Id: 1, Name: "fake", URL: chatCompletionsURL(srv.URL), Model: "fake-model",
				Weight:      1,
				PriceInPerM: decimal.NewFromFloat(1), PriceOutPerM: decimal.NewFromFloat(2),
			}
			out := callAIChannel(context.Background(), ch, defaultAIPrompt, "待审文本", 3000,
				seedAIVocabulary(), false)

			require.NotNil(t, out)
			assert.Equal(t, tc.wantOutcome, out.Outcome, tc.why)
			assert.Equal(t, tc.wantViolate, out.Violated, tc.why)
			assert.Equal(t, tc.wantTokens, out.TotalTokens,
				"用量必须记下来 —— 归零之后 cost_unknown 恒为 false,"+
					"这一行会被记成『花费 0 且确定』,而管理端低估告警的 SQL "+
					"(total_tokens > 0 AND ...)对它完全隐形。%s", tc.why)
			if tc.wantTokens > 0 {
				assert.Equal(t, promptTok, out.PromptTokens)
				assert.Equal(t, compTok, out.CompletionTokens)
				assert.True(t, out.Priced, "填了单价 + 有用量 = 这个花费数字算得准")
				// 独立算出的期望:40/1e6*1 + 8/1e6*2 = 0.000056
				assert.True(t, decimal.NewFromFloat(0.000056).Equal(out.CostUsd),
					"cost 应为 0.000056,实际 %s", out.CostUsd)
			} else {
				assert.False(t, out.Priced, "没有用量时不许把 0 标成准确值")
			}
		})
	}
}
