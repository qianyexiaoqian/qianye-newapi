package commission

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"strconv"
	"strings"
	"sync"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/qianye/config"
	"github.com/QuantumNous/new-api/qianye/db"
	"github.com/QuantumNous/new-api/qianye/guard"
	qymodel "github.com/QuantumNous/new-api/qianye/model"

	"github.com/shopspring/decimal"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// settingScope 是本模块在共享 qy_settings 表里的命名空间。
const settingScope = "commission"

// 运营可改的键。YAML 承载启动级配置,这里承载"上线后要频繁调"的参数 ——
// 改费率不该需要重启进程,但也绝不能让它落进上游主库的 options 表。
const (
	// 费率键的值是**百分比字符串**("10"、"10.25"),与 YAML 同一种写法。
	// 运营直接在 qy_settings 里看到的也是百分比,不需要再心算万分比。
	keyTopupRatePercent   = "topup_rate_percent"
	keyConsumeRatePercent = "consume_rate_percent"

	// keyRedemptionRatePercent 是兑换码那一档,与上面两个键**语义不同**:
	// 它是可空的。空串(或这一行根本不存在)= 没单独配 = 跟随充值档,
	// "0" 才是显式 0%。理由见 config.Commission.RedemptionRatePercent。
	keyRedemptionRatePercent = "redemption_rate_percent"

	// keyFiatRateDefault 是「站内佣金余额 → 法币」折算比例的**兜底档**。
	//
	// 取值是十进制字符串("7.3"),不是百分比 —— 它是一个乘数,不是一个比率,
	// 所以它既不在 isPercentKey 里也不在 isQuotaKey 里,写入侧单独一支。
	// 层级、口径与零值语义见 fiatrate.go 的文件头。
	//
	// 与可空的兑换码档不同,这一行**不可被清空**:清空之后没配分组档的用户会
	// 悄悄退回充值页汇率,而界面上还写着兜底档。想要「兜底 = 充值汇率」就把那个
	// 数字显式填进来,让它成为一个有人签字的决定。
	keyFiatRateDefault = "fiat_rate_default"

	keyMinSettleQuota    = "min_settle_quota"
	keyMaxPerOrderQuota  = "max_per_order_quota"
	keyHoldingDays       = "holding_days"
	keyDailyCapQuota     = "max_daily_quota_per_inviter"
	keyLargeAlertQuota   = "large_accrual_alert_quota"
	keyMinInviteeAgeHour = "min_invitee_age_hours"
	keyRefSalt           = "invitee_ref_salt"

	// legacyKey* 是 1.x 写进 qy_settings 的万分比键。仍然读(否则升级即掉费率),
	// 但一旦新键被写入就顺手删掉:两个键长期并存的话,"到底哪个生效"
	// 迟早会有人猜错。
	legacyKeyTopupRateBps   = "topup_rate_bps"
	legacyKeyConsumeRateBps = "consume_rate_bps"
)

// editableKeys 限定管理端可写的键。白名单而非黑名单:
// 让管理端能往共享 KV 表里写任意键,等于把别的模块的配置面也交出去了。
var editableKeys = []string{
	keyTopupRatePercent, keyConsumeRatePercent, keyRedemptionRatePercent,
	keyFiatRateDefault,
	keyMinSettleQuota, keyMaxPerOrderQuota,
	keyHoldingDays, keyDailyCapQuota, keyLargeAlertQuota, keyMinInviteeAgeHour,
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

// isFiatRateKey 是取值为**法币折算比例**的键(目前只有兜底档)。
//
// 单独一支而不是塞进百分比那一支:它是一个乘数(7.3 = 一美元折 7.3 元),
// 不是一个 0..100 的比率,校验区间、上界、小数位数与百分比全不一样。
// 混进去的后果是运营在界面上看到一个写着 "%" 的输入框,填 7.3 之后
// 佣金按 7.3% 折算 —— 一个差了两个数量级的资金参数。
func isFiatRateKey(k string) bool { return k == keyFiatRateDefault }

// opSettings 是 YAML 与运营覆盖合并后的生效配置。
type opSettings struct {
	// TopupRateUnits / ConsumeRateUnits 是内部整数费率:百分比 × 100
	// (10.25% = 1025)。对外(YAML、接口、界面)一律换算回百分比,
	// 对内一律整数 —— 资金计算不接受浮点误差。
	TopupRateUnits   int
	ConsumeRateUnits int
	// RedemptionRateUnits 是兑换码那一档,**指针**:nil = 没单独配。
	//
	// 不能用 int 的 0 表示"没配":0% 是一个合法且常见的运营配置(兑换码
	// 多用于活动赠送,不想为它付佣金)。用 0 兼任"没配"的话,升级那一刻
	// 全站兑换码返佣会静默清零,而账本上看不出任何异常 —— 每一行都自洽,
	// 只是费率变了。这正是本仓栽过多次的零值陷阱。
	RedemptionRateUnits *int
	// FiatRateDefault 是法币折算比例的兜底档,**指针**:nil = 从未配过,
	// 此时回落全站充值汇率(fiatrate.go 的第 3 层)。
	//
	// 不能用零值兼任"没配":0 会让 applyFiat 一分法币都不加而额度照加,
	// available_fiat 与 available_quota 就此永久漂移。0 在这一档是非法值,
	// 不是一个含义。存量站点升级上来一律是 nil,行为一分不变。
	FiatRateDefault    *decimal.Decimal
	MinSettleQuota     int64
	MaxPerOrderQuota   int64
	HoldingDays        int
	DailyCapQuota      int64 // 0 = 不限
	LargeAlertQuota    int64 // 0 = 不告警
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

// FiatRateDefaultString 是**配的是什么**:没配过时返回空串,而不是全站汇率。
//
// 与 RedemptionRatePercent 同一条理由:把回落值当成配置值回显,运营下一次
// 保存就把"跟随全站汇率"固化成一个显式数字,此后改充值汇率不再带动佣金折算 ——
// 一次什么都没改的保存,静默改变了系统行为。
func (s opSettings) FiatRateDefaultString() string {
	if s.FiatRateDefault == nil {
		return ""
	}
	return s.FiatRateDefault.String()
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

	saltOnce  sync.Mutex
	saltCache string
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
// 串在同一把互斥锁上,一次行锁等待就能让整条返佣链路停摆。
//
// 代价是并发首次加载时可能有几个协程各查一次 qy_settings(几行的表),比把
// 它们排成一队便宜得多。刻意不用 singleflight:合并执行时用的是首个调用方的
// ctx(见 inviter.go 对同一取舍的说明),而这里的调用方预算从 HTTP 的几秒到
// 后台任务的分钟级都有,一个用户按下取消不该连累结算任务读不到费率。
func effectiveCtx(ctx context.Context) opSettings {
	base := opSettings{}
	cm := config.Get().Commission
	base.TopupRateUnits = configRateUnits("topup_rate_percent", cm.TopupRatePercent)
	base.ConsumeRateUnits = configRateUnits("consume_rate_percent", cm.ConsumeRatePercent)
	base.RedemptionRateUnits = configNullableRateUnits(
		"redemption_rate_percent", cm.RedemptionRatePercent)
	base.MinSettleQuota = cm.MinSettleQuota
	base.MaxPerOrderQuota = cm.MaxPerOrderQuota
	base.HoldingDays = cm.HoldingDays

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
		// 少一个运营微调远比"整条返佣链路挂掉"轻。但必须留痕 —— 降级算出来的
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

// invalidateSettings 在管理端改配置后立即失效缓存,避免"改完 60 秒还没生效"的困惑。
// 同时广播给其它节点 —— 否则"立即生效"只对收到这次请求的那一个进程成立。
func invalidateSettings() {
	invalidateSettingsLocal()
	publishInvalidation(cacheKindSettings, 0)
}

// invalidateSettingsLocal 只清本进程,供 cachesync 重放远端流水时使用。
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

// configRateUnits 把 YAML 里的百分比字符串换算成内部整数费率。
//
// 空串按 0 处理且不告警:零值 Config(未加载配置的测试、扩展未启用)走这条路,
// 刷屏没有意义。真正写错了值才告警,并且一律回落 0 —— 不计佣可以被用户投诉
// 发现并补发,按猜出来的费率多发出去的钱是收不回来的。
func configRateUnits(field, raw string) int {
	if raw == "" {
		return 0
	}
	units, err := config.RatePercentUnits(raw)
	if err != nil {
		warnf("commission.%s = %q 无法解析,本次按 0 计佣: %v", field, raw, err)
		return 0
	}
	return units
}

// configNullableRateUnits 换算 YAML 里**可空**的百分比字段(目前只有兑换码档)。
//
// 空串一律返回 nil 而不是 0:那是"没单独配,跟随充值档"的唯一写法,
// 也是每一个存量站点升级上来的样子。写错了值同样返回 nil —— 与
// configRateUnits 回落 0 的取舍不同,这里回落到"跟随充值档"才是保持
// 存量行为,而按 0 计佣等于替一个填错格式的运营做出"兑换码不返佣"的决定。
func configNullableRateUnits(field, raw string) *int {
	if strings.TrimSpace(raw) == "" {
		return nil
	}
	units, err := config.RatePercentUnits(raw)
	if err != nil {
		warnf("commission.%s = %q 无法解析,本次按「跟随充值档」处理: %v", field, raw, err)
		return nil
	}
	return &units
}

// isQuotaKey 说出哪些可编辑项是"额度"。写入校验与读取回落共用它,
// 因此"界面能填的"与"库里能生效的"永远是同一个区间。
func isQuotaKey(key string) bool {
	switch key {
	case keyMinSettleQuota, keyMaxPerOrderQuota, keyDailyCapQuota, keyLargeAlertQuota:
		return true
	}
	return false
}

func applyOverrides(s *opSettings, m map[string]string) {
	rateOverride(&s.TopupRateUnits, m[keyTopupRatePercent], m[legacyKeyTopupRateBps])
	rateOverride(&s.ConsumeRateUnits, m[keyConsumeRatePercent], m[legacyKeyConsumeRateBps])
	nullableRateOverride(&s.RedemptionRateUnits, m[keyRedemptionRatePercent])
	fiatRateOverride(&s.FiatRateDefault, m[keyFiatRateDefault])
	quotaOverride(&s.MinSettleQuota, keyMinSettleQuota, m[keyMinSettleQuota])
	quotaOverride(&s.MaxPerOrderQuota, keyMaxPerOrderQuota, m[keyMaxPerOrderQuota])
	intOverride(&s.HoldingDays, m[keyHoldingDays])
	quotaOverride(&s.DailyCapQuota, keyDailyCapQuota, m[keyDailyCapQuota])
	quotaOverride(&s.LargeAlertQuota, keyLargeAlertQuota, m[keyLargeAlertQuota])
	intOverride(&s.MinInviteeAgeHours, m[keyMinInviteeAgeHour])
}

// rateOverride 应用运营覆盖的费率。
//
// 优先百分比键;没有才回落 1.x 的万分比键 —— 升级之后运营还没重新保存过
// 配置时,库里只有旧键,不读它就等于升级即掉费率。两种写法在数值上同尺度
// (百分比 × 100 = 万分之一),只是解析方式不同。
//
// 越界值一律丢弃而不是钳到边界:qy_settings 是可以被人手工 UPDATE 的,
// 一个被写坏的 999999 若被钳成 100% 就会静默地按全额返佣,而丢弃只是
// 回落到 YAML 的默认费率,损失有界且可解释。
func rateOverride(dst *int, percentRaw, legacyBpsRaw string) {
	if strings.TrimSpace(percentRaw) != "" {
		if v, err := config.RatePercentUnits(percentRaw); err == nil {
			*dst = v
		} else {
			warnf("qy_settings 里的返佣比例 %q 非法,已忽略: %v", percentRaw, err)
		}
		return
	}
	if strings.TrimSpace(legacyBpsRaw) == "" {
		return
	}
	v, err := strconv.Atoi(strings.TrimSpace(legacyBpsRaw))
	if err != nil || v < 0 || v > config.MaxRateUnits {
		warnf("qy_settings 里的旧版返佣比例 %q 非法,已忽略", legacyBpsRaw)
		return
	}
	*dst = v
}

// nullableRateOverride 应用运营覆盖的**兑换码档**费率。
//
// 与 rateOverride 的两处不同,都是零值陷阱逼出来的:
//
//  1. 没有 1.x 万分比旧键要兼容 —— 这一档是新增的,1.x 根本没有它。
//  2. 空值(行不存在,或者行存在但 v 为空)一律**不动 dst**,让 YAML 的取值
//     说了算,而 YAML 的空串本身就是 nil。绝不能在这里把空写成 0:那会把
//     "运营删掉了这条覆盖"变成"运营把兑换码返佣设成了 0%"。
//
// 越界/非法值同样丢弃并告警,理由与 rateOverride 逐字相同:qy_settings 是
// 可以被人手工 UPDATE 的,钳到边界会静默生效一个谁都没批准的费率。
func nullableRateOverride(dst **int, percentRaw string) {
	if strings.TrimSpace(percentRaw) == "" {
		return
	}
	v, err := config.RatePercentUnits(percentRaw)
	if err != nil {
		warnf("qy_settings 里的兑换码返佣比例 %q 非法,已忽略: %v", percentRaw, err)
		return
	}
	*dst = &v
}

// fiatRateOverride 应用运营覆盖的**法币折算兜底档**。
//
// 空值(行不存在,或者行存在但 v 为空)一律不动 dst —— 那是"从未配过",
// 由 fiatRateFor 回落全站充值汇率。非法值同样丢弃并告警,与 rateOverride /
// quotaOverride 逐字同一条理由:qy_settings 是可以被人手工 UPDATE 的,
// 钳到边界会静默生效一个谁都没批准的比例,而丢弃只是回落到下一层。
//
// 尤其不能把非法值钳成 0:那会让 applyFiat 一分法币都不加而额度照加,
// 是本档唯一一种"不报错、不告警、账本自己慢慢漂"的失败形状。
func fiatRateOverride(dst **decimal.Decimal, raw string) {
	if strings.TrimSpace(raw) == "" {
		return
	}
	v, err := parseFiatRate(raw)
	if err != nil {
		warnf("qy_settings 里的兜底法币比例 %q 非法,已忽略: %v", raw, err)
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

// quotaOverride 应用一条运营覆盖的**额度**门槛。
//
// 上界是 common.MaxQuota —— 全站额度换算的算术上界,见 common/quota_math.go。
// 越界一律丢弃并告警,不钳到边界 —— 与 rateOverride 同一条理由:
// qy_settings 是可以被人手工 UPDATE 的,钳到边界会静默生效一个谁都没批准的值,
// 而丢弃只是回落到 YAML 默认,损失有界且日志里说得清。
//
// 为什么读取侧也要卡:写入侧的 400 只挡住管理端这一条路。库里已经存着的
// 越界值(历史数据、手工 UPDATE、以后可能出现的其它写入方)不会因为
// 今天加了个校验就消失,而它造成的是「全站佣金永远不再落账」这种无声故障。
func quotaOverride(dst *int64, key, raw string) {
	if raw == "" {
		return
	}
	v, err := strconv.ParseInt(strings.TrimSpace(raw), 10, 64)
	if err != nil || v < 0 {
		return
	}
	if v > int64(common.MaxQuota) {
		warnf("qy_settings 里的 commission.%s = %d 超出主库额度上限 %d,已忽略并回落默认值 —— "+
			"这类门槛一旦超过 int32 上限就永远无法被满足(结算金额本身被夹在 int32 内)",
			key, v, common.MaxQuota)
		return
	}
	*dst = v
}

// writeSetting 在给定事务内落一条运营覆盖。
//
// 接 tx 而不是自取 db.Get():一次批量保存里的多个键必须要么全生效要么全不生效。
// 逐条自取连接时,第一个键写成功、第二个撞上死锁,库里就留下了一个谁都没有
// 批准的中间费率组合 —— 而所有节点会在 settingsCacheSeconds 内开始按它计佣,
// 接口那边只回了一个 500。这不是理论风险:死锁不是连接级错误,熔断不会开,
// 运营看到 500 会直接重试。
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
// 与 dropSetting 的区别不只是句柄:那个函数服务的是"新键写好了,顺手清掉
// 同义的旧键",失败无伤大雅所以刻意在事务外;这里清的是**运营本次要改的那一项**
// (把兑换码档从显式数字改回「跟随充值档」),它和同批次其它键必须同生共死 ——
// 一半生效的费率组合是谁都没有批准过的。
//
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

// dropSetting 删除一条运营覆盖。
//
// 只服务于"新键写入后清掉同义的旧键"这一个场景:两个语义相同的键长期
// 并存,迟早会有人在库里改了旧的那个然后奇怪为什么不生效。
// 删不掉不算致命错误 —— 新键已经写进去并且优先级更高。正因如此它刻意留在
// 主事务之外:一次清理失败不该把一笔已经批准的调价整个回滚掉。
func dropSetting(ctx context.Context, key string) {
	gdb := db.Get()
	if gdb == nil {
		return
	}
	if err := gdb.WithContext(ctx).Where("scope = ? AND k = ?", settingScope, key).
		Delete(&qymodel.Setting{}).Error; err != nil {
		db.MarkFailure(err)
		warnf("清理已废弃的运营配置键 %s 失败: %v", key, err)
	}
}

// refSalt 返回下线标识的 HMAC 密钥。
//
// 首次使用时自动生成并持久化。刻意不放 YAML:它必须"部署一次、永不轮换"
// (轮换会让所有历史 ref 失效),自动生成比依赖运维记得改默认值更可靠。
//
// 查库与首次生成都在 saltOnce 之外完成,只有写缓存那一步持锁。持锁查库时,
// 首次调用撞上一次慢查询会把所有计佣写入协程一起钉在这把锁上 —— 与
// effectiveCtx 那里是同一个形状,只是这里更隐蔽:它一个进程只发生一次。
// 同样自带冷路径预算,唯一调用点在计佣写入路径上,拿不到调用方 ctx。
func refSalt() string {
	saltOnce.Lock()
	cached := saltCache
	saltOnce.Unlock()
	if cached != "" {
		return cached
	}
	gdb := db.Get()
	if gdb == nil {
		return ""
	}
	ctx, cancel := guard.ColdContext(context.Background())
	defer cancel()

	var row qymodel.Setting
	err := gdb.WithContext(ctx).Where("scope = ? AND k = ?", settingScope, keyRefSalt).
		First(&row).Error
	if err != nil || row.V == "" {
		buf := make([]byte, 32)
		if _, err := rand.Read(buf); err != nil {
			common.SysError("qianye: 生成下线标识盐失败: " + err.Error())
			return ""
		}
		// DoNothing:多节点同时首启(以及本进程内多个协程同时首次调用)时
		// 只有一个写入生效,之后统一重读,保证全网取到同一个盐。
		create := qymodel.Setting{
			Scope: settingScope, K: keyRefSalt, V: hex.EncodeToString(buf),
			UpdatedAt: common.GetTimestamp(),
		}
		if err := gdb.WithContext(ctx).Clauses(clause.OnConflict{DoNothing: true}).
			Create(&create).Error; err != nil {
			db.MarkFailure(err)
			return ""
		}
		if err := gdb.WithContext(ctx).Where("scope = ? AND k = ?", settingScope, keyRefSalt).
			First(&row).Error; err != nil {
			return ""
		}
	}
	if row.V == "" {
		return ""
	}
	saltOnce.Lock()
	if saltCache == "" {
		saltCache = row.V
	}
	value := saltCache
	saltOnce.Unlock()
	return value
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
	// 消费方是本文件的 effectiveCtx。
	settingsDegrade = &degradeRecord{}

	// fiatRateDegrade 计"法币折算比例没能走到它本该走的那一层"。
	//
	// 两种触发:分组档读不到(整表回落)、库里某一层存着非法比例。消费方是
	// fiatrate.go 的 fiatRates / fiatRateFor。必须计数的理由与另外两个降级完全
	// 一致 —— 比例会被冻结进 accrual 行,事后再也分不出"这行是降级"还是
	// "当时配的就是这个"。
	fiatRateDegrade = &degradeRecord{}

	// groupRateDegrade 计"分组费率读不到,本次按全局默认费率计佣"。
	// 消费方是 grouprate.go 的 groupRates(),它两条**返回空表**的回落路径
	// 各上报一次(沿用旧快照不上报 —— 那是缓存的正常语义,费率仍然是对的)。
	groupRateDegrade = &degradeRecord{}

	// inviterGroupDegrade 计"主库读不到**上线**那一行,本次费率与法币比例
	// 一起跳过分组层"。消费方是 pricing.go 的 resolveInviterPricing。
	//
	// 单独一个计数器而不是并进上面两个:另外两个说的是"配置读不到",这一个
	// 说的是"人读不到",而后者恰恰是本轮把两档口径都改成上线分组之后**新增**
	// 的那一个主库依赖。运营看健康面板要能一眼分清"是扩展库挂了"还是
	// "主库挂了",这两件事的处置完全不同。
	//
	// 它也是这条路上唯一的痕迹:降级那一批 accrual 行的 rate_group 是空串
	// (见 rateDecision.Group),而空串在库里安静得很。
	inviterGroupDegrade = &degradeRecord{}
)

// degradeSilenceKey 标记"这一次解析是**展示**,不是计佣"。
//
// # 为什么需要它
//
// degraded.* 这几个计数器的全部用途是回答「哪段时间的佣金要复核」——
// 降级算出来的账目与正常账目在流水上长得一模一样,除了这个计数器再没有
// 第二个痕迹。fiatDecision 的注释因此写着「判定函数刻意不自己上报降级」。
//
// 而费率与法币比例被收进 resolveInviterPricing 这一个入口之后,用户端
// 「我的推广」页也走同一条解析路径,并且对同一个人连解析三次(充值/消费/
// 兑换码三档)。于是任何一个已登录用户按住 F5 就能把计数器按 3 倍速率推上去:
// 「要复核的佣金区间」变成了「用户刷新页面的次数」的函数,而真正的降级
// 会被淹没在里面(last_at 被一路推到刚刚、last_reason 被跨原因覆盖、
// 60 秒的告警槽也被只读流量占满)。
//
// 修法不是让展示路径复刻一份判定(那正是 pricing.go 存在的理由要避免的),
// 而是让它**走同一条路但闭嘴**:结果照旧准确,只是不往取证计数器里写。
// 判据挂在 ctx 上而不是加参数,是为了让 resolveRate / resolveFiatRate /
// groupRates 的签名不变 —— 单一解析入口那条守卫钉的就是这几个签名。
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
// 零值口径:ctx 上没有这个标记 = 计佣路径 = 照常上报。也就是说漏加标记
// 只会让计数器多计,不会让一次真实降级被吞掉 —— 这是两个方向里安全的那一边。
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
