package commission

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/qianye/db"
	qymodel "github.com/QuantumNous/new-api/qianye/model"
	"github.com/QuantumNous/new-api/qianye/service/audit"

	"github.com/shopspring/decimal"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

const (
	settleInviterBatch = 500  // 每轮处理的邀请人数上限
	settleAccrualBatch = 1000 // 单个邀请人单轮吸收的计佣行数上限
	// settleCloseMarginSec 是日聚合桶被标记为 settled 前必须多等的时间。
	// HotAsync 的投递到落库存在毫秒到秒级延迟,跨午夜的迟到事件仍会写进
	// 昨天的桶;不留余量就会把这部分佣金锁死在 settled 状态里再也发不出去。
	settleCloseMarginSec = 3600
)

// settleOutcome 是结算算术的结果。
//
// 单独抽成纯函数是刻意的:发多少、留多少余数、欠账怎么记,这些是本模块
// 唯一会直接导致资损的逻辑,必须能在没有数据库的情况下做数值走查。
type settleOutcome struct {
	// NetQuota 正数为发放,负数为回收。
	NetQuota int64
	// CarryAfter 是回写的余数。它承载所有不足 1 额度的零头,永不丢弃;
	// 为负表示欠账(冲正金额超过了可回收余额)。
	CarryAfter decimal.Decimal
	// Clipped 是被日封顶削掉、留待下轮发放的部分。
	Clipped int64
	Clamp   *common.QuotaClamp
}

// computeSettlement 计算一次结算。
//
//	total = 上轮余数 + 本轮增量
//	grant = floor(total)      ← 必须是 floor 而不是 round:round 会超发
//	carry = total - grant     ← 余数回写,这是"小额佣金不归零"的全部秘密
//
// dailyRemain < 0 表示不设日封顶。
func computeSettlement(carry, delta decimal.Decimal, available, minSettle, dailyRemain int64) settleOutcome {
	total := carry.Add(delta)

	// Floor 对正负都正确:floor(3.7)=3 余 0.7;floor(-3.2)=-4 余 0.8。
	// 两种情况的余数都落在 [0,1),不会凭空多发也不会少收。
	grantInt, clamp := common.QuotaFromDecimalChecked(total.Floor())
	net := int64(grantInt)

	var clipped int64
	switch {
	case net > 0:
		if dailyRemain >= 0 && net > dailyRemain {
			clipped = net - dailyRemain
			net = dailyRemain
		}
		// 未达结算门槛不是"丢弃",而是继续留在余数里等下一轮。
		if net < minSettle {
			net = 0
		}
	case net < 0:
		// 只从未提现的可用余额里回收。佣金一旦提现进平台余额可能已被消费,
		// 倒扣主库会让用户余额意外变负;超出部分记欠账,由未来佣金抵扣。
		reclaim := -net
		if reclaim > available {
			reclaim = available
		}
		if reclaim < 0 {
			reclaim = 0
		}
		net = -reclaim
	}

	return settleOutcome{
		NetQuota:   net,
		CarryAfter: total.Sub(decimal.NewFromInt(net)),
		Clipped:    clipped,
		Clamp:      clamp,
	}
}

// runSettle 是结算后台任务的入口,由 lease.Run 驱动。
func runSettle(ctx context.Context) {
	settleRuns.Add(1)
	repairStrandedAccruals(ctx)

	ids, err := pendingInviters(settleInviterBatch)
	if err != nil {
		warnf("获取待结算邀请人失败: %v", err)
		return
	}
	for _, id := range ids {
		if ctx.Err() != nil {
			return // 租约已丢失,立刻停手,绝不双跑写库
		}
		if err := settleUser(id); err != nil {
			settleFailed.Add(1)
			warnf("邀请人 %d 结算失败(下轮自动重试): %v", id, err)
		}
	}
}

func pendingInviters(limit int) ([]int, error) {
	gdb := db.Get()
	if gdb == nil {
		return nil, db.ErrNotReady
	}
	var ids []int
	err := gdb.Raw(`SELECT DISTINCT inviter_id FROM qy_commission_accrual
		WHERE status = ? AND mature_at <= ? AND settled_amount <> gross_amount
		ORDER BY inviter_id LIMIT ?`,
		StatusAccrued, common.GetTimestamp(), limit).Scan(&ids).Error
	if err != nil {
		db.MarkFailure(err)
		return nil, err
	}
	return ids, nil
}

// repairStrandedAccruals 把"已标记 settled 但金额又长了"的行放回待结算队列。
//
// 跨午夜的迟到事件会给已封板的日聚合桶追加金额。没有这个自愈步骤,
// 那部分佣金会永远停在 settled 状态,用户永远拿不到。
func repairStrandedAccruals(ctx context.Context) {
	gdb := db.Get()
	if gdb == nil || ctx.Err() != nil {
		return
	}
	now := common.GetTimestamp()
	// 只回看最近几天:迟到事件在秒级内就会出现,把范围限死才不会让这条
	// 自愈语句随着历史行数无限变慢。
	res := gdb.Model(&Accrual{}).
		Where("status = ? AND settled_amount <> gross_amount AND updated_at >= ?",
			StatusSettled, now-7*86400).
		Updates(map[string]any{"status": StatusAccrued, "updated_at": now})
	if res.Error != nil {
		db.MarkFailure(res.Error)
		return
	}
	if res.RowsAffected > 0 {
		warnf("修复了 %d 条已封板后又追加金额的计佣行", res.RowsAffected)
	}
}

// settleUser 结算单个邀请人。整个过程只发生在扩展库,不跨库、不动主库额度 ——
// 这是本模块最重要的安全属性:返佣入账永远不需要两阶段提交。
func settleUser(inviterId int) error {
	gdb := db.Get()
	if gdb == nil {
		return db.ErrNotReady
	}
	now := common.GetTimestamp()
	var granted, reclaimed int64
	var clampNote string

	err := gdb.Transaction(func(tx *gorm.DB) error {
		var rows []Accrual
		err := tx.Where("inviter_id = ? AND status = ? AND mature_at <= ? AND settled_amount <> gross_amount",
			inviterId, StatusAccrued, now).
			Order("id asc").Limit(settleAccrualBatch).Find(&rows).Error
		if err != nil {
			return err
		}
		if len(rows) == 0 {
			return nil
		}

		delta := decimal.Zero
		weighted := decimal.Zero
		for _, r := range rows {
			d := r.GrossAmount.Sub(r.SettledAmount)
			delta = delta.Add(d)
			weighted = weighted.Add(d.Mul(r.UsdRate))
		}
		if !delta.IsZero() {
			weighted = weighted.Div(delta)
		} else {
			weighted = decimal.Zero
		}

		bal, err := lockBalance(tx, inviterId)
		if err != nil {
			return err
		}
		s := effective()
		dailyRemain, err := dailyRemaining(tx, inviterId, s.DailyCapQuota, now)
		if err != nil {
			return err
		}
		out := computeSettlement(bal.UnsettledAmount, delta, bal.AvailableQuota, s.MinSettleQuota, dailyRemain)
		if out.Clamp != nil {
			// 单轮结算触顶 int32 是绝不该发生的事,必须留痕并告警;
			// 未发完的部分仍在 CarryAfter 里,下轮继续。
			clampNote = out.Clamp.Error()
			warnf("邀请人 %d 结算金额触顶: %s", inviterId, clampNote)
		}

		settlement := Settlement{
			SettleNo:        newSerialNo("CS"),
			UserId:          inviterId,
			AccrualCount:    len(rows),
			DeltaAmount:     delta,
			CarryBefore:     bal.UnsettledAmount,
			CarryAfter:      out.CarryAfter,
			UsdRateWeighted: weighted,
			Remark:          truncate(clampNote, 255),
			CreatedAt:       now,
		}
		if out.NetQuota >= 0 {
			settlement.GrantedQuota = out.NetQuota
		} else {
			settlement.ReclaimedQuota = -out.NetQuota
		}
		fiatDelta, newFiat := applyFiat(bal, out.NetQuota, weighted)
		settlement.FiatDelta = fiatDelta
		if err := tx.Create(&settlement).Error; err != nil {
			return err
		}

		if err := absorbAccruals(tx, rows, settlement.Id, now); err != nil {
			return err
		}

		updates := map[string]any{
			"unsettled_amount": out.CarryAfter,
			"available_fiat":   newFiat,
			"debt_blocked":     out.CarryAfter.IsNegative(),
			"last_settled_at":  now,
			"updated_at":       now,
		}
		switch {
		case out.NetQuota > 0:
			updates["available_quota"] = bal.AvailableQuota + out.NetQuota
			updates["total_earned_quota"] = bal.TotalEarnedQuota + out.NetQuota
		case out.NetQuota < 0:
			updates["available_quota"] = bal.AvailableQuota + out.NetQuota
			updates["total_clawback_quota"] = bal.TotalClawbackQuota - out.NetQuota
		}
		if err := tx.Model(&Balance{}).Where("user_id = ?", inviterId).Updates(updates).Error; err != nil {
			return err
		}

		granted, reclaimed = settlement.GrantedQuota, settlement.ReclaimedQuota
		if out.CarryAfter.IsNegative() {
			warnf("邀请人 %d 产生欠账 %s,提现已冻结,后续佣金将优先抵扣",
				inviterId, out.CarryAfter.String())
		}
		return nil
	})
	if err != nil {
		db.MarkFailure(err)
		return err
	}

	settleGranted.Add(granted)
	settleReclaimed.Add(reclaimed)
	if granted != 0 || reclaimed != 0 {
		audit.Write(nil, audit.Entry{
			Category:     qymodel.AuditCategoryCommission,
			Action:       "commission.settle",
			ActorType:    qymodel.ActorSystem,
			TargetUserId: inviterId,
			AmountQuota:  granted - reclaimed,
			Result:       qymodel.ResultOK,
			Reason:       clampNote,
		})
	}
	return nil
}

// lockBalance 取得余额行的排他锁,不存在则先建。
//
// 这是本模块与提现模块共同约定的唯一加锁点:双方都必须先锁住这一行,
// 否则"结算加额度"和"提现冻结额度"会互相覆盖。
func lockBalance(tx *gorm.DB, userId int) (*Balance, error) {
	now := common.GetTimestamp()
	seed := Balance{UserId: userId, CreatedAt: now, UpdatedAt: now}
	if err := tx.Clauses(clause.OnConflict{DoNothing: true}).Create(&seed).Error; err != nil {
		return nil, err
	}
	var bal Balance
	if err := db.LockForUpdate(tx).Where("user_id = ?", userId).Take(&bal).Error; err != nil {
		return nil, err
	}
	return &bal, nil
}

// dailyRemaining 返回今日还能发多少。返回 -1 表示不设封顶。
func dailyRemaining(tx *gorm.DB, userId int, cap int64, now int64) (int64, error) {
	if cap <= 0 {
		return -1, nil
	}
	dayStart := time.Unix(now, 0).UTC().Truncate(24 * time.Hour).Unix()
	var used int64
	err := tx.Model(&Settlement{}).
		Where("user_id = ? AND created_at >= ?", userId, dayStart).
		Select("COALESCE(SUM(granted_quota), 0)").Scan(&used).Error
	if err != nil {
		return 0, err
	}
	remain := cap - used
	if remain < 0 {
		remain = 0
	}
	return remain, nil
}

// absorbAccruals 用 CAS 回写"已被吸收多少"。
//
// 必须写"读到的那个 gross"而不是 gross_amount 列本身:日聚合桶在读与写之间
// 可能又被追加了金额,写列名会把这部分静默吞掉 —— 用户少拿钱且无迹可查。
func absorbAccruals(tx *gorm.DB, rows []Accrual, settlementId int64, now int64) error {
	closedBefore := bucketDate(now - settleCloseMarginSec)
	for _, r := range rows {
		status := StatusAccrued
		// 只有确定不会再增长的行才能封板:一次性来源(充值/兑换码/冲正),
		// 或者已经过了封板余量的日聚合桶。
		if r.SourceType != SourceConsume || r.BucketDate < closedBefore {
			status = StatusSettled
		}
		res := tx.Model(&Accrual{}).
			Where("id = ? AND settled_amount = ?", r.Id, r.SettledAmount).
			Updates(map[string]any{
				"settled_amount": r.GrossAmount,
				"settlement_id":  settlementId,
				"status":         status,
				"updated_at":     now,
			})
		if res.Error != nil {
			return res.Error
		}
		if res.RowsAffected == 0 {
			// 有并发结算改动了同一行。整笔回滚比"发了一半"安全得多,
			// 下一轮会用新的读值重算。
			return fmt.Errorf("commission: 计佣行 %d 的结算 CAS 失败,本批回滚", r.Id)
		}
	}
	return nil
}

// applyFiat 维护按冻结汇率折算的法币余额。
//
// 发放时按本批的加权汇率增加;回收时按比例缩减,这样剩余法币金额对应的
// 平均汇率保持不变 —— 绝不允许用当前汇率反算,那会让历史对账全错。
func applyFiat(bal *Balance, net int64, weightedRate decimal.Decimal) (delta, after decimal.Decimal) {
	qpu := decimal.NewFromFloat(common.QuotaPerUnit)
	switch {
	case net > 0:
		if qpu.IsZero() {
			return decimal.Zero, bal.AvailableFiat
		}
		delta = decimal.NewFromInt(net).Div(qpu).Mul(weightedRate).Round(6)
		return delta, bal.AvailableFiat.Add(delta)
	case net < 0:
		oldAvail := bal.AvailableQuota
		newAvail := oldAvail + net
		if oldAvail <= 0 || newAvail <= 0 {
			return bal.AvailableFiat.Neg(), decimal.Zero
		}
		after = bal.AvailableFiat.
			Mul(decimal.NewFromInt(newAvail)).
			Div(decimal.NewFromInt(oldAvail)).
			Round(6)
		return after.Sub(bal.AvailableFiat), after
	default:
		return decimal.Zero, bal.AvailableFiat
	}
}

// settleOne 供管理端"立即结算"接口复用。
func settleOne(userId int) error {
	if userId <= 0 {
		return errors.New("commission: 用户 id 非法")
	}
	return settleUser(userId)
}
