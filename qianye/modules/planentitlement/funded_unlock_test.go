package planentitlement

import (
	"testing"

	"github.com/QuantumNous/new-api/common"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// funded_unlock_test.go —— 「套餐耗尽闸门」在本包这一侧的判据。
//
// ═══════════ 它守的是复核里那条 blocker ═══════════
//
// 解锁的授予判据只看 `status='active' AND end_time > now`,与"这条订阅还剩多少
// 余额"完全无关。于是套餐额度烧光之后,用户仍然持有该套餐解锁的模型分组,并且
// 在 allow_wallet_overflow=true 时直接改从钱包出资继续跑 —— 一个只有买套餐才
// 够得着的分组(平台常把它的 GroupRatio 配成 0 做补贴价),被用钱包无限跑到套餐
// 自然到期。
//
// 修法是在 per-user 缓存条目里多带一份 FundedPlanIds(同一次查询取出,零额外
// I/O),再由 FundedUnlockState 把「解锁」与「还有余额」两件事**分开**回答。
// 本文件钉的就是这个分开:两个返回值任何一个被写成另一个的同义词,或者余额判据
// 被改回"不看 amount_used",这里都会红。
//
// 出资侧真正的拒绝动作在 qianye/modules/groupns(它还要把用户分组本身含不含这个
// 模型分组算进去),不在本包 —— 所以这里只断言本包必须给出的那两个布尔。

// fundedState 是一次干净的调用:每个用例都自己重建两层缓存,
// 否则上一个用例的 per-user 条目会让下一个用例读到别人的余额。
func fundedState(t *testing.T, userId int, modelGroup string) (bool, bool) {
	t.Helper()
	resetCaches()
	require.NoError(t, reload())
	return FundedUnlockState(userId, modelGroup)
}

func TestFundedUnlockStateSeparatesUnlockedFromFunded(t *testing.T) {
	gdb := newExtDB(t)
	mainDB := newMainDB(t)
	setGroupRatios(t, `{"default":1,"pro":0,"lab":0.5}`)
	seedPlan(t, mainDB, 1, "套餐A")
	putGrant(t, gdb, 1, "pro")

	t.Run("有余额:解锁成立、出资也成立", func(t *testing.T) {
		require.NoError(t, mainDB.Exec("DELETE FROM user_subscriptions").Error)
		seedSubscription(t, mainDB, 10, 7, 1, 30*86400, 1000, 400)
		unlocked, funded := fundedState(t, 7, "pro")
		assert.True(t, unlocked)
		assert.True(t, funded)
	})

	t.Run("额度烧光:解锁仍成立,但出资必须不成立", func(t *testing.T) {
		// 这一条就是复核里那条 blocker 的最小复现。funded 若为 true,
		// 闸门整体放行 → 用户在一个只有套餐才给得起的分组上用钱包无限跑。
		require.NoError(t, mainDB.Exec("DELETE FROM user_subscriptions").Error)
		seedSubscription(t, mainDB, 11, 7, 1, 30*86400, 1000, 1000)
		unlocked, funded := fundedState(t, 7, "pro")
		assert.True(t, unlocked, "订阅还没到期,分组仍然是这个套餐解锁的")
		assert.False(t, funded, "额度已用尽,这一笔不该再由这个套餐出资")
	})

	t.Run("用超也算烧光", func(t *testing.T) {
		// amount_used > amount_total 是结算差额的正常中间态,判据必须是 >=。
		require.NoError(t, mainDB.Exec("DELETE FROM user_subscriptions").Error)
		seedSubscription(t, mainDB, 12, 7, 1, 30*86400, 1000, 1200)
		_, funded := fundedState(t, 7, "pro")
		assert.False(t, funded)
	})

	t.Run("amount_total<=0 是不限量,不是零额度", func(t *testing.T) {
		// 上游 PreConsumeUserSubscription 的 `if sub.AmountTotal > 0` 分支就是
		// 这个口径。写成"零额度"会让所有不限量套餐在买回来的第一秒就被判耗尽。
		require.NoError(t, mainDB.Exec("DELETE FROM user_subscriptions").Error)
		seedSubscription(t, mainDB, 13, 7, 1, 30*86400, 0, 5000)
		unlocked, funded := fundedState(t, 7, "pro")
		assert.True(t, unlocked)
		assert.True(t, funded)
	})

	t.Run("多套餐:只要还有一张有余额的套餐解锁它,就算有出资", func(t *testing.T) {
		seedPlan(t, mainDB, 2, "套餐B")
		putGrant(t, gdb, 2, "pro")
		require.NoError(t, mainDB.Exec("DELETE FROM user_subscriptions").Error)
		seedSubscription(t, mainDB, 14, 7, 1, 30*86400, 1000, 1000) // 烧光
		seedSubscription(t, mainDB, 15, 7, 2, 60*86400, 1000, 10)   // 还有
		unlocked, funded := fundedState(t, 7, "pro")
		assert.True(t, unlocked)
		assert.True(t, funded, "另一张套餐还供得起,这一笔不该被闸门拦下")
	})

	t.Run("多套餐:全部烧光才算没有出资", func(t *testing.T) {
		require.NoError(t, mainDB.Exec("DELETE FROM user_subscriptions").Error)
		seedSubscription(t, mainDB, 16, 7, 1, 30*86400, 1000, 1000)
		seedSubscription(t, mainDB, 17, 7, 2, 60*86400, 500, 500)
		unlocked, funded := fundedState(t, 7, "pro")
		assert.True(t, unlocked)
		assert.False(t, funded)
	})

	t.Run("没被任何套餐解锁的分组:两个都是 false,闸门整体放行", func(t *testing.T) {
		// unlocked=false 时调用方必须放行:那一档是用户分组自己的成员资格,
		// 不归本闸门管,越权拒绝会把一次口径差异变成一次线上故障。
		require.NoError(t, mainDB.Exec("DELETE FROM user_subscriptions").Error)
		seedSubscription(t, mainDB, 18, 7, 1, 30*86400, 1000, 1000)
		unlocked, funded := fundedState(t, 7, "lab")
		assert.False(t, unlocked)
		assert.False(t, funded)
	})

	t.Run("auto 与匿名口径永不参与出资判定", func(t *testing.T) {
		require.NoError(t, mainDB.Exec("DELETE FROM user_subscriptions").Error)
		seedSubscription(t, mainDB, 19, 7, 1, 30*86400, 1000, 1000)
		unlocked, funded := fundedState(t, 7, autoGroup)
		assert.False(t, unlocked)
		assert.False(t, funded)

		unlocked, funded = fundedState(t, 0, "pro")
		assert.False(t, unlocked)
		assert.False(t, funded)
	})
}

// TestFundedUnlockStateCountsPendingPeriodReset 锁住「周期重置到期 = 仍有余额」。
//
// ── 它保护的是每个重置边界上的一次误拒 ──
//
// 上游的周期重置是**懒执行**的:amount_used 只在 PreConsumeUserSubscription 的事务内
// (maybeResetUserSubscriptionWithPlanTx)或 1 分钟一跳的 master-node 后台任务里才归零。
// 只看 amount_used/amount_total 的话,一张刚跨过重置点、额度即将归零重来的套餐会在这里
// 被判成「已耗尽」;而套餐耗尽闸门返回的是 AccessDenied 而不是 InsufficientUserQuota,
// wallet_first 连 trySubscription 回落都不会发生 —— **而 trySubscription 恰恰是唯一能
// 触发那次重置的路径**,所以这一档不会自愈,只能等后台任务。
// 集群误配(IsMasterNode 恒 false)时后台任务根本不跑,窗口无上界。
func TestFundedUnlockStateCountsPendingPeriodReset(t *testing.T) {
	gdb := newExtDB(t)
	mainDB := newMainDB(t)
	setGroupRatios(t, `{"default":1,"pro":0}`)
	seedPlan(t, mainDB, 1, "月度套餐")
	putGrant(t, gdb, 1, "pro")

	t.Run("重置时刻已过、重置尚未落库:必须算作有余额", func(t *testing.T) {
		require.NoError(t, mainDB.Exec("DELETE FROM user_subscriptions").Error)
		seedSubscription(t, mainDB, 30, 7, 1, 30*86400, 1000, 1000)
		require.NoError(t, mainDB.Exec(
			"UPDATE user_subscriptions SET next_reset_time = ? WHERE id = 30",
			common.GetTimestamp()-1).Error)
		unlocked, funded := fundedState(t, 7, "pro")
		assert.True(t, unlocked)
		assert.True(t, funded, "额度即将归零重来,不该在这个窗口里把付费用户挡死")
	})

	t.Run("重置时刻还没到:仍然是真耗尽", func(t *testing.T) {
		require.NoError(t, mainDB.Exec("DELETE FROM user_subscriptions").Error)
		seedSubscription(t, mainDB, 31, 7, 1, 30*86400, 1000, 1000)
		require.NoError(t, mainDB.Exec(
			"UPDATE user_subscriptions SET next_reset_time = ? WHERE id = 31",
			common.GetTimestamp()+86400).Error)
		_, funded := fundedState(t, 7, "pro")
		assert.False(t, funded, "下次重置还在未来,这一档就是真的耗尽")
	})

	t.Run("从不重置(next_reset_time=0):判据不变", func(t *testing.T) {
		require.NoError(t, mainDB.Exec("DELETE FROM user_subscriptions").Error)
		seedSubscription(t, mainDB, 32, 7, 1, 30*86400, 1000, 1000)
		_, funded := fundedState(t, 7, "pro")
		assert.False(t, funded)
	})
}

// TestFundedUnlockStateAcceptsGenericBalancePlans 锁住 funded 的判据是
// **CandidateUsable 而不是 Bind**。
//
// ── 它保护的是一条本来能付款、却被闸门砍掉的路径 ──
//
// 订阅出资的真实判据是 model.PreConsumeUserSubscription 候选循环里的
// QySubscriptionCandidateUsable:余额使用范围为「通用」的套餐可以为**任何**模型分组
// 出资,它根本不需要绑定 M。只在 Bind[planId][M] 命中的套餐里找余额,会把
// 「P1 解锁 M 但已用尽 + P2 通用余额充足」这种再正常不过的组合判成"耗尽",
// 而闸门返回的 AccessDenied 又不触发 wallet_first 的 trySubscription 回落。
func TestFundedUnlockStateAcceptsGenericBalancePlans(t *testing.T) {
	gdb := newExtDB(t)
	mainDB := newMainDB(t)
	setGroupRatios(t, `{"default":1,"pro":0}`)
	seedPlan(t, mainDB, 1, "解锁套餐")
	seedPlan(t, mainDB, 2, "通用余额套餐")
	putGrant(t, gdb, 1, "pro") // 只有 P1 绑定 pro

	t.Run("P1 解锁 pro 但已用尽、P2 通用余额充足:必须算作有出资", func(t *testing.T) {
		require.NoError(t, mainDB.Exec("DELETE FROM user_subscriptions").Error)
		seedSubscription(t, mainDB, 40, 7, 1, 30*86400, 1000, 1000)
		seedSubscription(t, mainDB, 41, 7, 2, 60*86400, 1000, 0)
		unlocked, funded := fundedState(t, 7, "pro")
		assert.True(t, unlocked, "pro 仍然由 P1 解锁")
		assert.True(t, funded, "P2 是通用余额,它付得起这一笔 —— 闸门不该拒绝")
	})

	t.Run("P2 的余额被限定在别的分组上:才算没有出资", func(t *testing.T) {
		putPolicy(t, gdb, 2, "restricted")
		putGrant(t, gdb, 2, "lab") // P2 的余额只能用在 lab 上
		enforceBalanceScope(t)
		require.NoError(t, mainDB.Exec("DELETE FROM user_subscriptions").Error)
		seedSubscription(t, mainDB, 42, 7, 1, 30*86400, 1000, 1000)
		seedSubscription(t, mainDB, 43, 7, 2, 60*86400, 1000, 0)
		unlocked, funded := fundedState(t, 7, "pro")
		assert.True(t, unlocked)
		assert.False(t, funded, "P2 的余额用不到 pro 上,pro 这一档确实没有出资来源")
	})
}
