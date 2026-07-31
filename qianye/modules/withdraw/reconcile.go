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
// 本任务由 lease.Run 驱动,多节点不会双跑;即便双跑,每一步也都是 CAS。
func reconcile(ctx context.Context) {
	if !config.Get().Withdraw.Enabled {
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
	settlePaying(ctx, stale, batch)
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
func settlePaying(ctx context.Context, stale int64, batch int) {
	var rows []Withdrawal
	err := db.Get().
		Where("status = ? AND settle_started_at > 0 AND settle_started_at < ?", StatusPaying, stale).
		Order("id asc").Limit(batch).Find(&rows).Error
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

	switch order.Status {
	case qymodel.StatusSuccess:
		if err := db.Get().Transaction(func(tx *gorm.DB) error {
			return finishPaid(tx, w, order.OrderNo)
		}); err != nil {
			db.MarkFailure(err)
			common.SysError("qianye/withdraw: 补做单号 " + w.WithdrawNo + " 的到账收尾失败: " + err.Error())
		}
	case qymodel.StatusFailed:
		// 资金单被判失败意味着补偿任务已通过 outbox 探针确认主库未生效,
		// 这里可以安全退回佣金。
		failWithdrawal(w, errors.New(fallbackReason(order.LastError)))
	case qymodel.StatusUncertain:
		holdForReview(w, "资金单已转人工裁决: "+fallbackReason(order.LastError))
	default:
		// 仍在 pending:交给 twophase 的补偿任务继续探针,这里不做任何判断。
		// 提前猜一个结果就是资损的开始。
	}
}

func fallbackReason(s string) string {
	if s == "" {
		return "主库确认未生效"
	}
	return s
}
