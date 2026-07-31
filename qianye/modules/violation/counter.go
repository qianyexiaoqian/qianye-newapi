package violation

import (
	"context"
	"errors"
	"fmt"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/qianye/config"
	"github.com/QuantumNous/new-api/qianye/db"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// counterState 是一次计数推进后的结果。
type counterState struct {
	HitCount int
	BanCycle int
	// Reached 表示推进后的计数已经达到封号阈值。
	//
	// 刻意用"已达"而不是"恰好跨越":跨越是一个只出现一次的瞬时信号,一旦被
	// 速率闸、影子模式或一次执行失败消费掉,下一次违规的 after-weight 就已经
	// 越过阈值了,判据永远为假 —— 该用户在整个滚动窗口(默认 24 小时)内再也
	// 不会被封号,而补偿任务只扫已存在的封禁行,对"从未认领成功"的跨越无能为力。
	// 改成"已达"之后,判据完全由持久化的 hit_count 推导,不再是一次性的:
	// 阻碍解除后的下一次违规会重新走到封号判定。
	// 重复封号由 (user_id, ban_cycle) 唯一索引兜住,代价只是一次冲突插入。
	Reached bool
}

// bumpCounter 原子地推进用户的滚动窗口计数。
//
// 并发正确性是这个函数存在的全部理由:多节点同时把计数推过阈值时,
// 必须保证只有一个节点观察到"跨越",否则会重复封号、重复告警。
//
// 实现要点:
//   - INSERT ... ON DUPLICATE KEY UPDATE 是单条原子语句,窗口过期判断与重置
//     都在这条语句里完成,不存在"读到过期窗口再写"的竞态;
//   - 紧随其后的 SELECT 在同一个事务里执行。upsert 已经对该行加了排他锁并持有到
//     提交,因此这次读到的必然是本次推进的结果,而不会读到别人已经推进过的值。
//     (刻意不用 LAST_INSERT_ID():它是会话级变量,GORM 连接池会把 Exec 与 Raw
//     发到不同连接上,跨连接读到的是别人的值 —— 这是最隐蔽的一类 bug。)
func bumpCounter(ctx context.Context, userId, weight int) (counterState, error) {
	var st counterState
	if weight <= 0 {
		return st, nil
	}
	gdb := db.Get()
	if gdb == nil {
		return st, db.ErrNotReady
	}

	cfg := config.Get().Violation
	windowHours := cfg.AutoBanWindowHours
	if windowHours <= 0 {
		windowHours = 24
	}
	now := common.GetTimestamp()
	winFrom := now - int64(windowHours)*3600

	err := gdb.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if err := tx.Exec(`INSERT INTO qy_violation_counter
			(user_id, window_start, hit_count, total_count, ban_cycle, last_hit_at, updated_at)
			VALUES (?, ?, ?, ?, 0, ?, ?)
			ON DUPLICATE KEY UPDATE
				hit_count    = IF(window_start < ?, ?, hit_count + ?),
				window_start = IF(window_start < ?, ?, window_start),
				total_count  = total_count + ?,
				last_hit_at  = ?,
				updated_at   = ?`,
			userId, now, weight, weight, now, now,
			winFrom, weight, weight,
			winFrom, now,
			weight, now, now).Error; err != nil {
			return err
		}
		var row Counter
		if err := tx.Where("user_id = ?", userId).Take(&row).Error; err != nil {
			return err
		}
		st.HitCount = row.HitCount
		st.BanCycle = row.BanCycle
		return nil
	})
	if err != nil {
		db.MarkFailure(err)
		return counterState{}, err
	}

	st.Reached = reachedThreshold(st.HitCount, cfg.AutoBanThreshold)
	return st, nil
}

// reachedThreshold 判断计数是否已经达到封号阈值。阈值 <= 0 表示关闭自动封号。
func reachedThreshold(after, threshold int) bool {
	return threshold > 0 && after >= threshold
}

// claimBan 尝试认领一次封号。
//
// (user_id, ban_cycle) 唯一索引就是分布式互斥锁:一个封禁周期内只可能有一个
// 节点插入成功。created == false 表示本周期已被认领,此时返回库里那一行 ——
// 调用方需要看它的状态才能判断"是已有结论"还是"被速率闸推迟、现在可以提升执行"。
func claimBan(ctx context.Context, gdb *gorm.DB, userId, cycle, hitCount int, recordId int64, status string) (*Ban, bool, error) {
	if gdb == nil {
		return nil, false, db.ErrNotReady
	}
	row := &Ban{
		UserId:          userId,
		BanCycle:        cycle,
		TriggerRecordId: recordId,
		HitCountAt:      hitCount,
		Threshold:       config.Get().Violation.AutoBanThreshold,
		Status:          status,
		CreatedAt:       common.GetTimestamp(),
	}
	res := gdb.WithContext(ctx).Clauses(clause.OnConflict{DoNothing: true}).Create(row)
	if res.Error != nil {
		db.MarkFailure(res.Error)
		return nil, false, res.Error
	}
	if res.RowsAffected == 1 {
		return row, true, nil
	}
	var existing Ban
	if err := gdb.WithContext(ctx).
		Where("user_id = ? AND ban_cycle = ?", userId, cycle).Take(&existing).Error; err != nil {
		db.MarkFailure(err)
		return nil, false, err
	}
	return &existing, false, nil
}

// revertCounter 在管理员撤销违规记录时回退计数。
//
// 带 window_start 条件:窗口已经滚动过就不回退 —— 那个计数值已经失效,
// 强行减会把当前窗口的合法计数扣掉,反而放过真正的违规用户。
func revertCounter(userId, weight int, windowStart int64) error {
	if weight <= 0 {
		return nil
	}
	gdb := db.Get()
	if gdb == nil {
		return db.ErrNotReady
	}
	return gdb.Exec(`UPDATE qy_violation_counter
		SET hit_count = GREATEST(hit_count - ?, 0),
		    total_count = GREATEST(total_count - ?, 0),
		    updated_at = ?
		WHERE user_id = ? AND window_start = ?`,
		weight, weight, common.GetTimestamp(), userId, windowStart).Error
}

// openNewBanCycle 在解封时把周期 +1。
//
// 不 +1 的后果:下次达到阈值时 claimBan 的唯一键必然冲突,自动封号从此
// 对该用户静默失效。这是本模块最隐蔽的失效模式,必须与解封绑定执行。
//
// resetCount 的语义在封号判据改成"已达阈值"之后变得更实在了:不清零就意味着
// 这些次数仍然算数,该用户解封后只要再违规一次就会立刻被重新封禁。
// 想给一次真正的重新开始,解封时必须勾上 reset_counter。
// (旧的"恰好跨越"判据下不清零等于白留 —— 计数摆在那里却永远不会再触发封号,
// 那正是 B3 要消除的静默失效。)
func openNewBanCycle(userId int, resetCount bool) error {
	gdb := db.Get()
	if gdb == nil {
		return db.ErrNotReady
	}
	sets := "ban_cycle = ban_cycle + 1, updated_at = ?"
	args := []any{common.GetTimestamp()}
	if resetCount {
		sets += ", hit_count = 0"
	}
	return gdb.Exec(fmt.Sprintf("UPDATE qy_violation_counter SET %s WHERE user_id = ?", sets),
		append(args, userId)...).Error
}

// resolveBanClaim 决定本次是否要执行封号,并把这个决定持久化。
//
// 返回非 nil 表示本节点拿到了执行权,调用方必须紧接着执行主库封号。返回 nil 的
// 每一种情况都在库里或日志里留了痕:绝不允许"该封没封"只活在一次函数调用里。
//
// 三条分支的取舍:
//   - 影子模式:一行封禁记录都不写。影子的定义就是"只观察、不产生任何处置副作用",
//     写认领行会污染管理端的封禁列表。信号不会因此丢失 —— 判据是持久化的 hit_count,
//     影子解除后该用户的下一次违规会重新走到这里。
//   - 速率闸:直接以 deferred 状态落行(而不是"先落 pending 再改状态"),
//     进程在两步之间崩溃会留下一行会被补偿任务执行的 pending,那等于绕过速率闸。
//   - 已存在的行:只有 deferred 可以被提升。pending / failed 是补偿任务的地盘,
//     banned / skipped / unbanned 是已经有结论的终态。
func resolveBanClaim(ctx context.Context, gdb *gorm.DB, rec *Record, st counterState) *Ban {
	if gdb == nil || !st.Reached {
		return nil
	}
	if shadow, reason := shadowActive(); shadow {
		shadowHits.Add(1)
		common.SysLog(fmt.Sprintf(
			"qianye/violation: 影子模式(%s),用户 %d 违规计数已达 %d,未执行自动封号",
			reason, rec.UserId, st.HitCount))
		return nil
	}

	rateExceeded := banRateExceeded()
	status := BanPending
	if rateExceeded {
		status = BanDeferred
	}
	ban, created, err := claimBan(ctx, gdb, rec.UserId, st.BanCycle, st.HitCount, rec.Id, status)
	if err != nil || ban == nil {
		return nil
	}
	if rateExceeded {
		if created {
			common.SysError(fmt.Sprintf(
				"qianye/violation: 每小时自动封号已达上限,用户 %d 的封号已记为 deferred(ban=%d)待人工处理",
				rec.UserId, ban.Id))
		}
		return nil
	}
	if !created {
		if ban.Status != BanDeferred {
			return nil
		}
		// deferred → pending 的 CAS 是这条提升路径唯一的互斥手段。
		res := gdb.WithContext(ctx).Model(&Ban{}).
			Where("id = ? AND status = ?", ban.Id, BanDeferred).
			Update("status", BanPending)
		if res.Error != nil {
			db.MarkFailure(res.Error)
			return nil
		}
		if res.RowsAffected == 0 {
			return nil
		}
		ban.Status = BanPending
	}
	noteBan()
	return ban
}

// maybeAutoBan 在计数达到阈值时执行封号。返回是否真的封了。
func maybeAutoBan(ctx context.Context, gdb *gorm.DB, rec *Record, st counterState) bool {
	ban := resolveBanClaim(ctx, gdb, rec, st)
	if ban == nil {
		return false
	}
	if err := disableUserForViolation(ctx, rec.UserId, ban); err != nil {
		if errors.Is(err, errBanSkipped) {
			markBan(gdb, ban.Id, BanSkipped, "")
			return false
		}
		markBan(gdb, ban.Id, BanFailed, err.Error())
		common.SysError(fmt.Sprintf("qianye/violation: 用户 %d 自动封禁失败: %v", rec.UserId, err))
		return false
	}
	markBan(gdb, ban.Id, BanBanned, "")
	return true
}

// markBan 更新封禁执行结果。失败时只记日志:封禁本身已经生效,
// 状态回写失败会被补偿任务收敛。
func markBan(gdb *gorm.DB, id int64, status, lastErr string) {
	if gdb == nil {
		return
	}
	updates := map[string]any{"status": status, "last_error": truncate(lastErr, 512)}
	if status == BanBanned {
		updates["banned_at"] = common.GetTimestamp()
	}
	if err := gdb.Model(&Ban{}).Where("id = ?", id).Updates(updates).Error; err != nil {
		db.MarkFailure(err)
		common.SysError(fmt.Sprintf("qianye/violation: 回写封禁状态失败(id=%d): %v", id, err))
	}
}
