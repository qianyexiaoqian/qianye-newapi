package service

import (
	"context"
	"testing"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/model"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func seedMidjourney(t *testing.T, task *model.Midjourney) *model.Midjourney {
	t.Helper()
	require.NoError(t, model.DB.AutoMigrate(&model.Midjourney{}))
	require.NoError(t, model.DB.Create(task).Error)
	t.Cleanup(func() { model.DB.Exec("DELETE FROM midjourneys") })
	return task
}

func userUsedQuota(t *testing.T, id int) int {
	t.Helper()
	var user model.User
	require.NoError(t, model.DB.Select("used_quota").Where("id = ?", id).First(&user).Error)
	return user.UsedQuota
}

func channelUsedQuota(t *testing.T, id int) int64 {
	t.Helper()
	var ch model.Channel
	require.NoError(t, model.DB.Select("used_quota").Where("id = ?", id).First(&ch).Error)
	return ch.UsedQuota
}

func midjourneyQuota(t *testing.T, id int) int {
	t.Helper()
	var task model.Midjourney
	require.NoError(t, model.DB.Select("quota").Where("id = ?", id).First(&task).Error)
	return task.Quota
}

// 一次 Midjourney 退款必须把五个账面元素同时反转：钱包额度、令牌额度、
// 用户累计用量、渠道累计用量、一条退款台账；并且清零 task.Quota 以便下一轮
// 轮询看到同一条失败任务时不再退第二次。
//
// 表里三行分别锁住三条独立的判据：
//   - 令牌那一份以前根本不退（TokenId 是这次同步才落到任务行上的）；
//   - 渠道用量按 billing_channel_id 而不是随重试漂移的 channel_id 回减；
//   - billing_channel_id 为 0 的历史行必须回落到 channel_id，不能把用量记到 0 号渠道。
func TestRefundMidjourneyQuotaReversesEveryLedgerElement(t *testing.T) {
	const (
		userID         = 1
		tokenID        = 1
		submitChannel  = 7
		billingChannel = 9
		initQuota      = 10_000
		initUsedQuota  = 4_000
		tokenRemain    = 5_000
		taskQuota      = 3_000
	)

	cases := []struct {
		name string
		// 任务行上的账本身份
		taskTokenId          int
		taskChannelId        int
		taskBillingChannelId int
		// 期望
		wantTokenRemain     int
		wantChargedChannel  int
		wantUntouchedChanel int
	}{
		{
			name:                 "token and billing channel recorded",
			taskTokenId:          tokenID,
			taskChannelId:        submitChannel,
			taskBillingChannelId: billingChannel,
			wantTokenRemain:      tokenRemain + taskQuota,
			wantChargedChannel:   billingChannel,
			wantUntouchedChanel:  submitChannel,
		},
		{
			name:                 "legacy row without billing channel falls back to channel_id",
			taskTokenId:          tokenID,
			taskChannelId:        submitChannel,
			taskBillingChannelId: 0,
			wantTokenRemain:      tokenRemain + taskQuota,
			wantChargedChannel:   submitChannel,
			wantUntouchedChanel:  billingChannel,
		},
		{
			name:                 "legacy row without token id leaves token untouched",
			taskTokenId:          0,
			taskChannelId:        submitChannel,
			taskBillingChannelId: billingChannel,
			wantTokenRemain:      tokenRemain,
			wantChargedChannel:   billingChannel,
			wantUntouchedChanel:  submitChannel,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			truncate(t)
			ctx := context.Background()

			require.NoError(t, model.DB.Create(&model.User{
				Id: userID, Username: "mj_user", Quota: initQuota,
				UsedQuota: initUsedQuota, Status: common.UserStatusEnabled,
			}).Error)
			seedToken(t, tokenID, userID, "sk-mj-refund", tokenRemain)
			require.NoError(t, model.DB.Create(&model.Channel{
				Id: submitChannel, Name: "submit", Key: "sk-a",
				Status: common.ChannelStatusEnabled, UsedQuota: taskQuota,
			}).Error)
			require.NoError(t, model.DB.Create(&model.Channel{
				Id: billingChannel, Name: "billing", Key: "sk-b",
				Status: common.ChannelStatusEnabled, UsedQuota: taskQuota,
			}).Error)

			task := seedMidjourney(t, &model.Midjourney{
				UserId:           userID,
				MjId:             "mj-refund-1",
				Action:           "IMAGINE",
				Progress:         "100%",
				Status:           "FAILURE",
				Quota:            taskQuota,
				TokenId:          tc.taskTokenId,
				ChannelId:        tc.taskChannelId,
				BillingChannelId: tc.taskBillingChannelId,
			})

			require.True(t, RefundMidjourneyQuota(ctx, task, "构图失败"))

			assert.Equal(t, initQuota+taskQuota, getUserQuota(t, userID),
				"wallet quota must be credited back")
			assert.Equal(t, initUsedQuota-taskQuota, userUsedQuota(t, userID),
				"used_quota must be rolled back so quota+used_quota stays flat")
			assert.Equal(t, tc.wantTokenRemain, getTokenRemainQuota(t, tokenID))
			assert.Equal(t, int64(0), channelUsedQuota(t, tc.wantChargedChannel),
				"the channel that was actually charged must be rolled back")
			assert.Equal(t, int64(taskQuota), channelUsedQuota(t, tc.wantUntouchedChanel),
				"the other channel must not be touched")

			log := getLastLog(t)
			require.NotNil(t, log)
			assert.Equal(t, model.LogTypeRefund, log.Type)
			assert.Equal(t, taskQuota, log.Quota)
			assert.Equal(t, tc.wantChargedChannel, log.ChannelId)
			assert.Equal(t, "mj_imagine", log.ModelName)

			// 幂等闩：Quota 已清零，再退一次不能动任何一个数。
			assert.Zero(t, task.Quota)
			assert.Zero(t, midjourneyQuota(t, task.Id))
			require.True(t, RefundMidjourneyQuota(ctx, task, "构图失败"))
			assert.Equal(t, initQuota+taskQuota, getUserQuota(t, userID))
			assert.Equal(t, initUsedQuota-taskQuota, userUsedQuota(t, userID))
			assert.Equal(t, tc.wantTokenRemain, getTokenRemainQuota(t, tokenID))
		})
	}
}
