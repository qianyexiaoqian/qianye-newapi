package model

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// A settlement delta that pushes amount_used past amount_total used to make
// PostConsumeUserSubscriptionDelta refuse the entire write. Refusing is correct
// for a reservation, but at settle time the request has already been served, so
// the refused part was simply never collected: the consume log recorded the
// full charge, the subscription stayed at its cap and the wallet was untouched
// (measured on the live gateway: billed 14,566, collected 4,200).
//
// SettleUserSubscriptionDelta clamps to the cap and reports how much landed, so
// the caller can charge the remainder to the wallet. Removing the clamp (going
// back to an error, or letting amount_used exceed amount_total) breaks the
// `applied` expectations below.
func TestSettleUserSubscriptionDeltaClampsToTotalAndReportsApplied(t *testing.T) {
	cases := []struct {
		name        string
		total       int64
		used        int64
		delta       int64
		wantApplied int64
		wantUsed    int64
	}{
		{name: "fits entirely", total: 12000, used: 1000, delta: 500, wantApplied: 500, wantUsed: 1500},
		{name: "exactly reaches the cap", total: 12000, used: 11500, delta: 500, wantApplied: 500, wantUsed: 12000},
		{name: "overshoots the cap", total: 4200, used: 4200, delta: 10366, wantApplied: 0, wantUsed: 4200},
		{name: "partially overshoots the cap", total: 4200, used: 3000, delta: 5000, wantApplied: 1200, wantUsed: 4200},
		{name: "refund is applied in full", total: 12000, used: 3004, delta: -2976, wantApplied: -2976, wantUsed: 28},
		{name: "refund clamps at zero", total: 12000, used: 100, delta: -500, wantApplied: -100, wantUsed: 0},
		{name: "unlimited total has no cap", total: 0, used: 5, delta: 999999, wantApplied: 999999, wantUsed: 1000004},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			truncateTables(t)
			sub := &UserSubscription{
				Id:          9301,
				UserId:      101,
				PlanId:      1,
				AmountTotal: tc.total,
				AmountUsed:  tc.used,
				Status:      "active",
			}
			require.NoError(t, DB.Create(sub).Error)

			applied, err := SettleUserSubscriptionDelta(sub.Id, tc.delta)
			require.NoError(t, err)
			assert.Equal(t, tc.wantApplied, applied)

			var after UserSubscription
			require.NoError(t, DB.Where("id = ?", sub.Id).First(&after).Error)
			assert.Equal(t, tc.wantUsed, after.AmountUsed)
			if tc.total > 0 {
				assert.LessOrEqual(t, after.AmountUsed, after.AmountTotal,
					"amount_used must never exceed amount_total")
			}
		})
	}
}
