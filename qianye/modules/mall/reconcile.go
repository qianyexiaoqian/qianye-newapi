package mall

import (
	"context"
	"errors"
	"fmt"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/qianye/config"
	"github.com/QuantumNous/new-api/qianye/db"
	qymodel "github.com/QuantumNous/new-api/qianye/model"
	"github.com/QuantumNous/new-api/qianye/modules/stardust"
	"github.com/QuantumNous/new-api/qianye/service/twophase"

	"gorm.io/gorm"
)

// reconcile.go —— 本模块的两个后台任务(都由 lease 保护,多节点不双跑)。
//
//	mall.reconcile  把停在 paid / held 的套餐订单按资金单终态收敛(transfer/reconcile.go 的形状)
//	mall.prune      到期清空实物订单的收货地址密文(PII 保留期)
//
// 对账补的是 twophase 补偿任务够不到的那一段:补偿任务把资金单推到终态之后,业务线程
// 若已经退出(或 execute.go:219"主库已生效但扩展库回写失败"那一支),订单会永远停在
// paid,用户既拿不到订阅 id 也拿不回星屑。

// pruneBatch 是地址清理单轮处理的行数上限。它同时是 IN 列表的长度,留在 PostgreSQL
// 绑定参数上限之内。
const pruneBatch = 200

// runReconcile 扫描过了宽限期仍停在 paid / held 的套餐订单,按资金单终态收敛。
//
// 每张单的处置只有四种,判据与 transfer 的 applyFundOrderStatus 同形:
//
//	Success                      → finalizePlanOrder(paid|held → done)
//	Failed                       → 再探一次针:MainNotApplied 才退星屑,否则挂 held + 告警
//	Pending / InDoubt / Uncertain → 只把展示状态改 held,绝不退(订阅可能已经发了)
//	Reversed                     → 商城不会产生,出现即属异常,告警交人工
func runReconcile(ctx context.Context) {
	if ctx.Err() != nil {
		return
	}
	handle := db.Get()
	if handle == nil {
		return
	}
	gdb := handle.WithContext(ctx)

	cfg := config.Get()
	grace := int64(cfg.Mall.PendingGraceSeconds)
	if grace <= 0 {
		grace = 60
	}
	batch := cfg.TwoPhase.BatchSize
	if batch <= 0 {
		batch = 200
	}
	// 等过宽限期再介入,避免和正在推进的业务线程抢同一笔。
	cutoff := common.GetTimestamp() - grace

	// 奖品套餐单没有资金单,走另一条收敛(prize.go);先扫它,再扫自购单。
	fulfillPendingPrizePlans(ctx, gdb, cutoff, batch)

	rows := make([]Order, 0, batch)
	if err := gdb.Where("kind = ? AND source <> ? AND status IN ? AND created_at < ?",
		KindPlan, SourceLottery, []string{StatusPaid, StatusHeld}, cutoff).
		Order("id asc").Limit(batch).Find(&rows).Error; err != nil {
		db.MarkFailure(err)
		common.SysError("qianye/mall: 扫描未落定的套餐订单失败: " + err.Error())
		return
	}
	for i := range rows {
		// 失去租约后立刻停手,否则会与接管节点双跑。
		if ctx.Err() != nil {
			return
		}
		reconcilePlanOrder(ctx, gdb, &rows[i])
	}
}

// reconcilePlanOrder 把一张套餐订单与它的资金单对齐。gdb 必须是已经绑好 ctx 的句柄。
func reconcilePlanOrder(ctx context.Context, gdb *gorm.DB, o *Order) {
	if o.FundOrderNo == "" {
		common.SysError("qianye/mall: 套餐订单 " + o.OrderNo + " 没有资金单号,需人工核对")
		return
	}
	var fo qymodel.FundOrder
	err := gdb.Where("order_no = ?", o.FundOrderNo).Take(&fo).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		// 订单与资金单是同一个事务插入的,查不到只能是数据被人为删过。
		common.SysError("qianye/mall: 套餐订单 " + o.OrderNo + " 找不到对应资金单 " + o.FundOrderNo + ",需人工核对")
		return
	}
	if err != nil {
		db.MarkFailure(err)
		return
	}

	switch fo.Status {
	case qymodel.StatusSuccess:
		if err := finalizePlanOrder(ctx, &fo); err != nil {
			common.SysError("qianye/mall: 套餐订单 " + o.OrderNo + " 收尾失败: " + err.Error())
		}
	case qymodel.StatusFailed:
		// 再确认一次主库探针:补偿任务与人工裁决落下的 failed 来自另一套判据,
		// 而退星屑是不可逆的 —— 判错就是白送一份订阅。
		if twophase.ProbeMainSide(&fo) != twophase.MainNotApplied {
			common.SysError("qianye/mall: 套餐订单 " + o.OrderNo +
				" 的资金单已失败,但主库探针无法确认订阅未发放,已挂起等待人工裁决(/mall/orders/:no/adjudicate)")
			if err := holdOrder(gdb, o, "资金单已失败但主库探针无法确认未生效,等待人工裁决"); err != nil {
				common.SysError("qianye/mall: 套餐订单 " + o.OrderNo + " 挂起失败: " + err.Error())
			}
			return
		}
		reason := "对账确认订阅未发放," + stardust.UnitName() + "已退回"
		if err := refundPlanOrder(gdb, o, []string{StatusPaid, StatusHeld}, reason, 0); err != nil {
			common.SysError("qianye/mall: 套餐订单 " + o.OrderNo + " 对账退款失败: " + err.Error())
		}
	case qymodel.StatusReversed:
		common.SysError("qianye/mall: 套餐订单 " + o.OrderNo + " 的资金单处于 reversed,商城不会产生这一态,需人工核对")
	default:
		// Pending / InDoubt / Uncertain:补偿任务还在推进它,只改展示状态。
		if err := holdOrder(gdb, o, "资金单尚未落定("+qymodel.StatusName(fo.Status)+"),等待补偿任务收敛"); err != nil {
			common.SysError("qianye/mall: 套餐订单 " + o.OrderNo + " 挂起失败: " + err.Error())
		}
	}
}

// runPrune 到期清空实物订单的收货地址与联系方式密文,并记下 address_pruned_at。
//
// 保留期读 mall.address_retention_days(校验器保证 ≥ 30):发货争议期内地址必须可查,
// 之后它就只是一份没有任何业务再需要的 PII。已完结(done / cancelled / failed)才清,
// 还在路上的(paid / shipped)一律不动。
func runPrune(ctx context.Context) {
	if ctx.Err() != nil {
		return
	}
	handle := db.Get()
	if handle == nil {
		return
	}
	days := config.Get().Mall.AddressRetentionDays
	if days <= 0 {
		return
	}
	pruneAddresses(handle.WithContext(ctx), common.GetTimestamp()-int64(days)*86400)
}

// pruneAddresses 是地址密文列**唯一**的置空点(secret_guard_test.go 的显式豁免)。
//
// 写成 SELECT 主键 + UPDATE ... WHERE id IN:GORM 只在 MySQL 上渲染 UPDATE/DELETE 的
// LIMIT,PostgreSQL 驱动会静默丢掉它,"按批清理"退化成一条无界 UPDATE。
func pruneAddresses(gdb *gorm.DB, cutoff int64) {
	ids := make([]int64, 0, pruneBatch)
	if err := gdb.Model(&Order{}).
		Where("kind = ? AND status NOT IN ? AND address_pruned_at = 0 AND updated_at < ?",
			KindPhysical, openStatuses, cutoff).
		Order("id asc").Limit(pruneBatch).Pluck("id", &ids).Error; err != nil {
		db.MarkFailure(err)
		common.SysError("qianye/mall: 扫描到期的收货地址失败: " + err.Error())
		return
	}
	if len(ids) == 0 {
		return
	}
	// 再带一次 address_pruned_at = 0:两个节点的租约交接窗口里可能重叠执行一轮,
	// 条件写在 UPDATE 上才不会把清理时刻刷成第二个时间戳。
	res := gdb.Model(&Order{}).Where("id IN ? AND address_pruned_at = 0", ids).
		Updates(map[string]any{
			"address_cipher":    []byte{},
			"address_nonce":     []byte{},
			"contact_cipher":    []byte{},
			"contact_nonce":     []byte{},
			"address_pruned_at": common.GetTimestamp(),
		})
	if res.Error != nil {
		db.MarkFailure(res.Error)
		common.SysError("qianye/mall: 清空到期的收货地址失败: " + res.Error.Error())
		return
	}
	if res.RowsAffected > 0 {
		common.SysLog(fmt.Sprintf("qianye/mall: 已清空 %d 张订单的收货地址(超过保留期)", res.RowsAffected))
	}
}
