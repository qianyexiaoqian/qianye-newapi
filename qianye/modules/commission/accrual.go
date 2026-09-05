package commission

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"strconv"
	"strings"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/qianye/config"
	"github.com/QuantumNous/new-api/qianye/db"

	"github.com/shopspring/decimal"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// rateUnitsDivisor 把内部整数费率(万分比)还原成比例。
var rateUnitsDivisor = decimal.NewFromInt(config.RatePercentScale * 100)

// calcGross 计算一笔佣金的精确金额(**星屑**),永不截断。
//
//	gross = base_quota × rate_bps / 10000 / quota_per_unit
//
// 这是本模块存在的核心理由。base_quota 是整数额度,单次对话常见 10~500;默认刻度下
// 1 星屑 = 500000 额度,于是 5% 的佣金落在 1e-6 星屑量级。任何一步提前取整都会把它
// 变成 0,一天几千次请求全部归零 —— 用户看到"用了一天没佣金"而钱被平台吞掉。
// 全精度累在 decimal 里,只在结算那一刻 floor 成整数星屑,零头进 unsettled_amount。
//
// 三个除数都是整数:rateUnits 是万分比(0.05、0.1025 在 decimal 下都可精确表示),
// quotaPerUnit 是刻度。整条链路上没有任何一步经过 float64。
//
// quotaPerUnit <= 0 一律返回 0 而不是"当 1 用":当 1 用意味着把额度数直接当星屑发,
// 默认刻度下是 50 万倍的超发。刻度读不到是配置或调用方的错,宁可这一笔不计佣。
func calcGross(baseQuota int64, rateUnits int, quotaPerUnit int64) decimal.Decimal {
	if baseQuota <= 0 || rateUnits <= 0 || quotaPerUnit <= 0 {
		return decimal.Zero
	}
	return decimal.NewFromInt(baseQuota).
		Mul(decimal.NewFromInt(int64(rateUnits))).
		Div(rateUnitsDivisor).
		Div(decimal.NewFromInt(quotaPerUnit))
}

// capGross 对单笔佣金封顶,并把"削掉了多少"一起交出来。
//
// 作用在"单次计佣增量"上而不是日聚合行的总额:日聚合行会跨越一整天,
// 用它当上限等于给用户设了个日封顶,那是另一个语义(见 opSettings.DailyCapStardust)。
//
// # 为什么必须返回削减量
//
// 封顶命中之后,那一行的 base_quota × rate_bps / 10000 / quota_per_unit 就不再等于
// gross_amount。削减量必须作为**事实**落在行上(Accrual.CappedAmount),让
//
//	base_quota × rate_bps / 10000 / quota_per_unit == gross_amount + capped_amount   (I3)
//
// 重新成为一条可以逐行验证的恒等式。
//
// # I3 的适用范围:只有正额计佣行,不是全表
//
// 它只在 consume.go 那两处经由 capGross 落库的行上成立,也就是
// source_type ∈ {consume, topup, redemption}。另外两类来源按设计就不满足它:
//
//	manual(api_admin_adjust.go)—— 手工增减既没有下线也没有费率,rate_bps 落 0
//	  而 gross 落管理员填的那个数。
//	clawback(clawback.go)—— base_quota 落负数(幂等指纹),原单被封顶削过时按
//	  实际比例等比冲正,超额冲正被 netAccrued 削到恰好冲平,三处各自把它打破。
//
// 账本体检(api_admin.go 的 ledgerCheck)因此刻意**不算 I3**。
func capGross(gross decimal.Decimal, maxPerOrder int64) (capped, shaved decimal.Decimal) {
	if maxPerOrder <= 0 {
		return gross, decimal.Zero
	}
	limit := decimal.NewFromInt(maxPerOrder)
	if gross.GreaterThan(limit) {
		return limit, gross.Sub(limit)
	}
	if gross.LessThan(limit.Neg()) {
		return limit.Neg(), gross.Sub(limit.Neg())
	}
	return gross, decimal.Zero
}

// maxSafeAmount 是落库前的合理性闸门。decimal(30,10) 的容量是 10^20,
// 业务上不可达;真出现这种数字一定是上游数据坏了,宁可拒写也不能污染账本。
var maxSafeAmount = decimal.New(1, 19)

func amountSane(d decimal.Decimal) bool { return d.Abs().LessThan(maxSafeAmount) }

// normalizeIdemKey 把任意长度的业务键压进 varchar(96)。
//
// trade_no 在主库是 varchar(255),直接截断会让两个不同订单撞成同一个幂等键 ——
// 那意味着第二笔充值不计佣。超长时改用哈希,保证单射。
func normalizeIdemKey(raw string) string {
	if len(raw) <= 96 {
		return raw
	}
	sum := sha256.Sum256([]byte(raw))
	return "h:" + hex.EncodeToString(sum[:])[:64]
}

// bucketDate 返回消费日聚合的日键。日界口径由 invite/dayline.go 统一给出。
func bucketDate(ts int64) string { return dayKey(ts) }

// bucketMatureAt 返回日聚合桶的成熟时间:整天结束之后再加持有期。
//
// 不用"首次写入时间 + N 天":桶会持续增长到当天结束,用首次写入时间会让
// 当天晚些时候的消费提前成熟,削弱持有期本来的防套利作用。
//
// 这里的"整天结束"就是日界,因此成熟时刻恰好落在某个日界上,而一日一结算
// 在日界之后的第一次心跳开跑 —— mature_at <= now 成立,当天结算。
// 到账日 = 消费日 + holding_days + 1(见 payoutDayOffset)。
//
// 负数一律按 0 处理,与 payoutDayOffset 同一条钳位。
func bucketMatureAt(day string, holdingDays int) int64 {
	if holdingDays < 0 {
		holdingDays = 0
	}
	start, ok := dayKeyStart(day)
	if !ok {
		return common.GetTimestamp() + int64(holdingDays)*secondsPerDay
	}
	return start + secondsPerDay + int64(holdingDays)*secondsPerDay
}

// newSerialNo 生成对外单号。
//
// 随机源必须是密码学安全的(common.GetUUID 走 crypto/rand):
// 禁止 common.GetRandomString,它内部是 math/rand,单号可预测就可被枚举。
func newSerialNo(prefix string) string {
	rnd := common.GetUUID()
	if len(rnd) > 12 {
		rnd = rnd[:12]
	}
	return prefix + time.Now().UTC().Format("20060102T150405") + "-" + rnd
}

// accrualInput 是一次计佣写入的全部参数。
type accrualInput struct {
	SourceType string
	IdemKey    string
	SourceRef  string

	InviterId int
	InviteeId int

	BaseQuota int64
	BaseMoney decimal.Decimal
	// RateUnits 是本次生效的费率(万分比),RateGroup 是判定它时用的那个分组 ——
	// 【推广人(上线)自己】的分组,由 resolveInviterPricing 一处解析。
	// 两者一起冻结进行,事后才解释得清"这笔为什么是这个数"。
	RateUnits int
	RateGroup string
	// QuotaPerUnit 是算 Gross 时用的那个刻度,与 RateUnits / RateGroup 一起冻结进行。
	QuotaPerUnit int64
	Gross        decimal.Decimal
	// Capped 是单笔封顶(max_per_order_stardust)从本次增量里削掉的金额,永远非负。
	// 它与 Gross 一起落库,使 I3 在被削过的行上依然成立 —— 见 capGross。
	Capped decimal.Decimal

	MatureAt   int64
	BucketDate string
	Status     string
	RiskFlags  string

	RefAccrualId int64
	Remark       string

	// Accumulate 为真时,幂等键冲突不是"跳过"而是"累加"(消费日聚合)。
	Accumulate bool
}

// writeAccrual 幂等地落一条计佣行。
//
// 第一个返回值表示"本次真的插入了一条新行"。调用方必须据此区分"新建"与
// "幂等命中":OnConflict{DoNothing} 命中冲突时不报错,只看 error 的调用方
// 会把一次重放当成新建 —— 计数器虚增,更糟的是管理端会照着**本次请求的**
// 参数写下一条金额虚高的成功审计,而审计表是资金系统事后仲裁的唯一凭据。
//
// 返回 error 只表示写库失败;幂等命中不是错误。
//
// ctx 必须一路传到 GORM 调用上:热路径 worker 的 200ms 上界只对
// WithContext(ctx) 的语句生效,漏接就会一直等到 innodb_lock_wait_timeout。
func writeAccrual(ctx context.Context, in accrualInput) (bool, error) {
	gdb := db.Get()
	if gdb == nil {
		return false, db.ErrNotReady
	}
	return writeAccrualTx(gdb.WithContext(ctx), in)
}

// writeAccrualTx 是 writeAccrual 的显式句柄版本,语义完全相同。
//
// 存在的理由只有一个:管理端的手工增减佣金必须在**持有余额行锁的那个事务里**
// 落账目行(见 api_admin_adjust.go)。自取 db.Get() 会拿到另一条连接,
// 那条 INSERT 就跑在锁外,校验读到的余额与写入之间重新出现了间隙。
func writeAccrualTx(gdb *gorm.DB, in accrualInput) (bool, error) {
	if gdb == nil {
		return false, db.ErrNotReady
	}
	if in.Gross.IsZero() {
		return false, nil
	}
	if !amountSane(in.Gross) {
		warnf("拒绝写入异常佣金金额 %s(来源 %s/%s)", in.Gross.String(), in.SourceType, in.IdemKey)
		return false, errors.New("commission: 佣金金额超出合理范围")
	}
	now := common.GetTimestamp()
	row := Accrual{
		AccrualNo:    newSerialNo("CA"),
		IdemScope:    in.SourceType,
		IdemKey:      normalizeIdemKey(in.IdemKey),
		InviterId:    in.InviterId,
		InviteeId:    in.InviteeId,
		SourceType:   in.SourceType,
		SourceRef:    truncate(in.SourceRef, 128),
		BaseQuota:    in.BaseQuota,
		BaseMoney:    in.BaseMoney,
		RateUnits:    in.RateUnits,
		RateGroup:    truncate(in.RateGroup, 64),
		QuotaPerUnit: in.QuotaPerUnit,
		GrossAmount:  in.Gross,
		CappedAmount: in.Capped,
		Status:       in.Status,
		RiskFlags:    truncate(in.RiskFlags, 255),
		MatureAt:     in.MatureAt,
		BucketDate:   in.BucketDate,
		RefAccrualId: in.RefAccrualId,
		Remark:       truncate(in.Remark, 255),
		CreatedAt:    now,
		UpdatedAt:    now,
	}

	// 两种模式都先走 DoNothing 的插入,再在冲突时补一条 UPDATE 累加。
	//
	// # 为什么累加不写成一条 ON CONFLICT DO UPDATE
	//
	// 因为那样就没法可移植地判断"这一次到底是新建还是幂等命中",而这个 bool
	// 正是本函数的返回值。RowsAffected 的口径三家不一致:MySQL 的
	// ON DUPLICATE KEY UPDATE 命中返回 **2**,PostgreSQL 的 ON CONFLICT
	// DO UPDATE 命中返回 **1** —— 与新插入完全同值。DoNothing 那一档三家统一
	// (命中 0),所以把两种模式都收敛到 DoNothing + 条件 UPDATE。
	res := gdb.Clauses(clause.OnConflict{
		Columns:   []clause.Column{{Name: "idem_scope"}, {Name: "idem_key"}},
		DoNothing: true,
	}).Create(&row)
	if res.Error != nil {
		db.MarkFailure(res.Error)
		accrualFailed.Add(1)
		return false, res.Error
	}
	inserted := res.RowsAffected == 1
	if in.Accumulate && !inserted {
		// 日聚合桶:冲突即累加。削减量与基数、佣金同步累加 —— 一天里可能有些
		// 增量触顶、有些没有,只有把每一次削掉的量都累上去,那条恒等式才对整行成立。
		upd := gdb.Model(&Accrual{}).
			Where("idem_scope = ? AND idem_key = ?", row.IdemScope, row.IdemKey).
			Updates(map[string]any{
				"base_quota":    gorm.Expr("base_quota + ?", in.BaseQuota),
				"gross_amount":  gorm.Expr("gross_amount + ?", in.Gross),
				"capped_amount": gorm.Expr("capped_amount + ?", in.Capped),
				"updated_at":    now,
			})
		if upd.Error != nil {
			db.MarkFailure(upd.Error)
			accrualFailed.Add(1)
			return false, upd.Error
		}
	}
	if in.Accumulate {
		accrualAccumulated.Add(1)
	} else if inserted {
		accrualCreated.Add(1)
	}
	alertLargeAccrual(in)
	// 封顶必须留痕:计数器给管理端健康面板,日志给排障。少发钱是运营
	// 显式配置的策略,但"这一行被削过"绝不能只存在于配置里。
	if in.Capped.IsPositive() {
		accrualCapped.Add(1)
		warnf("单笔封顶命中:%s/%s 的佣金被从 %s 削到 %s(上限 %d,邀请人 %d, 下线 %d)",
			in.SourceType, in.IdemKey, in.Gross.Add(in.Capped).String(), in.Gross.String(),
			effective().MaxPerOrderStardust, in.InviterId, in.InviteeId)
	}
	return inserted, nil
}

// alertLargeAccrual 对异常大额计佣告警。
//
// 单笔佣金突然跳到平时的百倍,要么是费率被改错,要么是有人在刷 ——
// 两种情况都必须让人立刻知道,而不是等月底对账才发现。
func alertLargeAccrual(in accrualInput) {
	limit := effective().LargeAlertStardust
	if limit <= 0 {
		return
	}
	if in.Gross.Abs().GreaterThan(decimal.NewFromInt(limit)) {
		warnf("单笔佣金 %s 超过告警阈值 %d(邀请人 %d, 下线 %d, 来源 %s)",
			in.Gross.String(), limit, in.InviterId, in.InviteeId, in.SourceType)
	}
}

// ───────────────────────── 通用小工具 ─────────────────────────

func truncate(s string, max int) string {
	if len(s) <= max {
		return s
	}
	r := []rune(s)
	for len(string(r)) > max {
		r = r[:len(r)-1]
	}
	return string(r)
}

func itoa(v int) string { return strconv.Itoa(v) }

func itoa64(v int64) string { return strconv.FormatInt(v, 10) }

// consumeIdemKey 是消费日聚合的幂等键。
//
// 除了(下线、日期)之外还带上**冻结的费率与分组**,因为日聚合行是
// "边增长边结算"的:桶会跨越一整天,而这一天里**上线**可能换了分组、运营
// 可能调了费率。只按(下线、日期)聚合的话,后来的增量会按新费率算出
// gross 累加进一行标着旧费率的记录里,那一行从此 base × rate ≠ gross。
//
// **上线也必须进键**(inviterId):换绑当天下线后续的消费会撞上旧上线那一行的
// 唯一键,金额被原子累加进去而 inviter_id 保持旧值 —— 钱结结实实发给了前一个上线。
// 进键之后换绑当天会落两行:换绑之前那段归旧上线,之后那段归新上线。
//
// **持有期也必须进键**(holdingDays):它决定这一行的 mature_at,而日聚合桶的
// 累加不改 mature_at —— 运营中午把 holding_days 从 7 改成 0,当天已建过桶的下线
// 在那之后的消费会按旧持有期再压 7 天,而界面都按新配置显示 T+1。
//
// 凡是被冻结进行、事后又决定这一行怎么处置的策略值,都必须参与聚合身份。
// 代价只是"改配置当天多出一行",而这正是账面上应该看得见的事实。
// 传进来的值必须与写进 MatureAt 的那个**同源钳位**(bucketMatureAt 把负数按 0 处理)。
func consumeIdemKey(inviterId int, inviteeId int, day string, rate rateDecision, holdingDays int) string {
	if holdingDays < 0 {
		holdingDays = 0
	}
	return SourceConsume + ":" + itoa(inviteeId) + ":" + day +
		":" + rate.Group + ":" + itoa(rate.Units) +
		":h" + itoa(holdingDays) +
		":u" + itoa(inviterId)
}

func topupIdemKey(tradeNo string) string { return SourceTopup + ":" + strings.TrimSpace(tradeNo) }

func redemptionIdemKey(id int) string { return SourceRedemption + ":" + itoa(id) }
