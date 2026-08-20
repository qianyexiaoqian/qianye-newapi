package model

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// 一条升组订阅失效之后，用户组必须对齐到**幸存的**那条升组订阅所给的组。
//
// 原先的判据问的是「名下还有没有别的活跃升组订阅」，还有就 return nil 保留
// 当前组 —— 而当前组恰恰是刚刚失效的那条给的。于是「先买便宜档的长周期、
// 再买贵档的最短周期」就成了一条自助升级通道：短的那条到期后，用户原地停在
// 贵档，把整个便宜档周期按贵档的倍率/权益用完；等便宜那条也到期时，
// `currentGroup != upgradeGroup` 让回退直接放弃，此后再没有任何任务会碰他，
// 于是**永久**停在高档位。
//
// 触发只需要用户自己控制的两个变量（购买顺序、周期长短）：
// applyUserGroupPurchaseRulesTx 对带额度的套餐直接放行，跨组顶替不生效，
// 两条不同目标组的订阅可以合法并存。
func TestExpiryAlignsGroupToTheSurvivingUpgradeNotTheExpiredOne(t *testing.T) {
	useChainDB(t)
	seedGroupUser(t, 9451, "default")
	// 两个都带额度 ⇒ 跨组顶替不生效，两条订阅并存。
	cheapLong := seedGroupPlan(t, 9451, "surv-cheap", false, 7200)
	priceyShort := seedGroupPlan(t, 9452, "surv-pricey", false, 7200)

	longSub := buyPlan(t, 9451, cheapLong)
	require.Equal(t, "surv-cheap", userGroupOf(t, 9451))
	shortSub := buyPlan(t, 9451, priceyShort)
	require.Equal(t, "surv-pricey", userGroupOf(t, 9451))
	require.NotEqual(t, longSub.Id, shortSub.Id, "两条带额度订阅必须并存,否则本用例测不到东西")

	// 只让贵档那条到期。
	require.NoError(t, DB.Model(&UserSubscription{}).
		Where("id = ?", shortSub.Id).
		Update("end_time", time.Now().Unix()-10).Error)
	_, err := ExpireDueSubscriptions(200)
	require.NoError(t, err)

	assert.Equal(t, "surv-cheap", userGroupOf(t, 9451),
		"贵档到期后必须落到幸存那条给的组;停在 surv-pricey 就是用最短周期的钱买到了整个便宜档周期的高档位")

	// 便宜那条也到期后，回到链根。
	expireEverything(t, 9451)
	assert.Equal(t, "default", userGroupOf(t, 9451),
		"两条都到期之后必须回 default;停在任何付费组都是永久免费送出去")
}

// 管理端作废一条升组订阅时同一条判据。
func TestInvalidateAlignsGroupToTheSurvivingUpgrade(t *testing.T) {
	useChainDB(t)
	seedGroupUser(t, 9452, "default")
	cheap := seedGroupPlan(t, 9453, "inv-cheap", false, 7200)
	pricey := seedGroupPlan(t, 9454, "inv-pricey", false, 7200)

	buyPlan(t, 9452, cheap)
	shortSub := buyPlan(t, 9452, pricey)
	require.Equal(t, "inv-pricey", userGroupOf(t, 9452))

	_, invErr := AdminInvalidateUserSubscription(int(shortSub.Id))
	require.NoError(t, invErr)
	assert.Equal(t, "inv-cheap", userGroupOf(t, 9452),
		"作废贵档那条之后必须落到幸存那条给的组")
}
