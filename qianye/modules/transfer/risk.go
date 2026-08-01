package transfer

import (
	"github.com/QuantumNous/new-api/qianye/config"
	"github.com/QuantumNous/new-api/qianye/db"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// reserveRisk 在扩展库事务内串行化风控判定并预占额度。
//
// 调用点固定在 twophase 的 LocalDetail 回调里,也就是与"落 pending 资金单 +
// 插入业务明细"同一个事务。这个绑定不能拆开:限额判定与计数写入之间只要存在
// 提交间隙,并发请求就会同时读到旧计数、同时通过校验。
//
// 两行状态记录必须按 user_id 升序加锁 —— A→B 与 B→A 同时发起时,
// 反序加锁会在扩展库里形成死锁环。
//
// cfg 由 create() 在受理时取一次快照后传进来,这里**绝不自己再读一次配置**。
// 门槛已经可以被管理端在线改(见 settings.go):受理与锁内各读各的时,
// 运营在这中间保存一次就会出现"受理放行、锁内拒绝",用户白吃一次冷却与
// 风控预占;方向相反时更糟 —— 用户看到的是旧门槛,实际按新门槛放行了。
func reserveRisk(tx *gorm.DB, req acceptedRequest, cfg config.Transfer, now int64) error {
	first, second := req.FromUserId, req.ToUserId
	if first > second {
		first, second = second, first
	}
	// 行可能还不存在(用户首次参与划转)。先按升序补齐,再按同样的顺序加锁。
	seed := []UserState{{UserId: first, UpdatedAt: now}, {UserId: second, UpdatedAt: now}}
	if err := tx.Clauses(clause.OnConflict{DoNothing: true}).Create(&seed).Error; err != nil {
		return err
	}

	var s1, s2 UserState
	if err := db.LockForUpdate(tx).Where("user_id = ?", first).First(&s1).Error; err != nil {
		return err
	}
	if err := db.LockForUpdate(tx).Where("user_id = ?", second).First(&s2).Error; err != nil {
		return err
	}
	sender, receiver := &s1, &s2
	if sender.UserId != req.FromUserId {
		sender, receiver = &s2, &s1
	}

	bucket := dayBucket(now)
	rollDay(sender, bucket)
	rollDay(receiver, bucket)

	// 收款方的入账笔数闸门也在这里判:两行都已在本事务内按升序加锁,
	// 判定放在 applyReservation 之前就自动串行化了,不需要额外加锁。
	if err := evaluateRisk(*sender, *receiver, cfg, req.Total, now); err != nil {
		return err
	}

	applyReservation(sender, receiver, req.Amount, req.Total, now, bucket)
	if err := saveState(tx, sender); err != nil {
		return err
	}
	return saveState(tx, receiver)
}

// evaluateRisk 判定本次划转是否被风控拦截:发起方四项 + 收款方入账笔数。
//
// 纯函数,不碰数据库:限额判定是本模块最容易写错的一段(边界差一、
// 配置 0 表示不限、跨日重置),必须能被直接测到。
//
// 两侧都要判是硬性要求:发起方限额只能约束"一个账号能转出多少",
// 对"200 个小号各转一笔到同一个汇集账号"完全无效 —— 每一笔都在各自的
// 发起方额度内,而这正是洗号/套现要走的形状。收款方的闸门是唯一能拦住它的。
func evaluateRisk(sender, receiver UserState, cfg config.Transfer, total, now int64) error {
	// 未结算的划转会让"余额"与"流水"的差额无法归因,人工对账时判断不出该退哪一笔,
	// 因此中间态期间硬闸门,不允许叠加第二笔。
	if sender.PendingCount > 0 {
		return errPendingExists
	}
	if cfg.CooldownSecs > 0 && sender.LastOutAt > 0 && now-sender.LastOutAt < int64(cfg.CooldownSecs) {
		return errCooldown
	}
	if cfg.DailyMaxCount > 0 && sender.DayOutCount+1 > cfg.DailyMaxCount {
		return errDailyCountExceeded
	}
	if cfg.DailyMaxQuota > 0 && sender.DayOutQuota+total > cfg.DailyMaxQuota {
		return errDailyLimitExceeded
	}
	// 收款方的判定放在最后:发起方自己的问题优先报出来,
	// 顺带避免在请求本就不合法时泄露"对方账户今天很忙"这一位信息。
	//
	// DayInCount 由调用方在 rollDay 之后传入,跨日已被清零;它在预占时 +1、
	// 失败时由 undoReservation 原路退还,所以失败的划转不会永久吃掉收款方的名额。
	if cfg.ReceiverDailyMaxInCount > 0 && receiver.DayInCount+1 > cfg.ReceiverDailyMaxInCount {
		return errReceiverDailyInExceeded
	}
	return nil
}

// rollDay 就地滚动自然日计数。
// 读到旧 bucket 即重置,因此不需要定时清零任务 —— 定时任务漏跑一次,
// 限额就会在跨日之后继续沿用昨天的计数。
func rollDay(s *UserState, bucket int32) {
	if s.DayBucket == bucket {
		return
	}
	s.DayBucket = bucket
	s.DayOutQuota = 0
	s.DayOutCount = 0
	s.DayInCount = 0
}

// applyReservation 乐观预占计数。主库失败时由 undoReservation 原路退还。
//
// 先占后用而不是"成功后再记账",是因为主库事务耗时不可控,
// 期间必须已经把额度锁住,否则并发请求会重复通过同一份日限额。
func applyReservation(sender, receiver *UserState, amount, total, now int64, bucket int32) {
	sender.DayBucket = bucket
	sender.DayOutQuota += total
	sender.DayOutCount++
	sender.LifetimeOutQuota += total
	sender.LastOutAt = now
	sender.PendingCount++
	sender.UpdatedAt = now

	receiver.DayBucket = bucket
	receiver.DayInCount++
	receiver.LifetimeInQuota += amount
	receiver.UpdatedAt = now
}

// undoReservation 退还预占。
//
// sameDay 为 false 表示预占之后已经跨日、日计数已被 rollDay 清零,
// 此时再减会把今天的额度凭空放大 —— 只退终身累计。
//
// LastOutAt 刻意不回滚:它已被覆盖,无法还原成上一笔的时刻。
// 结果是失败的划转仍然消耗一次冷却,方向保守,可以接受。
func undoReservation(sender, receiver *UserState, amount, total int64, sameDay bool, now int64) {
	if sameDay {
		sender.DayOutQuota = clampNonNegative64(sender.DayOutQuota - total)
		sender.DayOutCount = clampNonNegative(sender.DayOutCount - 1)
		receiver.DayInCount = clampNonNegative(receiver.DayInCount - 1)
	}
	sender.LifetimeOutQuota = clampNonNegative64(sender.LifetimeOutQuota - total)
	receiver.LifetimeInQuota = clampNonNegative64(receiver.LifetimeInQuota - amount)
	sender.PendingCount = clampNonNegative(sender.PendingCount - 1)
	sender.UpdatedAt = now
	receiver.UpdatedAt = now
}

// clampNonNegative 与 clampNonNegative64 防御性兜底:计数被扣成负数意味着
// 限额从此永久失效,比少统计一笔严重得多。
func clampNonNegative(v int) int {
	if v < 0 {
		return 0
	}
	return v
}

func clampNonNegative64(v int64) int64 {
	if v < 0 {
		return 0
	}
	return v
}

func saveState(tx *gorm.DB, s *UserState) error {
	return tx.Model(&UserState{}).Where("user_id = ?", s.UserId).Updates(map[string]any{
		"day_bucket":         s.DayBucket,
		"day_out_quota":      s.DayOutQuota,
		"day_out_count":      s.DayOutCount,
		"day_in_count":       s.DayInCount,
		"last_out_at":        s.LastOutAt,
		"lifetime_out_quota": s.LifetimeOutQuota,
		"lifetime_in_quota":  s.LifetimeInQuota,
		"pending_count":      s.PendingCount,
		"updated_at":         s.UpdatedAt,
	}).Error
}
