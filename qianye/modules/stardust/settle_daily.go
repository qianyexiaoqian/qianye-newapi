package stardust

import (
	"context"

	"github.com/QuantumNous/new-api/qianye/db"
	"github.com/QuantumNous/new-api/qianye/modules/invite"
	"github.com/QuantumNous/new-api/qianye/service/lease"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// settle_daily.go —— 一日一结算的运行记录(qy_sd_settle_run)的抢占 / 心跳 / 收口 / 重武装。
//
// 表是本包的 SettleRun:
// 三条硬要求:排空、轮次上界、续跑
// ("今天跑过了没有"落库,重启与多实例是同一个问题的两个面 —— 谁抢到那一行谁跑)。
//
// 重复跑是安全的:结算只吸收 computed / held 的桶,逐桶 CAS 成 settled,
// 第二次对同一批桶一颗星屑都发不出来。这条性质是"崩溃后接着跑"能成立的全部依据。

const (
	// settleRunMaxAttempts 是同一天最多尝试几次:没有上界的话,一个持续失败的用户
	// 会让整个队列被反复重跑一整天。用完之后只剩管理端 rerun。
	settleRunMaxAttempts = 5
	// settleRunStaleSecs 是"持有者已经不在了"的判据:心跳停了这么久,别的节点
	// (或重启后的本节点)可以接管今天这一行接着跑。排空过程每轮都刷心跳,
	// 跑得久本身不会被判死;租约 TTL 默认 60 秒,15 分钟给足了余量。
	settleRunStaleSecs = 900
)

// claimDailyRun 抢占"今天这一次结算"。返回 true 表示本节点拿到了。
//
// 四条路径,顺序不能换:
//
//	⓪ 重置写在本结算日开始之前的假记录(日界偏移被调整过)
//	① 接管心跳已停的运行(上一次进程死在半路)
//	② 重试今天没跑完的运行(有人报错,标成了 partial)
//	③ 今天还没有这一行 —— 插入
//
// INSERT 放最后:那一行在当天首次运行之后就一直存在,先 INSERT 等于每次心跳都必然
// 制造一次唯一键冲突,PrepareStmt 开启的连接上失败的预编译语句会被作废。
// 全部是条件写,RowsAffected 就是"我是不是唯一抢到的那个":租约与接管之间有窗口,
// 这一行是唯一能把"今天已经跑过"讲清楚的地方。
func claimDailyRun(ctx context.Context, runDate, targetDay string, now int64) (bool, error) {
	gdb := db.Get()
	if gdb == nil {
		return false, db.ErrNotReady
	}
	gdb = gdb.WithContext(ctx)
	holder := lease.Holder()

	// ⓪ 判据是 created_at:一条正常的记录必然建在它自己那一天之内。重置时把
	// created_at 一起改成 now,否则每次心跳都重新命中,变成"每天重跑一整轮"。
	res := gdb.Model(&SettleRun{}).
		Where("run_date = ? AND created_at < ?", runDate, invite.DayStart(now)).
		Updates(map[string]any{
			"status":       SettleRunRunning,
			"target_date":  targetDay,
			"holder":       holder,
			"attempts":     1,
			"started_at":   now,
			"heartbeat_at": now,
			"finished_at":  0,
			"created_at":   now,
			"remark":       "该记录写于本结算日开始之前(日界偏移被调整过),已重置重跑",
			"updated_at":   now,
		})
	if res.Error != nil {
		db.MarkFailure(res.Error)
		return false, res.Error
	}
	if res.RowsAffected > 0 {
		warnf("%s 的结算运行记录写于本结算日开始之前,已重置并重新结算", runDate)
		return true, nil
	}

	res = gdb.Model(&SettleRun{}).
		Where("run_date = ? AND status = ? AND heartbeat_at <= ? AND attempts < ?",
			runDate, SettleRunRunning, now-settleRunStaleSecs, settleRunMaxAttempts).
		Updates(map[string]any{
			"holder":       holder,
			"attempts":     gorm.Expr("attempts + 1"),
			"started_at":   now,
			"heartbeat_at": now,
			"finished_at":  0,
			"remark":       "接管了心跳已停的运行",
			"updated_at":   now,
		})
	if res.Error != nil {
		db.MarkFailure(res.Error)
		return false, res.Error
	}
	if res.RowsAffected > 0 {
		warnf("接管 %s 心跳已停的结算运行,继续排空当天队列", runDate)
		return true, nil
	}

	res = gdb.Model(&SettleRun{}).
		Where("run_date = ? AND status = ? AND attempts < ?", runDate, SettleRunPartial, settleRunMaxAttempts).
		Updates(map[string]any{
			"status":       SettleRunRunning,
			"holder":       holder,
			"attempts":     gorm.Expr("attempts + 1"),
			"started_at":   now,
			"heartbeat_at": now,
			"finished_at":  0,
			"updated_at":   now,
		})
	if res.Error != nil {
		db.MarkFailure(res.Error)
		return false, res.Error
	}
	if res.RowsAffected > 0 {
		return true, nil
	}

	row := SettleRun{
		RunDate:     runDate,
		TargetDate:  targetDay,
		Status:      SettleRunRunning,
		Holder:      holder,
		Attempts:    1,
		StartedAt:   now,
		HeartbeatAt: now,
		CreatedAt:   now,
		UpdatedAt:   now,
	}
	res = gdb.Clauses(clause.OnConflict{DoNothing: true}).Create(&row)
	if res.Error != nil {
		db.MarkFailure(res.Error)
		return false, res.Error
	}
	return res.RowsAffected == 1, nil
}

// heartbeatDailyRun 逐轮刷新心跳与进度。条件里带 holder 与 status:租约易主之后
// 本节点的这条写入必须落空,否则会把接管者的进度覆盖回去。
func heartbeatDailyRun(ctx context.Context, runDate string, st settleStats, now int64) error {
	gdb := db.Get()
	if gdb == nil {
		return db.ErrNotReady
	}
	return gdb.WithContext(ctx).Model(&SettleRun{}).
		Where("run_date = ? AND holder = ? AND status = ?", runDate, lease.Holder(), SettleRunRunning).
		Updates(map[string]any{
			"heartbeat_at":     now,
			"rounds":           st.Rounds,
			"processed":        st.Processed,
			"failed":           st.Failed,
			"held":             st.Held,
			"granted":          st.Granted,
			"invite_processed": st.InviteProcessed,
			"invite_held":      st.InviteHeld,
			"invite_failed":    st.InviteFailed,
			"invite_granted":   st.InviteGranted,
			"updated_at":       now,
		}).Error
}

// finishDailyRun 落终态。只有"两段都排空了且一个人都没失败"才算 done —— 只有 done 会让
// 今天不再重试。重算失败、有人报错、租约中途丢失,一律 partial:这一天还欠着星屑,
// 必须留一条明确的、能被面板看见的、并且会自动重试的记录。
func finishDailyRun(ctx context.Context, runDate string, st settleStats, now int64) error {
	gdb := db.Get()
	if gdb == nil {
		return db.ErrNotReady
	}
	status := SettleRunPartial
	if st.complete() {
		status = SettleRunDone
	}
	return gdb.WithContext(ctx).Model(&SettleRun{}).
		Where("run_date = ? AND holder = ? AND status = ?", runDate, lease.Holder(), SettleRunRunning).
		Updates(map[string]any{
			"status":           status,
			"finished_at":      now,
			"heartbeat_at":     now,
			"rounds":           st.Rounds,
			"processed":        st.Processed,
			"failed":           st.Failed,
			"held":             st.Held,
			"granted":          st.Granted,
			"invite_processed": st.InviteProcessed,
			"invite_held":      st.InviteHeld,
			"invite_failed":    st.InviteFailed,
			"invite_granted":   st.InviteGranted,
			"remark":           clip(st.Note, 255),
			"updated_at":       now,
		}).Error
}

// rearmDailyRun 把今天这一行重新武装成"还要再跑一次"。
//
// settleRunMaxAttempts 用完之后,即使失败原因已经消失,当天也再不会自动跑 ——
// claimDailyRun 的三条路径全部卡在 attempts < 5 上。管理端 rerun 完成之后调它,
// 让下一次心跳自己接手(重新排空、把状态收敛成 done),租约与"一天只跑一次"一个都不动。
func rearmDailyRun(ctx context.Context, runDate string, now int64) (bool, error) {
	gdb := db.Get()
	if gdb == nil {
		return false, db.ErrNotReady
	}
	res := gdb.WithContext(ctx).Model(&SettleRun{}).
		Where("run_date = ?", runDate).
		Updates(map[string]any{
			"status": SettleRunPartial,
			// 心跳一起清零:这一行卡在 running(进程死在半路)时,接管路径判的是
			// 心跳有多旧,不清零就要再等 settleRunStaleSecs。
			"attempts":     0,
			"finished_at":  0,
			"heartbeat_at": 0,
			"remark":       "管理端重跑之后重新排期,下一次心跳复核",
			"updated_at":   now,
		})
	if res.Error != nil {
		db.MarkFailure(res.Error)
		return false, res.Error
	}
	return res.RowsAffected > 0, nil
}
