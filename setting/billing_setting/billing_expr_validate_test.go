package billing_setting

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// pkg/billingexpr/expr.md promises that a billing expression is compiled and
// smoke-tested for non-negative results on save. The smoke test existed but had
// zero callers, so a syntactically broken expression could be persisted (400ing
// every request for that model, surviving restarts) and an arithmetically
// negative one turned settlement into a credit. controller/ratio_sync.go writes
// this same key from a remote site's pricing feed, which makes the gap remotely
// reachable.
//
// Removing the smoke-test call from ValidateBillingExprJSON turns every
// wantErr row here green-to-red.
func TestValidateBillingExprJSONRejectsBrokenAndNegativeExpressions(t *testing.T) {
	cases := []struct {
		name    string
		value   string
		wantErr bool
	}{
		{
			name:  "empty value is accepted",
			value: "",
		},
		{
			name:  "empty map is accepted",
			value: `{}`,
		},
		{
			name:  "blank expression for a model is skipped",
			value: `{"m":"   "}`,
		},
		{
			name:  "well-formed non-negative expression",
			value: `{"m":"tier(\"base\", p * 3 + c * 15)"}`,
		},
		{
			name:  "tiered expression with a request probe",
			value: `{"m":"param(\"service_tier\") == \"fast\" ? tier(\"fast\", p * 4) : tier(\"normal\", p * 2)"}`,
		},
		{
			name:    "expression that goes negative for small prompts",
			value:   `{"m":"tier(\"promo\", p * 3 - 20000)"}`,
			wantErr: true,
		},
		{
			name:    "expression that is negative everywhere",
			value:   `{"m":"tier(\"credit\", 0 - p)"}`,
			wantErr: true,
		},
		{
			name:    "syntactically broken expression",
			value:   `{"m":"tier(\"base\", p * )"}`,
			wantErr: true,
		},
		{
			name:    "not a JSON object",
			value:   `["tier(\"base\", p)"]`,
			wantErr: true,
		},
		{
			name:    "truncated JSON",
			value:   `{"m":"tier(\"base\", p)"`,
			wantErr: true,
		},
		{
			name:    "one bad model among several rejects the whole write",
			value:   `{"good":"tier(\"base\", p * 3)","bad":"tier(\"promo\", p - 999999)"}`,
			wantErr: true,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := ValidateBillingExprJSON(tc.value)
			if tc.wantErr {
				require.Error(t, err)
				return
			}
			assert.NoError(t, err)
		})
	}
}

func TestBillingExprOptionKeyMatchesTheRegisteredConfigKey(t *testing.T) {
	// The pre-persist validator switches on this literal key; if the config
	// prefix or the json tag ever changes, the validator silently stops running.
	assert.Equal(t, "billing_setting.billing_expr", BillingExprOptionKey)
}
