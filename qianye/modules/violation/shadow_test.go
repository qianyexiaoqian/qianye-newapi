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

// newPersistDB 建一个承载 qy_violation_record / qy_violation_payload /
// qy_violation_counter 的内存库。
//
// 计数器表以前**刻意不建**:bumpCounter 曾经写的是 MySQL 专有的
// `INSERT ... ON DUPLICATE KEY UPDATE` + `IF()`,SQLite 连语法都过不了,
// 于是只能把"计数器不可写"当探针 —— 影子必须 nil、真实必须报错。
// 那种探针只能证明"走到了那一步",证明不了"加对了几"。
// bumpCounter 改成三方言通用写法之后,计数值可以直接观察,探针换成真值断言。
func newPersistDB(t *testing.T) *gorm.DB {
	t.Helper()
	gdb, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	require.NoError(t, err)
	sqlDB, err := gdb.DB()
	require.NoError(t, err)
	sqlDB.SetMaxOpenConns(1) // 内存库按连接隔离
	require.NoError(t, gdb.AutoMigrate(&Record{}, &Payload{}, &Counter{}))
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

		require.NoError(t, persistRecord(ctx, gdb, rec, nil, rec.CountWeight, true))

		var n int64
		require.NoError(t, gdb.Model(&Counter{}).Where("user_id = ?", 42).Count(&n).Error)
		assert.Zero(t, n, "影子命中不得在计数器表里留下任何一行")

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

		// 正向断言:计数必须真的加到 3(= 规则的 count_weight)。
		// 没有这一条,上一个用例可以靠"persistRecord 什么都不做"骗过去。
		require.NoError(t, persistRecord(ctx, gdb, rec, nil, rec.CountWeight, false))

		var c Counter
		require.NoError(t, gdb.Where("user_id = ?", 42).Take(&c).Error,
			"真实命中必须推进计数器")
		assert.Equal(t, 3, c.HitCount, "权重 3 的一次真实命中把 hit_count 从 0 推到 3")
		assert.EqualValues(t, 3, c.TotalCount)

		var row Record
		require.NoError(t, gdb.Where("rec_no = ?", "vr_real_1").Take(&row).Error)
		assert.True(t, row.Counted, "真实命中必须标记为已计数")
		assert.Equal(t, 3, row.CounterAfter, "counter_after 必须是推进后的真值,不是哨兵")
	})

	t.Run("权重为 0 的规则本来就不计数", func(t *testing.T) {
		gdb := newPersistDB(t)
		rec := hitRecord("vr_zero_1", 42, false, 0)

		require.NoError(t, persistRecord(ctx, gdb, rec, nil, 0, false))
		var row Record
		require.NoError(t, gdb.Where("rec_no = ?", "vr_zero_1").Take(&row).Error)
		assert.False(t, row.Counted)
		var n int64
		require.NoError(t, gdb.Model(&Counter{}).Where("user_id = ?", 42).Count(&n).Error)
		assert.Zero(t, n, "权重 0 不该在计数器表里建行")
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
	require.NoError(t, gdb.AutoMigrate(&Counter{}, &CategoryCounter{}))

	require.NoError(t, gdb.Create(&Counter{
		UserId: 42, WindowStart: 1000, HitCount: 7, TotalCount: 31, BanCycle: 2,
		LastHitAt: 1500, UpdatedAt: 1500,
	}).Error)
	// 类型线是与账号总量线并列的封号触发器(anyReached 是 OR)。只清总量线的话,
	// 一个被类型线封掉的账号在"解封 + 重置计数"之后类型计数仍然停在阈值上,
	// 判据是 after >= threshold ⇒ 下一次同类命中必然再次越线;类型窗口配成 -1
	// (不限期限)时那个计数永远不会自然滚出,账号从此"解封一次、再犯一次就再封",
	// 而管理端没有任何页面显示这条线。
	require.NoError(t, gdb.Create(&CategoryCounter{
		UserId: 42, CategoryId: 2904, WindowStart: 1000, HitCount: 5, TotalCount: 9,
		LastHitAt: 1500, UpdatedAt: 1500,
	}).Error)

	before, catsBefore, reset, err := resetUserCounter(context.Background(), gdb, 42)
	require.NoError(t, err)
	assert.True(t, reset)
	assert.Equal(t, 7, before.HitCount, "审计要靠这个返回值记录清零前的状态")
	require.Len(t, catsBefore, 1, "清零前的类型线计数必须一起交给审计")
	assert.Equal(t, 5, catsBefore[0].HitCount)
	assert.EqualValues(t, 2904, catsBefore[0].CategoryId)

	var row Counter
	require.NoError(t, gdb.Where("user_id = ?", 42).Take(&row).Error)
	assert.Equal(t, 0, row.HitCount)
	assert.EqualValues(t, 31, row.TotalCount, "终身累计不得被清掉")
	assert.Equal(t, 2, row.BanCycle, "封禁周期回退会让自动封号对该用户永久失效")
	assert.Greater(t, row.WindowStart, int64(1000), "窗口必须重新起算")

	var cat CategoryCounter
	require.NoError(t, gdb.Where("user_id = ? AND category_id = ?", 42, 2904).Take(&cat).Error)
	assert.Equal(t, 0, cat.HitCount, "类型线必须一起清 —— 否则给的不是重新开始,是'再犯一次就再封'")
	assert.EqualValues(t, 9, cat.TotalCount, "类型线的终身累计同样不得被清掉")
	assert.Greater(t, cat.WindowStart, int64(1000), "类型线的窗口也必须重新起算")

	t.Run("没有计数行时不算失败", func(t *testing.T) {
		_, _, reset, err := resetUserCounter(context.Background(), gdb, 99)
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
