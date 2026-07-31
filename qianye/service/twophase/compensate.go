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
	err := db.Get().WithContext(ctx).
		Where("status = ? AND updated_at < ? AND next_probe_at <= ?",
			qymodel.StatusPending, now-grace, now).
		Order("id asc").Limit(batch).Find(&orders).Error
	if err != nil {
		db.MarkFailure(err)
		common.SysError("qianye: 补偿任务扫描失败: " + err.Error())
		return
	}
	if len(orders) == 0 {
		return
	}
	common.SysLog(fmt.Sprintf("qianye: 补偿任务发现 %d 笔待确认资金单", len(orders)))

	for i := range orders {
		// 失去租约后必须立刻停手,否则会与接管节点双跑。
		if ctx.Err() != nil {
			return
		}
		compensateOne(ctx, &orders[i])
	}
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
		resolveApplied(ctx, order)
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

func resolveApplied(ctx context.Context, order *qymodel.FundOrder) {
	if r, ok := resolverRegistry[order.Kind]; ok {
		if err := r(ctx, order); err != nil {
			backoff(ctx, order, err)
			return
		}
	}
	now := common.GetTimestamp()
	res := db.Get().WithContext(ctx).Model(&qymodel.FundOrder{}).
		Where("order_no = ? AND status = ?", order.OrderNo, qymodel.StatusPending).
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
		return // 业务线程已抢先处理
	}
	order.Status = qymodel.StatusSuccess
	common.SysLog(fmt.Sprintf("qianye: 补偿任务已确认单号 %s 主库生效并完成回写", order.OrderNo))
	auditTransition(order, qymodel.ResultOK, "补偿任务确认主库已生效")
}

func finalizeFailed(ctx context.Context, order *qymodel.FundOrder) {
	now := common.GetTimestamp()
	res := db.Get().WithContext(ctx).Model(&qymodel.FundOrder{}).
		Where("order_no = ? AND status = ?", order.OrderNo, qymodel.StatusPending).
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
		Where("order_no = ? AND status = ?", order.OrderNo, qymodel.StatusPending).
		Updates(map[string]any{
			"status":     qymodel.StatusUncertain,
			"last_error": reason,
			"updated_at": now,
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

// PruneOutbox 清理主库 outbox 中已终态单的历史行,避免无限增长。
func PruneOutbox(ctx context.Context) {
	cfg := config.Get().TwoPhase
	if !cfg.OutboxEnabled() || cfg.OutboxRetentionDays <= 0 {
		return
	}
	before := common.GetTimestamp() - int64(cfg.OutboxRetentionDays)*86400

	// 只清理扩展库侧已终态的单,避免删掉还需要探针判定的记录。
	var stuck int64
	if err := db.Get().WithContext(ctx).Model(&qymodel.FundOrder{}).
		Where("created_at < ? AND status IN ?", before,
			[]int8{qymodel.StatusPending, qymodel.StatusUncertain}).
		Count(&stuck).Error; err != nil {
		db.MarkFailure(err)
		return
	}
	if stuck > 0 {
		common.SysLog(fmt.Sprintf(
			"qianye: 仍有 %d 笔早于保留期的未终态资金单,暂缓清理 outbox", stuck))
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
	var total int64
	for i := 0; i < maxPruneRounds; i++ {
		if ctx.Err() != nil {
			break
		}
		deleted, err := model.QyPruneFundOutbox(before, batch)
		if err != nil {
			common.SysError("qianye: 清理主库 outbox 失败: " + err.Error())
			break
		}
		total += deleted
		if deleted < int64(batch) {
			break
		}
		time.Sleep(50 * time.Millisecond)
	}
	if total > 0 {
		common.SysLog(fmt.Sprintf("qianye: 已清理 %d 行历史 outbox", total))
	}
}

// Stats 汇总两阶段的健康指标,供管理端面板告警。
func Stats() map[string]any {
	m := map[string]any{}
	gdb := db.Get()
	if gdb == nil {
		return m
	}
	var pending, uncertain int64
	gdb.Model(&qymodel.FundOrder{}).Where("status = ?", qymodel.StatusPending).Count(&pending)
	gdb.Model(&qymodel.FundOrder{}).Where("status = ?", qymodel.StatusUncertain).Count(&uncertain)
	m["pending"] = pending
	m["uncertain"] = uncertain

	var oldest qymodel.FundOrder
	if err := gdb.Where("status = ?", qymodel.StatusPending).
		Order("created_at asc").First(&oldest).Error; err == nil {
		m["oldest_pending_age_sec"] = common.GetTimestamp() - oldest.CreatedAt
		m["oldest_pending_order_no"] = oldest.OrderNo
	}
	return m
}

// Interval 返回补偿任务的执行间隔。
func Interval() time.Duration {
	s := config.Get().TwoPhase.CompensateIntervalSeconds
	if s <= 0 {
		s = 30
	}
	return time.Duration(s) * time.Second
}
