package violation

import (
	"fmt"
	"sync/atomic"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/qianye/config"
)

// 熔断:规则事故的最后一道闸。
//
// 一条 `.*` 正则能在 30 秒内拦掉全站请求并把用户全部封号,而扣费与封号都很难
// 完全回滚(用户已经收到错误、已经被登出、已经发工单)。所以除了默认开启的
// 影子模式,还必须有"跑着跑着发现不对就自动退回影子模式"的自愈能力。
//
// 判据刻意选了两个正交的量:
//   - 拦截率(命中/评估):规则写得太宽的直接信号;
//   - 封号速率(每小时):即使拦截率不高,批量封号也必须先停下来给人看一眼。
const (
	// blockRateMinSamples 是拦截率生效所需的最小样本量。
	// 没有它,上线后第一个请求被拦就会算出 100% 拦截率并立刻自我熔断。
	blockRateMinSamples = 200
	// forcedShadowSeconds 是自动回落影子模式的持续时长。到期自动恢复,
	// 若问题仍在会再次触发 —— 比"永久锁死等人工"更适合无人值守的深夜。
	forcedShadowSeconds = 1800
	// banWindowSeconds 是封号速率的统计窗口。
	banWindowSeconds = 3600
)

var (
	scanTotal   atomic.Int64 // 累计评估次数(仅统计展示)
	blockTotal  atomic.Int64 // 累计拦截次数
	shadowHits  atomic.Int64 // 影子模式下的命中次数(切真实模式前的决策依据)
	recordDrops atomic.Int64 // 落库失败/丢弃次数

	// 滚动窗口计数。用"整窗清零"而非滑动窗口:代价是边界处判据略钝,
	// 换来的是零分配、零锁,适合放在 relay 热路径上。
	winStart   atomic.Int64
	winScanned atomic.Int64
	winBlocked atomic.Int64

	banWinStart atomic.Int64
	banWinCount atomic.Int64

	forcedShadowUntil  atomic.Int64
	forcedShadowReason atomic.Value // string
	forcedShadowCount  atomic.Int64
)

// windowSeconds 是拦截率的统计窗口。与规则刷新周期同量级:
// 窗口太长,事故要很久才被发现;太短,样本量凑不够。
const windowSeconds = 300

// shadowActive 返回当前**全局**是否处于影子模式,以及原因。
//
// 影子模式下:规则照常匹配、照常落一条记录、照常进统计,但
// **不扣费、不阻断、不封号、也不推进违规计数**(裁决 2)。
// 最后一条是本轮修的 P0:此前影子命中照样调 bumpCounter,而封号判据完全由
// 持久化的 hit_count 推导,于是影子命中把用户推过封号线、下一次真实命中直接落
// 封禁行 —— 正好是"不会真实执行"的反面。计数跳过点在 guard.go 的 persist。
//
// 全局取值有两个来源:管理端写在 qy_settings 的覆盖(优先),没有覆盖时回落
// YAML 的 violation.shadow_mode。叠加规则级 dry_run 在调用方完成,取更保守者胜。
func shadowActive() (bool, string) {
	if on, source := globalShadowMode(); on {
		return true, source
	}
	if common.GetTimestamp() < forcedShadowUntil.Load() {
		r, _ := forcedShadowReason.Load().(string)
		if r == "" {
			r = "breaker"
		}
		return true, r
	}
	return false, ""
}

// noteScan 记录一次规则评估,并在拦截率越界时自动回落影子模式。
func noteScan(blocked bool) {
	scanTotal.Add(1)
	if blocked {
		blockTotal.Add(1)
	}

	now := common.GetTimestamp()
	start := winStart.Load()
	if now-start >= windowSeconds {
		// 只有抢到 CAS 的那个请求负责换窗,其余请求把计数记进新窗口即可。
		if winStart.CompareAndSwap(start, now) {
			winScanned.Store(0)
			winBlocked.Store(0)
		}
	}
	scanned := winScanned.Add(1)
	var bl int64
	if blocked {
		bl = winBlocked.Add(1)
	} else {
		bl = winBlocked.Load()
	}

	limit := int64(config.Get().Violation.GlobalBlockRateLimitBps)
	if limit <= 0 || scanned < blockRateMinSamples {
		return
	}
	if bl*10000 > scanned*limit {
		tripShadow(fmt.Sprintf("block_rate %d/%d 超过 %d bps", bl, scanned, limit))
	}
}

// noteBan 记录一次自动封号。
func noteBan() {
	now := common.GetTimestamp()
	start := banWinStart.Load()
	if now-start >= banWindowSeconds {
		if banWinStart.CompareAndSwap(start, now) {
			banWinCount.Store(0)
		}
	}
	banWinCount.Add(1)
}

// banRateExceeded 判断自动封号是否已达小时上限。超限后只记录、不执行封号,
// 让管理员先看一眼再决定 —— 批量封号造成的信任损失远大于晚封几分钟。
func banRateExceeded() bool {
	limit := int64(config.Get().Violation.GlobalBanRateLimitPerHour)
	if limit <= 0 {
		return false
	}
	if common.GetTimestamp()-banWinStart.Load() >= banWindowSeconds {
		return false
	}
	if banWinCount.Load() >= limit {
		tripShadow(fmt.Sprintf("ban_rate %d/h 达到上限 %d", banWinCount.Load(), limit))
		return true
	}
	return false
}

// tripShadow 强制回落影子模式并告警。重复触发只延长窗口,不重复刷屏。
func tripShadow(reason string) {
	now := common.GetTimestamp()
	until := now + forcedShadowSeconds
	prev := forcedShadowUntil.Swap(until)
	forcedShadowReason.Store(reason)
	if prev > now {
		return
	}
	forcedShadowCount.Add(1)
	common.SysError("qianye/violation: 已自动回落影子模式(不再扣费/阻断/封号),原因: " + reason +
		" —— 请立即检查最近改动的规则")
}

// clearForcedShadow 供管理端在确认规则已修正后手动解除**熔断**。
//
// 它只清 forcedShadowUntil。全局影子开关(qy_settings 覆盖 / YAML)不在它的
// 管辖范围内 —— 那条路要走 adminPutMode。这一点必须由界面区分清楚:
// 界面上曾经只在 forced 为真时才渲染这个按钮,于是配置态影子下整页一个可点的
// 控件都没有,"无法调整模式"的直接观感就来自这里。
func clearForcedShadow() {
	forcedShadowUntil.Store(0)
	forcedShadowReason.Store("")
}

func breakerStats() map[string]any {
	shadow, reason := shadowActive()
	globalShadow, _ := globalShadowMode()
	return map[string]any{
		"shadow":        shadow,
		"shadow_reason": reason,
		// config_shadow 是 YAML 那一行的原值(覆盖被清掉后会回到它),
		// shadow_override 是管理端写在 qy_settings 的覆盖态(on/off/unset),
		// global_shadow 是两者合并后、尚未叠加熔断与规则级 dry_run 的全局取值。
		// 三个都下发是刻意的:界面必须能回答"现在这个模式是谁定的、清掉覆盖会变成什么"。
		"config_shadow":        config.Get().Violation.IsShadow(),
		"shadow_override":      overrideName(),
		"global_shadow":        globalShadow,
		"shadow_loaded_at":     modeLoadedAt.Load(),
		"shadow_load_fails":    modeLoadFail.Load(),
		"forced_shadow_until":  forcedShadowUntil.Load(),
		"forced_shadow_count":  forcedShadowCount.Load(),
		"window_scanned":       winScanned.Load(),
		"window_blocked":       winBlocked.Load(),
		"block_rate_limit_bps": config.Get().Violation.GlobalBlockRateLimitBps,
		"ban_window_count":     banWinCount.Load(),
		"ban_rate_limit_hour":  config.Get().Violation.GlobalBanRateLimitPerHour,
		"scan_total":           scanTotal.Load(),
		"block_total":          blockTotal.Load(),
		"shadow_hits":          shadowHits.Load(),
		"record_drops":         recordDrops.Load(),
		"scan_timeouts":        scanTimeouts.Load(),
		"rule_refresh_fails":   refreshFails.Load(),
	}
}
