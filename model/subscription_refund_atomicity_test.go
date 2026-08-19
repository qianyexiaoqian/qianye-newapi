package model

import (
	"errors"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

// subscription_refund_atomicity_test.go —— 「退套餐额度」与「把幂等记录标成
// refunded」必须同生共死。
//
// 缺陷形态:RefundSubscriptionPreConsume 在自己的事务里调
// PostConsumeUserSubscriptionDelta,而后者用的是**全局 DB 句柄另开一个事务**。
// 退款先独立提交,外层再写幂等记录;外层这一步一旦失败(连接被 kill、主备切换、
// 代理超时,以及 SQLite 上因内层提交让读快照失效而**必现**的 SQLITE_BUSY),
// 外层回滚回滚不掉内层已提交的额度返还,而 record.Status 仍是 consumed ——
// service/funding_source.go 的 refundWithRetry 会把整件事重试 3 次,每次都再退
// 一遍(实测一笔 3000 的预扣被退成 9000)。
//
// 修法是让内层复用外层的 tx。下面用注入的 UPDATE 失败把外层打挂,断言额度**没有**
// 被退 —— 这正是原子性的定义。把 postConsumeUserSubscriptionDeltaTx 换回
// PostConsumeUserSubscriptionDelta 会让这条红(在本包 MaxOpenConns(1) 的
// fixture 上表现为拿不到第二条连接而卡死到测试超时)。

func seedRefundFixture(t *testing.T, subId, userId int, amountTotal, amountUsed int64, requestId string, preConsumed int64) {
	t.Helper()
	require.NoError(t, DB.Create(&UserSubscription{
		Id:          subId,
		UserId:      userId,
		PlanId:      1,
		AmountTotal: amountTotal,
		AmountUsed:  amountUsed,
		Status:      "active",
		StartTime:   time.Now().Unix(),
		EndTime:     time.Now().Add(30 * 24 * time.Hour).Unix(),
	}).Error)
	require.NoError(t, DB.Create(&SubscriptionPreConsumeRecord{
		RequestId:          requestId,
		UserId:             userId,
		UserSubscriptionId: subId,
		PreConsumed:        preConsumed,
		Status:             "consumed",
	}).Error)
	t.Cleanup(func() {
		DB.Exec("DELETE FROM user_subscriptions WHERE id = ?", subId)
		DB.Exec("DELETE FROM subscription_pre_consume_records WHERE request_id = ?", requestId)
	})
}

func TestRefundSubscriptionPreConsumeIsAtomicWithItsIdempotencyRecord(t *testing.T) {
	t.Run("happy path refunds exactly once and flips the record", func(t *testing.T) {
		const requestId = "refund-atomic-ok"
		seedRefundFixture(t, 9101, 9001, 100_000, 9_000, requestId, 3_000)

		require.NoError(t, RefundSubscriptionPreConsume(requestId))

		var sub UserSubscription
		require.NoError(t, DB.Where("id = ?", 9101).First(&sub).Error)
		assert.Equal(t, int64(6_000), sub.AmountUsed)

		var record SubscriptionPreConsumeRecord
		require.NoError(t, DB.Where("request_id = ?", requestId).First(&record).Error)
		assert.Equal(t, "refunded", record.Status)

		// 重放(refundWithRetry 最多 3 次)不能再退一遍。
		require.NoError(t, RefundSubscriptionPreConsume(requestId))
		require.NoError(t, RefundSubscriptionPreConsume(requestId))
		require.NoError(t, DB.Where("id = ?", 9101).First(&sub).Error)
		assert.Equal(t, int64(6_000), sub.AmountUsed, "幂等记录必须挡住重放")
	})

	t.Run("a failing record write rolls the refund back with it", func(t *testing.T) {
		const requestId = "refund-atomic-rollback"
		seedRefundFixture(t, 9102, 9002, 100_000, 9_000, requestId, 3_000)

		// 只让幂等记录那一条 UPDATE 失败,订阅行的 UPDATE 照常成功 —— 这是
		// 「内层已提交、外层失败」的最小复现。
		const callbackName = "test:fail_pre_consume_record_update"
		require.NoError(t, DB.Callback().Update().Before("gorm:update").
			Register(callbackName, func(db *gorm.DB) {
				if db.Statement != nil && db.Statement.Table == "subscription_pre_consume_records" {
					db.AddError(errors.New("injected record update failure"))
				}
			}))
		t.Cleanup(func() { _ = DB.Callback().Update().Remove(callbackName) })

		err := RefundSubscriptionPreConsume(requestId)
		require.Error(t, err)

		var sub UserSubscription
		require.NoError(t, DB.Where("id = ?", 9102).First(&sub).Error)
		assert.Equal(t, int64(9_000), sub.AmountUsed,
			"幂等记录没写成功就绝不能把额度退出去 —— 否则重试会一次次重复退款")

		var record SubscriptionPreConsumeRecord
		require.NoError(t, DB.Where("request_id = ?", requestId).First(&record).Error)
		assert.Equal(t, "consumed", record.Status)
	})
}

// TestSubscriptionWriteOffAllowanceIsOnePerResetPeriod 钉住核销名额的发放:
// 一个重置周期一份,重置时归零。
//
// 名额是 service/funding_source.go 上「平台核销」那条路的唯一上界;没有它,
// 并发路数就是平台白送额度的倍数。
func TestSubscriptionWriteOffAllowanceIsOnePerResetPeriod(t *testing.T) {
	const subId = 9103
	require.NoError(t, DB.Create(&UserSubscription{
		Id:          subId,
		UserId:      9003,
		PlanId:      1,
		AmountTotal: 10_000,
		AmountUsed:  10_000,
		Status:      "active",
		StartTime:   time.Now().Unix(),
		EndTime:     time.Now().Add(30 * 24 * time.Hour).Unix(),
	}).Error)
	t.Cleanup(func() { DB.Exec("DELETE FROM user_subscriptions WHERE id = ?", subId) })

	claimed, err := ClaimSubscriptionWriteOff(subId)
	require.NoError(t, err)
	assert.True(t, claimed, "本周期第一次核销必须拿得到名额")

	for i := 0; i < 5; i++ {
		claimed, err = ClaimSubscriptionWriteOff(subId)
		require.NoError(t, err)
		assert.False(t, claimed, "同一周期内不能发第二份名额")
	}

	var sub UserSubscription
	require.NoError(t, DB.Where("id = ?", subId).First(&sub).Error)
	assert.Equal(t, 1, sub.WriteOffCount, "名额计数只能加一次")

	// 周期重置(resetUserSubscriptionTx 走的那条路)之后名额必须回来,
	// 否则用户下个周期的套餐尾数又变回花不掉。
	plan := &SubscriptionPlan{Id: 1, Title: "reset plan", DurationUnit: "month", DurationValue: 1, Enabled: true}
	require.NoError(t, DB.Transaction(func(tx *gorm.DB) error {
		var s UserSubscription
		if err := tx.Where("id = ?", subId).First(&s).Error; err != nil {
			return err
		}
		return resetUserSubscriptionTx(tx, &s, plan, time.Now().Unix(), true)
	}))

	require.NoError(t, DB.Where("id = ?", subId).First(&sub).Error)
	assert.Equal(t, int64(0), sub.AmountUsed)
	assert.Equal(t, 0, sub.WriteOffCount, "核销名额必须与已用量一起归零")

	claimed, err = ClaimSubscriptionWriteOff(subId)
	require.NoError(t, err)
	assert.True(t, claimed)
}
