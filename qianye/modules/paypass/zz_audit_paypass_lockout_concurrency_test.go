package paypass

import (
	"context"
	"strconv"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/QuantumNous/new-api/common"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// zz_audit_paypass_lockout_concurrency_test.go —— 资金系统审计:错误计数在
// "锁定到期残留态"下的并发原子性,以及首次设置的并发抢占。既有 concurrency_test.go
// 只证明"从干净态(fail_count=0)并发试密不丢计数",本文件补上它的盲区:账号被锁 →
// 到期 → fail_count 尚未清零的残留态,以及并发首次设置只能有唯一赢家。
//
// 攻击面:verify 步骤② 在锁到期时用 clearExpiredLock 把 fail_count 无条件清 0,
// 步骤④ 的 noteFailure 又做 fail_count = fail_count + 1。两条 UPDATE 分属不同
// 协程、互相穿插。如果"清零"能抹掉并发穿插进来的自增,攻击者就能靠"卡在锁到期
// 的那一刻发并发"把计数压回低位、拖慢重新锁定,拿到超出一个窗口的试密次数。

const auditConcurrency = 24

// 锁到期残留态下的并发试密:清零只应发生一次,其后每一次错误都必须被记满。
//
// # 为什么这是一条真不变量而不是凑覆盖率
//
// 它守的是"锁到期给新窗口"这条逻辑与"并发原子自增"这条逻辑的**交叉点**。
// 单独看两者既有测试都覆盖了,但它们相交的地方(清零 UPDATE 与自增 UPDATE
// 并发穿插)没有任何测试盯着。把 verify 步骤② 的清零挪到步骤④ 之后,或把
// clearExpiredLock 的 WHERE 守卫(locked_until <> 0)删掉让它每个并发者都清一次,
// 这条会立刻抓到"计数被清零吃掉了"。
//
// 阈值取允许区间的上界 100,避免"重新锁上后不再计数"这条支路混入 —— 那样就
// 分不清"计数没到"是被清零吃了还是被锁停了。
func TestAuditExpiredLockResetKeepsEveryConcurrentFailure(t *testing.T) {
	gdb := newTestDB(t)
	const userId = 7900
	setPassword(t, gdb, userId, goodPassword)
	putSetting(t, gdb, "pay_pwd_max_attempts", strconv.Itoa(maxMaxAttempts)) // 100,打不到

	// 残留态:上一轮锁定留下的高计数 + 已过期的锁。
	require.NoError(t, gdb.Model(&PayPassword{}).Where("user_id = ?", userId).
		Updates(map[string]any{
			"fail_count":   50,
			"locked_until": common.GetTimestamp() - 10,
		}).Error)

	var wg sync.WaitGroup
	errs := make([]error, auditConcurrency)
	for i := 0; i < auditConcurrency; i++ {
		wg.Add(1)
		go func(idx int) {
			defer wg.Done()
			errs[idx] = verify(context.Background(), userId, "definitely-wrong")
		}(i)
	}
	wg.Wait()

	for i, err := range errs {
		require.ErrorIs(t, err, errPayPwdWrong, "第 %d 个并发请求结果不对", i)
	}
	// 残留的 50 必须被清成 0(给新窗口),而清零之后的 24 次并发错误必须一次不少。
	// 若清零 UPDATE 抹掉了并发穿插的自增,这里会 < 24。
	assert.Equal(t, auditConcurrency, rowOf(t, gdb, userId).FailCount,
		"锁到期残留态下并发试密丢了计数 —— 清零 UPDATE 抹掉了并发的原子自增,"+
			"攻击者可借此把计数压回低位、拖慢重新锁定")
}

// 锁到期残留态下,并发试密同样必须能在生产阈值(5)下重新锁上。
//
// 上一条把阈值抬到打不到、单测"计数完整";这一条用生产默认阈值,证明
// "从残留态出发,并发暴破仍然会被重新锁住",即锁定策略在最需要它的场景
// (自动化爆破卡在锁到期瞬间)下不失效。
func TestAuditExpiredLockResidueStillRelocksUnderConcurrency(t *testing.T) {
	gdb := newTestDB(t)
	const userId = 7901
	setPassword(t, gdb, userId, goodPassword)
	putSetting(t, gdb, "pay_pwd_max_attempts", "5")
	putSetting(t, gdb, "pay_pwd_lock_minutes", "30")
	require.NoError(t, gdb.Model(&PayPassword{}).Where("user_id = ?", userId).
		Updates(map[string]any{
			"fail_count":   5,
			"locked_until": common.GetTimestamp() - 10,
		}).Error)

	var wg sync.WaitGroup
	for i := 0; i < auditConcurrency; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_ = verify(context.Background(), userId, "definitely-wrong")
		}()
	}
	wg.Wait()

	row := rowOf(t, gdb, userId)
	assert.GreaterOrEqual(t, row.FailCount, 5, "残留态并发暴破没有累计到阈值")
	assert.Greater(t, row.LockedUntil, common.GetTimestamp(), "残留态并发暴破没有重新锁上")
	// 锁上之后,连正确密码也必须被拒。
	assert.ErrorIs(t, verify(context.Background(), userId, goodPassword), errPayPwdLocked,
		"重新锁定后正确密码仍被放行 —— 锁只惩罚了本人、挡不住拿到密码的攻击者")
}

// 首次设置是一次性动作:同一新用户并发提交多个不同密码,只能有唯一赢家,
// 且最终落库的正是那个赢家的哈希 —— 不能出现"用户以为设的是 A、实际生效的是 B"。
//
// module.go 明确把首次设置称作"并发抢占的入口",但 TestSetIsFirstTimeOnly 只串行
// 试过。这条用并发把 claimFirstPassword 的 CAS(INSERT DO NOTHING → RowsAffected)
// 真正打出来:把它退化成"先 SELECT 判断没设过、再 INSERT"两步,并发下多个协程都会
// 判成"没设过"、最后落库的是后到的那个,这条会同时抓到"赢家不止一个"与"落库哈希
// 不属于任何一个赢家"。
func TestAuditConcurrentFirstSetHasExactlyOneWinner(t *testing.T) {
	gdb := newTestDB(t)
	const userId = 7950
	handleDB, err := handle(context.Background())
	require.NoError(t, err)

	// 每个协程一个各不相同的密码(因而哈希各不相同)。
	hashes := make([]string, auditConcurrency)
	for i := range hashes {
		h, herr := hashPassword("first-set-pwd-" + strconv.Itoa(i))
		require.NoError(t, herr)
		hashes[i] = h
	}

	var winners atomic.Int64
	var winningHash atomic.Value // string
	var wg sync.WaitGroup
	now := common.GetTimestamp()
	for i := 0; i < auditConcurrency; i++ {
		wg.Add(1)
		go func(idx int) {
			defer wg.Done()
			ok, cerr := claimFirstPassword(context.Background(), handleDB, userId, hashes[idx], now)
			if cerr != nil {
				return
			}
			if ok {
				winners.Add(1)
				winningHash.Store(hashes[idx])
			}
		}(i)
	}
	wg.Wait()

	assert.EqualValues(t, 1, winners.Load(),
		"并发首次设置出现了 %d 个赢家 —— CAS 退化成了读-改-写,首次设置不再是一次性动作",
		winners.Load())

	// 落库的哈希必须正是那个唯一赢家提交的,而不是"某个后到的协程悄悄盖上去"的。
	stored := rowOf(t, gdb, userId).Hash
	assert.Equal(t, winningHash.Load(), stored,
		"落库哈希不是那个 CAS 赢家的 —— 用户以为设的密码和实际生效的不是同一个")
}
