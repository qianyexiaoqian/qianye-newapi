package violation

import (
	"context"
	"strings"
	"testing"

	"github.com/QuantumNous/new-api/common"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// aireview_log_test.go —— 审核日志这一层的三条契约。
//
// 它们各自守住一个"坏了也没有症状"的地方:
//
//	留存内容    坏了 → 日志里那一列是空的,或者是一段与模型读到的不同的文本
//	保留期      坏了 → 表永远不清理(升级前的状态),几个月后才发现
//	一次性迁移  坏了 → 覆盖掉运营显式关掉的留存开关
//
// 三条都不依赖真实数据库以外的东西,与包内其余测试共用 newAIWiringDB。

// ─────────────────────────── 留存内容 ───────────────────────────

// TestReviewLogContentMirrorsWhatTheModelRead 钉住这一列的语义:
// **模型读到的是这些**,不是"用户发了什么"。
//
// 两者不同的场合就是本功能最需要它的场合:送审文本经 max_input_chars 头尾截断,
// 而留存又在此基础上再截一次。存原文会让日志与判定依据对不上 —— 一条判"未违规"
// 的记录旁边放着一段明显违规的原文,而真相是那一段根本没被送出去。
func TestReviewLogContentMirrorsWhatTheModelRead(t *testing.T) {
	long := strings.Repeat("甲", 300) + "违规关键词" + strings.Repeat("乙", 300)

	t.Run("关掉留存时一个字都不留", func(t *testing.T) {
		rt := &aiRuntime{MaxInputChars: 4000, LogContent: false, LogContentMaxChars: 1000}
		body, chars := reviewLogContent(rt, long, false)
		assert.Empty(t, body)
		// 字数也必须是 0:留一个"原文 606 字、留存 0 字"的组合会让界面显示
		// "已截断",而真相是"没开留存"。两种成因的处置人完全不同。
		assert.Zero(t, chars)
	})

	t.Run("字数记的是送审文本,不是原文", func(t *testing.T) {
		// 送审上限 100 → 模型只读到 100 字上下,哪怕原文有 600 多字。
		rt := &aiRuntime{MaxInputChars: 100, LogContent: true, LogContentMaxChars: 1000}
		body, chars := reviewLogContent(rt, long, false)
		require.NotEmpty(t, body)
		assert.Less(t, chars, 200,
			"content_chars 必须是送审文本的字数;记成原文字数会让界面永远显示「已截断」")
		assert.Contains(t, body, "[truncated]",
			"送审侧的截断标记要原样留在日志里 —— 那正是模型读到的东西")
	})

	t.Run("留存上限比送审上限更小时取头尾", func(t *testing.T) {
		rt := &aiRuntime{MaxInputChars: 4000, LogContent: true, LogContentMaxChars: 100}
		body, chars := reviewLogContent(rt, long, false)
		assert.Equal(t, 605, chars, "送审的是完整的 605 字(300 + 5 + 300)")
		assert.Less(t, len([]rune(body)), 200, "留存被压到上限附近")
		// 取头尾而不是只取开头:刷子习惯把违规内容塞在长 padding 之后,
		// 只留开头的日志会整页都是无意义的填充。
		assert.True(t, strings.HasPrefix(body, "甲"))
		assert.True(t, strings.HasSuffix(body, "乙"))
	})

	t.Run("脱敏发生在入库之前", func(t *testing.T) {
		rt := &aiRuntime{MaxInputChars: 4000, LogContent: true, LogContentMaxChars: 1000}
		body, _ := reviewLogContent(rt, "联系 a@b.com 或 13800138000,密钥 sk-abcdefghijklmnop1234", false)
		assert.NotContains(t, body, "a@b.com")
		assert.NotContains(t, body, "13800138000")
		assert.NotContains(t, body, "sk-abcdefghijklmnop1234")
		assert.Contains(t, body, "«email»")
	})

	t.Run("内联 base64 只留描述符", func(t *testing.T) {
		rt := &aiRuntime{MaxInputChars: 32000, LogContent: true, LogContentMaxChars: 8000}
		blob := "data:image/png;base64," + strings.Repeat("A", 800)
		body, _ := reviewLogContent(rt, "看这张图 "+blob, false)
		assert.NotContains(t, body, strings.Repeat("A", 800),
			"不剥离的话一行就是几百 KB,而那几百 KB 对「这是什么内容」没有任何贡献")
		assert.Contains(t, body, "image/png")
	})

	t.Run("判违规时始终留、留完整,不受开关与上限约束", func(t *testing.T) {
		// 这是项目方点名要的那一档:「送审内容你要留存一下。特别是判定违规的」。
		// 两条都要钉住 —— 总开关关着也照留、字数上限不再截第二刀 —— 因为它们
		// 各自的失效都无声无息:前者表现为"违规那一行也是未留存",后者表现为
		// "内容看起来完整,其实后半截(常常正是违规的那一段)被砍了"。
		rt := &aiRuntime{
			MaxInputChars: 4000, LogContent: false,
			LogContentMaxChars: 100, LogContentViolationFull: true,
		}
		body, chars := reviewLogContent(rt, long, true)
		assert.Equal(t, 605, chars)
		assert.Equal(t, 605, len([]rune(body)),
			"违规行不该再被 log_content_max_chars 截一刀")
		assert.NotContains(t, body, "[truncated]")

		// 同一份设置下,未违规的那一档仍然一个字都不留(总开关是关的)。
		empty, emptyChars := reviewLogContent(rt, long, false)
		assert.Empty(t, empty, "总开关关着时,未违规的行不该被这一档顺带打开")
		assert.Zero(t, emptyChars)
	})

	t.Run("这一档被显式关掉时,违规行跟着总开关走", func(t *testing.T) {
		// 运维显式关掉它是一次真实的决定(隐私口径),必须被尊重 ——
		// "违规行特殊对待"不是一条不可关闭的硬规则。
		rt := &aiRuntime{
			MaxInputChars: 4000, LogContent: false,
			LogContentMaxChars: 100, LogContentViolationFull: false,
		}
		body, chars := reviewLogContent(rt, long, true)
		assert.Empty(t, body)
		assert.Zero(t, chars)
	})

	t.Run("nil 快照不 panic", func(t *testing.T) {
		body, chars := reviewLogContent(nil, "任意内容", false)
		assert.Empty(t, body)
		assert.Zero(t, chars)
	})
}

// ─────────────────────────── 保留期 ───────────────────────────

// TestEffectiveAIReviewRetentionDays 钉住 0 的方向。
//
// 0 在这一格上是"没设置过",不是"永久保留"。反过来的话,升级后那一批
// ADD COLUMN 回填出来的 0 会让这张表继续无限长下去 —— 也就是升级前的缺陷
// 原样保留,而设置页上却写着"保留 0 天"。
func TestEffectiveAIReviewRetentionDays(t *testing.T) {
	assert.Equal(t, defaultAIReviewRetentionDays, effectiveAIReviewRetentionDays(0))
	assert.Equal(t, defaultAIReviewRetentionDays, effectiveAIReviewRetentionDays(-1))
	assert.Equal(t, 30, effectiveAIReviewRetentionDays(30))
	assert.Equal(t, maxAIReviewRetentionDays,
		effectiveAIReviewRetentionDays(maxAIReviewRetentionDays+1))
}

// TestAIReviewRetentionGCDeletesOnlyExpiredRows 钉住清理的边界与"真的删了"。
//
// 这张表在此之前**从来没有被清理过**,所以这条测试守的是一个新增的能力,
// 而不是一次回归。它同时守住批量删的形态:先取主键再按主键删 ——
// DELETE ... ORDER BY / LIMIT 是 MySQL 专有扩展,PostgreSQL 直接语法错误,
// 而那条错误只被写进日志,表现是"清理一直在跑、表一直在涨"。
func TestAIReviewRetentionGCDeletesOnlyExpiredRows(t *testing.T) {
	gdb := newAIWiringDB(t)
	// 台账库在测试里没分家,db.Log() 因此回落到同一个句柄 —— 这正是绝大多数
	// 部署的形态,也是这条清理必须先跑对的那一种。
	useDBForTest(t, gdb)

	now := common.GetTimestamp()
	require.NoError(t, gdb.Create(&AISetting{
		Id: 1, LogRetentionDays: 2,
		PreTimeoutMs: 1500, AsyncTimeoutMs: 8000,
		MaxInputChars: defaultAIMaxInputChars,
		CreatedAt:     now, UpdatedAt: now,
	}).Error)

	rows := []AIReview{
		{ReviewNo: "keep-now", CreatedAt: now},
		{ReviewNo: "keep-1d", CreatedAt: now - 86400},
		{ReviewNo: "drop-3d", CreatedAt: now - 3*86400},
		{ReviewNo: "drop-30d", CreatedAt: now - 30*86400},
	}
	for i := range rows {
		require.NoError(t, gdb.Create(&rows[i]).Error)
	}

	runAIReviewRetentionGC(context.Background())

	var left []string
	require.NoError(t, gdb.Model(&AIReview{}).Order("review_no").Pluck("review_no", &left).Error)
	assert.Equal(t, []string{"keep-1d", "keep-now"}, left,
		"保留期是 2 天:1 天前的必须留下,3 天前的必须删掉")
}

// TestAIReviewRetentionGCFallsBackToDefaultWithoutSetting 钉住"读不到设置行也要清理"。
//
// 回落到"不清理"是更危险的方向:它把一次瞬时读失败变成一张永远不清的表,
// 而症状要几个月后才出现。
func TestAIReviewRetentionGCFallsBackToDefaultWithoutSetting(t *testing.T) {
	gdb := newAIWiringDB(t)
	// 台账库在测试里没分家,db.Log() 因此回落到同一个句柄 —— 这正是绝大多数
	// 部署的形态,也是这条清理必须先跑对的那一种。
	useDBForTest(t, gdb)

	now := common.GetTimestamp()
	require.NoError(t, gdb.Create(&AIReview{
		ReviewNo: "old", CreatedAt: now - int64(defaultAIReviewRetentionDays+1)*86400,
	}).Error)
	require.NoError(t, gdb.Create(&AIReview{ReviewNo: "fresh", CreatedAt: now}).Error)

	runAIReviewRetentionGC(context.Background())

	var left []string
	require.NoError(t, gdb.Model(&AIReview{}).Pluck("review_no", &left).Error)
	assert.Equal(t, []string{"fresh"}, left)
}

// ─────────────────────────── 一次性迁移 ───────────────────────────

// TestAIReviewLogDefaultsMigrationNeverOverwritesADecision 是这条迁移的全部风险面。
//
// 它会打开内容留存 —— 也就是开始把用户请求内容抄进我们自己的库。可接受的
// 前提**只有一个**:它绝不覆盖任何人做过的决定。判据是三列同时为零值,而任何
// 一次管理端保存都会把另外两列写成正数(validateAISetting 不接受 0),
// 所以"我就是不想留内容"那个决定永远不满足条件。
func TestAIReviewLogDefaultsMigrationNeverOverwritesADecision(t *testing.T) {
	gdb := newAIWiringDB(t)
	now := common.GetTimestamp()

	// ① 存量行:三列都是 AutoMigrate 回填出来的零值。
	require.NoError(t, gdb.Create(&AISetting{
		Id: 1, Enabled: true, ThirdPartyNoticeAck: true,
		PreTimeoutMs: 1500, AsyncTimeoutMs: 8000,
		MaxInputChars: defaultAIMaxInputChars,
		CreatedAt:     now, UpdatedAt: now,
	}).Error)

	n, err := migrateAIReviewLogDefaults(context.Background(), gdb)
	require.NoError(t, err)
	assert.EqualValues(t, 1, n)

	var got AISetting
	require.NoError(t, gdb.Where("id = ?", 1).Take(&got).Error)
	assert.True(t, got.LogContent)
	assert.Equal(t, defaultAIReviewContentChars, got.LogContentMaxChars)
	assert.Equal(t, defaultAIReviewRetentionDays, got.LogRetentionDays)

	// ② 运营显式关掉留存(保存过一次,另外两列因此是正数)。再跑迁移必须无动于衷。
	require.NoError(t, gdb.Model(&AISetting{}).Where("id = ?", 1).
		Updates(map[string]any{
			"log_content":           false,
			"log_content_max_chars": 200,
			"log_retention_days":    7,
		}).Error)

	n, err = migrateAIReviewLogDefaults(context.Background(), gdb)
	require.NoError(t, err)
	assert.Zero(t, n, "保存过的行绝不能被迁移碰第二次")

	require.NoError(t, gdb.Where("id = ?", 1).Take(&got).Error)
	assert.False(t, got.LogContent, "运营关掉的留存被一次重启打开了 —— 这是本迁移唯一不可接受的失败")
	assert.Equal(t, 7, got.LogRetentionDays)

	// ③ 幂等:再跑一次仍然是 0 行。
	n, err = migrateAIReviewLogDefaults(context.Background(), gdb)
	require.NoError(t, err)
	assert.Zero(t, n)
}
