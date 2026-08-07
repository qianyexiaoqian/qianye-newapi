package config

import (
	"fmt"
	"math"
	"reflect"
	"strconv"

	"github.com/QuantumNous/new-api/common"
)

// 返佣比例的默认值,单位是百分比。
const (
	defaultTopupRatePercent   = "10"
	defaultConsumeRatePercent = "5"
)

// adoptDeprecatedRates 把已废弃的 commission.*_rate_bps 换算进新的 *_rate_percent。
//
// 为什么保留而不是直接删:本包是严格解析(KnownFields(true)),删掉字段会让
// 每一个还写着 topup_rate_bps 的部署在升级二进制的那一刻启动失败 ——
// 一个纯改名的动作不该造成停机。
//
// 只在新字段为空时才采纳旧值;两者都写了并且互相矛盾,由 checkRatePair 拒绝启动。
// 告警走 SysError 而不是 SysLog:淹没在 info 里的迁移提示等于没写。
func adoptDeprecatedRates(cm *Commission) {
	adopt := func(oldKey, newKey string, bps *int, percent *string) {
		if bps == nil {
			return
		}
		common.SysError(fmt.Sprintf(
			"qianye: commission.%s 已废弃,请改名为 %s 并直接填百分比(%d 等价于 %s)",
			oldKey, newKey, *bps, bpsToPercent(*bps)))
		if *percent == "" {
			*percent = bpsToPercent(*bps)
		}
	}
	adopt("topup_rate_bps", "topup_rate_percent",
		cm.TopupRateBpsDeprecated, &cm.TopupRatePercent)
	adopt("consume_rate_bps", "consume_rate_percent",
		cm.ConsumeRateBpsDeprecated, &cm.ConsumeRatePercent)
}

// adoptRetiredGroupPricing 处置已下线的 group_pricing 配置段。
//
// 「模型按分组单独定价」整个模块已下线,取而代之的是 (用户分组, 模型分组) 的分组倍率
// 矩阵。但配置段不能跟着一起删:本包是严格解析(KnownFields(true)),删掉字段会让
// 每一个还写着 group_pricing: 的部署在升级二进制的那一刻**启动失败** ——
// 一次功能下线不该造成停机。
//
// 于是保留一个 map 占位吸收整段,在这里喊一声并置 nil。告警走 SysError 而不是
// SysLog:淹在 info 里的迁移提示等于没写,而这一段的语义变化是"曾经参与扣费的规则
// 现在一条都不生效了",运维必须看见。
//
// 置 nil 是刻意的:让"配置里写没写过这一段"在加载之后不再可观测,断掉任何
// 后来者把它重新当成开关读的可能。
func adoptRetiredGroupPricing(c *Config) {
	if c.GroupPricingDeprecated == nil {
		return
	}
	common.SysError("qianye: 配置里仍有 group_pricing 段,但「模型按分组单独定价」已下线 —— " +
		"该段被整段忽略,其中的规则不再参与任何一次扣费。" +
		"分组级价格改由「用户分组 × 模型分组」倍率矩阵表达(管理端「用户分组」页)," +
		"确认无误后可以把这一段从 YAML 里删掉")
	c.GroupPricingDeprecated = nil
}

// adoptRetiredNewGroupDeny 处置已下线的「新分组默认全遮断」两个键。
//
// 那条默认与本轮拍定的口径**完全相反**:新口径是「未设定范围 = 全部模型分组可用,
// 按兜底倍率」,而它做的是"新分组一建出来就一个都不许用"。撤销时连同配置项一起撤,
// 不留一个没人用的开关 —— 留着它最坏的形状是有人把它设成 true 然后以为收紧生效了。
//
// 同样不能直接删字段:KnownFields(true) 下,任何仍写着这两个键的部署会在升级
// 二进制的那一刻启动失败。保留 Deprecated 占位吸收它们,在这里喊一声并置 nil。
//
// 告警走 SysError 而不是 SysLog:这两个键曾经的语义是"自动收紧",而现在它们
// 一个字节都不生效 —— 淹在 info 里的迁移提示等于没写。
func adoptRetiredNewGroupDeny(gm *GroupMatrix) {
	if gm.NewGroupDefaultDenyDeprecated != nil {
		common.SysError("qianye: group_matrix.new_group_default_deny 已废弃并被忽略 —— " +
			"「新分组默认全遮断」已下线。新口径是:用户分组**未设定范围**时,全部模型分组可用、" +
			"按各自的兜底倍率计费;需要限制某个用户分组请在管理端「用户分组」页为它显式设定范围。" +
			"确认无误后可以把这一行从 YAML 里删掉")
		gm.NewGroupDefaultDenyDeprecated = nil
	}
	if gm.NewGroupScanIntervalSecondsDeprecated != nil {
		common.SysError("qianye: group_matrix.new_group_scan_interval_seconds 已废弃并被忽略 —— " +
			"「新分组对账」后台任务已随「新分组默认全遮断」一并下线,该键不再有任何效果")
		gm.NewGroupScanIntervalSecondsDeprecated = nil
	}
}

// bpsToPercent 把万分比整数换算成百分比字符串,只做整数除法与取余,
// 不经过 float64 —— 这条路径最终会喂给费率解析,精度不能在这里丢。
//
// 非法(负数)输入原样带过去:换算不是校验的地方,checkRatePair 会点名报错。
func bpsToPercent(bps int) string {
	if bps < 0 {
		return strconv.Itoa(bps)
	}
	whole, frac := bps/RatePercentScale, bps%RatePercentScale
	if frac == 0 {
		return strconv.Itoa(whole)
	}
	if frac%10 == 0 {
		return fmt.Sprintf("%d.%d", whole, frac/10)
	}
	return fmt.Sprintf("%d.%02d", whole, frac)
}

// applyDefaults 把 YAML 里没有出现的键补成默认值。
//
// 判据是"键缺失",不是"值为零" —— 见本文件末尾 markNumbersUnset 的说明。
// 因此必须与解析前的 markNumbersUnset 成对调用:数值字段进来时带着哨兵,
// 出去时要么是文件里写的值、要么是默认值、要么是 0(没写也没默认值)。
//
// 字符串字段仍以空串为判据:pii_key、log_level 这些字段上"填了空串"与
// "没填"确实是同一件事,再套一层哨兵只会多一个可漂移的地方。
// 布尔开关的默认值由 *bool + boolOr 承载(见 config.go)。
func applyDefaults(c *Config) {
	d := &c.Database
	intDefault(&d.MaxIdleConns, 20)
	intDefault(&d.MaxOpenConns, 100)
	intDefault(&d.ConnMaxLifetimeSeconds, 600)
	intDefault(&d.ConnMaxIdleTimeSeconds, 120)
	intDefault(&d.ConnectTimeoutSeconds, 5)
	intDefault(&d.ReadTimeoutSeconds, 30)
	intDefault(&d.WriteTimeoutSeconds, 30)
	intDefault(&d.SlowThresholdMs, 200)
	strDefault(&d.LogLevel, "warn")

	r := &c.Runtime
	intDefault(&r.HotPathTimeoutMs, 200)
	intDefault(&r.HotAsyncTimeoutMs, 3000)
	intDefault(&r.ColdPathTimeoutMs, 3000)
	intDefault(&r.HealthIntervalSeconds, 15)
	intDefault(&r.BreakerFailureThreshold, 5)
	intDefault(&r.BreakerOpenSeconds, 30)
	intDefault(&r.LeaseTTLSeconds, 60)
	intDefault(&r.LeaseRenewSeconds, 20)
	intDefault(&r.HotHookQueueSize, 4096)
	intDefault(&r.HotHookWorkers, 2)

	t := &c.TwoPhase
	intDefault(&t.CompensateIntervalSeconds, 30)
	intDefault(&t.PendingGraceSeconds, 60)
	intDefault(&t.MaxProbeAttempts, 10)
	intDefault(&t.BatchSize, 200)
	intDefault(&t.ManualReviewAfterSeconds, 900)
	intDefault(&t.OutboxRetentionDays, 30)

	intDefault(&c.Audit.SnapshotMaxBytes, 4096)

	tr := &c.Transfer
	int64Default(&tr.MinQuota, 500000)
	int64Default(&tr.MaxPerTxQuota, 50000000)
	int64Default(&tr.DailyMaxQuota, 200000000)
	intDefault(&tr.DailyMaxCount, 20)
	intDefault(&tr.CooldownSecs, 10)
	strDefault(&tr.RecipientLookup, RecipientLookupID)
	intDefault(&tr.NewAccountFreezeHours, 24)
	intDefault(&tr.ReceiverDailyMaxInCount, 50)
	intDefault(&tr.LookupLogRetainDays, 30)

	cm := &c.Commission
	adoptDeprecatedRates(cm)
	strDefault(&cm.TopupRatePercent, defaultTopupRatePercent)
	strDefault(&cm.ConsumeRatePercent, defaultConsumeRatePercent)
	intDefault(&cm.Levels, 1)
	int64Default(&cm.MinSettleQuota, 1000)
	int64Default(&cm.MaxPerOrderQuota, 50000000)
	intDefault(&cm.HoldingDays, 7)
	intDefault(&cm.SettleIntervalSecs, 300)
	intDefault(&cm.InviterCacheSecs, 300)
	intDefault(&cm.TopupScanIntervalSec, 60)
	intDefault(&cm.TopupScanLookbackHours, 72)

	w := &c.Withdraw
	if len(w.Methods) == 0 {
		w.Methods = []string{WithdrawMethodQuota, WithdrawMethodFiat}
	}
	int64Default(&w.MinQuota, 500000)
	strDefault(&w.MinFiatAmount, "100")
	strDefault(&w.FiatCurrency, "CNY")
	strDefault(&w.RateFreezeMode, RateFreezeOperationSetting)
	strDefault(&w.RateFreezeFixed, "7.3")
	intDefault(&w.DailyMaxCount, 3)
	intDefault(&w.PayeeAccountMax, 3)
	intDefault(&w.ReviewSLAHours, 72)
	intDefault(&w.RemarkMaxRunes, 200)
	intDefault(&w.PIIKeyVersion, 1)
	intDefault(&w.CooldownSecs, 60)
	intDefault(&w.MaxPendingOrders, 3)
	int64Default(&w.MaxQuotaPerOrder, 500000000)
	int64Default(&w.DailyMaxQuota, 1000000000)
	intDefault(&w.PIIRetentionDays, 180)
	int64Default(&w.ProofMaxBytes, 2<<20)

	tk := &c.Ticket
	intDefault(&tk.TitleMaxRunes, 100)
	intDefault(&tk.BodyMaxRunes, 5000)
	intDefault(&tk.MaxOpenPerUser, 5)
	intDefault(&tk.DailyMaxCount, 10)
	intDefault(&tk.CooldownSecs, 60)
	intDefault(&tk.ReplyCooldownSecs, 10)
	intDefault(&tk.MaxMessagesPerTicket, 100)
	intDefault(&tk.AutoCloseDays, 7)
	int64Default(&tk.ImageMaxBytes, 2<<20)
	intDefault(&tk.ImageMaxPerMessage, 3)
	int64Default(&tk.ImageUserQuotaBytes, 200<<20)
	intDefault(&tk.ImageRetentionDays, 180)

	av := &c.Availability
	intDefault(&av.BucketSeconds, 300)
	intDefault(&av.FlushIntervalSeconds, 60)
	intDefault(&av.RetentionDays, 30)
	intDefault(&av.MaxSeriesPerQuery, 200)

	v := &c.Violation
	strDefault(&v.FeeMultiplier, "1.0")
	strDefault(&v.FixedFeeAmount, "0.05")
	int64Default(&v.MaxFeeQuota, 5000000)
	strDefault(&v.InsufficientBalancePolicy, InsufficientClamp)
	intDefault(&v.AutoBanWindowHours, 24)
	intDefault(&v.GlobalBlockRateLimitBps, 500)
	intDefault(&v.GlobalBanRateLimitPerHour, 20)
	intDefault(&v.EvidenceMaxBytes, 8192)
	intDefault(&v.EvidenceRetentionDays, 90)
	intDefault(&v.RuleCacheSeconds, 60)
	intDefault(&v.ScanTimeoutMs, 20)

	adoptRetiredGroupPricing(c)

	gm := &c.GroupMatrix
	intDefault(&gm.CacheSeconds, 30)
	intDefault(&gm.MaxStaleSeconds, 300)
	intDefault(&gm.PreviewLogDays, 7)
	intDefault(&gm.MaxPreviewPairs, 500)
	intDefault(&gm.PreviewSampleLimit, 20)
	intDefault(&gm.MaxGrants, 2000)
	adoptRetiredNewGroupDeny(gm)

	pe := &c.PlanEntitlement
	intDefault(&pe.CacheSeconds, 30)
	intDefault(&pe.UserCacheSeconds, 60)
	intDefault(&pe.UserMaxStaleSeconds, 300)

	lt := &c.Lottery
	intDefault(&lt.MaxActiveActivities, 20)
	int64Default(&lt.MaxStakeQuota, 5_000_000)
	int64Default(&lt.MaxTotalPrizeQuota, 50_000_000)
	int64Default(&lt.LargePrizeAlertQuota, 5_000_000)
	int64Default(&lt.PayPasswordThresholdQuota, 100_000)
	intDefault(&lt.EntryCloseGraceSeconds, 60)
	intDefault(&lt.RevealDelaySeconds, 60)
	intDefault(&lt.LockScanIntervalSeconds, 15)
	intDefault(&lt.RevealScanIntervalSeconds, 15)
	intDefault(&lt.PayoutIntervalSeconds, 10)
	intDefault(&lt.PayoutMaxAttempts, 8)
	intDefault(&lt.ExcludedManualAfterSeconds, 900)
	intDefault(&lt.MaxTotalEntriesHard, 50000)
	intDefault(&lt.MaxPrizeTiers, 10)
	intDefault(&lt.MaxOptions, 12)
	intDefault(&lt.DefaultGuessFeeBps, 500)
	intDefault(&lt.MaxGuessFeeBps, 2000)
	intDefault(&lt.SpendScanIntervalSeconds, 60)
	intDefault(&lt.SpendScanBatch, 2000)
	intDefault(&lt.SpendGapGuardSeconds, 60)
	intDefault(&lt.SpendMaxLookbackDays, 90)
	intDefault(&lt.SpendRetentionDays, 120)

	// 走到这里仍是哨兵的字段,是"文件里没写、也没有默认值"的那一批
	// (transfer.fee_bps、audit.retention_days、violation.auto_ban_threshold……)。
	// 它们的语义就是零值,还原成 0 之后 validate 与业务代码看到的东西与从前一致。
	clearUnsetNumbers(reflect.ValueOf(c).Elem())
}

func intDefault(p *int, def int) {
	if *p == missingInt {
		*p = def
	}
}

func int64Default(p *int64, def int64) {
	if *p == missingInt64 {
		*p = def
	}
}

func strDefault(p *string, def string) {
	if *p == "" {
		*p = def
	}
}

// ─────────────────── "这个键到底写没写" 的判定 ───────────────────
//
// 判据曾经是"零值即未配置"。它错在 0 在本配置里遍地都是**有含义的取值**:
// 冷却期 0 = 不限制、成熟期 0 = 当天结算、上限 0 = 不设限 —— 业务代码里那些
// `if cfg.X > 0` 的守卫就是证据,而旧判据让 else 分支永远点不亮,配置层
// 一直在悄悄替运维改主意。
//
// 实测抓到的形态:commission.holding_days 显式写 0,被补成默认的 7,计提行
// 要多等 8 天才结算。运营查配置看到的是 0,用户看到可提现佣金是 0,双方都
// 以为是对方的问题,而且没有任何一行日志。
//
// 换判据的做法是:解析前先把每个数值字段填成一个不可能被写出来的哨兵,
// 解析后仍是哨兵的就是"文件里没这个键"。
//
// 为什么不改成一份 "commission.holding_days" 这样的路径表:那种表里一个
// 拼写错误就等于该字段静默退回旧行为,而且没人看得出来 —— 与本缺陷是同一种
// 形状。走哨兵的话调用点传的仍是字段本身(intDefault(&cm.HoldingDays, 7)),
// 由编译器保证指不错。
//
// 为什么不把这 15 个字段改成 *int:调用点要加大量解引用,且会留下"有的字段
// 是指针有的不是"的第 N 份拷贝,下一个新增字段照哪份抄全看运气。
const (
	// 取整型下界当哨兵。它与真实取值的距离是这个方案唯一的假设,所以说清楚:
	// 一个运维要写出 -9223372036854775808 才会撞上它,而那个数在任何一个字段上
	// 都不表达任何意图。撞上之后该字段会被当成"没写"从而取默认值 —— 不是每个
	// 字段都有 validate 规则能把它拦成负数(commission.holding_days 就没有),
	// 因此这里不承诺兜底,只承诺撞不上。
	missingInt   = math.MinInt
	missingInt64 = math.MinInt64
)

// markNumbersUnset 在 YAML 解析【之前】给全部数值字段打上哨兵。
//
// 递归而不展平路径:嵌套段(Transfer / Withdraw 这类)与顶层字段走同一段代码,
// 新增一个配置段不需要在这里补任何东西。*int(已废弃的 *_rate_bps)不在此列 ——
// 指针本来就能区分"写了 0"和"没写",那正是本判据要补齐的能力。
func markNumbersUnset(v reflect.Value) {
	for i := 0; i < v.NumField(); i++ {
		f := v.Field(i)
		if !f.CanSet() {
			continue
		}
		switch f.Kind() {
		case reflect.Struct:
			markNumbersUnset(f)
		case reflect.Int:
			f.SetInt(missingInt)
		case reflect.Int64:
			f.SetInt(missingInt64)
		}
	}
}

// clearUnsetNumbers 把 applyDefaults 之后残留的哨兵还原成 0。
// 少了这一步,没有默认值的字段会带着 math.MinInt 进入业务代码。
func clearUnsetNumbers(v reflect.Value) {
	for i := 0; i < v.NumField(); i++ {
		f := v.Field(i)
		if !f.CanSet() {
			continue
		}
		switch f.Kind() {
		case reflect.Struct:
			clearUnsetNumbers(f)
		case reflect.Int:
			if f.Int() == missingInt {
				f.SetInt(0)
			}
		case reflect.Int64:
			if f.Int() == missingInt64 {
				f.SetInt(0)
			}
		}
	}
}
