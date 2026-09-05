package stardust

import (
	"context"
	"sort"
	"strconv"
	"strings"
	"sync"
	"unicode/utf8"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/qianye/config"
	"github.com/QuantumNous/new-api/qianye/db"
	"github.com/QuantumNous/new-api/qianye/guard"
	qymodel "github.com/QuantumNous/new-api/qianye/model"
	"github.com/QuantumNous/new-api/setting/operation_setting"

	"gorm.io/gorm/clause"
)

// settings.go —— 运营可在管理端修改的参数(qy_settings, scope=stardust)。
//
// 与 YAML 的分工:YAML 承载启动级、涉及资金与调度的闸门(enabled、刻度、结算延迟、
// 手调上限、扫描周期);这里承载"上线后要频繁调"的参数:单位名、入口开关、
// 四档比例、注册奖、告警阈值。改一次比例不该需要重启进程,但也绝不能让它落进
// 上游主库的 options 表。形状照 lottery/settings.go。

const settingScope = "stardust"

// 运营可改的键。白名单而非黑名单:让管理端往共享 KV 表里写任意键,
// 等于把别的模块的配置面也交出去了。
const (
	keyName                   = "name"
	keyShowEntry              = "show_entry"
	keyConsumeBps             = "consume_bps"
	keyInviteTopupBps         = "invite_topup_bps"
	keyInviteRedeemBps        = "invite_redeem_bps"
	keyInviteConsumeBps       = "invite_consume_bps"
	keyInviteRegisterStardust = "invite_register_stardust"
	keyHeldAlertDays          = "held_alert_days"
)

// editableKeys 限定管理端可写的键(design §9.2)。
//
// 刻意**不含** quota_per_unit 与 max_manual_adjust:前者是冻结进每一行日桶的刻度,
// 后者决定一个 HTTP 接口一次能凭空造出多少星屑 —— 两项只能改 YAML 并重启,
// 那是一次看得见、留得下痕迹的动作。
var editableKeys = []string{
	keyName, keyShowEntry, keyConsumeBps,
	keyInviteTopupBps, keyInviteRedeemBps, keyInviteConsumeBps, keyInviteRegisterStardust,
	keyHeldAlertDays,
}

// EditableKeys 返回白名单的一份拷贝,供管理端下发 editable_keys。
func EditableKeys() []string { return append([]string(nil), editableKeys...) }

const (
	// maxBps 是三档比例的上界:1000:1(每 1 美元等值返 1000 星屑)。
	// 必须与 qianye/config/validate.go 的 maxStardustBps 一致 —— 那是 YAML 一侧的
	// 同一道闸;两边不同的后果是"YAML 拒绝启动、在线却能写进去"。
	maxBps = 10_000_000
	// maxNameRunes 与 config.validateStardust 对 stardust.name 的限制同值。
	maxNameRunes = 16
	// maxHeldAlertDays:暂缓桶积龄告警阈值,一年之外没有意义。
	maxHeldAlertDays = 365
	// defaultName 是 config/defaults.go 给 stardust.name 的基线;这里再兜一次是因为
	// 测试与热重载可能绕过 applyDefaults 直接给出 Config,而一个空的单位名会让
	// 每一处文案都少一个词。
	defaultName = "星屑"
)

// opSettings 是 YAML 与运营覆盖合并后的生效配置。
type opSettings struct {
	Name      string
	ShowEntry bool
	// ConsumeBps 是消费返的全站默认比例(万分比);分组覆盖在 qy_sd_group_rate。
	ConsumeBps int
	// 四个邀请类正值都受支付合规门约束(D-G):合规未确认时按 0 生效,见 complianceGate。
	InviteTopupBps         int
	InviteRedeemBps        int
	InviteConsumeBps       int
	InviteRegisterStardust int64
	HeldAlertDays          int
}

// baseSettings 把一份 YAML 折成运营覆盖的基线。
//
// 单独成函数是因为它有两个消费方:effectiveCtx(生效值的起点)与配置接口的
// yaml_defaults(告诉运营"清掉覆盖会回到哪里")。两处各写一份对象字面量,
// 漂移的方向恰好是"界面上说默认全显示,实际默认全隐藏"。
func baseSettings(c config.Stardust) opSettings {
	name := strings.TrimSpace(c.Name)
	if name == "" {
		name = defaultName
	}
	return opSettings{
		Name:                   name,
		ShowEntry:              c.EntryShown(),
		ConsumeBps:             c.ConsumeBps,
		InviteTopupBps:         c.InviteTopupBps,
		InviteRedeemBps:        c.InviteRedeemBps,
		InviteConsumeBps:       c.InviteConsumeBps,
		InviteRegisterStardust: c.InviteRegisterStardust,
		HeldAlertDays:          c.HeldAlertDays,
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
// 少一个运营微调远比"星屑页整页打不开"轻。
//
// 合规门在**缓存之外**套:合规声明是主库 options 里的开关,管理员确认的那一刻
// 就该生效,不该被这里的 60 秒快照钉住。
func effectiveCtx(ctx context.Context) opSettings {
	base := baseSettings(config.Get().Stardust)

	settingsMu.Lock()
	if settingsCache != nil && common.GetTimestamp()-settingsLoaded < settingsCacheSeconds {
		cached := *settingsCache
		settingsMu.Unlock()
		return complianceGate(cached)
	}
	epoch := settingsEpoch
	settingsMu.Unlock()

	rows, err := loadOverrides(ctx)
	if err != nil {
		settingsMu.Lock()
		defer settingsMu.Unlock()
		if settingsCache != nil {
			return complianceGate(*settingsCache)
		}
		return complianceGate(base)
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
	return complianceGate(merged)
}

// effective 是给拿不到调用方 ctx 的地方(后台任务、引导端点 hook)用的形态。
func effective() opSettings {
	// 自带一个冷路径预算而不是裸查:裸查会一直等到扩展库 DSN 的 readTimeout
	// (默认 30 秒),而调用点里有正持着余额行锁的结算事务。
	ctx, cancel := guard.ColdContext(context.Background())
	defer cancel()
	return effectiveCtx(ctx)
}

// InviteRateSnapshot 交出**生效**的三档邀请类比例(万分比)与注册奖星屑数。
//
// 唯一的消费方是 commission 的健康面板:佣金的三档(充值 / 消费 / 兑换码)与这里的
// 三档打的是同一笔基数,两边同时为正就会给同一个上线落两笔星屑。谁也无权替运营
// 决定留哪一条,但"现在两条都开着"必须能在一个屏幕上看见 —— 光看各自的配置页
// 永远看不出重叠。
//
// 给的是全站生效值(YAML + qy_settings 覆盖 + 合规门),**不含**分组覆盖:
// 分组档只会让某些人更高或更低,不会让"两条线都在发"这件事从真变假。
func InviteRateSnapshot() (topupBps, redeemBps, consumeBps int, registerStardust int64) {
	s := effective()
	return s.InviteTopupBps, s.InviteRedeemBps, s.InviteConsumeBps, s.InviteRegisterStardust
}

func invalidateSettings() {
	settingsMu.Lock()
	settingsCache = nil
	settingsLoaded = 0
	settingsEpoch++
	settingsMu.Unlock()
}

// complianceGate 是支付合规门(D-G):`!IsPaymentComplianceConfirmed()` 时四个邀请类
// 正值一律按 0 处理。星屑能在商城换套餐,有真实价值,与上游的邀请奖励同一道门。
func complianceGate(s opSettings) opSettings {
	if operation_setting.IsPaymentComplianceConfirmed() {
		return s
	}
	s.InviteTopupBps, s.InviteRedeemBps, s.InviteConsumeBps, s.InviteRegisterStardust = 0, 0, 0, 0
	return s
}

// isInviteKey 回答"这个键是不是受合规门约束的邀请类奖励"。写侧拒绝正值与
// 读侧归零必须是同一组键。
func isInviteKey(key string) bool {
	return key == keyInviteTopupBps || key == keyInviteRedeemBps ||
		key == keyInviteConsumeBps || key == keyInviteRegisterStardust
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
// 越界值一律**丢弃并回落 YAML**并打一条 SysError,而不是钳到边界:钳取会让运营
// 以为自己配的是 50%、实际跑的是 20%,那正是本仓反复栽跟头的"以为改了其实没改"。
// 读写两侧的区间都取自 settingBounds(),只有一个定义点。
func mergeOverrides(base opSettings, rows map[string]string) opSettings {
	if raw, ok := rows[keyName]; ok {
		if name, valid := validName(raw); valid {
			base.Name = name
		} else {
			discardOverride(keyName, raw)
		}
	}
	if raw, ok := rows[keyShowEntry]; ok {
		// 接口写进去的是 "0"/"1",历史上也可能写 "true"/"false",ParseBool 恰好都收。
		if v, err := strconv.ParseBool(strings.TrimSpace(raw)); err == nil {
			base.ShowEntry = v
		} else {
			discardOverride(keyShowEntry, raw)
		}
	}
	bounds := settingBounds()
	if v, ok := overrideInt(rows, keyConsumeBps, bounds[keyConsumeBps]); ok {
		base.ConsumeBps = int(v)
	}
	if v, ok := overrideInt(rows, keyInviteTopupBps, bounds[keyInviteTopupBps]); ok {
		base.InviteTopupBps = int(v)
	}
	if v, ok := overrideInt(rows, keyInviteRedeemBps, bounds[keyInviteRedeemBps]); ok {
		base.InviteRedeemBps = int(v)
	}
	if v, ok := overrideInt(rows, keyInviteConsumeBps, bounds[keyInviteConsumeBps]); ok {
		base.InviteConsumeBps = int(v)
	}
	if v, ok := overrideInt(rows, keyInviteRegisterStardust, bounds[keyInviteRegisterStardust]); ok {
		base.InviteRegisterStardust = v
	}
	if v, ok := overrideInt(rows, keyHeldAlertDays, bounds[keyHeldAlertDays]); ok {
		base.HeldAlertDays = int(v)
	}
	return base
}

// overrideInt 取一个整数覆盖,判据是写侧那一份 Bound —— 区间只有一个定义点,
// 读写两侧不可能各说各的。
func overrideInt(rows map[string]string, key string, b Bound) (int64, bool) {
	raw, ok := rows[key]
	if !ok {
		return 0, false
	}
	v, err := strconv.ParseInt(strings.TrimSpace(raw), 10, 64)
	if err != nil || !b.Contains(v) {
		discardOverride(key, raw)
		return 0, false
	}
	return v, true
}

func discardOverride(key, raw string) {
	common.SysError("qianye/stardust: qy_settings 里的 " + key + " = " + strconv.Quote(raw) +
		" 越界或不合法,已丢弃并回落 YAML 基线;请在管理端重新保存一个合法值")
}

// validName 校验单位名:去空白后非空且不超过 maxNameRunes 个字符。
// 与 config.validateStardust 对 YAML 基线的规则同值。
func validName(raw string) (string, bool) {
	name := strings.TrimSpace(raw)
	if name == "" || utf8.RuneCountInString(name) > maxNameRunes {
		return "", false
	}
	return name, true
}

// Bound 是一个数值键的取值区间(闭区间)。
type Bound struct {
	Lo int64
	Hi int64
}

// Contains 是这个区间**唯一**的判定点:写侧(saveOverrides)与读侧(mergeOverrides)
// 都必须过它,否则升级之前落库的越界覆盖会继续被读出来生效。
func (b Bound) Contains(v int64) bool { return v >= b.Lo && v <= b.Hi }

// settingBounds 是每个**数值**键的区间;name 是字符串,由 validName 单独管。
//
// 上界写死而不是取自 YAML(与 lottery 不同):这三档比例在 YAML 里的值是**默认值**
// 而不是硬顶(分组表也可以配得比它高),真正的硬顶是 config.validate 那道
// maxStardustBps,两侧同值。
func settingBounds() map[string]Bound {
	return map[string]Bound{
		keyShowEntry:              {Lo: 0, Hi: 1},
		keyConsumeBps:             {Lo: 0, Hi: maxBps},
		keyInviteTopupBps:         {Lo: 0, Hi: maxBps},
		keyInviteRedeemBps:        {Lo: 0, Hi: maxBps},
		keyInviteConsumeBps:       {Lo: 0, Hi: maxBps},
		keyInviteRegisterStardust: {Lo: 0, Hi: int64(common.MaxQuota)},
		keyHeldAlertDays:          {Lo: 0, Hi: maxHeldAlertDays},
	}
}

// Bounds 供管理端下发 bounds,与 settingBounds 同一份。
func Bounds() map[string]Bound { return settingBounds() }

// settingsSnapshot 是生效值的下发 / 审计形状,键名与 editableKeys 逐字一致。
//
// 用 map 而不是结构体:前端按 editable_keys 逐键取值渲染,两边靠同一组字符串对齐。
// 布尔下发 0/1、name 下发字符串,其余整数。
func settingsSnapshot(s opSettings) map[string]any {
	showEntry := int64(0)
	if s.ShowEntry {
		showEntry = 1
	}
	return map[string]any{
		keyName:                   s.Name,
		keyShowEntry:              showEntry,
		keyConsumeBps:             int64(s.ConsumeBps),
		keyInviteTopupBps:         int64(s.InviteTopupBps),
		keyInviteRedeemBps:        int64(s.InviteRedeemBps),
		keyInviteConsumeBps:       int64(s.InviteConsumeBps),
		keyInviteRegisterStardust: s.InviteRegisterStardust,
		keyHeldAlertDays:          int64(s.HeldAlertDays),
	}
}

// snapshot 是当前生效值的快照,给审计的 before / after 用。
func snapshot() map[string]any { return settingsSnapshot(effective()) }

// saveOverrides 校验并在一个扩展库事务内落盘一批覆盖值,成功后失效本进程缓存。
//
// 校验放在数据层而不是只放在 HTTP 处理器里:一个越界值一旦落库,读侧会把它丢弃
// 回落,配置页却显示"已保存" —— 那正是"看到的与生效的不一致"。校验一律
// "越界即拒绝",不夹到边界。patch 的值是十进制整数字面量(布尔写 0/1,
// true/false 也收),name 是原文。返回的错误是 bizError,可直接回给管理员。
func saveOverrides(ctx context.Context, patch map[string]string, operatorId int) error {
	if len(patch) == 0 {
		return nil
	}
	bounds := settingBounds()
	compliance := operation_setting.IsPaymentComplianceConfirmed()
	normalized := make(map[string]string, len(patch))
	for key, raw := range patch {
		switch {
		case key == keyName:
			name, valid := validName(raw)
			if !valid {
				return errBadSetting("单位名不能为空白,且不能超过 " + strconv.Itoa(maxNameRunes) + " 个字符")
			}
			normalized[key] = name
		case key == keyShowEntry:
			v, err := strconv.ParseBool(strings.TrimSpace(raw))
			if err != nil {
				return errBadSetting("配置项 " + key + " 只接受 0 / 1")
			}
			normalized[key] = "0"
			if v {
				normalized[key] = "1"
			}
		default:
			b, ok := bounds[key]
			if !ok {
				return errBadSetting("包含不可在线修改的配置项: " + key)
			}
			v, err := strconv.ParseInt(strings.TrimSpace(raw), 10, 64)
			if err != nil {
				return errBadSetting("配置项 " + key + " 的取值不是整数")
			}
			if !b.Contains(v) {
				return errBadSetting("配置项 " + key + " 的取值超出允许范围 [" +
					strconv.FormatInt(b.Lo, 10) + ", " + strconv.FormatInt(b.Hi, 10) + "]")
			}
			// 合规门的写侧:镜像上游 controller/option.go 对邀请奖励的判据,收到正值
			// 直接拒绝,而不是收下来再按 0 生效 —— 后者会让配置页显示一个并不在
			// 生效的数字。
			if v > 0 && isInviteKey(key) && !compliance {
				return errComplianceRequired
			}
			normalized[key] = strconv.FormatInt(v, 10)
		}
	}

	gdb := db.Get()
	if gdb == nil {
		return db.ErrNotReady
	}
	// 按键名排序后写入:给并发的两次保存一个固定的加锁顺序 —— 两个管理员同时
	// 保存互相交叉的键集时,乱序写正是死锁的配方。
	keys := make([]string, 0, len(normalized))
	for k := range normalized {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	now := common.GetTimestamp()
	rows := make([]qymodel.Setting, 0, len(keys))
	for _, k := range keys {
		rows = append(rows, qymodel.Setting{
			Scope: settingScope, K: k, V: normalized[k], OperatorId: operatorId, UpdatedAt: now,
		})
	}
	err := gdb.WithContext(ctx).Clauses(clause.OnConflict{
		Columns:   []clause.Column{{Name: "scope"}, {Name: "k"}},
		DoUpdates: clause.AssignmentColumns([]string{"v", "operator_id", "updated_at"}),
	}).Create(&rows).Error
	if err != nil {
		db.MarkFailure(err)
		return wrapInternal("保存运营参数", err)
	}
	invalidateSettings()
	return nil
}
