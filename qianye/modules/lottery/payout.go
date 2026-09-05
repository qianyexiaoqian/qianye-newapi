package lottery

import (
	"context"
	"errors"
	"fmt"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/qianye/config"
	"github.com/QuantumNous/new-api/qianye/db"
	"github.com/QuantumNous/new-api/qianye/guard"
	qymodel "github.com/QuantumNous/new-api/qianye/model"
	"github.com/QuantumNous/new-api/qianye/modules/mall"
	"github.com/QuantumNous/new-api/qianye/modules/stardust"
	"github.com/QuantumNous/new-api/qianye/service/audit"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// payout.go —— 派奖/赔付/退款的逐笔驱动。
//
// # 开奖不动钱
//
// 开奖只往 qy_lot_payout 批量插 planned 行(单个扩展库事务 + 唯一键 +
// ON CONFLICT DO NOTHING),**绝不在开奖 handler 里循环入账**:
// 那会让一次开奖变成 N 次往返,任何一次失败都留下一个没人收拾的中间态。
//
// # 每一笔一个扩展库事务
//
// 钱现在只在扩展库:worker 逐笔 CAS 认领(planned/failed → paying),然后在
// **一个**扩展库事务里做三件事 —— 出款行 paying→paid 的 CAS、活动合计的补计
// (A)、stardust.Credit(U)。任何一步失败整笔回滚,出款行退回 failed 退避重试,
// 预算耗尽转 held 交人。没有主库、没有 outbox、没有代次:失败的尝试一个字节
// 都不留在账本上,所以重试永远是安全的重来,不是"重开一张单"。
//
// # 幂等键由服务端生成
//
// 与报名相反:出款由服务端发起,"谁在重试"是 worker 自己,因此账本上的幂等键是
// `lotpay:<payout_no>`。两个节点同时认领同一笔时,晚到的那一路在 CAS 上落空,
// 一步都不会走到 Credit;万一走到,DoNothing 的幂等行也会让它记不上第二笔。
//
// # 锁序(design-15 §3.3)
//
// 持有余额行锁的事务不得再去锁活动行。所以事务里活动合计的 UPDATE 排在
// Credit **之前**(活动 → 余额),与报名那一侧(reserveEntry → Debit)同一个方向,
// 反向顺序在结构上不可能出现。

// idemScopePayout 是出款在 qy_sd_ledger 上的幂等作用域。
const idemScopePayout = "lot_payout"

// refTypePayout 是出款流水的关联单据类型(qy_sd_ledger.ref_type)。
const refTypePayout = "lot_payout"

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
// 金额为 0 的计划直接跳过:stardust.Credit 的入口要求 amount > 0,而 0 星屑的
// 出款在账面上也不表达任何事实。这只可能出现在奖池分配的截断残差为 0 时,
// 守恒式仍然成立。
//
// # 文本奖是这条规则唯一的例外
//
// kind='text' 的金额恒为 0(它发的是一段兑换文本,不是星屑),按上面那条规则
// 会被**整批丢掉且不报错** —— 中奖者永远看不到自己的奖品,而系统零告警。
// 所以跳过条件必须显式排除它。
//
// 文本奖落库即 granted(终态),而不是 planned:
//
//	DrivePayouts 扫的是 status IN (planned, paying, failed),finishIfDone 的
//	未终态集合也是同样三个。落成 planned 意味着文本奖**默认会被出款 worker
//	捡走**,只能靠在那两处各补一个 kind 过滤来挡,漏一处就是一笔文本奖被当成
//	星屑驱动。granted 则是默认安全:它天然不在任何一个已有的扫描集合里,
//	那两处一行都不用改。安全性来自结构,而不是来自某个人记得写了 if。
func PlanPayouts(tx *gorm.DB, actId int64, plans []PayoutPlan) error {
	rows := make([]Payout, 0, len(plans))
	now := common.GetTimestamp()
	for _, p := range plans {
		// 商品奖(kind='product')与文本奖一样金额恒为 0,同样必须显式排除在
		// "amount<=0 跳过"之外;但它落 planned 而不是 granted:它的履行(生成商城
		// 订单)由出款 worker 做,做完才 granted。
		if p.Kind != PayoutText && p.Kind != PayoutProduct && p.Amount <= 0 {
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
// worker 先把行 CAS 成 paying 再开事务。进程若在这两步之间崩溃,只扫
// planned/failed 的设计会让那一行**永久卡在 paying 且再也不会被扫到** ——
// 一笔中奖派奖永久丢失,还不会告警。事务里 paying→paid 的 CAS 与账本的幂等键
// 让重入是安全的,扫 paying 因此也是安全的。
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

	if p.Kind == PayoutProduct {
		driveProductPayout(ctx, gdb, p)
		return
	}

	err := gdb.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		// 先推终态:paying→paid 的 CAS 落空说明别的路径已经收过尾,一步都不许再走 ——
		// 那正是重复加钱的形状。它同时是下面两条写入的幂等锚点。
		moved, err := markPayoutPaid(tx, p.PayoutNo)
		if err != nil {
			return err
		}
		if !moved {
			return nil
		}
		// A:活动合计(活动行锁)必须排在 U:入账(余额行锁)之前,见文件头的锁序。
		if err := addPaidPayoutToActivityTotals(tx, p); err != nil {
			return err
		}
		kind := stardust.KindLotPrize
		if p.Kind == PayoutRefund {
			kind = stardust.KindLotRefund
		}
		res, err := stardust.Credit(tx, stardust.Posting{
			UserId:    p.UserId,
			Kind:      kind,
			Amount:    p.AmountQuota,
			IdemScope: idemScopePayout,
			IdemKey:   payoutIdemKey(p.PayoutNo),
			RefType:   refTypePayout,
			RefNo:     p.PayoutNo,
			ActNo:     actNoOf(tx, p.ActId),
			Remark:    payoutLabel(p.Kind),
		})
		if err != nil {
			return err
		}
		return tx.Model(&Payout{}).Where("id = ?", p.Id).
			Update("order_no", res.LedgerNo).Error
	})
	if err != nil {
		failPayout(ctx, gdb, p, err)
	}
}

// driveProductPayout 履行一笔商品奖:在**一个**扩展库事务里做 paying→granted 的 CAS
// 与 mall.GrantPrizeTx(生成 0 元商城订单),把商城单号写回 mall_order_no。
//
// 调用方已经把行 CAS 成 paying。幂等由两道保证:这里的 CAS 只认 paying,
// 商城那一侧的幂等键是 lotprize:<payout_no> —— 重入命中原单、不重复建单。
// 任何一步失败整笔回滚,退回 failed 退避重试,预算耗尽转 held 交人(failPayout)。
//
// 事务提交之后的两件事:兑换码库存不足时挂旗告警(订单停在 paid,补码后由商城的
// 上传接口补齐);套餐奖立即发订阅(mall.FulfillPrizePlan,失败由 mall.reconcile 兜)。
func driveProductPayout(ctx context.Context, gdb *gorm.DB, p *Payout) {
	var grant *mall.GrantResult
	err := gdb.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var act Activity
		if err := tx.Select("id, act_no").Where("id = ?", p.ActId).Take(&act).Error; err != nil {
			return err
		}
		var prize Prize
		if err := tx.Select("product_no, prize_type").Where("act_id = ? AND tier = ?", p.ActId, p.Tier).
			Take(&prize).Error; err != nil {
			return err
		}
		if prize.Type() != PrizeTypeProduct || prize.ProductNo == "" {
			return fmt.Errorf("qianye/lottery: 出款 %s 指向的奖档 %d 不是商品奖", p.PayoutNo, p.Tier)
		}
		g, err := mall.GrantPrizeTx(tx, mall.GrantPrizeInput{
			UserId: p.UserId, ProductNo: prize.ProductNo,
			RefType: refTypePayout, RefNo: p.PayoutNo, ActNo: act.ActNo,
		})
		if err != nil {
			return err
		}
		res := tx.Model(&Payout{}).
			Where("id = ? AND status = ?", p.Id, PayoutPaying).
			Updates(map[string]any{
				"status":        PayoutGranted,
				"mall_order_no": g.Order.OrderNo,
				"settled_at":    common.GetTimestamp(),
				"last_error":    "",
			})
		if res.Error != nil {
			return res.Error
		}
		if res.RowsAffected != 1 {
			// 别的路径已经收过尾:整笔回滚,商城那一侧的建单随之消失(幂等键会让下一次
			// 重入命中它自己的原单,而不是这一次的)。
			return errStatusConflict
		}
		grant = g
		return nil
	})
	if err != nil {
		if errors.Is(err, errStatusConflict) {
			return
		}
		failPayout(ctx, gdb, p, err)
		return
	}
	p.Status, p.MallOrderNo = PayoutGranted, grant.Order.OrderNo
	afterProductGrant(ctx, p.ActId, p.PayoutNo, grant)
}

// afterProductGrant 是商品奖在扩展库事务提交之后的两步:码不够时挂旗告警,
// 套餐奖立即发订阅。转盘(spinWheel)与批次派奖(driveProductPayout)共用。
func afterProductGrant(ctx context.Context, actId int64, payoutNo string, g *mall.GrantResult) {
	if g == nil || g.Order == nil {
		return
	}
	if g.AwaitingCode {
		detail := "出款 " + payoutNo + " 的兑换码商品 " + g.Order.ProductNo + " 库存不足,商城订单 " +
			g.Order.OrderNo + " 停在 paid 等待补码(上传兑换码后自动发放)"
		if raiseFlag(ctx, actId, FlagPrizeCodeShort, detail) {
			common.SysError("qianye/lottery: " + detail)
		}
	}
	if g.NeedsPlanFulfill {
		// 主库那一步用一份新的冷预算:调用方的 ctx 可能已经被派奖事务用掉大半,
		// 而发订阅失败也不是灾难 —— mall.reconcile 会再发,订单在那之前停在 paid。
		fctx, cancel := guard.ColdContext(context.WithoutCancel(ctx))
		defer cancel()
		if err := mall.FulfillPrizePlan(fctx, g.Order.OrderNo); err != nil {
			common.SysError("qianye/lottery: 出款 " + payoutNo + " 的套餐奖立即发放失败,交由商城对账补发: " + err.Error())
		}
	}
}

// actNoOf 读出出款所属活动的编号,冗余进流水的 act_no 列。
//
// 活动删除之后流水永不删,此后只有 act_no 还能把一场活动的流水归拢到一起
// (ref_no 里的出款号已经 JOIN 不回活动了)。读不到活动时留空串,不阻断出款:
// 一个归拢用的冗余字段不该让一笔已经确定要发的钱发不出去。
func actNoOf(tx *gorm.DB, actId int64) string {
	var act Activity
	if err := tx.Select("act_no").Where("id = ?", actId).Take(&act).Error; err != nil {
		return ""
	}
	return act.ActNo
}

// payoutLabel 是出款在账本流水 remark 与日志里的名字。
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

// payoutIdemKey 是一笔出款在账本上的幂等键。
//
// 出款号本身由 crypto/rand 生成、不可枚举;加 `lotpay:` 前缀是让账本上的键一眼
// 看得出来源 —— 读日志的人不必先去查这个键属于哪张表。
func payoutIdemKey(payoutNo string) string {
	return "lotpay:" + payoutNo
}

// markPayoutPaid 把出款推进终态,返回这一次是不是真的推动了它。
//
// 幂等锚点是 paying → paid 的 CAS。只认 paying:planned 是一笔从未执行过的出款,
// 把它直接推成已到账等于系统认为钱给过了而用户永远收不到;failed / held 要先经
// 认领(drivePayout 的 CAS)或管理端「重试」回到队列,再由 worker 逐笔驱动。
// 返回 false 表示别的路径已经收过尾,调用方**一步都不许再走** —— 那正是
// 重复加钱的形状。
func markPayoutPaid(tx *gorm.DB, payoutNo string) (bool, error) {
	res := tx.Model(&Payout{}).
		Where("payout_no = ? AND status = ?", payoutNo, PayoutPaying).
		Updates(map[string]any{
			"status":     PayoutPaid,
			"settled_at": common.GetTimestamp(),
			"last_error": "",
		})
	if res.Error != nil {
		return false, res.Error
	}
	return res.RowsAffected == 1, nil
}

// addPaidPayoutToActivityTotals 把一笔**在收尾之后**才成功的出款补计进活动合计。
//
// 活动的 payout_quota / refund_quota 在 settling→finished 那一次 CAS 里被写成
// "当时已 paid 的合计"。而 finishIfDone 刻意把 held 当作终态放行,held/failed
// 之后仍可能被管理端「重试」推成 paid —— 那些星屑是真的发出去了(账本上有
// 流水行),活动上的合计却再也不会跟上,且没有任何对账会复核它:runReconcile
// 只核名单与哈希链,auditFinishedChains 连这一列都不 Select。
//
// 后果不是"少显示一点":held_quota 是实时 SUM,补发成功后它归零,于是同一笔钱
// 从 payout_quota 与 held_quota 两个口子同时消失。管理端"本场收支"因此把一场
// 净亏的活动显示成净赚,连符号都是反的,而列表页还会把它累加进总支出。
//
// 幂等由调用方那次 paying→paid 的 CAS 保证:它在一行出款上最多成功一次
// (paid 是终态,没有任何路径把它推回去),所以这里的 += 最多执行一次。
// 条件里的 status = finished 让收尾**之前**就 paid 的那些笔不被重复计入 ——
// 它们已经在收尾的聚合里了。
func addPaidPayoutToActivityTotals(tx *gorm.DB, p *Payout) error {
	if p.AmountQuota == 0 {
		return nil
	}
	col := "payout_quota"
	if p.Kind == PayoutRefund {
		col = "refund_quota"
	}
	return tx.Model(&Activity{}).
		Where("id = ? AND status = ?", p.ActId, StatusFinished).
		UpdateColumns(map[string]any{
			col:          gorm.Expr(col+" + ?", p.AmountQuota),
			"updated_at": common.GetTimestamp(),
		}).Error
}

// failPayout 处理出款事务失败的两种结局。
//
//   - 预算未耗尽 → 退回 failed,指数退避后重试。事务整体回滚,账本上一个字节
//     都没留下,重试就是干净的重来。
//   - 预算耗尽 → 转 held 交人(holdPayout)。到账后余额会超出系统上界
//     (stardust.ErrOverflow)也走这条:那不是重试能解决的,用户得先把星屑花掉一些。
func failPayout(ctx context.Context, gdb *gorm.DB, p *Payout, cause error) {
	msg := audit.Truncate(cause.Error(), 512)
	if errors.Is(cause, stardust.ErrOverflow) {
		holdPayout(ctx, gdb, p, "到账后余额会超出系统上界,需用户先消耗一部分"+stardust.UnitName()+": "+msg)
		return
	}
	if p.Attempts >= config.Get().Lottery.PayoutMaxAttempts {
		holdPayout(ctx, gdb, p, "重试次数已耗尽: "+msg)
		return
	}

	// 指数退避,上界 300 秒。防止一条坏单反复打爆扩展库。
	delay := int64(1) << uint(minInt(p.Attempts, 8))
	if delay > 300 {
		delay = 300
	}
	if err := gdb.WithContext(ctx).Model(&Payout{}).
		Where("id = ? AND status = ?", p.Id, PayoutPaying).
		Updates(map[string]any{
			"status":          PayoutFailed,
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
// held 的唯一来源是重试耗尽(与到账即溢出),处置是管理端「重试」(RetryPayout)。
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
		"qianye/lottery: 出款 %s 已转人工(用户 %d,%d %s): %s",
		p.PayoutNo, p.UserId, p.AmountQuota, stardust.UnitName(), reason))
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
// 只把 held/failed 推回 planned 并清零退避与次数,**绝不新建 payout 行** ——
// 新建一行就是绕过 uk(act_id, entry_id, kind) 重复发钱。
//
// "推回 planned"就足以让钱发出去:失败的尝试整笔回滚、账本上没有残行,
// 下一轮 worker 认领之后是一次干净的重来。paid 不能重试:那是资金终态。
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

	res := gdb.WithContext(ctx).Model(&Payout{}).
		Where("payout_no = ? AND status = ?", payoutNo, p.Status).
		Updates(map[string]any{
			"status":          PayoutPlanned,
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

// raiseFlag 落一条对账异常,管理端红点直读。
//
// 同一个 (act_id, code) 未解决时不重复插:一条卡单每 10 秒被扫一次,
// 不去重会在几小时内把异常列表刷成同一条消息的几千份拷贝,
// 而那正好会淹没真正的新异常。
//
// 但**去重不等于丢弃**:detail 里写的是"重算 X 与物化 Y 不一致"这种当场算出来的
// 数字,篡改再次发生时它会变。只跳过不更新,运营看到的就是一个早已不成立的旧数字,
// 而这条 flag 又是本模块唯一的事后篡改出口(qy_lot_flag 没有别的写入方)。
// 所以命中已有未解决行时改为**刷新 detail**,CreatedAt 保持首次检出时刻不动。
// raiseFlag 返回这条异常是不是**新的**(新建,或 detail 变了)。
//
// 调用方据此决定要不要再打一条日志/审计:后台扫描每 15 秒重跑一次同一场活动,
// 不带这个判据的调用点会以每分钟 4 条的速度往 qy_audit_logs 与日志文件里追加
// 逐字相同的行,一场卡住的活动一天约 6000 行,而挂起态没有 SLA(等人),
// 可以持续数天。本文件里的去重注释写明"不去重会在几小时内把异常列表刷成同一条
// 消息的几千份拷贝,而那正好会淹没真正的新异常" —— 这条纪律必须能被复用,
// 否则同一个函数里的另外两句照样刷屏(suspendReveal 就是这么发生的)。
func raiseFlag(ctx context.Context, actId int64, code, detail string) bool {
	gdb := db.Get()
	if gdb == nil {
		return false
	}
	// 句柄一次性绑上租约的预算:逐条 WithContext 漏一条,就等于在这条链路上开了一个
	// 没有上界的口子 —— 语句级预算只对 WithContext 的语句生效。
	changed, err := upsertFlag(gdb.WithContext(ctx), actId, code, detail)
	if err != nil {
		db.MarkFailure(err)
		return false
	}
	return changed
}

// upsertFlag 是 raiseFlag 的落库语义:同一个未解决的 (act_id, code) 只留一行,
// 但 detail 每次刷新,首次检出时刻保持不动。
//
// resolved=true 的历史行**不参与**去重 —— 这正是"处理完之后同类异常还能再报"的
// 前提,也是为什么必须有一个把 resolved 置 true 的产品入口(handleAdminResolveFlag)。
func upsertFlag(gdb *gorm.DB, actId int64, code, detail string) (bool, error) {
	detail = audit.Truncate(detail, 512)
	var existing Flag
	err := gdb.Model(&Flag{}).
		Where("act_id = ? AND code = ? AND resolved = ?", actId, code, false).
		Take(&existing).Error
	switch {
	case err == nil:
		if existing.Detail == detail {
			return false, nil
		}
		return true, gdb.Model(&Flag{}).Where("id = ?", existing.Id).
			Update("detail", detail).Error
	case !errors.Is(err, gorm.ErrRecordNotFound):
		return false, err
	}
	return true, gdb.Create(&Flag{
		ActId:     actId,
		Code:      code,
		Detail:    detail,
		CreatedAt: common.GetTimestamp(),
	}).Error
}

func minInt(a, b int) int {
	if a < b {
		return a
	}
	return b
}
