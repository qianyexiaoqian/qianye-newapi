package commission

import (
	"context"
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
func clawback(ctx context.Context, inviteeId int, refundQuota int64, idemKey, sourceRef, reason string) error {
	if refundQuota <= 0 {
		return nil
	}
	gdb := db.Get()
	if gdb == nil {
		return db.ErrNotReady
	}
	gdb = gdb.WithContext(ctx)

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

	amount := calcGross(refundQuota, origin.RateUnits)
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

	inserted, err := writeAccrual(ctx, accrualInput{
		SourceType: SourceClawback,
		IdemKey:    idemKey,
		SourceRef:  sourceRef,
		InviterId:  origin.InviterId,
		InviteeId:  inviteeId,
		BaseQuota:  -refundQuota,
		// 冲正原样复制原单冻结的费率与分组,绝不用当前值:原单按 8% 发出去、
		// 退款时按现行的 5% 冲回来,差额就永久留在邀请人账上。
		// 这与复制 UsdRate 是同一个道理。
		RateUnits: origin.RateUnits,
		RateGroup: origin.RateGroup,
		Gross:     amount.Neg(),
		UsdRate:   origin.UsdRate,
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
	// 只有真的插入了新行才计数:worker 重试与上游重复上报都会走到这里,
	// 幂等命中时自增会让"到底冲正了几笔"这个数字失去意义。
	if inserted {
		clawbackCreated.Add(1)
	}
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
func manualClawback(ctx context.Context, accrualId int64, quota int64, idemSuffix, reason string) (*Accrual, error) {
	if quota <= 0 || quota > int64(common.MaxQuota) {
		return nil, errors.New("commission: 冲正额度必须大于 0 且不超过单笔上限")
	}
	gdb := db.Get()
	if gdb == nil {
		return nil, db.ErrNotReady
	}
	gdb = gdb.WithContext(ctx)
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
	inserted, err := writeAccrual(ctx, accrualInput{
		SourceType: SourceClawback,
		IdemKey:    key,
		SourceRef:  origin.AccrualNo,
		InviterId:  origin.InviterId,
		InviteeId:  origin.InviteeId,
		// BaseQuota 冻结"管理员这一次填了多少",充当幂等指纹的金额分量。
		// 不能拿 Gross 反推:Gross 已被 remaining 削过,同一个请求在不同
		// 时刻会落出不同的值,拿它比对会把合法重试误判成冲突。
		// 取负号与自动冲正路径(clawback)保持同一符号约定。
		BaseQuota:    -quota,
		RateUnits:    origin.RateUnits,
		RateGroup:    origin.RateGroup,
		Gross:        amount.Neg(),
		UsdRate:      origin.UsdRate,
		MatureAt:     0,
		Status:       StatusAccrued,
		RefAccrualId: origin.Id,
		Remark:       truncate(reason, 255),
	})
	if err != nil {
		return nil, err
	}

	var created Accrual
	if err := gdb.Where("idem_scope = ? AND idem_key = ?", SourceClawback, normalizeIdemKey(key)).
		Take(&created).Error; err != nil {
		return nil, err
	}
	if !inserted {
		// 幂等命中。必须确认重放的确实是同一个请求:client_request_id 由前端
		// 生成并在弹窗打开时缓存,管理员改了 accrual_id 或金额再提交会复用同一个键。
		// 不比对就会返回旧单,而调用方按"本次新建"写下一条金额虚高的成功审计。
		if err := sameClawbackRequest(&created, accrualId, quota); err != nil {
			return nil, err
		}
		return &created, nil
	}
	clawbackCreated.Add(1)
	return &created, nil
}

// ErrClawbackIdemConflict 表示同一个幂等键被换了参数重放。
var ErrClawbackIdemConflict = errors.New("commission: 幂等键已被另一次冲正请求占用")

// clawbackAuditAmount 返回一次冲正在审计里应当记的金额。
//
// 必须取账本行的真实 Gross,而不是请求里的 quota:幂等重放与 remaining 削减
// 两种情况下 quota 都与资金侧实际发生的金额不符,而审计表是这套资金系统
// 事后仲裁的唯一凭据。先 Floor 再取整,不用 QuotaFromDecimal 自带的
// 四舍五入 —— 审计金额宁可略小于账本也不能虚报。取绝对值是因为
// AmountQuota 只记标量,"这是一次冲正"由 Action 表达。
func clawbackAuditAmount(a *Accrual) int64 {
	return int64(common.QuotaFromDecimal(a.GrossAmount.Abs().Floor()))
}

// sameClawbackRequest 比对幂等命中的旧单与本次请求的资金要素。
//
// 只比"请求本身说了什么"(冲哪一行、冲多少),不比落库后的 Gross ——
// Gross 受 remaining 削减,同一个请求在不同时刻可能得出不同的值。
func sameClawbackRequest(created *Accrual, accrualId, quota int64) error {
	if created.RefAccrualId != accrualId {
		return ErrClawbackIdemConflict
	}
	// BaseQuota 是本次修复才开始写入的指纹分量,历史行为 0。
	// 空指纹一律放行:把升级前落的老单判成冲突,等于让管理员永远重试不了。
	if created.BaseQuota != 0 && created.BaseQuota != -quota {
		return ErrClawbackIdemConflict
	}
	return nil
}
