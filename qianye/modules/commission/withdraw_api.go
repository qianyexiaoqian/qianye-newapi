package commission

import (
	"errors"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/qianye/db"

	"github.com/shopspring/decimal"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// 提现模块调用本文件时可能拿到的错误。
var (
	ErrInsufficient  = errors.New("commission: 可提现佣金不足")
	ErrDebtBlocked   = errors.New("commission: 存在冲正欠账,提现已冻结")
	ErrFreezeMissing = errors.New("commission: 未找到对应的冻结记录")
	ErrInvalidAmount = errors.New("commission: 提现额度非法")
)

// Withdrawable 返回用户当前可提现的佣金额度。
//
// 结算阶段只吸收已成熟(mature_at <= now)的计佣行,所以 available_quota
// 里的每一分钱都已经过了成熟期 —— 提现模块无需再做任何时间维度判断。
//
// 存在欠账时返回 0:欠账意味着账面上这个人还倒欠平台钱,先抵扣再谈提现。
func Withdrawable(userId int) (int64, error) {
	if userId <= 0 {
		return 0, ErrInvalidAmount
	}
	gdb := db.Get()
	if gdb == nil {
		return 0, db.ErrNotReady
	}
	var bal Balance
	err := gdb.Where("user_id = ?", userId).Take(&bal).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return 0, nil
	}
	if err != nil {
		db.MarkFailure(err)
		return 0, err
	}
	if bal.DebtBlocked || bal.UnsettledAmount.IsNegative() || bal.AvailableQuota < 0 {
		return 0, nil
	}
	return bal.AvailableQuota, nil
}

// LockBalance 取得该用户佣金余额行的排他锁,行不存在则先补一行。
//
// 提现模块用它把【申请阶段的风控闸门】与 FreezeForWithdraw 收敛到同一把锁上。
//
// 为什么这件事必须由本包提供:余额行是本模块与提现模块早就约定好的唯一加锁点
// (见 lockBalance),提现那边自己再挑一行来锁,等于凭空造出第二把锁,
// 两把锁互不认识,谁也拦不住谁。
//
// 调用约定:**必须是所在事务的第一条语句**。MySQL 的 REPEATABLE READ 快照建立
// 在事务里的第一次一致性读那一刻,加锁读不建立快照 —— 先做过普通查询再来加锁,
// 拿到锁之后读到的仍是加锁前的旧快照,基于计数的闸门会照样被并发绕过。
func LockBalance(tx *gorm.DB, userId int) error {
	if tx == nil {
		return errors.New("commission: 必须在扩展库事务内调用")
	}
	if userId <= 0 {
		return ErrInvalidAmount
	}
	_, err := lockBalance(tx, userId)
	return err
}

// FreezeForWithdraw 在扩展库事务内冻结提现额度。
//
// 调用约定(提现模块必须遵守):
//   - tx 必须是 qianye/db 的事务,与提现单据落库在同一个事务里;
//   - refNo 是提现单号,充当幂等键。同一单号重复调用是安全的空操作。
//
// 加锁点与结算任务完全一致(qy_commission_balance 的行锁),
// 因此"结算加额度"与"提现冻结额度"不会互相覆盖。
func FreezeForWithdraw(tx *gorm.DB, userId int, quota int64, refNo string) error {
	if err := validateFreezeArgs(tx, userId, quota, refNo); err != nil {
		return err
	}
	bal, err := lockBalance(tx, userId)
	if err != nil {
		return err
	}
	if bal.DebtBlocked || bal.UnsettledAmount.IsNegative() {
		return ErrDebtBlocked
	}
	if bal.AvailableQuota < quota {
		return ErrInsufficient
	}

	newAvail := bal.AvailableQuota - quota
	// 法币值按额度比例缩减,保持"剩余额度对应的平均冻结汇率"不变。
	// 绝不用当前汇率反算 —— 那会让历史佣金的法币口径随管理员改汇率而漂移。
	newFiat := scaleFiat(bal.AvailableFiat, newAvail, bal.AvailableQuota)
	takenFiat := bal.AvailableFiat.Sub(newFiat)

	done, err := claimFreezeOp(tx, userId, quota, takenFiat, refNo, FreezeActionFreeze)
	if err != nil || done {
		return err
	}
	return applyBalance(tx, userId, map[string]any{
		"available_quota": newAvail,
		"available_fiat":  newFiat,
		"frozen_quota":    bal.FrozenQuota + quota,
	})
}

// UnfreezeForWithdraw 在提现被拒或撤销时把额度放回可用池。
func UnfreezeForWithdraw(tx *gorm.DB, userId int, quota int64, refNo string) error {
	if err := validateFreezeArgs(tx, userId, quota, refNo); err != nil {
		return err
	}
	origin, err := loadFreeze(tx, refNo, quota)
	if err != nil {
		return err
	}
	bal, err := lockBalance(tx, userId)
	if err != nil {
		return err
	}
	if bal.FrozenQuota < quota {
		return ErrInsufficient
	}
	done, err := claimFreezeOp(tx, userId, quota, origin.Fiat, refNo, FreezeActionUnfreeze)
	if err != nil || done {
		return err
	}
	// 法币按当初冻走的原值回补,与额度一一对应。
	return applyBalance(tx, userId, map[string]any{
		"available_quota": bal.AvailableQuota + quota,
		"available_fiat":  bal.AvailableFiat.Add(origin.Fiat),
		"frozen_quota":    bal.FrozenQuota - quota,
	})
}

// SettleFrozen 在提现成功到账后把冻结额度正式转为已提现。
//
// 法币在冻结时就已经从 AvailableFiat 扣走,这里不再动它。
func SettleFrozen(tx *gorm.DB, userId int, quota int64, refNo string) error {
	if err := validateFreezeArgs(tx, userId, quota, refNo); err != nil {
		return err
	}
	origin, err := loadFreeze(tx, refNo, quota)
	if err != nil {
		return err
	}
	bal, err := lockBalance(tx, userId)
	if err != nil {
		return err
	}
	if bal.FrozenQuota < quota {
		return ErrInsufficient
	}
	done, err := claimFreezeOp(tx, userId, quota, origin.Fiat, refNo, FreezeActionSettle)
	if err != nil || done {
		return err
	}
	return applyBalance(tx, userId, map[string]any{
		"frozen_quota":    bal.FrozenQuota - quota,
		"withdrawn_quota": bal.WithdrawnQuota + quota,
	})
}

func validateFreezeArgs(tx *gorm.DB, userId int, quota int64, refNo string) error {
	if tx == nil {
		return errors.New("commission: 必须在扩展库事务内调用")
	}
	if userId <= 0 || refNo == "" {
		return ErrInvalidAmount
	}
	// 上界校验必不可少:这笔额度最终会流向主库的 int32 额度列,
	// 越界值在那里会静默溢出成负数。
	if quota <= 0 || quota > int64(common.MaxQuota) {
		return ErrInvalidAmount
	}
	return nil
}

// claimFreezeOp 登记一次冻结类操作。
// 返回 done == true 表示该 (refNo, action) 此前已执行过,调用方直接返回成功。
func claimFreezeOp(tx *gorm.DB, userId int, quota int64, fiat decimal.Decimal,
	refNo, action string) (bool, error) {
	rec := FreezeRecord{
		RefNo: refNo, Action: action, UserId: userId,
		Quota: quota, Fiat: fiat, CreatedAt: common.GetTimestamp(),
	}
	res := tx.Clauses(clause.OnConflict{DoNothing: true}).Create(&rec)
	if res.Error != nil {
		return false, res.Error
	}
	return res.RowsAffected == 0, nil
}

// loadFreeze 确认解冻/兑现对应着一次真实发生过的冻结,且金额一致。
// 缺了这道校验,任何人都能用一个没冻结过的单号把额度凭空放回可用池。
func loadFreeze(tx *gorm.DB, refNo string, quota int64) (*FreezeRecord, error) {
	var rec FreezeRecord
	err := tx.Where("ref_no = ? AND action = ?", refNo, FreezeActionFreeze).Take(&rec).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, ErrFreezeMissing
	}
	if err != nil {
		return nil, err
	}
	if rec.Quota != quota {
		return nil, ErrInvalidAmount
	}
	return &rec, nil
}

func applyBalance(tx *gorm.DB, userId int, updates map[string]any) error {
	updates["updated_at"] = common.GetTimestamp()
	return tx.Model(&Balance{}).Where("user_id = ?", userId).Updates(updates).Error
}

// scaleFiat 按额度比例缩放法币余额。
func scaleFiat(fiat decimal.Decimal, newQuota, oldQuota int64) decimal.Decimal {
	if oldQuota <= 0 || newQuota <= 0 {
		return decimal.Zero
	}
	return fiat.Mul(decimal.NewFromInt(newQuota)).Div(decimal.NewFromInt(oldQuota)).Round(6)
}
