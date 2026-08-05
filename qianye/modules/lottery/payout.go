package lottery

import (
	"context"
	"errors"
	"fmt"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/qianye/config"
	"github.com/QuantumNous/new-api/qianye/db"
	"github.com/QuantumNous/new-api/qianye/guard"
	qymodel "github.com/QuantumNous/new-api/qianye/model"
	"github.com/QuantumNous/new-api/qianye/service/audit"
	"github.com/QuantumNous/new-api/qianye/service/twophase"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// payout.go —— 派奖/赔付/退款的逐笔驱动,以及全模块统一的收尾判定 settleGuard。
//
// # 开奖不动钱
//
// 开奖只往 qy_lot_payout 批量插 planned 行(单个扩展库事务 + 唯一键 +
// ON CONFLICT DO NOTHING),**绝不在开奖 handler 里循环 Execute**:
// 那会让一次开奖变成 N 次跨库往返,任何一次失败都留下一个没人收拾的中间态。
//
// # 幂等键由服务端生成
//
// 与报名相反:出款由服务端发起,"谁在重试"是 worker 自己,因此 IdemKey 用
// payout_no + 代次。重入 Execute 必然命中本代次的原单,这正是 worker 敢扫
// paying 行的依据;而代次只在**主库探针确认没生效**之后才 +1,
// 那是"重开一张单不会重复发钱"的唯一前提。

// idemScopePayout 与 qy_fund_orders 的 (idem_scope, idem_key) 唯一索引配套。
const idemScopePayout = "lottery_payout"

// PayoutPlan 是一条待落库的出款计划。
type PayoutPlan struct {
	EntryId int64
	UserId  int
	Kind    string
	Tier    int
	DrawPos int
	Amount  int64
}

// PlanPayouts 批量登记出款计划。**必须在调用方的扩展库事务内执行。**
//
// ON CONFLICT DO NOTHING + uk(act_id, entry_id, kind) 让"开奖跑两遍"在计划层
// 就整体撞键,而不是重复发钱。重复触发被三重挡住:lease 单节点 + 活动状态 CAS
// + 这个唯一键 —— 三道里任何一道单独都不够(lease 会易主、CAS 会被并发的
// 人工操作抢走)。
//
// 金额为 0 的计划直接跳过:twophase 的入口要求 amount > 0,而 0 元出款在账面上
// 也不表达任何事实。这只可能出现在奖池分配的截断残差为 0 时,守恒式仍然成立。
//
// # 文本奖是这条规则唯一的例外
//
// kind='text' 的金额恒为 0(它发的是一段兑换文本,不是额度),按上面那条规则
// 会被**整批丢掉且不报错** —— 中奖者永远看不到自己的奖品,而系统零告警。
// 所以跳过条件必须显式排除它。
//
// 文本奖落库即 granted(终态),而不是 planned:
//
//	DrivePayouts 扫的是 status IN (planned, paying, failed),finishIfDone 的
//	未终态集合也是同样三个。落成 planned 意味着文本奖**默认会被出款 worker
//	捡走**,只能靠在那两处各补一个 kind 过滤来挡,漏一处就是一笔文本奖被当成
//	资金单驱动。granted 则是默认安全:它天然不在任何一个已有的扫描集合里,
//	那两处一行都不用改。安全性来自结构,而不是来自某个人记得写了 if。
func PlanPayouts(tx *gorm.DB, actId int64, plans []PayoutPlan) error {
	rows := make([]Payout, 0, len(plans))
	now := common.GetTimestamp()
	for _, p := range plans {
		if p.Kind != PayoutText && p.Amount <= 0 {
			continue
		}
		status := PayoutPlanned
		if p.Kind == PayoutText {
			status = PayoutGranted
		}
		rows = append(rows, Payout{
			PayoutNo:    newPayoutNo(),
			ActId:       actId,
			EntryId:     p.EntryId,
			Kind:        p.Kind,
			UserId:      p.UserId,
			Tier:        p.Tier,
			DrawPos:     p.DrawPos,
			AmountQuota: p.Amount,
			Status:      status,
			CreatedAt:   now,
		})
	}
	if len(rows) == 0 {
		return nil
	}
	return tx.Clauses(clause.OnConflict{DoNothing: true}).CreateInBatches(rows, 200).Error
}

// DrivePayouts 跑一轮出款。由 lease 任务调用,单节点执行。
//
// # 为什么扫 paying
//
// worker 先把行 CAS 成 paying 再调 Execute。进程若在这两步之间崩溃,只扫
// planned/failed 的设计会让那一行**永久卡在 paying 且再也不会被扫到** ——
// 一笔中奖派奖永久丢失,还不会告警。因为 IdemKey = payout_no,重入 Execute
// 必然命中原单,扫 paying 是安全的。
func DrivePayouts(ctx context.Context) {
	cfg := config.Get().Lottery
	if !cfg.Enabled {
		return
	}
	gdb := db.Get()
	if gdb == nil {
		return
	}
	// 句柄一次性绑上租约的预算:逐条 WithContext 漏一条,就等于在这条链路上开了一个
	// 没有上界的口子 —— 语句级预算只对 WithContext 的语句生效。
	gdb = gdb.WithContext(ctx)
	now := common.GetTimestamp()

	var rows []Payout
	err := gdb.WithContext(ctx).
		Where("status IN ? AND attempts < ? AND next_attempt_at <= ?",
			[]string{PayoutPlanned, PayoutPaying, PayoutFailed}, cfg.PayoutMaxAttempts, now).
		Order("id asc").Limit(200).Find(&rows).Error
	if err != nil {
		db.MarkFailure(err)
		common.SysError("qianye/lottery: 扫描待出款失败: " + err.Error())
		return
	}
	for i := range rows {
		// 失去租约后必须立刻停手,否则会与接管节点双跑。
		if ctx.Err() != nil {
			return
		}
		drivePayout(ctx, gdb, &rows[i])
	}
}

func drivePayout(ctx context.Context, gdb *gorm.DB, p *Payout) {
	// CAS 带上读到的那个状态:RowsAffected == 0 即别的节点抢到了,跳过。
	res := gdb.WithContext(ctx).Model(&Payout{}).
		Where("id = ? AND status = ?", p.Id, p.Status).
		Updates(map[string]any{
			"status":   PayoutPaying,
			"attempts": gorm.Expr("attempts + 1"),
		})
	if res.Error != nil {
		db.MarkFailure(res.Error)
		return
	}
	if res.RowsAffected == 0 {
		return
	}
	p.Attempts++

	req := twophase.Request{
		Kind:        qymodel.KindLotteryPayout,
		IdemScope:   idemScopePayout,
		IdemKey:     payoutIdemKey(p),
		UserId:      p.UserId,
		AmountQuota: p.AmountQuota,
		RefType:     "lottery_payout",
		RefId:       p.PayoutNo,
		LocalDetail: func(tx *gorm.DB, o *qymodel.FundOrder) error {
			return tx.Model(&Payout{}).Where("id = ?", p.Id).
				Update("order_no", o.OrderNo).Error
		},
		MainApply: func(tx *gorm.DB, o *qymodel.FundOrder) error {
			return creditMainQuota(tx, p)
		},
		AfterCommit: func(o *qymodel.FundOrder) {
			afterCredit(p, o.OrderNo)
		},
		LocalCommit: func(tx *gorm.DB, o *qymodel.FundOrder) error {
			return markPayoutPaid(tx, p.PayoutNo)
		},
	}
	// 幂等键是服务端生成的 payout_no + 代次,正常路径下不可能换参重放;但资金单
	// 一旦被人手工改过,没有指纹就只能默默按原单返回。代价是一次 sha256。
	req.Fingerprint = req.Digest(p.Kind)

	order, err := twophase.Execute(ctx, req)
	if err != nil {
		failPayout(ctx, gdb, p, order, err)
		return
	}
	if err := settleGuard(ctx, order, func(tx *gorm.DB) error {
		return markPayoutPaid(tx, p.PayoutNo)
	}); err != nil {
		// 尚未落定:保持 paying 不动,交 twophase 补偿任务 + 下一轮 worker。
		common.SysError(fmt.Sprintf("qianye/lottery: 出款 %s 尚未落定: %v", p.PayoutNo, err))
	}
}

// creditMainQuota 在主库事务内给用户加额度。
//
// 硬性约束(违反即资损):绝不 IncreaseUserQuota(无事务、无溢出校验,
// 且在批量模式下钱什么时候到账不可确定);加款必须带 headroom 上限 ——
// users.quota 是 int32 且上游全无溢出校验。
func creditMainQuota(tx *gorm.DB, p *Payout) error {
	headroom := int64(common.MaxQuota) - p.AmountQuota

	var u model.User
	// First 自带软删除过滤。绝不 Unscoped:给已注销账号加额度等于把钱转进一个
	// 再也取不出来的账户。
	if err := model.QyLockForUpdate(tx).Where("id = ?", p.UserId).First(&u).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return errUserUnavailable
		}
		return err
	}
	// 锁内复检:报名到派奖之间可能隔了几天,用户可能已被封禁。
	// 这里返回错误会让这一笔转 held 交人工 —— 钱是他赢的,不能因为账号状态
	// 自动没收,也不能给一个注销账号打钱。做成"配置项"是实现不了的选项。
	if u.Status != common.UserStatusEnabled {
		return errUserUnavailable
	}
	if int64(u.Quota) > headroom {
		return errQuotaOverflow
	}

	res := tx.Model(&model.User{}).
		Where("id = ? AND quota <= ?", p.UserId, headroom).
		Update("quota", gorm.Expr("quota + ?", p.AmountQuota))
	if res.Error != nil {
		return res.Error
	}
	if res.RowsAffected != 1 {
		return errQuotaOverflow
	}
	return nil
}

// afterCredit 处理主库提交之后的收尾。钱已经到账,任何失败都不能回滚。
func afterCredit(p *Payout, orderNo string) {
	if err := model.InvalidateUserCache(p.UserId); err != nil {
		common.SysError("qianye/lottery: 失效用户缓存失败(额度已入账): " + err.Error())
	}
	// 退款用 **LogTypeRefund**,派奖与赔付用 **LogTypeSystem**。
	//
	// 退款绝不能用 LogTypeConsume:那会让用户的"本月消费"凭空变小 ——
	// 与 violation.refundFee 的口径一致。派奖也不能用 LogTypeTopup:
	// 那会污染充值统计与按充值返佣的口径。
	logType := model.LogTypeSystem
	if p.Kind == PayoutRefund {
		logType = model.LogTypeRefund
	}
	model.QyRecordLedgerLog(p.UserId, logType,
		fmt.Sprintf("%s %d 额度 [%s]", payoutLabel(p.Kind), p.AmountQuota, p.PayoutNo), orderNo,
		map[string]any{
			"qy_module":        "lottery",
			"qy_lot_payout_no": p.PayoutNo,
			"qy_lot_kind":      p.Kind,
			"qy_quota":         p.AmountQuota,
		})
}

func payoutLabel(kind string) string {
	switch kind {
	case PayoutPrize:
		return "抽奖中奖到账"
	case PayoutWin:
		return "竞猜赔付到账"
	default:
		return "活动退款到账"
	}
}

// payoutIdemKey 是这一笔当前代次的幂等键。
//
// 代次 0 不带后缀:第一次出款的键就是 payout_no 本身,读日志的人不必先去
// 理解代次机制才能把一行日志和一张资金单对上。
func payoutIdemKey(p *Payout) string {
	if p.Epoch <= 0 {
		return p.PayoutNo
	}
	return p.PayoutNo + "#" + deci(p.Epoch)
}

// markPayoutPaid 把出款推进终态。
//
// LocalCommit、post-Execute 补做、补偿 Resolver、管理端重试四处共用同一个函数。
// 幂等锚点是"已经出手过的三种状态 → paid"的 CAS。
//
// 允许的来源是 paying / failed / held,**唯独不含 planned**:planned 是一笔
// 从未执行过的出款,把它直接推成已到账等于系统认为钱给过了而用户永远收不到。
// 而 held 必须在列:一笔在不可判定期间被转人工的出款,随后可能被补偿任务确认
// 主库已生效 —— 那时钱已经到账,只认 paying 的 CAS 会让这一行永远停在 held,
// 红点永远不消。放宽到这三种不会重复发钱:钱动没动由资金单与主库 outbox 决定,
// 这个函数只写业务行的状态。
func markPayoutPaid(tx *gorm.DB, payoutNo string) error {
	now := common.GetTimestamp()
	res := tx.Model(&Payout{}).
		Where("payout_no = ? AND status IN ?", payoutNo,
			[]string{PayoutPaying, PayoutFailed, PayoutHeld}).
		Updates(map[string]any{
			"status":     PayoutPaid,
			"settled_at": now,
			"last_error": "",
		})
	if res.Error != nil {
		return res.Error
	}
	if res.RowsAffected == 0 {
		// 别的路径已经收过尾。绝不在这里"补一次" —— 那正是重复加钱的形状。
		return nil
	}
	return nil
}

// failPayout 处理 Execute 返回错误的三种结局。
//
//   - Failed 且探针复核确实未生效 → **换一个代次**退避重试;次数耗尽转 held。
//   - Failed 但探针说已生效 → 两者矛盾,直接 held,绝不重试(会重复加钱)。
//   - pending / uncertain → **保持 paying 不动**,交 twophase 补偿任务;
//     重试预算耗尽后转 held,绝不让它停在一个再也不会被扫到的 paying 上。
//
// 第一种结局必须换代次:资金单一旦是 Failed,重入 Execute 只会幂等命中原单并
// 返回 ErrOrderFailed —— 沿用同一个幂等键的"重试"是纯粹的空转,自动重试与
// 管理端按钮都发不出这笔钱。换代次是安全的,前提正是探针刚刚确认过主库没动。
func failPayout(ctx context.Context, gdb *gorm.DB, p *Payout, order *qymodel.FundOrder, cause error) {
	msg := audit.Truncate(cause.Error(), 512)
	maxAttempts := config.Get().Lottery.PayoutMaxAttempts

	if order == nil || order.Status != qymodel.StatusFailed {
		// 不可判定:钱可能已经动了,绝不换代次重开单。保持 paying 让补偿任务
		// 与下一轮 worker 接手;预算耗尽就转人工 —— 没有这个出口,这一行会在
		// attempts 打满之后同时掉出 worker 的扫描范围与 held 的红点范围,
		// 变成一笔谁都不知道的丢单。
		if p.Attempts >= maxAttempts {
			holdPayout(ctx, gdb, p, "结果长期不可判定,重试预算已耗尽: "+msg)
			return
		}
		if err := gdb.WithContext(ctx).Model(&Payout{}).Where("id = ?", p.Id).
			Update("last_error", msg).Error; err != nil {
			db.MarkFailure(err)
		}
		common.SysError(fmt.Sprintf(
			"qianye/lottery: 出款 %s 结果不可判定,保持 paying 交补偿任务: %s", p.PayoutNo, msg))
		return
	}

	if mainSideApplied(order.OrderNo) {
		holdPayout(ctx, gdb, p, "资金单被判失败但主库探针显示已生效,需人工核对")
		return
	}
	if p.Attempts >= maxAttempts {
		holdPayout(ctx, gdb, p, "重试次数已耗尽: "+msg)
		return
	}

	// 指数退避,上界 300 秒。防止一条坏单反复打爆主库。
	delay := int64(1) << uint(minInt(p.Attempts, 8))
	if delay > 300 {
		delay = 300
	}
	if err := gdb.WithContext(ctx).Model(&Payout{}).
		Where("id = ? AND status = ?", p.Id, PayoutPaying).
		Updates(map[string]any{
			"status":          PayoutFailed,
			"epoch":           gorm.Expr("epoch + 1"),
			"next_attempt_at": common.GetTimestamp() + delay,
			"last_error":      msg,
		}).Error; err != nil {
		db.MarkFailure(err)
	}
}

// holdPayout 把出款转人工。**绝不自动放弃、绝不自动改判。**
//
// 资金系统必须有"我不知道 / 我不能自动决定,交给人"这个合法出口。
// 自动判失败会让一笔用户赢来的钱凭空消失;自动重试则可能重复加钱。
func holdPayout(ctx context.Context, gdb *gorm.DB, p *Payout, reason string) {
	res := gdb.WithContext(ctx).Model(&Payout{}).
		Where("id = ? AND status <> ?", p.Id, PayoutPaid).
		Updates(map[string]any{
			"status":     PayoutHeld,
			"last_error": audit.Truncate(reason, 512),
		})
	if res.Error != nil {
		db.MarkFailure(res.Error)
		return
	}
	if res.RowsAffected == 0 {
		return
	}
	common.SysError(fmt.Sprintf(
		"qianye/lottery: 出款 %s 已转人工(用户 %d,额度 %d): %s",
		p.PayoutNo, p.UserId, p.AmountQuota, reason))
	raiseFlag(ctx, p.ActId, FlagPayoutStuck, p.PayoutNo+": "+reason)
	audit.Write(nil, audit.Entry{
		TraceNo:      p.PayoutNo,
		Category:     auditCategory,
		Action:       "lottery.payout.held",
		ActorType:    qymodel.ActorSystem,
		TargetUserId: p.UserId,
		AmountQuota:  p.AmountQuota,
		Result:       qymodel.ResultPending,
		Reason:       audit.Truncate(reason, 512),
	})
}

// RetryPayout 是管理端"重试"按钮的落点。
//
// 只把 held/failed 推回 planned 并清零退避,**绝不新建 payout 行** ——
// 新建一行就是绕过 uk(act_id, entry_id, kind) 重复发钱。
//
// 但"推回 planned"本身不足以让钱发出去:本代次的资金单若已是 Failed,重入
// Execute 只会再拿到 ErrOrderFailed。所以这里先把本代次的资金单读出来,按它的
// 终态决定唯一安全的动作:
//
//	不存在        → 什么都没发生过,原样重排即可
//	Success       → 钱其实已经到账,直接收尾,绝不再发一次
//	Failed + 探针说没生效 → 换代次重开单(与自动重试同一条判据)
//	Failed + 探针说已生效 → 拒绝,这是需要人核对的矛盾现场
//	pending/uncertain     → 拒绝,交补偿任务;此刻重开单就是赌一把
func RetryPayout(ctx context.Context, payoutNo string) error {
	gdb := db.Get()
	if gdb == nil {
		return db.ErrNotReady
	}
	var p Payout
	if err := gdb.WithContext(ctx).Where("payout_no = ?", payoutNo).Take(&p).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return errPayoutNotFound
		}
		db.MarkFailure(err)
		return wrapInternal("重试出款", err)
	}
	if p.Status != PayoutHeld && p.Status != PayoutFailed {
		return errStatusConflict
	}

	nextEpoch := p.Epoch
	var order qymodel.FundOrder
	err := gdb.WithContext(ctx).
		Where("idem_scope = ? AND idem_key = ?", idemScopePayout, payoutIdemKey(&p)).
		Take(&order).Error
	switch {
	case errors.Is(err, gorm.ErrRecordNotFound):
		// 没有单据,本代次一步都没走出去,原样重排。
	case err != nil:
		db.MarkFailure(err)
		return wrapInternal("重试出款", err)
	case order.Status == qymodel.StatusSuccess:
		return gdb.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
			return markPayoutPaid(tx, payoutNo)
		})
	case order.Status == qymodel.StatusFailed:
		if mainSideApplied(order.OrderNo) {
			return errPayoutNeedsManual
		}
		nextEpoch = p.Epoch + 1
	default:
		return errPayoutNeedsManual
	}

	res := gdb.WithContext(ctx).Model(&Payout{}).
		Where("payout_no = ? AND status = ?", payoutNo, p.Status).
		Updates(map[string]any{
			"status":          PayoutPlanned,
			"epoch":           nextEpoch,
			"attempts":        0,
			"next_attempt_at": 0,
		})
	if res.Error != nil {
		db.MarkFailure(res.Error)
		return wrapInternal("重试出款", res.Error)
	}
	if res.RowsAffected == 0 {
		return errStatusConflict
	}
	return nil
}

// resolvePayoutAfterCompensation 是补偿任务确认主库已生效后的收尾回调。
// 必须幂等:同一张单可能被补偿多次,幂等由 markPayoutPaid 的 CAS 保证。
func resolvePayoutAfterCompensation(ctx context.Context, order *qymodel.FundOrder) error {
	if ctx.Err() != nil {
		return ctx.Err()
	}
	gdb := db.Get()
	if gdb == nil {
		return db.ErrNotReady
	}
	return gdb.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		return markPayoutPaid(tx, order.RefId)
	})
}

// InstallResolvers 注册两个补偿回调。由模块的 InstallHooks 调用。
//
// 缺了它们,补偿任务会把资金单推成 success 而业务侧永远停在中间态 ——
// 这在本仓的 violation 上真实发生过一次。
func InstallResolvers() {
	twophase.RegisterResolver(qymodel.KindLotteryEntry, resolveEntryAfterCompensation)
	twophase.RegisterResolver(qymodel.KindLotteryPayout, resolvePayoutAfterCompensation)
}

// ─────────────────────────── 共用收尾判定 ───────────────────────────

// settleGuard 是全模块统一的收尾姿势。**每一个 twophase.Execute 的调用点都必须走它。**
//
// # 为什么 err == nil 不够
//
// twophase.Execute 在三条路径上返回 nil 而业务侧并未落定:
//
//	B  主库已扣款、扩展库回写失败 —— execute.go 的阶段四只 SysError 然后
//	   return order, nil。这条最危险也最容易被漏掉。
//	D  幂等命中原单 Success —— LocalDetail / MainApply / LocalCommit 全部不执行。
//	E  markSuccess 的 CAS 落空且对方推成 Success —— LocalCommit 不执行。
//
// D 与 E 下业务副作用由**别人**做过或将要做;B 下没有任何人做过。三者无法在
// 调用点区分,所以统一做法是:确认单据已是 Success,再用与 LocalCommit
// **同一个幂等函数**补做一次,然后回读确认。
//
// 宁可让用户看到"尚未落定,请稍后复核",也绝不对外声称一笔并不存在的扣费或派奖。
func settleGuard(ctx context.Context, order *qymodel.FundOrder, apply func(tx *gorm.DB) error) error {
	if order == nil || order.Status != qymodel.StatusSuccess {
		return errNotSettled
	}
	gdb := db.Get()
	if gdb == nil {
		return db.ErrNotReady
	}
	// 收尾走独立预算并切断调用方的取消链:主库那一侧已经定局,一次客户端超时
	// 不该把收尾也打掉(与 twophase.settleContext 同形)。
	settleCtx, cancel := guard.ColdContext(context.WithoutCancel(ctx))
	defer cancel()

	if err := gdb.WithContext(settleCtx).Transaction(apply); err != nil {
		db.MarkFailure(err)
		common.SysError(fmt.Sprintf(
			"qianye/lottery: 单号 %s 主库已生效但扩展库收尾失败,交补偿任务: %v", order.OrderNo, err))
		return errNotSettled
	}
	return nil
}

// mainSideApplied 用主库 outbox 探针判定资金变更是否真的生效。
//
// 主库事务在 commit 阶段断连时 GORM 会返回错误,但事务其实可能已经提交。
// 探针本身失败时一律按"可能已生效"处理:宁可让一笔出款多等一会儿人工,
// 也不能重复发放 —— 前者是运营成本,后者是可被反复利用的超发漏洞。
func mainSideApplied(orderNo string) bool {
	if !config.Get().TwoPhase.OutboxEnabled() {
		return false
	}
	applied, err := model.QyProbeFundOutbox(orderNo)
	if err != nil {
		common.SysError("qianye/lottery: 单号 " + orderNo +
			" 探测主库 outbox 失败,按可能已生效处理: " + err.Error())
		return true
	}
	return applied
}

// raiseFlag 落一条对账异常,管理端红点直读。
//
// 同一个 (act_id, code) 未解决时不重复插:一条卡单每 10 秒被扫一次,
// 不去重会在几小时内把异常列表刷成同一条消息的几千份拷贝,
// 而那正好会淹没真正的新异常。
func raiseFlag(ctx context.Context, actId int64, code, detail string) {
	gdb := db.Get()
	if gdb == nil {
		return
	}
	// 句柄一次性绑上租约的预算:逐条 WithContext 漏一条,就等于在这条链路上开了一个
	// 没有上界的口子 —— 语句级预算只对 WithContext 的语句生效。
	gdb = gdb.WithContext(ctx)
	var n int64
	if err := gdb.WithContext(ctx).Model(&Flag{}).
		Where("act_id = ? AND code = ? AND resolved = ?", actId, code, false).
		Count(&n).Error; err != nil {
		db.MarkFailure(err)
		return
	}
	if n > 0 {
		return
	}
	row := &Flag{
		ActId:     actId,
		Code:      code,
		Detail:    audit.Truncate(detail, 512),
		CreatedAt: common.GetTimestamp(),
	}
	if err := gdb.WithContext(ctx).Create(row).Error; err != nil {
		db.MarkFailure(err)
	}
}

func minInt(a, b int) int {
	if a < b {
		return a
	}
	return b
}
