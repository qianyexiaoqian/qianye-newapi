package violation

import (
	"fmt"
	"sync/atomic"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/qianye/config"
)

// 熔断:规则事故的最后一道闸。
//
// # 为什么删掉了全局模式,熔断却留着
//
// 项目方拍板"删掉全局开关,模式只绑在规则上"。熔断看起来像是要被一起删掉的
// 那类东西 —— 它确实也会让规则不执行 —— 但它不是一个"模式":
//
//   - 模式是**人表达的意图**,人要理解它、切换它、为它负责;
//   - 熔断是**机器踩的刹车**,没有人打开过它,人只需要在它响的时候收到告警。
//
// 一条 `.*` 正则能在 30 秒内拦掉全站请求并把用户全部封号,而扣费与封号都很难
// 完全回滚(用户已经收到错误、已经被登出、已经发工单)。把这道自愈能力删掉,
// 换来的只是"少一个概念",代价是深夜无人值守时一次规则手滑没有任何东西拦得住。
// 所以这里的取舍是:**保留能力,取消它的"模式"身份** ——
//   - 它不再参与任何"当前是什么模式"的表达(shadowActive 已删除);
//   - 它是 effectiveShadow 里的一个运行期钳位:触发期间把**全部**规则按影子执行;
//   - 管理端只在它触发时显示一条告警 + 一个"解除"按钮,平时整块 UI 不存在。
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

// breakerTripped 返回熔断当前是否正在把全部规则钳成影子,以及触发描述。
//
// 只看 forcedShadowUntil 一个量。它**不是**"当前模式",唯一的调用方是
// effectiveShadow —— 那里先看规则自己的 Mode,规则说 enforce 时才轮到这道钳位。
func breakerTripped() (bool, string) {
	if common.GetTimestamp() >= forcedShadowUntil.Load() {
		return false, ""
	}
	r, _ := forcedShadowReason.Load().(string)
	if r == "" {
		r = ShadowReasonBreaker
	}
	return true, r
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

// clearForcedShadow 供管理端在确认规则已修正后手动解除熔断。
//
// 它只清 forcedShadowUntil。规则各自的 Mode 一个字节都不动 —— 解除熔断的语义是
// "刹车松开,各条规则回到它们自己声明的模式",顺手把规则转正就是替运营做了一个
// 他没做过的决定。
func clearForcedShadow() {
	forcedShadowUntil.Store(0)
	forcedShadowReason.Store("")
}

func breakerStats() map[string]any {
	tripped, reason := breakerTripped()
	return map[string]any{
		// forced_shadow 是唯一的"全站被钳成影子"信号。旧的 shadow /
		// shadow_reason / config_shadow / shadow_override / global_shadow 五个字段
		// 一起删掉了:它们描述的是已经不存在的全局模式层,留着只会让界面继续
		// 渲染一个改不动任何东西的开关。
		"forced_shadow":        tripped,
		"forced_shadow_reason": reason,
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
		// 频率计数的降级可见性。rate_local_hits > 0 表示计数正落在**每节点各数各的**
		// 进程内兜底上,多节点部署时单节点看到的条数只有真实值的约 1/N ——
		// 不把它摆出来,运营会照着被稀释的数字一路调低阈值。
		"rate_redis_fails": rateRedisFails.Load(),
		"rate_local_hits":  rateLocalHits.Load(),
		"rate_local_full":  rateLocalFull.Load(),
	}
}
