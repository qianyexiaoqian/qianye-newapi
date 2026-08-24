package lottery

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
	keyShowEntry = "show_entry"
	// 四个玩法各一个显示开关(见 play.go)。它们与 show_entry 是**串联**的:
	// show_entry 关掉整块,这四个各关一种玩法。分成五个键而不是一个列表键,
	// 是为了让每一项都能走 settingBounds 那套 0/1 区间校验与审计前后快照 ——
	// 一个 "draw_rank,guess" 这样的列表字符串既没有区间可校验,
	// 也会让审计里的 before/after 变成两串要人去 diff 的文本。
	keyShowPlayDrawRank     = "show_play_draw_rank"
	keyShowPlayDrawProb     = "show_play_draw_prob"
	keyShowPlayDrawBall     = "show_play_draw_ball"
	keyShowPlayGuess        = "show_play_guess"
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
	keyShowEntry,
	keyShowPlayDrawRank, keyShowPlayDrawProb, keyShowPlayDrawBall, keyShowPlayGuess,
	keyMaxActiveActivities, keyDefaultGuessFeeBps,
	keyMaxGuessFeeBps, keyMaxTotalPrizeQuota, keyLargePrizeAlertQuota,
}

// opSettings 是 YAML 与运营覆盖合并后的生效配置。
type opSettings struct {
	ShowEntry bool
	// 四个玩法开关**刻意没有 YAML 对应项**,基线写死为 true(见 baseSettings)。
	//
	// 理由是分工:YAML 承载启动级、涉及安全与资金的闸门(reveal_delay_seconds、
	// max_stake_quota、以及关掉整块的 lottery.enabled),这四项是纯展示口径、
	// 是运营的日常动作 —— "这一期只上竞猜"改一次要重启一次进程是荒唐的。
	// 整块下线仍然只有 YAML 的 lottery.enabled 一条路,那道硬闸没有被稀释。
	ShowPlayDrawRank     bool
	ShowPlayDrawProb     bool
	ShowPlayDrawBall     bool
	ShowPlayGuess        bool
	MaxActiveActivities  int
	DefaultGuessFeeBps   int
	MaxGuessFeeBps       int
	MaxTotalPrizeQuota   int64
	LargePrizeAlertQuota int64
}

// baseSettings 把一份 YAML 折成运营覆盖的基线。
//
// 单独成函数是因为它有两个消费方:effectiveCtx(生效值的起点)与配置接口的
// yaml_defaults(告诉运营"清掉覆盖会回到哪里")。两处各写一份对象字面量,
// 漂移的方向恰好是"界面上说默认全显示,实际默认全隐藏"。
func baseSettings(c config.Lottery) opSettings {
	return opSettings{
		ShowEntry: c.EntryShown(),
		// 零值口径:没配过 = 全部显示。见 play.go 文件头。
		ShowPlayDrawRank:     true,
		ShowPlayDrawProb:     true,
		ShowPlayDrawBall:     true,
		ShowPlayGuess:        true,
		MaxActiveActivities:  c.MaxActiveActivities,
		DefaultGuessFeeBps:   c.DefaultGuessFeeBps,
		MaxGuessFeeBps:       c.MaxGuessFeeBps,
		MaxTotalPrizeQuota:   c.MaxTotalPrizeQuota,
		LargePrizeAlertQuota: c.LargePrizeAlertQuota,
	}
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
	base := baseSettings(config.Get().Lottery)

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

	if v, ok := parseBoolIn(rows, keyShowEntry); ok {
		base.ShowEntry = v
	}
	// 玩法开关。读不懂的行(非法字面量、被人手改成 "yes")一律**丢弃并回落
	// 基线 = 显示**,与本文件其余越界值的处理同向:一行读不懂的配置不该让一整种
	// 玩法从站点上消失,那种消失没有任何一处会报错。
	if v, ok := parseBoolIn(rows, keyShowPlayDrawRank); ok {
		base.ShowPlayDrawRank = v
	}
	if v, ok := parseBoolIn(rows, keyShowPlayDrawProb); ok {
		base.ShowPlayDrawProb = v
	}
	if v, ok := parseBoolIn(rows, keyShowPlayDrawBall); ok {
		base.ShowPlayDrawBall = v
	}
	if v, ok := parseBoolIn(rows, keyShowPlayGuess); ok {
		base.ShowPlayGuess = v
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
	// 这两个键的区间**必须与写侧同源**,所以直接取 settingBounds() 而不是在这里
	// 复述一遍 lo/hi:写侧闸门只管今后的写入,读侧若还写死一份旧区间,升级之前
	// 落库的越界覆盖会继续被读出来生效,敞口一点没关(settings_bounds_test.go)。
	//
	// 奖品硬顶仍然只允许**调低**(quotaCeilingBound 在 YAML 为正时给 [1, yaml]);
	// YAML 为 0 时它本来就不限,在线随便配。
	bounds := settingBounds()
	if v, ok := parseInt64Within(rows, keyMaxTotalPrizeQuota, bounds[keyMaxTotalPrizeQuota]); ok {
		base.MaxTotalPrizeQuota = v
	}
	if v, ok := parseInt64Within(rows, keyLargePrizeAlertQuota, bounds[keyLargePrizeAlertQuota]); ok {
		base.LargePrizeAlertQuota = v
	}
	if base.DefaultGuessFeeBps > base.MaxGuessFeeBps {
		base.DefaultGuessFeeBps = base.MaxGuessFeeBps
	}
	return base
}

// parseBoolIn 取一个布尔覆盖。接口写进去的是 "0"/"1",但历史上也写过
// "true"/"false",两种都要认 —— strconv.ParseBool 恰好都收。
func parseBoolIn(rows map[string]string, key string) (bool, bool) {
	raw, ok := rows[key]
	if !ok {
		return false, false
	}
	v, err := strconv.ParseBool(strings.TrimSpace(raw))
	if err != nil {
		return false, false
	}
	return v, true
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

// parseInt64Within 取一个 int64 覆盖,判据是写侧那一份 settingBound ——
// 区间只有一个定义点,读写两侧不可能各说各的。
func parseInt64Within(rows map[string]string, key string, b settingBound) (int64, bool) {
	raw, ok := rows[key]
	if !ok {
		return 0, false
	}
	v, err := strconv.ParseInt(strings.TrimSpace(raw), 10, 64)
	if err != nil || !b.contains(v) {
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
