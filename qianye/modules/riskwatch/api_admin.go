package riskwatch

import (
	"context"
	"errors"
	"strings"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/qianye/config"
	"github.com/QuantumNous/new-api/qianye/db"
	"github.com/QuantumNous/new-api/qianye/groupname"
	"github.com/QuantumNous/new-api/qianye/guard"
	"github.com/QuantumNous/new-api/qianye/httpq"
	qymodel "github.com/QuantumNous/new-api/qianye/model"
	"github.com/QuantumNous/new-api/qianye/service/audit"

	"github.com/gin-gonic/gin"
	"gorm.io/gorm"
)

// api_admin.go —— 风控预警的管理端接口。
//
// # 审计口径
//
// 每个写接口各有**恰好两处** audit.WriteConfigUpdate:成功一条、失败一条。
// 失败那条是必须的 —— "有人在这一刻试图把某个用户的监听概率调到 100%、
// 被上限挡下了"与"没人动过"在事后是完全不同的两件事。
//
// 纯输入校验失败(任务名空、概率超范围)**不写**审计:那是管理员在输入框里
// 打字的过程,一次编辑会产生十几条,把真正有价值的那几条冲掉。分界线与 apiaddr
// 一致:有没有碰到库里的既有状态。
//
// # 为什么监听任务的写接口全都留痕
//
// 这个功能的产出是**别人的原始请求内容**。谁在什么时候开始盯谁、盯了多久、
// 用多大概率,本身就是必须可被审计的事实 —— 一个能悄悄建监听任务的管理员,
// 与一个能悄悄读用户数据的管理员是同一件事。
const (
	actionTaskCreate = "risk_watch.task.create"
	actionTaskUpdate = "risk_watch.task.update"
	actionTaskStop   = "risk_watch.task.stop"
	actionTaskStart  = "risk_watch.task.start"
	actionTaskDelete = "risk_watch.task.delete"
)

const (
	maxNameLen = 128
	maxNoteLen = 512
	// maxCaptureListSize 是记录列表的页长上限。比通用的 100 小是刻意的:
	// 每一行都带着一段可能几千字的正文,一页 100 条就是一次几百 KB 的响应。
	maxCaptureListSize = 50
)

// upsertReq 是新建 / 编辑监听任务的入参。
//
// 所有字段都是整行提交(编辑弹窗永远回填全部字段再提交),因此缺省一律按零值
// 处理,不做"缺省 = 保持原样"。两种缺省语义并存时谁也记不住哪个字段是哪种,
// 而在这张表上记错的代价是一个本该 1% 的任务变成 0%(什么都抓不到)或者
// 100%(把存储节点写满)。
type upsertReq struct {
	Name string `json:"name"`
	Note string `json:"note"`

	TargetUserId int    `json:"target_user_id"`
	TargetGroup  string `json:"target_group"`
	TargetModel  string `json:"target_model"`

	SampleBps  int `json:"sample_bps"`
	MaxRecords int `json:"max_records"`

	WindowMode       string `json:"window_mode"`
	StartsAt         int64  `json:"starts_at"`
	EndsAt           int64  `json:"ends_at"`
	CountdownSeconds int    `json:"countdown_seconds"`

	// RetentionDays 是指针:null = 跟随全局默认,0 = 永久保留。见 Task.RetentionDays。
	RetentionDays *int `json:"retention_days"`

	// Version 是乐观锁,只在编辑时校验。
	Version int `json:"version"`
}

// auditSnapshot 是写进审计 before/after 的字段白名单。
//
// 刻意不直接序列化 Task:白名单让"新增字段默认不进审计",而不是反过来。
type auditSnapshot struct {
	Id             int64  `json:"id"`
	Name           string `json:"name"`
	Note           string `json:"note"`
	TargetUserId   int    `json:"target_user_id"`
	TargetUsername string `json:"target_username"`
	TargetGroup    string `json:"target_group"`
	TargetModel    string `json:"target_model"`
	SampleBps      int    `json:"sample_bps"`
	MaxRecords     int    `json:"max_records"`
	Captured       int    `json:"captured"`
	WindowMode     string `json:"window_mode"`
	StartsAt       int64  `json:"starts_at"`
	EndsAt         int64  `json:"ends_at"`
	RetentionDays  *int   `json:"retention_days"`
	Status         string `json:"status"`
	StoppedReason  string `json:"stopped_reason"`
}

func snapshotOf(t Task) auditSnapshot {
	return auditSnapshot{
		Id: t.Id, Name: t.Name, Note: t.Note,
		TargetUserId: t.TargetUserId, TargetUsername: t.TargetUsername,
		TargetGroup: t.TargetGroup, TargetModel: t.TargetModel,
		SampleBps: t.SampleBps, MaxRecords: t.MaxRecords, Captured: t.Captured,
		WindowMode: t.WindowMode, StartsAt: t.StartsAt, EndsAt: t.EndsAt,
		RetentionDays: t.RetentionDays,
		Status:        t.Status, StoppedReason: t.StoppedReason,
	}
}

// applyTo 校验入参并写进目标行。creating 决定时间窗的起点怎么算。
//
// 校验顺序是刻意的:先纯格式(名字、长度、范围),最后才是作用域为空那一条。
// 后者要出的那句话("三项都不填等于全站监听")在一个连名字都没填的请求上
// 显示出来只会让人困惑。
func (r *upsertReq) applyTo(dst *Task, creating bool) error {
	name := strings.TrimSpace(r.Name)
	if name == "" {
		return errNameRequired
	}
	if len(name) > maxNameLen {
		return errNameTooLong
	}
	note := strings.TrimSpace(r.Note)
	if len(note) > maxNoteLen {
		return errNoteTooLong
	}
	if r.SampleBps <= 0 || r.SampleBps > 10000 {
		return errSampleRange
	}
	if r.MaxRecords < 0 || r.MaxRecords > maxRecordsHardCap {
		return errMaxRecordsRange
	}
	if r.TargetUserId < 0 {
		return errInvalidParam
	}

	group := groupname.Normalize(r.TargetGroup)
	targetModel := strings.TrimSpace(r.TargetModel)
	if len(group) > 64 || len(targetModel) > 128 {
		return errInvalidParam
	}
	if r.TargetUserId == 0 && group == "" && targetModel == "" {
		return errScopeEmpty
	}

	startsAt, endsAt, err := resolveWindow(r, creating, dst)
	if err != nil {
		return err
	}
	if err := checkRetention(r.RetentionDays); err != nil {
		return err
	}

	dst.Name, dst.Note = name, note
	dst.TargetUserId = r.TargetUserId
	dst.TargetGroup, dst.TargetModel = group, targetModel
	dst.SampleBps, dst.MaxRecords = r.SampleBps, r.MaxRecords
	dst.WindowMode = r.WindowMode
	dst.StartsAt, dst.EndsAt = startsAt, endsAt
	dst.CountdownSeconds = r.CountdownSeconds
	dst.RetentionDays = r.RetentionDays
	return nil
}

// maxRecordsHardCap 是抽取条数的硬上界。
//
// 它不是"够用就行"的一个数,而是这个存储节点的一道保险:一条记录带着最多
// capture_max_chars 个字符的正文,一百万条就是 GB 级。要抓得比这更多,
// 说明要的其实是全量日志,而不是一次取证。
const maxRecordsHardCap = 1_000_000

// resolveWindow 把三种时间窗形态归一成两个时间戳。
//
// 倒计时的起点取**这一刻**而不是任务的创建时间:一个停了三天又被重新启动的
// "倒计时 2 小时"任务,管理员要的显然是从按下按钮那一刻起的两小时。
func resolveWindow(r *upsertReq, creating bool, dst *Task) (startsAt, endsAt int64, err error) {
	now := common.GetTimestamp()
	switch r.WindowMode {
	case WindowForever:
		return 0, 0, nil
	case WindowRange:
		if r.EndsAt <= 0 || (r.StartsAt > 0 && r.EndsAt <= r.StartsAt) {
			return 0, 0, errWindowRange
		}
		// 编辑一个已经在跑的任务时,允许把结束时间改到"已经过去" —— 那等于
		// 立即结束,是一个合理的操作。新建时不允许:建一个生下来就过期的任务
		// 只可能是填错了。
		if creating && r.EndsAt <= now {
			return 0, 0, errWindowRange
		}
		return r.StartsAt, r.EndsAt, nil
	case WindowCountdown:
		if r.CountdownSeconds <= 0 || r.CountdownSeconds > maxCountdownSeconds {
			return 0, 0, errCountdownRange
		}
		// 编辑时不重算倒计时终点:那会让"只改了个备注"顺手把剩余时间重置成
		// 满格,而管理员在界面上只看到备注被改了。重新计时是"启动"那个动作
		// 的语义(见 adminStart)。
		if !creating && dst.EndsAt > 0 {
			return dst.StartsAt, dst.EndsAt, nil
		}
		return now, now + int64(r.CountdownSeconds), nil
	default:
		return 0, 0, errWindowMode
	}
}

// maxCountdownSeconds 是倒计时的上界:90 天。再长的监听请用"永久"并显式
// 在立案理由里写清楚 —— 一个 3650 天的倒计时与永久监听没有区别,
// 但它在列表页上会伪装成一个有终点的任务。
const maxCountdownSeconds = 90 * 24 * 3600

func checkRetention(days *int) error {
	if days == nil {
		return nil
	}
	if *days < 0 {
		return errRetentionRange
	}
	maxDays := config.Get().RiskWatch.MaxRetentionDays
	if maxDays > 0 && *days > maxDays {
		return errRetentionRange
	}
	return nil
}

// ───────────────────────────── 任务 ─────────────────────────────

// adminListTasks 下发监听任务列表。
func adminListTasks(c *gin.Context) {
	if !guard.RequireAPI(c, guard.FlagRiskWatch) {
		return
	}
	ctx, cancel := guard.ColdContext(c.Request.Context())
	defer cancel()
	gdb, err := handle(ctx)
	if err != nil {
		respondErr(c, err)
		return
	}

	page, size := httpq.Paginate(c, httpq.Spec{})
	q := gdb.Model(&Task{})
	if status := strings.TrimSpace(c.Query("status")); status != "" {
		q = q.Where("status = ?", status)
	}
	if uid := httpq.Int(c, "target_user_id", 0); uid > 0 {
		q = q.Where("target_user_id = ?", uid)
	}

	var total int64
	if err := q.Count(&total).Error; err != nil {
		db.MarkWatchFailure(err)
		respondErr(c, err)
		return
	}
	// 空切片而不是 nil:nil 会被序列化成 null,而前端对 null 与 [] 的处理
	// 不同(qianye/json_array_guard_test.go 守的就是这个)。
	items := make([]Task, 0, size)
	if err := q.Order("id desc").Offset(httpq.Offset(page, size)).Limit(size).Find(&items).Error; err != nil {
		db.MarkWatchFailure(err)
		respondErr(c, err)
		return
	}
	respondOK(c, gin.H{
		"items": items, "total": total, "page": page, "page_size": size,
		// 上限跟着列表一起下发:前端要用它决定"新建"按钮是否可用,自己抄一份
		// 就是同一常量的第二份拷贝,改后端时前端不会跟着变。
		"max_active_tasks":   config.Get().RiskWatch.MaxActiveTasks,
		"max_retention_days": config.Get().RiskWatch.MaxRetentionDays,
		"default_retention":  config.Get().RiskWatch.RetentionDays,
	})
}

// adminCreateTask 立一个新的监听任务。
func adminCreateTask(c *gin.Context) {
	if !guard.RequireAPI(c, guard.FlagRiskWatch) {
		return
	}
	var req upsertReq
	if err := c.ShouldBindJSON(&req); err != nil {
		respondErr(c, errInvalidParam)
		return
	}
	now := common.GetTimestamp()
	row := Task{Status: StatusRunning, CreatedBy: c.GetInt("id"), Version: 1, CreatedAt: now, UpdatedAt: now}
	if err := req.applyTo(&row, true); err != nil {
		respondErr(c, err)
		return
	}

	ctx, cancel := guard.ColdContext(c.Request.Context())
	defer cancel()
	gdb, err := handle(ctx)
	if err != nil {
		respondErr(c, err)
		return
	}

	if err := prepareTarget(ctx, gdb, &row); err != nil {
		audit.WriteConfigUpdate(c, audit.ConfigChange{
			Action: actionTaskCreate, Result: qymodel.ResultFail,
			Reason: err.Error(), After: snapshotOf(row),
		})
		respondErr(c, err)
		return
	}
	if err := insertTask(ctx, gdb, &row); err != nil {
		audit.WriteConfigUpdate(c, audit.ConfigChange{
			Action: actionTaskCreate, Result: qymodel.ResultFail,
			Reason: err.Error(), After: snapshotOf(row),
		})
		respondErr(c, err)
		return
	}
	audit.WriteConfigUpdate(c, audit.ConfigChange{Action: actionTaskCreate, After: snapshotOf(row)})
	// 立刻重载快照:靠 snapshot_seconds 自然到期的话,管理员点完"建立"之后
	// 最多还要等 30 秒才开始抓 —— 而立案往往就是为了赶上正在发生的那一波。
	_ = Reload()
	respondOK(c, row)
}

// prepareTarget 校验目标用户存在并抄下用户名,同时把住运行中任务数上限。
//
// 两件事合在一起是因为它们是**同一类**:都要碰库里的既有状态,因此都属于
// "失败要写审计"的那一档(见本文件顶部的审计口径)。
func prepareTarget(ctx context.Context, gdb *gorm.DB, row *Task) error {
	if row.TargetUserId > 0 {
		// 用户住在**主库**,这是本模块唯一一次跨库读。它不在任何事务里,
		// 读到的用户名只是一份用于展示的快照(见 Task.TargetUsername),
		// 因此不违反"存储节点的表不与别的库 JOIN"那条硬约束。
		u, err := model.GetUserById(row.TargetUserId, false)
		if err != nil || u == nil {
			return errTargetUserNotFound
		}
		row.TargetUsername = truncate(u.Username, 64)
	}
	maxActive := config.Get().RiskWatch.MaxActiveTasks
	if maxActive <= 0 {
		return nil
	}
	n, err := countActive(ctx, gdb)
	if err != nil {
		return err
	}
	if n >= int64(maxActive) {
		return errTooManyActive
	}
	return nil
}

// adminUpdateTask 编辑一个监听任务。
//
// 已经停止的任务同样可以编辑(改名、补立案理由、延长保留期),但改不动它的
// 状态 —— 重新开跑是 adminStart 的事。两者分开是因为它们的审计含义不同:
// "改了参数"与"又开始盯了"必须是两条不同的记录。
func adminUpdateTask(c *gin.Context) {
	if !guard.RequireAPI(c, guard.FlagRiskWatch) {
		return
	}
	id, ok := httpq.PathInt64(c, "id")
	if !ok {
		respondErr(c, errInvalidParam)
		return
	}
	var req upsertReq
	if err := c.ShouldBindJSON(&req); err != nil {
		respondErr(c, errInvalidParam)
		return
	}

	ctx, cancel := guard.ColdContext(c.Request.Context())
	defer cancel()
	gdb, err := handle(ctx)
	if err != nil {
		respondErr(c, err)
		return
	}

	before, err := loadTask(ctx, gdb, id)
	if err != nil {
		respondErr(c, err)
		return
	}
	if before.Version != req.Version {
		respondErr(c, errConflict)
		return
	}

	after := before
	if err := req.applyTo(&after, false); err != nil {
		// 纯输入校验,不写审计(见文件顶部口径)。
		respondErr(c, err)
		return
	}
	if err := prepareTargetOnUpdate(&before, &after); err != nil {
		audit.WriteConfigUpdate(c, audit.ConfigChange{
			Action: actionTaskUpdate, Result: qymodel.ResultFail,
			Reason: err.Error(), Before: snapshotOf(before), After: snapshotOf(after),
		})
		respondErr(c, err)
		return
	}
	after.UpdatedAt = common.GetTimestamp()
	after.Version = before.Version + 1

	res := gdb.Model(&Task{}).Where("id = ? AND version = ?", id, before.Version).Updates(map[string]any{
		"name":              after.Name,
		"note":              after.Note,
		"target_user_id":    after.TargetUserId,
		"target_username":   after.TargetUsername,
		"user_group":        after.TargetGroup,
		"model_name":        after.TargetModel,
		"sample_bps":        after.SampleBps,
		"max_records":       after.MaxRecords,
		"window_mode":       after.WindowMode,
		"starts_at":         after.StartsAt,
		"ends_at":           after.EndsAt,
		"countdown_seconds": after.CountdownSeconds,
		"retention_days":    after.RetentionDays,
		"updated_at":        after.UpdatedAt,
		"version":           after.Version,
	})
	if res.Error != nil || res.RowsAffected == 0 {
		reason := errConflict
		if res.Error != nil {
			db.MarkWatchFailure(res.Error)
		}
		audit.WriteConfigUpdate(c, audit.ConfigChange{
			Action: actionTaskUpdate, Result: qymodel.ResultFail,
			Reason: reason.Error(), Before: snapshotOf(before), After: snapshotOf(after),
		})
		respondErr(c, reason)
		return
	}
	// 保留期改了就把已有记录的到期时刻一起改掉。不做的话,那些行会按**旧**
	// 保留期被清掉,而管理员刚刚在界面上把它从 7 天改成 90 天 —— 他会在第八天
	// 发现证据没了,而任务上明明写着 90。
	if err := rewriteExpiry(ctx, gdb, &before, &after); err != nil {
		respondErr(c, err)
		return
	}
	audit.WriteConfigUpdate(c, audit.ConfigChange{
		Action: actionTaskUpdate, Before: snapshotOf(before), After: snapshotOf(after),
	})
	_ = Reload()
	respondOK(c, after)
}

// prepareTargetOnUpdate 只在监听目标真的换了人时才去主库核对。
//
// 不无条件核对是因为这条路径最常见的用法是"补一句立案理由":为它去主库查一次
// 用户,会让一个被停用/删号的目标把一次纯文字编辑也挡下来 —— 而那恰恰是
// 事后补记录最需要能做的操作。
func prepareTargetOnUpdate(before, after *Task) error {
	if after.TargetUserId == before.TargetUserId {
		after.TargetUsername = before.TargetUsername
		return nil
	}
	after.TargetUsername = ""
	if after.TargetUserId == 0 {
		return nil
	}
	u, err := model.GetUserById(after.TargetUserId, false)
	if err != nil || u == nil {
		return errTargetUserNotFound
	}
	after.TargetUsername = truncate(u.Username, 64)
	return nil
}

// rewriteExpiry 在任务保留期变化后重写已有记录的到期时刻。
//
// 它是 Capture.ExpiresAt 这个设计(落库时折算成绝对时刻)必须配套的另一半。
// 一次带索引的 UPDATE,行数上界是这个任务的 max_records —— 有边界,而且只在
// 管理员真的改了保留期时才发生。
func rewriteExpiry(ctx context.Context, gdb *gorm.DB, before, after *Task) error {
	if samePtrInt(before.RetentionDays, after.RetentionDays) {
		return nil
	}
	days := config.Get().RiskWatch.RetentionDays
	if after.RetentionDays != nil {
		days = *after.RetentionDays
	}
	expr := "created_at + ?"
	args := []any{int64(days) * 86400}
	if days <= 0 {
		// 永久保留:整列归零,GC 的 WHERE 里有 expires_at > 0,归零即永不清理。
		expr, args = "0", nil
	}
	err := gdb.WithContext(ctx).Model(&Capture{}).Where("task_id = ?", after.Id).
		UpdateColumn("expires_at", gorm.Expr(expr, args...)).Error
	if err != nil {
		db.MarkWatchFailure(err)
	}
	return err
}

func samePtrInt(a, b *int) bool {
	if a == nil || b == nil {
		return a == nil && b == nil
	}
	return *a == *b
}

// adminStopTask 手动停止一个监听任务。
func adminStopTask(c *gin.Context) {
	// 停止不需要 ctx 也不需要句柄:它是一次纯状态迁移,库那一侧的写由
	// transitionTask 统一完成。参数写成 `_` 而不是收下不用 —— 后者会被
	// qianye/ctx_guard_test.go 判红,那条守卫防的是"接了 ctx 却没往语句上挂"。
	transitionTask(c, actionTaskStop, func(_ context.Context, _ *gorm.DB, before *Task) (Task, error) {
		if before.Status != StatusRunning {
			// 已经停了的任务再停一次是幂等的,直接回当前状态。把它做成错误
			// 只会让"两个管理员同时点停止"里的一个看到一句吓人的红字。
			return *before, nil
		}
		after := *before
		after.Status = StatusStopped
		after.StoppedAt = common.GetTimestamp()
		after.StoppedReason = ReasonManual
		return after, nil
	})
}

// adminStartTask 重新启动一个已停止的任务。
//
// 倒计时形态在这里**重新计时**:管理员按下"启动"的语义就是"从现在起再盯这么久"。
// 编辑接口刻意不重算(见 resolveWindow),两者的分工必须泾渭分明,否则改个
// 备注就会悄悄把剩余时间重置成满格。
func adminStartTask(c *gin.Context) {
	transitionTask(c, actionTaskStart, func(ctx context.Context, gdb *gorm.DB, before *Task) (Task, error) {
		if before.Status == StatusRunning {
			return *before, nil
		}
		after := *before
		now := common.GetTimestamp()
		switch {
		case after.WindowMode == WindowCountdown && after.CountdownSeconds > 0:
			after.StartsAt, after.EndsAt = now, now+int64(after.CountdownSeconds)
		case after.EndsAt > 0 && after.EndsAt <= now:
			// 固定时间窗已经过去了,重启没有意义:它会立刻被 SweepWindows 再停一次,
			// 而管理员会以为按钮坏了。
			return *before, errNotRestartable
		}
		// 抽满之后重启:条数上限跟着一起放开,否则第一次命中就会立刻再停一次。
		// 不清零 captured —— 那会让"这个任务一共抓了多少条"失去答案,而记录还在库里。
		if after.MaxRecords > 0 && after.Captured >= after.MaxRecords {
			return *before, errAlreadyFull
		}
		if maxActive := config.Get().RiskWatch.MaxActiveTasks; maxActive > 0 {
			n, err := countActive(ctx, gdb)
			if err != nil {
				return *before, err
			}
			if n >= int64(maxActive) {
				return *before, errTooManyActive
			}
		}
		after.Status = StatusRunning
		after.StoppedAt, after.StoppedReason = 0, ""
		return after, nil
	})
}

// transitionTask 是"读一行 → 算出新状态 → 带版本写回 → 两处审计 → 重载快照"
// 这条链路的唯一实现。
//
// 停止与启动共用它不是为了省行数,而是因为其中三步(乐观锁、失败留痕、
// 重载快照)每一步漏掉都不会报错:漏了乐观锁是两个管理员互相覆盖,漏了失败
// 留痕是被拒的操作零痕迹,漏了重载是"我明明停了它还在记"。
func transitionTask(c *gin.Context, action string, next func(context.Context, *gorm.DB, *Task) (Task, error)) {
	if !guard.RequireAPI(c, guard.FlagRiskWatch) {
		return
	}
	id, ok := httpq.PathInt64(c, "id")
	if !ok {
		respondErr(c, errInvalidParam)
		return
	}
	version := httpq.Int(c, "version", -1)

	ctx, cancel := guard.ColdContext(c.Request.Context())
	defer cancel()
	gdb, err := handle(ctx)
	if err != nil {
		respondErr(c, err)
		return
	}
	before, err := loadTask(ctx, gdb, id)
	if err != nil {
		respondErr(c, err)
		return
	}
	// version 是可选的:停止/启动是幂等动作,而"停止"这个意图不会因为别人
	// 刚改过备注就失效。传了就校验 —— 前端列表页拿得到 version,校验能让
	// "我点的是那一行"这件事有据可查。
	if version >= 0 && before.Version != version {
		respondErr(c, errConflict)
		return
	}

	after, err := next(ctx, gdb, &before)
	if err != nil {
		audit.WriteConfigUpdate(c, audit.ConfigChange{
			Action: action, Result: qymodel.ResultFail,
			Reason: err.Error(), Before: snapshotOf(before),
		})
		respondErr(c, err)
		return
	}
	if after.Status == before.Status {
		respondOK(c, before) // 幂等:状态没变就不写库、不留痕。
		return
	}
	after.UpdatedAt = common.GetTimestamp()
	after.Version = before.Version + 1

	res := gdb.Model(&Task{}).Where("id = ? AND version = ?", id, before.Version).Updates(map[string]any{
		"status":         after.Status,
		"starts_at":      after.StartsAt,
		"ends_at":        after.EndsAt,
		"stopped_at":     after.StoppedAt,
		"stopped_reason": after.StoppedReason,
		"updated_at":     after.UpdatedAt,
		"version":        after.Version,
	})
	if res.Error != nil || res.RowsAffected == 0 {
		if res.Error != nil {
			db.MarkWatchFailure(res.Error)
		}
		audit.WriteConfigUpdate(c, audit.ConfigChange{
			Action: action, Result: qymodel.ResultFail,
			Reason: errConflict.Error(), Before: snapshotOf(before), After: snapshotOf(after),
		})
		respondErr(c, errConflict)
		return
	}
	audit.WriteConfigUpdate(c, audit.ConfigChange{
		Action: action, Before: snapshotOf(before), After: snapshotOf(after),
	})
	_ = Reload()
	respondOK(c, after)
}

// adminDeleteTask 删除一个任务连同它的全部记录。
//
// 硬删而不是软删,并且**连带删记录**:留着一堆没有任务归属的记录,既查不到
// 是谁为什么抓的,又会一直占着这个存储节点 —— 而它们的保留期本来就跟着任务走。
// "删的是哪一个"由审计的 before 快照回答。
func adminDeleteTask(c *gin.Context) {
	if !guard.RequireAPI(c, guard.FlagRiskWatch) {
		return
	}
	id, ok := httpq.PathInt64(c, "id")
	if !ok {
		respondErr(c, errInvalidParam)
		return
	}
	ctx, cancel := guard.ColdContext(c.Request.Context())
	defer cancel()
	gdb, err := handle(ctx)
	if err != nil {
		respondErr(c, err)
		return
	}
	before, err := loadTask(ctx, gdb, id)
	if err != nil {
		respondErr(c, err)
		return
	}

	// 记录先删、任务后删,而且**不放进一个事务**:一次删除可能涉及上百万行,
	// 把它包进事务会在这个正在被热路径写入的库上持有一个巨大的 undo。
	// 中途失败留下的是"任务还在、记录少了一批",下一次点删除会继续 ——
	// 而反过来(任务没了、记录还在)才是查不清的那种残留。
	if err := gdb.Where("task_id = ?", id).Delete(&Capture{}).Error; err != nil {
		db.MarkWatchFailure(err)
		audit.WriteConfigUpdate(c, audit.ConfigChange{
			Action: actionTaskDelete, Result: qymodel.ResultFail,
			Reason: err.Error(), Before: snapshotOf(before),
		})
		respondErr(c, err)
		return
	}
	if err := gdb.Where("id = ?", id).Delete(&Task{}).Error; err != nil {
		db.MarkWatchFailure(err)
		audit.WriteConfigUpdate(c, audit.ConfigChange{
			Action: actionTaskDelete, Result: qymodel.ResultFail,
			Reason: err.Error(), Before: snapshotOf(before),
		})
		respondErr(c, err)
		return
	}
	audit.WriteConfigUpdate(c, audit.ConfigChange{Action: actionTaskDelete, Before: snapshotOf(before)})
	_ = Reload()
	respondOK(c, gin.H{"deleted": true})
}

// ───────────────────────────── 记录 ─────────────────────────────

// captureBrief 是列表页的一行,**不含正文**。
//
// 正文只在详情接口下发。列表带正文的话,一页 50 条就是一次几百 KB 的响应,
// 而管理员在列表上要看的只是"什么时候、哪把密钥、什么模型" —— 正文是点进去
// 才看的东西。它同时是一道很自然的权限边界:翻列表不等于读了每一条的内容。
type captureBrief struct {
	Id           int64  `json:"id"`
	TaskId       int64  `json:"task_id"`
	UserId       int    `json:"user_id"`
	Username     string `json:"username"`
	TokenId      int    `json:"token_id"`
	TokenName    string `json:"token_name"`
	UserGroup    string `json:"user_group"`
	ModelName    string `json:"model_name"`
	RequestId    string `json:"request_id"`
	ClientIP     string `json:"client_ip"`
	IsStream     bool   `json:"is_stream"`
	PromptTokens int    `json:"prompt_tokens"`
	ContentChars int    `json:"content_chars"`
	Truncated    bool   `json:"truncated"`
	HasFiles     bool   `json:"has_files"`
	CreatedAt    int64  `json:"created_at"`
	ExpiresAt    int64  `json:"expires_at"`
}

// adminListCaptures 下发监听记录列表。
func adminListCaptures(c *gin.Context) {
	if !guard.RequireAPI(c, guard.FlagRiskWatch) {
		return
	}
	ctx, cancel := guard.ColdContext(c.Request.Context())
	defer cancel()
	gdb, err := handle(ctx)
	if err != nil {
		respondErr(c, err)
		return
	}

	page, size := httpq.Paginate(c, httpq.Spec{DefaultSize: 20, MaxSize: maxCaptureListSize})
	q := gdb.Model(&Capture{})
	if taskId := httpq.Int64(c, "task_id", 0); taskId > 0 {
		q = q.Where("task_id = ?", taskId)
	}
	if userId := httpq.Int(c, "user_id", 0); userId > 0 {
		q = q.Where("user_id = ?", userId)
	}
	if m := strings.TrimSpace(c.Query("model_name")); m != "" {
		q = q.Where("model_name = ?", truncate(m, 128))
	}
	if start := httpq.Int64(c, "start_ts", 0); start > 0 {
		q = q.Where("created_at >= ?", start)
	}
	if end := httpq.Int64(c, "end_ts", 0); end > 0 {
		q = q.Where("created_at <= ?", end)
	}

	var total int64
	if err := q.Count(&total).Error; err != nil {
		db.MarkWatchFailure(err)
		respondErr(c, err)
		return
	}
	rows := make([]Capture, 0, size)
	// 显式选列而不是 SELECT *:content 是这张表上唯一的大字段,列表页不需要它,
	// 而一次 50 行的全列查询会把它整段读进内存再丢掉。
	if err := q.Select("id, task_id, user_id, username, token_id, token_name, user_group, " +
		"model_name, request_id, client_ip, is_stream, prompt_tokens, content_chars, truncated, " +
		"files, created_at, expires_at").
		// 排序键是 (created_at desc, id desc):两列复合索引 idx_..._task /
		// idx_..._user 的第二列就是 created_at,这样"按任务/按用户筛 + 翻页"
		// 直接吃索引。id 兜住同一秒内的全序 —— 缺了它,同一秒写入的几条记录
		// 在不同执行计划下顺序会变,翻页时会漏行或重复行。
		Order("created_at desc, id desc").Offset(httpq.Offset(page, size)).Limit(size).Find(&rows).Error; err != nil {
		db.MarkWatchFailure(err)
		respondErr(c, err)
		return
	}
	items := make([]captureBrief, 0, len(rows))
	for _, r := range rows {
		items = append(items, captureBrief{
			Id: r.Id, TaskId: r.TaskId, UserId: r.UserId, Username: r.Username,
			TokenId: r.TokenId, TokenName: r.TokenName,
			UserGroup: r.UserGroup, ModelName: r.ModelName,
			RequestId: r.RequestId, ClientIP: r.ClientIP,
			IsStream: r.IsStream, PromptTokens: r.PromptTokens,
			ContentChars: r.ContentChars, Truncated: r.Truncated,
			HasFiles:  r.Files != "",
			CreatedAt: r.CreatedAt, ExpiresAt: r.ExpiresAt,
		})
	}
	respondOK(c, gin.H{"items": items, "total": total, "page": page, "page_size": size})
}

// adminGetCapture 下发一条记录的完整内容。
func adminGetCapture(c *gin.Context) {
	if !guard.RequireAPI(c, guard.FlagRiskWatch) {
		return
	}
	id, ok := httpq.PathInt64(c, "id")
	if !ok {
		respondErr(c, errInvalidParam)
		return
	}
	ctx, cancel := guard.ColdContext(c.Request.Context())
	defer cancel()
	gdb, err := handle(ctx)
	if err != nil {
		respondErr(c, err)
		return
	}
	var row Capture
	if err := gdb.Where("id = ?", id).Take(&row).Error; err != nil {
		// 判据是 GORM 的哨兵而不是错误文本:文本随驱动与版本变,而"这条记录
		// 已过保留期被清理"与"数据库出错了"在页面上要说两句完全不同的话。
		if errors.Is(err, gorm.ErrRecordNotFound) {
			respondErr(c, errCaptureNotFound)
			return
		}
		db.MarkWatchFailure(err)
		respondErr(c, err)
		return
	}
	respondOK(c, row)
}

// adminStats 是这一页顶部那几个数字。
//
// 它同时下发存储节点的健康读数:这个功能挂掉之后最常见的表现是"列表一直是空的",
// 而空列表与"这个用户很干净"长得一模一样 —— 页面必须能分辨这两件事。
func adminStats(c *gin.Context) {
	if !guard.RequireAPI(c, guard.FlagRiskWatch) {
		return
	}
	ctx, cancel := guard.ColdContext(c.Request.Context())
	defer cancel()
	gdb, err := handle(ctx)
	if err != nil {
		respondErr(c, err)
		return
	}

	var running, tasks, captures int64
	if err := gdb.Model(&Task{}).Count(&tasks).Error; err != nil {
		db.MarkWatchFailure(err)
		respondErr(c, err)
		return
	}
	if err := gdb.Model(&Task{}).Where("status = ?", StatusRunning).Count(&running).Error; err != nil {
		db.MarkWatchFailure(err)
		respondErr(c, err)
		return
	}
	if err := gdb.Model(&Capture{}).Count(&captures).Error; err != nil {
		db.MarkWatchFailure(err)
		respondErr(c, err)
		return
	}
	respondOK(c, gin.H{
		"tasks":              tasks,
		"running":            running,
		"captures":           captures,
		"max_active_tasks":   config.Get().RiskWatch.MaxActiveTasks,
		"max_retention_days": config.Get().RiskWatch.MaxRetentionDays,
		"default_retention":  config.Get().RiskWatch.RetentionDays,
		"capture_max_chars":  config.Get().RiskWatch.CaptureMaxChars,
		"store":              db.WatchStats(),
	})
}
