package commission

// autocredit.go —— 佣金可用余额**自动入账**成星屑。D-16 的核心。
//
// # 形状
//
// 一日一结算把成熟的计佣变成 qy_commission_balance.available(星屑);本文件的后台
// 任务(lease `commission.credit`)每个周期扫一遍 available >= min_credit_stardust
// 且没有欠账的余额行,在**一个扩展库事务**里把钱搬过去:
//
//	锁佣金余额行 → 锁内复核门槛 / 欠账 → stardust.Credit(kind=commission_credit)
//	→ 写 qy_commission_credit 一行(done)→ available −= n、credited += n
//
// 没有用户申请、没有审核、没有法币,也不碰主库额度。
//
// # 为什么这里不再有两阶段、冻结列与人工裁决
//
// D-15 的佣金记的是「星辉」—— 主库 users.quota 的展示名 —— 所以入账是一次**跨库**
// 转账:扩展库先把钱冻住,主库加额度,再回扩展库销账。三个库事务、一张资金单、
// 一个探针、一条人工裁决通道、一个对账循环,以及 pending / failed / held 三个中间态,
// 全部是为了回答同一个问题:"主库到底动没动"。
//
// D-16 佣金改记星屑之后,佣金账本(qy_commission_*)与星屑账本(qy_sd_*)在**同一个
// 库**里。一次入账就是一个本地事务:要么佣金余额和星屑流水一起落,要么一起回滚。
// 那个问题不再存在,于是回答它的那一整层跟着消失 —— 连同 qy_commission_freeze 表、
// balance.frozen_quota 列、KindCommissionCredit 资金单与本文件此前的 reconcile 循环。
//
// 幂等:星屑那一侧以 (idem_scope=commission_credit, idem_key=credit_no) 唯一索引兜住。
// credit_no 在事务内生成,事务回滚它就不存在,所以重试永远是"新的一笔",不会重复发钱。

import (
	"context"
	"errors"
	"fmt"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/qianye/config"
	"github.com/QuantumNous/new-api/qianye/db"
	qymodel "github.com/QuantumNous/new-api/qianye/model"
	"github.com/QuantumNous/new-api/qianye/modules/stardust"
	"github.com/QuantumNous/new-api/qianye/service/audit"

	"gorm.io/gorm"
)

const (
	creditIdemScope = "commission_credit"
	refTypeCredit   = "commission_credit"
	// creditBatch 是单个周期最多处理的余额行数。
	creditBatch = 200
)

var (
	errCreditBelowMin   = errors.New("commission: 可用余额未达自动入账门槛")
	errCreditDebt       = errors.New("commission: 存在欠账,暂停自动入账")
	errCreditQuotaMoved = errors.New("commission: 可用余额在入账前被并发改动")
)

// runCredit 是自动入账后台任务的入口,由 lease.Run 按 credit_interval_seconds 驱动。
func runCredit(ctx context.Context) {
	if ctx.Err() != nil || !config.Get().Commission.Enabled {
		return
	}
	s := effective()
	ids, err := creditCandidates(ctx, s.MinCreditStardust, creditBatch)
	if err != nil {
		warnf("选取待入账的余额行失败: %v", err)
		return
	}
	if len(ids) == 0 {
		return
	}
	var st creditBatchStats
	for _, id := range ids {
		if ctx.Err() != nil {
			break
		}
		st.add(creditUserDrain(ctx, id, s))
	}
	st.audit()
}

// creditBatchStats 是一个周期的入账结局汇总,给"每批一条"的系统审计与日志用。
type creditBatchStats struct {
	Done, Skipped int
	Amount        int64
}

func (st *creditBatchStats) add(o creditOutcome) {
	st.Done += o.Done
	if o.Skipped {
		st.Skipped++
	}
	st.Amount += o.Amount
}

// audit 落一条系统审计。发了钱才写:一个空转的周期不该在审计表里留一行。
//
// AmountQuota 是审计表的通用金额列,这里装的是本批入账的**星屑**总数 ——
// 审计表不区分币种,读的时候按 category=commission 的口径解释。
func (st creditBatchStats) audit() {
	if st.Done == 0 {
		return
	}
	audit.Write(nil, audit.Entry{
		Category:    qymodel.AuditCategoryCommission,
		Action:      "commission.credit.batch",
		ActorType:   qymodel.ActorSystem,
		AmountQuota: st.Amount,
		Result:      qymodel.ResultOK,
		Reason: fmt.Sprintf("自动入账一批:%d 笔到账,合计 %d 星屑,门槛未达跳过 %d 人",
			st.Done, st.Amount, st.Skipped),
	})
}

// creditCandidates 选出一页达到门槛的余额行。
//
// 排除条件与事务里锁内复核的完全一致(门槛 / 欠账 / 负余数),这里只是不加锁的
// 预筛,免得对每一行都取锁。按 user_id 升序:一个周期最多 creditBatch 行,超出的
// 下一个周期继续 —— 入账不是"当天必须发完"的事,慢一个周期没有资金风险。
func creditCandidates(ctx context.Context, minCredit int64, limit int) ([]int, error) {
	gdb := db.Get()
	if gdb == nil {
		return nil, db.ErrNotReady
	}
	ids := make([]int, 0, limit)
	err := gdb.WithContext(ctx).Model(&Balance{}).
		Where("available >= ? AND debt_blocked = ? AND unsettled_amount >= 0", minCredit, false).
		Order("user_id asc").Limit(limit).Pluck("user_id", &ids).Error
	if err != nil {
		db.MarkFailure(err)
		return nil, err
	}
	return ids, nil
}

// creditOutcome 是一个用户这一轮的入账结局。
type creditOutcome struct {
	Done    int
	Skipped bool
	Amount  int64
}

// creditUserMaxRounds 是同一个人在一个周期里最多入账几笔。
//
// 它防的不是业务规模,而是"某个人的余额远超 max_per_order_stardust 时把整个周期占满"。
// 剩下的下一个周期接着发。
const creditUserMaxRounds = 20

// creditUserDrain 给一个人按 max_per_order_stardust 分批入账,直到余额低于门槛或
// 撞上本周期上限。
func creditUserDrain(ctx context.Context, userId int, s opSettings) creditOutcome {
	var out creditOutcome
	for i := 0; i < creditUserMaxRounds; i++ {
		if ctx.Err() != nil {
			return out
		}
		amount, err := creditOnce(ctx, userId, s)
		switch {
		case errors.Is(err, errCreditBelowMin), errors.Is(err, errCreditDebt), errors.Is(err, errCreditQuotaMoved):
			if out.Done == 0 {
				out.Skipped = true
			}
			return out
		case errors.Is(err, stardust.ErrOverflow):
			// 星屑余额触顶。这不是本模块能修的事,钱原样留在可用余额里等运营处置 ——
			// 绝不能因为"发不进去"就把它从佣金账本里抹掉。
			warnf("用户 %d 的星屑余额已触顶,本笔佣金留在可用余额里未发放", userId)
			return out
		case err != nil:
			// 事务整体回滚,什么都没发生。记日志,下个周期再来。
			warnf("用户 %d 自动入账失败(本周期跳过): %v", userId, err)
			return out
		}
		out.Done++
		out.Amount += amount
	}
	return out
}

// creditOnce 在一个扩展库事务里把一笔佣金余额发成星屑,返回实发星屑数。
//
// 顺序不变量:**佣金余额行锁必须是本事务里的第一条语句**,与结算 / 冲正 / 手工调整
// 共用同一把锁(lockBalance);星屑余额行锁在其后(stardust.Credit 内部)。这个先后
// 在全模块唯一,不会与任何一条别的路径构成反向持锁。
func creditOnce(ctx context.Context, userId int, s opSettings) (int64, error) {
	gdb := db.Get()
	if gdb == nil {
		return 0, db.ErrNotReady
	}
	var granted int64
	err := gdb.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		bal, err := lockBalance(tx, userId)
		if err != nil {
			return err
		}
		amount, err := creditAmountFor(bal, s)
		if err != nil {
			return err
		}

		now := common.GetTimestamp()
		credit := &Credit{
			CreditNo:   newSerialNo("CC"),
			UserId:     userId,
			Amount:     amount,
			Status:     CreditStatusDone,
			CreatedAt:  now,
			FinishedAt: now,
		}
		// 星屑那一侧先写:它是唯一会因为业务原因(余额触顶)整笔拒绝的一步,
		// 让它在佣金账本被改动之前失败,回滚的是一个几乎空的事务。
		res, err := stardust.Credit(tx, stardust.Posting{
			UserId:    userId,
			Kind:      stardust.KindCommissionCredit,
			Amount:    amount,
			IdemScope: creditIdemScope,
			IdemKey:   credit.CreditNo,
			RefType:   refTypeCredit,
			RefNo:     credit.CreditNo,
			Remark:    "推广佣金自动入账",
		})
		if err != nil {
			return err
		}
		credit.LedgerNo = res.LedgerNo
		if err := tx.Create(credit).Error; err != nil {
			return err
		}

		// available → credited 用条件 UPDATE 而不是读改写:锁已经拿在手里,
		// 条件只是最后一道断言 —— 它一旦不成立说明有人绕过 lockBalance 改了账。
		upd := tx.Model(&Balance{}).
			Where("user_id = ? AND available >= ?", userId, amount).
			Updates(map[string]any{
				"available":        gorm.Expr("available - ?", amount),
				"credited":         gorm.Expr("credited + ?", amount),
				"last_credited_at": now,
				"updated_at":       now,
			})
		if upd.Error != nil {
			return upd.Error
		}
		if upd.RowsAffected != 1 {
			return errCreditQuotaMoved
		}
		granted = amount
		return nil
	})
	if err != nil {
		return 0, err
	}
	creditDone.Add(1)
	return granted, nil
}

// creditAmountFor 决定这一笔发多少:min(available, max_per_order_stardust),且不低于门槛。
func creditAmountFor(bal *Balance, s opSettings) (int64, error) {
	if bal.DebtBlocked || bal.UnsettledAmount.IsNegative() {
		return 0, errCreditDebt
	}
	if bal.Available < s.MinCreditStardust || bal.Available <= 0 {
		return 0, errCreditBelowMin
	}
	amount := bal.Available
	if s.MaxPerOrderStardust > 0 && amount > s.MaxPerOrderStardust {
		amount = s.MaxPerOrderStardust
	}
	if amount > int64(common.MaxQuota) {
		amount = int64(common.MaxQuota)
	}
	if amount < s.MinCreditStardust {
		// max_per_order_stardust 配得比门槛还小:分批的每一笔都够不到门槛。
		// 这是配置矛盾,不是资金问题 —— 按门槛发,让 max_per_order 的上限让位,
		// 但绝不超过实际可用余额。
		amount = s.MinCreditStardust
	}
	if amount > bal.Available {
		amount = bal.Available
	}
	return amount, nil
}

// currentBalanceRow 读一行余额;不存在时返回零值行(不建行)。
func currentBalanceRow(ctx context.Context, userId int) (Balance, error) {
	gdb := db.Get()
	if gdb == nil {
		return Balance{}, db.ErrNotReady
	}
	var bal Balance
	err := gdb.WithContext(ctx).Where("user_id = ?", userId).Take(&bal).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return Balance{UserId: userId}, nil
	}
	if err != nil {
		db.MarkFailure(err)
		return Balance{}, err
	}
	return bal, nil
}

// nextCreditAt 是"下一次自动入账最早什么时候跑",给用户端界面回答"我的钱什么时候到"。
// 任务按固定周期跑,这里给的是从现在起的下一个周期边界,不承诺精确到秒。
func nextCreditAt(now int64) int64 {
	interval := int64(config.Get().Commission.CreditIntervalSecs)
	if interval <= 0 {
		interval = 300
	}
	return now + interval - now%interval
}

// creditSnapshot 是健康面板那一段。
//
// D-15 时它报的是"在途 / 挂起的入账单"—— 那两个数只在跨库两阶段下才有意义。现在
// 入账是本地事务,没有在途也没有挂起,能报的只有"累计发出去了多少"。留一个恒为
// 零的在途计数比不留更糟:运维会以为系统在盯着一个其实不存在的队列。
func creditSnapshot(ctx context.Context) map[string]any {
	out := map[string]any{"credited_rows": int64(0), "credited_stardust": int64(0)}
	gdb := db.Get()
	if gdb == nil {
		out["error"] = db.ErrNotReady.Error()
		return out
	}
	var row struct {
		Cnt    int64
		Amount int64
	}
	if err := gdb.WithContext(ctx).Model(&Credit{}).
		Select("COUNT(*) AS cnt, COALESCE(SUM(amount), 0) AS amount").
		Where("status = ?", CreditStatusDone).Scan(&row).Error; err != nil {
		out["error"] = err.Error()
		return out
	}
	out["credited_rows"] = row.Cnt
	out["credited_stardust"] = row.Amount
	return out
}
