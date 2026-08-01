package violation

import (
	"context"
	"net/http/httptest"
	"testing"

	"github.com/QuantumNous/new-api/common"
	qymodel "github.com/QuantumNous/new-api/qianye/model"
	relaycommon "github.com/QuantumNous/new-api/relay/common"

	"github.com/gin-gonic/gin"
	"github.com/glebarez/sqlite"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

// shadow_test.go —— 影子模式的行为回归。
//
// 这里锁的是两件事,它们此前都是坏的:
//
//  1. **影子命中不得推进违规计数**(裁决 2 的 P0)。旧实现只跳过了"执行封号"
//     那一步,bumpCounter 照常调用;而封号判据 reachedThreshold(after, threshold)
//     完全由持久化的 hit_count 推导,于是影子命中把用户推过封号线,
//     下一次**真实**命中直接落封禁行,再由 runBanCompensate 执行封号。
//  2. **全局影子开关必须能被管理端改**。旧实现里它只存在于 YAML、默认 true,
//     而叠加语义取更保守者胜 —— 全局为真时规则级 dry_run 怎么调都没用,
//     这就是需求原文说的「违规规则无法调整模式」。

// newPersistDB 建一个只承载 qy_violation_record / qy_violation_payload 的内存库。
//
// **刻意不建 qy_violation_counter。** bumpCounter 用的是 MySQL 方言的
// `INSERT ... ON DUPLICATE KEY UPDATE`,SQLite 连语法都过不了,所以"计数变成几"
// 在这里根本观察不到。于是把"计数器不可写"当成探针:persistRecord 只要走到
// bumpCounter 就必然带着错误返回,而绕开它就返回 nil。
// 这样一来两个方向都被钉死 —— 影子必须 nil、真实必须报错,
// 把 persistRecord 里的 `shadow ||` 删掉,第一条立刻变红。
func newPersistDB(t *testing.T) *gorm.DB {
	t.Helper()
	gdb, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	require.NoError(t, err)
	sqlDB, err := gdb.DB()
	require.NoError(t, err)
	sqlDB.SetMaxOpenConns(1) // 内存库按连接隔离
	require.NoError(t, gdb.AutoMigrate(&Record{}, &Payload{}))
	t.Cleanup(func() { _ = sqlDB.Close() })
	return gdb
}

// newSettingsDB 建一个只承载 qy_settings 的内存库。
func newSettingsDB(t *testing.T) *gorm.DB {
	t.Helper()
	gdb, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	require.NoError(t, err)
	sqlDB, err := gdb.DB()
	require.NoError(t, err)
	sqlDB.SetMaxOpenConns(1)
	require.NoError(t, gdb.AutoMigrate(&qymodel.Setting{}))
	t.Cleanup(func() { _ = sqlDB.Close() })
	return gdb
}

func hitRecord(recNo string, userId int, shadow bool, weight int) *Record {
	counterAfter := 0
	if shadow {
		counterAfter = CounterAfterShadow
	}
	return &Record{
		RecNo: recNo, UserId: userId, RuleId: 1, Phase: PhasePrompt,
		Action: ActionRecord, Shadow: shadow, CountWeight: weight,
		CounterAfter: counterAfter, Status: RecordActive, FeeStatus: FeeStatusNone,
		CreatedAt: common.GetTimestamp(),
	}
}

// TestShadowHitNeverTouchesTheBanCounter 是本轮 P0 的核心回归。
//
// 勘察实测复现过的形状:阈值 3,两条 dry_run 命中把 hit_count 推到 2,
// 第三条**真实**命中读到 3 就落了一行 pending 封禁 —— 影子模式把用户封了。
func TestShadowHitNeverTouchesTheBanCounter(t *testing.T) {
	useTestConfig(t, "  enabled: true\n  shadow_mode: false\n  auto_ban_threshold: 3\n")
	ctx := context.Background()

	t.Run("影子命中止步于落记录,一个字节都不写计数器", func(t *testing.T) {
		gdb := newPersistDB(t)
		rec := hitRecord("vr_shadow_1", 42, true, 3)

		require.NoError(t, persistRecord(ctx, gdb, rec, nil, rec.CountWeight, true),
			"影子命中不得触及计数器;报错说明它仍然走到了 bumpCounter")

		var row Record
		require.NoError(t, gdb.Where("rec_no = ?", "vr_shadow_1").Take(&row).Error)
		assert.False(t, row.Counted, "影子记录的 counted 必须为 false")
		assert.Equal(t, CounterAfterShadow, row.CounterAfter,
			"影子记录的 counter_after 没有真实答案,必须是哨兵值而不是 0")
		assert.Equal(t, 3, row.CountWeight,
			"count_weight 仍要留着:它回答「若真实执行会给计数加几」")
	})

	t.Run("真实命中仍然会去推进计数器", func(t *testing.T) {
		gdb := newPersistDB(t)
		rec := hitRecord("vr_real_1", 42, false, 3)

		// 反向断言:计数器表不存在,所以"报错"恰恰证明 bumpCounter 被调用了。
		// 没有这一条,上一个用例可以靠"persistRecord 什么都不做"骗过去。
		assert.Error(t, persistRecord(ctx, gdb, rec, nil, rec.CountWeight, false),
			"真实命中必须推进计数器")
	})

	t.Run("权重为 0 的规则本来就不计数", func(t *testing.T) {
		gdb := newPersistDB(t)
		rec := hitRecord("vr_zero_1", 42, false, 0)

		require.NoError(t, persistRecord(ctx, gdb, rec, nil, 0, false))
		var row Record
		require.NoError(t, gdb.Where("rec_no = ?", "vr_zero_1").Take(&row).Error)
		assert.False(t, row.Counted)
	})
}

// TestNewRecordFreezesShadowContext 固化影子记录必须带上的核查上下文。
//
// 影子模式的全部价值就是让管理员在切真实模式之前回答"这些命中如果真执行会怎样"。
// 少了模型/分组/令牌/命中片段就无从判断,少了 counter_after 的哨兵值就会被当成
// "计数确实是 0"。
func TestNewRecordFreezesShadowContext(t *testing.T) {
	useTestConfig(t, "  enabled: true\n  shadow_mode: true\n")
	gin.SetMode(gin.TestMode)

	build := func(shadow bool) *Record {
		c, _ := gin.CreateTestContext(httptest.NewRecorder())
		c.Request = httptest.NewRequest("POST", "/v1/chat/completions", nil)
		c.Set("username", "alice")
		c.Set("token_name", "sk-main")
		info := &relaycommon.RelayInfo{
			UserId: 42, TokenId: 7, OriginModelName: "gpt-4o", UsingGroup: "vip",
			RequestId: "req-1",
		}
		v := &verdict{
			Rule:    &compiledRule{R: Rule{Id: 9, Name: "csam_v3", PublicReason: "内容违规"}},
			Terms:   []string{"badword"},
			Snippet: "...badword...",
		}
		return newRecord(c, info, PhasePrompt, scanInput{}, v, shadow, false)
	}

	shadowRec := build(true)
	assert.Equal(t, CounterAfterShadow, shadowRec.CounterAfter)
	assert.Equal(t, "gpt-4o", shadowRec.ModelName)
	assert.Equal(t, "vip", shadowRec.UsingGroup)
	assert.Equal(t, 7, shadowRec.TokenId)
	assert.Equal(t, "sk-main", shadowRec.TokenName)
	assert.Equal(t, "badword", shadowRec.MatchedTerms)
	assert.Equal(t, int64(9), shadowRec.RuleId)
	assert.NotZero(t, shadowRec.CreatedAt)

	assert.Equal(t, 0, build(false).CounterAfter,
		"真实记录的 counter_after 由 persistRecord 回写,建记录时必须是 0 而不是哨兵值")
}

// TestGlobalShadowModeIsAdminAdjustable 是「无法调整模式」的回归。
//
// 关键的一条是第二个子用例:YAML 写着 shadow_mode: true(而且它的默认值也是
// true),管理端把覆盖写成 0 之后必须真的退出影子模式。旧实现里
// shadowActive() 见到配置为真就无条件返回 shadow,规则级与管理端都无从覆盖。
func TestGlobalShadowModeIsAdminAdjustable(t *testing.T) {
	ctx := context.Background()

	t.Run("没有覆盖时回落 YAML", func(t *testing.T) {
		useTestConfig(t, "  enabled: true\n  shadow_mode: true\n")
		isolateBreaker(t)
		gdb := newSettingsDB(t)

		require.NoError(t, refreshModeWith(ctx, gdb))
		on, reason := shadowActive()
		assert.True(t, on)
		assert.Equal(t, shadowSourceConfig, reason)
		assert.Equal(t, "unset", overrideName())
	})

	t.Run("覆盖为真实执行时不再回落 YAML", func(t *testing.T) {
		useTestConfig(t, "  enabled: true\n  shadow_mode: true\n")
		isolateBreaker(t)
		gdb := newSettingsDB(t)

		require.NoError(t, writeShadowSetting(ctx, gdb, false, 1))
		invalidateMode()
		require.NoError(t, refreshModeWith(ctx, gdb))

		on, _ := shadowActive()
		assert.False(t, on, "管理端关掉影子模式后必须真的退出,否则规则模式永远调不动")
		assert.Equal(t, "off", overrideName())
	})

	t.Run("覆盖为影子时压过 YAML 的真实执行", func(t *testing.T) {
		useTestConfig(t, "  enabled: true\n  shadow_mode: false\n")
		isolateBreaker(t)
		gdb := newSettingsDB(t)

		require.NoError(t, writeShadowSetting(ctx, gdb, true, 1))
		invalidateMode()
		require.NoError(t, refreshModeWith(ctx, gdb))

		on, reason := shadowActive()
		assert.True(t, on)
		assert.Equal(t, shadowSourceSettings, reason)
	})

	t.Run("清掉覆盖后重新跟随 YAML", func(t *testing.T) {
		useTestConfig(t, "  enabled: true\n  shadow_mode: false\n")
		isolateBreaker(t)
		gdb := newSettingsDB(t)

		require.NoError(t, writeShadowSetting(ctx, gdb, true, 1))
		invalidateMode()
		require.NoError(t, refreshModeWith(ctx, gdb))
		require.True(t, func() bool { on, _ := shadowActive(); return on }())

		require.NoError(t, dropShadowSetting(ctx, gdb))
		invalidateMode()
		require.NoError(t, refreshModeWith(ctx, gdb))

		on, _ := shadowActive()
		assert.False(t, on)
		assert.Equal(t, "unset", overrideName())
	})

	t.Run("被手工改坏的取值一律按影子处理", func(t *testing.T) {
		useTestConfig(t, "  enabled: true\n  shadow_mode: false\n")
		isolateBreaker(t)
		gdb := newSettingsDB(t)

		require.NoError(t, gdb.Create(&qymodel.Setting{
			Scope: settingScope, K: keyShadowMode, V: "yes-please",
			UpdatedAt: common.GetTimestamp(),
		}).Error)
		invalidateMode()
		require.NoError(t, refreshModeWith(ctx, gdb))

		on, _ := shadowActive()
		assert.True(t, on,
			"没人知道现在该是什么模式时,唯一不会造成不可逆损失的选择是不扣费不封号")
	})

	t.Run("在途刷新不得盖掉刚提交的切换", func(t *testing.T) {
		useTestConfig(t, "  enabled: true\n  shadow_mode: true\n")
		isolateBreaker(t)
		gdb := newSettingsDB(t)

		// 模拟一次已经读完库、还没写回缓存的旧刷新。
		staleEpoch := modeEpoch.Load()

		require.NoError(t, writeShadowSetting(ctx, gdb, false, 1))
		invalidateMode()
		require.NoError(t, refreshModeWith(ctx, gdb))
		require.Equal(t, "off", overrideName())

		// 旧刷新此刻才写回:它读到的是切换之前的"没有覆盖",必须被丢弃。
		storeMode(staleEpoch, shadowUnset)
		assert.Equal(t, "off", overrideName(),
			"在途的旧快照把新值盖掉的话,此后一个刷新周期内全站仍按旧模式跑")
	})
}

// TestEffectiveShadowSuperposition 固化叠加语义:取更保守者胜。
//
// 两条职责必须各自独立成立:全局开关是上线安全阀与熔断自愈(要能一票否决所有
// 规则),规则级 dry_run 是单条规则的灰度(全局放开时仍要拦住自己那一条)。
func TestEffectiveShadowSuperposition(t *testing.T) {
	ctx := context.Background()
	cases := []struct {
		name         string
		globalShadow bool
		dryRun       bool
		want         bool
		wantReason   string
	}{
		{"全局真实 + 规则真实 = 真实执行", false, false, false, ""},
		{"全局真实 + 规则灰度 = 只有这条规则影子", false, true, true, "rule_dry_run"},
		{"全局影子 + 规则真实 = 全局一票否决", true, false, true, shadowSourceSettings},
		{"全局影子 + 规则灰度 = 影子", true, true, true, shadowSourceSettings},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			useTestConfig(t, "  enabled: true\n  shadow_mode: false\n")
			isolateBreaker(t)
			gdb := newSettingsDB(t)
			require.NoError(t, writeShadowSetting(ctx, gdb, tc.globalShadow, 1))
			invalidateMode()
			require.NoError(t, refreshModeWith(ctx, gdb))

			on, reason := effectiveShadow(&compiledRule{R: Rule{Id: 1, DryRun: tc.dryRun}})
			assert.Equal(t, tc.want, on)
			assert.Equal(t, tc.wantReason, reason)
		})
	}
}

// TestResetUserCounterClearsOnlyTheBanDriver 固化历史脏数据的补救出口。
//
// 现网的计数器里已经混进了影子命中(修复只能保证从此不再混入),
// 而历史行无法分辨哪几次来自影子。清零动作因此必须存在,但只能动
// hit_count —— 那是自动封号判据的唯一输入;total_count 是终身展示值,
// ban_cycle 是封禁认领的互斥键,回退它会让该用户的自动封号永久静默失效。
func TestResetUserCounterClearsOnlyTheBanDriver(t *testing.T) {
	gdb, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	require.NoError(t, err)
	sqlDB, err := gdb.DB()
	require.NoError(t, err)
	sqlDB.SetMaxOpenConns(1)
	t.Cleanup(func() { _ = sqlDB.Close() })
	require.NoError(t, gdb.AutoMigrate(&Counter{}))

	require.NoError(t, gdb.Create(&Counter{
		UserId: 42, WindowStart: 1000, HitCount: 7, TotalCount: 31, BanCycle: 2,
		LastHitAt: 1500, UpdatedAt: 1500,
	}).Error)

	before, reset, err := resetUserCounter(context.Background(), gdb, 42)
	require.NoError(t, err)
	assert.True(t, reset)
	assert.Equal(t, 7, before.HitCount, "审计要靠这个返回值记录清零前的状态")

	var row Counter
	require.NoError(t, gdb.Where("user_id = ?", 42).Take(&row).Error)
	assert.Equal(t, 0, row.HitCount)
	assert.EqualValues(t, 31, row.TotalCount, "终身累计不得被清掉")
	assert.Equal(t, 2, row.BanCycle, "封禁周期回退会让自动封号对该用户永久失效")
	assert.Greater(t, row.WindowStart, int64(1000), "窗口必须重新起算")

	t.Run("没有计数行时不算失败", func(t *testing.T) {
		_, reset, err := resetUserCounter(context.Background(), gdb, 99)
		require.NoError(t, err)
		assert.False(t, reset)
	})
}
