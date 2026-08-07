package planentitlement

import (
	"testing"

	"github.com/QuantumNous/new-api/model"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// balancescope_e2e_test.go —— 「余额使用范围」从配置到真实扣费的端到端核实。
//
// ═══════════ 为什么这一组必须走真的 PreConsumeUserSubscription ═══════════
//
// 本模块自己的用例只能证明 CandidateUsable 这个**判定**是对的。判定对而接线断,
// 表现恰好是设计里最不能接受的那一种:管理端显示「已经限制住了」,用户端显示
// 「这笔余额在这里用不了」,而钱照样从那张套餐里扣走。那种状态下模块内的用例
// 全绿 —— 它们根本没有碰过扣费路径。
//
// 所以这一组直接调上游的 model.PreConsumeUserSubscription,断言的是**钱从哪里出**。

// installBalanceScopeWiring 装上生产环境 InstallHooks 装的那两个 hook。
//
// 与生产逐字相同的两行:测试里自己重写一份判定,证明的就只是"我写的那份判定
// 和我写的那份判定一致"。
func installBalanceScopeWiring(t *testing.T) {
	t.Helper()
	enforceBalanceScope(t)
	prevCandidate := model.QySubscriptionCandidateUsable
	prevOverflow := model.QyWalletOverflowAllowedDespiteStrict
	model.QySubscriptionCandidateUsable = CandidateUsable
	model.QyWalletOverflowAllowedDespiteStrict = WalletOverflowAllowedDespiteStrict
	t.Cleanup(func() {
		model.QySubscriptionCandidateUsable = prevCandidate
		model.QyWalletOverflowAllowedDespiteStrict = prevOverflow
	})
}

// disablePlanEntitlement 拉下本模块的 kill switch(plan_entitlement.enabled)。
func disablePlanEntitlement(t *testing.T) {
	t.Helper()
	cfg := *qyConfig.Load()
	cfg.PlanEntitlement.Enabled = falseP()
	prev := qyConfig.Swap(&cfg)
	t.Cleanup(func() { qyConfig.Store(prev) })
}

// TestRestrictedPlanIsSkippedNotSpentOnOtherGroups 是本轮 balance_scope 的主断言。
//
// 场景就是项目方那句话的字面意思:套餐 A「仅限 pro」,用户拿它去跑 lab。
// 断言两件事,缺一件这个功能都不算做完:
//
//	用在 pro 上   照常从这张套餐扣(不能把人挡在自己买的东西外面)
//	用在 lab 上   这张套餐被**跳过**,钱不从它这里出 —— 而它的余额一分没动
func TestRestrictedPlanIsSkippedNotSpentOnOtherGroups(t *testing.T) {
	gdb := newExtDB(t)
	mainDB := newMainDB(t)
	setGroupRatios(t, `{"pro":0.8,"lab":0.1}`)
	installBalanceScopeWiring(t)

	seedPlan(t, mainDB, 1, "仅限 pro 的套餐")
	sub := seedSubscription(t, mainDB, 11, 7, 1, 86400, 1000, 0)
	putGrant(t, gdb, 1, "pro")
	putPolicy(t, gdb, 1, ScopeRestricted)
	require.NoError(t, reload())

	got, err := model.PreConsumeUserSubscription("req-pro", 7, "gpt-4o", 0, 100, "pro")
	require.NoError(t, err, "在它绑定的模型分组上必须照常出资")
	assert.Equal(t, sub.Id, got.UserSubscriptionId)

	_, err = model.PreConsumeUserSubscription("req-lab", 7, "gpt-4o", 0, 100, "lab")
	require.Error(t, err, "「仅限 pro」的套餐不该为 lab 出资")

	var row model.UserSubscription
	require.NoError(t, mainDB.Where("id = ?", sub.Id).First(&row).Error)
	assert.EqualValues(t, 100, row.AmountUsed,
		"lab 那一笔一分钱都不该从这张套餐里扣 —— 只有 pro 那 100 是它出的")
}

// TestUniversalPlanKeepsUpstreamBehaviour 是上面那条的反面,也是「零行为变化」的证明。
//
// 没配范围 ≡ universal ≡ 上游今天的行为。这一条与上一条用同一份数据、只差一行配置,
// 因此它能把"过滤生效了"与"过滤把所有东西都挡住了"区分开。
func TestUniversalPlanKeepsUpstreamBehaviour(t *testing.T) {
	gdb := newExtDB(t)
	mainDB := newMainDB(t)
	setGroupRatios(t, `{"pro":0.8,"lab":0.1}`)
	installBalanceScopeWiring(t)

	seedPlan(t, mainDB, 1, "通用套餐")
	sub := seedSubscription(t, mainDB, 11, 7, 1, 86400, 1000, 0)
	putGrant(t, gdb, 1, "pro") // 绑定了分组,但**没有**把范围设成仅限
	require.NoError(t, reload())

	got, err := model.PreConsumeUserSubscription("req-lab", 7, "gpt-4o", 0, 100, "lab")
	require.NoError(t, err, "通用余额可以用在任何模型分组上 —— 这是上游行为,不该被改动")
	assert.Equal(t, sub.Id, got.UserSubscriptionId)
}

// TestRestrictedPlanFallsThroughToTheNextCandidate 守「跳过」与「余额不足」在
// 候选循环里的行为一致性。
//
// 被范围跳过的套餐必须像余额不足一样顺延到下一个候选,而不是让整次扣费失败。
// 顺延对象刻意选**更晚到期**的那一张:错误实现若把跳过写成 break,这条会红。
func TestRestrictedPlanFallsThroughToTheNextCandidate(t *testing.T) {
	gdb := newExtDB(t)
	mainDB := newMainDB(t)
	setGroupRatios(t, `{"pro":0.8,"lab":0.1}`)
	installBalanceScopeWiring(t)

	seedPlan(t, mainDB, 1, "仅限 pro")
	seedPlan(t, mainDB, 2, "通用")
	seedSubscription(t, mainDB, 11, 7, 1, 86400, 1000, 0)                 // 先到期,但仅限 pro
	universal := seedSubscription(t, mainDB, 12, 7, 2, 86400*30, 1000, 0) // 后到期,通用
	putGrant(t, gdb, 1, "pro")
	putPolicy(t, gdb, 1, ScopeRestricted)
	require.NoError(t, reload())

	got, err := model.PreConsumeUserSubscription("req-lab", 7, "gpt-4o", 0, 100, "lab")
	require.NoError(t, err)
	assert.Equal(t, universal.Id, got.UserSubscriptionId,
		"被范围跳过之后必须顺延到下一个候选,而不是让整次扣费失败")
}

// TestSkippedPlanCannotBlockWalletFallback 守那个「谁都没有配出来的死锁」。
//
// 一张「仅限 pro + 不许钱包回退」的套餐,在用户请求 lab 时根本不是出资候选。
// 它若仍然投票禁止钱包回退,用户在 lab 上就既扣不到套餐余额、也不许用钱包。
// 判据:**只有本次真的能出资的套餐才有资格禁止钱包回退**。
func TestSkippedPlanCannotBlockWalletFallback(t *testing.T) {
	gdb := newExtDB(t)
	mainDB := newMainDB(t)
	setGroupRatios(t, `{"pro":0.8,"lab":0.1}`)
	installBalanceScopeWiring(t)

	seedPlan(t, mainDB, 1, "仅限 pro,不许回退")
	sub := seedSubscription(t, mainDB, 11, 7, 1, 86400, 1000, 0)
	require.NoError(t, mainDB.Model(&model.UserSubscription{}).
		Where("id = ?", sub.Id).Update("allow_wallet_overflow", false).Error)
	putGrant(t, gdb, 1, "pro")
	putPolicy(t, gdb, 1, ScopeRestricted)
	require.NoError(t, reload())

	allow, err := model.UserActiveSubscriptionsAllowWalletOverflow(7, "lab")
	require.NoError(t, err)
	assert.True(t, allow,
		"这张套餐在 lab 上根本不是候选,它无权禁止钱包回退 —— "+
			"否则用户在 lab 上既扣不到套餐余额、也不许用钱包")

	allow, err = model.UserActiveSubscriptionsAllowWalletOverflow(7, "pro")
	require.NoError(t, err)
	assert.False(t, allow,
		"在它自己绑定的分组上它**是**候选,上游那条「不许回退」必须照旧生效")
}

// TestKillSwitchStopsBalanceScopeImmediately 守 plan_entitlement.enabled 是真的开关。
//
// 一个要等缓存过期才生效的 kill switch,在出事的那一刻正好来不及。
func TestKillSwitchStopsBalanceScopeImmediately(t *testing.T) {
	gdb := newExtDB(t)
	mainDB := newMainDB(t)
	setGroupRatios(t, `{"pro":0.8,"lab":0.1}`)
	installBalanceScopeWiring(t)

	seedPlan(t, mainDB, 1, "仅限 pro")
	sub := seedSubscription(t, mainDB, 11, 7, 1, 86400, 1000, 0)
	putGrant(t, gdb, 1, "pro")
	putPolicy(t, gdb, 1, ScopeRestricted)
	require.NoError(t, reload())
	require.False(t, CandidateUsable(1, "lab"))

	disablePlanEntitlement(t)
	assert.True(t, CandidateUsable(1, "lab"),
		"关掉 plan_entitlement.enabled 之后,余额过滤必须立刻停止生效,不等快照过期")

	got, err := model.PreConsumeUserSubscription("req-lab", 7, "gpt-4o", 0, 100, "lab")
	require.NoError(t, err)
	assert.Equal(t, sub.Id, got.UserSubscriptionId)
}
