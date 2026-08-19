package claude

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/QuantumNous/new-api/constant"
	relaycommon "github.com/QuantumNous/new-api/relay/common"
	"github.com/QuantumNous/new-api/relaykit/types"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// stream_midflight_error_billing_test.go —— 上游在流中途报错时,已经产生的用量
// 必须被交回去结算。
//
// 缺陷形态:ClaudeStreamHandler 用一个外层 err 记住最后一次
// HandleStreamResponseData 的结果,非 nil 就 `return nil, err` —— 把已经累计的
// claudeInfo.Usage 整份丢掉。调用方(relay/claude_handler.go)据此 return,
// PostTextConsumeQuota 一次都不跑,controller/relay.go 的 defer 全额退预扣。
// 实测:客户端收到全部 20 段正文、上游 40000 input + 30000 output 真烧掉,
// users.quota 分毫未动、logs 表零新增 —— 平台一分不收且账面上没有任何一行
// 指向这笔钱。走 claude.Adaptor 的全部渠道(Anthropic / Bedrock / Vertex /
// DeepSeek / Moonshot / MiniMax / Volcengine / Zhipu-4v …)都在影响面里。
//
// 全仓只有这一个流式 handler 这么写;openai/gemini/dify/baidu/responses 都是
// 无论如何 `return usage, nil`。

func newClaudeStreamContext(t *testing.T) (*gin.Context, *relaycommon.RelayInfo) {
	t.Helper()
	gin.SetMode(gin.TestMode)
	// StreamScannerHandler 用 constant.StreamingTimeout 造 ticker,零值会 panic。
	prevTimeout := constant.StreamingTimeout
	constant.StreamingTimeout = 60
	t.Cleanup(func() { constant.StreamingTimeout = prevTimeout })

	recorder := httptest.NewRecorder()
	ctx, _ := gin.CreateTestContext(recorder)
	ctx.Request = httptest.NewRequest(http.MethodPost, "/v1/messages", nil)

	info := &relaycommon.RelayInfo{
		RelayFormat:     types.RelayFormatClaude,
		OriginModelName: "claude-midflight-model",
		IsStream:        true,
	}
	// UpstreamModelName / ChannelId 挂在嵌入的 *ChannelMeta 上,生产路径由
	// InitChannelMeta 填;这里手工给一份最小的。
	info.ChannelMeta = &relaycommon.ChannelMeta{
		ChannelId:         991,
		UpstreamModelName: "claude-midflight-model",
	}
	return ctx, info
}

func sseResponse(body string) *http.Response {
	return &http.Response{
		StatusCode: http.StatusOK,
		Header:     http.Header{"Content-Type": []string{"text/event-stream"}},
		Body:       io.NopCloser(strings.NewReader(body)),
	}
}

func TestClaudeStreamBillsUsageThatWasAlreadyProducedWhenTheUpstreamAborts(t *testing.T) {
	// message_start 带完整 input_tokens,随后推正文,最后上游报 overloaded_error
	// —— Anthropic 流式协议里传递错误的正常方式。
	body := strings.Join([]string{
		`event: message_start`,
		`data: {"type":"message_start","message":{"id":"msg_1","model":"claude-midflight-model","usage":{"input_tokens":40000,"output_tokens":0}}}`,
		``,
		`event: content_block_delta`,
		`data: {"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"hello"}}`,
		``,
		`event: message_delta`,
		`data: {"type":"message_delta","delta":{"stop_reason":"end_turn"},"usage":{"output_tokens":30000}}`,
		``,
		`event: error`,
		`data: {"type":"error","error":{"type":"overloaded_error","message":"Overloaded"}}`,
		``,
	}, "\n")

	ctx, info := newClaudeStreamContext(t)
	usage, apiErr := ClaudeStreamHandler(ctx, sseResponse(body), info)

	require.Nil(t, apiErr,
		"正文已经交付、上游 token 已经烧掉:把错误交回去等于全额退预扣、一行日志都不写")
	require.NotNil(t, usage)
	assert.Equal(t, 40000, usage.PromptTokens)
	assert.Equal(t, 30000, usage.CompletionTokens)
}

func TestClaudeStreamStillSurfacesAnErrorWhenNothingWasProduced(t *testing.T) {
	// 开流即错:一个 token 都没产生。这一格必须仍然把错误交回去,
	// 否则重试与错误日志一起失效,而且平台会为一次完全没发生的调用记账。
	body := strings.Join([]string{
		`event: error`,
		`data: {"type":"error","error":{"type":"overloaded_error","message":"Overloaded"}}`,
		``,
	}, "\n")

	ctx, info := newClaudeStreamContext(t)
	usage, apiErr := ClaudeStreamHandler(ctx, sseResponse(body), info)

	require.NotNil(t, apiErr, "没有任何用量的失败流仍然是一次失败,必须可重试")
	assert.Nil(t, usage)
}

func TestClaudeStreamHappyPathIsUnchanged(t *testing.T) {
	body := strings.Join([]string{
		`event: message_start`,
		`data: {"type":"message_start","message":{"id":"msg_2","model":"claude-midflight-model","usage":{"input_tokens":1200,"output_tokens":0}}}`,
		``,
		`event: content_block_delta`,
		`data: {"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"hi"}}`,
		``,
		`event: message_delta`,
		`data: {"type":"message_delta","delta":{"stop_reason":"end_turn"},"usage":{"output_tokens":34}}`,
		``,
		`event: message_stop`,
		`data: {"type":"message_stop"}`,
		``,
	}, "\n")

	ctx, info := newClaudeStreamContext(t)
	usage, apiErr := ClaudeStreamHandler(ctx, sseResponse(body), info)

	require.Nil(t, apiErr)
	require.NotNil(t, usage)
	assert.Equal(t, 1200, usage.PromptTokens)
	assert.Equal(t, 34, usage.CompletionTokens)
}
