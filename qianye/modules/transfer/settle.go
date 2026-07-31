package transfer

import (
	"fmt"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/logger"
	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/qianye/db"

	"gorm.io/gorm"
)

// quotaSnapshot 是主库事务内读到的锁定余额。
// 只在内存里传递,最终落进明细行 —— 主库 users 表没有历史版本,
// 不留快照就没有任何证据能回答"扣款那一刻余额是多少"。
type quotaSnapshot struct {
	FromBefore int64
	FromAfter  int64
	ToBefore   int64
	ToAfter    int64
}

// settlement 描述一次结算的目标状态。
type settlement struct {
	status     string
	failCode   string
	failReason string
	snap       *quotaSnapshot
}

// settleDetailTx 把明细推进到终态并结算风控预占,必须在扩展库事务内调用。
//
// 幂等由 risk_held 的 CAS 保证:业务线程与补偿任务都可能来结算同一笔,
// 没有这道闸门就会把日累计重复退还,等于凭空多出一次划转额度。
func settleDetailTx(tx *gorm.DB, orderNo string, s settlement) error {
	var row Order
	if err := tx.Where("order_no = ?", orderNo).First(&row).Error; err != nil {
		return err
	}
	if !row.RiskHeld {
		return nil // 已结算过
	}

	now := common.GetTimestamp()
	if s.status == statusUncertain {
		// 结果不可判定时绝不退还预占:退了意味着"钱可能已经转走,但额度重新可用",
		// 这是唯一会造成超发的方向。等人工裁决后由对账任务收敛。
		return tx.Model(&Order{}).
			Where("order_no = ? AND status = ?", orderNo, statusPending).
			Updates(map[string]any{
				"status":      statusUncertain,
				"fail_code":   s.failCode,
				"fail_reason": s.failReason,
			}).Error
	}

	updates := map[string]any{
		"status":      s.status,
		"risk_held":   false,
		"settled_at":  now,
		"fail_code":   s.failCode,
		"fail_reason": s.failReason,
	}
	if s.snap != nil {
		updates["from_quota_before"] = s.snap.FromBefore
		updates["from_quota_after"] = s.snap.FromAfter
		updates["to_quota_before"] = s.snap.ToBefore
		updates["to_quota_after"] = s.snap.ToAfter
	}
	res := tx.Model(&Order{}).Where("order_no = ? AND risk_held = ?", orderNo, true).Updates(updates)
	if res.Error != nil {
		return res.Error
	}
	if res.RowsAffected == 0 {
		return nil // 并发结算,让给对方
	}

	if s.status == statusSuccess {
		// 划转确实发生了,日累计与终身累计都应当保留,只释放中间态计数。
		return releasePendingCount(tx, row.FromUserId, now)
	}
	return refundReservation(tx, row, now)
}

// releasePendingCount 只释放"未结算笔数",不动任何额度计数。
func releasePendingCount(tx *gorm.DB, userId int, now int64) error {
	var s UserState
	if err := db.LockForUpdate(tx).Where("user_id = ?", userId).First(&s).Error; err != nil {
		return err
	}
	s.PendingCount = clampNonNegative(s.PendingCount - 1)
	s.UpdatedAt = now
	return saveState(tx, &s)
}

// refundReservation 原路退还失败划转占用的全部风控计数。
// 两行仍按 user_id 升序加锁,与 reserveRisk 保持同一顺序。
func refundReservation(tx *gorm.DB, row Order, now int64) error {
	first, second := row.FromUserId, row.ToUserId
	if first > second {
		first, second = second, first
	}
	var s1, s2 UserState
	if err := db.LockForUpdate(tx).Where("user_id = ?", first).First(&s1).Error; err != nil {
		return err
	}
	if err := db.LockForUpdate(tx).Where("user_id = ?", second).First(&s2).Error; err != nil {
		return err
	}
	sender, receiver := &s1, &s2
	if sender.UserId != row.FromUserId {
		sender, receiver = &s2, &s1
	}

	// 预占之后若已跨日,日计数早被 rollDay 清零,再减会把今天的额度凭空放大。
	sameDay := sender.DayBucket == dayBucket(row.CreatedAt)
	undoReservation(sender, receiver, row.Amount, row.Amount+row.FeeQuota, sameDay, now)
	if err := saveState(tx, sender); err != nil {
		return err
	}
	return saveState(tx, receiver)
}

// settleDetail 是 settleDetailTx 的独立事务版本,供补偿路径与失败回滚使用。
func settleDetail(orderNo string, s settlement) error {
	gdb := db.Get()
	if gdb == nil {
		return db.ErrNotReady
	}
	return gdb.Transaction(func(tx *gorm.DB) error {
		return settleDetailTx(tx, orderNo, s)
	})
}

// stampLedger 在写主库账本日志之前先把"已写"标记与余额快照落库。
//
// 顺序刻意是"先置位再写日志":置位失败会让补偿任务重写日志,用户会看到
// 两条余额变动记录、像是被扣了两次;而先置位后写失败只是少一条信息性日志。
// 后者危害小得多。
func stampLedger(orderNo string, snap quotaSnapshot) error {
	gdb := db.Get()
	if gdb == nil {
		return db.ErrNotReady
	}
	res := gdb.Model(&Order{}).
		Where("order_no = ? AND ledger_written = ?", orderNo, false).
		Updates(map[string]any{
			"ledger_written":    true,
			"from_quota_before": snap.FromBefore,
			"from_quota_after":  snap.FromAfter,
			"to_quota_before":   snap.ToBefore,
			"to_quota_after":    snap.ToAfter,
		})
	if res.Error != nil {
		db.MarkFailure(res.Error)
		return res.Error
	}
	if res.RowsAffected == 0 {
		return errLedgerAlreadyStamped
	}
	return nil
}

// errLedgerAlreadyStamped 表示账本日志已由别的路径写过,调用方直接跳过即可。
var errLedgerAlreadyStamped = fmt.Errorf("qianye/transfer: 账本日志已写入")

// writeLedgerLogs 给双方各写一条主库账本日志。
//
// 单号写进 logs.request_id(该列有索引),运营可以直接按单号检索;
// 结构化字段放 other 顶层而不是 admin_info —— 上游会对普通用户剥离 admin_info,
// 放进去用户就看不到自己的划转单号了。
//
// withBalance 为 false 用于崩溃后的补写路径:那时余额快照从未落库,
// 打印一个 0 会让用户以为自己被清空了,宁可不显示。
func writeLedgerLogs(row Order, withBalance bool) {
	feeNote := ""
	if row.FeeQuota > 0 {
		feeNote = fmt.Sprintf(",手续费 %s", logger.LogQuota(int(row.FeeQuota)))
	}
	outBalance, inBalance := "", ""
	if withBalance {
		outBalance = fmt.Sprintf(",划转后余额 %s", logger.LogQuota(int(row.FromQuotaAfter)))
		inBalance = fmt.Sprintf(",划转后余额 %s", logger.LogQuota(int(row.ToQuotaAfter)))
	}

	model.QyRecordLedgerLog(row.FromUserId, model.LogTypeTopup,
		fmt.Sprintf("余额划转转出 %s 至用户 %s(ID %d)%s%s",
			logger.LogQuota(int(row.Amount)), maskUsername(row.ToUsername), row.ToUserId,
			feeNote, outBalance),
		row.OrderNo,
		map[string]interface{}{
			"qy_transfer_direction": "out",
			"qy_counterparty_id":    row.ToUserId,
			"qy_fee_quota":          row.FeeQuota,
		})

	model.QyRecordLedgerLog(row.ToUserId, model.LogTypeTopup,
		fmt.Sprintf("收到用户 %s(ID %d)的余额划转 %s%s",
			maskUsername(row.FromUsername), row.FromUserId,
			logger.LogQuota(int(row.Amount)), inBalance),
		row.OrderNo,
		map[string]interface{}{
			"qy_transfer_direction": "in",
			"qy_counterparty_id":    row.FromUserId,
			"qy_fee_quota":          int64(0),
		})
}
