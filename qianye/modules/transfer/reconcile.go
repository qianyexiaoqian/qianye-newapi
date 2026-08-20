package transfer

import (
	"context"
	"fmt"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/qianye/config"
	"github.com/QuantumNous/new-api/qianye/db"
	qymodel "github.com/QuantumNous/new-api/qianye/model"

	"gorm.io/gorm"
)

// resolveAfterCompensation 由 twophase 补偿任务在确认主库已生效后回调。
//
// 补偿任务只知道"钱动了",不知道这笔业务还欠什么收尾:账本日志要不要补写、
// 明细要不要置终态、未结算计数要不要释放,只有本模块清楚。必须幂等 ——
// 同一单可能被补偿多轮。
func resolveAfterCompensation(_ context.Context, order *qymodel.FundOrder) error {
	return finalizeSuccess(order.OrderNo)
}

// finalizeSuccess 幂等地完成一笔已生效划转的扩展库收尾。
func finalizeSuccess(orderNo string) error {
	backfillLedger(orderNo)
	return settleDetail(orderNo, settlement{status: statusSuccess})
}

// backfillLedger 在进程崩溃导致账本日志没写成时补写。
//
// 靠 ledger_written 的 CAS 保证只写一次:重复写会让用户在账单里看到两条
// 余额变动,直观上就像被扣了两次钱。写失败不返回错误 —— 日志是信息性的,
// 不能因为它挡住明细置终态。
func backfillLedger(orderNo string) {
	gdb := db.Get()
	if gdb == nil {
		return
	}
	var row Order
	if err := gdb.Where("order_no = ?", orderNo).First(&row).Error; err != nil {
		db.MarkFailure(err)
		return
	}
	if row.LedgerWritten {
		return
	}
	res := gdb.Model(&Order{}).
		Where("order_no = ? AND ledger_written = ?", orderNo, false).
		Update("ledger_written", true)
	if res.Error != nil {
		db.MarkFailure(res.Error)
		return
	}
	if res.RowsAffected == 0 {
		return
	}
	// 走到这里说明余额快照从未落库(崩在 stampLedger 之前),不显示划转后余额。
	writeLedgerLogs(row, false)
}

// reconcile 是本模块的对账任务,由 lease 保护,多节点不会双跑。
//
// 它补的是 twophase 补偿任务够不到的那一段:补偿任务把资金单推进到终态后,
// 若业务线程恰好已经退出,明细行会永远停在 pending、风控计数永远不释放,
// 用户从此发不出第二笔划转。
func reconcile(ctx context.Context) {
	gdb := db.Get()
	if gdb == nil {
		return
	}
	syncStuckOrders(ctx, gdb)
	pruneLookupLogs(ctx, gdb)
}

func syncStuckOrders(ctx context.Context, gdb *gorm.DB) {
	cfg := config.Get().TwoPhase
	batch := cfg.BatchSize
	if batch <= 0 {
		batch = 200
	}
	// 等过宽限期再介入,避免和正在推进的业务线程抢同一笔。
	cutoff := common.GetTimestamp() - int64(cfg.PendingGraceSeconds)

	var rows []Order
	if err := gdb.Where("status IN ? AND created_at < ?",
		[]string{statusPending, statusUncertain}, cutoff).
		Order("id asc").Limit(batch).Find(&rows).Error; err != nil {
		db.MarkFailure(err)
		common.SysError("qianye/transfer: 扫描未结算划转失败: " + err.Error())
		return
	}

	for _, row := range rows {
		// 失去租约后立刻停手,否则会与接管节点双跑。
		if ctx.Err() != nil {
			return
		}
		var order qymodel.FundOrder
		if err := gdb.Where("order_no = ?", row.OrderNo).First(&order).Error; err != nil {
			// 明细与资金单是同一个事务插入的,查不到只能是数据被人为删过。
			common.SysError(fmt.Sprintf(
				"qianye/transfer: 划转 %s 找不到对应资金单,需人工核对: %v", row.OrderNo, err))
			continue
		}
		if err := applyFundOrderStatus(row.OrderNo, order); err != nil {
			common.SysError(fmt.Sprintf(
				"qianye/transfer: 同步划转 %s 的终态失败: %v", row.OrderNo, err))
		}
	}
}

// applyFundOrderStatus 把资金单的终态同步到业务明细。
// Pending 与 Reversed 一律不动:前者由 twophase 补偿任务负责推进,
// 后者划转业务不会产生,出现即属异常,交人工。
func applyFundOrderStatus(orderNo string, order qymodel.FundOrder) error {
	switch order.Status {
	case qymodel.StatusSuccess:
		return finalizeSuccess(orderNo)
	case qymodel.StatusFailed:
		// 再确认一次主库探针。commit 断连那一支现在落 in_doubt 而不是 failed,
		// 但存量单、补偿任务与人工裁决落下的 failed 来自另一套判据,
		// 而退还预占是不可逆的 —— 判错就是超发。
		if mainSideApplied(&order) {
			markUncertainAfterConflict(orderNo)
			return nil
		}
		return settleDetail(orderNo, settlement{
			status:     statusFailed,
			failCode:   "qy_main_not_applied",
			failReason: truncate(order.LastError, 255),
		})
	case qymodel.StatusUncertain:
		// 只改展示状态,绝不退还预占:钱可能已经转走,退了就是超发。
		return settleDetail(orderNo, settlement{
			status:     statusUncertain,
			failCode:   "qy_uncertain",
			failReason: truncate(order.LastError, 255),
		})
	default:
		return nil
	}
}

// pruneLookupLogs 清理过期的收款人解析日志。
// 这张表按请求量增长,不清理会在高流量站点上变成最大的一张表。
//
// 保留期必须读 transfer.lookup_log_retain_days,不能写死:这张表记的是
// "谁查过谁的收款人",是可关联到个人的行为日志 —— 合规口径可能要求 7 天,
// 风控排查又可能要求 90 天。写死成常量的话,运维照着 YAML 改完重启,
// 闸门其实一动没动,而且没有任何迹象能让他发现(原审计 C1/C2 就是这个形状)。
//
// 与 withdraw.pii_retention_days 同口径:<=0 表示关掉清理。显式写 0 就是关掉;
// 【不写这个键】才会取默认的 30 天 —— 少配一个键不该让行为日志永久留存。
func pruneLookupLogs(ctx context.Context, gdb *gorm.DB) {
	days := config.Get().Transfer.LookupLogRetainDays
	if days <= 0 || ctx.Err() != nil {
		return
	}
	before := common.GetTimestamp() - int64(days)*86400
	res := gdb.Where("created_at < ?", before).Limit(1000).Delete(&LookupLog{})
	if res.Error != nil {
		db.MarkFailure(res.Error)
		return
	}
	if res.RowsAffected > 0 {
		common.SysLog(fmt.Sprintf("qianye/transfer: 已清理 %d 条过期的收款人解析日志", res.RowsAffected))
	}
}
