package model

// subscription_deleted_plan_test.go —— 一条指向已删除套餐的订阅，只能拖垮它自己。
//
// ═══════════════════ 它防的是一次「整号锁死」 ═══════════════════
//
// 管理员删掉一个套餐行之后，用户身上那条已购订阅仍然存着 PlanId 指向它。
// PreConsumeUserSubscription 取候选时会去加载套餐，而加载失败原本是直接
// `return err` —— 于是：
//
//	getSubscriptionPlanByIdTx 返回 gorm.ErrRecordNotFound（"record not found"）
//	  → 整个预扣事务失败
//	  → service/billing_session.go 的回落判断只认 "no active subscription" 与
//	    "subscription quota insufficient" 两个子串，都不匹配
//	  → 错误码落成 ErrorCodeUpdateDataError
//	  → billing_session.go 的 `if apiErr.GetErrorCode() != InsufficientUserQuota`
//	    成立，直接 return，**不回落钱包**
//
// 结果是这个用户的**每一次请求**都被打挂：换任何模型、换任何模型分组、
// 钱包里有多少钱，都救不回来。一行脏数据锁死一个账号的全部用量。
//
// 修法是把这一条候选**跳过**（continue）而不是让整个事务失败。跳过之后循环
// 走完会落到 "subscription quota insufficient"，那正是回落判断认识的形状。
//
// ── 为什么必须真跑而不是扫源码 ──
//
// 这里要钉的不是「代码里有没有 continue」，而是**错误的形状**：它决定上层
// 会不会回落钱包。换一种写法只要错误文本变了，回落就再次失效，而源码断言
// 看不出来。所以这条用真库跑，并直接断言错误文本里含那个子串。
//
// ── 进程内缓存让这个 BUG 在生产上是间歇性的 ──
//
// getSubscriptionPlanByIdTx 命中缓存时不查库，所以套餐刚删掉的那段时间里
// 一切正常，等 TTL 过期才开始全线失败。用例因此刻意用一个**从未存在过**的
// PlanId，保证走到查库那一步。

import (
	"strings"
	"testing"

	"github.com/glebarez/sqlite"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

func setupDeletedPlanTestDB(t *testing.T) {
	t.Helper()
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	require.NoError(t, err)
	require.NoError(t, db.AutoMigrate(
		&SubscriptionPlan{},
		&UserSubscription{},
		&SubscriptionPreConsumeRecord{},
	))
	previousDB := DB
	DB = db
	t.Cleanup(func() { DB = previousDB })
}

// TestDeletedPlanSubscriptionFallsBackInsteadOfKillingTheAccount
//
// 一条指向已删除套餐的订阅被跳过，报出来的必须是「订阅出不了资」这一档
// —— 那是上层唯一会回落钱包的形状。
func TestDeletedPlanSubscriptionFallsBackInsteadOfKillingTheAccount(t *testing.T) {
	setupDeletedPlanTestDB(t)

	const userId = 4242
	// 指向一个从未存在过的套餐 id：等价于「套餐被删了」，且保证不命中进程内缓存。
	require.NoError(t, DB.Create(&UserSubscription{
		UserId:      userId,
		PlanId:      999999,
		AmountTotal: 100000,
		AmountUsed:  0,
		Status:      "active",
		EndTime:     0, // 永久
	}).Error)

	_, err := PreConsumeUserSubscription("req-deleted-plan", userId, "gpt-4o", 0, 10, "default")

	require.Error(t, err, "这条订阅定不出价，预扣本就该失败")
	assert.True(t,
		strings.Contains(err.Error(), "subscription quota insufficient") ||
			strings.Contains(err.Error(), "no active subscription"),
		"错误必须落在 service/billing_session.go 认识的两个子串之一，否则钱包回落不触发、"+
			"该用户所有请求都会被打挂；实际拿到的是：%v", err)
	assert.NotContains(t, err.Error(), "record not found",
		"原始的 record not found 一旦漏上去，错误码会落成 UpdateDataError，回落判断不成立")
}

// TestDeletedPlanDoesNotBlockAHealthySubscription
//
// 同一个用户身上既有坏订阅又有好订阅时，坏的那条不能拖累好的那条。
//
// 这一条比上一条更接近真实故障：候选按 end_time 排序，坏订阅完全可能排在前面。
// 原实现在第一条就 return，后面那条永远轮不到。
func TestDeletedPlanDoesNotBlockAHealthySubscription(t *testing.T) {
	setupDeletedPlanTestDB(t)

	const userId = 4243

	plan := &SubscriptionPlan{Title: "healthy", DurationUnit: "month", DurationValue: 1}
	require.NoError(t, DB.Create(plan).Error)
	require.Positive(t, plan.Id)

	// 坏订阅排在前面：end_time 更早 ⇒ Order("end_time asc, id asc") 先取到它。
	require.NoError(t, DB.Create(&UserSubscription{
		UserId: userId, PlanId: 999998,
		AmountTotal: 100000, Status: "active", EndTime: GetDBTimestamp() + 3600,
	}).Error)
	require.NoError(t, DB.Create(&UserSubscription{
		UserId: userId, PlanId: plan.Id,
		AmountTotal: 100000, Status: "active", EndTime: GetDBTimestamp() + 86400,
	}).Error)

	res, err := PreConsumeUserSubscription("req-mixed", userId, "gpt-4o", 0, 10, "default")

	require.NoError(t, err, "坏订阅必须被跳过，好订阅照常出资")
	require.NotNil(t, res)
	assert.EqualValues(t, 10, res.PreConsumed)
}
