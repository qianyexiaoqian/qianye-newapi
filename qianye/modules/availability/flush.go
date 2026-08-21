package availability

import (
	"context"
	"fmt"
	"sync"
	"sync/atomic"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/qianye/config"
	"github.com/QuantumNous/new-api/qianye/db"
	"github.com/QuantumNous/new-api/qianye/service/lease"

	"github.com/bytedance/gopkg/util/gopool"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

const (
	defaultFlushSeconds = 60
	minFlushSeconds     = 5
	defaultRetentionDay = 15

	// hourRetentionFactor 让小时表的保留期是 5 分钟表的 12 倍。
	// 行数相当(5 分钟表 12 行才折成小时表 1 行),但查询 30 天只需扫 1/12。
	// 保留期没有独立配置字段,故由主保留期派生。
	hourRetentionFactor = 12
	maxHourRetentionDay = 365

	cleanupBatchSize  = 5000
	maxCleanupBatches = 200
	cleanupPause      = 200 * time.Millisecond

	// rollupBacktrackHours 每轮重跑最近 3 个整点。
	// 迟到的 flush(节点卡顿、DB 短暂不可用后回填)会改写已经 rollup 过的小时,
	// 只推进游标就会永久丢掉这部分数据;覆盖语义让重跑是安全的。
	rollupBacktrackHours = 3

	// maxRollupHoursPerRun 是单轮汇总的**整点条数**上限,不是墙钟跨度上限。
	// 两者的差别就是本模块最贵的那个 bug,理由见 pendingRollupHours。
	maxRollupHoursPerRun = 48

	// rollupUpsertBatch 是小时表批量 upsert 的分批大小。
	// (分组 × 模型) 的基数通常 < 500,分批只是给基数失控的站点兜底,
	// 避免单条 INSERT 的占位符撞上 MySQL 的 65535 上限。
	rollupUpsertBatch = 200
)

// startTasks 启动本模块的后台任务。
func startTasks() {
	if !config.Get().Availability.Enabled {
		return
	}
	warnAttemptLevelUnsupported()

	// flush 刻意不加租约:每个节点持有自己的内存热桶,必须各自落库。
	// 累加 upsert + 唯一索引保证多节点的结果被正确合并到同一行。
	gopool.Go(flushLoop)

	// rollup 与 cleanup 是全局唯一工作,必须走租约 —— common.IsMasterNode
	// 只是个环境变量,多节点都配成 master 时会双跑。
	lease.Run("availability.rollup", 10*time.Minute, runRollup)
	lease.Run("availability.cleanup", 6*time.Hour, runCleanup)
}

func flushInterval() time.Duration {
	n := config.Get().Availability.FlushIntervalSeconds
	if n <= 0 {
		n = defaultFlushSeconds
	}
	if n < minFlushSeconds {
		n = minFlushSeconds
	}
	return time.Duration(n) * time.Second
}

// flushLoop 用 sleep 而非固定 ticker:间隔支持配置热更新。
func flushLoop() {
	for {
		time.Sleep(flushInterval())
		if !config.Get().Availability.Enabled || !db.Available() {
			continue
		}
		flushOnce()
	}
}

// flushOnce 把全部内存热桶落库。
//
// 与上游 pkg/perf_metrics 的关键差异:上游只 flush 已完成的桶,在默认的
// 小时桶配置下意味着 DB 数据最多滞后 1 小时。这里连当前未完成桶一起 flush ——
// 累加 upsert 让分批落盘的结果完全正确,代价只是同一行被多次 UPDATE。
// 这是本模块做到「≤1 分钟新鲜度」的关键。
func flushOnce() {
	gdb := db.Get()
	if gdb == nil {
		return
	}
	statFlushRuns.Add(1)
	now := common.GetTimestamp()
	size := bucketSeconds()
	current := alignBucket(now, size)

	var rows int64
	hotBuckets.Range(func(key, value any) bool {
		k := key.(dimKey)
		c := value.(*counters)
		drained := c.drain()
		if drained.ReqTotal == 0 {
			// 只回收两个桶周期之前的空桶:更新的桶随时可能被采样写入,
			// 删掉之后并发的 observe 仍持有旧指针,那次累加就白丢了。
			if k.bucketTs < current-2*size {
				if _, ok := hotBuckets.LoadAndDelete(key); ok {
					hotSeries.Add(-1)
				}
			}
			return true
		}
		drained.BucketTs = k.bucketTs
		drained.GroupName = k.group
		drained.ModelName = k.model
		drained.UpdatedAt = now

		if err := upsertBucket(gdb, bucketTable, &drained); err != nil {
			// 失败即把数据加回内存等下一轮,绝不重试、绝不阻塞:
			// flush 协程卡住会让内存桶无限堆积。
			c.restore(&drained)
			statFlushFail.Add(1)
			db.MarkFailure(err)
			warnThrottled("flush", err)
			return true
		}
		rows++
		return true
	})

	statFlushRows.Add(rows)
	statFlushAt.Store(now)
	if rows > 0 {
		db.MarkSuccess()
	}
}

// upsertBucket 执行累加 upsert。
//
// 幂等性来自唯一索引 (bucket_ts, group_name, model_name) + 累加语义:
// 同一节点不会重复提交同一批(drain 已清零),多节点同时提交则被 DB 行锁串行化,
// 结果正确合并。刻意不包事务 —— 单行累加本身原子,包起来只会拉长锁持有时间。
func upsertBucket(gdb *gorm.DB, table string, row *Bucket) error {
	values := row.counterMap()
	assignments := make(map[string]interface{}, len(values)+1)
	for col, v := range values {
		if v == 0 {
			continue
		}
		assignments[col] = gorm.Expr(table+"."+col+" + ?", v)
	}
	assignments["updated_at"] = row.UpdatedAt

	return gdb.Table(table).Clauses(clause.OnConflict{
		Columns: []clause.Column{
			{Name: "bucket_ts"}, {Name: "group_name"}, {Name: "model_name"},
		},
		DoUpdates: clause.Assignments(assignments),
	}).Create(row).Error
}

// ─────────────────────────────── rollup ───────────────────────────────

// runRollup 把 5 分钟桶汇总成小时桶。
func runRollup(ctx context.Context) {
	gdb := db.Get()
	if gdb == nil {
		return
	}
	// 只汇总已经走完的整点:把半小时的数据固化成"一小时"会让曲线凭空塌陷。
	hours, ok := pendingRollupHours(gdb, alignHour(common.GetTimestamp()))
	if !ok || len(hours) == 0 {
		return
	}

	// 覆盖语义的赋值列清单:全部计数列 + updated_at。一次算好复用,
	// 免得每个整点都重建一遍同样的切片。
	assign := append(counterColumns(), "updated_at")
	for _, h := range hours {
		if ctx.Err() != nil {
			return // 租约已丢失,立刻停手,否则就是双跑
		}
		n, err := rollupHour(gdb, h, assign)
		if err != nil {
			db.MarkFailure(err)
			warnThrottled("rollup", err)
			return // 不推进游标,下一轮重跑;覆盖语义保证安全
		}
		statRollupRows.Add(n)
	}
	statRollupAt.Store(common.GetTimestamp())
}

// pendingRollupHours 列出本轮要汇总的整点,时间升序。
//
// 游标仍取自小时表的 MAX(bucket_ts) —— 不单独维护游标表,少一处需要与数据保持
// 一致的状态;回退 rollupBacktrackHours 个小时重跑,吸收迟到的 flush。
//
// ★ 但窗口必须由 5 分钟表里**真实存在的整点**决定,不能是"游标 + N 小时墙钟"。
// 两者叠加会死锁:5 分钟表一旦出现超过 (maxRollupHoursPerRun - rollupBacktrackHours)
// 小时的空档,窗口里就一行新数据都查不到 → 小时表 MAX 不变 → 下一轮算出同一个
// 窗口 → 游标永久卡死。线上实测的样子是小时表停在 1 行、5 分钟表已横跨 14 天、
// rollup 空跑了一千多轮;而前端「7 天 / 30 天」读的正是小时表(见 query.go 的
// useHourTable),5 分钟表到期清理后这段历史就永久没了。
//
// 改成直接问"游标之后哪些整点有数据",空档被一次跳过。成本上限没有放松:
// 每轮仍然最多 maxRollupHoursPerRun 条,只是这个数从"48 小时墙钟"变成
// "48 个有数据的整点",而且每条都保证真的会写出行,不再有空转。
func pendingRollupHours(gdb *gorm.DB, endHour int64) ([]int64, bool) {
	var latest int64
	if err := gdb.Table(hourTable).Select("COALESCE(MAX(bucket_ts), 0)").Scan(&latest).Error; err != nil {
		db.MarkFailure(err)
		warnThrottled("rollup_cursor", err)
		return nil, false
	}
	var from int64 // 小时表为空时从 5 分钟表最早的一行开始补齐
	if latest > 0 {
		from = alignHour(latest) - rollupBacktrackHours*3600
	}

	// bucket_ts - (bucket_ts % 3600) 是三个库通用的"对齐到整点"写法:
	// MySQL 的 `/` 返回小数、整除要用方言 DIV,而 `%` 三个库语义一致。
	var hours []int64
	err := gdb.Table(bucketTable).
		Select("bucket_ts - (bucket_ts % 3600) AS hour_ts").
		Where("bucket_ts >= ? AND bucket_ts < ?", from, endHour).
		Group("bucket_ts - (bucket_ts % 3600)").
		Order("hour_ts ASC").
		Limit(maxRollupHoursPerRun).
		Scan(&hours).Error
	if err != nil {
		db.MarkFailure(err)
		warnThrottled("rollup_cursor", err)
		return nil, false
	}
	return hours, true
}

// rollupHour 汇总单个整点。
//
// ★ 覆盖语义(冲突时整列赋值),不是累加 —— 这是与 flush 完全相反的一点,
// 也是本模块最容易写错的地方。源数据是完整的一小时,重跑必须得到同样的结果;
// 写成累加的话,每重跑一次数字就翻一倍。
//
// 刻意不用 `INSERT ... SELECT ... ON DUPLICATE KEY UPDATE`:那是 MySQL 方言,
// 违反"三库兼容"的硬约束,更要命的是单测跑在 SQLite 上,方言语句在测试里
// 一行都跑不到 —— rollup 恰恰是最难在生产上察觉出错的一段。改成
// 「聚合查询 + clause.OnConflict 批量写回」之后 GORM 按方言渲染冲突子句
// (MySQL 的 VALUES(col) / SQLite 与 PostgreSQL 的 excluded.col),
// 代价只是多一次往返,换来的是这条路径能被真正测到。
func rollupHour(gdb *gorm.DB, hourTs int64, assign []string) (int64, error) {
	var rows []Bucket
	err := gdb.Table(bucketTable).
		Select(selectSums("group_name, model_name")).
		Where("bucket_ts >= ? AND bucket_ts < ?", hourTs, hourTs+3600).
		Group("group_name, model_name").
		Find(&rows).Error
	if err != nil {
		return 0, err
	}
	if len(rows) == 0 {
		return 0, nil
	}

	now := common.GetTimestamp()
	for i := range rows {
		rows[i].BucketTs = hourTs
		rows[i].UpdatedAt = now
	}
	res := gdb.Table(hourTable).Clauses(clause.OnConflict{
		Columns: []clause.Column{
			{Name: "bucket_ts"}, {Name: "group_name"}, {Name: "model_name"},
		},
		DoUpdates: clause.AssignmentColumns(assign),
	}).CreateInBatches(rows, rollupUpsertBatch)
	return res.RowsAffected, res.Error
}

// ─────────────────────────────── cleanup ───────────────────────────────

func runCleanup(ctx context.Context) {
	gdb := db.Get()
	if gdb == nil {
		return
	}
	days := config.Get().Availability.RetentionDays
	if days <= 0 {
		days = defaultRetentionDay
	}
	hourDays := days * hourRetentionFactor
	if hourDays > maxHourRetentionDay {
		hourDays = maxHourRetentionDay
	}

	now := common.GetTimestamp()
	deleteBefore(ctx, gdb, bucketTable, now-int64(days)*86400)
	deleteBefore(ctx, gdb, hourTable, now-int64(hourDays)*86400)
	statCleanupAt.Store(now)
}

// deleteBefore 分批限速删除。
//
// 分批是硬要求:一条 DELETE 干掉上百万行会长时间持有行锁、撑爆 binlog,
// 顺带把同一个库上的其它扩展功能一起拖死。
// 分批用"先取一批主键、再按主键删"而不是 DELETE ... LIMIT:
// LIMIT 子句在 DELETE 上是 MySQL 专有扩展,PostgreSQL 直接语法错误。
// 两跳的代价是一次额外的索引扫描(bucket_ts 上有索引),换来的是同一段代码
// 在三种方言上行为一致 —— 与 modules/ticket/tasks.go 的处理同口径。
func deleteBefore(ctx context.Context, gdb *gorm.DB, table string, cutoff int64) {
	for i := 0; i < maxCleanupBatches; i++ {
		if ctx.Err() != nil {
			return
		}
		var ids []int64
		if err := gdb.WithContext(ctx).Table(table).
			Where("bucket_ts < ?", cutoff).
			Order("bucket_ts").
			Limit(cleanupBatchSize).
			Pluck("id", &ids).Error; err != nil {
			db.MarkFailure(err)
			warnThrottled("cleanup", err)
			return
		}
		if len(ids) == 0 {
			return
		}
		res := gdb.WithContext(ctx).Exec("DELETE FROM "+table+" WHERE id IN ?", ids)
		if res.Error != nil {
			db.MarkFailure(res.Error)
			warnThrottled("cleanup", res.Error)
			return
		}
		statCleanupRow.Add(res.RowsAffected)
		if int64(len(ids)) < cleanupBatchSize {
			return
		}
		time.Sleep(cleanupPause)
	}
}

// warnThrottled 按场景限频告警。
// 新库长时间不可用时,每分钟一条足以说明问题,逐次打印只会淹没日志。
var lastWarnAt sync.Map // string → *atomic.Int64

func warnThrottled(scene string, err error) {
	v, _ := lastWarnAt.LoadOrStore(scene, &atomic.Int64{})
	slot := v.(*atomic.Int64)
	now := common.GetTimestamp()
	last := slot.Load()
	if now-last < 60 {
		return
	}
	if !slot.CompareAndSwap(last, now) {
		return
	}
	common.SysError(fmt.Sprintf("qianye/availability: %s 失败: %v", scene, err))
}
