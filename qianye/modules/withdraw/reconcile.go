package withdraw

import (
	"context"
	"errors"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/qianye/config"
	"github.com/QuantumNous/new-api/qianye/db"
	qymodel "github.com/QuantumNous/new-api/qianye/model"

	"gorm.io/gorm"
)

// reconcile 收拾跨库兑现留下的中间态。
//
// 覆盖两类卡单:
//
//	A. approved 却没进入兑现 —— 进程崩在"审核通过"与"启动兑现"之间。
//	   重新触发即可,startPaying 的 CAS 天然幂等。
//	B. paying 迟迟不落终态 —— 判定依据只有 qy_fund_orders 的状态,
//	   它背后是主库 outbox 这个唯一精确探针。绝不去 logs 里 LIKE 反查单号:
//	   日志库可能是 ClickHouse(全表扫),而且日志写失败并不代表钱没动。
//
// 顺带做保留期到期的 PII 清理 —— 它同样是"到点才做一次的收尾",
// 与对账共用同一把租约,不必再多一个后台任务。
//
// 三个清理面缺一不可,少一个保留期就是半个摆设:提现单上的收款快照(Payee)、
// 用户保存的收款方式(PayeeAccount,存的是同一份银行卡号密文)、以及明文访问
// 审计(PiiAudit,保留期独立且更长,见 piiAuditInvestigationDays)。
//
// 本任务由 lease.Run 驱动,多节点不会双跑;即便双跑,每一步也都是 CAS。
func reconcile(ctx context.Context) {
	if !config.Get().Withdraw.Enabled {
		return
	}
	gdb := db.Get()
	if gdb == nil {
		return
	}
	batch := config.Get().TwoPhase.BatchSize
	if batch <= 0 {
		batch = 200
	}
	// 复用两阶段的宽限期而不是新造一个配置项:两者判定的是同一件事 ——
	// "这笔跨库操作已经久到不像还在正常执行中了"。
	grace := int64(config.Get().TwoPhase.PendingGraceSeconds)
	if grace <= 0 {
		grace = 60
	}
	stale := common.GetTimestamp() - grace

	resumeApproved(ctx, stale, batch)
	settlePaying(ctx, gdb, stale, batch)
	pruneExpiredPii(ctx, gdb, batch)
}

// resumeApproved 重新拾起卡在 approved 的自动到账单。
func resumeApproved(ctx context.Context, stale int64, batch int) {
	if !config.Get().Withdraw.AutoCredit() {
		return
	}
	var rows []Withdrawal
	err := db.Get().
		Where("status = ? AND method = ? AND updated_at < ?",
			StatusApproved, config.WithdrawMethodQuota, stale).
		Order("id asc").Limit(batch).Find(&rows).Error
	if err != nil {
		db.MarkFailure(err)
		common.SysError("qianye/withdraw: 扫描待兑现单失败: " + err.Error())
		return
	}
	for i := range rows {
		// 失去租约后必须立刻停手,否则会与接管节点双跑。
		if ctx.Err() != nil {
			return
		}
		if err := creditQuota(ctx, &rows[i]); err != nil {
			common.SysError("qianye/withdraw: 重试兑现单号 " + rows[i].WithdrawNo + " 失败: " + err.Error())
		}
	}
}

// settlePaying 依据资金单的终态收尾 paying 单。
func settlePaying(ctx context.Context, gdb *gorm.DB, stale int64, batch int) {
	rows, err := scanStalePaying(gdb, stale, batch)
	if err != nil {
		db.MarkFailure(err)
		common.SysError("qianye/withdraw: 扫描兑现中单失败: " + err.Error())
		return
	}
	for i := range rows {
		if ctx.Err() != nil {
			return
		}
		settleOnePaying(&rows[i])
	}
}

// scanStalePaying 取出需要自动收尾的 paying 单。
//
// `reconcile_state <> hold` 不是可选的过滤条件,而是这条链路的资金安全边界:
// hold 表示"主库结果不可判定,已转人工",它附着在 paying 上 —— 单据的 status
// 仍然是 paying。不排除的话下一轮扫描会重新命中同一张单,而 failWithdrawal 的
// CAS 条件正是 `From: paying`,必然成功:人工裁决被自动流程无声推翻,主库额度
// 已经加过、佣金又回到可用池,同一笔钱可以再提一次。
//
// hold 单只能由三条路径推走:管理员的 resolveHold、补偿任务探到主库已生效后的
// finishPaid,以及这两者共同的前提 —— 有人真的判定了"钱到底动没动"。
func scanStalePaying(gdb *gorm.DB, stale int64, batch int) ([]Withdrawal, error) {
	var rows []Withdrawal
	err := gdb.
		Where("status = ? AND reconcile_state <> ? AND settle_started_at > 0 AND settle_started_at < ?",
			StatusPaying, ReconcileHold, stale).
		Order("id asc").Limit(batch).Find(&rows).Error
	return rows, err
}

// settleAction 是一张 paying 单该被怎么收尾。
type settleAction int

const (
	// actionWait 资金单仍在推进中,这里什么都不做。
	actionWait settleAction = iota
	// actionFinish 主库确认已生效,补做到账收尾。
	actionFinish
	// actionRefund 主库确定未生效,退回佣金。
	actionRefund
	// actionHold 结果不可判定,转人工。
	actionHold
)

// decideSettle 依据资金单状态与主库探针判定收尾动作。
//
// 抽成纯函数是因为这是全模块最贵的一次判断:判错一次的代价是"用户主库额度 +N,
// 佣金 N 也回到可用池",同一笔佣金可以被重复领取。它必须能被直接测到,
// 而不是埋在一个需要真实数据库才能跑到的循环里。
//
// probe 用惰性回调传入:探针要查主库 outbox,只有 Failed 这一条分支需要它,
// 其余分支不该为了一个用不上的判据去做一次跨库查询。
func decideSettle(status int8, probe func() bool) settleAction {
	switch status {
	case qymodel.StatusSuccess:
		return actionFinish
	case qymodel.StatusFailed:
		// 资金单被判 failed【不等于】主库确定没动。
		//
		// twophase.markFailed 在业务线程拿到确定性错误时就会置 failed,而主库事务
		// 完全可能是在 commit 阶段断连的 —— 服务端已提交、客户端收到 error。
		// 那条路径从来没有探过针,所以这里必须自己再探一次 outbox
		// (与 transfer/reconcile.go 的 applyFundOrderStatus 同口径)。
		// 探到已生效就转人工,绝不自动退回佣金:那是可以被主动构造的超发面
		// (在兑现瞬间打满主库连接让事务超时)。
		if probe() {
			return actionHold
		}
		return actionRefund
	case qymodel.StatusUncertain:
		return actionHold
	default:
		// 仍在 pending:交给 twophase 的补偿任务继续探针,这里不做任何判断。
		// 提前猜一个结果就是资损的开始。
		return actionWait
	}
}

func settleOnePaying(w *Withdrawal) {
	if w.OrderNo == "" {
		// paying 与 order_no 是同一个事务写进去的,不该出现只有其一的情况。
		holdForReview(w, "兑现中但缺少资金单号,数据异常")
		return
	}

	var order qymodel.FundOrder
	err := db.Get().Where("order_no = ?", w.OrderNo).Take(&order).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		holdForReview(w, "找不到对应的资金单,无法判定主库是否已生效")
		return
	}
	if err != nil {
		db.MarkFailure(err)
		return
	}

	switch decideSettle(order.Status, func() bool { return mainSideApplied(w.OrderNo) }) {
	case actionFinish:
		if err := db.Get().Transaction(func(tx *gorm.DB) error {
			return finishPaid(tx, w, order.OrderNo)
		}); err != nil {
			db.MarkFailure(err)
			common.SysError("qianye/withdraw: 补做单号 " + w.WithdrawNo + " 的到账收尾失败: " + err.Error())
		}
	case actionRefund:
		failWithdrawal(w, errors.New(fallbackReason(order.LastError)))
	case actionHold:
		holdForReview(w, holdReason(order))
	}
}

// holdReason 说明这张单为什么被转人工 —— 这是管理员裁决时看到的第一句话。
func holdReason(order qymodel.FundOrder) string {
	if order.Status == qymodel.StatusFailed {
		return "资金单被判失败但主库探针显示已生效,需人工核对"
	}
	return "资金单已转人工裁决: " + fallbackReason(order.LastError)
}

func fallbackReason(s string) string {
	if s == "" {
		return "主库确认未生效"
	}
	return s
}
