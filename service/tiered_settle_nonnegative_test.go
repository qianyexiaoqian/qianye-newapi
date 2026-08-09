package service

import (
	"testing"

	"github.com/QuantumNous/new-api/pkg/billingexpr"
	relaycommon "github.com/QuantumNous/new-api/relay/common"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// Tiered expressions are free-form arithmetic written by the operator, and
// param(...) reads straight from the client's request body, so the result can
// legitimately come out negative ("first 20k tokens free" is a natural shape).
// Every other billing path has a floor — the per-token branch has `<=0 -> 1`,
// the per-call branch settles to 0 — but the tiered result was returned as-is.
// A negative quota reaches SettleBilling as a negative delta, the funding
// source runs Increase, and the charge becomes a top-up.
//
// Removing the clamp in TryTieredSettle makes the negative rows return their
// raw negative quota and turns this red.
func TestTryTieredSettleNeverReturnsANegativeQuota(t *testing.T) {
	cases := []struct {
		name       string
		expr       string
		groupRatio float64
		prompt     float64
		completion float64
		wantQuota  int
	}{
		{
			name:       "promo expression goes negative for a small prompt",
			expr:       `tier("promo", p * 3 - 20000)`,
			groupRatio: 1,
			prompt:     100,
			completion: 0,
			wantQuota:  0,
		},
		{
			name:       "expression that is negative everywhere",
			expr:       `tier("credit", 0 - p * 10)`,
			groupRatio: 1,
			prompt:     5000,
			completion: 10,
			wantQuota:  0,
		},
		{
			name:       "same promo expression stays positive above the threshold",
			expr:       `tier("promo", p * 3 - 20000)`,
			groupRatio: 1,
			prompt:     1_000_000,
			completion: 0,
			// (1e6*3 - 20000) / 1e6 * 500000 = 1_490_000
			wantQuota: 1_490_000,
		},
		{
			name:       "ordinary expression is unaffected",
			expr:       `tier("default", p * 2 + c * 10)`,
			groupRatio: 0.5,
			prompt:     1_000_000,
			completion: 100_000,
			// (2e6 + 1e6) / 1e6 * 500000 * 0.5 = 750_000
			wantQuota: 750_000,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			relayInfo := &relaycommon.RelayInfo{
				OriginModelName:       "tiered-nonneg-model",
				TieredBillingSnapshot: makeSnapshot(tc.expr, tc.groupRatio, 0, 0),
			}
			ok, quota, _ := TryTieredSettle(relayInfo, billingexpr.TokenParams{
				P: tc.prompt, C: tc.completion, Len: tc.prompt,
			})
			require.True(t, ok)
			assert.GreaterOrEqual(t, quota, 0, "a settlement must never be a credit")
			assert.Equal(t, tc.wantQuota, quota)
		})
	}
}
