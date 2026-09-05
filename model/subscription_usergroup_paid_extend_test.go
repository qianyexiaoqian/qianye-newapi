package model

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// subscription_usergroup_paid_extend_test.go —— 「用户组商品」的同组续期分支
// 不得碰一张**带额度付费订阅**的 end_time。
//
// applyUserGroupPurchaseRulesTx 的启用闸只判**新买的这个套餐** NoQuota,而被匹配
// 到的 existing 是按 `upgrade_group <> '' AND status='active'` 选出来的,完全可以是
// 一张「升组 + 送额度」的付费订阅。此前同组分支对 existing 无条件改 end_time(永久档
// 更是写成 0 = 永不过期),于是一件几块钱的纯商品就能把一张贵套餐的有效期整段往后推、
// 它没花完的额度跟着一起延寿 —— 站点白送出一段本该收费的高档位寿命与余额。
//
// 修复:同组分支补上跨组顶替分支早就有的 `!existing.NoQuota` 判据 —— 带额度订阅
// 与新买的纯商品**并存**(原样不动,纯商品另落一行),不再被当作续期目标。

// seedPermanentGroupPlan 建一个永久纯商品(seedGroupPlan 只造 custom 时长,补这一档)。
func seedPermanentGroupPlan(t *testing.T, id int, upgradeGroup string) *SubscriptionPlan {
	t.Helper()
	plan := &SubscriptionPlan{
		Id:           id,
		Title:        "forever-" + upgradeGroup,
		Enabled:      true,
		NoQuota:      true,
		UpgradeGroup: upgradeGroup,
		DurationUnit: SubscriptionDurationPermanent,
	}
	require.NoError(t, DB.Create(plan).Error)
	t.Cleanup(func() { DB.Exec("DELETE FROM subscription_plans WHERE id = ?", id) })
	return plan
}

func reloadSubscription(t *testing.T, id int) UserSubscription {
	t.Helper()
	var row UserSubscription
	require.NoError(t, DB.Where("id = ?", id).Take(&row).Error)
	return row
}

func countSubscriptionsOfPlan(t *testing.T, userId, planId int) int64 {
	t.Helper()
	var n int64
	require.NoError(t, DB.Model(&UserSubscription{}).
		Where("user_id = ? AND plan_id = ?", userId, planId).Count(&n).Error)
	return n
}

func TestUserGroupProductDoesNotExtendPaidQuotaSubscription(t *testing.T) {
	t.Run("时效纯商品不得延长带额度付费订阅的到期时间", func(t *testing.T) {
		useChainDB(t)
		seedGroupUser(t, 9501, "default")

		// 一张「升组 + 送额度」的付费订阅:1 小时有效,带额度。
		paid := seedGroupPlan(t, 9501, "vip", false, 3600)
		paidSub := buyPlan(t, 9501, paid)
		require.False(t, paidSub.NoQuota, "前提:这是一张带额度的订阅")
		beforeEnd := paidSub.EndTime
		beforeStatus := reloadSubscription(t, paidSub.Id).Status

		// 一件同目标组、1 天有效的纯商品。
		cheap := seedGroupPlan(t, 9502, "vip", true, 86400)
		got := buyPlan(t, 9501, cheap)

		after := reloadSubscription(t, paidSub.Id)
		assert.Equal(t, beforeEnd, after.EndTime,
			"纯商品不得延长带额度订阅的有效期 —— 它没花完的额度会跟着一起延寿")
		assert.Equal(t, beforeStatus, after.Status, "带额度订阅应原样不动,不被作废")

		// 纯商品应当落成它自己的一行,而不是去改别人的行。
		require.NotNil(t, got)
		assert.NotEqual(t, paidSub.Id, got.Id, "纯商品必须独立成行,不能返回带额度那一行")
		assert.True(t, got.NoQuota)
		assert.EqualValues(t, 1, countSubscriptionsOfPlan(t, 9501, cheap.Id),
			"纯商品应当落成它自己的一行")
		assert.Equal(t, "vip", userGroupOf(t, 9501), "两者都指向 vip,用户组应为 vip")
	})

	t.Run("永久纯商品不得把带额度付费订阅改成永不过期", func(t *testing.T) {
		useChainDB(t)
		seedGroupUser(t, 9511, "default")

		paid := seedGroupPlan(t, 9511, "vip", false, 3600)
		paidSub := buyPlan(t, 9511, paid)
		require.NotZero(t, paidSub.EndTime, "前提:它本来有到期时间")
		beforeEnd := paidSub.EndTime

		forever := seedPermanentGroupPlan(t, 9512, "vip")
		buyPlan(t, 9511, forever)

		after := reloadSubscription(t, paidSub.Id)
		assert.Equal(t, beforeEnd, after.EndTime,
			"永久纯商品不得把带额度订阅写成 end_time=0 —— 它剩下的额度会从此永不过期")
		assert.NotZero(t, after.EndTime)
		assert.EqualValues(t, 1, countSubscriptionsOfPlan(t, 9511, forever.Id),
			"永久纯商品应当落成它自己的一行")
	})

	t.Run("回归:纯商品对纯商品的同组续期仍从原到期时间顺延、不新建行", func(t *testing.T) {
		useChainDB(t)
		seedGroupUser(t, 9521, "default")

		month := seedGroupPlan(t, 9521, "vip", true, 3600)
		first := buyPlan(t, 9521, month)
		firstEnd := first.EndTime

		second := buyPlan(t, 9521, month)
		assert.Equal(t, first.Id, second.Id, "同组纯商品续期不得新建订阅行")
		assert.Equal(t, firstEnd+3600, reloadSubscription(t, first.Id).EndTime,
			"必须从原到期时间往后接")
		assert.EqualValues(t, 1, countSubscriptionsOfPlan(t, 9521, month.Id))
	})
}
