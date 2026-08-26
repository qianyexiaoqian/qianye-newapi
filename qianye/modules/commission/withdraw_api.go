package commission

import (
	"errors"
	"strconv"

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
	// ErrFiatUnavailable:这笔额度在账本上折不出一个正的法币值。
	//
	// 它不是"用户没钱"(那是 ErrInsufficient),而是"available_fiat 这一列
	// 没法回答这笔额度值多少钱":余额是在三层折算比例全部拿不出正数的那段时间里
	// 攒起来的(fiatLayerNone),或者是本模块上线前的存量行。
	// 法币单必须在这里停住 —— 继续走下去就是打一张金额为 0 的款单。
	ErrFiatUnavailable = errors.New("commission: 佣金余额的法币折算不可用")
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

// DebtStatus 是一个人当下的冲正欠账状态。
type DebtStatus struct {
	// Blocked 为真即“这个人账面上还倒欠平台钱”，新的提现申请会被拒。
	Blocked bool
	// Unsettled 是未结算余数；为负表示冲正金额超过了当时可回收的可用余额，
	// 差额挂在这里等未来的佣金来吸收。
	Unsettled decimal.Decimal
}

// LoadDebtStatuses 只读地批量返回一组人的欠账状态，供审核界面展示。
//
// 冲正欠账只在【提交提现】那一刻拦一次（FreezeForWithdraw），而冲正按设计只吃
// available、吃不到已经冻住的 frozen。于是“先提现冻住 → 下线退款触发冲正 →
// 管理员照常审批放款”是一条完整且无告警的通路，而 approve / mark-paid 才是这笔钱
// 最后一次还能被拦回来的地方（驳回与标记失败都会把 frozen 退回 available，而退回后
// 的 available 正是下一次结算能吃到的那一桶）。信号在系统里本来就存在（佣金余额页
// 有 debt_blocked 徽标与筛选），只是不在审核人正在看的那张单上 —— 这个函数就是为了
// 把它放到那张单上。
//
// 它不做任何拦截：设计文档 §10.6 把欠账判据写在“提现发起”那一步，改成在放款侧
// 硬拦是另一个产品决策；当下要修的是“审核人看不见”。
//
// 余额行不存在（从没计过佣）的人不会出现在返回的 map 里，调用方拿到零值即可。
// 批量而不是逐个查：审核列表一页几十张单，逐张查就是一个 N+1。
func LoadDebtStatuses(userIds []int) (map[int]DebtStatus, error) {
	out := make(map[int]DebtStatus, len(userIds))
	ids := make([]int, 0, len(userIds))
	seen := make(map[int]bool, len(userIds))
	for _, id := range userIds {
		if id <= 0 || seen[id] {
			continue
		}
		seen[id] = true
		ids = append(ids, id)
	}
	if len(ids) == 0 {
		return out, nil
	}
	gdb := db.Get()
	if gdb == nil {
		return out, db.ErrNotReady
	}
	var rows []Balance
	if err := gdb.Where("user_id IN ?", ids).Find(&rows).Error; err != nil {
		db.MarkFailure(err)
		return out, err
	}
	for i := range rows {
		out[rows[i].UserId] = DebtStatus{
			Blocked:   rows[i].DebtBlocked || rows[i].UnsettledAmount.IsNegative(),
			Unsettled: rows[i].UnsettledAmount,
		}
	}
	return out, nil
}

// WithdrawableFiat 返回可提现额度在账本上对应的法币值,供提现页做预览。
//
// 它与 QuoteWithdrawFiat 读的是同一列(available_fiat),差别只在于:预览是
// 锁外只读、可能在用户点提交前被一次结算改动,所以界面必须把它标成预览值。
// 提现页此前展示的是充值页汇率(operation_setting.USDExchangeRate),而单据
// 的金额来自另一套计算 —— 用户看到的、单据开出的、账本扣走的是三个数。
//
// 余额行不存在(从没计过佣)时返回金额 0 + 当前配置的币种,不是错误。
func WithdrawableFiat(userId int) (FiatQuote, error) {
	if userId <= 0 {
		return FiatQuote{}, ErrInvalidAmount
	}
	gdb := db.Get()
	if gdb == nil {
		return FiatQuote{}, db.ErrNotReady
	}
	var bal Balance
	err := gdb.Where("user_id = ?", userId).Take(&bal).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		// 从没计过佣的人也要看到币种:提现页照样得写清楚"这里说的钱是哪一种"。
		// 金额为 0,币种回落当前配置(与 frozenFiatCurrency 的存量行口径一致)。
		return FiatQuote{Currency: frozenFiatCurrency("")}, nil
	}
	if err != nil {
		db.MarkFailure(err)
		return FiatQuote{}, err
	}
	if bal.DebtBlocked || bal.UnsettledAmount.IsNegative() || bal.AvailableFiat.LessThanOrEqual(decimal.Zero) {
		return FiatQuote{Currency: frozenFiatCurrency(bal.FiatCurrency)}, nil
	}
	return FiatQuote{Amount: bal.AvailableFiat, Currency: frozenFiatCurrency(bal.FiatCurrency)}, nil
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

	// 法币值按额度比例缩减,保持"剩余额度对应的平均冻结汇率"不变。
	// 绝不用当前汇率反算 —— 那会让历史佣金的法币口径随管理员改汇率而漂移。
	newAvail := bal.AvailableQuota - quota
	takenFiat := fiatTakenBy(bal, quota)

	done, err := claimFreezeOp(tx, userId, quota, takenFiat, refNo, FreezeActionFreeze)
	if err != nil || done {
		return err
	}
	return applyBalance(tx, userId, map[string]any{
		"available_quota": newAvail,
		"available_fiat":  bal.AvailableFiat.Sub(takenFiat),
		"frozen_quota":    bal.FrozenQuota + quota,
	})
}

// FiatQuote 是「按账本口径,这笔额度值多少法币」。
type FiatQuote struct {
	// Amount 是冻结这笔额度时会从 available_fiat 里削走的绝对金额,
	// 也就是提现单上的应付金额(gross)必须等于的那个数。
	Amount decimal.Decimal
	// Currency 是这笔法币余额被攒起来时冻结的币种,不是当前配置。
	Currency string
}

// QuoteWithdrawFiat 给出一笔法币提现单的金额,口径与 FreezeForWithdraw 削走的
// 那笔 available_fiat **逐位相同**。
//
// # 为什么必须由本包给这个数
//
// 提现单的应付金额曾经是提现模块自己算的:quota / QuotaPerUnit × 充值页汇率
// (operation_setting.USDExchangeRate 或 withdraw.rate_freeze_fixed)。而账本里
// 的 available_fiat 是按**计佣当刻的三层折算比例**(分组档 → 兜底档 → 全站汇率)
// 一笔笔攒起来的绝对值。两个数各算各的,于是:
//
//   - 给 vip 分组配 8.5 的结汇比例 → 用户在「我的推广」页看到 ¥850,
//     提现单却按充值汇率开成 ¥100,平台系统性少付,而那个杠杆对打款零影响;
//   - 管理员把充值汇率从 1 改到 7.3 → 同一笔早已按比例 1 冻结的余额
//     开出 7.3 倍的单,平台系统性超付。
//
// 两侧同源之后,「这笔提现打多少钱」在全站只有一个答案:账本上被冻走的那个数。
//
// # 调用约定
//
// 必须与 FreezeForWithdraw 在**同一个事务**里,且调用点已经持有该用户的佣金
// 余额行锁(提现侧由 submitInTx 的第一条语句 LockBalance 保证)。否则两次读
// 之间余额可能被结算或冲正改动,单据金额就会与实际冻走的数对不上。
func QuoteWithdrawFiat(tx *gorm.DB, userId int, quota int64) (FiatQuote, error) {
	if err := validateFreezeArgs(tx, userId, quota, quoteRefNo); err != nil {
		return FiatQuote{}, err
	}
	bal, err := lockBalance(tx, userId)
	if err != nil {
		return FiatQuote{}, err
	}
	if bal.DebtBlocked || bal.UnsettledAmount.IsNegative() {
		return FiatQuote{}, ErrDebtBlocked
	}
	if bal.AvailableQuota < quota {
		return FiatQuote{}, ErrInsufficient
	}
	amount := fiatTakenBy(bal, quota)
	if amount.LessThanOrEqual(decimal.Zero) {
		// available_fiat 是 0(或被人手工写成了负数)而额度是正的。
		// 按这个数开单就是一张 0 元的打款单,必须停在这里并留痕。
		common.SysError("qianye/commission: 用户 " + strconv.Itoa(userId) +
			" 的佣金余额法币折算不可用(available_quota=" +
			strconv.FormatInt(bal.AvailableQuota, 10) + ", available_fiat=" +
			bal.AvailableFiat.String() + "),已拒绝法币提现建单")
		return FiatQuote{}, ErrFiatUnavailable
	}
	return FiatQuote{Amount: amount, Currency: frozenFiatCurrency(bal.FiatCurrency)}, nil
}

// quoteRefNo 只是为了复用 validateFreezeArgs 的参数校验(它要求 refNo 非空)。
// 试算不写任何冻结流水,这个值不会落库。
const quoteRefNo = "quote"

// fiatTakenBy 是「冻走 quota 时,available_fiat 会被削掉多少」。
//
// FreezeForWithdraw 与 QuoteWithdrawFiat 共用它:两处各写一遍等于让"单据金额"
// 与"账本扣减"重新分叉成两个数,而那正是本函数被提出来要消灭的缺陷。
func fiatTakenBy(bal *Balance, quota int64) decimal.Decimal {
	return bal.AvailableFiat.Sub(scaleFiat(bal.AvailableFiat, bal.AvailableQuota-quota, bal.AvailableQuota))
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
	// 上界校验必不可少:这笔额度最终会流向主库的额度列(上界 common.MaxQuota),
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
