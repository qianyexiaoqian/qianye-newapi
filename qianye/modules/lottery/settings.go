package lottery

import (
	"context"
	"strconv"
	"sync"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/qianye/config"
	"github.com/QuantumNous/new-api/qianye/db"
	"github.com/QuantumNous/new-api/qianye/guard"
	qymodel "github.com/QuantumNous/new-api/qianye/model"

	"gorm.io/gorm/clause"
)

// settings.go —— 运营可在管理端修改的参数(qy_settings, scope=lottery)。
//
// 与 YAML 的分工:YAML 承载启动级、涉及安全的闸门(reveal_delay_seconds、
// 各种硬上限);这里承载"上线后要频繁调"的参数。改一次手续费不该需要重启进程,
// 但也绝不能让它落进上游主库的 options 表。

const settingScope = "lottery"

// 运营可改的键。白名单而非黑名单:让管理端往共享 KV 表里写任意键,
// 等于把别的模块的配置面也交出去了。
const (
	keyShowEntry            = "show_entry"
	keyMaxActiveActivities  = "max_active_activities"
	keyDefaultGuessFeeBps   = "default_guess_fee_bps"
	keyMaxGuessFeeBps       = "max_guess_fee_bps"
	keyMaxTotalPrizeQuota   = "max_total_prize_quota"
	keyLargePrizeAlertQuota = "large_prize_alert_quota"
)

// editableKeys 限定管理端可写的键。
//
// 刻意**不含** reveal_delay_seconds 与 max_stake_quota:前者是承诺-揭示协议的
// 安全间隔,后者决定单笔扣款上限 —— 这两项能被在线改写,等于把整套公正性与
// 资金闸门的控制权交给一个 HTTP 接口。它们只能改 YAML 并重启,那是一次
// 看得见、留得下痕迹的动作。
var editableKeys = []string{
	keyShowEntry, keyMaxActiveActivities, keyDefaultGuessFeeBps,
	keyMaxGuessFeeBps, keyMaxTotalPrizeQuota, keyLargePrizeAlertQuota,
}

// opSettings 是 YAML 与运营覆盖合并后的生效配置。
type opSettings struct {
	ShowEntry            bool
	MaxActiveActivities  int
	DefaultGuessFeeBps   int
	MaxGuessFeeBps       int
	MaxTotalPrizeQuota   int64
	LargePrizeAlertQuota int64
}

const settingsCacheSeconds = 60

var (
	settingsMu     sync.Mutex
	settingsCache  *opSettings
	settingsLoaded int64
	settingsEpoch  uint64
)

// effectiveCtx 返回当前生效的运营配置。
//
// 查库放在互斥锁之外:持锁查库时一条慢 SELECT 会把所有读配置的协程串在同一把
// 锁上。代价是并发首次加载时可能有几个协程各查一次(几行的表),比排队便宜得多。
//
// 读不到覆盖值时退回上一份快照、再退回 YAML,而不是让整个功能停摆 ——
// 少一个运营微调远比"活动页整页打不开"轻。
func effectiveCtx(ctx context.Context) opSettings {
	c := config.Get().Lottery
	base := opSettings{
		ShowEntry:            c.EntryShown(),
		MaxActiveActivities:  c.MaxActiveActivities,
		DefaultGuessFeeBps:   c.DefaultGuessFeeBps,
		MaxGuessFeeBps:       c.MaxGuessFeeBps,
		MaxTotalPrizeQuota:   c.MaxTotalPrizeQuota,
		LargePrizeAlertQuota: c.LargePrizeAlertQuota,
	}

	settingsMu.Lock()
	if settingsCache != nil && common.GetTimestamp()-settingsLoaded < settingsCacheSeconds {
		cached := *settingsCache
		settingsMu.Unlock()
		return cached
	}
	epoch := settingsEpoch
	settingsMu.Unlock()

	rows, err := loadOverrides(ctx)
	if err != nil {
		settingsMu.Lock()
		defer settingsMu.Unlock()
		if settingsCache != nil {
			return *settingsCache
		}
		return base
	}
	merged := mergeOverrides(base, rows)

	settingsMu.Lock()
	defer settingsMu.Unlock()
	// 代次校验:查库期间管理员改过配置并调了 invalidateSettings(),
	// 在途的旧快照必须丢弃,否则会把新值静默盖掉 60 秒。
	if epoch == settingsEpoch {
		cp := merged
		settingsCache = &cp
		settingsLoaded = common.GetTimestamp()
	}
	return merged
}

// effective 是给拿不到调用方 ctx 的地方(后台任务)用的形态。
func effective() opSettings {
	// 自带一个冷路径预算而不是裸查:裸查会一直等到扩展库 DSN 的 readTimeout
	// (默认 30 秒),而调用点里有正持着活动行锁的结算事务。
	ctx, cancel := guard.ColdContext(context.Background())
	defer cancel()
	return effectiveCtx(ctx)
}

func invalidateSettings() {
	settingsMu.Lock()
	settingsCache = nil
	settingsLoaded = 0
	settingsEpoch++
	settingsMu.Unlock()
}

func loadOverrides(ctx context.Context) (map[string]string, error) {
	gdb := db.Get()
	if gdb == nil {
		return nil, db.ErrNotReady
	}
	rows := make([]qymodel.Setting, 0, len(editableKeys))
	if err := gdb.WithContext(ctx).
		Where("scope = ?", settingScope).Find(&rows).Error; err != nil {
		db.MarkFailure(err)
		return nil, err
	}
	out := make(map[string]string, len(rows))
	for _, r := range rows {
		out[r.K] = r.V
	}
	return out, nil
}

// mergeOverrides 把运营覆盖合并进 YAML 基线。
//
// 越界值一律**丢弃并回落 YAML**,而不是钳到边界:钳取会让运营以为自己配的是
// 50%、实际跑的是 20%,那正是本仓反复栽跟头的"以为改了其实没改"。
func mergeOverrides(base opSettings, rows map[string]string) opSettings {
	c := config.Get().Lottery

	if v, ok := rows[keyShowEntry]; ok {
		if b, err := strconv.ParseBool(v); err == nil {
			base.ShowEntry = b
		}
	}
	// 上界必须与 settingBounds() 同源取自 YAML。写死一个 1000 的后果不是"写入
	// 时被拦住就行了":写入闸门只管**今后**的写入,升级之前已经落库的越界覆盖
	// (旧上界允许到 1000)会继续被这里读出来并生效,敞口一点没关,而配置页会
	// 同时显示 effective=500 与 bounds.max=20 却不报任何异常。
	if v, ok := parseIntIn(rows, keyMaxActiveActivities, 1, c.MaxActiveActivities); ok {
		base.MaxActiveActivities = v
	}
	if v, ok := parseIntIn(rows, keyMaxGuessFeeBps, 0, c.MaxGuessFeeBps); ok {
		base.MaxGuessFeeBps = v
	}
	if v, ok := parseIntIn(rows, keyDefaultGuessFeeBps, 0, base.MaxGuessFeeBps); ok {
		base.DefaultGuessFeeBps = v
	}
	// 奖品上限只允许**调低**:YAML 里的值是安全上界,允许在线调高等于把
	// "抽奖派奖是净增发"这道唯一的闸门交给一个 HTTP 接口。
	if v, ok := parseInt64In(rows, keyMaxTotalPrizeQuota, 1, c.MaxTotalPrizeQuota); ok {
		base.MaxTotalPrizeQuota = v
	}
	if v, ok := parseInt64In(rows, keyLargePrizeAlertQuota, 0, c.MaxTotalPrizeQuota); ok {
		base.LargePrizeAlertQuota = v
	}
	if base.DefaultGuessFeeBps > base.MaxGuessFeeBps {
		base.DefaultGuessFeeBps = base.MaxGuessFeeBps
	}
	return base
}

func parseIntIn(rows map[string]string, key string, lo, hi int) (int, bool) {
	raw, ok := rows[key]
	if !ok {
		return 0, false
	}
	v, err := strconv.Atoi(raw)
	if err != nil || v < lo || v > hi {
		return 0, false
	}
	return v, true
}

func parseInt64In(rows map[string]string, key string, lo, hi int64) (int64, bool) {
	raw, ok := rows[key]
	if !ok {
		return 0, false
	}
	v, err := strconv.ParseInt(raw, 10, 64)
	if err != nil || v < lo || v > hi {
		return 0, false
	}
	return v, true
}

// saveOverrides 在一个扩展库事务内落盘全部覆盖值。
func saveOverrides(ctx context.Context, operatorId int, kv map[string]string) error {
	gdb := db.Get()
	if gdb == nil {
		return db.ErrNotReady
	}
	now := common.GetTimestamp()
	rows := make([]qymodel.Setting, 0, len(kv))
	for k, v := range kv {
		rows = append(rows, qymodel.Setting{
			Scope: settingScope, K: k, V: v, OperatorId: operatorId, UpdatedAt: now,
		})
	}
	if len(rows) == 0 {
		return nil
	}
	err := gdb.WithContext(ctx).Clauses(clause.OnConflict{
		Columns:   []clause.Column{{Name: "scope"}, {Name: "k"}},
		DoUpdates: clause.AssignmentColumns([]string{"v", "operator_id", "updated_at"}),
	}).Create(&rows).Error
	if err != nil {
		db.MarkFailure(err)
	}
	return err
}
