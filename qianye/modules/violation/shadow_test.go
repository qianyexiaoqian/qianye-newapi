package violation

import (
	"context"
	"net/http/httptest"
	"testing"

	"github.com/QuantumNous/new-api/common"
	relaycommon "github.com/QuantumNous/new-api/relay/common"

	"github.com/gin-gonic/gin"
	"github.com/glebarez/sqlite"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

// shadow_test.go —— 影子模式的行为回归。
//
// 本轮之后这里锁的是三件事:
//
//  1. **影子命中不得推进违规计数**(裁决 2 的 P0,沿用)。
//  2. **模式只由规则的 Mode 决定**,未知取值一律落影子;熔断只在规则说 enforce
//     时才起作用,并且给出一个**可区分**的原因。
//  3. **影子记录必须带齐做分析所需的上下文**,包括"若真实执行会扣多少钱"
//     与"这一条为什么是影子"。

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

// TestShadowHitNeverTouchesTheBanCounter 是裁决 2 的核心回归。
//
// 勘察实测复现过的形状:阈值 3,两条影子命中把 hit_count 推到 2,
// 第三条**真实**命中读到 3 就落了一行 pending 封禁 —— 影子模式把用户封了。
func TestShadowHitNeverTouchesTheBanCounter(t *testing.T) {
	useTestConfig(t, "  enabled: true\n  auto_ban_threshold: 3\n")
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

// TestEffectiveShadowIsDecidedByRuleModeAlone 是本轮 A 的核心回归。
//
// 项目方拍板"模式绑在规则上,删掉全局开关"。这张表把新语义逐格钉死,并且刻意
// 覆盖两种未知取值 —— 滚动升级期间旧节点写下的行 mode 是空串,DBA 手工插的行
// 可能是任意字符串,而这两种行**绝不能**被当成真实执行。
func TestEffectiveShadowIsDecidedByRuleModeAlone(t *testing.T) {
	useTestConfig(t, "  enabled: true\n")

	cases := []struct {
		name       string
		mode       string
		breaker    bool
		want       bool
		wantReason string
	}{
		{"规则声明影子 = 影子", ModeShadow, false, true, ShadowReasonRuleMode},
		{"规则声明真实 = 真实执行", ModeEnforce, false, false, ""},
		{"空 mode 一律按影子(滚动升级/未迁移的行)", "", false, true, ShadowReasonRuleMode},
		{"无法识别的 mode 也按影子(手工 SQL 写坏)", "ENFORCE_", false, true, ShadowReasonRuleMode},
		{"熔断只钳住声明为真实的规则", ModeEnforce, true, true, ShadowReasonBreaker},
		{"熔断期间影子规则的原因仍是 rule_mode", ModeShadow, true, true, ShadowReasonRuleMode},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			isolateBreaker(t)
			if tc.breaker {
				// 直接用熔断的公开触发点,而不是手写 forcedShadowUntil:
				// 后者会让"tripShadow 忘了设 until"这种缺陷从这条测试底下溜过去。
				tripShadow(ShadowReasonBreaker)
			}
			on, reason := effectiveShadow(&compiledRule{R: Rule{Id: 1, Mode: tc.mode}})
			assert.Equal(t, tc.want, on)
			assert.Equal(t, tc.wantReason, reason)
		})
	}

	t.Run("规则为 nil 时按影子", func(t *testing.T) {
		isolateBreaker(t)
		on, reason := effectiveShadow(nil)
		assert.True(t, on)
		assert.Equal(t, ShadowReasonRuleMode, reason)
	})
}

// TestNewRecordFreezesShadowAnalysisContext 固化影子记录必须带上的分析上下文。
//
// 项目方给影子模式定的用途是「抓取涉嫌违规用户的日志、上下文,我要进行分析」。
// 这条测试就是那句话的可执行版本:逐项断言分析必需的字段都在记录上。
// 少了模型/分组/令牌/命中片段就无从判断,少了 counter_after 的哨兵值会被当成
// "计数确实是 0",少了 shadow_reason 就分不清"预期内的观察样本"与"熔断现场"。
func TestNewRecordFreezesShadowAnalysisContext(t *testing.T) {
	useTestConfig(t, "  enabled: true\n")
	gin.SetMode(gin.TestMode)

	build := func(shadow bool, reason string) *Record {
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
		return newRecord(captureRecordCtx(c, info), PhasePrompt, scanInput{}, v, shadow, reason, false)
	}

	shadowRec := build(true, ShadowReasonRuleMode)
	assert.Equal(t, CounterAfterShadow, shadowRec.CounterAfter)
	assert.Equal(t, "gpt-4o", shadowRec.ModelName)
	assert.Equal(t, "vip", shadowRec.UsingGroup)
	assert.Equal(t, 7, shadowRec.TokenId)
	assert.Equal(t, "sk-main", shadowRec.TokenName)
	assert.Equal(t, "badword", shadowRec.MatchedTerms)
	assert.Equal(t, "...badword...", shadowRec.MatchSnippet)
	assert.Equal(t, int64(9), shadowRec.RuleId)
	assert.Equal(t, "req-1", shadowRec.RequestId)
	assert.NotZero(t, shadowRec.CreatedAt)
	assert.Equal(t, ShadowReasonRuleMode, shadowRec.ShadowReason)

	live := build(false, ShadowReasonBreaker)
	assert.Equal(t, 0, live.CounterAfter,
		"真实记录的 counter_after 由 persistRecord 回写,建记录时必须是 0 而不是哨兵值")
	assert.Equal(t, "", live.ShadowReason,
		"shadow=false 却带着影子原因是自相矛盾的行,会污染按原因分组的统计")
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

// TestSnapshotCountsRulesByMode 固化「现在有几条规则在真实扣钱」这个数。
//
// 删掉全局开关之后它不再是一个布尔值,而管理端横幅完全靠它判断要不要提示
// 「整站还在观察期」。判据必须与 effectiveShadow 完全一致(`== ModeEnforce`):
// 写成 `!= ModeShadow` 的话,空 mode 的行会被算成真实执行,横幅就会声称
// 有规则在扣钱 —— 而那些行在热路径上其实是影子。
func TestSnapshotCountsRulesByMode(t *testing.T) {
	useTestConfig(t, "  enabled: true\n")
	gdb := newRuleDB(t) // 建库 + 接到 db.Get(),自带一条 mode 为默认值的规则
	// 自带的那一条走 GORM default,mode = shadow。再补三条覆盖其余取值。
	base := func(name, mode string) *Rule {
		return &Rule{
			Name: name, Enabled: true, Priority: 2, Mode: mode,
			Phase: PhasePrompt, MatchType: MatchKeyword, Pattern: "x",
			Action: ActionRecord, FeeMode: FeeNone, CountWeight: 1,
			CreatedAt: 1, UpdatedAt: 1,
		}
	}
	require.NoError(t, gdb.Create(base("真实-1", ModeEnforce)).Error)
	require.NoError(t, gdb.Create(base("真实-2", ModeEnforce)).Error)
	legacy := base("未迁移", ModeShadow)
	require.NoError(t, gdb.Create(legacy).Error)
	// mode 带 gorm default tag,Create 造不出空值,只能裸 SQL 改。
	require.NoError(t, gdb.Exec(
		"UPDATE qy_violation_rule SET mode = '' WHERE id = ?", legacy.Id).Error)

	require.NoError(t, reload(true))
	snap := Snapshot()
	assert.Equal(t, 2, snap.enforceRules, "只有显式 enforce 的两条算真实执行")
	assert.Equal(t, 2, snap.shadowRules,
		"默认那条 + 空 mode 那条都必须算进影子;空 mode 被算成真实,横幅就会谎报有规则在扣钱")
}
