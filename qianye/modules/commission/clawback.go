package commission

import (
	"errors"
	"strconv"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/qianye/db"

	"github.com/shopspring/decimal"
	"gorm.io/gorm"
)

// ErrNothingToClawback 表示该下线名下没有可冲正的佣金。
var ErrNothingToClawback = errors.New("commission: 没有可冲正的佣金")

// clawbackIdemKey 生成冲正的幂等键。
//
// 必须在投递到异步队列之前算好并随闭包带走:worker 会重试,
// 每次重试重新生成 key 就等于把一次退款冲正成好几次。
func clawbackIdemKey(taskId string, userId int, quota int64) string {
	if taskId != "" {
		return SourceClawback + ":task:" + taskId + ":" + strconv.FormatInt(quota, 10)
	}
	// 没有任务号时只能用一次性随机键。它保证"同一次退款的多次重试"归并,
	// 但无法归并"同一次退款被上游重复上报"—— 上游没提供任何稳定标识。
	return SourceClawback + ":u" + itoa(userId) + ":" + common.GetUUID()
}

// clawback 为一笔退款生成负额计佣行。
//
// 账本 append-only:冲正永远是一条独立的负额行,绝不去改原行。
// 这样"原本发了多少、后来冲了多少"在任何时刻都能各自查证。
func clawback(inviteeId int, refundQuota int64, idemKey, sourceRef, reason string) error {
	if refundQuota <= 0 {
		return nil
	}
	gdb := db.Get()
	if gdb == nil {
		return db.ErrNotReady
	}

	var origin Accrual
	err := gdb.Where("invitee_id = ? AND gross_amount > 0", inviteeId).
		Order("id desc").Take(&origin).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil // 这个下线从未产生过佣金,无需冲正
	}
	if err != nil {
		db.MarkFailure(err)
		return err
	}

	amount := calcGross(refundQuota, origin.RateBps)
	remaining, err := netAccrued(gdb, inviteeId)
	if err != nil {
		return err
	}
	// 冲正上限是"这个下线到目前为止一共产生过多少净佣金"。
	// 超额冲正会让邀请人为别的下线挣的钱买单。
	if remaining.LessThanOrEqual(decimal.Zero) {
		return nil
	}
	if amount.GreaterThan(remaining) {
		amount = remaining
	}
	if amount.IsZero() {
		return nil
	}

	err = writeAccrual(accrualInput{
		SourceType: SourceClawback,
		IdemKey:    idemKey,
		SourceRef:  sourceRef,
		InviterId:  origin.InviterId,
		InviteeId:  inviteeId,
		BaseQuota:  -refundQuota,
		RateBps:    origin.RateBps,
		Gross:      amount.Neg(),
		UsdRate:    origin.UsdRate,
		// 冲正立即成熟:让它陪着原单等成熟期,等于给"充值→拿佣金→退款"
		// 留出一个可以先提现走人的窗口。
		MatureAt:     0,
		Status:       StatusAccrued,
		RefAccrualId: origin.Id,
		Remark:       truncate(reason, 255),
	})
	if err != nil {
		return err
	}
	clawbackCreated.Add(1)
	return nil
}

// netAccrued 返回某个下线名下的净计佣额(已扣除历史冲正)。
func netAccrued(gdb *gorm.DB, inviteeId int) (decimal.Decimal, error) {
	var raw string
	err := gdb.Model(&Accrual{}).
		Where("invitee_id = ? AND status <> ?", inviteeId, StatusVoided).
		Select("COALESCE(SUM(gross_amount), 0)").Scan(&raw).Error
	if err != nil {
		db.MarkFailure(err)
		return decimal.Zero, err
	}
	d, err := decimal.NewFromString(raw)
	if err != nil {
		return decimal.Zero, nil
	}
	return d, nil
}

// manualClawback 是管理端的人工冲正入口。
//
// quota 是管理员填写的整数额度,不按费率换算 —— 人工冲正的场景(拒付、
// 事后判定为刷单)本来就无法用费率反推。
func manualClawback(accrualId int64, quota int64, idemSuffix, reason string) (*Accrual, error) {
	if quota <= 0 || quota > int64(common.MaxQuota) {
		return nil, errors.New("commission: 冲正额度必须大于 0 且不超过单笔上限")
	}
	gdb := db.Get()
	if gdb == nil {
		return nil, db.ErrNotReady
	}
	var origin Accrual
	if err := gdb.Where("id = ?", accrualId).Take(&origin).Error; err != nil {
		return nil, ErrNothingToClawback
	}

	amount := decimal.NewFromInt(quota)
	remaining, err := netAccrued(gdb, origin.InviteeId)
	if err != nil {
		return nil, err
	}
	if remaining.LessThanOrEqual(decimal.Zero) {
		return nil, ErrNothingToClawback
	}
	if amount.GreaterThan(remaining) {
		amount = remaining
	}

	key := SourceClawback + ":manual:" + idemSuffix
	if err := writeAccrual(accrualInput{
		SourceType:   SourceClawback,
		IdemKey:      key,
		SourceRef:    origin.AccrualNo,
		InviterId:    origin.InviterId,
		InviteeId:    origin.InviteeId,
		RateBps:      origin.RateBps,
		Gross:        amount.Neg(),
		UsdRate:      origin.UsdRate,
		MatureAt:     0,
		Status:       StatusAccrued,
		RefAccrualId: origin.Id,
		Remark:       truncate(reason, 255),
	}); err != nil {
		return nil, err
	}
	clawbackCreated.Add(1)

	var created Accrual
	if err := gdb.Where("idem_scope = ? AND idem_key = ?", SourceClawback, normalizeIdemKey(key)).
		Take(&created).Error; err != nil {
		return nil, err
	}
	return &created, nil
}
