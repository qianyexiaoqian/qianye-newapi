package model

import (
	"errors"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// The wallet / token pre-consume gates used to read the balance, compare it in
// Go, and then issue an unconditional decrement. That is a
// time-of-check/time-of-use race: the balance predicate and the deduction have
// to be the same statement, otherwise concurrent requests each observe the same
// sufficient balance and all of them deduct, breaching the cap (measured on the
// live gateway: 12 concurrent per-call requests against a 10,000 wallet all
// succeeded, leaving -110,000; 8 of 12 against a 10,000 limited token left
// -70,000).
//
// These tests pin the observable contract of the conditional statements: an
// amount the balance cannot cover must be rejected *and* must leave the row
// untouched. Dropping the `AND quota >= ?` / `AND remain_quota >= ?` predicate
// turns the "insufficient" rows green-to-red immediately, because the deduction
// then succeeds and drives the balance negative.

func TestPreConsumeUserQuotaNeverOverdrawsTheWallet(t *testing.T) {
	cases := []struct {
		name        string
		balance     int
		request     int
		wantErr     bool
		wantBalance int
	}{
		{name: "covers the request", balance: 1000, request: 400, wantErr: false, wantBalance: 600},
		{name: "exactly covers the request", balance: 1000, request: 1000, wantErr: false, wantBalance: 0},
		{name: "one short", balance: 1000, request: 1001, wantErr: true, wantBalance: 1000},
		{name: "far short", balance: 1000, request: 17385, wantErr: true, wantBalance: 1000},
		{name: "empty wallet", balance: 0, request: 1, wantErr: true, wantBalance: 0},
		{name: "already in debt", balance: -500, request: 1, wantErr: true, wantBalance: -500},
		{name: "zero amount is a no-op", balance: 1000, request: 0, wantErr: false, wantBalance: 1000},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			truncateTables(t)
			user := &User{Id: 7701, Username: "preconsume-guard", Quota: tc.balance}
			require.NoError(t, DB.Create(user).Error)

			err := PreConsumeUserQuota(user.Id, tc.request)
			if tc.wantErr {
				require.Error(t, err)
				assert.True(t, errors.Is(err, ErrInsufficientUserQuota),
					"insufficient balance must surface ErrInsufficientUserQuota, got %v", err)
			} else {
				require.NoError(t, err)
			}

			var after User
			require.NoError(t, DB.Where("id = ?", user.Id).First(&after).Error)
			assert.Equal(t, tc.wantBalance, after.Quota)
		})
	}
}

func TestPreConsumeUserQuotaRejectsNegativeAmount(t *testing.T) {
	truncateTables(t)
	require.NoError(t, DB.Create(&User{Id: 7702, Username: "preconsume-neg", Quota: 1000}).Error)

	require.Error(t, PreConsumeUserQuota(7702, -1))

	var after User
	require.NoError(t, DB.Where("id = ?", 7702).First(&after).Error)
	assert.Equal(t, 1000, after.Quota)
}

func TestPreConsumeTokenQuotaNeverBreachesALimitedTokenCap(t *testing.T) {
	cases := []struct {
		name       string
		remain     int
		used       int
		unlimited  bool
		request    int
		wantErr    bool
		wantRemain int
		wantUsed   int
	}{
		{name: "limited token covers it", remain: 10000, used: 0, request: 4000, wantRemain: 6000, wantUsed: 4000},
		{name: "limited token exactly covers it", remain: 10000, used: 5, request: 10000, wantRemain: 0, wantUsed: 10005},
		{name: "limited token one short", remain: 10000, used: 0, request: 10001, wantErr: true, wantRemain: 10000, wantUsed: 0},
		{name: "limited token far short", remain: 1200, used: 0, request: 1502, wantErr: true, wantRemain: 1200, wantUsed: 0},
		{name: "unlimited token ignores the cap", remain: 0, used: 0, unlimited: true, request: 10000, wantRemain: -10000, wantUsed: 10000},
		{name: "zero amount is a no-op", remain: 10000, used: 0, request: 0, wantRemain: 10000, wantUsed: 0},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			truncateTables(t)
			token := &Token{
				Id:             8801,
				UserId:         7701,
				Key:            "preconsume-guard-key",
				Name:           "preconsume-guard",
				RemainQuota:    tc.remain,
				UsedQuota:      tc.used,
				UnlimitedQuota: tc.unlimited,
			}
			require.NoError(t, DB.Create(token).Error)

			err := PreConsumeTokenQuota(token.Id, token.Key, tc.request, tc.unlimited)
			if tc.wantErr {
				require.Error(t, err)
				assert.True(t, errors.Is(err, ErrInsufficientTokenQuota),
					"insufficient token quota must surface ErrInsufficientTokenQuota, got %v", err)
			} else {
				require.NoError(t, err)
			}

			var after Token
			require.NoError(t, DB.Where("id = ?", token.Id).First(&after).Error)
			assert.Equal(t, tc.wantRemain, after.RemainQuota)
			assert.Equal(t, tc.wantUsed, after.UsedQuota)
		})
	}
}
