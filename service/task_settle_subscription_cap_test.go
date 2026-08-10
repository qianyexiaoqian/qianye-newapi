package service

import (
	"context"
	"testing"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/model"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// 异步任务的差额结算曾经沿用 model.PostConsumeUserSubscriptionDelta:
// 一旦 amount_used + delta 越过 amount_total,整笔写入被拒并返回 error,
// 而 settleTaskQuotaDelta 拿到 error 后直接 return —— 同时发生五件事:
// 套餐不收、钱包不补收、令牌不扣、task.Quota 不回写、账单日志一行不写。
// 超出套餐上限的那部分服务完全免费,且账面上没有任何一行指向它。
//
// relay 链路早已改用 SettleUserSubscriptionDelta(钳位 + 回报落点)+ 钱包补收,
// 异步任务链路漏了。这组用例钉死两条链路同一口径:套餐上限仍是硬上限,
// 收不到的那部分落到钱包,「日志记多少 == 实际收多少」。
//
// 把 taskAdjustFunding 换回 PostConsumeUserSubscriptionDelta,或者删掉钱包
// 补收那一段,下面的用例会红。
func TestTaskSettleChargesOverCapRemainderToWallet(t *testing.T) {
	const (
		userID      = 7301
		tokenID     = 7302
		channelID   = 7303
		subID       = 7304
		startQuota  = 1_000_000
		tokenRemain = 1_000_000
		preConsumed = 3000
	)

	cases := []struct {
		name string
		// 套餐上限与结算前已用量
		amountTotal      int64
		amountUsedBefore int64
		// 任务完成后的实际应扣额度
		actualQuota int
		// 独立算出的期望
		wantAmountUsed  int64
		wantWalletDelta int // 钱包被扣掉多少（正数=扣、负数=退）
		wantSubApplied  int64
		wantShortfall   int64
		wantLogType     int
		wantLogQuota    int
	}{
		{
			name:             "差额完全装得进套餐:钱包一分不动",
			amountTotal:      100000,
			amountUsedBefore: 3000,
			actualQuota:      5000,
			wantAmountUsed:   5000, // 3000 + delta 2000
			wantWalletDelta:  0,
			wantSubApplied:   2000,
			wantShortfall:    0,
			wantLogType:      model.LogTypeConsume,
			wantLogQuota:     2000,
		},
		{
			name:             "差额跨过上限:套餐吃到封顶,余下的钱包补收",
			amountTotal:      5000,
			amountUsedBefore: 3000,
			actualQuota:      12763,
			wantAmountUsed:   5000, // 封顶,不是 12763
			wantWalletDelta:  7763, // 9763 - 2000
			wantSubApplied:   2000, // 5000 - 3000
			wantShortfall:    7763, // delta 9763 撞上限后的余额
			wantLogType:      model.LogTypeConsume,
			wantLogQuota:     9763,
		},
		{
			name:             "套餐已经封顶:整笔差额都由钱包收",
			amountTotal:      4200,
			amountUsedBefore: 4200,
			actualQuota:      13566,
			wantAmountUsed:   4200,
			wantWalletDelta:  10566,
			wantSubApplied:   0,
			wantShortfall:    10566,
			wantLogType:      model.LogTypeConsume,
			wantLogQuota:     10566,
		},
		{
			name:             "退款照旧只走套餐:负差额绝不往钱包里补",
			amountTotal:      100000,
			amountUsedBefore: 3000,
			actualQuota:      1000,
			wantAmountUsed:   1000, // 3000 - 2000
			wantWalletDelta:  0,
			wantSubApplied:   -2000,
			wantShortfall:    0,
			wantLogType:      model.LogTypeRefund,
			wantLogQuota:     2000,
		},
		{
			name:             "退款时套餐已被清零:夹到 0,钱包不许凭空进账",
			amountTotal:      100000,
			amountUsedBefore: 0,
			actualQuota:      1000,
			wantAmountUsed:   0,
			wantWalletDelta:  0, // 关键:不是 +2000
			wantSubApplied:   0,
			wantShortfall:    0,
			wantLogType:      model.LogTypeRefund,
			wantLogQuota:     2000,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			truncate(t)
			seedUser(t, userID, startQuota)
			seedToken(t, tokenID, userID, "sk-task-cap", tokenRemain)
			seedChannel(t, channelID)
			seedSubscription(t, subID, userID, tc.amountTotal, tc.amountUsedBefore)

			task := makeTask(userID, channelID, preConsumed, tokenID, BillingSourceSubscription, subID)
			require.NoError(t, model.DB.Create(task).Error)

			RecalculateTaskQuota(context.Background(), task, tc.actualQuota, "cap test")

			delta := tc.actualQuota - preConsumed

			// 1. 套餐上限仍然是硬上限。
			assert.Equal(t, tc.wantAmountUsed, getSubscriptionUsed(t, subID), "套餐已用量")
			assert.LessOrEqual(t, getSubscriptionUsed(t, subID), tc.amountTotal)

			// 2. 撞上限收不到的那部分必须由钱包补收（且只在扣费方向）。
			assert.Equal(t, startQuota-tc.wantWalletDelta, getUserQuota(t, userID), "钱包余额")

			// 3. 扣费方向:两个来源加起来 == 这笔差额，一分不多一分不少。
			//    退款方向刻意不守恒 —— 套餐夹到 0 之后剩下的部分绝不补进钱包
			//    (那笔钱当初未必是从钱包出的)，所以这里断言的是"钱包零变动"。
			if delta > 0 {
				assert.Equal(t, int64(delta), tc.wantSubApplied+tc.wantShortfall,
					"用例自身的期望必须自洽:套餐 + 钱包 == 差额")
			} else {
				assert.Zero(t, tc.wantShortfall, "退款方向永远不允许出现钱包补收")
				assert.Zero(t, tc.wantWalletDelta, "退款方向钱包必须零变动")
			}

			// 4. 结算没有被中途放弃：令牌扣了、task.Quota 回写了、日志写了。
			assert.Equal(t, tokenRemain-delta, getTokenRemainQuota(t, tokenID), "令牌额度")
			assert.Equal(t, tc.actualQuota, task.Quota)
			assert.Equal(t, tc.actualQuota, getTaskQuota(t, task.ID))

			log := getLastLog(t)
			require.NotNil(t, log, "差额结算必须留下账单日志")
			assert.Equal(t, tc.wantLogType, log.Type)
			assert.Equal(t, tc.wantLogQuota, log.Quota, "日志记多少 == 实际收多少")

			var other map[string]any
			require.NoError(t, common.UnmarshalJsonStr(log.Other, &other))
			assert.EqualValues(t, tc.wantSubApplied, other["subscription_post_delta"],
				"日志必须写明套餐真正吃下了多少")
			assert.EqualValues(t, tc.wantShortfall, other["wallet_quota_deducted"],
				"日志必须写明改由钱包补收了多少")
			assert.EqualValues(t, subID, other["subscription_id"])
		})
	}
}
