package commission

import (
	"crypto/rand"
	"encoding/hex"
	"strconv"
	"strings"
	"sync"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/qianye/config"
	"github.com/QuantumNous/new-api/qianye/db"
	qymodel "github.com/QuantumNous/new-api/qianye/model"

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
	keyTopupRatePercent, keyConsumeRatePercent, keyMinSettleQuota, keyMaxPerOrderQuota,
	keyHoldingDays, keyDailyCapQuota, keyLargeAlertQuota, keyMinInviteeAgeHour,
}

// percentKeys 是取值为百分比字符串的键,其余键一律是整数。
func isPercentKey(k string) bool {
	return k == keyTopupRatePercent || k == keyConsumeRatePercent
}

// opSettings 是 YAML 与运营覆盖合并后的生效配置。
type opSettings struct {
	// TopupRateUnits / ConsumeRateUnits 是内部整数费率:百分比 × 100
	// (10.25% = 1025)。对外(YAML、接口、界面)一律换算回百分比,
	// 对内一律整数 —— 资金计算不接受浮点误差。
	TopupRateUnits     int
	ConsumeRateUnits   int
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

// settingsCacheSeconds 是运营配置的刷新周期。
//
// 刻意不起后台协程去刷:那样每个节点都要一个裸 goroutine,而配置只在
// 非热路径(异步 worker、结算任务、HTTP handler)被读到,惰性刷新足够。
const settingsCacheSeconds = 60

var (
	settingsMu     sync.Mutex
	settingsCache  *opSettings
	settingsLoaded int64

	saltOnce  sync.Mutex
	saltCache string
)

// effective 返回当前生效的运营配置。禁止在 relay 线程调用 —— 它可能查库。
func effective() opSettings {
	base := opSettings{}
	cm := config.Get().Commission
	base.TopupRateUnits = configRateUnits("topup_rate_percent", cm.TopupRatePercent)
	base.ConsumeRateUnits = configRateUnits("consume_rate_percent", cm.ConsumeRatePercent)
	base.MinSettleQuota = cm.MinSettleQuota
	base.MaxPerOrderQuota = cm.MaxPerOrderQuota
	base.HoldingDays = cm.HoldingDays

	settingsMu.Lock()
	defer settingsMu.Unlock()
	now := common.GetTimestamp()
	if settingsCache != nil && now-settingsLoaded < settingsCacheSeconds {
		merged := *settingsCache
		return merged
	}
	overrides, err := loadOverrides()
	if err != nil {
		// 读不到覆盖值时退回 YAML,而不是让计佣停摆:少一个运营微调
		// 远比"整条返佣链路挂掉"轻。
		if settingsCache != nil {
			return *settingsCache
		}
		return base
	}
	applyOverrides(&base, overrides)
	settingsCache = &base
	settingsLoaded = now
	return base
}

// invalidateSettings 在管理端改配置后立即失效缓存,避免"改完 60 秒还没生效"的困惑。
func invalidateSettings() {
	settingsMu.Lock()
	settingsCache = nil
	settingsLoaded = 0
	settingsMu.Unlock()
}

func loadOverrides() (map[string]string, error) {
	gdb := db.Get()
	if gdb == nil {
		return nil, db.ErrNotReady
	}
	var rows []qymodel.Setting
	if err := gdb.Where("scope = ?", settingScope).Find(&rows).Error; err != nil {
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

func applyOverrides(s *opSettings, m map[string]string) {
	rateOverride(&s.TopupRateUnits, m[keyTopupRatePercent], m[legacyKeyTopupRateBps])
	rateOverride(&s.ConsumeRateUnits, m[keyConsumeRatePercent], m[legacyKeyConsumeRateBps])
	int64Override(&s.MinSettleQuota, m[keyMinSettleQuota])
	int64Override(&s.MaxPerOrderQuota, m[keyMaxPerOrderQuota])
	intOverride(&s.HoldingDays, m[keyHoldingDays])
	int64Override(&s.DailyCapQuota, m[keyDailyCapQuota])
	int64Override(&s.LargeAlertQuota, m[keyLargeAlertQuota])
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

func intOverride(dst *int, raw string) {
	if raw == "" {
		return
	}
	if v, err := strconv.Atoi(raw); err == nil && v >= 0 {
		*dst = v
	}
}

func int64Override(dst *int64, raw string) {
	if raw == "" {
		return
	}
	if v, err := strconv.ParseInt(raw, 10, 64); err == nil && v >= 0 {
		*dst = v
	}
}

// writeSetting 落一条运营覆盖。调用方负责写审计 —— 费率变更必须可追溯到人。
func writeSetting(key, value string, operatorId int) error {
	gdb := db.Get()
	if gdb == nil {
		return db.ErrNotReady
	}
	row := qymodel.Setting{
		Scope:      settingScope,
		K:          key,
		V:          value,
		OperatorId: operatorId,
		UpdatedAt:  common.GetTimestamp(),
	}
	err := gdb.Clauses(clause.OnConflict{
		Columns:   []clause.Column{{Name: "scope"}, {Name: "k"}},
		DoUpdates: clause.AssignmentColumns([]string{"v", "operator_id", "updated_at"}),
	}).Create(&row).Error
	if err != nil {
		db.MarkFailure(err)
	}
	return err
}

// dropSetting 删除一条运营覆盖。
//
// 只服务于"新键写入后清掉同义的旧键"这一个场景:两个语义相同的键长期
// 并存,迟早会有人在库里改了旧的那个然后奇怪为什么不生效。
// 删不掉不算致命错误 —— 新键已经写进去并且优先级更高。
func dropSetting(key string) {
	gdb := db.Get()
	if gdb == nil {
		return
	}
	if err := gdb.Where("scope = ? AND k = ?", settingScope, key).
		Delete(&qymodel.Setting{}).Error; err != nil {
		db.MarkFailure(err)
		warnf("清理已废弃的运营配置键 %s 失败: %v", key, err)
	}
}

// refSalt 返回下线标识的 HMAC 密钥。
//
// 首次使用时自动生成并持久化。刻意不放 YAML:它必须"部署一次、永不轮换"
// (轮换会让所有历史 ref 失效),自动生成比依赖运维记得改默认值更可靠。
func refSalt() string {
	saltOnce.Lock()
	defer saltOnce.Unlock()
	if saltCache != "" {
		return saltCache
	}
	gdb := db.Get()
	if gdb == nil {
		return ""
	}
	var row qymodel.Setting
	err := gdb.Where("scope = ? AND k = ?", settingScope, keyRefSalt).First(&row).Error
	if err == nil && row.V != "" {
		saltCache = row.V
		return saltCache
	}

	buf := make([]byte, 32)
	if _, err := rand.Read(buf); err != nil {
		common.SysError("qianye: 生成下线标识盐失败: " + err.Error())
		return ""
	}
	generated := hex.EncodeToString(buf)
	// DoNothing:多节点同时首启时只有一个写入生效,之后统一重读。
	create := qymodel.Setting{
		Scope: settingScope, K: keyRefSalt, V: generated, UpdatedAt: common.GetTimestamp(),
	}
	if err := gdb.Clauses(clause.OnConflict{DoNothing: true}).Create(&create).Error; err != nil {
		db.MarkFailure(err)
		return ""
	}
	if err := gdb.Where("scope = ? AND k = ?", settingScope, keyRefSalt).First(&row).Error; err != nil {
		return ""
	}
	saltCache = row.V
	return saltCache
}
