package service

import (
	"math"
	"net/http/httptest"
	"testing"
	"time"

	relaycommon "github.com/QuantumNous/new-api/relay/common"
	"github.com/QuantumNous/new-api/relaykit/dto"
	"github.com/QuantumNous/new-api/relaykit/types"
	hosttypes "github.com/QuantumNous/new-api/types"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func upstreamUsageRelayInfo() *relaycommon.RelayInfo {
	return &relaycommon.RelayInfo{
		RelayFormat:     types.RelayFormatOpenAI,
		OriginModelName: "qy-usage-probe",
		PriceData: hosttypes.PriceData{
			ModelRatio:      1,
			CompletionRatio: 1,
			CacheRatio:      1,
			ImageRatio:      1,
			GroupRatioInfo:  hosttypes.GroupRatioInfo{GroupRatio: 1},
		},
		StartTime: time.Now(),
	}
}

// 「上游没返回计费信息就不收钱」这道闸门的判据是 prompt + completion 的符号。
// 两个 int 相加会回绕:prompt=completion=MaxInt64 时和是 -2,闸门判成「没有可
// 计费用量」,整笔免单 —— 溢出发生在**判据**上,金额侧的饱和保护一点都用不上。
func TestUpstreamTokenCountsCannotOverflowTheBillableGate(t *testing.T) {
	gin.SetMode(gin.TestMode)
	w := httptest.NewRecorder()
	ctx, _ := gin.CreateTestContext(w)

	cases := []struct {
		name             string
		prompt           int
		completion       int
		wantPrompt       int
		wantCompletion   int
		wantTotalTokens  int
		wantBillableGate bool
	}{
		{
			name:             "两个分量都报 MaxInt64:相加会回绕成 -2",
			prompt:           math.MaxInt64,
			completion:       math.MaxInt64,
			wantPrompt:       math.MaxInt32,
			wantCompletion:   math.MaxInt32,
			wantTotalTokens:  math.MaxInt32,
			wantBillableGate: true,
		},
		{
			name:             "只有 prompt 溢出",
			prompt:           math.MaxInt64,
			completion:       10,
			wantPrompt:       math.MaxInt32,
			wantCompletion:   10,
			wantTotalTokens:  math.MaxInt32,
			wantBillableGate: true,
		},
		{
			name:             "负的 completion 不得把真实发生的 prompt 一起吃掉",
			prompt:           100000,
			completion:       -100000,
			wantPrompt:       100000,
			wantCompletion:   0,
			wantTotalTokens:  100000,
			wantBillableGate: true,
		},
		{
			name:             "两路都是负数",
			prompt:           -2000000000,
			completion:       -2000000000,
			wantPrompt:       0,
			wantCompletion:   0,
			wantTotalTokens:  0,
			wantBillableGate: false,
		},
		{
			name:             "正常用量原样通过",
			prompt:           10,
			completion:       10,
			wantPrompt:       10,
			wantCompletion:   10,
			wantTotalTokens:  20,
			wantBillableGate: true,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			summary := calculateTextQuotaSummary(ctx, upstreamUsageRelayInfo(), &dto.Usage{
				PromptTokens:     tc.prompt,
				CompletionTokens: tc.completion,
			})

			assert.Equal(t, tc.wantPrompt, summary.PromptTokens)
			assert.Equal(t, tc.wantCompletion, summary.CompletionTokens)
			assert.Equal(t, tc.wantTotalTokens, summary.TotalTokens)
			assert.Equal(t, tc.wantBillableGate, summary.hasBillableUsage(),
				"这一步为 false 就等于整笔免单")
			if tc.wantBillableGate {
				assert.Positive(t, summary.Quota, "闸门放行了就必须真的算出钱来")
			}
		})
	}
}

// cached_tokens 在 OpenAI 语义下按协议是 prompt_tokens 的子集(base 正是从
// prompt 里减掉它)。上游报一个大于 prompt 的值时,那一段是净加上去的:单次
// 请求即可把余额扣到 int32 饱和,扣费与真实 prompt 规模彻底脱钩。
func TestUpstreamSubsetTokensAreCappedAtPromptTokens(t *testing.T) {
	gin.SetMode(gin.TestMode)
	w := httptest.NewRecorder()
	ctx, _ := gin.CreateTestContext(w)

	t.Run("cached_tokens 大于 prompt_tokens 时夹回 prompt", func(t *testing.T) {
		summary := calculateTextQuotaSummary(ctx, upstreamUsageRelayInfo(), &dto.Usage{
			PromptTokens:     10,
			CompletionTokens: 10,
			PromptTokensDetails: dto.InputTokenDetails{
				CachedTokens: 2000000000,
			},
		})

		require.Equal(t, 10, summary.CacheTokens)
		// max(10-10,0)*1 + 10*1(cache) + 10*1(completion) = 20
		assert.Equal(t, 20, summary.Quota)
	})

	t.Run("image_tokens 大于 prompt_tokens 时夹回 prompt", func(t *testing.T) {
		summary := calculateTextQuotaSummary(ctx, upstreamUsageRelayInfo(), &dto.Usage{
			PromptTokens:     10,
			CompletionTokens: 10,
			PromptTokensDetails: dto.InputTokenDetails{
				ImageTokens: 2000000000,
			},
		})

		require.Equal(t, 10, summary.ImageTokens)
		assert.Equal(t, 20, summary.Quota)
	})

	t.Run("cache_write 大于 prompt_tokens 时夹回 prompt", func(t *testing.T) {
		relayInfo := upstreamUsageRelayInfo()
		relayInfo.PriceData.CacheCreationRatio = 1
		summary := calculateTextQuotaSummary(ctx, relayInfo, &dto.Usage{
			PromptTokens:     10,
			CompletionTokens: 10,
			PromptTokensDetails: dto.InputTokenDetails{
				CacheWriteTokens: 2000000000,
			},
		})

		require.Equal(t, 10, summary.CacheCreationTokens)
		assert.Equal(t, 20, summary.Quota)
	})

	t.Run("合法的子集关系一个字节都不动", func(t *testing.T) {
		relayInfo := upstreamUsageRelayInfo()
		relayInfo.PriceData.CacheRatio = 0.5
		summary := calculateTextQuotaSummary(ctx, relayInfo, &dto.Usage{
			PromptTokens:     1000,
			CompletionTokens: 100,
			PromptTokensDetails: dto.InputTokenDetails{
				CachedTokens: 400,
			},
		})

		require.Equal(t, 400, summary.CacheTokens)
		require.Equal(t, 1000, summary.PromptTokens)
		// (1000-400) + 400*0.5 + 100*1 = 900
		assert.Equal(t, 900, summary.Quota)
	})
}

// Claude 语义下 prompt_tokens 本就不含缓存段,不存在子集关系 —— 夹了才是错的。
func TestClaudeSemanticCacheTokensAreNotCappedAtPromptTokens(t *testing.T) {
	gin.SetMode(gin.TestMode)
	w := httptest.NewRecorder()
	ctx, _ := gin.CreateTestContext(w)

	relayInfo := upstreamUsageRelayInfo()
	relayInfo.FinalRequestRelayFormat = types.RelayFormatClaude
	relayInfo.PriceData.CacheRatio = 0.1

	summary := calculateTextQuotaSummary(ctx, relayInfo, &dto.Usage{
		PromptTokens:     10,
		CompletionTokens: 10,
		PromptTokensDetails: dto.InputTokenDetails{
			CachedTokens: 5000,
		},
	})

	require.True(t, summary.IsClaudeUsageSemantic)
	require.Equal(t, 5000, summary.CacheTokens)
	// Claude 语义:base 不减缓存。10 + 5000*0.1 + 10 = 520
	assert.Equal(t, 520, summary.Quota)
}
