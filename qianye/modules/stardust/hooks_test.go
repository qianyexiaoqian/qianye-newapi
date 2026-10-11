package stardust

import (
	"context"
	"testing"

	"github.com/QuantumNous/new-api/model"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// hooks_test.go —— 两个事件型获得途径(注册奖、套餐返)。hook 体只做内存预过滤后经 guard.HotAsync 投递,
// 用例直接调 worker 里跑的 accrue* 函数:要钉住的是"给谁、给多少、幂等键是什么、
// 什么情况下一颗都不给"。

func TestAccrueRegisterPaysFixedRewardOnce(t *testing.T) {
	env := newTaskEnv(t, nil) // invite_register_stardust = 50
	withCompliance(t, true)
	ctx := context.Background()
	seedMainUser(t, env.main, model.User{Id: 311})
	seedMainUser(t, env.main, model.User{Id: 312, InviterId: 311})
	seedMainUser(t, env.main, model.User{Id: 313})
	seedMainUser(t, env.main, model.User{Id: 314, InviterId: 311})

	require.NoError(t, accrueRegister(ctx, 312))
	require.NoError(t, accrueRegister(ctx, 312), "同一次注册投递两次")
	l := ledgerOf(t, env.ext, 311)
	require.Len(t, l, 1)
	assert.Equal(t, string(KindInviteRegister), l[0].Kind)
	assert.EqualValues(t, 50, l[0].Amount)
	assert.Equal(t, registerIdemScope, l[0].IdemScope)
	assert.Equal(t, "register:312", l[0].IdemKey)
	assert.Equal(t, "register", l[0].RefType)
	assert.Equal(t, "312", l[0].RefNo)
	assert.Equal(t, 312, l[0].PeerUserId)
	assert.Equal(t, "default", l[0].RateGroup)

	require.NoError(t, accrueRegister(ctx, 313))
	assert.EqualValues(t, 1, ledgerCount(t, env.ext), "没有上线:不记账")

	withCompliance(t, false)
	require.NoError(t, accrueRegister(ctx, 314))
	assert.EqualValues(t, 1, ledgerCount(t, env.ext), "合规未确认:注册奖按 0 生效")
}

func TestAccruePlanGrantFollowsPlanRewardDefinition(t *testing.T) {
	env := newTaskEnv(t, nil)
	withCompliance(t, true)
	ctx := context.Background()
	seedMainUser(t, env.main, model.User{Id: 321})
	seedMainUser(t, env.main, model.User{Id: 322, InviterId: 321})

	// 没配过的套餐:按售价 1:1 返买家、只有 order / balance 两条来源。
	order := model.QySubscriptionGrant{UserId: 322, PlanId: 5, SubscriptionId: 9, Source: PlanSourceOrder, TradeNo: "SUB-1", PriceAmount: 10, Money: 8}
	require.NoError(t, accruePlanGrant(ctx, order, 1_000))
	require.NoError(t, accruePlanGrant(ctx, order, 2_000), "同一订单再投递一次")
	l := ledgerOf(t, env.ext, 322)
	require.Len(t, l, 1)
	assert.Equal(t, string(KindPlanBuyer), l[0].Kind)
	assert.EqualValues(t, 8, l[0].Amount, "order 来源用实付 Money:$8 → 8 星屑")
	assert.Equal(t, planIdemScope, l[0].IdemScope)
	assert.Equal(t, "sub:SUB-1", l[0].IdemKey)
	assert.Equal(t, "subscription", l[0].RefType)
	assert.Equal(t, "9", l[0].RefNo)
	assert.Equal(t, 10_000, l[0].RateBps)
	assert.EqualValues(t, 4_000_000, l[0].BaseQuota)
	assert.Empty(t, ledgerOf(t, env.ext, 321), "默认 inviter_bps=0")

	balance := model.QySubscriptionGrant{UserId: 322, PlanId: 5, SubscriptionId: 9, Source: PlanSourceBalance, TradeNo: "SUB-2", PriceAmount: 10, Money: 6, Renewed: true}
	require.NoError(t, accruePlanGrant(ctx, balance, 3_000))
	l = ledgerOf(t, env.ext, 322)
	require.Len(t, l, 2)
	assert.EqualValues(t, 6, l[1].Amount, "balance 来源按实扣的余额购买价($6),不是在线支付价;续期照返")
	assert.Equal(t, "sub:SUB-2", l[1].IdemKey)

	admin := model.QySubscriptionGrant{UserId: 322, PlanId: 5, SubscriptionId: 9, Source: PlanSourceAdmin, PriceAmount: 10}
	require.NoError(t, accruePlanGrant(ctx, admin, 4_000))
	mall := model.QySubscriptionGrant{UserId: 322, PlanId: 5, SubscriptionId: 9, Source: "stardust", TradeNo: "SUBSD-1", PriceAmount: 10}
	require.NoError(t, accruePlanGrant(ctx, mall, 5_000))
	assert.Len(t, ledgerOf(t, env.ext, 322), 2, "admin 默认不在来源里;商城自购永远不返")

	// 配过的套餐:买家 50%、来源 admin + order。历史行里的 InviterBps 不再生效 ——
	// 上线的套餐返已移到推广佣金。
	require.NoError(t, savePlanReward(ctx, PlanReward{PlanId: 6, BuyerBps: 5000, InviterBps: 2500, Sources: "admin,order"}))
	adminGrant := model.QySubscriptionGrant{UserId: 322, PlanId: 6, SubscriptionId: 10, Source: PlanSourceAdmin, PriceAmount: 20, Renewed: true}
	require.NoError(t, accruePlanGrant(ctx, adminGrant, 6_000))
	require.NoError(t, accruePlanGrant(ctx, adminGrant, 6_000))
	l = ledgerOf(t, env.ext, 322)
	require.Len(t, l, 3)
	assert.EqualValues(t, 10, l[2].Amount)
	assert.Equal(t, "sub:admin10:6000", l[2].IdemKey)
	assert.Equal(t, "10", l[2].RefNo)
	assert.Equal(t, 5000, l[2].RateBps)
	assert.Empty(t, ledgerOf(t, env.ext, 321), "星屑不再记 plan_inviter:上线那份走推广佣金")

	redeemGrant := model.QySubscriptionGrant{UserId: 322, PlanId: 6, SubscriptionId: 11, Source: PlanSourceRedemption, RedemptionId: 3, PriceAmount: 20}
	require.NoError(t, accruePlanGrant(ctx, redeemGrant, 7_000))
	assert.EqualValues(t, 3, ledgerCount(t, env.ext), "redemption 不在这份套餐的来源里")
}
