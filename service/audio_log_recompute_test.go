package service

import (
	"net/http"
	"net/http/httptest"
	"testing"

	relaycommon "github.com/QuantumNous/new-api/relay/common"
	"github.com/QuantumNous/new-api/setting/ratio_setting"
	"github.com/QuantumNous/new-api/types"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// 一条音频消费日志必须能用**它自己记下的明细**复算出**它自己记下的金额**。
//
// 曾经不能:GenerateAudioOtherInfo / GenerateWssOtherInfo 直接读 usage 原值,
// 而真正参与计费的是两处归一化之后的值 —— normalizeAudioTokenDetails 在上游
// 不报 text_tokens 时用「总数 − 音频」兜底文本 token(OpenAI 兼容渠道走
// /v1/chat/completions 的主路一处都不补 text_tokens,所以这是默认形状),
// reasoningTokensOutsideCompletion 把落在 completion 之外的思考 token 补进输出。
//
// 活体实测过的那一笔:usage {prompt 100, completion 1, total 154,
// prompt_details.audio 20, completion_details.reasoning 53},模型
// ModelRatio2 / CompletionRatio3 / AudioRatio10 / AudioCompletionRatio2,
// 实收 884;而同一行日志里 text_input=0、text_output=0、audio_input=20,
// 照着代回去只得 400。用户看到的是「输出 1 个 token 收 884」,
// 而申诉、退款仲裁与对账脚本都答不出这 884 是怎么来的。
//
// 判据故意写成"用日志字段重算金额",而不是"字段等于某个常数" ——
// 后者会随着任何一次口径调整一起改绿,前者不会。
func TestAudioConsumeLogCanBeRecomputedFromItsOwnFields(t *testing.T) {
	gin.SetMode(gin.TestMode)

	const (
		modelRatio           = 2.0
		completionRatio      = 3.0
		audioRatio           = 10.0
		audioCompletionRatio = 2.0
		groupRatio           = 1.0
	)

	// 上游原样上报的形态:不报 text_tokens,思考 token 落在 completion 之外。
	rawPrompt, rawCompletion, rawTotal := 100, 1, 154
	rawAudioIn, rawAudioOut, reasoning := 20, 0, 53

	// 计费侧走的两处归一化,与 PostAudioConsumeQuota 里逐字同源。
	completionTokens := rawCompletion + reasoning
	in := normalizeAudioTokenDetails(rawPrompt, TokenDetails{AudioTokens: rawAudioIn})
	out := normalizeAudioTokenDetails(completionTokens, TokenDetails{AudioTokens: rawAudioOut})

	// 倍率是按模型名从 ratio_setting 全局表里取的,所以必须先真的写进去 ——
	// 否则这条判据会跑在一组默认倍率上,与注释里那笔活体实测对不上。
	require.NoError(t, ratio_setting.UpdateCompletionRatioByJSONString(`{"probe-audio":3}`))
	require.NoError(t, ratio_setting.UpdateAudioRatioByJSONString(`{"probe-audio":10}`))
	require.NoError(t, ratio_setting.UpdateAudioCompletionRatioByJSONString(`{"probe-audio":2}`))

	quota, clamp := calculateAudioQuota(QuotaInfo{
		InputDetails: in, OutputDetails: out,
		ModelName: "probe-audio", ModelRatio: modelRatio, GroupRatio: groupRatio,
	})
	require.Nil(t, clamp)
	// 独立算出的期望:(80 文本输入 + 54 文本输出×3 + 20 音频×10 + 0) × 2 = 884
	require.Equal(t, 884, quota, "先确认计费侧本身是对的,否则下面的复算没有意义")
	_ = rawTotal

	res := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(res)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/chat/completions", nil)
	relayInfo := &relaycommon.RelayInfo{
		PriceData:   types.PriceData{},
		ChannelMeta: &relaycommon.ChannelMeta{},
	}

	other := GenerateAudioOtherInfo(c, relayInfo, in, out,
		modelRatio, groupRatio, completionRatio, audioRatio, audioCompletionRatio, 0, 0)

	// 用日志里的四项明细 + 日志里的四个倍率复算金额。
	textIn := other["text_input"].(int)
	textOut := other["text_output"].(int)
	audioIn := other["audio_input"].(int)
	audioOut := other["audio_output"].(int)
	recomputed := int(
		(float64(textIn) +
			float64(textOut)*other["completion_ratio"].(float64) +
			float64(audioIn)*other["audio_ratio"].(float64) +
			float64(audioOut)*other["audio_ratio"].(float64)*other["audio_completion_ratio"].(float64)) *
			other["model_ratio"].(float64) * groupRatio)

	assert.Equal(t, quota, recomputed,
		"日志里记的是 text_input=%d text_output=%d audio_input=%d audio_output=%d,"+
			"照着复算得到 %d,而实收 %d —— 这一行账单自己和自己对不上",
		textIn, textOut, audioIn, audioOut, recomputed, quota)

	// 逐项钉住归一化后的取值,免得复算恒等式被"两边一起错"糊弄过去。
	assert.Equal(t, 80, textIn, "上游不报 text_tokens 时必须按『总数 − 音频』兜底,不是 0")
	assert.Equal(t, 54, textOut, "落在 completion 之外的思考 token 必须记进输出,不是 1")
	assert.Equal(t, 20, audioIn)
	assert.Equal(t, 0, audioOut)
}
