package commission

import (
	"context"
	"strconv"
	"sync"
	"testing"

	"github.com/QuantumNous/new-api/common"
	qymodel "github.com/QuantumNous/new-api/qianye/model"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

// 本文件守 F4:配置读取不得持锁查库,且必须接调用方的 ctx。
//
// 为什么不能只测"读出来的值对不对":那种测试在缺陷存在时照样全绿 ——
// 缺陷不在算出来的配置里,而在**这条 SELECT 是在什么状态下发出的**。
// 持锁查库时,结算 worker 撞上一次行锁等待就会把用户端"我的推广"、管理端
// 健康面板和另一个 worker 全部串在 settingsMu 上;没接 ctx 时,那条
// SELECT 会一直等到 DSN 的 readTimeout(默认 30 秒)。
// 因此下面两类断言都从"运行时状态"入手:一类在查询进行中反向探测互斥锁,
// 一类用已取消的 ctx 逼数据库层表态。

// lockProbe 记录"某条 SQL 执行期间,某把互斥锁是不是被同一个协程占着"。
//
// TryLock 在持锁协程自己调用时返回 false —— 正是这个性质让它能在回调里
// 分辨出"查库这一步在不在临界区内"。
type lockProbe struct {
	fired bool
	busy  bool
}

// probeDuringQuery 在每条打到 table 的查询执行期间探一次 tryLock。
func probeDuringQuery(t *testing.T, gdb *gorm.DB, table string, tryLock func() bool, unlock func()) *lockProbe {
	t.Helper()
	p := &lockProbe{}
	const name = "test:probe_lock_during_query"
	require.NoError(t, gdb.Callback().Query().Before("gorm:query").Register(name, func(tx *gorm.DB) {
		if tx.Statement == nil || tx.Statement.Table != table {
			return
		}
		p.fired = true
		if tryLock() {
			unlock()
			return
		}
		p.busy = true
	}))
	t.Cleanup(func() { _ = gdb.Callback().Query().Remove(name) })
	return p
}

// seedBlockedRelation 拉黑一条邀请关系并让缓存立即失效。
func seedBlockedRelation(t *testing.T, gdb *gorm.DB, inviteeId int) {
	t.Helper()
	now := common.GetTimestamp()
	require.NoError(t, gdb.Create(&InviteRelation{
		InviteeId:  inviteeId,
		InviterId:  42,
		InviteeRef: "ref-" + strconv.Itoa(inviteeId),
		Blocked:    true,
		CreatedAt:  now,
		UpdatedAt:  now,
	}).Error)
	invalidateBlocked()
}

// resetDegrade 清掉一个降级计数器,避免测试之间互相渗漏。
func resetDegrade(d *degradeRecord) {
	d.mu.Lock()
	d.count, d.lastAt, d.lastWarn, d.reason = 0, 0, 0, ""
	d.mu.Unlock()
}

// TestEffectiveQueriesOutsideSettingsLock 锁定"查 qy_settings 时 settingsMu 是空闲的"。
//
// 把 effectiveCtx 里的查库那一步挪回临界区内(或换回 Lock + defer Unlock 的写法),
// 这条立刻变红。
func TestEffectiveQueriesOutsideSettingsLock(t *testing.T) {
	gdb := newTestDB(t)
	useConfig(t, commissionRateConfig("10", "5"))

	probe := probeDuringQuery(t, gdb, "qy_settings", settingsMu.TryLock, settingsMu.Unlock)

	invalidateSettings()
	require.Equal(t, 500, effective().ConsumeRateUnits, "前提:本次确实回库读了一遍运营覆盖")
	require.True(t, probe.fired, "前提:探针挂上了 —— 没触发说明这条测试什么都没验")
	assert.False(t, probe.busy,
		"查 qy_settings 期间 settingsMu 被占着:一条慢 SELECT 会把结算 worker、"+
			"用户端推广页和健康面板全部串在这把锁上")
}

// TestRefSaltQueriesOutsideItsLock 是同一条性质在 refSalt 上的版本。
//
// refSalt 一个进程只查一次库,持锁查库因此更隐蔽 —— 但首次调用撞上慢查询时,
// 所有计佣写入协程会一起钉死在 saltOnce 上。
func TestRefSaltQueriesOutsideItsLock(t *testing.T) {
	gdb := newTestDB(t)
	useConfig(t, commissionRateConfig("10", "5"))

	probe := probeDuringQuery(t, gdb, "qy_settings", saltOnce.TryLock, saltOnce.Unlock)

	require.NotEmpty(t, refSalt(), "前提:盐生成成功,确实走了查库那条路")
	require.True(t, probe.fired, "前提:探针挂上了")
	assert.False(t, probe.busy, "查库/首次生成盐期间 saltOnce 必须是空闲的")
}

// TestLoadOverridesHonorsContext 锁定"运营配置查询接调用方预算"。
//
// 去掉 loadOverrides 里的 WithContext(ctx),查询会照常成功返回,这条即红。
// 用已取消的 ctx 而不是超时,是为了让断言不依赖任何计时。
func TestLoadOverridesHonorsContext(t *testing.T) {
	gdb := newTestDB(t)
	useConfig(t, commissionRateConfig("10", "5"))
	setSettingOverride(t, gdb, keyConsumeRatePercent, "8.25")
	resetDegrade(settingsDegrade)
	t.Cleanup(func() { resetDegrade(settingsDegrade) })

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	_, err := loadOverrides(ctx)
	require.Error(t, err, "ctx 已取消,查询必须失败 —— 成功说明它根本没拿到调用方的 ctx")
	assert.ErrorIs(t, err, context.Canceled)

	// 读不到覆盖时必须回落 YAML 默认继续计佣(停摆比少一个微调糟得多),
	// 但这次降级必须被计数:降级算出来的佣金与正常佣金在流水上无从区分。
	got := effectiveCtx(ctx)
	assert.Equal(t, 500, got.ConsumeRateUnits, "读不到覆盖必须回落 YAML 的 5%,而不是 0")
	stats := settingsDegrade.stats()
	assert.EqualValues(t, 1, stats["count"], "静默降级必须留痕,否则事后无法复核那批佣金")
	assert.Positive(t, stats["last_at"])
	assert.Contains(t, stats["last_reason"], "读取运营配置失败")
}

// invalidateDuringQuery 在打到 table 的**第一条**查询返回之后,模拟"管理端刚好
// 在这一刻提交了改动并失效了缓存"。
//
// 查库移出临界区之后,SELECT 返回与写回缓存之间就有了一个真实窗口;这个回调
// 把那个窗口固定成一个可复现的交错,而不是靠并发碰运气。回调只触发一次,
// 后续的重新加载才能读到新值。
func invalidateDuringQuery(t *testing.T, gdb *gorm.DB, table string, commit func()) {
	t.Helper()
	const name = "test:invalidate_during_query"
	var once sync.Once
	require.NoError(t, gdb.Callback().Query().After("gorm:query").Register(name, func(tx *gorm.DB) {
		if tx.Statement == nil || tx.Statement.Table != table {
			return
		}
		once.Do(commit)
	}))
	t.Cleanup(func() { _ = gdb.Callback().Query().Remove(name) })
}

// TestSettingsInvalidationSurvivesInFlightLoad 是 M8 的回归。
//
// 场景:worker 的 SELECT 读到旧的 8% → 管理员把费率改成 3%、事务提交、
// 调 invalidateSettings() → worker 回来把 8% 无条件写回缓存并盖上新时间戳。
// 此后 60 秒全按已经作废的费率计佣,而 RateUnits 会被冻结进 accrual 行,
// 与合法行长得一模一样、事后无法区分。
func TestSettingsInvalidationSurvivesInFlightLoad(t *testing.T) {
	gdb := newTestDB(t)
	useConfig(t, commissionRateConfig("10", "5"))
	setSettingOverride(t, gdb, keyConsumeRatePercent, "8")

	invalidateDuringQuery(t, gdb, "qy_settings", func() {
		require.NoError(t, gdb.Model(&qymodel.Setting{}).
			Where("scope = ? AND k = ?", settingScope, keyConsumeRatePercent).
			Update("v", "3").Error)
		invalidateSettings()
	})

	ctx := context.Background()
	require.Equal(t, 800, effectiveCtx(ctx).ConsumeRateUnits,
		"前提:本次读到的确实是在途的旧快照")
	assert.Equal(t, 300, effectiveCtx(ctx).ConsumeRateUnits,
		"管理端已经把费率改成 3% 并失效了缓存,在途的旧快照不得把它按回去")
}

// TestGroupRatesQueriesOutsideItsLock 是 F4 那条性质在分组费率上的版本。
func TestGroupRatesQueriesOutsideItsLock(t *testing.T) {
	gdb := newTestDB(t)
	useConfig(t, commissionRateConfig("10", "5"))
	seedGroupRate(t, gdb, "vip", "12.5", "8.25", true)

	probe := probeDuringQuery(t, gdb, "qy_commission_group_rate", groupRateMu.TryLock, groupRateMu.Unlock)

	invalidateGroupRates()
	require.Contains(t, groupRates(context.Background()), "vip", "前提:本次确实回库读了一遍规则")
	require.True(t, probe.fired, "前提:探针挂上了 —— 没触发说明这条测试什么都没验")
	assert.False(t, probe.busy,
		"查分组费率期间 groupRateMu 被占着:一条慢 SELECT 会把所有计佣协程串在这把锁上")
}

// TestGroupRateInvalidationSurvivesInFlightLoad 是 groupRates 的代次校验。
func TestGroupRateInvalidationSurvivesInFlightLoad(t *testing.T) {
	gdb := newTestDB(t)
	useConfig(t, commissionRateConfig("10", "5"))
	seedGroupRate(t, gdb, "vip", "12.5", "8.25", true)

	invalidateDuringQuery(t, gdb, "qy_commission_group_rate", func() {
		seedGroupRateUpdate(t, gdb, "vip", "1", "1") // 内含 invalidateGroupRates
	})

	ctx := context.Background()
	require.Equal(t, 825, groupRates(ctx)["vip"].ConsumeRateUnits, "前提:本次读到的是在途旧快照")
	assert.Equal(t, 100, groupRates(ctx)["vip"].ConsumeRateUnits,
		"运营已经把 vip 改成 1% 并失效了缓存,在途快照不得把 8.25% 按回去再冻进账本")
}

// TestBlockedInviteesQueriesOutsideItsLock 是同一条性质在拉黑集合上的版本。
func TestBlockedInviteesQueriesOutsideItsLock(t *testing.T) {
	gdb := newTestDB(t)
	seedBlockedRelation(t, gdb, 901)

	probe := probeDuringQuery(t, gdb, "qy_invite_relation", blockedMu.TryLock, blockedMu.Unlock)

	invalidateBlocked()
	require.True(t, blockedInvitees(context.Background())[901], "前提:本次确实回库读了一遍")
	require.True(t, probe.fired, "前提:探针挂上了")
	assert.False(t, probe.busy,
		"查拉黑集合期间 blockedMu 被占着:计佣写入协程会全部串在这把锁上")
}

// TestBlockedInvalidationSurvivesInFlightLoad 是 blockedInvitees 的代次校验。
//
// 管理员拉黑一个正在刷单的下线之后,在途的旧快照把"没人被拉黑"写回缓存,
// 接下来 60 秒仍然照常给他计佣 —— 而拉黑本来就是为了立刻止血。
func TestBlockedInvalidationSurvivesInFlightLoad(t *testing.T) {
	gdb := newTestDB(t)

	invalidateDuringQuery(t, gdb, "qy_invite_relation", func() {
		seedBlockedRelation(t, gdb, 901) // 内含 invalidateBlocked
	})

	ctx := context.Background()
	require.False(t, blockedInvitees(ctx)[901], "前提:本次读到的是在途旧快照(还没有人被拉黑)")
	assert.True(t, blockedInvitees(ctx)[901],
		"管理员已经拉黑并失效了缓存,在途快照不得把空集合按回去")
}

// TestGroupRatesNotesDegradeWhenQueryFails 是 M14 的驱动式回归。
//
// 原有的 TestAdminHealth_ExposesDegradeCounters 是在测试里自己调 note() 再断言
// 自己刚写进去的值,从不驱动 groupRates() —— 把两处 note() 整段删掉它照样全绿。
// 这条从真实的失败路径驱动:没有旧快照可沿用时返回空表,那一刻起所有分组
// 一律按全局默认费率计佣,而结果会冻结进账本行。
func TestGroupRatesNotesDegradeWhenQueryFails(t *testing.T) {
	gdb := newTestDB(t)
	useConfig(t, commissionRateConfig("10", "5"))
	seedGroupRate(t, gdb, "vip", "12.5", "8.25", true)
	resetDegrade(groupRateDegrade)
	t.Cleanup(func() { resetDegrade(groupRateDegrade) })

	invalidateGroupRates()
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	assert.Empty(t, groupRates(ctx), "读不到规则必须回落空表继续计佣,而不是停摆")
	stats := groupRateDegrade.stats()
	assert.EqualValues(t, 1, stats["count"], "静默降级必须留痕,否则事后无从复核那批佣金")
	assert.Positive(t, stats["last_at"])
	assert.Contains(t, stats["last_reason"], "读取分组费率失败")

	// 反向约束:降级不得把空表写进缓存,否则故障恢复后 60 秒内所有分组
	// 仍然按全局默认费率计佣。
	assert.Equal(t, 825, groupRates(context.Background())["vip"].ConsumeRateUnits,
		"下一次调用必须重新回库,而不是复用降级时的空表")
}
