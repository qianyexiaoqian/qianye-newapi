package lottery

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/qianye/config"
	"github.com/QuantumNous/new-api/qianye/db"
	qymodel "github.com/QuantumNous/new-api/qianye/model"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// spend.go —— "近 N 日消费"的数据源。
//
// # 为什么必须预聚合,不能实时查 logs
//
// logs 有两个硬约束:
//   - 它可能在独立日志库甚至 ClickHouse 上(model.QyLogDBSharesMainDB 就是
//     为此存在),无法与 users JOIN;
//   - 它的索引是 (user_id, id) 与 (created_at, type),**没有
//     (user_id, created_at) 复合索引** —— 几百万行上按"某用户近 30 天
//     type=2 的 SUM(quota)"会退化成按 user_id 取一大段再过滤。
//
// 而报名是用户点了按钮在等的路径。点查 + 短 TTL 缓存在重度用户上依然是
// 几万行扫描,不可接受。
//
// # 双通道
//
//	通道 A 增量(runSpendScan):按主键低水位前扫 logs(与
//	  commission/topup_scan.go 同形状),便宜、近实时,但只在有自增 id 的
//	  部署上成立。
//	通道 B 重算(runSpendRecalc):对已收口的自然日整日聚合,SET 语义、
//	  完全幂等、全引擎适用。它修掉通道 A 任何可能的跳行,也是 ClickHouse
//	  部署下的唯一通道(那里 Log.Id 恒为 0)。
//
// # 误差方向
//
// 增量通道的延迟与任何跳行都只会**少算**消费 → 只会让符合条件的人晚一点
// 符合,不会让不符合的人混进来。方向安全,管理端表单注明。

// spendCursorKey 是低水位游标在共享 qy_kv 表里的键。
const spendCursorKey = "lottery.spend_low_water"

// spendReadyKey 记录日桶数据从哪一天(YYYYMMDD)起是可信的。
//
// 冷启动时表为空,"近 30 日消费"会全员误拒。活动创建时若条件窗口早于这一天,
// 直接 400 不让保存 —— 把误拒挡在配置阶段,而不是等到用户报名时。
const spendReadyKey = "lottery.spend_ready_from"

// spendRecalcCursorKey 记录重算道已经确认到哪一天。
const spendRecalcCursorKey = "lottery.spend_recalc_day"

// spendReadyPendingKey 记录本次回填是从哪一天开始的。
//
// 它与 spendReadyKey 分开:后者的语义是"日桶从这天起已经可信",在回填追平
// 今天之前谁都不该看到它。
const spendReadyPendingKey = "lottery.spend_ready_pending"

// dayBucket 把 unix 秒换算成 YYYYMMDD。
//
// 用本地时区而不是 UTC:运营说的"近 7 天"是他所在时区的自然日,
// 这个口径必须与管理端界面上显示的日期一致。
func dayBucket(ts int64) int32 {
	t := time.Unix(ts, 0)
	return int32(t.Year()*10000 + int(t.Month())*100 + t.Day())
}

// dayRange 返回某个 YYYYMMDD 的 [起, 止) unix 秒区间。
func dayRange(day int32) (int64, int64) {
	y := int(day) / 10000
	m := (int(day) / 100) % 100
	d := int(day) % 100
	start := time.Date(y, time.Month(m), d, 0, 0, 0, 0, time.Local)
	return start.Unix(), start.AddDate(0, 0, 1).Unix()
}

// RecentSpend 读近 days 天(含今天)的消费额度合计。
//
// 这就是主键上的一段范围扫描,最多 days 行,索引覆盖。
// **本函数是报名路径唯一允许的消费查询入口** —— 绝不查 logs。
func RecentSpend(ctx context.Context, userId int, days int) (int64, error) {
	if days <= 0 {
		return 0, nil
	}
	gdb := db.Get()
	if gdb == nil {
		return 0, db.ErrNotReady
	}
	from := dayBucket(common.GetTimestamp() - int64(days-1)*86400)

	var sum struct{ Total int64 }
	err := gdb.WithContext(ctx).Model(&SpendDaily{}).
		Select("COALESCE(SUM(quota), 0) AS total").
		Where("user_id = ? AND day >= ?", userId, from).
		Scan(&sum).Error
	if err != nil {
		db.MarkFailure(err)
		return 0, err
	}
	return sum.Total, nil
}

// SpendReadyFrom 返回日桶数据可信的起始日(YYYYMMDD),0 表示尚未回填过。
func SpendReadyFrom() int32 {
	gdb := db.Get()
	if gdb == nil {
		return 0
	}
	var kv qymodel.KV
	if err := gdb.Where("k = ?", spendReadyKey).Take(&kv).Error; err != nil {
		return 0
	}
	v, err := strconv.ParseInt(kv.V, 10, 32)
	if err != nil {
		return 0
	}
	return int32(v)
}

// ─────────────────────────── 通道 A:增量 ───────────────────────────

// spendKey 是日桶的聚合键。
type spendKey struct {
	UserId int
	Day    int32
}

// runSpendScan 按主键低水位前扫 logs 并累进日桶。
//
// ClickHouse 部署下 Log.Id 恒为 0(没有自增),扫描不成立 —— 那时本通道
// 直接返回,由通道 B 承担全部工作。
func runSpendScan(ctx context.Context) {
	cfg := config.Get().Lottery
	if !cfg.Enabled || common.UsingLogDatabase(common.DatabaseTypeClickHouse) {
		return
	}
	gdb := db.Get()
	if gdb == nil {
		return
	}
	// 句柄一次性绑上租约的预算,后续语句与被传入的子函数都带着它。
	gdb = gdb.WithContext(ctx)

	low, err := loadInt64KV(ctx, gdb, spendCursorKey)
	if err != nil {
		common.SysError("qianye/lottery: 读取消费扫描游标失败: " + err.Error())
		return
	}
	// 时间护栏:自增 id 在插入时分配、提交可能更晚,裸 `id > watermark`
	// 会跳过后提交的小 id 行。只处理已经"老到不可能还有更小 id 在途"的行。
	guardTs := common.GetTimestamp() - int64(cfg.SpendGapGuardSeconds)

	batch := cfg.SpendScanBatch
	if batch <= 0 {
		batch = 2000
	}
	// 单轮最多 10 批,避免一次占用日志库太久;剩下的下个周期继续。
	for round := 0; round < 10; round++ {
		if ctx.Err() != nil {
			return
		}
		rows := make([]model.Log, 0, batch)
		err := model.QyLogDB().WithContext(ctx).
			Where("id > ? AND type = ? AND created_at <= ?", low, model.LogTypeConsume, guardTs).
			Order("id asc").Limit(batch).Find(&rows).Error
		if err != nil {
			common.SysError("qianye/lottery: 扫描消费日志失败: " + err.Error())
			return
		}
		if len(rows) == 0 {
			return
		}

		agg := make(map[spendKey]*SpendDaily, len(rows))
		var maxId int64
		for i := range rows {
			r := &rows[i]
			if int64(r.Id) > maxId {
				maxId = int64(r.Id)
			}
			if r.Quota <= 0 || r.UserId <= 0 {
				continue
			}
			k := spendKey{UserId: r.UserId, Day: dayBucket(r.CreatedAt)}
			cur, ok := agg[k]
			if !ok {
				cur = &SpendDaily{UserId: k.UserId, Day: k.Day}
				agg[k] = cur
			}
			cur.Quota += int64(r.Quota)
			cur.Cnt++
		}

		if err := applySpendDelta(ctx, gdb, agg); err != nil {
			// 游标不前进:下一轮把这一批连同后面的一起重扫。少算是安全方向,
			// 双计不是 —— 所以宁可停在原地。
			common.SysError("qianye/lottery: 累进消费日桶失败,游标不前进: " + err.Error())
			return
		}
		if maxId <= low {
			return
		}
		if err := saveInt64KV(ctx, gdb, spendCursorKey, maxId); err != nil {
			common.SysError("qianye/lottery: 写入消费扫描游标失败: " + err.Error())
			return
		}
		low = maxId
		if len(rows) < batch {
			return
		}
	}
}

// applySpendDelta 把一批增量累加进日桶。
//
// 用数据库端的加法而不是 SELECT-改-写:后者在多节点下会互相覆盖。
// 已被重算道确认(final = true)的日桶不再接受增量 —— 重算是权威,
// 再往上加就是双计。
//
// # 为什么是"先 UPDATE 再插入"而不是 ON CONFLICT ... WHERE
//
// 扩展库只支持 MySQL,而 gorm.io/driver/mysql 的 ON CONFLICT 构造器**整段丢弃
// Where 子句**(它只会输出 ON DUPLICATE KEY UPDATE)。写成那样,"不往 final
// 桶上累加"这道防护在生产上恒为空操作,冷启动时重算道刚确认过的历史日桶会被
// 增量道再加一遍 —— 近 N 日消费变成实际值的两倍,方向恰好与设计声称的
// "只会少算"相反,直接变成消费门槛被腰斩。SQLite(测试用)支持带 Where 的
// ON CONFLICT,所以这个差异在测试里根本看不见。
//
// 两步写法在每种数据库上语义一致:UPDATE 只命中 final=false 的行;没命中说明
// 这一天要么还没有行(插入),要么已经 final(插入撞唯一键,DoNothing 跳过)。
func applySpendDelta(ctx context.Context, gdb *gorm.DB, agg map[spendKey]*SpendDaily) error {
	if len(agg) == 0 {
		return nil
	}
	now := common.GetTimestamp()
	err := gdb.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		for k, v := range agg {
			res := tx.Model(&SpendDaily{}).
				Where("user_id = ? AND day = ? AND final = ?", k.UserId, k.Day, false).
				Updates(map[string]any{
					"quota":      gorm.Expr("quota + ?", v.Quota),
					"cnt":        gorm.Expr("cnt + ?", v.Cnt),
					"updated_at": now,
				})
			if res.Error != nil {
				return res.Error
			}
			if res.RowsAffected > 0 {
				continue
			}
			row := SpendDaily{UserId: k.UserId, Day: k.Day, Quota: v.Quota, Cnt: v.Cnt, UpdatedAt: now}
			if err := tx.Clauses(clause.OnConflict{DoNothing: true}).Create(&row).Error; err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		db.MarkFailure(err)
	}
	return err
}

// ─────────────────────────── 通道 B:重算 ───────────────────────────

// runSpendRecalc 对已收口的自然日整日重算,SET 语义、完全幂等。
//
// 它是权威通道:修掉增量道任何可能的跳行,并且在 ClickHouse 上是唯一可行的
// 形态(那里没有自增 id,但按天分组聚合恰恰是它最擅长的操作)。
//
// 同时负责写 spend_ready_from —— 第一次成功重算的那一天就是日桶可信的起点。
func runSpendRecalc(ctx context.Context) {
	cfg := config.Get().Lottery
	if !cfg.Enabled {
		return
	}
	gdb := db.Get()
	if gdb == nil {
		return
	}
	// 句柄一次性绑上租约的预算,后续语句与被传入的子函数都带着它。
	gdb = gdb.WithContext(ctx)

	today := dayBucket(common.GetTimestamp())
	lookback := cfg.SpendMaxLookbackDays
	if lookback <= 0 {
		lookback = 90
	}
	earliest := dayBucket(common.GetTimestamp() - int64(lookback)*86400)

	cursor, err := loadInt64KV(ctx, gdb, spendRecalcCursorKey)
	if err != nil {
		common.SysError("qianye/lottery: 读取消费重算游标失败: " + err.Error())
		return
	}
	from := int32(cursor)
	if from < earliest {
		from = earliest
	}
	// 记住本次回填的起点。spend_ready_from 只能等**整个窗口都补完**才写:
	// 在处理第一天时就写上,会让 checkSpendWindow 立刻放行"近 30 日消费"这类
	// 条件,而那时 today-89..today-1 的日桶全是空的 —— 每一个真正符合条件的
	// 用户都会被拒,而且他看到的提示是"你的近期消费不达标",完全无从判断
	// 问题出在平台侧。宁可多等一两个小时不让办这类活动。
	if SpendReadyFrom() == 0 {
		pending, err := loadInt64KV(ctx, gdb, spendReadyPendingKey)
		if err != nil {
			common.SysError("qianye/lottery: 读取消费回填起点失败: " + err.Error())
			return
		}
		if pending == 0 {
			if err := saveInt64KV(ctx, gdb, spendReadyPendingKey, int64(from)); err != nil {
				common.SysError("qianye/lottery: 写入消费回填起点失败: " + err.Error())
				return
			}
		}
	}

	// 每轮最多补 7 个自然日:冷启动时慢慢爬,稳态下只有昨天一天要算。
	day := from
	for n := 0; day < today && n < 7; n++ {
		if ctx.Err() != nil {
			return
		}
		if err := recalcSpendDay(ctx, gdb, day); err != nil {
			common.SysError(fmt.Sprintf("qianye/lottery: 重算 %d 的消费日桶失败: %v", day, err))
			return
		}
		if err := saveInt64KV(ctx, gdb, spendRecalcCursorKey, int64(day)+1); err != nil {
			common.SysError("qianye/lottery: 写入消费重算游标失败: " + err.Error())
			return
		}
		// 游标按自然日推进,不能简单 +1(月末会跳到不存在的日期)。
		start, _ := dayRange(day)
		day = dayBucket(start + 86400 + 3600)
	}

	// 追平到今天,回填才算完成。此刻起"近 N 日消费"这道门槛才有可信的数据。
	if day < today || SpendReadyFrom() != 0 {
		return
	}
	ready, err := loadInt64KV(ctx, gdb, spendReadyPendingKey)
	if err != nil || ready == 0 {
		return
	}
	if err := saveInt64KV(ctx, gdb, spendReadyKey, ready); err != nil {
		common.SysError("qianye/lottery: 写入消费可信起始日失败: " + err.Error())
	}
}

// recalcSpendDay 用一整天的聚合覆盖该日的全部日桶。
//
// DELETE + 批量 INSERT 而不是 UPSERT:SET 语义要求"这一天多出来的行也要消失",
// 而 UPSERT 只会改写还存在的键。整个操作在一个扩展库事务里,天然原子。
func recalcSpendDay(ctx context.Context, gdb *gorm.DB, day int32) error {
	start, end := dayRange(day)

	type agg struct {
		UserId int
		Total  int64
		Cnt    int
	}
	rows := make([]agg, 0, 512)
	err := model.QyLogDB().WithContext(ctx).Model(&model.Log{}).
		Select("user_id, SUM(quota) AS total, COUNT(*) AS cnt").
		Where("type = ? AND created_at >= ? AND created_at < ?", model.LogTypeConsume, start, end).
		Group("user_id").Scan(&rows).Error
	if err != nil {
		return err
	}

	now := common.GetTimestamp()
	err = gdb.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if err := tx.Where("day = ?", day).Delete(&SpendDaily{}).Error; err != nil {
			return err
		}
		buckets := make([]SpendDaily, 0, len(rows))
		for _, r := range rows {
			if r.UserId <= 0 || r.Total <= 0 {
				continue
			}
			buckets = append(buckets, SpendDaily{
				UserId: r.UserId, Day: day, Quota: r.Total, Cnt: r.Cnt,
				Final: true, UpdatedAt: now,
			})
		}
		if len(buckets) == 0 {
			return nil
		}
		return tx.CreateInBatches(&buckets, 500).Error
	})
	if err != nil {
		db.MarkFailure(err)
	}
	return err
}

// pruneSpendDaily 清理超出保留期的日桶。
//
// **这是本模块唯一允许的物理删除**:日桶是可从 logs 重建的派生数据,不是证据。
// 七张证据表(activity/seed/prize/option/entry/payout/event)显式排除在外,
// 由 retention_guard_test.go 的 AST 断言守住。
func pruneSpendDaily(ctx context.Context) {
	cfg := config.Get().Lottery
	if !cfg.Enabled || cfg.SpendRetentionDays <= 0 {
		return
	}
	gdb := db.Get()
	if gdb == nil {
		return
	}
	// 句柄一次性绑上租约的预算,后续语句与被传入的子函数都带着它。
	gdb = gdb.WithContext(ctx)
	cutoff := dayBucket(common.GetTimestamp() - int64(cfg.SpendRetentionDays)*86400)
	if err := gdb.WithContext(ctx).Where("day < ?", cutoff).Delete(&SpendDaily{}).Error; err != nil {
		db.MarkFailure(err)
		common.SysError("qianye/lottery: 清理消费日桶失败: " + err.Error())
	}
}

// ─────────────────────────── 游标读写 ───────────────────────────

func loadInt64KV(ctx context.Context, gdb *gorm.DB, key string) (int64, error) {
	var kv qymodel.KV
	err := gdb.WithContext(ctx).Where("k = ?", key).Take(&kv).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return 0, nil
	}
	if err != nil {
		db.MarkFailure(err)
		return 0, err
	}
	v, err := strconv.ParseInt(kv.V, 10, 64)
	if err != nil {
		// 游标损坏时报错而不是从 0 重来:从 0 重扫会把日桶再加一遍(双计),
		// 那比停下来严重得多。
		return 0, fmt.Errorf("qianye/lottery: 游标 %s 的值 %q 不是整数", key, kv.V)
	}
	return v, nil
}

func saveInt64KV(ctx context.Context, gdb *gorm.DB, key string, v int64) error {
	row := qymodel.KV{K: key, V: strconv.FormatInt(v, 10), UpdatedAt: common.GetTimestamp()}
	err := gdb.WithContext(ctx).Clauses(clause.OnConflict{
		Columns:   []clause.Column{{Name: "k"}},
		DoUpdates: clause.AssignmentColumns([]string{"v", "updated_at"}),
	}).Create(&row).Error
	if err != nil {
		db.MarkFailure(err)
	}
	return err
}
