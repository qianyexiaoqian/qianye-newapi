package service

import (
	"context"
	"testing"

	"github.com/QuantumNous/new-api/model"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func getUserUsedQuotaAndRequestCount(t *testing.T, id int) (int, int) {
	t.Helper()
	var user model.User
	require.NoError(t, model.DB.Select("used_quota", "request_count").Where("id = ?", id).First(&user).Error)
	return user.UsedQuota, user.RequestCount
}

func getChannelUsedQuota(t *testing.T, id int) int {
	t.Helper()
	var ch model.Channel
	require.NoError(t, model.DB.Select("used_quota").Where("id = ?", id).First(&ch).Error)
	return int(ch.UsedQuota)
}

// 退款必须同步回减 used_quota，否则「总额度」(quota + used_quota) 随退款次数单调
// 虚增，最终超过用户真实充值总额 —— 用量报表与配额告警一起失真。
//
// 请求次数不跟着减：提交时已经计过一次请求，退款不是一次新请求，用
// UpdateUserUsedQuotaAndRequestCount 会把 request_count 也加一（方向还是错的）。
// 同步自上游 58d4e9bd3。
func TestRefundTaskQuotaReversesUsedQuotaWithoutTouchingRequestCount(t *testing.T) {
	truncate(t)
	ctx := context.Background()

	const userID, tokenID, channelID = 21, 21, 21
	const initQuota, preConsumed = 10000, 3000

	seedUser(t, userID, initQuota)
	seedToken(t, tokenID, userID, "sk-refund-used-quota", 5000)
	seedChannel(t, channelID)

	// 提交时刻：预扣额同时记进用户用量、请求次数、渠道用量。
	model.UpdateUserUsedQuotaAndRequestCount(userID, preConsumed)
	model.UpdateChannelUsedQuota(channelID, preConsumed)
	usedBefore, requestsBefore := getUserUsedQuotaAndRequestCount(t, userID)
	require.Equal(t, preConsumed, usedBefore)
	require.Equal(t, 1, requestsBefore)
	require.Equal(t, preConsumed, getChannelUsedQuota(t, channelID))

	task := makeTask(userID, channelID, preConsumed, tokenID, BillingSourceWallet, 0)
	require.NoError(t, model.DB.Create(task).Error)

	require.True(t, RefundTaskQuota(ctx, task, "task failed: upstream error"))

	usedAfter, requestsAfter := getUserUsedQuotaAndRequestCount(t, userID)
	assert.Equal(t, 0, usedAfter, "退款后累计用量必须回到提交前")
	assert.Equal(t, requestsBefore, requestsAfter, "退款不是一次新请求，请求次数不能变")
	assert.Equal(t, 0, getChannelUsedQuota(t, channelID), "渠道用量同样要回减")

	// 资金侧仍然按原样退回，这次改动没有动它。
	assert.Equal(t, initQuota+preConsumed, getUserQuota(t, userID))
}

// 差额结算的两个方向都要调整用量。改动前负差额那一支什么都不做，于是每一笔
// 「预扣多了、结算退回一部分」都在 used_quota 上永久留下那一部分虚增。
func TestSettleTaskQuotaDeltaAdjustsUsedQuotaInBothDirections(t *testing.T) {
	cases := []struct {
		name         string
		preConsumed  int
		actualQuota  int
		wantUsed     int
		wantChannel  int
		wantRequests int
	}{
		{name: "结算多扣（正差额）", preConsumed: 3000, actualQuota: 5000, wantUsed: 5000, wantChannel: 5000, wantRequests: 1},
		{name: "结算退回（负差额）", preConsumed: 3000, actualQuota: 1200, wantUsed: 1200, wantChannel: 1200, wantRequests: 1},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			truncate(t)
			ctx := context.Background()

			const userID, tokenID, channelID = 22, 22, 22
			seedUser(t, userID, 100000)
			seedToken(t, tokenID, userID, "sk-settle-used-quota", 100000)
			seedChannel(t, channelID)

			model.UpdateUserUsedQuotaAndRequestCount(userID, tc.preConsumed)
			model.UpdateChannelUsedQuota(channelID, tc.preConsumed)

			task := makeTask(userID, channelID, tc.preConsumed, tokenID, BillingSourceWallet, 0)
			require.NoError(t, model.DB.Create(task).Error)

			RecalculateTaskQuota(ctx, task, tc.actualQuota, "差额结算测试")

			used, requests := getUserUsedQuotaAndRequestCount(t, userID)
			assert.Equal(t, tc.wantUsed, used)
			assert.Equal(t, tc.wantChannel, getChannelUsedQuota(t, channelID))
			assert.Equal(t, tc.wantRequests, requests, "结算阶段不得再累加请求次数")
		})
	}
}
