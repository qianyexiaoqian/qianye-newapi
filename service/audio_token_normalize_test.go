package service

import (
	"os"
	"strings"
	"testing"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/setting/ratio_setting"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// audio_token_normalize_test.go —— 音频计价不许把「上游没报的那一段」白送。
//
// 缺陷形状:calculateAudioQuota 只按 TextTokens / AudioTokens 逐项累加,而这两个
// 数**完全由上游决定**。上游报了 audio_tokens 却没报 text_tokens 时,除音频以外
// 的全部输入输出免费;/v1/responses 更狠 —— 那条协议本来就没有 text_tokens 字段,
// 于是一次纯文本对话只要模型名撞上 gpt-4o-audio* 前缀,四项明细全是 0,金额被
// 地板托成 1(实测同一份 usage 走文本路 3750、走音频路 1)。
//
// 倍率的文本路径在同样情形下是「从 prompt 里逐项扣减子类、剩下的按基础价收」
// (text_quota.go 的 baseTokens),两条路径口径不能相反。

func TestNormalizeAudioTokenDetailsNeverGivesTokensAwayForFree(t *testing.T) {
	cases := []struct {
		name  string
		total int
		in    TokenDetails
		want  TokenDetails
	}{
		{
			name:  "上游报得完整:恒等变换,一分钱不多收",
			total: 1000,
			in:    TokenDetails{TextTokens: 990, AudioTokens: 10},
			want:  TokenDetails{TextTokens: 990, AudioTokens: 10},
		},
		{
			name:  "报了 audio 没报 text:补成 总数−音频",
			total: 1000,
			in:    TokenDetails{TextTokens: 0, AudioTokens: 10},
			want:  TokenDetails{TextTokens: 990, AudioTokens: 10},
		},
		{
			name:  "四项明细全空(/v1/responses 的标准形状):整段按文本收",
			total: 1000,
			in:    TokenDetails{},
			want:  TokenDetails{TextTokens: 1000},
		},
		{
			name:  "上游报的 text 偏小(reasoning 不算在 text 里):按总数兜底",
			total: 1500,
			in:    TokenDetails{TextTokens: 500, AudioTokens: 0},
			want:  TokenDetails{TextTokens: 1500},
		},
		{
			name:  "全是音频:文本补成 0,不会补出负数",
			total: 1000,
			in:    TokenDetails{TextTokens: 0, AudioTokens: 1000},
			want:  TokenDetails{TextTokens: 0, AudioTokens: 1000},
		},
		{
			name:  "音频比总数还大(上游自相矛盾):文本不补,音频原样",
			total: 100,
			in:    TokenDetails{TextTokens: 0, AudioTokens: 500},
			want:  TokenDetails{TextTokens: 0, AudioTokens: 500},
		},
		{
			name:  "总数为 0 而明细有值:明细说了算,不能被清零",
			total: 0,
			in:    TokenDetails{TextTokens: 20, AudioTokens: 5},
			want:  TokenDetails{TextTokens: 20, AudioTokens: 5},
		},
		{
			name:  "上游报了负数:夹回 0,不许变成负计费",
			total: 100,
			in:    TokenDetails{TextTokens: -50, AudioTokens: -10},
			want:  TokenDetails{TextTokens: 100, AudioTokens: 0},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			assert.Equal(t, tc.want, normalizeAudioTokenDetails(tc.total, tc.in))
		})
	}
}

// 单调不减:补齐之后的金额永远不低于改动前,不存在「原本收得到、改完反而少收」。
func TestNormalizedAudioQuotaIsNeverLowerThanBefore(t *testing.T) {
	const model = "qy-audio-normalize-test"
	prevRatio := ratio_setting.GetAudioRatioCopy()
	prevJSON, err := common.Marshal(prevRatio)
	require.NoError(t, err)
	require.NoError(t, ratio_setting.UpdateAudioRatioByJSONString(`{"`+model+`":10}`))
	t.Cleanup(func() {
		require.NoError(t, ratio_setting.UpdateAudioRatioByJSONString(string(prevJSON)))
	})

	quotaOf := func(in, out TokenDetails) int {
		q, _ := calculateAudioQuota(QuotaInfo{
			InputDetails: in, OutputDetails: out,
			ModelName: model, ModelRatio: 1, GroupRatio: 1,
		})
		return q
	}

	rawIn := TokenDetails{AudioTokens: 10}
	rawOut := TokenDetails{AudioTokens: 4}
	before := quotaOf(rawIn, rawOut)
	after := quotaOf(
		normalizeAudioTokenDetails(1000, rawIn),
		normalizeAudioTokenDetails(500, rawOut),
	)
	assert.Greater(t, after, before,
		"上游漏报 text_tokens 时,补齐之后必须真的多收到那 990+496 个 token 的钱")

	// 上游报得完整时两者必须逐位相同 —— 否则这条补丁就是在给正常请求加价。
	fullIn := TokenDetails{TextTokens: 990, AudioTokens: 10}
	fullOut := TokenDetails{TextTokens: 496, AudioTokens: 4}
	assert.Equal(t,
		quotaOf(fullIn, fullOut),
		quotaOf(normalizeAudioTokenDetails(1000, fullIn), normalizeAudioTokenDetails(500, fullOut)),
		"上游报得对的时候必须是恒等变换")
}

// TestEveryAudioQuotaCallSiteNormalizesFirst 是这条补丁的**接线**守卫。
//
// 纯函数写对了、调用点没接上,是本仓的头号缺陷形状:三个入口
// (PreWssConsumeQuota / PostWssConsumeQuota / PostAudioConsumeQuota)
// 里漏掉任何一个,那条链上的音频计费就继续白送 token,而整包测试全绿。
//
// 因此逐个调用点扫源码:每一次 calculateAudioQuota 之前都必须先补齐两侧明细。
func TestEveryAudioQuotaCallSiteNormalizesFirst(t *testing.T) {
	raw, err := os.ReadFile("quota.go")
	require.NoError(t, err)
	src := string(raw)

	const call = "calculateAudioQuota(quotaInfo)"
	sites := 0
	for offset := 0; ; {
		idx := strings.Index(src[offset:], call)
		if idx < 0 {
			break
		}
		at := offset + idx
		offset = at + len(call)
		sites++

		from := at - 600
		if from < 0 {
			from = 0
		}
		window := src[from:at]
		assert.Equal(t, 2, strings.Count(window, "normalizeAudioTokenDetails("),
			"第 %d 个 calculateAudioQuota 调用点之前没有把输入与输出两侧的 token 明细补齐;"+
				"上游漏报 text_tokens 时这条链会把除音频以外的全部 token 免费送出去", sites)
	}
	assert.Equal(t, 3, sites,
		"音频计价的调用点应当恰好是 PreWss / PostWss / PostAudio 三处;"+
			"数目变了说明有人新加或删掉了一条链路,请连同补齐一起检查")
}
