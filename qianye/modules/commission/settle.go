package commission

import (
	"context"
	"errors"
	"fmt"

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
	// Net 正数为发放,负数为回收。
	Net int64
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
		// 只从尚未入账的可用余额里回收。佣金一旦自动入账进主库余额可能已被消费,
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
		Net:        net,
		CarryAfter: total.Sub(decimal.NewFromInt(net)),
		Clipped:    clipped,
		Clamp:      clamp,
	}
}

// pendingInviters 选出一页需要结算的邀请人,来源有两路。
//
// 第二路(只剩余数、没有新增计佣行)不能省:被日封顶或结算门槛削掉的部分
// 全额留在 unsettled_amount 里,而 absorbAccruals 会把本批**全部** accrual
// 的 settled_amount 写成 gross_amount。只按第一路选人的话,那笔 carry 就只能
// 等这个邀请人名下再产生新的计佣行才有机会发出 —— 下线一旦停止消费,
// 这笔钱永远拿不到。computeSettlement 的算术一直是对的,断链在调度层。
//
// # 排序口径必须是"等得最久的先发",不能是 id 升序
//
// 两路来源都带 LIMIT settleInviterBatch(500),而排序键决定了超出批量之后
// 谁被留下。旧代码两路都写 ORDER BY <id> ASC:活跃邀请人一旦超过 500,
// 每一轮取到的永远是 id 最小的那 500 个,而他们的下线还在消费,所以下一轮
// 他们照样命中 —— id 更大的邀请人**永远**排不进来,佣金无限期停在 accrual
// 里发不出去。钱不会丢(accrual 一直在),但用户永远拿不到,且没有任何告警
// 会响:队列没满、没有降级、三条恒等式全部成立。
//
// 现在两路各按各自的"等待时长"排:
//
//   - 第一路按 MIN(mature_at):名下最老的一笔未吸收计佣先发。结算会把这批
//     accrual 的 settled_amount 写成 gross_amount,于是他整个人退出候选集;
//     等他再次产生计佣行时,那一行的 mature_at 是新的,自然排到队尾。
//   - 第二路按 last_settled_at:距上次结算最久的先发。结算会把这一列写成
//     now,同样退到队尾。
//
// 两路都是"发过就退到队尾"的 FIFO,因此每个邀请人的等待轮次有上界
// (≈ 2 × 候选人数 / 批量),不再存在永远排不进来的人。
//
// # 排空一整天的队列靠的是键集游标,不是"再查一次"
//
// 一日一结算必须在一次跑里排空整个队列(见 settle_daily.go)。光靠反复调用
// 本函数是排不空的:两路来源都是 ORDER BY … LIMIT,而"这一轮没发出去的人"
// 会原样停在队首 —— 被日封顶削成 net=0 的 carry-only 邀请人不落结算单、
// 不刷新 last_settled_at;报错的那些人两路 WHERE 都还命中。他们只要多到
// 填满一个批量,后面的人这一整天一次都轮不到,而循环会一直取到同一批。
//
// 所以本函数收一个键集游标:每一页从上一页的末键之后继续取,不依赖"上一页
// 的人已经从候选集里消失"。游标键就是各自的排序键,与 ORDER BY 严格一致:
//
//	第一路 (MIN(mature_at), inviter_id)   —— HAVING 里比较聚合值,三库通用
//	第二路 (last_settled_at, user_id)
//
// 这样任何一页都严格前进,排空的轮次上界等于候选人数 / 批量,与失败与否无关。
type inviterCursor struct {
	// HasA/HasB 区分"游标为零"与"游标真的停在 0 上"。mature_at 与
	// last_settled_at 都可能是 0(存量行、从没结算过的余额行),用零值当
	// "没有游标"会让第一页把这些人整批跳过 —— 而他们恰恰是等得最久的那批。
	HasA      bool
	MatureAt  int64
	InviterId int

	HasB      bool
	SettledAt int64
	UserId    int
}

// inviterHead 是一行候选人及其排序键。
type inviterHead struct {
	Id    int
	OrdAt int64
}

// pendingInvitersPage 取一页候选人。more 为 false 表示两路都已取空。
//
// 返回的 next 必须原样传回下一次调用,否则就退化成"永远取第一页"。
func pendingInvitersPage(limit int, cur inviterCursor) (ids []int, next inviterCursor, more bool, err error) {
	gdb := db.Get()
	if gdb == nil {
		return nil, cur, false, db.ErrNotReady
	}
	now := common.GetTimestamp()

	// GROUP BY + HAVING 聚合函数是三种数据库都支持的标准写法;
	// inviter_id 作为次级键只为让同一时刻成熟的行有个确定顺序。
	// 列别名不能叫 key/order —— 两者在 MySQL 都是保留字。
	var acc []inviterHead
	q := `SELECT inviter_id AS id, MIN(mature_at) AS ord_at FROM qy_commission_accrual
		WHERE status = ? AND mature_at <= ? AND settled_amount <> gross_amount
		GROUP BY inviter_id `
	args := []any{StatusAccrued, now}
	if cur.HasA {
		q += `HAVING MIN(mature_at) > ? OR (MIN(mature_at) = ? AND inviter_id > ?) `
		args = append(args, cur.MatureAt, cur.MatureAt, cur.InviterId)
	}
	q += `ORDER BY MIN(mature_at) ASC, inviter_id ASC LIMIT ?`
	args = append(args, limit)
	if err := gdb.Raw(q, args...).Scan(&acc).Error; err != nil {
		db.MarkFailure(err)
		return nil, cur, false, err
	}

	// 门槛取 minSettle 而不是 1:net 要 >= minSettle 才发得出去(见
	// computeSettlement),按 >= 1 选人会让每个零头在 1..minSettle 之间的
	// 邀请人每个周期都白跑一次加锁事务,而且永远发不出来。
	// 第二路两种候选:
	//   · 正余数达到 minSettle 的(等着被发出去);
	//   · **负余数且账上还有可回收余额的**(欠账,等着被收回来)。
	// 少了后半句,一个被冲正打成 debt_blocked 的推广人永远不会再被调度选中:
	// 第一路要求 settled_amount <> gross_amount,而 absorbAccruals 已经把冲正行
	// 写成 settled==gross,于是他从两路里同时消失。表现是自动入账被永久暂停而
	// 没有任何告警会响 —— 三条恒等式在这个状态下全部成立。
	// 负余数这一支的判据是 `< 0`,**不是** `<= -1`。
	//
	// `<= -1` 把 (-1, 0) 区间的欠账整段漏在调度之外,而那个区间不是理论边角:
	// computeSettlement 的 reclaim 被 available 钳住时,carry 恰好等于
	// -(欠账 - available),欠账超出可回收余额的部分只要小于 1 个额度单位就落
	// 在里面;而自动冲正额 = calcGross(refundQuota, rateUnits) 天然带小数。
	// 随后任何一条把额度退回 available 的动作(自动入账失败退回)都会构成
	// 「欠账 + available>0」。
	//
	// 落进这个区间的人从**两路里同时消失**:第一路要求 settled_amount <>
	// gross_amount,而 absorbAccruals 已经把冲正行写成 settled==gross。
	// 于是 debt_blocked 永久为真、自动入账永久暂停 —— 用户拿不到账上确实
	// 存在的可用额度,平台也永远收不回那笔应收,而 I1/I2 两条恒等式在这个
	// 状态下**全部成立**,余额页、总表、健康面板一致显示正常。
	//
	// 判据与 settleUser 的 carry-only 预筛(peek.UnsettledAmount.IsNegative())
	// 就此同口径 —— 那两半原本自相矛盾:预筛受理任何负数,SQL 只捞 <= -1。
	var carry []inviterHead
	q = `SELECT user_id AS id, last_settled_at AS ord_at FROM qy_commission_balance
		WHERE (unsettled_amount >= ? OR (unsettled_amount < 0 AND available > 0)) `
	args = []any{carryFloor(effective().MinSettleStardust)}
	if cur.HasB {
		q += `AND (last_settled_at > ? OR (last_settled_at = ? AND user_id > ?)) `
		args = append(args, cur.SettledAt, cur.SettledAt, cur.UserId)
	}
	q += `ORDER BY last_settled_at ASC, user_id ASC LIMIT ?`
	args = append(args, limit)
	if err := gdb.Raw(q, args...).Scan(&carry).Error; err != nil {
		db.MarkFailure(err)
		return nil, cur, false, err
	}

	ids, takenA, takenB := mergeInviterIds(headIds(acc), headIds(carry), limit)

	// 游标只推进到**这一页真的看过**的最后一行。看过多少由 mergeInviterIds
	// 说了算:批量被另一路占满时,本路多读出来的那几行没被看过,必须留给下一页。
	next = cur
	if takenA > 0 {
		next.HasA = true
		next.MatureAt, next.InviterId = acc[takenA-1].OrdAt, acc[takenA-1].Id
	}
	if takenB > 0 {
		next.HasB = true
		next.SettledAt, next.UserId = carry[takenB-1].OrdAt, carry[takenB-1].Id
	}
	return ids, next, len(acc) > 0 || len(carry) > 0, nil
}

func headIds(rows []inviterHead) []int {
	out := make([]int, 0, len(rows))
	for _, r := range rows {
		out = append(out, r.Id)
	}
	return out
}

// pendingInviters 取第一页候选人。管理端健康面板用它估算积压深度。
func pendingInviters(limit int) ([]int, error) {
	ids, _, _, err := pendingInvitersPage(limit, inviterCursor{})
	return ids, err
}

// carryFloor 是"值得为它跑一轮 carry-only 结算"的余数下界。
func carryFloor(minSettle int64) int64 {
	if minSettle < 1 {
		return 1
	}
	return minSettle
}

// mergeInviterIds 合并两路待结算来源:去重、轮流取、截断到 limit。
//
// 两路各自已经按"等得最久的在前"排好(见 pendingInviters),所以这里**不能**
// 再按 id 排序 —— 那会把两条 FIFO 队列的优先级信息整个丢掉,退化回
// "id 小的永远优先",也就是这次要修的那个饥饿。
//
// 轮流取而不是先 a 后 b:第一路在活跃站点上长期塞满整个批量,顺序拼接
// 会让第二路(只剩余数、下线已经停止消费的人)一个名额都拿不到。轮流取
// 保证任何一路在另一路非空时至少拿到 limit/2 个名额,两边的等待轮次都有上界。
// 一路取空之后剩余名额全部让给另一路,不浪费批量。
//
// takenA / takenB 是各路"已经看过"的元素个数,排空循环据此推进键集游标。
// 必须是"看过"而不是"取用":被去重丢掉的那一个同样不该再看第二遍。
// 反过来,**截断之后没看过的那些绝不能算进去** —— 旧写法是先全量合并、
// 最后 merged[:limit],那样两路各有半个批量的人被读出来又被丢掉,游标却
// 已经越过了他们,于是这批人这一整天一次都轮不到,而且没有任何信号会响。
func mergeInviterIds(a, b []int, limit int) (merged []int, takenA, takenB int) {
	seen := make(map[int]struct{}, len(a)+len(b))
	merged = make([]int, 0, len(a)+len(b))
	take := func(id int) {
		if id <= 0 {
			return
		}
		if _, dup := seen[id]; dup {
			return
		}
		seen[id] = struct{}{}
		merged = append(merged, id)
	}
	full := func() bool { return limit > 0 && len(merged) >= limit }
	for i := 0; i < len(a) || i < len(b); i++ {
		if i < len(a) {
			if full() {
				return merged, takenA, takenB
			}
			takenA = i + 1
			take(a[i])
		}
		if i < len(b) {
			if full() {
				return merged, takenA, takenB
			}
			takenB = i + 1
			take(b[i])
		}
	}
	return merged, takenA, takenB
}

// settleNeeded 判断本轮是否真的要落一张结算单。
//
// 判据只有"这一轮有没有钱动过":net != 0 才落单。
//
// 曾经的判据是 accrualCount > 0 || net != 0,理由是"结算单是 absorbAccruals
// 回写 settlement_id 的载体"。那个理由站不住:settlement_id 全仓没有任何读取方
// (只在 absorbAccruals 里被写),而"这批计佣已经被吸收"这个事实由
// settled_amount == gross_amount 承载,与结算单无关。真正的代价是行数 ——
// 结算周期默认 300 秒 = 每天 288 轮,每个还有零头在滚的邀请人每轮落一张
// 全零单,行数按"邀请人数 × 结算周期"膨胀,而这张单里没有一个字段是
// 别处推不出来的:delta 等于本批 accrual 的 settled_amount 增量,
// carry_before / carry_after 等于 balance.unsettled_amount 的前后值。
//
// 不落单**不等于**不干活:len(rows) > 0 时 absorbAccruals 与余额回写照常执行,
// 这批计佣行仍然会被吸收、余数仍然会往前滚,下一轮 pendingInviters 不会
// 再把它们捞出来重算。恒等式 Σsettled_amount − Σ(granted−reclaimed) = 余数
// 两边同时加 delta,照样成立(见 TestSettleUserSkipsZeroValueSettlement)。
//
// clamped 是唯一的例外:单轮结算触顶 int32 时,Remark 里那行触顶说明只有
// 结算单装得下,哪怕本轮恰好被日封顶削成 0 也必须留痕。
func settleNeeded(net int64, clamped bool) bool {
	return net != 0 || clamped
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
	res := gdb.WithContext(ctx).Model(&Accrual{}).
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
// 结算只把成熟的计佣变成**可用余额**;真正加进主库额度(星辉)的是 autocredit.go
// 的两阶段资金单,那是另一步、另一把锁序里的另一件事。
// 返回值 more 表示"这一次只吸收到了取批上界,同一个人名下还有已成熟的计佣行"。
// 调用方必须继续调,否则剩下的行要等下一次运行才发得出去 —— 一日一结算之下
// 那就是整整一天(见 settleUserDrain)。
func settleUser(inviterId int) (more bool, err error) {
	gdb := db.Get()
	if gdb == nil {
		return false, db.ErrNotReady
	}
	now := common.GetTimestamp()
	var granted, reclaimed int64
	var clampNote string
	var moreRows bool

	err = gdb.Transaction(func(tx *gorm.DB) error {
		var rows []Accrual
		err := tx.Where("inviter_id = ? AND status = ? AND mature_at <= ? AND settled_amount <> gross_amount",
			inviterId, StatusAccrued, now).
			Order("id asc").Limit(settleAccrualBatch).Find(&rows).Error
		if err != nil {
			return err
		}
		// 取到满批就说明这个人名下还有没读完的已成熟计佣行。
		moreRows = len(rows) >= settleAccrualBatch
		if len(rows) == 0 {
			// carry-only 结算:没有新的计佣增量,但上一轮被日封顶 / 结算门槛
			// 削掉的余数还留在 unsettled_amount 里。这里直接早退,那笔钱就只能
			// 等新的计佣行出现才有机会发出 —— 必须继续往下走一遍。
			//
			// 先做一次不加锁的预筛:余数不足 1 额度时连行锁都不必取,
			// 也避免管理端对任意 user_id 调 settleOne 时凭空建出余额行。
			var peek Balance
			err := tx.Where("user_id = ?", inviterId).Take(&peek).Error
			if errors.Is(err, gorm.ErrRecordNotFound) {
				return nil
			}
			if err != nil {
				return err
			}
			// 负结转是**欠账**,它的钱要从 available 里回收,而不是等
			// 未来的新计佣行。
			//
			// 这一支原先与正余数共用 `LessThan(1)`,而**所有负数都满足它** ——
			// 于是 debt_blocked 一旦置上,哪怕账上可用余额远超欠账,结算跑多少次
			// 都清不掉:自动入账被永久暂停,用户拿不到自己账上确实存在的钱,
			// 平台也永远收不回那笔应收。
			if peek.UnsettledAmount.IsNegative() {
				if peek.Available <= 0 {
					// 账上一分可回收的都没有,这一轮确实做不了事。
					return nil
				}
			} else if peek.UnsettledAmount.LessThan(decimal.NewFromInt(1)) {
				return nil
			}
		}

		delta := decimal.Zero
		for _, r := range rows {
			delta = delta.Add(r.GrossAmount.Sub(r.SettledAmount))
		}

		bal, err := lockBalance(tx, inviterId)
		if err != nil {
			return err
		}
		s := effective()
		win, err := resolveCapWindow(tx, bal, s.DailyCapStardust, now)
		if err != nil {
			return err
		}
		out := computeSettlement(bal.UnsettledAmount, delta, bal.Available,
			s.MinSettleStardust, win.remaining(s.DailyCapStardust))
		if out.Clamp != nil {
			// 单轮结算触顶 int32 是绝不该发生的事,必须留痕并告警;
			// 未发完的部分仍在 CarryAfter 里,下轮继续。
			clampNote = out.Clamp.Error()
			warnf("邀请人 %d 结算金额触顶: %s", inviterId, clampNote)
		}
		needRow := settleNeeded(out.Net, out.Clamp != nil)
		if !needRow && len(rows) == 0 {
			// 本轮什么都没发生:没有计佣行要吸收,余数也一分没动
			// (delta 与 net 同为零 ⇒ CarryAfter 恒等于 CarryBefore)。
			// 连 updated_at 都不该写,否则空转的每一轮都在给余额表制造写入。
			return nil
		}

		// settlementId 为 0 表示"本轮没有落结算单",此时这批计佣行照常被吸收:
		// 钱进了余数,吸收与否决定的只是下一轮会不会把它们再读一遍。
		var settlementId int64
		if needRow {
			settlement := Settlement{
				SettleNo:     newSerialNo("CS"),
				UserId:       inviterId,
				AccrualCount: len(rows),
				DeltaAmount:  delta,
				CarryBefore:  bal.UnsettledAmount,
				CarryAfter:   out.CarryAfter,
				Remark:       truncate(clampNote, 255),
				CreatedAt:    now,
			}
			if out.Net >= 0 {
				settlement.Granted = out.Net
			} else {
				settlement.Reclaimed = -out.Net
			}
			if err := tx.Create(&settlement).Error; err != nil {
				return err
			}
			settlementId = settlement.Id
			granted, reclaimed = settlement.Granted, settlement.Reclaimed
		}

		if err := absorbAccruals(tx, rows, settlementId, now); err != nil {
			return err
		}

		// 日封顶窗口与余额在同一次写里推进。只累加发放:回收(负 net)不退还
		// 当天的封顶额度 —— 封顶限的是"一天最多发出去多少",不是净额,
		// 否则一次冲正就能换回一份新的发放余量。
		capGrantedAfter := win.Granted
		if out.Net > 0 {
			capGrantedAfter += out.Net
		}
		updates := map[string]any{
			"daily_cap_window_start": win.Start,
			"daily_cap_granted":      capGrantedAfter,
			"unsettled_amount":       out.CarryAfter,
			"debt_blocked":           out.CarryAfter.IsNegative(),
			"last_settled_at":        now,
			"updated_at":             now,
		}
		switch {
		case out.Net > 0:
			updates["available"] = bal.Available + out.Net
			updates["total_earned"] = bal.TotalEarned + out.Net
		case out.Net < 0:
			updates["available"] = bal.Available + out.Net
			updates["total_clawback"] = bal.TotalClawback - out.Net
		}
		if err := tx.Model(&Balance{}).Where("user_id = ?", inviterId).Updates(updates).Error; err != nil {
			return err
		}

		if out.CarryAfter.IsNegative() {
			warnf("邀请人 %d 产生欠账 %s,自动入账已暂停,后续佣金将优先抵扣",
				inviterId, out.CarryAfter.String())
		}
		return nil
	})
	if err != nil {
		db.MarkFailure(err)
		return false, err
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
	return moreRows, nil
}

// settleUserMaxPasses 是单个邀请人在一次运行里最多取几批计佣行。
//
// 200 × settleAccrualBatch(1000) = 20 万行/人/次,远超任何真实规模。它和
// settleDrainMaxRounds 是同一个作用:防的不是业务量,而是"CAS 反复失败导致
// 永远取到同一批"这种死循环占着租约。
const settleUserMaxPasses = 200

// settleUserDrain 把一个邀请人**当次**已成熟的计佣行全部吸收完。
//
// settleUser 单次最多取 settleAccrualBatch(1000)行。旧的 300 秒调度下这个
// 上界无害:同一个人一天还有 287 次机会。改成一日一结算之后它变成**日级**
// 上界 —— 实测给一个邀请人插 1400 行已成熟计佣,一次日结只发出 1000 行,
// 剩下 400 行原样留到明天,而这一跑照常报 status=done / failed=0 / remark 空,
// 面板上"今天跑完了"是假的。积压每天只消化 1000 行,日新增超过 1000 就永久发散。
//
// 返回 drained=false 表示撞上了 settleUserMaxPasses:这一天必须标成 partial
// 并重试,绝不能报 done。
func settleUserDrain(inviterId int) (drained bool, err error) {
	for pass := 0; pass < settleUserMaxPasses; pass++ {
		more, err := settleUser(inviterId)
		if err != nil {
			return false, err
		}
		if !more {
			return true, nil
		}
	}
	return false, nil
}

// lockBalance 取得余额行的排他锁,不存在则先建。
//
// 这是结算、冲正、手工调整与自动入账共同约定的唯一加锁点:各方都必须先锁住
// 这一行,否则"结算加可用"和"入账搬走可用"会互相覆盖。
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

// capWindow 是日封顶的当前窗口:起点与窗口内已发出的额度。
//
// 它随余额行一起持久化(Balance.DailyCapWindowStart / DailyCapGranted),
// 而不是每次拿当前日界去现算 —— 理由见 Balance 上那两列的说明。
type capWindow struct {
	Start   int64
	Granted int64
}

// remaining 返回本窗口还能发多少。cap <= 0(不设封顶)时返回 -1。
func (w capWindow) remaining(cap int64) int64 {
	if cap <= 0 {
		return -1
	}
	remain := cap - w.Granted
	if remain < 0 {
		remain = 0
	}
	return remain
}

// resolveCapWindow 判定这一次结算落在哪个日封顶窗口里。
//
// 调用方必须已经持有该用户的余额行锁(lockBalance),bal 就是锁内读到的那一行。
//
// 三条分支:
//
//	窗口起点为 0    本列上线前的存量行。按旧口径(结算行 created_at)补一次
//	               「今天已发」,否则升级当天封顶白白多一份。
//	已满 24 小时    开新窗口,起点取当前日界(dayStart),已发清零。
//	其余           沿用余额行上记着的窗口。**日界怎么挪都进不了这一支**,
//	               这正是「改一次配置就把封顶重新加满」被堵死的地方。
//
// 「已满 24 小时」而不是「日界变了」是这条修复的全部要点:稳定偏移下两者
// 完全等价(相邻日界正好相差 86400),而偏移被改动时前者不会凭空多出一个窗口。
func resolveCapWindow(tx *gorm.DB, bal *Balance, cap int64, now int64) (capWindow, error) {
	switch {
	case bal.DailyCapWindowStart == 0:
		w := capWindow{Start: dayStart(now)}
		if cap <= 0 {
			// 没开封顶就不必为存量行多查一次:开启封顶那一刻会走到这里补。
			return w, nil
		}
		var used int64
		if err := tx.Model(&Settlement{}).
			Where("user_id = ? AND created_at >= ?", bal.UserId, w.Start).
			Select("COALESCE(SUM(granted), 0)").Scan(&used).Error; err != nil {
			return capWindow{}, err
		}
		w.Granted = used
		return w, nil
	case now >= bal.DailyCapWindowStart+secondsPerDay:
		return capWindow{Start: dayStart(now)}, nil
	default:
		return capWindow{Start: bal.DailyCapWindowStart, Granted: bal.DailyCapGranted}, nil
	}
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

// settleOne 供管理端"立即结算"接口复用。
func settleOne(userId int) error {
	if userId <= 0 {
		return errors.New("commission: 用户 id 非法")
	}
	drained, err := settleUserDrain(userId)
	if err != nil {
		return err
	}
	if !drained {
		return fmt.Errorf("commission: 用户 %d 的已成熟计佣行超过 %d 行,本次只结算了一部分,请再执行一次",
			userId, settleUserMaxPasses*settleAccrualBatch)
	}
	return nil
}
