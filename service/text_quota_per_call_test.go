package service

import (
	"net/http/httptest"
	"testing"
	"time"

	relaycommon "github.com/QuantumNous/new-api/relay/common"
	"github.com/QuantumNous/new-api/relaykit/dto"
	hosttypes "github.com/QuantumNous/new-api/types"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
)

// Per-call billing is defined as "one successful call, one fixed price": the
// charge is ModelPrice * QuotaPerUnit * GroupRatio and is provably independent
// of the token counts (a 2-token and a 7,702-token prompt both cost 20,000).
// The "upstream returned no usage, we cannot charge" fallback keyed off
// TotalTokens applied to that branch too, so an upstream that answered with an
// explicitly zeroed usage block made the call free — and the pre-consumed
// amount was refunded on top.
//
// Restoring the unconditional zeroing turns the zero-usage row red.
func TestPerCallQuotaIsIndependentOfUpstreamTokenCounts(t *testing.T) {
	gin.SetMode(gin.TestMode)

	const modelPrice = 0.04 // -> 0.04 * 500000 * 1 = 20000

	cases := []struct {
		name  string
		usage *dto.Usage
		want  int
	}{
		{
			name:  "ordinary usage",
			usage: &dto.Usage{PromptTokens: 2, CompletionTokens: 9, TotalTokens: 11},
			want:  20000,
		},
		{
			name:  "large usage costs the same",
			usage: &dto.Usage{PromptTokens: 7702, CompletionTokens: 119, TotalTokens: 7821},
			want:  20000,
		},
		{
			name:  "explicitly zeroed upstream usage still costs one call",
			usage: &dto.Usage{PromptTokens: 0, CompletionTokens: 0, TotalTokens: 0},
			want:  20000,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			w := httptest.NewRecorder()
			ctx, _ := gin.CreateTestContext(w)

			relayInfo := &relaycommon.RelayInfo{
				OriginModelName: "per-call-model",
				StartTime:       time.Now(),
				PriceData: hosttypes.PriceData{
					UsePrice:       true,
					ModelPrice:     modelPrice,
					GroupRatioInfo: hosttypes.GroupRatioInfo{GroupRatio: 1},
				},
			}

			summary := calculateTextQuotaSummary(ctx, relayInfo, tc.usage)
			assert.Equal(t, tc.want, summary.Quota)
		})
	}
}

// The same fallback must stay in place for per-token billing, where a missing
// token count genuinely means the charge cannot be computed.
func TestPerTokenQuotaStaysZeroWhenUpstreamReportsNoUsage(t *testing.T) {
	gin.SetMode(gin.TestMode)
	w := httptest.NewRecorder()
	ctx, _ := gin.CreateTestContext(w)

	relayInfo := &relaycommon.RelayInfo{
		OriginModelName: "per-token-model",
		StartTime:       time.Now(),
		PriceData: hosttypes.PriceData{
			ModelRatio:      0.6,
			CompletionRatio: 5,
			GroupRatioInfo:  hosttypes.GroupRatioInfo{GroupRatio: 1},
		},
	}

	summary := calculateTextQuotaSummary(ctx, relayInfo, &dto.Usage{})
	assert.Equal(t, 0, summary.Quota)
}
