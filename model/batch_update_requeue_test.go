package model

// batch_update_requeue_test.go —— 批量队列落库失败时,增量必须回到队列。
//
// 缺陷形态(审计 consistency 视角 #8 的后半):batchUpdate() 先把整个
// store 换出来,之后 increaseTokenQuota / updateUserQuotaUsedQuotaAndRequestCount
// 出错时只 common.SysLog 一行就把增量丢掉 —— 不重排队、不回滚缓存。
// 于是一次数据库抖动就等于把那一批扣费永久抹掉:logs 里记着钱花了、佣金照常
// 按它计算并发放,而 users.quota / used_quota / request_count 一个字节没动。
// 方向是"用户白嫖 + 平台在同一笔上亏两次"。
//
// 前半(优雅退出不 flush)由 batch_update_flush_test.go 守着,这里只打失败分支。

import (
	"errors"
	"testing"

	"github.com/QuantumNous/new-api/common"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

// batchUpdateWhileUsersTableIsDown 在"users 表写不进去"的状态下跑一轮批量落库。
//
// 走 GORM 回调而不是关连接:关连接会把同一个进程里别的用例一起打挂。
// 故障必须在函数返回前解除 —— 用例接下来要断言"恢复之后恰好落一次"。
func batchUpdateWhileUsersTableIsDown(t *testing.T) {
	t.Helper()
	const name = "test:fail_user_update"
	require.NoError(t, DB.Callback().Update().Before("gorm:update").Register(name, func(tx *gorm.DB) {
		if tx.Statement.Table == "users" {
			tx.AddError(errors.New("Error 1205: Lock wait timeout exceeded"))
		}
	}))
	defer func() { _ = DB.Callback().Update().Remove(name) }()
	batchUpdate()
}

func queuedDelta(type_ int, id int) int {
	batchUpdateLocks[type_].Lock()
	defer batchUpdateLocks[type_].Unlock()
	return batchUpdateStores[type_][id]
}

// TestBatchUpdateRequeuesUserQuotaOnFailure 是本体:一次失败之后增量还在队列里,
// 恢复之后**恰好**落库一次。
//
// 独立算出的期望:排队 −30,000 额度 / +30,000 used / +1 次请求。失败那一轮
// users 表一个字节不动;恢复后 quota 100,000 → 70,000,used_quota 0 → 30,000,
// request_count 0 → 1。多落一次就是 40,000,少落一次就是 100,000 —— 两边都能看出来。
func TestBatchUpdateRequeuesUserQuotaOnFailure(t *testing.T) {
	const userId = 9711
	require.NoError(t, DB.Create(&User{
		Id: userId, Username: "batch_requeue_user", Quota: 100_000,
	}).Error)
	t.Cleanup(func() { DB.Exec("DELETE FROM users WHERE id = ?", userId) })

	prev := common.BatchUpdateEnabled
	common.BatchUpdateEnabled = true
	t.Cleanup(func() {
		common.BatchUpdateEnabled = prev
		batchUpdate() // 队列是包级状态,跑完排空,免得漏进别的用例
	})

	require.NoError(t, DecreaseUserQuota(userId, 30_000, false))
	UpdateUserUsedQuotaAndRequestCount(userId, 30_000)
	require.Equal(t, -30_000, queuedDelta(BatchUpdateTypeUserQuota, userId),
		"前提:开了批量更新之后这些扣减确实只在内存里")

	batchUpdateWhileUsersTableIsDown(t)

	var user User
	require.NoError(t, DB.Where("id = ?", userId).First(&user).Error)
	require.Equal(t, 100_000, user.Quota, "前提:这一轮确实没落库")

	assert.Equal(t, -30_000, queuedDelta(BatchUpdateTypeUserQuota, userId),
		"落库失败的额度增量必须回到队列 —— 丢掉它就是把一笔已经计入账单的消费白送")
	assert.Equal(t, 30_000, queuedDelta(BatchUpdateTypeUsedQuota, userId))
	assert.Equal(t, 1, queuedDelta(BatchUpdateTypeRequestCount, userId))

	// 数据库恢复,下一轮把它落进去,而且只落一次。
	batchUpdate()
	require.NoError(t, DB.Where("id = ?", userId).First(&user).Error)
	assert.Equal(t, 70_000, user.Quota)
	assert.Equal(t, 30_000, user.UsedQuota)
	assert.Equal(t, 1, user.RequestCount)

	batchUpdate()
	require.NoError(t, DB.Where("id = ?", userId).First(&user).Error)
	assert.Equal(t, 70_000, user.Quota, "重排队不能变成重复扣款")
}

// TestBatchUpdateRequeueMergesWithNewDeltas 钉住重排队用的是"相加"而不是"覆盖"。
//
// 覆盖式重排队在真实时序下会吞钱:一批正在落库(队列已经被换出),此时新的
// 请求把新的扣费排进空队列;落库失败后如果用赋值写回,那笔新扣费就被旧值顶掉。
// 这里用 GORM 回调在**那条 UPDATE 正在执行的那一刻**排进新增量,把这个窗口
// 变成一条确定性的用例,而不是靠并发碰运气。
func TestBatchUpdateRequeueMergesWithNewDeltas(t *testing.T) {
	const userId = 9712
	require.NoError(t, DB.Create(&User{
		Id: userId, Username: "batch_merge_user", Quota: 100_000,
	}).Error)
	t.Cleanup(func() { DB.Exec("DELETE FROM users WHERE id = ?", userId) })

	prev := common.BatchUpdateEnabled
	common.BatchUpdateEnabled = true
	t.Cleanup(func() {
		common.BatchUpdateEnabled = prev
		batchUpdate()
	})

	require.NoError(t, DecreaseUserQuota(userId, 10_000, false))

	const name = "test:fail_and_enqueue"
	require.NoError(t, DB.Callback().Update().Before("gorm:update").Register(name, func(tx *gorm.DB) {
		if tx.Statement.Table != "users" {
			return
		}
		// 队列此刻是空的(已被换出),新的一笔消费排了进来。
		require.NoError(t, DecreaseUserQuota(userId, 5_000, false))
		tx.AddError(errors.New("Error 1205: Lock wait timeout exceeded"))
	}))
	batchUpdate()
	require.NoError(t, DB.Callback().Update().Remove(name))

	assert.Equal(t, -15_000, queuedDelta(BatchUpdateTypeUserQuota, userId),
		"重排队必须与在途新增量相加;覆盖会把落库期间产生的那笔消费吞掉")

	batchUpdate()
	var user User
	require.NoError(t, DB.Where("id = ?", userId).First(&user).Error)
	assert.Equal(t, 85_000, user.Quota)
}

// TestBatchUpdateRequeuesTokenQuotaOnFailure 是令牌那一路的同一条不变量。
//
// 令牌的 remain_quota 是"这把 key 还能花多少"。丢掉一次扣减的方向同样是
// 用户白嫖 —— 而且它与用户额度是两笔独立的账,补不回来。
func TestBatchUpdateRequeuesTokenQuotaOnFailure(t *testing.T) {
	const tokenId = 9713
	require.NoError(t, DB.Create(&Token{
		Id: tokenId, UserId: 1, Key: "batch-requeue-token", Name: "batch requeue",
		RemainQuota: 100_000,
	}).Error)
	t.Cleanup(func() { DB.Exec("DELETE FROM tokens WHERE id = ?", tokenId) })

	prev := common.BatchUpdateEnabled
	common.BatchUpdateEnabled = true
	t.Cleanup(func() {
		common.BatchUpdateEnabled = prev
		batchUpdate()
	})

	require.NoError(t, DecreaseTokenQuota(tokenId, "batch-requeue-token", 20_000))
	require.Equal(t, -20_000, queuedDelta(BatchUpdateTypeTokenQuota, tokenId))

	const name = "test:fail_token_update"
	require.NoError(t, DB.Callback().Update().Before("gorm:update").Register(name, func(tx *gorm.DB) {
		if tx.Statement.Table == "tokens" {
			tx.AddError(errors.New("Error 1205: Lock wait timeout exceeded"))
		}
	}))
	batchUpdate()
	require.NoError(t, DB.Callback().Update().Remove(name))

	assert.Equal(t, -20_000, queuedDelta(BatchUpdateTypeTokenQuota, tokenId),
		"令牌额度落库失败同样必须回到队列")

	batchUpdate()
	var tk Token
	require.NoError(t, DB.Where("id = ?", tokenId).First(&tk).Error)
	assert.EqualValues(t, 80_000, tk.RemainQuota)
}
