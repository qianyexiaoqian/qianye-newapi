package service

import (
	"testing"

	"github.com/QuantumNous/new-api/model"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// When a subscription settlement overshoots amount_total the excess used to be
// dropped on the floor: model.PostConsumeUserSubscriptionDelta refused the
// whole write, SubscriptionFunding.Settle propagated the error, and
// PostTextConsumeQuota only logged it — after having already written a consume
// log for the full amount. The gateway billed 14,566 and collected 4,200.
//
// The subscription cap stays a real cap; the uncollected remainder is charged
// to the wallet instead, so "logged == collected" holds. Removing the wallet
// top-up (or restoring the all-or-nothing write) turns these red.
func TestSubscriptionSettleChargesTheOverCapRemainderToTheWallet(t *testing.T) {
	const (
		userId      = 6301
		subId       = 6401
		startQuota  = 1_000_000
		amountTotal = 4200
	)

	cases := []struct {
		name              string
		amountUsedBefore  int64
		settleDelta       int
		wantAmountUsed    int64
		wantWalletCharged int
	}{
		{
			name:              "delta fits inside the subscription",
			amountUsedBefore:  1000,
			settleDelta:       500,
			wantAmountUsed:    1500,
			wantWalletCharged: 0,
		},
		{
			name:              "subscription already at its cap, whole delta goes to the wallet",
			amountUsedBefore:  amountTotal,
			settleDelta:       10366,
			wantAmountUsed:    amountTotal,
			wantWalletCharged: 10366,
		},
		{
			name:              "delta straddles the cap, remainder goes to the wallet",
			amountUsedBefore:  3000,
			settleDelta:       5000,
			wantAmountUsed:    amountTotal,
			wantWalletCharged: 3800,
		},
		{
			name:              "refund is applied to the subscription, wallet untouched",
			amountUsedBefore:  3004,
			settleDelta:       -2976,
			wantAmountUsed:    28,
			wantWalletCharged: 0,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			truncate(t)
			seedUser(t, userId, startQuota)
			seedSubscription(t, subId, userId, amountTotal, tc.amountUsedBefore)

			funding := &SubscriptionFunding{userId: userId, subscriptionId: subId}
			require.NoError(t, funding.Settle(tc.settleDelta))

			var sub model.UserSubscription
			require.NoError(t, model.DB.Where("id = ?", subId).First(&sub).Error)
			assert.Equal(t, tc.wantAmountUsed, sub.AmountUsed)
			assert.LessOrEqual(t, sub.AmountUsed, sub.AmountTotal)

			quotaAfter, err := model.GetUserQuota(userId, true)
			require.NoError(t, err)
			assert.Equal(t, startQuota-tc.wantWalletCharged, quotaAfter)

			assert.Equal(t, int64(tc.wantWalletCharged), funding.SettleWalletShortfall())
			assert.Equal(t, int64(tc.settleDelta)-int64(tc.wantWalletCharged), funding.SettleApplied())

			// The whole point: nothing is lost between the two sources.
			collected := int(funding.SettleApplied()) + int(funding.SettleWalletShortfall())
			assert.Equal(t, tc.settleDelta, collected,
				"subscription + wallet must add up to the settled delta")
		})
	}
}
