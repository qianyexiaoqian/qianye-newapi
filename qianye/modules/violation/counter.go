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
	// Crossed 表示本次推进恰好跨过了封号阈值。
	// 注意是"跨越"而不是"大于等于":后者会让阈值之后的每一次违规都尝试封号。
	Crossed bool
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

	st.Crossed = crossedThreshold(st.HitCount, weight, cfg.AutoBanThreshold)
	return st, nil
}

// crossedThreshold 判断本次推进是否"恰好跨过"阈值。
//
// 判据是跨越(推进前 < 阈值 且 推进后 >= 阈值)而不是"达到"(推进后 >= 阈值):
// 后者会让阈值之后的每一次违规都去尝试封号。配合 bumpCounter 保证的
// "每个并发 worker 拿到唯一的推进后计数",跨越者必然有且只有一个。
func crossedThreshold(after, weight, threshold int) bool {
	if threshold <= 0 || weight <= 0 {
		return false
	}
	return after >= threshold && after-weight < threshold
}

// claimBan 尝试认领一次封号。
//
// (user_id, ban_cycle) 唯一索引就是分布式互斥锁:一个封禁周期内只可能有一个
// 节点插入成功。返回 nil 表示别人已经认领,本节点必须什么都不做。
func claimBan(ctx context.Context, userId, cycle, hitCount int, recordId int64) (*Ban, error) {
	gdb := db.Get()
	if gdb == nil {
		return nil, db.ErrNotReady
	}
	row := &Ban{
		UserId:          userId,
		BanCycle:        cycle,
		TriggerRecordId: recordId,
		HitCountAt:      hitCount,
		Threshold:       config.Get().Violation.AutoBanThreshold,
		Status:          BanPending,
		CreatedAt:       common.GetTimestamp(),
	}
	res := gdb.WithContext(ctx).Clauses(clause.OnConflict{DoNothing: true}).Create(row)
	if res.Error != nil {
		db.MarkFailure(res.Error)
		return nil, res.Error
	}
	if res.RowsAffected == 0 {
		return nil, nil // 已被其他节点认领
	}
	return row, nil
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

// maybeAutoBan 在计数跨越阈值时执行封号。返回是否真的封了。
func maybeAutoBan(ctx context.Context, rec *Record, st counterState) bool {
	cfg := config.Get().Violation
	if cfg.AutoBanThreshold <= 0 || !st.Crossed {
		return false
	}
	if shadow, reason := shadowActive(); shadow {
		shadowHits.Add(1)
		common.SysLog(fmt.Sprintf(
			"qianye/violation: 影子模式(%s),用户 %d 违规计数已达 %d,未执行自动封号",
			reason, rec.UserId, st.HitCount))
		return false
	}
	// 速率闸在认领之前:超限时连认领都不做,这样恢复后仍能正常触发,
	// 而不是留下一堆 pending 的假认领把后续周期堵死。
	if banRateExceeded() {
		common.SysError(fmt.Sprintf(
			"qianye/violation: 每小时自动封号已达上限,用户 %d 的封号被推迟为人工处理", rec.UserId))
		return false
	}

	ban, err := claimBan(ctx, rec.UserId, st.BanCycle, st.HitCount, rec.Id)
	if err != nil || ban == nil {
		return false
	}
	noteBan()

	if err := disableUserForViolation(rec.UserId, ban); err != nil {
		if errors.Is(err, errBanSkipped) {
			markBan(ban.Id, BanSkipped, "")
			return false
		}
		markBan(ban.Id, BanFailed, err.Error())
		common.SysError(fmt.Sprintf("qianye/violation: 用户 %d 自动封禁失败: %v", rec.UserId, err))
		return false
	}
	markBan(ban.Id, BanBanned, "")
	return true
}

// markBan 更新封禁执行结果。失败时只记日志:封禁本身已经生效,
// 状态回写失败会被补偿任务收敛。
func markBan(id int64, status, lastErr string) {
	gdb := db.Get()
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
