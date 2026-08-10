package model

import (
	"testing"

	"github.com/QuantumNous/new-api/common"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// quota_batch_visibility_test.go — a spendable balance must never be deferred.
//
// `users.quota` and `tokens.remain_quota` are gates: PreConsumeUserQuota /
// PreConsumeTokenQuota read them inside the WHERE clause of the very statement
// that spends them. Pre-consume therefore cannot be queued (a queued delta
// carries no condition), which means *every* counterpart of a pre-consume —
// refund, settle delta, rollback — has to be written through as well.
//
// Leaving the counterparts on the batch-update queue produced a
// database-visible dip that lasted a whole BATCH_UPDATE_INTERVAL. Measured on
// the live gateway with BATCH_UPDATE_ENABLED=true: a wallet holding 30,000 with
// a 27,000 pre-consume showed 3,000 in the database while the user really had
// 29,972, and the immediately following request was rejected with
// insufficient_user_quota. Same shape on the token side, where
// IncreaseTokenQuota did not even take a `db` flag, so no caller could opt out.
//
// These tests run with BatchUpdateEnabled forced on — that is the whole point.
// Re-introducing `if common.BatchUpdateEnabled { addNewRecord(...); return }`
// in any of the four functions turns them red on the very next read.

// withBatchUpdate turns the batch-update queue on for one test and restores the
// previous setting afterwards.
func withBatchUpdate(t *testing.T) {
	t.Helper()
	previous := common.BatchUpdateEnabled
	common.BatchUpdateEnabled = true
	t.Cleanup(func() { common.BatchUpdateEnabled = previous })
}

func TestWalletMovementsAreVisibleImmediatelyUnderBatchUpdate(t *testing.T) {
	cases := []struct {
		name    string
		balance int
		apply   func(t *testing.T, id int) //nolint:thelper // each case is the assertion subject
		want    int
	}{
		{
			name:    "refund of a pre-consume",
			balance: 30000,
			apply: func(t *testing.T, id int) {
				require.NoError(t, PreConsumeUserQuota(id, 27000))
				require.NoError(t, IncreaseUserQuota(id, 27000))
			},
			want: 30000,
		},
		{
			name:    "settle delta returns the unused reservation",
			balance: 30000,
			apply: func(t *testing.T, id int) {
				require.NoError(t, PreConsumeUserQuota(id, 27000))
				// actual 28, pre-consumed 27000 → give back 26972.
				require.NoError(t, IncreaseUserQuota(id, 26972))
			},
			want: 29972,
		},
		{
			name:    "settle delta charges more than reserved",
			balance: 30000,
			apply: func(t *testing.T, id int) {
				require.NoError(t, PreConsumeUserQuota(id, 27000))
				require.NoError(t, DecreaseUserQuota(id, 500))
			},
			want: 2500,
		},
		{
			name:    "subscription shortfall charged to the wallet",
			balance: 30000,
			apply: func(t *testing.T, id int) {
				require.NoError(t, DecreaseUserQuota(id, 4523))
			},
			want: 25477,
		},
		{
			name:    "debt is still recorded, never deferred",
			balance: 100,
			apply: func(t *testing.T, id int) {
				require.NoError(t, DecreaseUserQuota(id, 10759))
			},
			want: -10659,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			withBatchUpdate(t)
			truncateTables(t)
			user := &User{Id: 7801, Username: "batch-visibility", Quota: tc.balance}
			require.NoError(t, DB.Create(user).Error)

			tc.apply(t, user.Id)

			var after User
			require.NoError(t, DB.Where("id = ?", user.Id).First(&after).Error)
			assert.Equal(t, tc.want, after.Quota,
				"the database must already show the settled balance; a queued counterpart leaves a dip that rejects the user's next request")
		})
	}
}

// The dip is only observable through the gate, so assert it there too: a user
// whose reservation was returned must be able to spend that money again right
// away, with no flush in between.
func TestReturnedReservationIsSpendableAgainWithoutWaitingForAFlush(t *testing.T) {
	withBatchUpdate(t)
	truncateTables(t)
	require.NoError(t, DB.Create(&User{Id: 7802, Username: "batch-gate", Quota: 30000}).Error)

	require.NoError(t, PreConsumeUserQuota(7802, 27000))
	require.NoError(t, IncreaseUserQuota(7802, 26972)) // settle: actual cost 28

	require.NoError(t, PreConsumeUserQuota(7802, 27000),
		"the second request was rejected while the user really had 29,972 — that is the batch-update dip")

	var after User
	require.NoError(t, DB.Where("id = ?", 7802).First(&after).Error)
	assert.Equal(t, 2972, after.Quota)
}

func TestTokenMovementsAreVisibleImmediatelyUnderBatchUpdate(t *testing.T) {
	cases := []struct {
		name        string
		remain      int
		unlimited   bool
		apply       func(t *testing.T, tok *Token) //nolint:thelper // each case is the assertion subject
		wantRemain  int
		wantUsedGTE int
	}{
		{
			name:   "rollback after the funding side refused",
			remain: 30000,
			apply: func(t *testing.T, tok *Token) {
				require.NoError(t, PreConsumeTokenQuota(tok.Id, tok.Key, 27000, false))
				require.NoError(t, IncreaseTokenQuota(tok.Id, tok.Key, 27000))
			},
			wantRemain: 30000,
		},
		{
			name:   "settle delta returns the unused reservation",
			remain: 30000,
			apply: func(t *testing.T, tok *Token) {
				require.NoError(t, PreConsumeTokenQuota(tok.Id, tok.Key, 27000, false))
				require.NoError(t, IncreaseTokenQuota(tok.Id, tok.Key, 26972))
			},
			wantRemain:  29972,
			wantUsedGTE: 28,
		},
		{
			name:   "settle delta charges more than reserved",
			remain: 30000,
			apply: func(t *testing.T, tok *Token) {
				require.NoError(t, PreConsumeTokenQuota(tok.Id, tok.Key, 27000, false))
				require.NoError(t, DecreaseTokenQuota(tok.Id, tok.Key, 500))
			},
			wantRemain:  2500,
			wantUsedGTE: 27500,
		},
		{
			name:      "unlimited token keeps counting without a cap",
			remain:    0,
			unlimited: true,
			apply: func(t *testing.T, tok *Token) {
				require.NoError(t, PreConsumeTokenQuota(tok.Id, tok.Key, 27000, true))
				require.NoError(t, IncreaseTokenQuota(tok.Id, tok.Key, 26972))
			},
			wantRemain:  -28,
			wantUsedGTE: 28,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			withBatchUpdate(t)
			truncateTables(t)
			tok := &Token{
				Id:             7811,
				UserId:         7810,
				Key:            "batch-visibility-key",
				Name:           "batch-visibility",
				RemainQuota:    tc.remain,
				UnlimitedQuota: tc.unlimited,
			}
			require.NoError(t, DB.Create(tok).Error)

			tc.apply(t, tok)

			var after Token
			require.NoError(t, DB.Where("id = ?", tok.Id).First(&after).Error)
			assert.Equal(t, tc.wantRemain, after.RemainQuota,
				"remain_quota must already reflect the settled amount; a queued counterpart rejects the token's next request")
			assert.GreaterOrEqual(t, after.UsedQuota, tc.wantUsedGTE)
		})
	}
}

// The accumulators are not gates and stay batched — removing that would throw
// away the optimisation for no correctness gain. This pins which side of the
// line each column is on.
func TestPureAccumulatorsStayOnTheBatchQueue(t *testing.T) {
	withBatchUpdate(t)
	truncateTables(t)
	require.NoError(t, DB.Create(&User{Id: 7803, Username: "batch-accumulator", Quota: 30000}).Error)

	UpdateUserUsedQuotaAndRequestCount(7803, 1234)

	var after User
	require.NoError(t, DB.Where("id = ?", 7803).First(&after).Error)
	assert.Zero(t, after.UsedQuota, "used_quota is a write-only counter and is still deferred")
	assert.Zero(t, after.RequestCount)
	assert.Equal(t, 30000, after.Quota, "the accumulator path must never touch the spendable balance")

	batchUpdate()

	require.NoError(t, DB.Where("id = ?", 7803).First(&after).Error)
	assert.Equal(t, 1234, after.UsedQuota)
	assert.Equal(t, 1, after.RequestCount)
	assert.Equal(t, 30000, after.Quota)
}
