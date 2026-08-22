/*
Copyright (C) 2023-2026 QuantumNous

This program is free software: you can redistribute it and/or modify
it under the terms of the GNU Affero General Public License as
published by the Free Software Foundation, either version 3 of the
License, or (at your option) any later version.

This program is distributed in the hope that it will be useful,
but WITHOUT ANY WARRANTY; without even the implied warranty of
MERCHANTABILITY or FITNESS FOR A PARTICULAR PURPOSE. See the
GNU Affero General Public License for more details.

You should have received a copy of the GNU Affero General Public License
along with this program. If not, see <https://www.gnu.org/licenses/>.

For commercial licensing, please contact support@quantumnous.com
*/
package service

import (
	"os"
	"regexp"
	"strings"
	"testing"

	"github.com/QuantumNous/new-api/relaykit/dto"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// audio_billing_parity_test.go —— 音频/实时这两条结算路必须与文本路口径一致。
//
// 两条路一直在漂:文本路修过的两件事,音频路都没跟。它们都是**静默少收**,
// 请求照常 200、日志照常写着价格,只有金额是错的。

// TestAudioPathDoesNotFreeAPerCallRequestWhenTotalTokensIsZero 守
// 「按次计费与 token 数严格无关」。
//
// `if totalTokens == 0 { quota = 0 }` 这条兜底只对**按量**计费成立:那时没有
// token 数确实算不出金额。按次计费的金额是 ModelPrice × QuotaPerUnit × GroupRatio,
// 与 token 数没有任何关系 —— 拿 TotalTokens 当开关等于把一次已经完成的调用整笔
// 免单:预扣的钱全额退回,一分钱收不到,而日志上还写着「模型价格 X」。
//
// 文本路早就把这条判据收窄到 `if !UsePrice`(service/text_quota.go),音频与
// 实时这两处一直原封不动。线上实测:同一份 usage(prompt 100 / completion 50 /
// 音频明细齐全 / total_tokens 0)打到一个按次定价的音频模型上 → HTTP 200 拿到
// 回答、扣 0;只把 total_tokens 改回 150 → 扣 375000;同一份 usage 打到按次定价
// 的**文本**模型上 → 扣 375000。上游把 token 明细报全却漏报 total_tokens,是
// 二手中转与自建 OpenAI 兼容服务里常见的形状。
//
// 判据落在源码上而不是跑一次 handler:这两处结算函数要一个真实的 gin.Context、
// 真实的用户/令牌/渠道行才跑得起来,而被守的东西只有一件 ——
// **那个开关不许再无视 UsePrice**。
func TestAudioPathDoesNotFreeAPerCallRequestWhenTotalTokensIsZero(t *testing.T) {
	raw, err := os.ReadFile("quota.go")
	require.NoError(t, err)
	src := string(raw)

	sites := 0
	for offset := 0; ; {
		idx := strings.Index(src[offset:], "if totalTokens == 0 {")
		if idx < 0 {
			break
		}
		at := offset + idx
		offset = at + 1
		sites++

		to := at + 700
		if to > len(src) {
			to = len(src)
		}
		window := src[at:to]
		assert.Containsf(t, window, "if !usePrice {",
			"第 %d 处 `totalTokens == 0` 兜底仍然无条件把 quota 清零 —— "+
				"按次计费的金额与 token 数严格无关,这等于把一次已经完成的调用整笔免单", sites)
	}
	assert.Equal(t, 2, sites,
		"这条兜底应当恰好出现在 PostWssConsumeQuota 与 PostAudioConsumeQuota 两处;"+
			"数目变了说明有人新加了一条结算链,请连同这条判据一起检查")

	// 记账那一步也必须跟着改:按次定价 + total_tokens=0 时金额是全额,
	// 却因为原来的 else 分支而不进 used_quota 与请求计数 —— 钱扣了,统计上不存在。
	assert.Equal(t, 2, strings.Count(src, "if totalTokens > 0 || quota > 0 {"),
		"按次定价在 total_tokens=0 时仍然收钱,这笔钱必须进 used_quota 与请求计数")
}

// TestAudioPathBillsReasoningTokensOutsideCompletion 守「思考 token 要计费」。
//
// 这条口径在本仓是既定的:原生 Gemini 路径把 ThoughtsTokenCount 加进
// CompletionTokens、Gemini→OpenAI 转换用 total−prompt 兜底、文本路走
// reasoningTokensOutsideCompletion、阶梯路走 BuildTieredTokenParams。唯独音频
// 这条路一次都不调,直接采信 usage.CompletionTokens。
//
// 表现:同一次推理的两种上报写法,金额差 reasoning ÷ completion。
// 实测 {p:100, c:1, reasoning:53, total:154, 音频明细} 与
// {p:100, c:54, reasoning:53, total:154, 同样明细} 在文本路收同一个数,
// 在音频路差 30%(思考型语音模型上可以差 50 倍)。方向是少收,纯平台损失。
//
// 补进去的位置必须在 normalizeAudioTokenDetails **之前**:那个函数拿
// 「总数 − 音频」兜底文本 token,而 reasoning 落在 completion 之外时,它拿到的
// 那个"总数"本身就是缺的那个数,兜底只能兜到 1。
func TestAudioPathBillsReasoningTokensOutsideCompletion(t *testing.T) {
	t.Run("判据本身:只认三个数字互相印证的那一种形状", func(t *testing.T) {
		cases := []struct {
			name  string
			usage dto.Usage
			want  int
		}{
			{
				name: "reasoning 在 completion 之外,三数印证",
				usage: dto.Usage{
					PromptTokens: 100, CompletionTokens: 1, TotalTokens: 154,
					CompletionTokenDetails: dto.OutputTokenDetails{ReasoningTokens: 53},
				},
				want: 53,
			},
			{
				name: "reasoning 已经含在 completion 里:不许重复补",
				usage: dto.Usage{
					PromptTokens: 100, CompletionTokens: 54, TotalTokens: 154,
					CompletionTokenDetails: dto.OutputTokenDetails{ReasoningTokens: 53},
				},
				want: 0,
			},
			{
				name: "三数对不上(Claude 语义 total > prompt+completion):一个字都不补",
				usage: dto.Usage{
					PromptTokens: 100, CompletionTokens: 1, TotalTokens: 999,
					CompletionTokenDetails: dto.OutputTokenDetails{ReasoningTokens: 53},
				},
				want: 0,
			},
		}
		for _, tc := range cases {
			t.Run(tc.name, func(t *testing.T) {
				u := tc.usage
				assert.Equal(t, tc.want, reasoningTokensOutsideCompletion(&u))
			})
		}
	})

	t.Run("这一位值多少钱:同一次推理的两种上报写法必须收同一个数", func(t *testing.T) {
		// 倍率:模型 2 × 补全 3 × 分组 1.5,音频 10。同一次推理,两种上报写法。
		info := func(completion int) QuotaInfo {
			q := QuotaInfo{
				InputDetails:  TokenDetails{TextTokens: 70, AudioTokens: 30},
				OutputDetails: TokenDetails{AudioTokens: 0},
				ModelName:     "qy-audio-reason",
				ModelRatio:    2,
				GroupRatio:    1.5,
			}
			q.OutputDetails = normalizeAudioTokenDetails(completion, q.OutputDetails)
			return q
		}
		outside := dto.Usage{
			PromptTokens: 100, CompletionTokens: 1, TotalTokens: 154,
			CompletionTokenDetails: dto.OutputTokenDetails{ReasoningTokens: 53},
		}
		inside := dto.Usage{
			PromptTokens: 100, CompletionTokens: 54, TotalTokens: 154,
			CompletionTokenDetails: dto.OutputTokenDetails{ReasoningTokens: 53},
		}

		corrected, _ := calculateAudioQuota(
			info(outside.CompletionTokens + reasoningTokensOutsideCompletion(&outside)))
		reference, _ := calculateAudioQuota(
			info(inside.CompletionTokens + reasoningTokensOutsideCompletion(&inside)))
		assert.Equal(t, reference, corrected,
			"同一次推理换一种上报写法就该收同一个数")

		naive, _ := calculateAudioQuota(info(outside.CompletionTokens))
		assert.Less(t, naive, corrected,
			"直接采信 usage.CompletionTokens 会少收 —— 这条断言就是这一位值多少钱")
		assert.Greater(t, corrected-naive, int(0))
	})

	t.Run("接线:音频结算路必须真的调它,而且在归一化之前", func(t *testing.T) {
		raw, err := os.ReadFile("quota.go")
		require.NoError(t, err)
		src := string(raw)

		call := regexp.MustCompile(
			`completionTokens := usage\.CompletionTokens \+ reasoningTokensOutsideCompletion\(usage\)`)
		loc := call.FindStringIndex(src)
		require.NotNil(t, loc,
			"PostAudioConsumeQuota 没有把 completion 之外的思考 token 补进来 —— "+
				"同一次推理换一种上报写法就少收 reasoning÷completion 的比例")

		normalize := strings.Index(src[loc[1]:],
			"normalizeAudioTokenDetails(completionTokens,")
		require.GreaterOrEqual(t, normalize, 0,
			"补出来的 completionTokens 没有被喂给 normalizeAudioTokenDetails,"+
				"那等于算了一遍又扔掉")
		assert.Less(t, normalize, 400,
			"补齐必须紧挨着归一化:中间隔太远,下一个人很容易在两者之间插一段"+
				"重新读 usage.CompletionTokens 的代码")
	})
}
