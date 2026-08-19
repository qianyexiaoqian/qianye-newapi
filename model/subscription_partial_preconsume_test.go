package model

import (
	"testing"

	"github.com/QuantumNous/new-api/common"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func seedPartialPlan(t *testing.T) int {
	t.Helper()
	plan := SubscriptionPlan{
		Title:            "partial-plan-" + common.GetRandomString(4),
		Enabled:          true,
		QuotaResetPeriod: "never",
	}
	require.NoError(t, DB.Create(&plan).Error)
	return plan.Id
}

func seedPartialSubscription(t *testing.T, userId, planId int, total, used int64) UserSubscription {
	t.Helper()
	sub := UserSubscription{
		UserId:      userId,
		PlanId:      planId,
		Status:      "active",
		AmountTotal: total,
		AmountUsed:  used,
		EndTime:     0, // 0 = 不过期
	}
	require.NoError(t, DB.Create(&sub).Error)
	return sub
}

// TestSubscriptionPreConsumeTakesWhatIsLeft 钉住"套餐余额必须能花到 0"。
//
// 候选筛选原来用的是**预扣估算额**(`if remain < amount { continue }`),而预扣额
// 是真实花费的几十到上百倍。于是每张余额型套餐用到尾巴时都会留下"一次预扣额 −1"
// 的残额:那笔钱既花不掉(后续请求的预扣额只会更大),也没有任何提示,用户看到的
// 是"套餐还有余额,却在扣钱包"。实测阈值精确落在预扣额上;整张 amount_total 小于
// 一次预扣额的套餐更是从头到尾一次都出不了资。
func TestSubscriptionPreConsumeTakesWhatIsLeft(t *testing.T) {
	truncateTables(t)
	planId := seedPartialPlan(t)

	cases := []struct {
		name   string
		total  int64
		used   int64
		amount int64
		want   int64
	}{
		{"余额够,整额预扣", 10000, 0, 3048, 3048},
		{"余额恰好等于预扣额", 3048, 0, 3048, 3048},
		{"余额差 1:必须按剩余额度部分预扣,而不是改扣钱包", 3048, 1, 3048, 3047},
		{"整张套餐都小于一次预扣额:照样能出资", 1000, 0, 3048, 1000},
		{"只剩 1 也要花掉", 3048, 3047, 3048, 1},
	}
	for i, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			userId := 7000 + i
			sub := seedPartialSubscription(t, userId, planId, tc.total, tc.used)
			res, err := PreConsumeUserSubscription(
				"req-partial-"+common.GetRandomString(8), userId, "any-model", 0, tc.amount, "default")
			require.NoError(t, err)
			assert.Equal(t, tc.want, res.PreConsumed)
			assert.Equal(t, tc.used+tc.want, res.AmountUsedAfter)

			var after UserSubscription
			require.NoError(t, DB.First(&after, sub.Id).Error)
			assert.Equal(t, tc.used+tc.want, after.AmountUsed,
				"库里记的已用量必须等于真正预扣的那个数")
		})
	}
}

// 余额已经用完的套餐仍然要跳过 —— 部分预扣不等于允许预扣 0。
func TestSubscriptionPreConsumeStillSkipsAnExhaustedPlan(t *testing.T) {
	truncateTables(t)
	planId := seedPartialPlan(t)
	seedPartialSubscription(t, 7100, planId, 5000, 5000)

	_, err := PreConsumeUserSubscription("req-exhausted", 7100, "any-model", 0, 100, "default")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "subscription quota insufficient")
}

// TestSubscriptionPreConsumePrefersAPlanThatCoversInFull 钉住两轮选人的顺序。
//
// 部分预扣**只在一张都覆盖不了时**才发生。第一轮仍然是改动之前的行为:最先到期
// 的那张余额不够就顺延给下一张 —— 否则「先到期的额度会白白作废」这条既有语义
// 会被换成「先到期的先出一点、剩下的从钱包走」,把本可以由另一张套餐付的钱
// 推给了钱包。
func TestSubscriptionPreConsumePrefersAPlanThatCoversInFull(t *testing.T) {
	truncateTables(t)
	planId := seedPartialPlan(t)

	// 最先到期的只剩 10,第二张能整额覆盖。
	early := seedPartialSubscription(t, 7200, planId, 1000, 990)
	early.EndTime = common.GetTimestamp() + 86400
	require.NoError(t, DB.Save(&early).Error)
	mid := seedPartialSubscription(t, 7200, planId, 1000, 0)
	mid.EndTime = common.GetTimestamp() + 86400*10
	require.NoError(t, DB.Save(&mid).Error)

	res, err := PreConsumeUserSubscription("req-full-cover", 7200, "any-model", 0, 100, "default")
	require.NoError(t, err)
	assert.Equal(t, mid.Id, res.UserSubscriptionId,
		"能整额覆盖的那张必须优先,部分预扣只是覆盖不了时的兜底")
	assert.EqualValues(t, 100, res.PreConsumed)

	var earlyAfter UserSubscription
	require.NoError(t, DB.First(&earlyAfter, early.Id).Error)
	assert.EqualValues(t, 990, earlyAfter.AmountUsed, "第一轮命中时先到期那张一分都不该动")
}
