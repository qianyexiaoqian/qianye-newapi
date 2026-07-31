package availability

import (
	"context"
	"fmt"
	"strings"
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
	maxRollupHoursPerRun = 48
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
	endHour := alignHour(common.GetTimestamp())
	start, ok := rollupStart(gdb, endHour)
	if !ok {
		return
	}
	if end := start + maxRollupHoursPerRun*3600; end < endHour {
		endHour = end
	}

	cols := counterColumns()
	for h := start; h < endHour; h += 3600 {
		if ctx.Err() != nil {
			return // 租约已丢失,立刻停手,否则就是双跑
		}
		n, err := rollupHour(gdb, h, cols)
		if err != nil {
			db.MarkFailure(err)
			warnThrottled("rollup", err)
			return // 不推进游标,下一轮重跑;覆盖语义保证安全
		}
		statRollupRows.Add(n)
	}
	statRollupAt.Store(common.GetTimestamp())
}

// rollupStart 计算本轮起点。
//
// 不单独维护游标表:小时表自己的 MAX(bucket_ts) 就是游标,少一处需要与数据
// 保持一致的状态。回退 rollupBacktrackHours 个小时重跑,吸收迟到数据。
func rollupStart(gdb *gorm.DB, endHour int64) (int64, bool) {
	var latest int64
	if err := gdb.Table(hourTable).Select("COALESCE(MAX(bucket_ts), 0)").Scan(&latest).Error; err != nil {
		db.MarkFailure(err)
		warnThrottled("rollup_cursor", err)
		return 0, false
	}
	if latest > 0 {
		start := alignHour(latest) - rollupBacktrackHours*3600
		return start, start < endHour
	}

	// 小时表还是空的:从 5 分钟表最早的一行开始补齐。
	var earliest int64
	if err := gdb.Table(bucketTable).Select("COALESCE(MIN(bucket_ts), 0)").Scan(&earliest).Error; err != nil {
		db.MarkFailure(err)
		warnThrottled("rollup_cursor", err)
		return 0, false
	}
	if earliest == 0 {
		return 0, false
	}
	start := alignHour(earliest)
	return start, start < endHour
}

// rollupHour 汇总单个整点。
//
// ★ 覆盖语义(= VALUES(col)),不是累加 —— 这是与 flush 完全相反的一点,
// 也是本模块最容易写错的地方。源数据是完整的一小时,重跑必须得到同样的结果;
// 写成累加的话,每重跑一次数字就翻一倍。
func rollupHour(gdb *gorm.DB, hourTs int64, cols []string) (int64, error) {
	sums := make([]string, 0, len(cols))
	updates := make([]string, 0, len(cols)+1)
	for _, col := range cols {
		sums = append(sums, "SUM("+col+")")
		updates = append(updates, col+" = VALUES("+col+")")
	}
	updates = append(updates, "updated_at = VALUES(updated_at)")

	// SELECT 列表里的两个常量直接内联而不用占位符:它们是本地计算出的 int64,
	// 不存在注入面;而 INSERT ... SELECT 的投影位上放无类型占位符,
	// 在预编译语句下 MySQL 的类型推断并不稳定。
	sql := fmt.Sprintf(
		`INSERT INTO %s (bucket_ts, group_name, model_name, %s, updated_at)
		 SELECT %d, group_name, model_name, %s, %d
		 FROM %s WHERE bucket_ts >= ? AND bucket_ts < ?
		 GROUP BY group_name, model_name
		 ON DUPLICATE KEY UPDATE %s`,
		hourTable, strings.Join(cols, ", "),
		hourTs, strings.Join(sums, ", "), common.GetTimestamp(),
		bucketTable, strings.Join(updates, ", "))

	res := gdb.Exec(sql, hourTs, hourTs+3600)
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
func deleteBefore(ctx context.Context, gdb *gorm.DB, table string, cutoff int64) {
	for i := 0; i < maxCleanupBatches; i++ {
		if ctx.Err() != nil {
			return
		}
		res := gdb.Exec("DELETE FROM "+table+" WHERE bucket_ts < ? LIMIT ?", cutoff, cleanupBatchSize)
		if res.Error != nil {
			db.MarkFailure(res.Error)
			warnThrottled("cleanup", res.Error)
			return
		}
		statCleanupRow.Add(res.RowsAffected)
		if res.RowsAffected < cleanupBatchSize {
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
