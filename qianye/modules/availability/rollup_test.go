package availability

import (
	"context"
	"testing"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/qianye/config"

	"github.com/glebarez/sqlite"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

// newRollupEnv 把扩展库句柄换成一个只有两张可用率表的 sqlite 内存库。
//
// 生产的扩展库固定是 MySQL,这里跑 sqlite 是刻意的:rollup 原先用的是
// `INSERT ... SELECT ... ON DUPLICATE KEY UPDATE`,MySQL 方言,在 sqlite 上
// 直接语法错误 —— 也就是说这条最容易写错的路径过去在单测里一行都跑不到。
// 现在它必须能在 sqlite 上跑通,这本身就是"三库兼容"的一条断言。
func newRollupEnv(t *testing.T) *gorm.DB {
	t.Helper()
	gdb, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{Logger: logger.Discard})
	require.NoError(t, err)
	sqlDB, err := gdb.DB()
	require.NoError(t, err)
	sqlDB.SetMaxOpenConns(1) // :memory: 每条连接是一个独立的库
	// 两张表都走生产的 AutoMigrate,不许手工补建 —— 这本身就是判据。
	//
	// 原先这里只 AutoMigrate(&Bucket{}),小时表靠抄建表语句 + 手工
	// `CREATE UNIQUE INDEX uk_hour_dim` 补出来,理由写的是"HourBucket 内嵌
	// Bucket 复用了同一份索引名标签,而 sqlite 的索引名是库级唯一"。
	// 那个规避把真缺陷藏了整整一轮:索引名在 PostgreSQL 上同样是 schema 级唯一,
	// 生产的 PG 部署里小时表一条索引都没建出来,rollupHour 的 ON CONFLICT
	// 恒报 42P10、小时表永远为空。现在索引名由 `composite:` 按表名派生,
	// 两张表天然不撞,AutoMigrate 在 sqlite/MySQL/PostgreSQL 上都能把
	// 小时表连同它的唯一索引一起建出来 —— 把名字改回硬编码,这一行当场变红。
	require.NoError(t, gdb.AutoMigrate(&Bucket{}, &HourBucket{}))

	prevHandle := qyDBHandle.Swap(gdb)
	prevHealthy := qyDBHealthy.Swap(true)
	prevCfg := qyConfig.Swap(&config.Config{
		Enabled:      true,
		Availability: config.Availability{Enabled: true, BucketSeconds: 300},
	})
	t.Cleanup(func() {
		qyDBHandle.Store(prevHandle)
		qyDBHealthy.Store(prevHealthy)
		qyConfig.Store(prevCfg)
		_ = sqlDB.Close()
	})
	return gdb
}

func seedBucket(t *testing.T, gdb *gorm.DB, ts int64, group, model string, reqTotal, success int64) {
	t.Helper()
	require.NoError(t, gdb.Create(&Bucket{
		BucketTs: ts, GroupName: group, ModelName: model,
		ReqTotal: reqTotal, SuccessCount: success, UpdatedAt: ts,
	}).Error)
}

func hourRows(t *testing.T, gdb *gorm.DB) []Bucket {
	t.Helper()
	var rows []Bucket
	require.NoError(t, gdb.Table(hourTable).Order("bucket_ts ASC, group_name ASC, model_name ASC").Find(&rows).Error)
	return rows
}

// rollup 的游标必须能跨过 5 分钟表里的空档。
//
// 这是线上实测坐实的那个死锁:游标取自小时表的 MAX(bucket_ts),而窗口按墙钟
// 封顶 maxRollupHoursPerRun 小时。5 分钟表一旦出现超过
// (maxRollupHoursPerRun - rollupBacktrackHours) 小时的空档,窗口里查不到任何
// 新数据 → 小时表 MAX 不变 → 下一轮算出同一个窗口 → 永久卡死。
// 现场的样子是:小时表 1 行、5 分钟表 141 行横跨 14 天、租约 fence 已经 1900+,
// 也就是空跑了一千九百多轮,而前端「7 天 / 30 天」读的正是小时表。
//
// 空档宽度取 maxRollupHoursPerRun + 10 小时,确保它落在旧实现的窗口之外;
// 断言看的是"空档之后那个整点有没有被汇总",这正是修复前后唯一的行为差异。
func TestRollupCursorCrossesGapInFiveMinuteTable(t *testing.T) {
	gdb := newRollupEnv(t)

	now := common.GetTimestamp()
	// 最后一个有数据的整点必须已经走完,否则它会被"只汇总已走完的整点"挡掉。
	after := alignHour(now) - 3600
	before := after - int64(maxRollupHoursPerRun+10)*3600

	seedBucket(t, gdb, before+300, "default", "gpt-5", 10, 9)
	seedBucket(t, gdb, after+600, "default", "gpt-5", 20, 18)

	runRollup(context.Background())

	rows := hourRows(t, gdb)
	require.Len(t, rows, 2, "空档两侧的整点都必须被汇总")
	assert.Equal(t, before, rows[0].BucketTs)
	assert.Equal(t, after, rows[1].BucketTs, "游标卡在空档前,空档之后的数据永远补不上")
	assert.Equal(t, int64(20), rows[1].ReqTotal)
	assert.Equal(t, int64(18), rows[1].SuccessCount)
}

// 存量补齐:小时表已经有一行老游标,5 分钟表在它之后横跨多天。
// 这是本机线上库的形状 —— 修复必须能把这批存量一次补齐,而不是每轮只挪一点。
func TestRollupBacklogCatchesUpPastStaleCursor(t *testing.T) {
	gdb := newRollupEnv(t)

	base := alignHour(common.GetTimestamp()) - int64(maxRollupHoursPerRun*4)*3600
	// 老游标:小时表停在 base,而 5 分钟表的数据散落在之后几天。
	require.NoError(t, gdb.Table(hourTable).Create(&Bucket{
		BucketTs: base, GroupName: "default", ModelName: "gpt-5",
		ReqTotal: 1, SuccessCount: 1, UpdatedAt: base,
	}).Error)

	seedBucket(t, gdb, base+300, "default", "gpt-5", 1, 1)
	dataHours := []int64{base + 70*3600, base + 100*3600, base + 150*3600}
	for _, h := range dataHours {
		seedBucket(t, gdb, h+120, "default", "gpt-5", 5, 4)
	}

	runRollup(context.Background())

	got := make([]int64, 0, 4)
	for _, row := range hourRows(t, gdb) {
		got = append(got, row.BucketTs)
	}
	assert.Equal(t, append([]int64{base}, dataHours...), got,
		"每一个有数据的整点都要被补齐,空档不能再挡住后面的数据")
}

// rollup 是覆盖语义而不是累加:同一个整点重跑多少次,结果都必须一样。
// 写成累加的话每重跑一次数字就翻一倍,而 rollupBacktrackHours 保证了
// 最近几个整点每轮都会被重跑 —— 这条断言守的是「可用率凭空变好/变坏」。
func TestRollupIsIdempotentAndPicksUpLateFlush(t *testing.T) {
	gdb := newRollupEnv(t)

	hour := alignHour(common.GetTimestamp()) - 3600
	seedBucket(t, gdb, hour+300, "default", "gpt-5", 10, 8)

	runRollup(context.Background())
	runRollup(context.Background())

	rows := hourRows(t, gdb)
	require.Len(t, rows, 1)
	assert.Equal(t, int64(10), rows[0].ReqTotal, "重跑必须覆盖而不是累加")
	assert.Equal(t, int64(8), rows[0].SuccessCount)

	// 迟到的 flush 落进同一个整点(另一个 5 分钟桶),下一轮必须把它并进来。
	seedBucket(t, gdb, hour+900, "default", "gpt-5", 4, 4)
	runRollup(context.Background())

	rows = hourRows(t, gdb)
	require.Len(t, rows, 1)
	assert.Equal(t, int64(14), rows[0].ReqTotal, "回退重跑要能吸收迟到数据")
	assert.Equal(t, int64(12), rows[0].SuccessCount)
}

// 只汇总已经走完的整点:把半小时的数据固化成"一小时"会让 7 天 / 30 天的
// 曲线在最新的那个点上凭空塌陷,而那个点恰恰是运营最先看的。
func TestRollupSkipsCurrentIncompleteHour(t *testing.T) {
	gdb := newRollupEnv(t)

	now := common.GetTimestamp()
	seedBucket(t, gdb, alignHour(now), "default", "gpt-5", 7, 7)

	runRollup(context.Background())

	assert.Empty(t, hourRows(t, gdb), "当前未走完的整点不得被汇总")
}

// 单轮成本上限仍然存在,只是从"48 小时墙钟"变成"48 个有数据的整点"。
// 没有它,一次积压几个月的补齐会在一轮里发出上千条语句。
func TestRollupCapsHoursPerRun(t *testing.T) {
	gdb := newRollupEnv(t)

	base := alignHour(common.GetTimestamp()) - int64(maxRollupHoursPerRun+5)*3600
	for i := 0; i < maxRollupHoursPerRun+3; i++ {
		seedBucket(t, gdb, base+int64(i)*3600+60, "default", "gpt-5", 1, 1)
	}

	runRollup(context.Background())
	assert.Len(t, hourRows(t, gdb), maxRollupHoursPerRun, "单轮条数必须封顶")

	// 下一轮接着往前走,不需要等墙钟。
	runRollup(context.Background())
	assert.Len(t, hourRows(t, gdb), maxRollupHoursPerRun+3)
}
