package commission

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/qianye/config"
	"github.com/QuantumNous/new-api/qianye/db"
	"github.com/QuantumNous/new-api/setting/operation_setting"

	"github.com/shopspring/decimal"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// rateUnitsDivisor 把内部整数费率还原成比例。
//
// 内部整数 = 百分比 × 100(10.25% → 1025),而百分比本身再除以 100 才是比例,
// 所以分母是 100 × 100 = 10000。这个数值恰好与旧的万分比分母相同 ——
// 对外从万分比改成百分比,并没有动内部的运算精度。
var rateUnitsDivisor = decimal.NewFromInt(config.RatePercentScale * 100)

// calcGross 计算一笔佣金的精确金额,永不截断。
//
// 这是本模块存在的核心理由。quota 是 int32,单次对话常见 10~500,
// 5% 的佣金是 0.5~25。用 int(float64(base)*rate) 会把 0.5 直接变成 0,
// 一天几千次请求全部归零,用户看到"用了一天没佣金"而钱被平台吞掉。
//
// rateUnits 是整数(百分比 × 100),除以 10000 在 decimal 下无精度损失
// (0.05、0.1025 都可精确表示)。费率从配置读进来的那一刻就已经是整数,
// 整条链路上没有任何一步经过 float64。
func calcGross(baseQuota int64, rateUnits int) decimal.Decimal {
	if baseQuota <= 0 || rateUnits <= 0 {
		return decimal.Zero
	}
	return decimal.NewFromInt(baseQuota).
		Mul(decimal.NewFromInt(int64(rateUnits))).
		Div(rateUnitsDivisor)
}

// capGross 对单笔佣金封顶。
//
// 作用在"单次计佣增量"上而不是日聚合行的总额:日聚合行会跨越一整天,
// 用它当上限等于给用户设了个日封顶,那是另一个语义(见 opSettings.DailyCapQuota)。
func capGross(gross decimal.Decimal, maxPerOrder int64) decimal.Decimal {
	if maxPerOrder <= 0 {
		return gross
	}
	limit := decimal.NewFromInt(maxPerOrder)
	if gross.GreaterThan(limit) {
		return limit
	}
	if gross.LessThan(limit.Neg()) {
		return limit.Neg()
	}
	return gross
}

// maxSafeAmount 是落库前的合理性闸门。decimal(30,10) 的容量是 10^20,
// 业务上不可达;真出现这种数字一定是上游数据坏了,宁可拒写也不能污染账本。
var maxSafeAmount = decimal.New(1, 19)

func amountSane(d decimal.Decimal) bool { return d.Abs().LessThan(maxSafeAmount) }

// normalizeIdemKey 把任意长度的业务键压进 varchar(96)。
//
// trade_no 在主库是 varchar(255),直接截断会让两个不同订单撞成同一个幂等键 ——
// 那意味着第二笔充值不返佣。超长时改用哈希,保证单射。
func normalizeIdemKey(raw string) string {
	if len(raw) <= 96 {
		return raw
	}
	sum := sha256.Sum256([]byte(raw))
	return "h:" + hex.EncodeToString(sum[:])[:64]
}

// bucketDate 返回消费日聚合的自然日键。
//
// 固定用 UTC:节点可能分布在不同时区,用本地时间会让同一笔消费在不同节点
// 落进不同的桶,唯一索引失效、行数翻倍。
func bucketDate(ts int64) string {
	return time.Unix(ts, 0).UTC().Format("20060102")
}

// bucketMatureAt 返回日聚合桶的成熟时间:整天结束之后再加成熟期。
//
// 不用"首次写入时间 + N 天":桶会持续增长到当天结束,用首次写入时间会让
// 当天晚些时候的消费提前成熟,削弱成熟期本来的防套利作用。
func bucketMatureAt(day string, holdingDays int) int64 {
	t, err := time.ParseInLocation("20060102", day, time.UTC)
	if err != nil {
		return common.GetTimestamp() + int64(holdingDays)*86400
	}
	return t.Unix() + 86400 + int64(holdingDays)*86400
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

// currentUsdRate 读取计佣当刻的汇率并冻结进 accrual 行。
//
// USDExchangeRate 是管理员可随时热改的全局变量。不冻结的话,改一次汇率
// 全部历史佣金的法币口径都会跟着变,财务对账永远对不上。
func currentUsdRate() decimal.Decimal {
	return decimal.NewFromFloat(operation_setting.USDExchangeRate)
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
	// RateUnits 是本次生效的费率(百分比 × 100),RateGroup 是它来自哪个
	// 被邀请人分组。两者一起冻结进行,事后才解释得清"这笔为什么是这个数"。
	RateUnits int
	RateGroup string
	Gross     decimal.Decimal

	// UsdRate 为零时取当前汇率。冲正必须显式传入原单的汇率,
	// 否则冲正与原单会按两个不同汇率折算法币,账永远平不了。
	UsdRate decimal.Decimal

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
	gdb = gdb.WithContext(ctx)
	if in.Gross.IsZero() {
		return false, nil
	}
	if !amountSane(in.Gross) {
		warnf("拒绝写入异常佣金金额 %s(来源 %s/%s)", in.Gross.String(), in.SourceType, in.IdemKey)
		return false, errors.New("commission: 佣金金额超出合理范围")
	}
	now := common.GetTimestamp()
	usdRate := in.UsdRate
	if usdRate.IsZero() {
		usdRate = currentUsdRate()
	}
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
		GrossAmount:  in.Gross,
		UsdRate:      usdRate,
		Status:       in.Status,
		RiskFlags:    truncate(in.RiskFlags, 255),
		MatureAt:     in.MatureAt,
		BucketDate:   in.BucketDate,
		RefAccrualId: in.RefAccrualId,
		Remark:       truncate(in.Remark, 255),
		CreatedAt:    now,
		UpdatedAt:    now,
	}

	conflict := clause.OnConflict{
		Columns:   []clause.Column{{Name: "idem_scope"}, {Name: "idem_key"}},
		DoNothing: true,
	}
	if in.Accumulate {
		// 日聚合桶:冲突即累加。这一步在数据库层原子完成,
		// 多节点同时 flush 同一个桶也不需要任何应用层锁。
		conflict = clause.OnConflict{
			Columns: []clause.Column{{Name: "idem_scope"}, {Name: "idem_key"}},
			DoUpdates: clause.Assignments(map[string]any{
				"base_quota":   gorm.Expr("qy_commission_accrual.base_quota + ?", in.BaseQuota),
				"gross_amount": gorm.Expr("qy_commission_accrual.gross_amount + ?", in.Gross),
				"updated_at":   now,
			}),
		}
	}

	res := gdb.Clauses(conflict).Create(&row)
	if res.Error != nil {
		db.MarkFailure(res.Error)
		accrualFailed.Add(1)
		return false, res.Error
	}
	// RowsAffected 口径(MySQL):新插入 1,ON DUPLICATE KEY UPDATE 命中 2,
	// DoNothing 命中 0。因此 == 1 在累加与非累加两种模式下都恰好是"新插入"。
	inserted := res.RowsAffected == 1
	if in.Accumulate {
		accrualAccumulated.Add(1)
	} else if inserted {
		accrualCreated.Add(1)
	}
	alertLargeAccrual(in)
	return inserted, nil
}

// alertLargeAccrual 对异常大额计佣告警。
//
// 单笔佣金突然跳到平时的百倍,要么是费率被改错,要么是有人在刷 ——
// 两种情况都必须让人立刻知道,而不是等月底对账才发现。
func alertLargeAccrual(in accrualInput) {
	limit := effective().LargeAlertQuota
	if limit <= 0 {
		return
	}
	if in.Gross.Abs().GreaterThan(decimal.NewFromInt(limit)) {
		warnf("单笔佣金 %s 超过告警阈值 %d(邀请人 %d, 下线 %d, 来源 %s)",
			in.Gross.String(), limit, in.InviterId, in.InviteeId, in.SourceType)
	}
}

// ───────────────────────── 邀请关系与风控快照 ─────────────────────────

// ensureRelation 在首次见到某个下线时落一行关系快照。
//
// 脱敏名在这里算好并缓存,列表页因此零主库访问 —— 邀请人永远拿不到
// 下线的真实用户名与邮箱。
func ensureRelation(ctx context.Context, inviterId, inviteeId int, rawName string, boundAt int64) {
	gdb := db.Get()
	if gdb == nil {
		return
	}
	gdb = gdb.WithContext(ctx)
	risk := ""
	blocked := false
	// 互邀环路:A 邀 B 且 B 邀 A,是最常见的双账号自刷手法。
	if peer, _, err := resolveInviter(ctx, inviterId); err == nil && peer.InviterId == inviteeId {
		risk = "reciprocal_invite"
		blocked = true
	}
	now := common.GetTimestamp()
	row := InviteRelation{
		InviteeId:  inviteeId,
		InviterId:  inviterId,
		MaskedName: truncate(MaskUsername(rawName), 64),
		InviteeRef: inviteeRef(inviteeId, refSalt()),
		BoundAt:    boundAt,
		RiskFlags:  risk,
		Blocked:    blocked,
		CreatedAt:  now,
		UpdatedAt:  now,
	}
	// 已存在就不动:管理员可能手工改过 Blocked,自动流程不该覆盖人的决定。
	if err := gdb.Clauses(clause.OnConflict{DoNothing: true}).Create(&row).Error; err != nil {
		db.MarkFailure(err)
	}
	if blocked {
		invalidateBlocked()
	}
}

var (
	blockedMu     sync.Mutex
	blockedSet    map[int]bool
	blockedLoaded int64
)

// blockedInvitees 返回被拉黑的邀请关系集合。
//
// 拉黑是极少数情形,整表拉进内存(每 60 秒刷一次)远比每条计佣事件
// 回一次库便宜。
func blockedInvitees(ctx context.Context) map[int]bool {
	blockedMu.Lock()
	defer blockedMu.Unlock()
	now := common.GetTimestamp()
	if blockedSet != nil && now-blockedLoaded < settingsCacheSeconds {
		return blockedSet
	}
	gdb := db.Get()
	if gdb == nil {
		if blockedSet != nil {
			return blockedSet
		}
		return map[int]bool{}
	}
	var ids []int
	if err := gdb.WithContext(ctx).Model(&InviteRelation{}).Where("blocked = ?", true).Pluck("invitee_id", &ids).Error; err != nil {
		db.MarkFailure(err)
		if blockedSet != nil {
			return blockedSet
		}
		return map[int]bool{}
	}
	m := make(map[int]bool, len(ids))
	for _, id := range ids {
		m[id] = true
	}
	blockedSet = m
	blockedLoaded = now
	return m
}

func invalidateBlocked() {
	blockedMu.Lock()
	blockedSet = nil
	blockedLoaded = 0
	blockedMu.Unlock()
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

// consumeIdemKey 是消费日聚合的幂等键。
//
// 除了(下线、自然日)之外还带上**冻结的费率与分组**,因为日聚合行是
// "边增长边结算"的:桶会跨越一整天,而这一天里下线可能换了分组、运营
// 可能调了费率。只按(下线、日期)聚合的话,后来的增量会按新费率算出
// gross 累加进一行标着旧费率的记录里,那一行从此 base × rate ≠ gross,
// 永远对不平,也没法向用户解释。
//
// 费率变了就落新的一行:唯一索引照样防重复,每一行仍然自洽。代价只是
// 改费率当天多出一行,而这正是账面上应该看得见的事实。
func consumeIdemKey(inviteeId int, day string, rate rateDecision) string {
	return SourceConsume + ":" + itoa(inviteeId) + ":" + day +
		":" + rate.Group + ":" + itoa(rate.Units)
}

func topupIdemKey(tradeNo string) string { return SourceTopup + ":" + strings.TrimSpace(tradeNo) }

func redemptionIdemKey(id int) string { return SourceRedemption + ":" + itoa(id) }
