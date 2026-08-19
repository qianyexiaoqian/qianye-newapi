package twophase

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/qianye/config"
	qymodel "github.com/QuantumNous/new-api/qianye/model"

	"github.com/glebarez/sqlite"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
	gormlogger "gorm.io/gorm/logger"
)

// probe_db_test.go —— "证据什么时候可以销毁,以及没有证据时该怎么答"。
//
// 探针行是"主库那一笔钱到底动没动"的唯一权威证据。两条不变量:
//
//  1. 保留期清理只能销毁**已 Success** 单的证据。Failed 单恰恰是需要证据的那些
//     (commit 阶段断连时它其实已经把钱动了),证据一没,ProbeMainSide 就会
//     把"查不到"读成"确定没动",管理端一按重试就把同一笔钱再发一次。
//  2. 查不到证据时必须回答"不可判定"而不是"确定没动"。探针关掉、探针报错、
//     探针行缺失,三种"查不到"里都可能藏着一笔已经动过的钱。

// newProbeEnv 装好扩展库(资金单)与主库(探针表)两条真实句柄。
func newProbeEnv(t *testing.T, outboxOn bool, retentionDays int) (ext *gorm.DB, main *gorm.DB) {
	t.Helper()

	open := func(name string) *gorm.DB {
		dsn := filepath.Join(t.TempDir(), name) + "?_pragma=busy_timeout(5000)"
		gdb, err := gorm.Open(sqlite.Open(dsn), &gorm.Config{Logger: gormlogger.Discard})
		require.NoError(t, err)
		sqlDB, err := gdb.DB()
		require.NoError(t, err)
		t.Cleanup(func() { _ = sqlDB.Close() })
		return gdb
	}

	ext = open("qy_ext.db")
	require.NoError(t, ext.AutoMigrate(&qymodel.FundOrder{}))
	main = open("main.db")
	require.NoError(t, main.AutoMigrate(&model.QyFundOutbox{}))

	prevHandle := qyDBHandle.Swap(ext)
	prevHealthy := qyDBHealthy.Swap(true)
	prevMain := model.DB
	model.DB = main

	auditOff := false
	prevCfg := qyConfig.Swap(&config.Config{
		Audit: config.Audit{Enabled: &auditOff},
		TwoPhase: config.TwoPhase{
			MainOutboxEnabled:   &outboxOn,
			OutboxRetentionDays: retentionDays,
			BatchSize:           2,
		},
	})
	t.Cleanup(func() {
		qyDBHandle.Store(prevHandle)
		qyDBHealthy.Store(prevHealthy)
		qyConfig.Store(prevCfg)
		model.DB = prevMain
	})
	return ext, main
}

func seedProbeOrder(t *testing.T, ext *gorm.DB, orderNo string, status int8, createdAt int64) {
	t.Helper()
	require.NoError(t, ext.Create(&qymodel.FundOrder{
		OrderNo: orderNo, Kind: qymodel.KindLotteryPayout, Status: status,
		IdemScope: "probe", IdemKey: orderNo,
		UserId: 7, AmountQuota: 500, CreatedAt: createdAt, UpdatedAt: createdAt,
	}).Error)
}

func seedProbeRow(t *testing.T, main *gorm.DB, orderNo string, createdAt int64) {
	t.Helper()
	require.NoError(t, main.Create(&model.QyFundOutbox{
		OrderNo: orderNo, Kind: qymodel.KindLotteryPayout,
		UserId: 7, Amount: 500, CreatedAt: createdAt,
	}).Error)
}

// ProbeMainSide 的三态必须严格区分。合并成 bool 的那一版把"没有证据"
// 读成"确定没动",而调用方据此做的是不可逆的重发与回滚。
func TestProbeMainSide_ThreeValued(t *testing.T) {
	cases := []struct {
		name     string
		outboxOn bool
		hasRow   bool
		nilOrder bool
		want     MainVerdict
	}{
		{name: "探针在 → 确定已生效", outboxOn: true, hasRow: true, want: MainApplied},
		{name: "探针可用但查不到 → 确定未生效", outboxOn: true, want: MainNotApplied},
		{name: "探针整体关掉 → 不可判定", outboxOn: false, want: MainUnknown},
		{name: "探针关掉且本来有行 → 仍然不可判定", outboxOn: false, hasRow: true, want: MainUnknown},
		{name: "没有单据 → 不可判定", outboxOn: true, nilOrder: true, want: MainUnknown},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			ext, main := newProbeEnv(t, tc.outboxOn, 30)
			now := common.GetTimestamp()
			seedProbeOrder(t, ext, "LP-probe", qymodel.StatusFailed, now)
			if tc.hasRow {
				seedProbeRow(t, main, "LP-probe", now)
			}
			var order *qymodel.FundOrder
			if !tc.nilOrder {
				order = &qymodel.FundOrder{OrderNo: "LP-probe", CreatedAt: now}
			}
			assert.Equal(t, tc.want, ProbeMainSide(order))
		})
	}
}

// MainUnknown 必须是零值:任何忘记赋值、任何新增的判不出来的分支,
// 都要退化成"交给人",而不是退化成"可以再发一次钱"。
func TestMainVerdict_ZeroValueIsUnknown(t *testing.T) {
	var v MainVerdict
	assert.Equal(t, MainUnknown, v)
}

// 保留期清理只能销毁已 Success 单的探针行。
//
// 这是 F1 的直接回归:旧实现按 created_at 一刀切,把一张 Failed 单
// (commit 断连、钱其实已经动了)的唯一证据删掉,ProbeMainSide 随后只能
// 读出"确定没动",管理端重试就是第二次发钱。
func TestPruneOutbox_KeepsEvidenceForUnsettledOrders(t *testing.T) {
	ext, main := newProbeEnv(t, true, 30)
	old := common.GetTimestamp() - 40*86400
	fresh := common.GetTimestamp()

	seed := func(orderNo string, status int8, createdAt int64) {
		seedProbeOrder(t, ext, orderNo, status, createdAt)
		seedProbeRow(t, main, orderNo, createdAt)
	}
	seed("LP-success-old", qymodel.StatusSuccess, old)
	seed("LP-failed-old", qymodel.StatusFailed, old)
	seed("LP-pending-old", qymodel.StatusPending, old)
	seed("LP-uncertain-old", qymodel.StatusUncertain, old)
	seed("LP-success-fresh", qymodel.StatusSuccess, fresh)
	// 有探针行却没有资金单:主库动过钱而扩展库没有对应记账,这条孤儿证据
	// 更不能删 —— 删了这笔资金变更在系统里就彻底没有痕迹了。
	seedProbeRow(t, main, "LP-orphan-old", old)

	PruneOutbox(context.Background())

	var left []string
	require.NoError(t, main.Model(&model.QyFundOutbox{}).Order("order_no").
		Pluck("order_no", &left).Error)
	assert.Equal(t, []string{
		"LP-failed-old", "LP-orphan-old", "LP-pending-old",
		"LP-success-fresh", "LP-uncertain-old",
	}, left, "只有已 Success 且过了保留期的探针行可以删")

	// 证据还在 ⇒ 探针仍能给出 MainApplied,重发闸门继续有效。
	assert.Equal(t, MainApplied,
		ProbeMainSide(&qymodel.FundOrder{OrderNo: "LP-failed-old", CreatedAt: old}))
}

// 逐行判定必须能跨过删不掉的行继续推进。
//
// 没有主键游标的话,每一轮都会重新捞到同一批"删不掉的行",
// 一旦它们占满一个批次,清理就永远停在原地。
func TestPruneOutbox_CursorAdvancesPastRetainedRows(t *testing.T) {
	ext, main := newProbeEnv(t, true, 30) // BatchSize=2
	old := common.GetTimestamp() - 40*86400

	// 前两行(= 一整批)都是删不掉的,可删的那些排在它们后面。
	for _, tc := range []struct {
		no     string
		status int8
	}{
		{"LP-a-failed", qymodel.StatusFailed},
		{"LP-b-failed", qymodel.StatusFailed},
		{"LP-c-success", qymodel.StatusSuccess},
		{"LP-d-success", qymodel.StatusSuccess},
		{"LP-e-success", qymodel.StatusSuccess},
	} {
		seedProbeOrder(t, ext, tc.no, tc.status, old)
		seedProbeRow(t, main, tc.no, old)
	}

	PruneOutbox(context.Background())

	var left []string
	require.NoError(t, main.Model(&model.QyFundOutbox{}).Order("order_no").
		Pluck("order_no", &left).Error)
	assert.Equal(t, []string{"LP-a-failed", "LP-b-failed"}, left,
		"整整一批都删不掉时,清理必须跳过它们继续往后走")
}

// 关掉保留期(0 天)= 永久保留,一行都不能删。
func TestPruneOutbox_RetentionZeroKeepsEverything(t *testing.T) {
	ext, main := newProbeEnv(t, true, 0)
	old := common.GetTimestamp() - 400*86400
	seedProbeOrder(t, ext, "LP-old", qymodel.StatusSuccess, old)
	seedProbeRow(t, main, "LP-old", old)

	PruneOutbox(context.Background())

	var n int64
	require.NoError(t, main.Model(&model.QyFundOutbox{}).Count(&n).Error)
	assert.EqualValues(t, 1, n)
}
