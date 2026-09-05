package stardust

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/qianye/config"
	"github.com/QuantumNous/new-api/qianye/db"
	"github.com/QuantumNous/new-api/qianye/groupname"
	"github.com/QuantumNous/new-api/qianye/modules/invite"
	"github.com/QuantumNous/new-api/qianye/service/lease"

	"github.com/shopspring/decimal"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// settle.go —— 消费返的次日结算(design-15 §4.1)。
//
// 数据源是 LOG_DB 的 logs(type=2),按日整日重算(SET 语义、幂等、可重跑),
// 不走 QyOnConsumeLog 单槽 hook(D-B)。三个量用同一把时钟写死(settleTargetDay):
// run_date 是"今天"、D 是"昨天"、门槛是 dayStart(now) + settle_delay_minutes。
//
// 每个用户的结算是**一个扩展库事务**:锁余额行 → 事务内重读候选桶(FOR UPDATE)→
// 冻结档位 → floor(carry + Σgross) → Credit → 逐桶 CAS 成 settled → 余数回写。
// 把"冻结"放进事务而不是事务之前,是为了关掉这样一个窗口:另一次运行(管理端 rerun
// 与心跳并发)在冻结之前读到 gross=0 的 computed 桶并把它结成 settled,那笔钱就永远发不出去。
// 事务内重读 + 余额行锁让并发的第二笔在锁上排队,轮到它时桶已经 settled、CAS 落空回滚。

const (
	secondsPerDay = int64(86400)
	// settleUserBatch 是每轮选人的上限;settleDrainMaxRounds 是单次运行的轮次上界。
	// 400 × 500 = 20 万人/次,远超本站量级;它防的不是业务规模,而是"选人 SQL 写错
	// 导致永远取到同一批"这种死循环占着租约。
	settleUserBatch      = 500
	settleDrainMaxRounds = 400
	// settleAggregateTimeout 是 LOG_DB 单日聚合的截止时间(照 invite 的
	// dailyConsumeQueryTimeout 形状)。覆盖索引由 invite.StartSharedInfra 后台补建,
	// 缺失时无索引基线是秒级到分钟级;超时就报当天 partial 而不是拖住 LOG_DB。
	settleAggregateTimeout = 180 * time.Second

	settleRefType   = "sd_settle"
	settleIdemScope = "sd_settle"
)

// 三条排除口径的 LIKE 模式:常量、不含用户输入,写法照 model/log.go 的
// `logs.other LIKE ?`(四种日志库都用裸 LIKE,不加 ESCAPE、不加 LOWER)。
var (
	likeViolationFee = `%"violation_fee":true%`
	likeChannelTest  = `%"` + model.ChannelTestLogOtherKey + `":true%`
	likeSubscription = `%"billing_source":"subscription"%`
)

// warnf 是本包后台任务与 hook 的统一告警出口。
func warnf(format string, args ...any) {
	common.SysError("qianye/stardust: " + fmt.Sprintf(format, args...))
}

func init() {
	taskStarters = append(taskStarters, func() {
		// 跨节点失效通道与 logs 覆盖索引是邀请与星屑共用的设施,邀请关着时也要起
		// (design-15 §4.6);它自带 sync.Once,与 invite.StartTasks 里那次不冲突。
		if invite.SharedInfraWanted() {
			invite.StartSharedInfra()
		}
		every := config.Get().Stardust.SettleIntervalSeconds
		if every <= 0 {
			every = 300
		}
		// 必须走 lease.Run:多节点都配成 master 时结算会双跑。租约只保证"同一时刻
		// 只有一个节点在跑","今天只跑一次"由 qy_sd_settle_run 上的条件写承担。
		lease.Run("stardust.settle", time.Duration(every)*time.Second, runSettle)
	})
}

// settleTargetDay 回答一次心跳的三个量:run_date(今天)、D(昨天)、门槛是否已过。
//
// 纯函数:日界与偏移全部来自 invite 导出的日界函数(D-C,不造第三份"一天")。
// delay=0 退化为"日界后第一次心跳开跑";未到门槛时也返回 run_date 与 D,
// 供状态接口展示"下一次结算的是哪一天"。
func settleTargetDay(now int64, settleDelayMinutes int) (runDate, targetDay string, ready bool) {
	if settleDelayMinutes < 0 {
		settleDelayMinutes = 0
	}
	start := invite.DayStart(now)
	runDate = invite.DayKey(now)
	targetDay = invite.DayKey(start - 1)
	ready = now >= start+int64(settleDelayMinutes)*60
	return runDate, targetDay, ready
}

// runSettle 是结算后台任务的入口,由 lease.Run 按心跳周期驱动。
//
// 每次心跳先过门槛(不抢占、不建行),再问"今天这一次跑过了没有":
// 没跑过就抢下来,结 D 这一天并排空 held 队列。
func runSettle(ctx context.Context) {
	if ctx.Err() != nil {
		return
	}
	now := common.GetTimestamp()
	runDate, day, ready := settleTargetDay(now, config.Get().Stardust.SettleDelayMinutes)
	if !ready {
		return
	}
	claimed, err := claimDailyRun(ctx, runDate, day, now)
	if err != nil {
		warnf("抢占 %s 的结算运行记录失败: %v", runDate, err)
		return
	}
	if !claimed {
		return // 今天已经跑完,或别的节点正在跑
	}
	st := settleDay(ctx, day, settleKey{Day: day, RunDate: runDate})
	if err := finishDailyRun(ctx, runDate, st, common.GetTimestamp()); err != nil {
		warnf("回写 %s 的结算运行记录失败: %v", runDate, err)
	}
}

// settleKey 决定一次运行给每个用户的流水用什么幂等键。
//
// 日结:sdsettle:<run_date>:<user_id>(design §3.2,每人每次运行一行)。
// 管理端重跑:sdrerun:<D>:<user_id>:<nonce> —— 必须与日结分族:同一天里先重跑了
// 老日子 D'、再跑当天日结,两笔都是合法发放,共用一个键会让第二笔被判成重放,
// 桶标 settled 而钱没发。nonce 取发起时刻,让同一天的两次重跑也不撞键。
type settleKey struct {
	Day     string
	RunDate string
	Rerun   bool
	Nonce   string
}

func (k settleKey) idemKey(userId int) string {
	if k.Rerun {
		return "sdrerun:" + k.Day + ":" + strconv.Itoa(userId) + ":" + k.Nonce
	}
	return "sdsettle:" + k.RunDate + ":" + strconv.Itoa(userId)
}

// settleStats 是一次结算(日结或重跑)的结果,落进 qy_sd_settle_run 或重跑响应。
type settleStats struct {
	// Recomputed 是这次重算插入的日桶数((U,D) 已 held / settled 的不算)。
	Recomputed int
	Rounds     int
	// Processed 是发放成功的用户数(含 net=0 只结转余数的);Held 是被暂缓的;
	// Skipped 是事务内重读时已无候选桶的(被并发运行抢先结掉);Failed 是报错的。
	Processed int
	Held      int
	Skipped   int
	Failed    int
	Granted   int64
	// Invite* 是同一跑里**下线消费返**那一段的结果,按邀请人计(settle_invite.go)。
	// 它在消费返排空之后跑;消费返没排空(轮次上界 / 租约丢失)时这一段整个不跑,
	// 留给当天的 partial 重试。
	InviteProcessed int
	InviteHeld      int
	InviteSkipped   int
	InviteFailed    int
	InviteGranted   int64
	// Drained 为真表示候选集取空了;Aborted 为真表示租约中途丢了。
	Drained bool
	Aborted bool
	Note    string
}

// complete 回答"这一跑能不能算 done":两段都排空、一个人都没失败、租约没丢。
func (st settleStats) complete() bool {
	return st.Drained && !st.Aborted && st.Failed == 0 && st.InviteFailed == 0
}

// settleDay 结 D 这一天:重算日桶,然后排空候选集(D 的 computed ∪ 任意日期的 held)。
//
// 单个用户报错只计数、不中断:让第 300 个人的错误吃掉后面 300 个人当天的星屑是
// 不可接受的。选人用键集游标(user_id 递增)而不是"再查一次":held 的用户结完还在
// 候选集里,不推进游标就会一直取到同一批。
func settleDay(ctx context.Context, day string, key settleKey) (st settleStats) {
	n, rows, err := recomputeDay(ctx, day)
	if err != nil {
		st.Note = "重算 " + day + " 失败: " + err.Error()
		warnf("%s", st.Note)
		return st
	}
	st.Recomputed = n

	cursor := 0
	for st.Rounds < settleDrainMaxRounds {
		if ctx.Err() != nil {
			st.Aborted = true
			st.Note = "租约中途丢失,队列未排空"
			return st
		}
		ids, err := candidateUsers(ctx, day, cursor, settleUserBatch)
		if err != nil {
			st.Note = "选人失败: " + err.Error()
			warnf("%s", st.Note)
			return st
		}
		st.Rounds++
		if len(ids) == 0 {
			st.Drained = true
			// 消费返排空之后才轮到下线消费返:两段用同一批 logs 聚合、同一把日界,
			// 但候选集不同(一个按消费者、一个按邀请人),所以分两段排空。
			settleInviteDay(ctx, day, key, rows, &st)
			return st
		}
		for _, id := range ids {
			if ctx.Err() != nil {
				st.Aborted = true
				st.Note = "租约中途丢失,队列未排空"
				return st
			}
			out, err := settleUser(ctx, id, day, key)
			switch {
			case err != nil:
				st.Failed++
				warnf("用户 %d 的消费返结算失败(本日稍后重试): %v", id, err)
			case out.Held:
				st.Held++
			case out.Skipped:
				st.Skipped++
			default:
				st.Processed++
				st.Granted += out.Net
			}
		}
		cursor = ids[len(ids)-1]
		if !key.Rerun {
			if err := heartbeatDailyRun(ctx, key.RunDate, st, common.GetTimestamp()); err != nil {
				warnf("刷新 %s 结算运行心跳失败: %v", key.RunDate, err)
			}
		}
	}
	st.Note = "达到单次排空轮次上界,剩余队列本日稍后重试"
	warnf("%s 的结算排空达到轮次上界 %d,队列未排空", day, settleDrainMaxRounds)
	return st
}

// consumeAgg 是 logs 侧聚合出来的一行。
type consumeAgg struct {
	UserId    int   `gorm:"column:user_id"`
	BaseQuota int64 `gorm:"column:base_quota"`
}

// recomputeDay 重算 D 的日桶:DELETE 当天 computed 行,再按聚合结果 DoNothing 插入。
//
// (U,D) 已存在的(只可能是 held / settled)跳过不重算 —— 已 held 的桶 gross 冻结于
// 首次计算值,rerun 不追溯(D-D 的既定代价)。gross / rate 此时先不填,定档在结算事务里。
// 返回真正插入的行数,以及这一天的 logs 聚合本身 —— 下线消费返用同一份聚合分桶,
// 不再扫第二遍 LOG_DB。
func recomputeDay(ctx context.Context, day string) (int, []consumeAgg, error) {
	start, ok := invite.DayKeyStart(day)
	if !ok {
		return 0, nil, errBadRequest("day 必须是 YYYYMMDD")
	}
	rows, err := aggregateDayConsume(ctx, start, start+secondsPerDay, nil)
	if err != nil {
		return 0, nil, err
	}
	gdb := db.Get()
	if gdb == nil {
		return 0, nil, db.ErrNotReady
	}
	now := common.GetTimestamp()
	inserted := 0
	err = gdb.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if err := tx.Where("bucket_date = ? AND status = ?", day, AccrualComputed).
			Delete(&Accrual{}).Error; err != nil {
			return err
		}
		buckets := make([]Accrual, 0, len(rows))
		for _, r := range rows {
			if r.BaseQuota <= 0 {
				continue
			}
			buckets = append(buckets, Accrual{
				UserId: r.UserId, BucketDate: day, BaseQuota: r.BaseQuota,
				Gross: decimal.Zero, Status: AccrualComputed, ComputedAt: now,
			})
		}
		if len(buckets) == 0 {
			return nil
		}
		res := tx.Clauses(clause.OnConflict{
			Columns:   []clause.Column{{Name: "user_id"}, {Name: "bucket_date"}},
			DoNothing: true,
		}).CreateInBatches(&buckets, 500)
		if res.Error != nil {
			return res.Error
		}
		inserted = int(res.RowsAffected)
		return nil
	})
	if err != nil {
		db.MarkFailure(err)
		return 0, nil, wrapInternal("重算 "+day+" 的日桶", err)
	}
	return inserted, rows, nil
}

// aggregateDayConsume 在 LOG_DB 上按用户汇总 [start, end) 的 type=2 消费。
//
// 只算 type=2、**不减 type=6**(D-D:退款不冲减)。排除三条都只在 logs.other 里:
// 违规扣费、渠道测试(兜底 token_name = 模型测试 AND token_id = 0)、订阅出资
// (开关 stardust.exclude_subscription_consume)。other / token_name 用 COALESCE 包一层:
// `NOT LIKE` 对 NULL 的结果是 NULL,会把整行静默丢掉。不读 logs.group、不 JOIN,
// ClickHouse 日志库同一条 SQL 可跑。
//
// userIds 为 nil 表示全站;非 nil 时只看这些人(用户端"我的下线今天花了多少"用它,
// 口径与日结逐字相同 —— 今天看到的数就是明天结算的基数)。
func aggregateDayConsume(ctx context.Context, start, end int64, userIds []int) ([]consumeAgg, error) {
	logDB := model.QyLogDB()
	if logDB == nil {
		return nil, errors.New("日志库未初始化")
	}
	qctx, cancel := context.WithTimeout(ctx, settleAggregateTimeout)
	defer cancel()
	q := logDB.WithContext(qctx).Model(&model.Log{}).
		Select("user_id, COALESCE(SUM(quota), 0) AS base_quota").
		Where("type = ?", model.LogTypeConsume).
		Where("created_at >= ? AND created_at < ?", start, end).
		Where("COALESCE(other, '') NOT LIKE ?", likeViolationFee).
		Where("COALESCE(other, '') NOT LIKE ?", likeChannelTest).
		Where("NOT (COALESCE(token_name, '') = ? AND COALESCE(token_id, 0) = 0)", model.ChannelTestTokenName)
	if config.Get().Stardust.SubscriptionConsumeExcluded() {
		q = q.Where("COALESCE(other, '') NOT LIKE ?", likeSubscription)
	}
	if userIds != nil {
		q = q.Where("user_id IN ?", userIds)
	}
	rows := make([]consumeAgg, 0, 256)
	if err := q.Group("user_id").Having("SUM(quota) > 0").Scan(&rows).Error; err != nil {
		return nil, fmt.Errorf("聚合 logs 失败(超时会让当天 partial 重试): %w", err)
	}
	return rows, nil
}

// candidateUsers 取一页候选用户:D 的 computed 桶 ∪ 任意日期的 held 桶,按 user_id 去重、
// 键集游标递增。第二路不能省:D 日被暂缓、之后停止消费的用户否则永远发不出去
// (否则是饥饿:一批人永远排不到)。
func candidateUsers(ctx context.Context, day string, after, limit int) ([]int, error) {
	gdb := db.Get()
	if gdb == nil {
		return nil, db.ErrNotReady
	}
	ids := make([]int, 0, limit)
	err := gdb.WithContext(ctx).Model(&Accrual{}).Distinct().
		Where("user_id > ?", after).
		Where("(bucket_date = ? AND status = ?) OR status = ?", day, AccrualComputed, AccrualHeld).
		Order("user_id asc").Limit(limit).Pluck("user_id", &ids).Error
	if err != nil {
		db.MarkFailure(err)
		return nil, err
	}
	return ids, nil
}

// userSnapshot 是结算前锁外读到的一次主库快照(D-D:best-effort 的策略闸门,不是账务判定)。
type userSnapshot struct {
	Found   bool
	Deleted bool
	Group   string
	Quota   int64
	Status  int
}

// holdReason 按 D-D 的顺序判定要不要暂缓:注销 / 不存在 → 封禁 → 透支。
func (s userSnapshot) holdReason() string {
	switch {
	case !s.Found || s.Deleted:
		return HoldAccountRemoved
	case s.Status != common.UserStatusEnabled:
		return HoldAccountDisabled
	case s.Quota < 0:
		return HoldOverdraft
	}
	return ""
}

// loadUserSnapshot 读一次主库 users(Unscoped:软删的账号要能被识别成 account_removed)。
// 多参数 Select 让 GORM 按方言给 group 这个保留字加引号。
func loadUserSnapshot(ctx context.Context, userId int) (userSnapshot, error) {
	if model.DB == nil {
		return userSnapshot{}, errors.New("主库尚未初始化")
	}
	var rows []struct {
		Id        int
		Group     string
		Quota     int64
		Status    int
		DeletedAt gorm.DeletedAt
	}
	err := model.DB.WithContext(ctx).Unscoped().Model(&model.User{}).
		Select("id", "group", "quota", "status", "deleted_at").
		Where("id = ?", userId).Limit(1).Scan(&rows).Error
	if err != nil {
		return userSnapshot{}, fmt.Errorf("读取用户 %d 的主库快照: %w", userId, err)
	}
	if len(rows) == 0 {
		return userSnapshot{}, nil
	}
	r := rows[0]
	return userSnapshot{Found: true, Deleted: r.DeletedAt.Valid, Group: r.Group, Quota: r.Quota, Status: r.Status}, nil
}

// consumeRateFor 解析消费返档位:分组表的 consume_bps → 回落全站 stardust.consume_bps。
// 决定 D 日档位的是结算这一刻读到的 users.group(§4.1)。
func consumeRateFor(ctx context.Context, group string) int {
	if r, ok := groupRateFor(ctx, group); ok && r.ConsumeBps != nil {
		return *r.ConsumeBps
	}
	return effective().ConsumeBps
}

// grossOf 是日桶的应返星屑:base × bps / 10000 / quota_per_unit,截断到 10 位小数。
//
// 截断而不是四舍五入,且截到列的 scale:结算把 Go 里算出的数求和后落账,库里存的必须
// 逐字等于参与求和的数,否则 I2(Σ settled gross == Σ 流水 + carry)在第一天就不成立。
// QuoRem 是精确的整数商,不经 16 位浮动精度的除法。
func grossOf(base int64, bps int, qpu int64) decimal.Decimal {
	if base <= 0 || bps <= 0 || qpu <= 0 {
		return decimal.Zero
	}
	num := decimal.NewFromInt(base).Mul(decimal.NewFromInt(int64(bps)))
	den := decimal.NewFromInt(10_000).Mul(decimal.NewFromInt(qpu))
	q, _ := num.QuoRem(den, 10)
	return q
}

// settleOutcome 是一个用户的结算结果。
type settleOutcome struct {
	Held    bool
	Skipped bool
	Net     int64
}

// settleUser 结算一个用户名下的全部候选桶(一个扩展库事务)。
//
// 快照在事务之前、锁外读一次(§4.1 步骤 3);三种暂缓都不 Credit。
// 发放:floor(carry + Σgross) 只走正分支(gross ≥ 0),net > 0 才写流水;
// net=0 的桶同样 settled 且 ledger_id=0,零头进 carry。任一桶 CAS 落空整笔回滚。
func settleUser(ctx context.Context, userId int, day string, key settleKey) (settleOutcome, error) {
	snap, err := loadUserSnapshot(ctx, userId)
	if err != nil {
		return settleOutcome{}, err
	}
	reason := snap.holdReason()
	group := groupname.Effective(snap.Group)
	bps := consumeRateFor(ctx, snap.Group)
	qpu := QuotaPerUnit()

	gdb := db.Get()
	if gdb == nil {
		return settleOutcome{}, db.ErrNotReady
	}
	var out settleOutcome
	err = gdb.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		bal, err := LockBalance(tx, userId)
		if err != nil {
			return err
		}
		var buckets []Accrual
		if err := db.LockForUpdate(tx).
			Where("user_id = ? AND ((bucket_date = ? AND status = ?) OR status = ?)",
				userId, day, AccrualComputed, AccrualHeld).
			Order("bucket_date asc, id asc").Find(&buckets).Error; err != nil {
			return err
		}
		if len(buckets) == 0 {
			out.Skipped = true
			return nil
		}
		now := common.GetTimestamp()
		if reason != "" {
			out.Held = true
			return holdBuckets(tx, userId, buckets, reason, group, bps, qpu, now)
		}

		sum := decimal.Zero
		var baseTotal int64
		for i := range buckets {
			b := &buckets[i]
			if b.Status == AccrualComputed {
				b.RateBps, b.UserGroup, b.QuotaPerUnit = bps, group, qpu
				b.Gross = grossOf(b.BaseQuota, bps, qpu)
			}
			sum = sum.Add(b.Gross)
			baseTotal += b.BaseQuota
		}
		total := bal.Carry.Add(sum)
		netInt, clamp := common.QuotaFromDecimalChecked(total.Floor())
		if clamp != nil {
			warnf("用户 %d 的消费返换算触顶: %s", userId, clamp.Error())
		}
		net := int64(netInt)
		if net < 0 {
			return fmt.Errorf("stardust: 用户 %d 的结算净额为负(%d),拒绝落账", userId, net)
		}
		var ledgerId int64
		if net > 0 {
			res, err := Credit(tx, Posting{
				UserId: userId, Kind: KindConsumeRebate, Amount: net,
				IdemScope: settleIdemScope, IdemKey: key.idemKey(userId),
				RefType: settleRefType, RefNo: key.RunDate,
				RateBps: bps, RateGroup: group, BaseQuota: baseTotal,
				Remark: "消费返 " + day,
			})
			if err != nil {
				return err
			}
			if !res.Inserted {
				// 键已被占用 = 这一次运行已经给他发过;桶却还是 computed/held,说明两次
				// 运行交错。绝不能把桶标成 settled 而不发钱,整笔回滚等下一次。
				return fmt.Errorf("stardust: 幂等键 %s 已存在(流水 %s),本次结算整笔回滚",
					key.idemKey(userId), res.LedgerNo)
			}
			ledgerId = res.LedgerId
		}
		carryAfter := total.Sub(decimal.NewFromInt(net))
		for _, b := range buckets {
			set := map[string]any{
				"status": AccrualSettled, "ledger_id": ledgerId, "settled_at": now, "hold_reason": "",
			}
			if b.Status == AccrualComputed {
				set["rate_bps"], set["user_group"], set["quota_per_unit"], set["gross"] =
					b.RateBps, b.UserGroup, b.QuotaPerUnit, b.Gross
			}
			res := tx.Model(&Accrual{}).Where("id = ? AND status = ?", b.Id, b.Status).Updates(set)
			if res.Error != nil {
				return res.Error
			}
			if res.RowsAffected != 1 {
				return fmt.Errorf("stardust: 日桶 %d 的结算 CAS 失败(状态已被并发改动),本批回滚", b.Id)
			}
		}
		if err := tx.Model(&Balance{}).Where("user_id = ?", userId).Updates(map[string]any{
			"carry": carryAfter, "hold_reason": "", "updated_at": now,
		}).Error; err != nil {
			return err
		}
		out.Net = net
		return nil
	})
	if err != nil {
		db.MarkFailure(err)
		return settleOutcome{}, err
	}
	return out, nil
}

// holdBuckets 把一个用户的候选桶全部标成 held(不 Credit),余额行的 hold_reason 同步。
//
// computed 桶在这一刻冻结档位(此后 rerun 不追溯);已 held 的桶只刷新原因。
// 后者不断言 RowsAffected:值没变时 MySQL 报 0 行,而那不是并发冲突。
func holdBuckets(tx *gorm.DB, userId int, buckets []Accrual, reason, group string, bps int, qpu int64, now int64) error {
	for _, b := range buckets {
		set := map[string]any{"status": AccrualHeld, "hold_reason": reason}
		if b.Status == AccrualComputed {
			set["rate_bps"], set["user_group"], set["quota_per_unit"], set["gross"] =
				bps, group, qpu, grossOf(b.BaseQuota, bps, qpu)
		}
		res := tx.Model(&Accrual{}).Where("id = ? AND status = ?", b.Id, b.Status).Updates(set)
		if res.Error != nil {
			return res.Error
		}
		if b.Status == AccrualComputed && res.RowsAffected != 1 {
			return fmt.Errorf("stardust: 日桶 %d 的暂缓 CAS 失败(状态已被并发改动),本批回滚", b.Id)
		}
	}
	return tx.Model(&Balance{}).Where("user_id = ?", userId).Updates(map[string]any{
		"hold_reason": reason, "updated_at": now,
	}).Error
}

// RerunStats 是管理端重跑一天的结果(契约 §3 settle/rerun)。
//
// Invite* 是下线消费返那一段(按邀请人计),与消费返的四个数分开报。
type RerunStats struct {
	Recomputed    int   `json:"recomputed"`
	Settled       int   `json:"settled"`
	Held          int   `json:"held"`
	Skipped       int   `json:"skipped"`
	Failed        int   `json:"failed"`
	Granted       int64 `json:"granted"`
	InviteSettled int   `json:"invite_settled"`
	InviteHeld    int   `json:"invite_held"`
	InviteFailed  int   `json:"invite_failed"`
	InviteGranted int64 `json:"invite_granted"`
}

// rerunDay 同步重跑桶日 D:只对 computed / held 桶生效,已 settled 的一律不动。
//
// 拒绝尚未封口的日子:D 的窗口要到日界 + settle_delay_minutes 之后才算齐;提前把
// 半天的消费结成 settled,剩下半天在次日重算时会被 (U,D) 唯一键挡在门外 —— 那笔钱
// 就永久少了。重跑的是今天的目标日时,顺手把今天的运行记录重新武装,
// 让下一次心跳再核一遍并把状态收敛成 done。
func rerunDay(ctx context.Context, day string) (RerunStats, error) {
	start, ok := invite.DayKeyStart(day)
	if !ok {
		return RerunStats{}, errBadRequest("day 必须是 YYYYMMDD")
	}
	now := common.GetTimestamp()
	delay := config.Get().Stardust.SettleDelayMinutes
	if delay < 0 {
		delay = 0
	}
	if now < start+secondsPerDay+int64(delay)*60 {
		return RerunStats{}, errBadRequest("该日的消费日志窗口尚未封口(要到日界 + settle_delay_minutes 之后)")
	}
	st := settleDay(ctx, day, settleKey{
		Day: day, RunDate: invite.DayKey(now), Rerun: true, Nonce: strconv.FormatInt(now, 10),
	})
	out := RerunStats{
		Recomputed: st.Recomputed, Settled: st.Processed, Held: st.Held,
		Skipped: st.Skipped, Failed: st.Failed, Granted: st.Granted,
		InviteSettled: st.InviteProcessed, InviteHeld: st.InviteHeld,
		InviteFailed: st.InviteFailed, InviteGranted: st.InviteGranted,
	}
	if !st.Drained {
		return out, wrapInternal("重跑 "+day, errors.New(st.Note))
	}
	if runDate, target, ready := settleTargetDay(now, delay); ready && target == day {
		if _, err := rearmDailyRun(ctx, runDate, now); err != nil {
			warnf("重跑 %s 后重新武装 %s 的运行记录失败: %v", day, runDate, err)
		}
	}
	return out, nil
}

// settleStatus 是契约 §3 GET /stardust/settle/status 的数据。
//
// next_settle_at 只由后端下发(D-03):今天这一跑已 done 就指向明天,否则指向今天的门槛
// (已过门槛而未 done 时它在过去,前端据此显示"进行中 / 待重试")。
func settleStatus(ctx context.Context) map[string]any {
	now := common.GetTimestamp()
	delay := config.Get().Stardust.SettleDelayMinutes
	if delay < 0 {
		delay = 0
	}
	runDate, target, ready := settleTargetDay(now, delay)
	out := map[string]any{
		"last_run":       nil,
		"next_settle_at": invite.DayStart(now) + int64(delay)*60,
		"target_day":     target,
		"run_date":       runDate,
		"ready":          ready,
		"max_attempts":   settleRunMaxAttempts,
	}
	gdb := db.Get()
	if gdb == nil {
		out["error"] = db.ErrNotReady.Error()
		return out
	}
	var rows []SettleRun
	// run_date <= 今天:日界偏移被调小时表里会留下未来日期的行,不过滤会把一个
	// **未来**的日期当成"上一跑"。
	if err := gdb.WithContext(ctx).Where("run_date <= ?", runDate).
		Order("run_date desc").Limit(1).Find(&rows).Error; err != nil {
		db.MarkFailure(err)
		out["error"] = err.Error()
		return out
	}
	if len(rows) == 0 {
		return out
	}
	r := rows[0]
	out["last_run"] = map[string]any{
		"run_date":         r.RunDate,
		"target_date":      r.TargetDate,
		"status":           r.Status,
		"attempts":         r.Attempts,
		"started_at":       r.StartedAt,
		"finished_at":      r.FinishedAt,
		"processed":        r.Processed,
		"failed":           r.Failed,
		"held":             r.Held,
		"granted":          r.Granted,
		"invite_processed": r.InviteProcessed,
		"invite_held":      r.InviteHeld,
		"invite_failed":    r.InviteFailed,
		"invite_granted":   r.InviteGranted,
		"remark":           r.Remark,
	}
	if r.RunDate == runDate && r.Status == SettleRunDone {
		out["next_settle_at"] = invite.NextDayStart(now) + int64(delay)*60
	}
	return out
}
