package model

import (
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func seedSubscriptionResetPlan(t *testing.T, plan *SubscriptionPlan) {
	t.Helper()
	require.NoError(t, DB.Create(plan).Error)
}

func seedSubscriptionResetSub(t *testing.T, sub *UserSubscription) {
	t.Helper()
	require.NoError(t, DB.Create(sub).Error)
}

// seedSubscriptionResetUser 建一个带角色的归属人。
// 整盘重置现在会逐行回查归属人角色，查不到的一律按「管不着」跳过（fail-closed），
// 所以这些用例必须显式把人建出来，而不能只建订阅行。
func seedSubscriptionResetUser(t *testing.T, id int, role int) {
	t.Helper()
	require.NoError(t, DB.Create(&User{
		Id:       id,
		Username: "qy-sub-reset-" + strconv.Itoa(id),
		Password: "x",
		// aff_code 有唯一索引，空串在第二行就会撞。
		AffCode:     "qyaff" + strconv.Itoa(id),
		Role:        role,
		Status:      common.UserStatusEnabled,
		DisplayName: "qy-sub-reset",
	}).Error)
}

func getSubscriptionResetSub(t *testing.T, id int) UserSubscription {
	t.Helper()
	var sub UserSubscription
	require.NoError(t, DB.Where("id = ?", id).First(&sub).Error)
	return sub
}

func TestAdminResetUserSubscriptionsByPlanResetsAllActiveMatchesAndAdvancesTime(t *testing.T) {
	truncateTables(t)

	now := GetDBTimestamp()
	plan := &SubscriptionPlan{
		Id:               9101,
		Title:            "Pro",
		PriceAmount:      10,
		DurationUnit:     SubscriptionDurationMonth,
		DurationValue:    1,
		TotalAmount:      1000,
		QuotaResetPeriod: SubscriptionResetDaily,
	}
	otherPlan := &SubscriptionPlan{
		Id:               9102,
		Title:            "Basic",
		PriceAmount:      1,
		DurationUnit:     SubscriptionDurationMonth,
		DurationValue:    1,
		TotalAmount:      100,
		QuotaResetPeriod: SubscriptionResetDaily,
	}
	seedSubscriptionResetPlan(t, plan)
	seedSubscriptionResetPlan(t, otherPlan)

	activeEnd := now + 30*24*3600
	expiredEnd := now - 1
	seedSubscriptionResetSub(t, &UserSubscription{Id: 9201, UserId: 101, PlanId: plan.Id, AmountTotal: 1000, AmountUsed: 300, StartTime: now - 3600, EndTime: activeEnd, Status: "active", LastResetTime: now - 3600, NextResetTime: now + 120})
	seedSubscriptionResetSub(t, &UserSubscription{Id: 9202, UserId: 101, PlanId: plan.Id, AmountTotal: 1000, AmountUsed: 500, StartTime: now - 3600, EndTime: activeEnd, Status: "active", LastResetTime: now - 3600, NextResetTime: now + 120})
	seedSubscriptionResetSub(t, &UserSubscription{Id: 9203, UserId: 101, PlanId: otherPlan.Id, AmountTotal: 100, AmountUsed: 60, StartTime: now - 3600, EndTime: activeEnd, Status: "active", LastResetTime: now - 3600, NextResetTime: now + 120})
	seedSubscriptionResetSub(t, &UserSubscription{Id: 9204, UserId: 101, PlanId: plan.Id, AmountTotal: 1000, AmountUsed: 700, StartTime: now - 7200, EndTime: expiredEnd, Status: "active", LastResetTime: now - 3600, NextResetTime: now - 10})
	seedSubscriptionResetSub(t, &UserSubscription{Id: 9205, UserId: 102, PlanId: plan.Id, AmountTotal: 1000, AmountUsed: 800, StartTime: now - 3600, EndTime: activeEnd, Status: "active", LastResetTime: now - 3600, NextResetTime: now + 120})
	seedSubscriptionResetSub(t, &UserSubscription{Id: 9206, UserId: 101, PlanId: plan.Id, AmountTotal: 1000, AmountUsed: 900, StartTime: now - 3600, EndTime: activeEnd, Status: "cancelled", LastResetTime: now - 3600, NextResetTime: now + 120})

	beforeReset := GetDBTimestamp()
	result, err := AdminResetUserSubscriptionsByPlan(101, plan.Id, true)
	afterReset := GetDBTimestamp()

	require.NoError(t, err)
	require.NotNil(t, result)
	assert.Equal(t, plan.Id, result.PlanId)
	assert.Equal(t, 2, result.MatchedCount)
	assert.Equal(t, 2, result.ResetCount)
	assert.Equal(t, 1, result.UserCount)
	assert.Equal(t, []int{101}, result.AffectedUserIds)
	assert.True(t, result.AdvanceResetTime)

	for _, id := range []int{9201, 9202} {
		sub := getSubscriptionResetSub(t, id)
		assert.Zero(t, sub.AmountUsed)
		assert.GreaterOrEqual(t, sub.LastResetTime, beforeReset)
		assert.LessOrEqual(t, sub.LastResetTime, afterReset)
		assert.Equal(t, calcNextResetTime(time.Unix(sub.LastResetTime, 0), plan, sub.EndTime), sub.NextResetTime)
	}
	assert.EqualValues(t, 60, getSubscriptionResetSub(t, 9203).AmountUsed)
	assert.EqualValues(t, 700, getSubscriptionResetSub(t, 9204).AmountUsed)
	assert.EqualValues(t, 800, getSubscriptionResetSub(t, 9205).AmountUsed)
	assert.EqualValues(t, 900, getSubscriptionResetSub(t, 9206).AmountUsed)
}

func TestAdminResetUserSubscriptionsByPlanKeepsResetTimes(t *testing.T) {
	truncateTables(t)

	now := GetDBTimestamp()
	plan := &SubscriptionPlan{
		Id:               9301,
		Title:            "Team",
		PriceAmount:      20,
		DurationUnit:     SubscriptionDurationMonth,
		DurationValue:    1,
		TotalAmount:      2000,
		QuotaResetPeriod: SubscriptionResetMonthly,
	}
	seedSubscriptionResetPlan(t, plan)

	lastReset := now - 86400
	nextReset := now + 86400
	seedSubscriptionResetSub(t, &UserSubscription{Id: 9302, UserId: 201, PlanId: plan.Id, AmountTotal: 2000, AmountUsed: 1200, StartTime: now - 172800, EndTime: now + 30*24*3600, Status: "active", LastResetTime: lastReset, NextResetTime: nextReset})

	result, err := AdminResetUserSubscriptionsByPlan(201, plan.Id, false)

	require.NoError(t, err)
	assert.False(t, result.AdvanceResetTime)
	sub := getSubscriptionResetSub(t, 9302)
	assert.Zero(t, sub.AmountUsed)
	assert.Equal(t, lastReset, sub.LastResetTime)
	assert.Equal(t, nextReset, sub.NextResetTime)
}

func TestAdminResetUserSubscriptionsByPlanNoActiveMatchReturnsError(t *testing.T) {
	truncateTables(t)

	now := GetDBTimestamp()
	plan := &SubscriptionPlan{
		Id:            9401,
		Title:         "Expired",
		PriceAmount:   10,
		DurationUnit:  SubscriptionDurationMonth,
		DurationValue: 1,
		TotalAmount:   1000,
	}
	seedSubscriptionResetPlan(t, plan)
	seedSubscriptionResetSub(t, &UserSubscription{Id: 9402, UserId: 301, PlanId: plan.Id, AmountTotal: 1000, AmountUsed: 500, StartTime: now - 7200, EndTime: now - 1, Status: "active"})

	result, err := AdminResetUserSubscriptionsByPlan(301, plan.Id, true)

	require.Error(t, err)
	assert.Nil(t, result)
	assert.True(t, strings.Contains(err.Error(), "该用户没有有效的此套餐订阅"))
}

func TestAdminResetPlanSubscriptionsResetsAllActiveUsers(t *testing.T) {
	truncateTables(t)

	now := GetDBTimestamp()
	plan := &SubscriptionPlan{
		Id:               9501,
		Title:            "Business",
		PriceAmount:      30,
		DurationUnit:     SubscriptionDurationMonth,
		DurationValue:    1,
		TotalAmount:      3000,
		QuotaResetPeriod: SubscriptionResetNever,
	}
	seedSubscriptionResetPlan(t, plan)

	for _, id := range []int{401, 402, 403, 404} {
		seedSubscriptionResetUser(t, id, common.RoleCommonUser)
	}

	activeEnd := now + 30*24*3600
	seedSubscriptionResetSub(t, &UserSubscription{Id: 9502, UserId: 401, PlanId: plan.Id, AmountTotal: 3000, AmountUsed: 1000, StartTime: now - 3600, EndTime: activeEnd, Status: "active", LastResetTime: now - 3600, NextResetTime: now + 10})
	seedSubscriptionResetSub(t, &UserSubscription{Id: 9503, UserId: 401, PlanId: plan.Id, AmountTotal: 3000, AmountUsed: 1100, StartTime: now - 3500, EndTime: activeEnd, Status: "active", LastResetTime: now - 3600, NextResetTime: now + 10})
	seedSubscriptionResetSub(t, &UserSubscription{Id: 9504, UserId: 402, PlanId: plan.Id, AmountTotal: 3000, AmountUsed: 1200, StartTime: now - 3400, EndTime: activeEnd, Status: "active", LastResetTime: now - 3600, NextResetTime: now + 10})
	seedSubscriptionResetSub(t, &UserSubscription{Id: 9505, UserId: 403, PlanId: plan.Id, AmountTotal: 3000, AmountUsed: 1300, StartTime: now - 7200, EndTime: now - 1, Status: "active", LastResetTime: now - 3600, NextResetTime: now - 10})
	seedSubscriptionResetSub(t, &UserSubscription{Id: 9506, UserId: 404, PlanId: plan.Id, AmountTotal: 3000, AmountUsed: 1400, StartTime: now - 3600, EndTime: activeEnd, Status: "cancelled", LastResetTime: now - 3600, NextResetTime: now + 10})

	result, err := AdminResetPlanSubscriptions(plan.Id, true, SubscriptionActor{UserId: 1, Role: common.RoleRootUser})

	require.NoError(t, err)
	require.NotNil(t, result)
	assert.Equal(t, 3, result.MatchedCount)
	assert.Zero(t, result.SkippedCount)
	assert.Equal(t, 3, result.ResetCount)
	assert.Equal(t, 2, result.UserCount)
	assert.Equal(t, []int{401, 402}, result.AffectedUserIds)
	for _, id := range []int{9502, 9503, 9504} {
		sub := getSubscriptionResetSub(t, id)
		assert.Zero(t, sub.AmountUsed)
		assert.Zero(t, sub.LastResetTime)
		assert.Zero(t, sub.NextResetTime)
	}
	assert.EqualValues(t, 1300, getSubscriptionResetSub(t, 9505).AmountUsed)
	assert.EqualValues(t, 1400, getSubscriptionResetSub(t, 9506).AmountUsed)
}

func TestAdminResetPlanSubscriptionsNoMatchSucceeds(t *testing.T) {
	truncateTables(t)

	plan := &SubscriptionPlan{
		Id:            9601,
		Title:         "Empty",
		PriceAmount:   10,
		DurationUnit:  SubscriptionDurationMonth,
		DurationValue: 1,
		TotalAmount:   1000,
	}
	seedSubscriptionResetPlan(t, plan)

	result, err := AdminResetPlanSubscriptions(plan.Id, true, SubscriptionActor{UserId: 1, Role: common.RoleRootUser})

	require.NoError(t, err)
	require.NotNil(t, result)
	assert.Zero(t, result.MatchedCount)
	assert.Zero(t, result.ResetCount)
	assert.Zero(t, result.UserCount)
	assert.Empty(t, result.AffectedUserIds)
}

// TestAdminResetPlanSubscriptionsSkipsUsersTheActorCannotManage 守住整盘重置的操作人判据。
//
// 重置 = 把 amount_used 清回 0 = 再送一轮额度。按人那条路(AdminResetUserSubscriptionsByPlan)
// 早就有 requireManageableUser，整盘这条路原先一道都没有，于是一个 role=10 管理员
// 只要自己名下有该套餐，一次调用就把**自己**的已用量清零，顺带动了 role=100。
// 这里逐行断言：管得着的被重置，自己和同级/更高级的原样不动，且 skipped_count 报出来。
func TestAdminResetPlanSubscriptionsSkipsUsersTheActorCannotManage(t *testing.T) {
	truncateTables(t)

	now := GetDBTimestamp()
	plan := &SubscriptionPlan{
		Id:               9701,
		Title:            "Gated",
		PriceAmount:      1,
		DurationUnit:     SubscriptionDurationMonth,
		DurationValue:    1,
		TotalAmount:      3000,
		QuotaResetPeriod: SubscriptionResetNever,
	}
	seedSubscriptionResetPlan(t, plan)

	const actorId = 501
	seedSubscriptionResetUser(t, actorId, common.RoleAdminUser) // 操作人自己
	seedSubscriptionResetUser(t, 502, common.RoleAdminUser)     // 同级
	seedSubscriptionResetUser(t, 503, common.RoleRootUser)      // 更高级
	seedSubscriptionResetUser(t, 504, common.RoleCommonUser)    // 管得着

	activeEnd := now + 30*24*3600
	for i, userId := range []int{actorId, 502, 503, 504} {
		seedSubscriptionResetSub(t, &UserSubscription{
			Id: 9710 + i, UserId: userId, PlanId: plan.Id,
			AmountTotal: 3000, AmountUsed: 7777,
			StartTime: now - 3600, EndTime: activeEnd, Status: "active",
		})
	}

	result, err := AdminResetPlanSubscriptions(plan.Id, true, SubscriptionActor{UserId: actorId, Role: common.RoleAdminUser})

	require.NoError(t, err)
	require.NotNil(t, result)
	assert.Equal(t, 4, result.MatchedCount, "四条都匹配到了")
	assert.Equal(t, 1, result.ResetCount, "只有 504 被真的重置")
	assert.Equal(t, 3, result.SkippedCount, "自己 + 同级 + 更高级 三条被跳过")
	assert.Equal(t, []int{504}, result.AffectedUserIds)

	assert.EqualValues(t, 7777, getSubscriptionResetSub(t, 9710).AmountUsed, "role=10 不能给自己再送一轮")
	assert.EqualValues(t, 7777, getSubscriptionResetSub(t, 9711).AmountUsed, "同级不能互相重置")
	assert.EqualValues(t, 7777, getSubscriptionResetSub(t, 9712).AmountUsed, "role=10 不能动 role=100")
	assert.Zero(t, getSubscriptionResetSub(t, 9713).AmountUsed, "管得着的那一个照常重置")

	// root 仍然能重置全部四条（含自己），否则这道判据就把超管一起挡了。
	rootResult, err := AdminResetPlanSubscriptions(plan.Id, true, SubscriptionActor{UserId: 503, Role: common.RoleRootUser})
	require.NoError(t, err)
	assert.Equal(t, 4, rootResult.ResetCount)
	assert.Zero(t, rootResult.SkippedCount)
	for id := 9710; id <= 9713; id++ {
		assert.Zero(t, getSubscriptionResetSub(t, id).AmountUsed)
	}
}

// TestAdminResetPlanSubscriptionsRejectsMissingActor 守住 fail-closed：
// 忘了传操作人身份时必须报错，不能退化成「谁都能重置谁」。
func TestAdminResetPlanSubscriptionsRejectsMissingActor(t *testing.T) {
	truncateTables(t)
	plan := &SubscriptionPlan{
		Id: 9801, Title: "NoActor", PriceAmount: 1,
		DurationUnit: SubscriptionDurationMonth, DurationValue: 1, TotalAmount: 100,
	}
	seedSubscriptionResetPlan(t, plan)

	result, err := AdminResetPlanSubscriptions(plan.Id, true, SubscriptionActor{})
	require.Error(t, err)
	assert.Nil(t, result)
}

// TestGetUserSubscriptionOwnerReportsTheOwner 是作废/硬删除那两条接口的判据前提：
// 目标不在报文里，只能靠订阅行反查归属人。
func TestGetUserSubscriptionOwnerReportsTheOwner(t *testing.T) {
	truncateTables(t)
	now := GetDBTimestamp()
	plan := &SubscriptionPlan{
		Id: 9901, Title: "Owner", PriceAmount: 1,
		DurationUnit: SubscriptionDurationMonth, DurationValue: 1, TotalAmount: 100,
	}
	seedSubscriptionResetPlan(t, plan)
	seedSubscriptionResetUser(t, 601, common.RoleRootUser)
	seedSubscriptionResetSub(t, &UserSubscription{
		Id: 9910, UserId: 601, PlanId: plan.Id, AmountTotal: 100,
		StartTime: now - 60, EndTime: now + 3600, Status: "active",
	})

	ownerId, err := GetUserSubscriptionOwner(9910)
	require.NoError(t, err)
	assert.Equal(t, 601, ownerId)

	_, err = GetUserSubscriptionOwner(0)
	assert.Error(t, err)
	_, err = GetUserSubscriptionOwner(99999)
	assert.Error(t, err, "不存在的订阅必须报错，不能回一个 0 让调用方去 requireManageableUser(0)")
}
