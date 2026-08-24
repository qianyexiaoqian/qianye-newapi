package twophase

import (
	"context"
	"fmt"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/qianye/config"
	"github.com/QuantumNous/new-api/qianye/db"
	qymodel "github.com/QuantumNous/new-api/qianye/model"
	"github.com/QuantumNous/new-api/qianye/service/audit"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// resolverRegistry 让各业务模块注册"补偿成功后要补做什么"。
//
// 补偿任务只负责判定主库到底动没动;至于动了之后该扣哪本佣金账、
// 该把哪张提现单标成已到账,只有业务模块自己知道。
var resolverRegistry = map[string]Resolver{}

// Resolver 在补偿任务确认主库已生效后,补做扩展库侧的收尾。
// 必须幂等:同一单可能被补偿多次。
type Resolver func(ctx context.Context, order *qymodel.FundOrder) error

// RegisterResolver 由各业务模块在初始化时调用。
func RegisterResolver(kind string, r Resolver) { resolverRegistry[kind] = r }

// Compensate 扫描停在 pending 的资金单并推进它们。
//
// 这是整个资金系统的安全网:任何"主库已提交但扩展库没回写"的中间态,
// 最终都由它收敛。没有它,那些单会永远停在 pending,钱动了却查不出来。
//
// 补偿链路上的每一条语句都用租约 ctx,包括 finalizeFailed / markUncertain / backoff
// 这些状态回写:这里与 Execute 的收尾路径不同 —— 租约丢失意味着**别的节点已经接管**,
// 继续写就是双跑。写不进去的单仍是 pending,下一轮(由接管节点或本节点重新拿到租约后)
// 会原样重扫,不会丢。
func Compensate(ctx context.Context) {
	cfg := config.Get().TwoPhase
	grace := int64(cfg.PendingGraceSeconds)
	batch := cfg.BatchSize
	if batch <= 0 {
		batch = 200
	}
	now := common.GetTimestamp()

	var orders []qymodel.FundOrder
	// 扫描范围是**两个**未定局状态,不是只有 pending:
	// in_doubt(主库 COMMIT 断连)同样等着探针给结论,漏掉它等于把那些单
	// 永远钉在原地 —— 而它们恰恰是"钱可能已经动了"的那一批。
	err := db.Get().WithContext(ctx).
		Where("status IN ? AND updated_at < ? AND next_probe_at <= ?",
			qymodel.UnsettledStatuses(), now-grace, now).
		Order("id asc").Limit(batch).Find(&orders).Error
	if err != nil {
		db.MarkFailure(err)
		common.SysError("qianye: 补偿任务扫描失败: " + err.Error())
		return
	}
	if len(orders) > 0 {
		common.SysLog(fmt.Sprintf("qianye: 补偿任务发现 %d 笔待确认资金单", len(orders)))
	}

	for i := range orders {
		// 失去租约后必须立刻停手,否则会与接管节点双跑。
		if ctx.Err() != nil {
			return
		}
		compensateOne(ctx, &orders[i])
	}

	// Uncertain 的两个出口(自动复判 + 积压告警)跟着同一个租约跑:
	// 它们与上面的收敛共用主库探针,分成两个租约只会让同一批单被两处并发探。
	reprobeUncertain(ctx)
	_ = alarmOnBacklog(ctx)
}

func compensateOne(ctx context.Context, order *qymodel.FundOrder) {
	cfg := config.Get().TwoPhase

	if !cfg.OutboxEnabled() {
		// 没有 outbox 探针时无法区分"主库没动"和"主库动了但记录丢了"。
		// 这种情况一律转人工,绝不猜测 —— 猜错就是资损。
		markUncertain(ctx, order, "未启用主库 outbox 探针,无法自动判定,请人工核对")
		return
	}

	applied, err := model.QyProbeFundOutbox(order.OrderNo)
	if err != nil {
		// 主库不可用只退避,绝不改状态。
		backoff(ctx, order, err)
		return
	}

	if applied {
		resolveApplied(ctx, order, qymodel.UnsettledStatuses(), "补偿任务确认主库已生效")
		return
	}

	// 主库确定没动。但要等足够久才敢判失败 —— 可能只是主库事务还没提交。
	age := common.GetTimestamp() - order.CreatedAt
	if age > int64(cfg.ManualReviewAfterSeconds) {
		finalizeFailed(ctx, order)
		return
	}
	backoff(ctx, order, nil)
}

// resolveApplied 在探针确认主库已生效后收尾。
//
// from 是允许被推进的来源状态集合:常规补偿传 UnsettledStatuses(pending / in_doubt),
// Uncertain 的自动复判传 {StatusUncertain}。用参数而不是写死,是因为这两条路径
// 的 CAS 归属完全不同,而收尾动作一字不差。
func resolveApplied(ctx context.Context, order *qymodel.FundOrder, from []int8, reason string) {
	// 提交后收尾必须排在 Resolver 之前:主库额度已经变了,用户缓存此刻就是错的。
	// Resolver 失败会走 backoff 再等一轮,缓存没有理由陪着一起等。
	runPostCommit(ctx, db.Get(), order)

	if r, ok := resolverRegistry[order.Kind]; ok {
		if err := r(ctx, order); err != nil {
			backoff(ctx, order, err)
			return
		}
	}
	now := common.GetTimestamp()
	res := db.Get().WithContext(ctx).Model(&qymodel.FundOrder{}).
		Where("order_no = ? AND status IN ?", order.OrderNo, from).
		Updates(map[string]any{
			"status":     qymodel.StatusSuccess,
			"settled_at": now,
			"updated_at": now,
		})
	if res.Error != nil {
		db.MarkFailure(res.Error)
		return
	}
	if res.RowsAffected == 0 {
		return // 业务线程或管理员已抢先处理
	}
	order.Status = qymodel.StatusSuccess
	common.SysLog(fmt.Sprintf("qianye: 单号 %s 已确认主库生效并完成回写(%s)", order.OrderNo, reason))
	auditTransition(order, qymodel.ResultOK, reason)
}

func finalizeFailed(ctx context.Context, order *qymodel.FundOrder) {
	now := common.GetTimestamp()
	res := db.Get().WithContext(ctx).Model(&qymodel.FundOrder{}).
		Where("order_no = ? AND status IN ?", order.OrderNo, qymodel.UnsettledStatuses()).
		Updates(map[string]any{
			"status":     qymodel.StatusFailed,
			"last_error": "补偿任务确认主库未生效",
			"updated_at": now,
		})
	if res.Error != nil {
		db.MarkFailure(res.Error)
		return
	}
	if res.RowsAffected == 0 {
		return
	}
	order.Status = qymodel.StatusFailed
	auditTransition(order, qymodel.ResultFail, "补偿任务确认主库未生效")
}

// backoff 指数退避,防止一条坏单反复打爆主库。
// 重试次数耗尽后转 Uncertain 交人工,而不是无限重试。
func backoff(ctx context.Context, order *qymodel.FundOrder, cause error) {
	cfg := config.Get().TwoPhase
	attempts := order.Attempts + 1

	if attempts >= cfg.MaxProbeAttempts {
		reason := "探针重试次数已耗尽"
		if cause != nil {
			reason += ": " + cause.Error()
		}
		markUncertain(ctx, order, reason)
		return
	}

	delay := int64(1) << uint(min(attempts, 8)) // 2,4,8,...,256 秒
	if delay > 300 {
		delay = 300
	}
	now := common.GetTimestamp()
	updates := map[string]any{
		"attempts":      attempts,
		"next_probe_at": now + delay,
		"updated_at":    now,
	}
	if cause != nil {
		// 用 rune 安全截断:last_error 是 utf8mb4 的 varchar(512),
		// 裸字节切会把中文错误信息切出非法尾巴,整条 UPDATE 被 1366 拒绝,
		// 退避信息与重试计数一起丢失。
		updates["last_error"] = audit.Truncate(cause.Error(), maxErrBytes)
	}
	if err := db.Get().WithContext(ctx).Model(&qymodel.FundOrder{}).
		Where("order_no = ?", order.OrderNo).Updates(updates).Error; err != nil {
		db.MarkFailure(err)
	}
}

// markUncertain 把单据转入人工裁决。
//
// 资金系统必须有"我不知道,交给人"这个合法出口。
// 没有它,补偿任务只能在"重试到死"和"猜一个结果"之间选,两者都不可接受。
func markUncertain(ctx context.Context, order *qymodel.FundOrder, reason string) {
	now := common.GetTimestamp()
	// 转人工是这套系统里最不能丢的一条记录:理由被裸字节切断会让整行 UPDATE
	// 与审计写入双双被 utf8mb4 列拒绝,单据留在 pending 继续被补偿任务空转。
	reason = audit.Truncate(reason, maxErrBytes)
	res := db.Get().WithContext(ctx).Model(&qymodel.FundOrder{}).
		Where("order_no = ? AND status IN ?", order.OrderNo, qymodel.UnsettledStatuses()).
		Updates(map[string]any{
			"status":     qymodel.StatusUncertain,
			"last_error": reason,
			// 复判节奏由 next_probe_at 承载:进 uncertain 之后不必立刻再探一次
			// (刚刚就是探不出来才进来的),等一个人工复核周期再说。
			"next_probe_at": now + int64(config.Get().TwoPhase.ManualReviewAfterSeconds),
			"updated_at":    now,
		})
	if res.Error != nil {
		db.MarkFailure(res.Error)
		return
	}
	if res.RowsAffected == 0 {
		return
	}
	order.Status = qymodel.StatusUncertain
	// 这是需要人介入的异常,必须显式告警而不只是写库。
	common.SysError(fmt.Sprintf(
		"qianye: 资金单 %s 已转入人工裁决(用户 %d,金额 %d): %s",
		order.OrderNo, order.UserId, order.AmountQuota, reason))
	audit.Write(nil, audit.Entry{
		TraceNo:      order.OrderNo,
		Category:     qymodel.AuditCategoryFund,
		Action:       "fund." + order.Kind + ".uncertain",
		ActorType:    qymodel.ActorSystem,
		ActorUserId:  order.UserId,
		TargetUserId: order.PeerUserId,
		AmountQuota:  order.AmountQuota,
		Result:       qymodel.ResultPending,
		Reason:       reason,
	})
}

// reprobeUncertain 是 Uncertain 的**自动出口**:探针恢复后重新判一次。
//
// # 为什么必须有它
//
// 一张单进 Uncertain 的原因几乎总是暂时的:主库当时不可用、探针查询报错、
// 退避次数在一次网络抖动里被耗光。原因消失之后,单据却仍停在 Uncertain,
// 而 Compensate 只扫未定局态 —— 于是一笔本来五秒钟就能判清的钱,要等一个人
// 打开对账台、按下按钮。一个只有人工出口的状态,在运营眼里就等于"钱被困住"。
//
// # 为什么敢自动改判
//
// 判据没有变松:仍然只认 MainApplied 与 MainNotApplied 两个**确定**取值,
// MainUnknown(探针关掉、查询报错、单据缺失)一律原地不动。
// MainNotApplied 的可信度依赖 PruneOutbox 只清 Success 单那条不变量 —— 与
// compensateOne 用的是同一条判据、同一份证据。
//
// CAS 从 Uncertain 出发,因此永远不会推翻管理员刚刚下的裁决:人先按下按钮,
// 这里的 RowsAffected 就是 0。
//
// 节奏由 next_probe_at 承载,一个人工复核周期一次:探针要打主库,
// 而 Uncertain 单在库里是常驻的,每 30 秒全表探一遍等于给主库加一份常态负载。
func reprobeUncertain(ctx context.Context) {
	cfg := config.Get().TwoPhase
	if !cfg.OutboxEnabled() {
		// 没有探针就没有任何新证据可言,复判只会空转。
		return
	}
	gdb := db.Get()
	if gdb == nil {
		return
	}
	batch := cfg.BatchSize
	if batch <= 0 {
		batch = 200
	}
	now := common.GetTimestamp()

	var orders []qymodel.FundOrder
	if err := gdb.WithContext(ctx).
		Where("status = ? AND next_probe_at <= ?", qymodel.StatusUncertain, now).
		Order("id asc").Limit(batch).Find(&orders).Error; err != nil {
		db.MarkFailure(err)
		common.SysError("qianye: 人工裁决单复判扫描失败: " + err.Error())
		return
	}

	for i := range orders {
		if ctx.Err() != nil {
			return
		}
		order := &orders[i]
		// 先把下一次复判推远,无论这次判出什么:探针报错时若不推远,
		// 主库持续不可用会让每一轮补偿都把整批 Uncertain 单再探一遍。
		if err := gdb.WithContext(ctx).Model(&qymodel.FundOrder{}).
			Where("order_no = ? AND status = ?", order.OrderNo, qymodel.StatusUncertain).
			Update("next_probe_at", now+int64(cfg.ManualReviewAfterSeconds)).Error; err != nil {
			db.MarkFailure(err)
			continue
		}

		switch ProbeMainSide(order) {
		case MainApplied:
			resolveApplied(ctx, order, []int8{qymodel.StatusUncertain}, "探针复判确认主库已生效")
		case MainNotApplied:
			resolveNotApplied(ctx, order)
		default:
			// 仍然判不出来。原地不动等下一轮或等人 —— 绝不猜。
		}
	}
}

// resolveNotApplied 在复判确认主库未生效后把 Uncertain 单落回 Failed。
//
// 这是"钱确实没动"的正式宣告,业务侧据此回滚预占/解冻佣金(各模块的对账任务
// 会看到 Failed 并再探一次针)。只有 MainNotApplied 这一个确定取值能走到这里。
func resolveNotApplied(ctx context.Context, order *qymodel.FundOrder) {
	const reason = "探针复判确认主库未生效"
	now := common.GetTimestamp()
	res := db.Get().WithContext(ctx).Model(&qymodel.FundOrder{}).
		Where("order_no = ? AND status = ?", order.OrderNo, qymodel.StatusUncertain).
		Updates(map[string]any{
			"status":     qymodel.StatusFailed,
			"last_error": reason,
			"updated_at": now,
		})
	if res.Error != nil {
		db.MarkFailure(res.Error)
		return
	}
	if res.RowsAffected == 0 {
		return // 管理员已抢先裁决
	}
	order.Status = qymodel.StatusFailed
	common.SysLog(fmt.Sprintf("qianye: 单号 %s %s,已自动落 failed", order.OrderNo, reason))
	auditTransition(order, qymodel.ResultFail, reason)
}

// backlogAlarmIntervalSeconds 是积压告警的最小间隔。
//
// 补偿任务默认 30 秒一轮,不限流的话一张卡住的单一天能刷 2880 条 SysError,
// 把真正的新异常淹掉 —— 那与没有告警等价。一小时一次既能进值班视野,
// 又不会盖住别的日志。
const backlogAlarmIntervalSeconds = 3600

// backlogAlarmKey 是积压告警静默期在共享 qy_kv 表里的键。
//
// 静默期**必须落在库上,不能是包级变量**。原先那句注释("只被持有租约的那一个
// 节点读写")的前提不成立:lease.runOnce 每轮结束都主动 Release(lease_until 归 0),
// 下一轮谁先 tick 谁抢到,租约在节点之间自由漂移 —— 实测两个进程各持有自己的
// 计时器,同一条告警在 2 分钟内被喊了两次,而间隔常量写的是 1 小时。
// 多节点部署下静默期因此退化成"每节点每小时一条",N 个节点就是 N 倍;
// 进程重启也会立刻再喊一次,滚动发布时叠加。
const backlogAlarmKey = "twophase.backlog_alarm_at"

// BacklogSnapshot 是一次积压告警的内容。
type BacklogSnapshot struct {
	Count       int64
	AmountQuota int64
	OldestNo    string
	OldestAge   int64
}

// alarmOnBacklog 周期性地把"有多少钱正卡在人工队列里"喊出来。
// 返回本轮实际喊出去的内容;nil 表示这一轮没喊(没积压,或还在静默期内)。
//
// 只有 Stats() 是不够的:那是拉取式的,要有人主动打开对账台才看得到。
// 一笔停在 Uncertain 的资金单意味着一个用户的钱既没到账也没退回,
// 它必须能主动出现在值班的日志流里。
//
// **静默期只在真的喊过之后才起算。** 早期实现在"查到 0 笔"时也盖了时间戳,
// 于是一笔刚刚卡住的资金单最多要等一个静默期(1 小时)才会被喊出来 ——
// 而绝大多数轮次都是 0 笔,这等于把告警的响应时间从"下一轮"退化成"下一小时"。
// 代价是每轮多一条走索引的 COUNT,那点开销换不回一小时的静默。
func alarmOnBacklog(ctx context.Context) *BacklogSnapshot {
	now := common.GetTimestamp()
	gdb := db.Get()
	if gdb == nil {
		return nil
	}
	var count int64
	if err := gdb.WithContext(ctx).Model(&qymodel.FundOrder{}).
		Where("status = ?", qymodel.StatusUncertain).Count(&count).Error; err != nil {
		db.MarkFailure(err)
		return nil
	}
	if count == 0 {
		return nil
	}
	// 抢静默期的槽位 —— 抢不到说明**集群里已经有节点在这一小时里喊过了**。
	// 顺序刻意是"先数,后抢":静默期只在真的有积压时才起算,否则绝大多数
	// 0 笔的轮次会把响应时间从"下一轮"退化成"下一小时"。
	if !claimBacklogAlarmSlot(gdb.WithContext(ctx), now) {
		return nil
	}

	snap := &BacklogSnapshot{Count: count}
	if err := gdb.WithContext(ctx).Model(&qymodel.FundOrder{}).
		Where("status = ?", qymodel.StatusUncertain).
		Select("COALESCE(SUM(amount_quota), 0)").Scan(&snap.AmountQuota).Error; err != nil {
		db.MarkFailure(err)
	}
	var oldest qymodel.FundOrder
	if err := gdb.WithContext(ctx).Where("status = ?", qymodel.StatusUncertain).
		Order("created_at asc").First(&oldest).Error; err == nil {
		snap.OldestNo = oldest.OrderNo
		snap.OldestAge = now - oldest.CreatedAt
	}
	common.SysError(fmt.Sprintf(
		"qianye: 资金对账台积压 %d 笔待人工裁决的资金单,合计 %d 额度,最久的一笔是 %s(%d 秒)",
		snap.Count, snap.AmountQuota, snap.OldestNo, snap.OldestAge))
	return snap
}

// claimBacklogAlarmSlot 用 qy_kv 上的一次条件更新抢下"这一小时的告警权"。
//
// 判据放在 updated_at(bigint)而不是 v(text):三个方言对 text 的数值比较规则
// 各不相同,而这一行的语义本来就是"上次喊的时间"。RowsAffected == 1 才算抢到,
// 与本仓所有跨节点 CAS 同形。
//
// 抢不到(别的节点刚喊过、或库不可用)一律返回 false —— 宁可少喊一条,
// 也不要在多节点上把同一条告警喊 N 遍,那正是这个静默期要压住的东西。
// gdb 必须是**已经绑好 ctx 的句柄**(调用点传 gdb.WithContext(ctx)):
// 补偿任务的 ctx 就是租约,丢了租约还写就是双跑。
func claimBacklogAlarmSlot(gdb *gorm.DB, now int64) bool {
	seed := qymodel.KV{K: backlogAlarmKey, V: "backlog alarm silence window", UpdatedAt: 0}
	if err := gdb.Clauses(clause.OnConflict{DoNothing: true}).
		Create(&seed).Error; err != nil {
		db.MarkFailure(err)
		return false
	}
	res := gdb.Model(&qymodel.KV{}).
		Where("k = ? AND updated_at <= ?", backlogAlarmKey, now-backlogAlarmIntervalSeconds).
		Update("updated_at", now)
	if res.Error != nil {
		db.MarkFailure(res.Error)
		return false
	}
	return res.RowsAffected == 1
}

// PruneOutbox 清理主库 outbox 中已成功单的历史行,避免无限增长。
//
// # 只清 Success 单,而且是逐行判定
//
// 探针行是"主库那一笔钱到底动没动"的唯一权威证据(见 ProbeMainSide)。
// 需要这条证据的恰恰是**非 Success** 的单:InDoubt 单就是 commit 阶段断连留下的,
// 钱到底动没动完全靠这一行说话;存量 Failed 单同样混着这种误判;
// Pending / Uncertain 更是等着人或补偿任务来判。
// 已经 Success 的单没有人会再去问"动没动",它们的探针行才是可以丢的历史。
//
// 旧实现按 created_at 一刀切,并只用一个全局闸门(存在早于保留期的 Pending/Uncertain
// 单就整体暂缓)来"保护"证据。这个闸门恰好漏掉了 Failed —— 而 Failed 正是探针存在的
// 唯一理由。后果是:保留期一到,一笔 commit 断连的出款就失去了全部证据,
// ProbeMainSide 只能读出"没生效",管理端一按重试就把同一笔奖金再发一次,
// 且账上不留任何痕迹。所以判据必须从"全局 + 时间"改成"逐行 + 资金单状态"。
//
// 顺带修掉的副作用:全局闸门意味着一笔卡住的 Pending 单会让整张表永远停止清理。
// 逐行判定严格更精确,不需要那道闸门。
//
// 查不到对应资金单的探针行一律保留:那是"主库动过钱但扩展库没有对应记账"的孤儿证据,
// 删掉它等于把一笔说不清的资金变更从系统里彻底抹掉。
func PruneOutbox(ctx context.Context) {
	cfg := config.Get().TwoPhase
	if !cfg.OutboxEnabled() || cfg.OutboxRetentionDays <= 0 {
		return
	}
	before := common.GetTimestamp() - int64(cfg.OutboxRetentionDays)*86400

	// 扩展库不可用时一行都不能删:判据全在那一侧,读不到就等于不知道哪些行
	// 已经没用了 —— 那正是"按时间盲删"的老毛病。
	gdb := db.Get()
	if gdb == nil {
		return
	}

	batch := cfg.BatchSize
	if batch <= 0 {
		batch = 200
	}

	// 分批循环而不是一轮一批:任务间隔是 6 小时,单轮只删 batch 行时,
	// 日均资金笔数一旦超过 batch,outbox 就是净增长,保留期永远追不上。
	// 上限 maxPruneRounds 是为了让单轮工作量有界(默认 200×50 = 1 万行/轮),
	// 每轮之间让出并检查租约:失去租约后继续删会与接管节点双跑。
	const maxPruneRounds = 50
	var total, kept, cursor int64
	for i := 0; i < maxPruneRounds; i++ {
		if ctx.Err() != nil {
			break
		}
		rows, err := model.QyScanFundOutbox(before, cursor, batch)
		if err != nil {
			common.SysError("qianye: 扫描主库 outbox 失败: " + err.Error())
			break
		}
		if len(rows) == 0 {
			break
		}
		cursor = rows[len(rows)-1].Id

		nos := make([]string, 0, len(rows))
		for j := range rows {
			nos = append(nos, rows[j].OrderNo)
		}
		var settled []string
		if err := gdb.WithContext(ctx).Model(&qymodel.FundOrder{}).
			Where("order_no IN ? AND status = ?", nos, qymodel.StatusSuccess).
			Pluck("order_no", &settled).Error; err != nil {
			db.MarkFailure(err)
			common.SysError("qianye: 清理 outbox 前查扩展库资金单状态失败: " + err.Error())
			break
		}
		done := make(map[string]struct{}, len(settled))
		for _, no := range settled {
			done[no] = struct{}{}
		}
		ids := make([]int64, 0, len(rows))
		for j := range rows {
			if _, ok := done[rows[j].OrderNo]; ok {
				ids = append(ids, rows[j].Id)
			}
		}
		kept += int64(len(rows) - len(ids))

		deleted, err := model.QyDeleteFundOutbox(ids)
		if err != nil {
			common.SysError("qianye: 清理主库 outbox 失败: " + err.Error())
			break
		}
		total += deleted
		if len(rows) < batch {
			break
		}
		time.Sleep(50 * time.Millisecond)
	}
	if total > 0 || kept > 0 {
		common.SysLog(fmt.Sprintf(
			"qianye: 已清理 %d 行历史 outbox,保留 %d 行(对应资金单尚未成功,探针仍是唯一证据)",
			total, kept))
	}
}

// Stats 汇总两阶段的健康指标,供管理端面板告警。
func Stats() map[string]any {
	m := map[string]any{}
	gdb := db.Get()
	if gdb == nil {
		return m
	}
	var pending, inDoubt, uncertain int64
	gdb.Model(&qymodel.FundOrder{}).Where("status = ?", qymodel.StatusPending).Count(&pending)
	gdb.Model(&qymodel.FundOrder{}).Where("status = ?", qymodel.StatusInDoubt).Count(&inDoubt)
	gdb.Model(&qymodel.FundOrder{}).Where("status = ?", qymodel.StatusUncertain).Count(&uncertain)
	m["pending"] = pending
	// in_doubt 与 pending 分开报:两者都是"未定局",但 in_doubt 意味着
	// **主库 COMMIT 已经发出**,钱很可能已经动了。把它并进 pending 会让
	// 面板上最需要被看见的那一档消失在最常见的那一档里。
	m["in_doubt"] = inDoubt
	m["uncertain"] = uncertain

	// 键名是 unsettled 而不是 pending:这条查询扫的是 UnsettledStatuses()
	// (Pending + InDoubt),而上面那个 pending 计数只数 status=Pending。
	// 叫同一个名字会让面板出现自相矛盾的一屏 —— "待处理 0" 配 "最老待定单已挂
	// 4 分 13 秒 + 单号 XXX",而英文界面下两处逐字都是 pending,运维只能认为
	// 其中一处坏了,然后两处都不信。而这一屏恰恰出现在积压全部落在 in_doubt
	// 那一档(钱最可能已经动过、最需要被看见的那一档)的时候。
	var oldest qymodel.FundOrder
	if err := gdb.Where("status IN ?", qymodel.UnsettledStatuses()).
		Order("created_at asc").First(&oldest).Error; err == nil {
		m["oldest_unsettled_age_sec"] = common.GetTimestamp() - oldest.CreatedAt
		m["oldest_unsettled_order_no"] = oldest.OrderNo
	}
	// 人工队列里最久的那一笔:积压告警看的是同一个数,面板要能自己看到它,
	// 而不是只能等日志里那条一小时一次的 SysError。
	var oldestUncertain qymodel.FundOrder
	if err := gdb.Where("status = ?", qymodel.StatusUncertain).
		Order("created_at asc").First(&oldestUncertain).Error; err == nil {
		m["oldest_uncertain_age_sec"] = common.GetTimestamp() - oldestUncertain.CreatedAt
		m["oldest_uncertain_order_no"] = oldestUncertain.OrderNo
	}
	return m
}

// ResolveManually 落一次管理员的人工裁决。
//
// # 为什么裁决不能只是一条 UPDATE
//
// 原实现直接把 status 从 Uncertain 改成 Success/Failed 就返回。资金单是变了,
// 但业务侧一无所知:violation 的记录会永远停在 charged(该模块根本没有对账任务,
// 它只在请求线程里 confirmRefundSettled 一次),用户缓存也不会失效 ——
// 管理员按下"确认已生效",用户账上的余额在缓存 TTL 内仍是旧的。
//
// 所以裁决成功 = 走与补偿任务**完全相同**的那条收尾链路(PostCommit + Resolver),
// 只是 CAS 的来源状态换成 Uncertain。裁决失败则只落状态:各模块的对账任务看到
// Failed 会自己再探一次针再决定要不要回滚,那条判断不该在这里重做一遍。
//
// 返回 false 表示 CAS 落空(单据已被自动复判或另一个管理员推进),调用方按
// "状态已变化,请刷新"处理。
func ResolveManually(ctx context.Context, order *qymodel.FundOrder, target int8, reason string) (bool, error) {
	gdb := db.Get()
	if gdb == nil {
		return false, db.ErrNotReady
	}
	if target == qymodel.StatusSuccess {
		resolveApplied(ctx, order, []int8{qymodel.StatusUncertain}, reason)
		if order.Status != qymodel.StatusSuccess {
			// Resolver 失败或 CAS 落空。前者已经写进 backoff 的 last_error,
			// 后者说明别人赢了 —— 两种都不能对管理员报"裁决完成"。
			return false, nil
		}
		return true, nil
	}

	now := common.GetTimestamp()
	res := gdb.WithContext(ctx).Model(&qymodel.FundOrder{}).
		Where("order_no = ? AND status = ?", order.OrderNo, qymodel.StatusUncertain).
		Updates(map[string]any{
			"status":     target,
			"settled_at": now,
			"updated_at": now,
			"last_error": audit.Truncate(reason, maxErrBytes),
		})
	if res.Error != nil {
		db.MarkFailure(res.Error)
		return false, res.Error
	}
	if res.RowsAffected == 0 {
		return false, nil
	}
	order.Status = target
	order.SettledAt = now
	return true, nil
}

// Interval 返回补偿任务的执行间隔。
func Interval() time.Duration {
	s := config.Get().TwoPhase.CompensateIntervalSeconds
	if s <= 0 {
		s = 30
	}
	return time.Duration(s) * time.Second
}

// AdjudicateFailedAsApplied 按人工核对的结论,把一张**已经判失败**的资金单改判为已生效。
//
// # 为什么它不能与 ResolveManually 合成一个入口
//
// ResolveManually 只从 Uncertain 出发。那是系统自己说"我不知道"的状态,人做的
// 是替系统把那句话补完。本函数从 **Failed** 出发 —— 那是系统已经给过结论的终态,
// 改写它等于推翻系统的判定。两者的危险程度不同,挂载点也必须不同:前者是通用
// 对账台上的按钮,后者只能挂在"人真的去主库核对过流水"的那一处。
//
// # 存在的理由是一个真实的死角
//
// 资金单被判 Failed,而主库探针说的是"已生效"或"判不出来"(MainApplied /
// MainUnknown)。此时:
//
//   - 补偿任务不扫 Failed(它只扫 UnsettledStatuses),reprobeUncertain 只扫 Uncertain;
//   - 业务侧不敢换代次重开单,那是重复发钱;
//   - 通用对账台的裁决口只收 Uncertain。
//
// 三条路全堵死,这一笔谁都动不了,永远挂在业务侧的挂起态上 —— 钱既没到用户
// 账上,也没有任何人能宣布它到底算不算发过。
//
// # 收尾链路与补偿任务逐字相同
//
// 提交后收尾 + 业务 Resolver + CAS,只把 CAS 的来源状态换成 Failed。重复调用
// 不会重复记账:账本行的一次性由 claimAfterCommit 的 CAS 保证,业务明细的
// 一次性由各模块 Resolver 自己的 CAS 保证。
//
// 返回 false 表示 CAS 落空(单据已被别人推进),调用方按"状态已变化,请刷新"处理。
// order 不是 Failed 时同样返回 false 而不是报错:调用方本来就该先自己判一次状态
// 并给出精确文案,这里只是最后一道不许绕过的前提。
func AdjudicateFailedAsApplied(ctx context.Context, order *qymodel.FundOrder, reason string) (bool, error) {
	if db.Get() == nil {
		return false, db.ErrNotReady
	}
	if order == nil || order.Status != qymodel.StatusFailed {
		return false, nil
	}
	resolveApplied(ctx, order, []int8{qymodel.StatusFailed}, reason)
	return order.Status == qymodel.StatusSuccess, nil
}
