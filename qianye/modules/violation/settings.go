package violation

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"sync/atomic"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/qianye/config"
	"github.com/QuantumNous/new-api/qianye/db"
	"github.com/QuantumNous/new-api/qianye/guard"
	qymodel "github.com/QuantumNous/new-api/qianye/model"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// settings.go —— 全局影子开关的运营覆盖层。
//
// # 为什么必须有这一层
//
// 在此之前 violation.shadow_mode 只存在于 YAML,而 IsShadow() 的默认值是 true:
// 也就是说**全新部署一律处于影子模式,且管理端没有任何办法退出**。
// 唯一的写入口是 POST /api/qy/admin/config/reload,而它只是重读磁盘上的 YAML;
// 规则级 dry_run 是端到端通的,但叠加语义取更保守者胜,全局为真时规则级无从覆盖。
// 结果就是需求原文说的「违规规则无法调整模式」。
//
// 形状照抄 qianye/modules/commission/settings.go 与 qianye/modules/transfer/settings.go
// (同一轮里 transfer 也在做同一件事,scope 不同、口径一致):
// YAML 是底,qy_settings 是覆盖,消费方读到的永远是合并后的单一取值。
//
// # 与那两个模块唯一的形状差异:缓存刷新不能查库
//
// commission / transfer 的 effectiveCtx 允许在调用方线程里惰性查库,因为它们的
// 消费方都在冷路径(HTTP 处理器、结算任务)。本模块的消费方 shadowActive() 挂在
// relay 热路径上(PreRelayGuard / PostRelayGuard),那里一次查库就是全站延迟。
// 因此这里改用 rules.go 里 maybeRefresh 的同一套形状:进程内 atomic 快照 +
// 到期后经 guard.HotAsync 异步重载,热路径只做一次 atomic 比较。
const settingScope = "violation"

// keyShadowMode 是全局影子开关在 qy_settings 里的行键。
//
// 取值是十进制整数字符串 "1"(影子)/ "0"(真实执行),与 transfer 那边
// "运营配置一律整数字符串"的口径一致。行不存在 = 没有覆盖 = 回落 YAML。
//
// 刻意**不抄** commission / transfer 那份 editableKeys 白名单:那两个模块的管理端
// 收的是任意 {key: value} 补丁,必须有白名单挡住"往共享 KV 表里写别的模块的键"。
// 这里只有一个开关,接口收的是 {"shadow": true|false|null} —— 键名根本不来自请求,
// 白名单会是一张零消费方的表,而零消费方的配置面正是本仓库反复栽跟头的形状。
const keyShadowMode = "shadow_mode"

// 影子覆盖的三态。刻意让"未加载 / 无覆盖"落在零值上:进程刚起、还没读过库时
// atomic.Uint32 就是 0,globalShadowMode 会回落 YAML(默认 true = 影子),
// 也就是更保守的一侧,不需要额外的初始化步骤去保证这一点。
const (
	shadowUnset uint32 = iota // qy_settings 里没有这一行,或还没读过库
	shadowOff                 // 覆盖为"真实执行"
	shadowOn                  // 覆盖为"影子"
)

// 影子来源。写进 breaker 状态与前端横幅,让管理员一眼看出"现在这个模式是谁定的"。
const (
	shadowSourceSettings = "settings" // 管理端在 qy_settings 里设的
	shadowSourceConfig   = "config"   // YAML 的兜底默认值
)

// modeCacheSeconds 是影子开关快照的刷新周期。
//
// 比规则快照(默认 60 秒)短一半:这是个安全阀,"关掉影子模式多久才真的开始执行"
// 与"发现规则写错后多久才真的停下来"都由它决定,而它只是一次主键查询。
const modeCacheSeconds = 30

var (
	shadowOverride    atomic.Uint32
	modeNextRefreshAt atomic.Int64
	modeLoadedAt      atomic.Int64
	// modeEpoch 是快照的代次,每次 invalidateMode 自增。
	//
	// 异步刷新在途时管理员改了开关:那份在途的旧值会把刚写下的新值静默盖掉,
	// 此后 30 秒全站仍按旧模式跑 —— 而这正是"我明明关掉了影子模式却没生效"
	// 这类投诉最难查的形态。代次让写回方能发现"我读的那一版已经作废了"。
	modeEpoch    atomic.Uint64
	modeWarnAt   atomic.Int64
	modeLoadFail atomic.Int64
)

// globalShadowMode 返回全局影子开关的生效值与来源。
//
// 叠加语义(shadowActive → 规则级 dry_run)保持不变:全局是一票否决的总闸,
// 规则级只能让单条规则更保守,不能让它更激进。
//
// 覆盖为 shadowOff 时**不再回落 YAML** —— 那正是这一层存在的意义:
// YAML 默认 true,不允许覆盖就等于永远退不出影子模式。
func globalShadowMode() (bool, string) {
	maybeRefreshMode()
	switch shadowOverride.Load() {
	case shadowOn:
		return true, shadowSourceSettings
	case shadowOff:
		return false, ""
	}
	if config.Get().Violation.IsShadow() {
		return true, shadowSourceConfig
	}
	return false, ""
}

// overrideName 把当前覆盖态摊成对外字符串,供管理端界面判断"要不要显示恢复默认"。
func overrideName() string {
	switch shadowOverride.Load() {
	case shadowOn:
		return "on"
	case shadowOff:
		return "off"
	}
	return "unset"
}

// maybeRefreshMode 是快照的自维护入口,与 rules.go 的 maybeRefresh 同形。
//
// 刻意放在消费方(globalShadowMode)里而不是挂在 PreRelayGuard/PostRelayGuard 上:
// 挂在调用点上的话,任何一个新增的消费方都得记得再挂一次,而"配置定义了、
// 调度层没接上"正是本仓库反复出现的断链形状。放在消费方里就不可能漏。
func maybeRefreshMode() {
	now := common.GetTimestamp()
	next := modeNextRefreshAt.Load()
	if now < next {
		return
	}
	if !modeNextRefreshAt.CompareAndSwap(next, now+modeCacheSeconds) {
		return // 别的请求已经抢到刷新权
	}
	guard.HotAsync("violation.mode_refresh", func(ctx context.Context) error {
		return refreshMode(ctx)
	})
}

// invalidateMode 让下一次读立刻回源,并作废所有在途的刷新结果。
func invalidateMode() {
	modeEpoch.Add(1)
	modeNextRefreshAt.Store(0)
}

// refreshMode 从扩展库重新读一次全局影子开关。
func refreshMode(ctx context.Context) error {
	gdb := db.Get()
	if gdb == nil {
		return db.ErrNotReady
	}
	// 句柄在交出去之前就绑上 ctx:refreshModeWith 会再绑一次(WithContext 幂等),
	// 但"交出去的句柄必须已经带着预算"这条是 qianye/ctx_guard_test.go 的硬约束 ——
	// 没接 ctx 换来的不是"不会被取消",是"没有任何上界"。
	return refreshModeWith(ctx, gdb.WithContext(ctx))
}

// refreshModeWith 是上面那条逻辑的本体,gdb 由调用方注入以便直接测。
//
// # 读失败为什么保留上一份快照而不是回落 YAML
//
// 回落 YAML 的方向是**变严**(默认 true = 影子),看起来安全,实则是另一种事故:
// 运营已经确认误判率、关掉了影子模式,扩展库抖一下全站风控就静默退回"只记录",
// 而且没有任何人会收到通知。沿用上一份快照最多陈旧几十秒,并且失败会计数告警。
//
// # 值非法时为什么强制影子
//
// qy_settings 是可以被人手工 UPDATE 的。一个解析不出来的值意味着"没人知道现在
// 该是什么模式",此时唯一不会造成不可逆损失的选择是不扣费不封号。
func refreshModeWith(ctx context.Context, gdb *gorm.DB) error {
	epoch := modeEpoch.Load()

	var row qymodel.Setting
	err := gdb.WithContext(ctx).
		Where("scope = ? AND k = ?", settingScope, keyShadowMode).Take(&row).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		storeMode(epoch, shadowUnset)
		return nil
	}
	if err != nil {
		db.MarkFailure(err)
		modeLoadFail.Add(1)
		warnThrottled("读取全局影子开关失败,继续沿用上一份快照: " + err.Error())
		return err
	}

	state, perr := parseShadowValue(row.V)
	if perr != nil {
		modeLoadFail.Add(1)
		warnThrottled(fmt.Sprintf(
			"qy_settings 里的 %s.%s = %q 无法解析,本次强制按影子模式运行(不扣费/不阻断/不封号): %v",
			settingScope, keyShadowMode, row.V, perr))
		storeMode(epoch, shadowOn)
		return nil
	}
	storeMode(epoch, state)
	return nil
}

// storeMode 在代次未变时写回快照。
//
// 代次变了说明这份结果在途期间已被 invalidateMode 作废(管理端刚改过开关),
// 此时必须丢弃 —— 写回等于把一次已经生效的切换按回去,而且会盖上一个新鲜的
// 时间戳,让后续 modeCacheSeconds 秒都读不到真值。
func storeMode(epoch uint64, state uint32) {
	if modeEpoch.Load() != epoch {
		return
	}
	shadowOverride.Store(state)
	modeLoadedAt.Store(common.GetTimestamp())
}

// parseShadowValue 解析 qy_settings 里的开关取值。
//
// 主写法是 "1" / "0"(与 transfer 的整数字符串口径一致);
// 同时收 true/false,因为这一行迟早会被人用 SQL 直接改。
func parseShadowValue(raw string) (uint32, error) {
	s := strings.ToLower(strings.TrimSpace(raw))
	switch s {
	case "true":
		return shadowOn, nil
	case "false":
		return shadowOff, nil
	}
	v, err := strconv.Atoi(s)
	if err != nil {
		return shadowUnset, fmt.Errorf("%q 不是 0/1", raw)
	}
	switch v {
	case 0:
		return shadowOff, nil
	case 1:
		return shadowOn, nil
	}
	return shadowUnset, fmt.Errorf("取值 %d 超出允许区间 [0, 1]", v)
}

func formatShadowValue(shadow bool) string {
	if shadow {
		return "1"
	}
	return "0"
}

// writeShadowSetting 落一条全局影子开关的运营覆盖。
//
// 只有一个键,因此不需要 commission / transfer 那套"整批一个事务 + 键名排序"的
// 死锁防护 —— 那套防护针对的是"一半键写进去、另一半 400"的中间态,单键写入
// 天然不存在这种状态。调用方负责写审计,成功与失败都要写。
func writeShadowSetting(ctx context.Context, gdb *gorm.DB, shadow bool, operatorId int) error {
	if gdb == nil {
		return db.ErrNotReady
	}
	row := qymodel.Setting{
		Scope:      settingScope,
		K:          keyShadowMode,
		V:          formatShadowValue(shadow),
		OperatorId: operatorId,
		UpdatedAt:  common.GetTimestamp(),
	}
	err := gdb.WithContext(ctx).Clauses(clause.OnConflict{
		Columns:   []clause.Column{{Name: "scope"}, {Name: "k"}},
		DoUpdates: clause.AssignmentColumns([]string{"v", "operator_id", "updated_at"}),
	}).Create(&row).Error
	if err != nil {
		db.MarkFailure(err)
	}
	return err
}

// dropShadowSetting 删除覆盖,让全局模式重新跟随 YAML。
//
// 这是一个真实的运营动作而不是"清理残留":YAML 里的 shadow_mode 是发布口径,
// 覆盖是临时处置。事故处理完之后必须有办法把临时处置收回去,否则 YAML 那一行
// 从此永远说不上话,下一个人改它会以为改了没生效。
func dropShadowSetting(ctx context.Context, gdb *gorm.DB) error {
	if gdb == nil {
		return db.ErrNotReady
	}
	err := gdb.WithContext(ctx).
		Where("scope = ? AND k = ?", settingScope, keyShadowMode).
		Delete(&qymodel.Setting{}).Error
	if err != nil {
		db.MarkFailure(err)
	}
	return err
}

// warnThrottled 按 modeCacheSeconds 节流地打一条配置告警。
//
// 读失败与非法值都是"每个刷新周期都会重现"的状态:不节流就会把日志刷满,
// 真正需要看到的那条反而被埋掉。
func warnThrottled(msg string) {
	now := common.GetTimestamp()
	last := modeWarnAt.Load()
	if now-last < modeCacheSeconds {
		return
	}
	if !modeWarnAt.CompareAndSwap(last, now) {
		return
	}
	common.SysError("qianye/violation: " + msg)
}
