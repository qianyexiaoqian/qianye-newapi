package commission

import (
	"context"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/qianye/db"
	"github.com/QuantumNous/new-api/qianye/service/lease"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// settle_daily.go —— 一日一结算的调度。
//
// # 与旧的"每 300 秒跑一批"有什么不同
//
// 旧调度每 300 秒取一批(500 人)结算,取完就收工。那个节奏下"没轮到"是
// 无害的:5 分钟后还有 287 次机会。改成一天一次之后,同样一句"取一批就收工"
// 会变成**第 501 个人要等到明天** —— 600 个活跃邀请人就是天天有 100 人延后
// 一天,而且延后的是谁完全取决于排序键,没有任何信号会响。
//
// 所以一日一结算的三条硬要求,缺一条这个改动就是资金缺陷:
//
//  1. 排空:循环取批直到两路来源都取空,不是取一批就收工。
//  2. 上界:循环必须有轮次上界,否则一条选人 SQL 写错就是死循环占着租约。
//  3. 续跑:中途某个人报错不能让后面的人当天全部收不到;进程中途死掉,
//     重启后必须能接着跑完今天这一次,而不是等到明天。
//
// # "今天跑过了没有"存在哪
//
// 存在扩展库的 qy_commission_settle_run,一天一行,run_date 唯一索引。
// 不能存进程内变量:重启就忘,一天可能跑很多次;也不能只靠租约,租约只回答
// "现在谁在跑",不回答"今天跑完了没有"。落库之后多实例与重启是同一个问题的
// 两个面 —— 谁抢到那一行谁跑,抢不到的直接返回。
//
// 重复跑是**安全**的(见 TestDailyRunRestartDoesNotDoublePay):settleUser 只
// 吸收 settled_amount <> gross_amount 的行,absorbAccruals 用 CAS 把它们写成
// gross_amount,所以第二次跑对同一批行一分钱都发不出来。这条性质是"崩溃后
// 接着跑"能成立的全部依据,不是假设 —— 有测试直接钉住它。

// SettleRun 是一天一行的结算运行记录:既是"今天跑过了没有"的状态,
// 也是管理端健康面板要看的那几个数(跑了多久、处理了多少人、有没有失败)。
//
// 两件事合在一张表里是刻意的:分开就会出现"状态说跑完了、记录说没跑"的
// 两份事实,而它们本来就是同一次运行的两个侧面。
type SettleRun struct {
	Id int64 `json:"id" gorm:"primaryKey;autoIncrement"`
	// RunDate 是结算日界口径下的"今天"(见 dayline.go),不是 UTC 自然日,
	// 也不是服务器本地日 —— 它必须与 bucket_date 同源。
	RunDate string `json:"run_date" gorm:"type:varchar(8);not null;uniqueIndex:uk_qy_csr_date"`
	Status  string `json:"status" gorm:"type:varchar(16);not null;default:''"`
	// Holder 是最后一次尝试的持有者(节点名:随机后缀),与租约表同一口径。
	Holder   string `json:"holder" gorm:"type:varchar(200);not null;default:''"`
	Attempts int    `json:"attempts" gorm:"not null;default:0"`

	StartedAt  int64 `json:"started_at" gorm:"not null;default:0"`
	FinishedAt int64 `json:"finished_at" gorm:"not null;default:0"`
	// HeartbeatAt 在排空过程中逐轮刷新。它是"持有者还活着"的唯一证据:
	// 只看 status=running 的话,一次进程被 kill 会让今天这一行永远停在
	// running,后面谁都不敢接手,当天剩下的人一分钱都拿不到。
	HeartbeatAt int64 `json:"heartbeat_at" gorm:"not null;default:0"`

	Rounds    int `json:"rounds" gorm:"not null;default:0"`
	Processed int `json:"processed" gorm:"not null;default:0"`
	Failed    int `json:"failed" gorm:"not null;default:0"`

	GrantedQuota   int64 `json:"granted_quota" gorm:"not null;default:0"`
	ReclaimedQuota int64 `json:"reclaimed_quota" gorm:"not null;default:0"`

	Remark    string `json:"remark" gorm:"type:varchar(255);not null;default:''"`
	CreatedAt int64  `json:"created_at" gorm:"not null;default:0"`
	UpdatedAt int64  `json:"updated_at" gorm:"not null;default:0"`
}

func (SettleRun) TableName() string { return "qy_commission_settle_run" }

// 运行状态。partial 与 done 的区别只有一个:partial 今天还会被重试。
const (
	settleRunRunning = "running"
	settleRunDone    = "done"
	settleRunPartial = "partial"
)

const (
	// settleDrainMaxRounds 是单次运行的取批轮次上界。
	// 400 × settleInviterBatch(500) = 20 万邀请人/次,远超本站量级;
	// 它防的不是业务规模,而是"选人 SQL 写错导致永远取得到同一批"这种死循环。
	settleDrainMaxRounds = 400
	// settleRunMaxAttempts 是同一天最多尝试几次。中途失败(某些人报错)会把
	// 这一天标成 partial,下一次心跳重试;没有上界的话,一个持续失败的邀请人
	// 会让整个队列被反复重跑一整天。
	settleRunMaxAttempts = 5
	// settleRunStaleSecs 是"持有者已经不在了"的判据:心跳停了这么久,
	// 别的节点(或重启后的本节点)可以接管今天这一行接着跑。
	//
	// 取值必须显著大于一次排空的正常耗时,而排空过程每轮都刷心跳,所以
	// 跑得久本身不会被判死。租约 TTL 默认 60 秒,15 分钟给足了余量。
	settleRunStaleSecs = 900
)

// runSettle 是结算后台任务的入口,由 lease.Run 按心跳周期驱动。
//
// 每次心跳只做一件事:今天这一次跑过了没有?没跑过就抢下来,排空整个队列。
func runSettle(ctx context.Context) {
	if ctx.Err() != nil {
		return
	}
	now := common.GetTimestamp()
	day := dayKey(now)

	claimed, err := claimDailyRun(day, now)
	if err != nil {
		warnf("抢占 %s 的结算运行记录失败: %v", day, err)
		return
	}
	if !claimed {
		return // 今天已经跑完,或别的节点正在跑
	}

	settleRuns.Add(1)
	repairStrandedAccruals(ctx)

	st := drainSettle(ctx, day)
	if err := finishDailyRun(day, st, common.GetTimestamp()); err != nil {
		warnf("回写 %s 的结算运行记录失败: %v", day, err)
	}
}

// claimDailyRun 抢占"今天这一次结算"。返回 true 表示本节点拿到了。
//
// 三条路径,顺序不能换:
//
//	① 接管心跳已停的运行(上一次进程死在半路)
//	② 重试今天没跑完的运行(有人报错,标成了 partial)
//	③ 今天还没有这一行 —— 插入
//
// 把 INSERT 放最后与 lease.Acquire 是同一条理由:表里那一行在当天首次运行
// 之后就一直存在,先 INSERT 等于**每次心跳都必然制造一次唯一键冲突**。
// 在 PrepareStmt 开启的连接上(qianye/db 默认开),失败的预编译语句会被作废,
// 同一条连接上紧随其后的语句可能拿到 "statement is closed"。
//
// 三条路径全部是条件写,RowsAffected 就是"我是不是唯一抢到的那个"。租约已经
// 保证同一时刻只有一个节点在跑,这里的条件写是第二道闸:租约过期与接管之间
// 有窗口,而这一行是唯一能把"今天已经跑过"这件事讲清楚的地方。
func claimDailyRun(day string, now int64) (bool, error) {
	gdb := db.Get()
	if gdb == nil {
		return false, db.ErrNotReady
	}
	holder := lease.Holder()

	// ⓪ 一行"写在本结算日开始之前"的记录是假的,必须重置后重跑。
	//
	// run_date 来自 dayKey(now),而 dayKey 受 commission.day_offset_minutes 管辖。
	// 把偏移**往前调**会让进程在今天就为未来某个 run_date 建行并跑完;偏移改回去
	// 之后,那一天真正到来时下面三条路径全部落空(status 已经是 done),**那一整天
	// 的结算被永久跳过**,而面板还照常显示 ran_today=true。实测走过一遍:
	// 19:06 UTC 把偏移 0→291,19:09 就建出 run_date=20260819 并 done;偏移改回 0
	// 之后,真正的 20260819 那一天一个人都没结算。
	//
	// 判据是 created_at:一条正常的记录必然建在它自己那一天之内。重置时把
	// created_at 一起改成 now,否则这一条会在每次心跳都重新命中,变成
	// "每天重跑一整轮"。
	res := gdb.Model(&SettleRun{}).
		Where("run_date = ? AND created_at < ?", day, dayStart(now)).
		Updates(map[string]any{
			"status":       settleRunRunning,
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
		warnf("%s 的结算运行记录写于本结算日开始之前,已重置并重新排空当天队列", day)
		return true, nil
	}

	res = gdb.Model(&SettleRun{}).
		Where("run_date = ? AND status = ? AND heartbeat_at <= ? AND attempts < ?",
			day, settleRunRunning, now-settleRunStaleSecs, settleRunMaxAttempts).
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
		warnf("接管 %s 心跳已停的结算运行,继续排空当天队列", day)
		return true, nil
	}

	res = gdb.Model(&SettleRun{}).
		Where("run_date = ? AND status = ? AND attempts < ?",
			day, settleRunPartial, settleRunMaxAttempts).
		Updates(map[string]any{
			"status":       settleRunRunning,
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
		RunDate:     day,
		Status:      settleRunRunning,
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

// rearmDailyRun 把今天这一行重新武装成"还要再跑一次"。
//
// settleRunMaxAttempts(5)用完之后,即使失败原因已经消失,当天也再不会自动跑:
// claimDailyRun 的三条路径全部卡在 attempts < 5 上。实测在**完全没有故障**的
// 库上把 attempts 手工改成 5,后续心跳一个人都不结算;改回 4 就立刻排空 ——
// 也就是说这个计数器本身就是唯一的闸门。生产默认 300 秒心跳下,一次约 25 分钟
// 的偏侧故障(锁等待、扩展库主从切换、租约续租抖动)就能把当天剩下所有人的
// 佣金推到明天,而运营手上只有"逐个用户手动结算"这一条路。
//
// 所以必须有一个"再试一次"的入口。这里刻意不直接在请求线程里跑排空:
// 排空可能持续很久,而 HTTP 超时会让运营以为失败了又点一次;把状态改成
// partial / attempts=0 之后,下一次心跳(最长 settle_interval_seconds)自己接手,
// 租约与"一天只跑一次"两条不变量一个都不动。
func rearmDailyRun(day string, now int64) (bool, error) {
	gdb := db.Get()
	if gdb == nil {
		return false, db.ErrNotReady
	}
	res := gdb.Model(&SettleRun{}).
		Where("run_date = ?", day).
		Updates(map[string]any{
			"status": settleRunPartial,
			// 心跳一起清零:这一行卡在 running(进程死在半路)时,接管路径
			// 判的是心跳有多旧,不清零就要再等 settleRunStaleSecs。
			"attempts":     0,
			"finished_at":  0,
			"heartbeat_at": 0,
			"remark":       "管理员手动重新排期,下一次心跳重跑",
			"updated_at":   now,
		})
	if res.Error != nil {
		db.MarkFailure(res.Error)
		return false, res.Error
	}
	return res.RowsAffected > 0, nil
}

// drainStats 是一次排空的结果。
type drainStats struct {
	Rounds    int
	Processed int
	Failed    int
	// Granted/Reclaimed 取自全局计数器的差值。它们只服务于面板,可能被同一
	// 时刻的管理端手动结算掺进来一点 —— 精确的发放金额在结算单里,不在这。
	Granted   int64
	Reclaimed int64
	// Drained 为真表示两路来源都取空了,今天的队列确实排完了。
	Drained bool
	// Capped 是"这个人名下的计佣行多到一次运行都吸收不完"的人数。
	// 不为 0 时这一天绝不能报 done:钱还压着,而选人 SQL 那一侧已经取空了,
	// Drained 单独一个数完全看不出这件事。
	Capped int
	// Aborted 为真表示租约中途丢了。此时**必须**标成 partial:
	// 队列没排完,而今天只有这一次机会。
	Aborted bool
	Note    string
}

// drainSettle 排空整个待结算队列。
//
// 单个邀请人报错只计数、不中断:一日一结算之下,让第 300 个人的错误吃掉
// 后面 300 个人当天的佣金是不可接受的。报错的人会在本日的下一次重试里
// 重新被选中(键集游标每次运行都从头开始)。
//
// 返回值必须具名:发放金额是在 defer 里按计数器差值补上的,匿名返回值会在
// defer 之前就被复制走,面板上那两个数会恒为 0。
func drainSettle(ctx context.Context, day string) (st drainStats) {
	grantedBefore, reclaimedBefore := settleGranted.Load(), settleReclaimed.Load()
	defer func() {
		st.Granted = settleGranted.Load() - grantedBefore
		st.Reclaimed = settleReclaimed.Load() - reclaimedBefore
	}()

	var cur inviterCursor
	seen := make(map[int]struct{})
	for st.Rounds < settleDrainMaxRounds {
		if ctx.Err() != nil {
			st.Aborted = true
			st.Note = "租约中途丢失,队列未排空"
			return st
		}
		ids, next, more, err := pendingInvitersPage(settleInviterBatch, cur)
		if err != nil {
			st.Note = "选人失败: " + err.Error()
			warnf("获取待结算邀请人失败: %v", err)
			return st
		}
		st.Rounds++
		cur = next
		if !more {
			st.Drained = true
			return st
		}
		for _, id := range ids {
			if ctx.Err() != nil {
				st.Aborted = true
				st.Note = "租约中途丢失,队列未排空"
				return st
			}
			// 同一个人可能先后从两路来源被选中(先吸收计佣行,又剩下零头)。
			// 当天只结算一次:第二次也发不出别的钱,只会白跑一次加锁事务。
			if _, dup := seen[id]; dup {
				continue
			}
			seen[id] = struct{}{}
			drained, err := settleUserDrain(id)
			if err != nil {
				st.Failed++
				settleFailed.Add(1)
				warnf("邀请人 %d 结算失败(今天稍后重试): %v", id, err)
				continue
			}
			st.Processed++
			if !drained {
				st.Capped++
				warnf("邀请人 %d 名下的已成熟计佣行一次运行吸收不完,今天稍后重试", id)
			}
		}
		if err := heartbeatDailyRun(day, st, common.GetTimestamp()); err != nil {
			warnf("刷新 %s 结算运行心跳失败: %v", day, err)
		}
	}
	st.Note = "达到单次排空轮次上界,剩余队列今天稍后重试"
	warnf("%s 的结算排空达到轮次上界 %d,队列未排空", day, settleDrainMaxRounds)
	return st
}

// heartbeatDailyRun 逐轮刷新心跳与进度。
//
// 条件里带 holder 与 status:租约易主之后本节点的这条写入必须落空,
// 否则会把接管者的进度覆盖回去。
func heartbeatDailyRun(day string, st drainStats, now int64) error {
	gdb := db.Get()
	if gdb == nil {
		return db.ErrNotReady
	}
	return gdb.Model(&SettleRun{}).
		Where("run_date = ? AND holder = ? AND status = ?", day, lease.Holder(), settleRunRunning).
		Updates(map[string]any{
			"heartbeat_at": now,
			"rounds":       st.Rounds,
			"processed":    st.Processed,
			"failed":       st.Failed,
			"updated_at":   now,
		}).Error
}

// finishDailyRun 落终态。
//
// 只有"排空了且一个人都没失败"才算 done —— 只有 done 会让今天不再重试。
// 队列没排空、有人报错、租约中途丢失,一律 partial:这一天还欠着钱,
// 必须留一条明确的、能被面板看见的、并且会自动重试的记录。
func finishDailyRun(day string, st drainStats, now int64) error {
	gdb := db.Get()
	if gdb == nil {
		return db.ErrNotReady
	}
	status := settleRunPartial
	if st.Drained && st.Failed == 0 && st.Capped == 0 && !st.Aborted {
		status = settleRunDone
	}
	if st.Capped > 0 && st.Note == "" {
		st.Note = "有邀请人的计佣行一次吸收不完,今天稍后重试"
	}
	return gdb.Model(&SettleRun{}).
		Where("run_date = ? AND holder = ? AND status = ?", day, lease.Holder(), settleRunRunning).
		Updates(map[string]any{
			"status":          status,
			"finished_at":     now,
			"heartbeat_at":    now,
			"rounds":          st.Rounds,
			"processed":       st.Processed,
			"failed":          st.Failed,
			"granted_quota":   st.Granted,
			"reclaimed_quota": st.Reclaimed,
			"remark":          truncate(st.Note, 255),
			"updated_at":      now,
		}).Error
}

// dailySettleSnapshot 是健康面板那一段。
//
// 面板要能回答四个问题,一个都不能少:今天跑过了吗、跑了多久、处理了多少人、
// 有没有中途失败。此前面板上只有 pending_inviters(积压深度),它在一日一结算
// 之下几乎没有信息量 —— 一天里绝大部分时间它都是"有一堆人等着",那是正常的。
func dailySettleSnapshot(now int64) map[string]any {
	day := dayKey(now)
	out := map[string]any{
		"today":              day,
		"day_offset_minutes": int(dayOffsetSeconds() / 60),
		"next_run_after":     nextDayStart(now),
		"max_attempts":       settleRunMaxAttempts,
		"ran_today":          false,
	}
	gdb := db.Get()
	if gdb == nil {
		out["error"] = db.ErrNotReady.Error()
		return out
	}
	var rows []SettleRun
	// 取最近两天:今天这一行回答"跑过没有",前一行让"昨天跑成什么样"
	// 在同一屏里可见 —— 一日一结算之下,昨天没跑成是今天才会被发现的事。
	// run_date <= today 是必需的:偏移被调小(或多节点偏移口径不一致)时表里会
	// 留下未来日期的行,不过滤的话面板会把一个**未来**的日期标成"昨天那一跑",
	// 同时把真正的昨天挤出这两行的窗口 —— 运营正在排查偏移问题的那一天,
	// 面板给的恰好是反向线索。
	if err := gdb.Where("run_date <= ?", day).
		Order("run_date desc").Limit(2).Find(&rows).Error; err != nil {
		db.MarkFailure(err)
		out["error"] = err.Error()
		return out
	}
	for _, r := range rows {
		view := runView(r, now)
		if r.RunDate == day {
			out["ran_today"] = r.Status == settleRunDone
			out["current"] = view
			continue
		}
		if _, dup := out["previous"]; !dup {
			out["previous"] = view
		}
	}
	return out
}

func runView(r SettleRun, now int64) map[string]any {
	elapsed := r.FinishedAt - r.StartedAt
	if r.FinishedAt == 0 {
		elapsed = now - r.StartedAt
	}
	if elapsed < 0 {
		elapsed = 0
	}
	return map[string]any{
		"run_date":        r.RunDate,
		"status":          r.Status,
		"holder":          r.Holder,
		"attempts":        r.Attempts,
		"started_at":      r.StartedAt,
		"finished_at":     r.FinishedAt,
		"heartbeat_at":    r.HeartbeatAt,
		"duration_sec":    elapsed,
		"rounds":          r.Rounds,
		"processed":       r.Processed,
		"failed":          r.Failed,
		"granted_quota":   r.GrantedQuota,
		"reclaimed_quota": r.ReclaimedQuota,
		"remark":          r.Remark,
	}
}
