package grouppricing

import (
	"testing"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/model"

	"github.com/glebarez/sqlite"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

// reconcile_test.go —— 对账口径。
//
// 这里锁的是那个运营会拿去做上线决策的数字:「切换后这个月会多收/少收多少」。
// 算错的后果不是报表难看,是有人据此按下了关闭影子模式的按钮。

// useLogDB 建一个只含 logs 表的临时主库日志库。
func useLogDB(t *testing.T) *gorm.DB {
	t.Helper()
	gdb, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	require.NoError(t, err)
	sqlDB, err := gdb.DB()
	require.NoError(t, err)
	sqlDB.SetMaxOpenConns(1)
	require.NoError(t, gdb.AutoMigrate(&model.Log{}))

	prev := model.LOG_DB
	model.LOG_DB = gdb
	t.Cleanup(func() {
		model.LOG_DB = prev
		_ = sqlDB.Close()
	})
	return gdb
}

func seedLog(t *testing.T, gdb *gorm.DB, ts int64, group, modelName string, quota int) {
	t.Helper()
	require.NoError(t, gdb.Create(&model.Log{
		CreatedAt: ts, Type: model.LogTypeConsume,
		Group: group, ModelName: modelName, Quota: quota,
	}).Error)
}

func seedBucket(t *testing.T, gdb *gorm.DB, ts int64, group, modelName, mode, oldV, newV string, exact bool, requests int64) {
	t.Helper()
	require.NoError(t, gdb.Create(&ShadowBucket{
		BucketTs: alignBucket(ts), GroupName: group, ModelName: modelName, Mode: mode,
		OldValue: oldV, NewValue: newV, Exact: exact, Requests: requests,
		SampleRequestId: "req-x",
	}).Error)
}

// TestShadowSummaryComputesExactDelta 锁定核心折算。
//
// 倍率从 2 降到 0.5 → 系数 0.25 → 实际扣了 1000,切换后只会扣 250,差额 -750。
// 这个数必须精确,不是"大约少四分之三"。
func TestShadowSummaryComputesExactDelta(t *testing.T) {
	useConfig(t, true, true)
	gdb := newTestDB(t)
	logDB := useLogDB(t)

	now := common.GetTimestamp()
	start, end := now-3600, now+3600
	seedBucket(t, gdb, now, "vip", "gpt-4o", ModeRatio, "2", "0.5", true, 10)
	seedLog(t, logDB, now, "vip", "gpt-4o", 600)
	seedLog(t, logDB, now, "vip", "gpt-4o", 400)
	// 区间外与别的分组的日志都不该被算进来。
	seedLog(t, logDB, start-7200, "vip", "gpt-4o", 999999)
	seedLog(t, logDB, now, "default", "gpt-4o", 888888)

	sum, err := buildShadowSummary(start, end)
	require.NoError(t, err)
	require.Empty(t, sum.QuotaSourceError)
	require.Len(t, sum.Segments, 1)

	s := sum.Segments[0]
	assert.Equal(t, int64(10), s.Requests)
	assert.Equal(t, int64(1000), s.ActualQuota)
	assert.Equal(t, "0.250000", s.Factor)
	assert.Equal(t, int64(-750), s.DeltaQuota, "切换后少收 750")
	assert.True(t, s.ShareIsExact)
	assert.Equal(t, "req-x", s.SampleRequestId)

	assert.Equal(t, int64(1000), sum.TotalActualQuota)
	assert.Equal(t, int64(-750), sum.TotalDeltaQuota)
	assert.Zero(t, sum.InexactRequests)
}

// TestShadowSummaryExcludesInexactSegments 锁定"不精确的段不许混进合计"。
//
// 旧值为 0、计价口径切换这两类段没有可用的折算系数。把它们默默当成 0 差额,
// 会让合计看起来完整而实际漏了一块 —— 而运营正是拿这个合计去判断影响面的。
func TestShadowSummaryExcludesInexactSegments(t *testing.T) {
	useConfig(t, true, true)
	gdb := newTestDB(t)
	logDB := useLogDB(t)

	now := common.GetTimestamp()
	start, end := now-3600, now+3600
	seedBucket(t, gdb, now, "vip", "a", ModeRatio, "2", "1", true, 4)
	seedBucket(t, gdb, now, "vip", "b", ModeRatio, "0", "1", true, 5)    // 旧值为 0
	seedBucket(t, gdb, now, "vip", "c", ModePrice, "", "0.02", false, 6) // 口径切换
	seedLog(t, logDB, now, "vip", "a", 800)
	seedLog(t, logDB, now, "vip", "b", 0)
	seedLog(t, logDB, now, "vip", "c", 500)

	sum, err := buildShadowSummary(start, end)
	require.NoError(t, err)
	require.Len(t, sum.Segments, 3)

	byModel := map[string]ShadowSegment{}
	for _, s := range sum.Segments {
		byModel[s.ModelName] = s
	}
	assert.Equal(t, int64(-400), byModel["a"].DeltaQuota)
	assert.False(t, byModel["b"].Exact)
	assert.NotEmpty(t, byModel["b"].Reason)
	assert.Zero(t, byModel["b"].DeltaQuota)
	assert.False(t, byModel["c"].Exact)
	assert.NotEmpty(t, byModel["c"].Reason)

	assert.Equal(t, int64(-400), sum.TotalDeltaQuota, "只有可折算的段进合计")
	assert.Equal(t, int64(11), sum.InexactRequests, "不可折算的请求数必须单独露出来")
	assert.Equal(t, int64(15), sum.TotalRequests)
}

// TestShadowSummarySplitsWhenRuleChangedMidWindow 锁定同一维度换过规则值的处理。
//
// 主库日志里没有"当时生效的是哪一版规则",只能按请求数占比把金额分摊回去。
// 这个分摊必须被标成不精确(ShareIsExact=false),否则运营会以为那是个准确数字。
func TestShadowSummarySplitsWhenRuleChangedMidWindow(t *testing.T) {
	useConfig(t, true, true)
	gdb := newTestDB(t)
	logDB := useLogDB(t)

	now := common.GetTimestamp()
	start, end := now-2*3600, now+3600
	seedBucket(t, gdb, now-3600, "vip", "gpt-4o", ModeRatio, "2", "1", true, 3)
	seedBucket(t, gdb, now, "vip", "gpt-4o", ModeRatio, "2", "0.5", true, 1)
	seedLog(t, logDB, now, "vip", "gpt-4o", 1000)

	sum, err := buildShadowSummary(start, end)
	require.NoError(t, err)
	require.Len(t, sum.Segments, 2)

	for _, s := range sum.Segments {
		assert.False(t, s.ShareIsExact, "跨规则版本的金额是按请求数分摊的,必须标成不精确")
	}
	// 3/4 的请求走 factor=0.5(分摊 750,差额 -375),
	// 1/4 的请求走 factor=0.25(分摊 250,差额 -187)。
	byNew := map[string]ShadowSegment{}
	for _, s := range sum.Segments {
		byNew[s.NewValue] = s
	}
	assert.Equal(t, int64(750), byNew["1"].AttributedQuota)
	assert.Equal(t, int64(-375), byNew["1"].DeltaQuota)
	assert.Equal(t, int64(250), byNew["0.5"].AttributedQuota)
	assert.Equal(t, int64(-187), byNew["0.5"].DeltaQuota)
}

// TestShadowSummaryRejectsOversizedRange:logs 是全站最大的表之一,
// 无上界的范围查询会把日志库拖垮,而这个接口是管理员随手点的。
func TestShadowSummaryRejectsOversizedRange(t *testing.T) {
	useConfig(t, true, true)
	newTestDB(t)
	now := common.GetTimestamp()

	_, err := buildShadowSummary(now-(maxReconcileDays+1)*86400, now)
	assert.Error(t, err)

	_, err = buildShadowSummary(now, now)
	assert.Error(t, err, "结束时间不大于开始时间必须报错")
}

// TestShadowSummarySurvivesLogDBFailure:主库日志取不到时,请求数与系数仍然
// 要返回,并显式说明金额列不可信。给一份看起来完整实则缺金额的报表,
// 比给一份带说明的半份报表危险得多。
func TestShadowSummarySurvivesLogDBFailure(t *testing.T) {
	useConfig(t, true, true)
	gdb := newTestDB(t)

	prev := model.LOG_DB
	model.LOG_DB = nil
	t.Cleanup(func() { model.LOG_DB = prev })

	now := common.GetTimestamp()
	seedBucket(t, gdb, now, "vip", "gpt-4o", ModeRatio, "2", "1", true, 3)

	sum, err := buildShadowSummary(now-3600, now+3600)
	require.NoError(t, err)
	assert.NotEmpty(t, sum.QuotaSourceError)
	require.Len(t, sum.Segments, 1)
	assert.Equal(t, int64(3), sum.Segments[0].Requests)
	assert.Zero(t, sum.Segments[0].DeltaQuota)
}
