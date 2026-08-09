package sora

import (
	"net/http/httptest"
	"testing"

	relaycommon "github.com/QuantumNous/new-api/relay/common"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// "seconds" becomes an OtherRatio, i.e. a direct multiplier on the request's
// quota, so it must be bounded before it reaches the quota calculation
// (AGENTS.md, Billing safety invariants). Two things were wrong here:
//
//   - no upper bound at all, while every sibling adaptor (gemini, vertex, ali,
//     the remix path) clamps to MaxTaskDurationSeconds; and
//   - the field priority was inverted relative to the validator. The validator
//     reads Duration first and only falls back to Seconds; billing read Seconds
//     first. `{"duration":1,"seconds":"40000"}` therefore validated as 1 second
//     and billed as 40,000 — a documented cap bypassed by one request body.
func TestEstimateBillingBoundsAndResolvesTheDurationLikeTheValidator(t *testing.T) {
	gin.SetMode(gin.TestMode)

	cases := []struct {
		name        string
		req         relaycommon.TaskSubmitReq
		wantSeconds float64
		wantSize    float64
	}{
		{
			name:        "duration only",
			req:         relaycommon.TaskSubmitReq{Duration: 8},
			wantSeconds: 8,
			wantSize:    1,
		},
		{
			name:        "seconds string only",
			req:         relaycommon.TaskSubmitReq{Seconds: "12"},
			wantSeconds: 12,
			wantSize:    1,
		},
		{
			name:        "duration wins over seconds, matching validateTaskDurationBounds",
			req:         relaycommon.TaskSubmitReq{Duration: 1, Seconds: "40000"},
			wantSeconds: 1,
			wantSize:    1,
		},
		{
			name:        "oversized duration is clamped to the documented cap",
			req:         relaycommon.TaskSubmitReq{Duration: 999999},
			wantSeconds: relaycommon.MaxTaskDurationSeconds,
			wantSize:    1,
		},
		{
			name:        "oversized seconds string is clamped too",
			req:         relaycommon.TaskSubmitReq{Seconds: "40000"},
			wantSeconds: relaycommon.MaxTaskDurationSeconds,
			wantSize:    1,
		},
		{
			name:        "neither field set falls back to the default",
			req:         relaycommon.TaskSubmitReq{},
			wantSeconds: 4,
			wantSize:    1,
		},
		{
			name:        "negative duration falls back to the default",
			req:         relaycommon.TaskSubmitReq{Duration: -100},
			wantSeconds: 4,
			wantSize:    1,
		},
		{
			name:        "portrait size carries its own multiplier",
			req:         relaycommon.TaskSubmitReq{Duration: 4, Size: "1024x1792"},
			wantSeconds: 4,
			wantSize:    1.666667,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			recorder := httptest.NewRecorder()
			ctx, _ := gin.CreateTestContext(recorder)
			ctx.Set("task_request", tc.req)

			adaptor := &TaskAdaptor{}
			ratios := adaptor.EstimateBilling(ctx, &relaycommon.RelayInfo{TaskRelayInfo: &relaycommon.TaskRelayInfo{}})
			require.NotNil(t, ratios)
			assert.Equal(t, tc.wantSeconds, ratios["seconds"])
			assert.Equal(t, tc.wantSize, ratios["size"])
		})
	}
}
