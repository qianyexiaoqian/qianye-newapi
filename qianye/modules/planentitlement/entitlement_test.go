package planentitlement

import (
	"reflect"
	"testing"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/model"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// entitlement_test.go —— 第二层缓存的两个失败方向,以及余额范围过滤的判定。
//
// 两个方向都要量,而且它们**不是**同一件事:
//
//	冷未命中 + 读失败 → fail-closed(视为无解锁)。分不清"没有套餐"与"读不到",
//	                    失败时放行等于让付费解锁变成免费。
//	已有正结果 + 刷新失败 → serve-stale。用户已经付过款,而且陈旧期内他仍按该
//	                    模型分组的正确倍率付费,平台不损失一分钱。
//
// 只实现其中一个方向的代价:前者缺失是收入漏洞,后者缺失是"扩展库抖一下,
// 所有付费用户当场 403"。

// breakMainDB 让主库上的订阅查询必定失败,模拟"读不到"。
func breakMainDB(t *testing.T) {
	t.Helper()
	require.NoError(t, model.DB.Migrator().DropTable(&model.UserSubscription{}))
	t.Cleanup(func() { _ = model.DB.AutoMigrate(&model.UserSubscription{}) })
}

// TestColdMissFailsClosed 守 fail-closed 那一档,并守它**有声**。
//
// 这条路径的表现是"少数付费用户偶发 403",与"这个分组真的没配"长得一模一样。
// 没有计数器就永远查不出来,所以计数器与行为一样重要,一起断言。
func TestColdMissFailsClosed(t *testing.T) {
	gdb := newExtDB(t)
	mainDB := newMainDB(t)
	setGroupRatios(t, `{"default":1,"pro":0.8}`)
	seedPlan(t, mainDB, 1, "套餐A")
	putGrant(t, gdb, 1, "pro")
	require.NoError(t, reload())
	seedSubscription(t, mainDB, 11, 7, 1, 86400, 1000, 0)
	breakMainDB(t)

	in := map[string]string{"default": "默认分组"}
	got := Resolve(7, in)
	assert.Equal(t, reflect.ValueOf(in).Pointer(), reflect.ValueOf(got).Pointer(),
		"读不到就当没有解锁 —— 放行等于让付费解锁变成免费")
	assert.Positive(t, failClosed.Load(),
		"fail-closed 必须计数:它的表现与「分组真的没配」完全一样,不出数就查不出来")
	assert.Positive(t, Health()["entitlement_fail_closed"],
		"健康面板必须能看到它,非 0 即需要关注")
}

// TestStaleEntryIsServedWhileRefreshFails 守 serve-stale。
//
// 用直接改写缓存条目的 LoadedAt 来制造"陈旧",而不是 sleep:sleep 会让这条用例
// 又慢又不稳,最后一定被人加大阈值或删掉。
func TestStaleEntryIsServedWhileRefreshFails(t *testing.T) {
	gdb := newExtDB(t)
	mainDB := newMainDB(t)
	setGroupRatios(t, `{"default":1,"pro":0.8}`)
	seedPlan(t, mainDB, 1, "套餐A")
	putGrant(t, gdb, 1, "pro")
	require.NoError(t, reload())
	seedSubscription(t, mainDB, 11, 7, 1, 86400, 1000, 0)

	// 先正常解析一次,把这个人的解锁装进缓存。
	require.Contains(t, Resolve(7, map[string]string{"default": "默认分组"}), "pro")

	// 让它变陈旧(超过 user_cache_seconds=60,但没超过 user_max_stale_seconds=300),
	// 同时让回源必定失败。
	e, found, err := getUserCache().Get(7)
	require.NoError(t, err)
	require.True(t, found)
	e.LoadedAt = common.GetTimestamp() - 120
	getUserCache().Set(7, e)
	breakMainDB(t)

	got := Resolve(7, map[string]string{"default": "默认分组"})
	assert.Contains(t, got, "pro",
		"刷新失败时必须继续沿用上一次成功的结果 —— 用户已经付过款,"+
			"而且陈旧期内他仍按该分组的正确倍率付费,平台不损失一分钱")

	// 超过 max_stale 之后才降级:那时已经不能再声称"这是他刚才的状态"。
	e.LoadedAt = common.GetTimestamp() - 400
	getUserCache().Set(7, e)
	in := map[string]string{"default": "默认分组"}
	assert.Equal(t, reflect.ValueOf(in).Pointer(), reflect.ValueOf(Resolve(7, in)).Pointer())
}

// TestNegativeResultHasShorterTTL 守「刚买完套餐多久能用」。
//
// "这个人没有任何活跃订阅"是必须被缓存的正常值(绝大多数用户都是这样),
// 但它同时是**刚买完套餐的人**上一秒的状态。两者用同一个 TTL 的话,
// 新用户买完之后最长要等一个完整周期才能用上他付过钱的分组。
func TestNegativeResultHasShorterTTL(t *testing.T) {
	newExtDB(t)
	newMainDB(t)
	assert.Equal(t, int64(60), userCacheSeconds())
	assert.Equal(t, int64(15), negativeCacheSeconds(),
		"负结果的缓存时长必须明显短于正结果,它就是「买完还不能用」那段等待的上界")
}

// TestInvalidateUserTakesEffectImmediately 守购买后主动失效那条路径的落点。
//
// 订阅创建事务里的名额闸门会顺手调 InvalidateUser(见 modules/subscription/gate.go)。
// 这里只量"失效之后确实会重新回源",闸门那一侧的调用由它自己的用例覆盖。
func TestInvalidateUserTakesEffectImmediately(t *testing.T) {
	gdb := newExtDB(t)
	mainDB := newMainDB(t)
	setGroupRatios(t, `{"default":1,"pro":0.8}`)
	seedPlan(t, mainDB, 1, "套餐A")
	putGrant(t, gdb, 1, "pro")
	require.NoError(t, reload())

	in := map[string]string{"default": "默认分组"}
	// 还没买:负结果进缓存。
	require.Equal(t, reflect.ValueOf(in).Pointer(), reflect.ValueOf(Resolve(7, in)).Pointer())

	seedSubscription(t, mainDB, 11, 7, 1, 86400, 1000, 0)
	assert.Equal(t, reflect.ValueOf(in).Pointer(), reflect.ValueOf(Resolve(7, in)).Pointer(),
		"负结果还在缓存里,这一刻拿不到解锁是预期行为")

	InvalidateUser(7)
	assert.Contains(t, Resolve(7, in), "pro", "失效之后必须立刻重新回源")
}

// TestCandidateUsableFailsOpen 守余额范围过滤的**方向**。
//
// 这个判定跑在 model.PreConsumeUserSubscription 的候选循环里 —— 主库事务内、
// 候选行已持 FOR UPDATE 锁。任何"不确定"都必须返回 true(不跳过):
// 最坏结果是仅限套餐被当成通用花掉(钱还在用户自己的池子里,可事后对账),
// 而 fail-closed 会卡住一个已上锁的扣款事务。
func TestCandidateUsableFailsOpen(t *testing.T) {
	gdb := newExtDB(t)
	setGroupRatios(t, `{"pro":0.8,"lab":0.1}`)
	enforceBalanceScope(t)

	// 快照从未加载 —— 一切照上游来。
	current.Store(nil)
	assert.True(t, CandidateUsable(1, "pro"))

	putGrant(t, gdb, 1, "pro")
	putPolicy(t, gdb, 1, ScopeRestricted)
	putGrant(t, gdb, 2, "pro") // 有绑定但没配范围 → universal
	putPolicy(t, gdb, 3, "什么乱七八糟的值")
	require.NoError(t, reload())

	s := Current()
	assert.True(t, s.CandidateUsable(1, "pro"), "仅限套餐用在它绑定的分组上:可用")
	assert.False(t, s.CandidateUsable(1, "lab"), "仅限套餐用在别的分组上:跳过(不是余额不足)")
	assert.True(t, s.CandidateUsable(1, ""), "拿不到本次的模型分组时不改变上游行为")
	assert.True(t, s.CandidateUsable(2, "lab"), "没配范围 ≡ 通用")
	assert.True(t, s.CandidateUsable(99, "lab"), "没配过的套餐 ≡ 通用")
	assert.Equal(t, ScopeUniversal, s.BalanceScope(3),
		"脏值必须倒向 universal(与上游一致),而不是倒向「用户的余额突然用不了了」")
}

// TestBalanceScopeIsInertUntilSubscriptionSideWiresIt 守「配置」与「生效」是同一个判据。
//
// 生产接线现已完成(见 planentitlement.go 的 InstallHooks 与
// qianye/modules/planentitlement/balancescope_e2e_test.go 的端到端断言)。
// 本用例守的是**部分接线**这个中间状态 —— 有人把赋值那两行删了却留下握手,
// 或者反过来。它刻意不装 hook,因此断言的是握手本身的语义。
//
// 余额范围的判定住在本包,但让它真的生效的那一步在订阅侧(把 CandidateUsable
// 接到 model.QySubscriptionCandidateUsable 上)。两条线之间只靠"应该已经接上了"
// 的话,会出现最不能接受的一种状态:管理端配了「仅限 G」、用户端据此显示
// 「这笔余额在 H 上用不了」,而扣费路径根本没过滤,那笔钱照样从这张套餐里扣 ——
// **界面在骗人**,骗的正是"我的钱去哪了"这个问题。
//
// 所以没接线时判定恒为"可用",展示侧读的是同一个函数,两边同时说真话。
func TestBalanceScopeIsInertUntilSubscriptionSideWiresIt(t *testing.T) {
	gdb := newExtDB(t)
	setGroupRatios(t, `{"pro":0.8,"lab":0.1}`)
	putGrant(t, gdb, 1, "pro")
	putPolicy(t, gdb, 1, ScopeRestricted)
	require.NoError(t, reload())

	assert.True(t, CandidateUsable(1, "lab"),
		"订阅侧还没接线时,「仅限」只是一份存下来的配置,判定必须恒为可用")
	assert.False(t, BalanceScopeEnforced())
	assert.True(t, Health()["needs_attention"].(bool),
		"配了仅限却没生效必须进 needs_attention —— 否则这个状态从任何页面上都看不出来")

	enforceBalanceScope(t)
	assert.False(t, CandidateUsable(1, "lab"), "接线之后同一份配置立刻算数")
}
