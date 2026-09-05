package commission

import (
	"context"
	"strconv"
	"strings"
	"sync"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/qianye/config"
	"github.com/QuantumNous/new-api/qianye/db"
	"github.com/QuantumNous/new-api/qianye/guard"
	qymodel "github.com/QuantumNous/new-api/qianye/model"
	"github.com/QuantumNous/new-api/qianye/modules/invite"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// settingScope 是本模块在共享 qy_settings 表里的命名空间。
const settingScope = "commission"

// 运营可改的键。YAML 承载启动级配置,这里承载"上线后要频繁调"的参数 ——
// 改费率不该需要重启进程,但也绝不能让它落进上游主库的 options 表。
const (
	// 费率键的值是**百分比字符串**("10"、"10.25"):运营在 qy_settings 里看到的
	// 也是百分比,不需要再心算万分比。YAML 里同一档是万分比整数,两者同尺度。
	keyTopupRatePercent   = "topup_rate_percent"
	keyConsumeRatePercent = "consume_rate_percent"

	// keyRedemptionRatePercent 是兑换码那一档,与上面两个键**语义不同**:
	// 它是可空的。空串(或这一行根本不存在)= 没单独配 = 跟随充值档,
	// "0" 才是显式 0%。理由见 config.Commission.RedemptionRateBps。
	keyRedemptionRatePercent = "redemption_rate_percent"

	keyMinSettleStardust   = "min_settle_stardust"
	keyMaxPerOrderStardust = "max_per_order_stardust"
	keyHoldingDays         = "holding_days"
	keyMinCreditStardust   = "min_credit_stardust"
	keyDailyCapStardust    = "max_daily_stardust_per_inviter"
	keyLargeAlertStardust  = "large_accrual_alert_stardust"
	keyMinInviteeAgeHour   = "min_invitee_age_hours"
)

// editableKeys 限定管理端可写的键。白名单而非黑名单:
// 让管理端能往共享 KV 表里写任意键,等于把别的模块的配置面也交出去了。
var editableKeys = []string{
	keyTopupRatePercent, keyConsumeRatePercent, keyRedemptionRatePercent,
	keyMinSettleStardust, keyMaxPerOrderStardust, keyHoldingDays, keyMinCreditStardust,
	keyDailyCapStardust, keyLargeAlertStardust, keyMinInviteeAgeHour,
}

// percentKeys 是取值为百分比字符串的键,其余键一律是整数。
func isPercentKey(k string) bool {
	return k == keyTopupRatePercent || k == keyConsumeRatePercent ||
		isNullablePercentKey(k)
}

// isNullablePercentKey 是百分比键里**允许为空**的那一部分。
//
// 空在这里不是"没填",而是一个有含义的取值:"这一档没单独配,跟随充值档"。
// 单独列出来是因为写入路径要为它分叉 —— 其余百分比键收到空串必须 400
// (那是运营把输入框清空了),而这些键收到空串要去**删掉**那一行覆盖。
func isNullablePercentKey(k string) bool {
	return k == keyRedemptionRatePercent
}

// opSettings 是 YAML 与运营覆盖合并后的生效配置。
type opSettings struct {
	// TopupRateUnits / ConsumeRateUnits 是内部整数费率:万分比(10.25% = 1025)。
	// 对外(接口、界面)一律换算回百分比,对内一律整数 —— 资金计算不接受浮点误差。
	TopupRateUnits   int
	ConsumeRateUnits int
	// RedemptionRateUnits 是兑换码那一档,**指针**:nil = 没单独配。
	//
	// 不能用 int 的 0 表示"没配":0% 是一个合法且常见的运营配置(兑换码
	// 多用于活动赠送,不想为它付佣金)。用 0 兼任"没配"的话,兑换码计佣会
	// 静默清零,而账本上看不出任何异常 —— 每一行都自洽,只是费率变了。
	RedemptionRateUnits *int
	MinSettleStardust   int64
	MaxPerOrderStardust int64
	HoldingDays         int
	// MinCreditStardust 是自动入账门槛:可用余额达到它才开资金单。
	MinCreditStardust  int64
	DailyCapStardust   int64 // 0 = 不限
	LargeAlertStardust int64 // 0 = 不告警
	MinInviteeAgeHours int   // 0 = 不限
}

// TopupRatePercent / ConsumeRatePercent 是下发给接口与前端的百分比形式。
func (s opSettings) TopupRatePercent() string   { return config.FormatRatePercent(s.TopupRateUnits) }
func (s opSettings) ConsumeRatePercent() string { return config.FormatRatePercent(s.ConsumeRateUnits) }

// RedemptionRatePercent 是**配的是什么**:没单独配时返回空串,而不是充值档的值。
// 接口回显与审计快照都用它 —— 把回落值当成配置值显示,运营下一次保存就会把
// "跟随"固化成一个显式数字,从此改充值档不再带动兑换码。
func (s opSettings) RedemptionRatePercent() string {
	if s.RedemptionRateUnits == nil {
		return ""
	}
	return config.FormatRatePercent(*s.RedemptionRateUnits)
}

// EffectiveRedemptionRateUnits 是**实际按几个点算**:没单独配时跟随充值档。
//
// 它只回答全局这一层。分组那一层的覆盖在 resolveRate 里,那里的顺序是
// 分组兑换码档 → 这里 → 分组充值档 → 全局充值档。
func (s opSettings) EffectiveRedemptionRateUnits() int {
	if s.RedemptionRateUnits == nil {
		return s.TopupRateUnits
	}
	return *s.RedemptionRateUnits
}

// EffectiveRedemptionRatePercent 是上面那个数的百分比形式,给接口与界面回显。
func (s opSettings) EffectiveRedemptionRatePercent() string {
	return config.FormatRatePercent(s.EffectiveRedemptionRateUnits())
}

// settingsCacheSeconds 是运营配置的刷新周期。
//
// 刻意不起后台协程去刷:那样每个节点都要一个裸 goroutine,而配置只在
// 非热路径(异步 worker、结算任务、HTTP handler)被读到,惰性刷新足够。
const settingsCacheSeconds = 60

var (
	settingsMu     sync.Mutex
	settingsCache  *opSettings
	settingsLoaded int64
	// settingsEpoch 是缓存的代次,每次失效自增。
	//
	// 查库放到临界区之外之后,"SELECT 返回"与"写回缓存"之间就出现了一个窗口:
	// 管理员在这中间改了费率并调 invalidateSettings(),在途的旧快照会把它
	// 静默盖掉,此后 60 秒全按旧费率计佣,而 RateUnits 会被冻结进 accrual 行,
	// 与合法行长得一模一样、事后无法区分。代次让写回方能发现"我读的那一版
	// 已经作废了"并丢弃本次结果。
	settingsEpoch uint64
)

// effective 返回当前生效的运营配置。禁止在 relay 线程调用 —— 它可能查库。
//
// 给拿不到调用方 ctx 的调用点用(结算任务、计佣写入路径)。它自带一个冷路径
// 预算而不是裸查:裸查会一直等到扩展库 DSN 的 readTimeout(默认 30 秒),
// 而调用点里就有正持着 qy_commission_balance 行锁的结算事务。
// 能拿到 ctx 的地方(HTTP 处理器)一律改调 effectiveCtx。
func effective() opSettings {
	ctx, cancel := guard.ColdContext(context.Background())
	defer cancel()
	return effectiveCtx(ctx)
}

// effectiveCtx 是接调用方预算的形式。
//
// 查库这一步刻意放在 settingsMu 的临界区之外。持锁查库时,一条慢 SELECT 会把
// 所有读配置的协程 —— 结算 worker、用户端"我的推广"、管理端健康面板 ——
// 串在同一把互斥锁上,一次行锁等待就能让整条计佣链路停摆。
//
// 代价是并发首次加载时可能有几个协程各查一次 qy_settings(几行的表),比把
// 它们排成一队便宜得多。刻意不用 singleflight:合并执行时用的是首个调用方的
// ctx,而这里的调用方预算从 HTTP 的几秒到后台任务的分钟级都有,一个用户按下
// 取消不该连累结算任务读不到费率。
func effectiveCtx(ctx context.Context) opSettings {
	base := opSettings{}
	cm := config.Get().Commission
	base.TopupRateUnits = configRateUnits(cm.TopupRateBps)
	base.ConsumeRateUnits = configRateUnits(cm.ConsumeRateBps)
	base.RedemptionRateUnits = configNullableRateUnits(cm.RedemptionRateBps)
	base.MinSettleStardust = cm.MinSettleStardust
	base.MaxPerOrderStardust = cm.MaxPerOrderStardust
	base.HoldingDays = cm.HoldingDays
	base.MinCreditStardust = cm.MinCreditStardust

	settingsMu.Lock()
	if settingsCache != nil && common.GetTimestamp()-settingsLoaded < settingsCacheSeconds {
		merged := *settingsCache
		settingsMu.Unlock()
		return merged
	}
	epoch := settingsEpoch
	settingsMu.Unlock()

	overrides, err := loadOverrides(ctx)
	if err != nil {
		// 读不到覆盖值时退回上一份快照、再退回 YAML,而不是让计佣停摆:
		// 少一个运营微调远比"整条计佣链路挂掉"轻。但必须留痕 —— 降级算出来的
		// 佣金和正常佣金在流水上长得一模一样,不计数事后就无从复核。
		settingsDegrade.noteCtx(ctx, "读取运营配置失败: "+err.Error())
		settingsMu.Lock()
		defer settingsMu.Unlock()
		if settingsCache != nil {
			return *settingsCache
		}
		return base
	}
	applyOverrides(&base, overrides)
	settingsMu.Lock()
	// 代次变了说明这份快照在途期间已经被 invalidateSettings 作废(管理端改了
	// 费率并提交)。此时只把结果返给本次调用方,绝不写回缓存 —— 写回等于把
	// 一次已经生效的调价按回去,而且会盖上一个新鲜的时间戳,让后续 60 秒
	// 都读不到真值。
	if settingsEpoch == epoch {
		settingsCache = &base
		settingsLoaded = common.GetTimestamp()
	}
	settingsMu.Unlock()
	return base
}

// cacheKindSettings / cacheKindGroupRate 是本包两把进程内缓存在跨节点失效通道
// (invite/cachesync.go)里的类别名。通道由 invite 代为广播与分发,本包在
// InstallHooks 里登记"收到这一类时清什么"。
const (
	cacheKindSettings  = "cm_settings"
	cacheKindGroupRate = "cm_group_rate"
)

// invalidateSettings 在管理端改配置后立即失效缓存,避免"改完 60 秒还没生效"的困惑。
// 同时广播给其它节点 —— 否则"立即生效"只对收到这次请求的那一个进程成立。
func invalidateSettings() {
	invalidateSettingsLocal()
	invite.PublishInvalidation(cacheKindSettings, 0)
}

// invalidateSettingsLocal 只清本进程,供跨节点通道重放远端流水时使用。
func invalidateSettingsLocal() {
	settingsMu.Lock()
	settingsCache = nil
	settingsLoaded = 0
	settingsEpoch++
	settingsMu.Unlock()
}

// loadOverrides 读出本模块在 qy_settings 里的全部运营覆盖。
//
// 必须接 ctx:它是调用方预算的唯一着力点,也是熔断可用性探针(db.WithOpProbe)
// 唯一认得的形式 —— 没接 ctx 的语句既没有超时保护,也没资格给熔断投健康票。
func loadOverrides(ctx context.Context) (map[string]string, error) {
	gdb := db.Get()
	if gdb == nil {
		return nil, db.ErrNotReady
	}
	var rows []qymodel.Setting
	if err := gdb.WithContext(ctx).Where("scope = ?", settingScope).Find(&rows).Error; err != nil {
		db.MarkFailure(err)
		return nil, err
	}
	m := make(map[string]string, len(rows))
	for _, r := range rows {
		m[r.K] = r.V
	}
	return m, nil
}

// configRateUnits 把 YAML 里的万分比整数收进内部费率。
//
// 越界一律回落 0 而不是钳到边界:validate 已经在启动时拦下了非法值,这里再见到
// 只可能是零值 Config(未加载配置的测试、扩展未启用),按 0 计佣是安全的一侧 ——
// 不计佣可以被用户投诉发现并补发,按猜出来的费率多发出去的钱是收不回来的。
func configRateUnits(bps int) int {
	if bps < 0 || bps > config.MaxRateUnits {
		return 0
	}
	return bps
}

// configNullableRateUnits 收 YAML 里**可空**的万分比字段(目前只有兑换码档)。
// nil 一律原样返回:那是"没单独配,跟随充值档"的唯一写法。
func configNullableRateUnits(bps *int) *int {
	if bps == nil {
		return nil
	}
	units := configRateUnits(*bps)
	return &units
}

// isAmountKey 说出哪些可编辑项是"星屑数"。写入校验与读取回落共用它,
// 因此"界面能填的"与"库里能生效的"永远是同一个区间。
func isAmountKey(key string) bool {
	switch key {
	case keyMinSettleStardust, keyMaxPerOrderStardust, keyMinCreditStardust, keyDailyCapStardust, keyLargeAlertStardust:
		return true
	}
	return false
}

func applyOverrides(s *opSettings, m map[string]string) {
	rateOverride(&s.TopupRateUnits, m[keyTopupRatePercent])
	rateOverride(&s.ConsumeRateUnits, m[keyConsumeRatePercent])
	nullableRateOverride(&s.RedemptionRateUnits, m[keyRedemptionRatePercent])
	amountOverride(&s.MinSettleStardust, keyMinSettleStardust, m[keyMinSettleStardust])
	amountOverride(&s.MaxPerOrderStardust, keyMaxPerOrderStardust, m[keyMaxPerOrderStardust])
	intOverride(&s.HoldingDays, m[keyHoldingDays])
	amountOverride(&s.MinCreditStardust, keyMinCreditStardust, m[keyMinCreditStardust])
	amountOverride(&s.DailyCapStardust, keyDailyCapStardust, m[keyDailyCapStardust])
	amountOverride(&s.LargeAlertStardust, keyLargeAlertStardust, m[keyLargeAlertStardust])
	intOverride(&s.MinInviteeAgeHours, m[keyMinInviteeAgeHour])
}

// rateOverride 应用运营覆盖的费率(百分比字符串)。
//
// 越界值一律丢弃而不是钳到边界:qy_settings 是可以被人手工 UPDATE 的,
// 一个被写坏的 999999 若被钳成 100% 就会静默地按全额计佣,而丢弃只是
// 回落到 YAML 的默认费率,损失有界且可解释。
func rateOverride(dst *int, percentRaw string) {
	if strings.TrimSpace(percentRaw) == "" {
		return
	}
	v, err := config.RatePercentUnits(percentRaw)
	if err != nil {
		warnf("qy_settings 里的计佣比例 %q 非法,已忽略: %v", percentRaw, err)
		return
	}
	*dst = v
}

// nullableRateOverride 应用运营覆盖的**兑换码档**费率。
//
// 空值(行不存在,或者行存在但 v 为空)一律**不动 dst**,让 YAML 的取值说了算,
// 而 YAML 的 nil 本身就是"跟随"。绝不能在这里把空写成 0:那会把"运营删掉了
// 这条覆盖"变成"运营把兑换码计佣设成了 0%"。
func nullableRateOverride(dst **int, percentRaw string) {
	if strings.TrimSpace(percentRaw) == "" {
		return
	}
	v, err := config.RatePercentUnits(percentRaw)
	if err != nil {
		warnf("qy_settings 里的兑换码计佣比例 %q 非法,已忽略: %v", percentRaw, err)
		return
	}
	*dst = &v
}

func intOverride(dst *int, raw string) {
	if raw == "" {
		return
	}
	if v, err := strconv.Atoi(raw); err == nil && v >= 0 {
		*dst = v
	}
}

// amountOverride 应用一条运营覆盖的**额度**门槛。
//
// 上界是 common.MaxQuota —— 全站额度换算的算术上界,见 common/quota_math.go。
// 越界一律丢弃并告警,不钳到边界 —— 与 rateOverride 同一条理由。
//
// 为什么读取侧也要卡:写入侧的 400 只挡住管理端这一条路。库里已经存着的
// 越界值(手工 UPDATE、以后可能出现的其它写入方)不会因为今天加了个校验就消失,
// 而它造成的是「全站佣金永远不再落账」这种无声故障。
func amountOverride(dst *int64, key, raw string) {
	if raw == "" {
		return
	}
	v, err := strconv.ParseInt(strings.TrimSpace(raw), 10, 64)
	if err != nil || v < 0 {
		return
	}
	if v > int64(common.MaxQuota) {
		warnf("qy_settings 里的 commission.%s = %d 超出星屑上限 %d,已忽略并回落默认值 —— "+
			"这类门槛一旦超过上限就永远无法被满足(结算金额本身被夹在上限内)",
			key, v, common.MaxQuota)
		return
	}
	*dst = v
}

// writeSetting 在给定事务内落一条运营覆盖。
//
// 接 tx 而不是自取 db.Get():一次批量保存里的多个键必须要么全生效要么全不生效。
// 逐条自取连接时,第一个键写成功、第二个撞上死锁,库里就留下了一个谁都没有
// 批准的中间费率组合 —— 而所有节点会在 settingsCacheSeconds 内开始按它计佣。
//
// 调用方负责写审计,成功与失败都要写 —— 费率变更必须可追溯到人。
func writeSetting(tx *gorm.DB, key, value string, operatorId int) error {
	if tx == nil {
		return db.ErrNotReady
	}
	row := qymodel.Setting{
		Scope:      settingScope,
		K:          key,
		V:          value,
		OperatorId: operatorId,
		UpdatedAt:  common.GetTimestamp(),
	}
	err := tx.Clauses(clause.OnConflict{
		Columns:   []clause.Column{{Name: "scope"}, {Name: "k"}},
		DoUpdates: clause.AssignmentColumns([]string{"v", "operator_id", "updated_at"}),
	}).Create(&row).Error
	if err != nil {
		db.MarkFailure(err)
	}
	return err
}

// deleteSettingTx 在给定事务内清掉一条运营覆盖,让该项回落到 YAML。
//
// 它清的是**运营本次要改的那一项**(把兑换码档从显式数字改回「跟随充值档」),
// 它和同批次其它键必须同生共死 —— 一半生效的费率组合是谁都没有批准过的。
// 删不到行不是错误:本来就没有覆盖,与删掉了覆盖是同一个终态。
func deleteSettingTx(tx *gorm.DB, key string) error {
	if tx == nil {
		return db.ErrNotReady
	}
	err := tx.Where("scope = ? AND k = ?", settingScope, key).Delete(&qymodel.Setting{}).Error
	if err != nil {
		db.MarkFailure(err)
	}
	return err
}

// ───────────────────────── 降级留痕 ─────────────────────────

// degradeRecord 记录一类"配置读不到,于是按默认口径继续算钱"的降级。
//
// 回落本身是对的:停止计佣比少一个运营微调糟得多。问题在于降级算出来的那批
// 账目与正常账目在流水上长得一模一样 —— 事后无法区分"这一行是降级"还是
// "当时配的就是这个费率"。计数 + 最近一次时间与原因经管理端健康接口暴露,
// 运营至少能知道"那段时间的佣金要复核"。
type degradeRecord struct {
	mu       sync.Mutex
	count    int64
	lastAt   int64
	lastWarn int64
	reason   string
}

// degradeWarnThrottleSeconds 限制降级告警的打印频率。降级往往意味着扩展库整体
// 不可用,每条计佣事件都打一行会把日志淹掉,反而看不见。
const degradeWarnThrottleSeconds = 60

var (
	// settingsDegrade 计"运营配置读不到,本次按上一份快照或 YAML 默认费率计佣"。
	settingsDegrade = &degradeRecord{}

	// groupRateDegrade 计"分组费率读不到,本次按全局默认费率计佣"。
	// 消费方是 grouprate.go 的 groupRates(),它两条**返回空表**的回落路径
	// 各上报一次(沿用旧快照不上报 —— 那是缓存的正常语义,费率仍然是对的)。
	groupRateDegrade = &degradeRecord{}

	// inviterGroupDegrade 计"主库读不到**上线**那一行,本次费率跳过分组层"。
	// 消费方是 pricing.go 的 resolveInviterPricing。
	//
	// 单独一个计数器:另外两个说的是"配置读不到",这一个说的是"人读不到",
	// 运营看健康面板要能一眼分清"是扩展库挂了"还是"主库挂了"。
	// 它也是这条路上唯一的痕迹:降级那一批 accrual 行的 rate_group 是空串。
	inviterGroupDegrade = &degradeRecord{}
)

// degradeSilenceKey 标记"这一次解析是**展示**,不是计佣"。
//
// 用户端「我的推广」页走与计佣同一条解析路径,并且对同一个人连解析三次。
// 不静默的话任何一个已登录用户按住 F5 就能把降级计数器按 3 倍速率推上去,
// 「要复核的佣金区间」变成了「用户刷新页面的次数」的函数。修法不是让展示路径
// 复刻一份判定,而是让它**走同一条路但闭嘴**。判据挂在 ctx 上而不是加参数,
// 是为了让 resolveRate / groupRates 的签名不变。
type degradeSilenceKey struct{}

// silentDegradeCtx 把 ctx 标成"只读展示"。只有展示路径可以用它:
// 任何会把结果冻结进账本的路径都必须让降级被记下来。
func silentDegradeCtx(ctx context.Context) context.Context {
	return context.WithValue(ctx, degradeSilenceKey{}, true)
}

func degradeSilenced(ctx context.Context) bool {
	if ctx == nil {
		return false
	}
	v, _ := ctx.Value(degradeSilenceKey{}).(bool)
	return v
}

// noteCtx 是接调用方 ctx 的上报形式:展示路径静默,计佣路径照常计数。
//
// 零值口径:ctx 上没有这个标记 = 计佣路径 = 照常上报。漏加标记只会让计数器多计,
// 不会让一次真实降级被吞掉 —— 这是两个方向里安全的那一边。
func (d *degradeRecord) noteCtx(ctx context.Context, reason string) {
	if degradeSilenced(ctx) {
		return
	}
	d.note(reason)
}

func (d *degradeRecord) note(reason string) {
	now := common.GetTimestamp()
	d.mu.Lock()
	d.count++
	d.lastAt = now
	d.reason = reason
	count := d.count
	shouldWarn := now-d.lastWarn >= degradeWarnThrottleSeconds
	if shouldWarn {
		d.lastWarn = now
	}
	d.mu.Unlock()
	if shouldWarn {
		warnf("配置降级,本次按默认口径计佣(累计 %d 次,期间的佣金需复核): %s", count, reason)
	}
}

func (d *degradeRecord) stats() map[string]any {
	d.mu.Lock()
	defer d.mu.Unlock()
	return map[string]any{"count": d.count, "last_at": d.lastAt, "last_reason": d.reason}
}
