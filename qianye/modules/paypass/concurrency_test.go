package paypass

import (
	"context"
	"strconv"
	"sync"
	"testing"

	"github.com/QuantumNous/new-api/common"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// concurrentAttempts 是并发试密的协程数。
//
// 取 12 而不是更大:这条断言的判据是"计数必须等于并发数",而不是"跑得够久"。
// 12 已经足以让读-改-写这种写法丢掉大部分计数(实测把 counter.go 的自增换成
// 读-改-写后,计数掉到 4),再往上只是让 bcrypt 与 sqlite 的写锁把 CI 拖慢。
const concurrentAttempts = 12

// 同一账号并发试密,错误计数必须一次不少地记满。
//
// # 这条测试守的是什么
//
// 裁决 1 的原话:「错误计数必须防并发绕过:同一账号并发打 N 个请求不能只算 1 次。」
// 反面写法(读-改-写)在任何隔离级别下都会丢计数:N 个协程读到同一个旧值,
// 各自 +1 写回,最终只涨 1。攻击者只要保持并发就永远到不了锁定阈值 ——
// 也就是说锁定策略在最需要它的场景(自动化爆破)下恰好失效。
//
// 这里刻意用一个高得打不到的阈值,把"计数是否完整"与"锁定是否触发"分成两条
// 独立的断言:合在一起时,一个丢了 15 次计数但仍然锁上的实现会蒙混过关。
func TestConcurrentWrongAttemptsAreAllCounted(t *testing.T) {
	gdb := newTestDB(t)
	putSetting(t, gdb, "pay_pwd_max_attempts", strconv.Itoa(maxMaxAttempts))
	setPassword(t, gdb, 7100, goodPassword)

	var wg sync.WaitGroup
	errs := make([]error, concurrentAttempts)
	for i := 0; i < concurrentAttempts; i++ {
		wg.Add(1)
		go func(idx int) {
			defer wg.Done()
			errs[idx] = verify(context.Background(), 7100, "definitely-wrong")
		}(i)
	}
	wg.Wait()

	for i, err := range errs {
		require.ErrorIs(t, err, errPayPwdWrong, "第 %d 个并发请求的结果不对", i)
	}
	assert.Equal(t, concurrentAttempts, rowOf(t, gdb, 7100).FailCount,
		"并发试密丢了计数 —— 错误计数必须是数据库侧的原子自增,不能是读-改-写")
}

// 并发试密同样必须触发锁定,且锁定时间不会被并发者互相缩短。
//
// # 为什么这里断言的是"至少 5 次"而不是"正好 20 次"
//
// 锁一旦落下,后到的请求走的是"已锁定"分支,**不再计数** —— 这是刻意的:
// 让锁定期内的失败继续累加,等于给了一条"持续打请求把锁越续越长"的骚扰路径。
// 于是最终计数落在 [阈值, 并发数] 区间内,取决于有多少个协程在锁落下之前
// 就通过了锁检查。计数完整性由上面那条(阈值调到打不到)独立钉住,
// 这里只负责"并发下确实锁上了、且锁的时长没被互相压短"。
func TestConcurrentWrongAttemptsStillLock(t *testing.T) {
	gdb := newTestDB(t)
	putSetting(t, gdb, "pay_pwd_max_attempts", "5")
	putSetting(t, gdb, "pay_pwd_lock_minutes", "20")
	setPassword(t, gdb, 7110, goodPassword)

	start := common.GetTimestamp()
	var wg sync.WaitGroup
	for i := 0; i < concurrentAttempts; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_ = verify(context.Background(), 7110, "definitely-wrong")
		}()
	}
	wg.Wait()

	row := rowOf(t, gdb, 7110)
	assert.GreaterOrEqual(t, row.FailCount, 5,
		"并发试密没有累计到阈值 —— 计数一定是被读-改-写吞掉了")
	assert.LessOrEqual(t, row.FailCount, concurrentAttempts,
		"计数超过了并发数 —— 说明有请求被重复计了")
	// 只断言"锁确实落下且时长按配置算":并发下各协程的 now 只差一两秒,
	// 这条断言抓不到"锁被压短"——那一条由 TestNoteFailureNeverShortensAnExistingLock
	// 单独盯着,写在这里只会是一条永真断言。
	assert.GreaterOrEqual(t, row.LockedUntil, start+20*60,
		"锁定时长与 pay_pwd_lock_minutes 对不上")
	assert.ErrorIs(t, verify(context.Background(), 7110, goodPassword), errPayPwdLocked)
}

// 加锁语句只延长、不缩短。
//
// 直接测 noteFailure 而不是靠并发去撞:并发下 now 只差一两秒,即使把
// `locked_until < ?` 这个守卫删掉,断言也几乎不可能变红 —— 那样的测试是
// 假回归。这里把已有的锁设得远在未来,再让它记一次失败,守卫没了就会被压短。
func TestNoteFailureNeverShortensAnExistingLock(t *testing.T) {
	gdb := newTestDB(t)
	setPassword(t, gdb, 7120, goodPassword)
	now := common.GetTimestamp()
	longLock := now + 100000
	require.NoError(t, gdb.Model(&PayPassword{}).Where("user_id = ?", 7120).
		Updates(map[string]any{"fail_count": 9, "locked_until": longLock}).Error)

	handleDB, err := handle(context.Background())
	require.NoError(t, err)
	_, err = noteFailure(context.Background(), handleDB, 7120, now,
		policy{MaxAttempts: 1, LockMinutes: 1})
	require.NoError(t, err)

	assert.Equal(t, longLock, rowOf(t, gdb, 7120).LockedUntil,
		"已有的锁被一次新的失败压短了 —— 加锁必须带 locked_until < ? 守卫")
}
