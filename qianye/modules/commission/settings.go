package commission

import (
	"crypto/rand"
	"encoding/hex"
	"strconv"
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
	keyTopupRateBps      = "topup_rate_bps"
	keyConsumeRateBps    = "consume_rate_bps"
	keyMinSettleQuota    = "min_settle_quota"
	keyMaxPerOrderQuota  = "max_per_order_quota"
	keyHoldingDays       = "holding_days"
	keyDailyCapQuota     = "max_daily_quota_per_inviter"
	keyLargeAlertQuota   = "large_accrual_alert_quota"
	keyMinInviteeAgeHour = "min_invitee_age_hours"
	keyRefSalt           = "invitee_ref_salt"
)

// editableKeys 限定管理端可写的键。白名单而非黑名单:
// 让管理端能往共享 KV 表里写任意键,等于把别的模块的配置面也交出去了。
var editableKeys = []string{
	keyTopupRateBps, keyConsumeRateBps, keyMinSettleQuota, keyMaxPerOrderQuota,
	keyHoldingDays, keyDailyCapQuota, keyLargeAlertQuota, keyMinInviteeAgeHour,
}

// opSettings 是 YAML 与运营覆盖合并后的生效配置。
type opSettings struct {
	TopupRateBps       int
	ConsumeRateBps     int
	MinSettleQuota     int64
	MaxPerOrderQuota   int64
	HoldingDays        int
	DailyCapQuota      int64 // 0 = 不限
	LargeAlertQuota    int64 // 0 = 不告警
	MinInviteeAgeHours int   // 0 = 不限
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

	saltOnce  sync.Mutex
	saltCache string
)

// effective 返回当前生效的运营配置。禁止在 relay 线程调用 —— 它可能查库。
func effective() opSettings {
	base := opSettings{}
	cm := config.Get().Commission
	base.TopupRateBps = cm.TopupRateBps
	base.ConsumeRateBps = cm.ConsumeRateBps
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

func applyOverrides(s *opSettings, m map[string]string) {
	intOverride(&s.TopupRateBps, m[keyTopupRateBps])
	intOverride(&s.ConsumeRateBps, m[keyConsumeRateBps])
	int64Override(&s.MinSettleQuota, m[keyMinSettleQuota])
	int64Override(&s.MaxPerOrderQuota, m[keyMaxPerOrderQuota])
	intOverride(&s.HoldingDays, m[keyHoldingDays])
	int64Override(&s.DailyCapQuota, m[keyDailyCapQuota])
	int64Override(&s.LargeAlertQuota, m[keyLargeAlertQuota])
	intOverride(&s.MinInviteeAgeHours, m[keyMinInviteeAgeHour])
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
