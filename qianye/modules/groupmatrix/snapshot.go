package groupmatrix

import (
	"context"
	"fmt"
	"sort"
	"sync/atomic"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/qianye/config"
	"github.com/QuantumNous/new-api/qianye/db"
	"github.com/QuantumNous/new-api/qianye/guard"
	"github.com/QuantumNous/new-api/setting/ratio_setting"
)

// Snapshot 是权威可选清单的只读内存视图。
//
// 整体替换(atomic.Pointer),读端零锁;正在使用旧快照的请求持有引用,
// 不会读到半成品。**调用方禁止修改它内部的任何 map。**
type Snapshot struct {
	// Version 取 scopes/grants 里最大的 updated_at,只用于展示与排障。
	Version int64
	// Scopes 是"已被接管"的用户分组。键不存在 = inherit = 上游说了算。
	Scopes map[string]Scope
	// Grants 是每个被接管用户分组的可选模型分组集合。
	// 被接管但没有任何 grant 时,这里是一张**空 map 而不是缺键** ——
	// 空清单是合法配置(隔离组),必须与"没配过"区分开。
	Grants map[string]map[string]struct{}
	// DroppedGrants 记录编译期被剔除的清单项(模型分组已从分组倍率表删除)。
	// 它进 /admin/health 与矩阵页告警,不参与判定。
	DroppedGrants []string
}

var (
	current atomic.Pointer[Snapshot]
	// loadedAt 刻意放在 Snapshot 之外:Snapshot 一旦发布就是只读的,
	// 在里面就地改时间戳会与并发读构成数据竞争。
	loadedAt      atomic.Int64
	nextRefreshAt atomic.Int64
	refreshFails  atomic.Int64
	// staleWarns 统计"快照已陈旧"的告警次数。注意与 grouppricing 的 staleDrops
	// 语义不同:那边陈旧即丢弃,这里陈旧只告警,快照照常生效。
	staleWarns atomic.Int64
	// fallbackRequests 统计"因为快照从未加载成功而回落上游"的次数。
	// 回落可以接受,回落无声不行。
	fallbackRequests atomic.Int64
	// droppedWarns 是"清单项的模型分组已从倍率表消失"告警的限频计数。
	droppedWarns atomic.Int64
)

// hotAsync 是异步刷新的派发口。抽成变量只为让测试能同步执行它 ——
// 生产默认必须是 guard.HotAsync(有界队列 + 独立 worker + panic 拦截)。
var hotAsync = guard.HotAsync

func cfg() config.GroupMatrix { return config.Get().GroupMatrix }

// enabled 是 L1 kill switch 的判据。
//
// 它**刻意不看扩展库是否可用**:即使扩展库挂了、快照是坏的,读 YAML 快照的
// 那次 atomic load 也走得到。这才叫 kill switch —— 一个需要数据库活着才能
// 关掉的开关,在数据库出事时正好关不掉。
func enabled() bool { return config.Enabled() && cfg().Enabled }

func cacheSeconds() int64 {
	n := int64(cfg().CacheSeconds)
	if n <= 0 {
		n = 30
	}
	return n
}

func maxStaleSeconds() int64 {
	n := int64(cfg().MaxStaleSeconds)
	if n <= 0 {
		n = 300
	}
	if c := cacheSeconds(); n < c {
		n = c
	}
	return n
}

// activeSnapshot 返回当前生效的快照,从未成功加载过时返回 nil。
//
// ══════════════ 降级方向:永久沿用最后一份成功快照 ══════════════
//
// 这一条与 qianye/modules/grouppricing 的 activeSnapshot **刻意相反**,
// 那边超过 max_stale 就丢弃快照回落"无覆盖"。两者的判据不同:
//
//	grouppricing 陈旧 = 按一份来历不明的旧规则**扣钱**  → 丢弃更安全
//	groupmatrix  陈旧 = **可见性**略旧                   → 保留更安全
//
// 因为丢弃这份快照只有两个去处:回落上游(更宽松)或全站 403。
// 而"回落上游"不是无害地回到今天的状态 —— 本站有 `管理员分组=0`、
// `浅夜の自己人=0`、`清芯=0` 三个 ratio=0 的模型分组,收紧的目的之一就是
// 不让普通用户选中它们。扩展库抖动的那几秒里回落上游,等于被收紧的用户
// 重新可以把令牌指向免费分组。**那是真实的资金泄漏,不是"短暂宽松"。**
//
// 按「资金安全 > 可回退」排序,last-known-good 是唯一正确解。
// 代价(一次撤销可能延迟生效)由陈旧告警兜底。
func activeSnapshot() *Snapshot {
	s := current.Load()
	if s == nil {
		return nil
	}
	if age := common.GetTimestamp() - loadedAt.Load(); age > maxStaleSeconds() {
		if n := staleWarns.Add(1); n == 1 || n%1000 == 0 {
			common.SysError(fmt.Sprintf(
				"qianye/groupmatrix: 可选清单快照已陈旧 %d 秒(上限 %d),累计告警 %d 次 —— "+
					"快照仍在生效(刻意的:回落上游会让被收紧的用户重新选到免费分组),"+
					"但最近一次矩阵改动可能还没在本节点生效,请检查扩展库健康状态",
				age, maxStaleSeconds(), n))
		}
	}
	return s
}

// SnapshotView 是给管理端与健康面板的只读视图。
// 第二个返回值为 false 表示**从未**成功加载过。
func SnapshotView() (*Snapshot, bool) {
	s := current.Load()
	return s, s != nil
}

// Health 是 /admin/health 里本模块那一段。
//
// 回落可以接受,回落无声不行:snapshot_loaded=false 必须能一眼看到,
// 并且文案要直说"当前按上游全局白名单放行"。
func Health() map[string]any {
	s := current.Load()
	out := map[string]any{
		"enabled":            enabled(),
		"snapshot_loaded":    s != nil,
		"refresh_fails":      refreshFails.Load(),
		"fallback_requests":  fallbackRequests.Load(),
		"stale_warns":        staleWarns.Load(),
		"max_stale_seconds":  maxStaleSeconds(),
		"write_guard_on":     cfg().WriteGuardOn(),
		"dropped_grants":     []string{},
		"managed_groups":     0,
		"enforced_groups":    0,
		"snapshot_age_secs":  int64(0),
		"snapshot_version":   int64(0),
		"needs_attention":    s == nil && enabled(),
		"unloaded_behaviour": "分组可选清单未加载,当前按上游全局白名单放行(读侧恒等返回,写侧校验一并放行)",
	}
	if s == nil {
		return out
	}
	managed, enforced := 0, 0
	for _, sc := range s.Scopes {
		managed++
		if sc.Mode == ModeEnforce {
			enforced++
		}
	}
	dropped := make([]string, 0, len(s.DroppedGrants))
	dropped = append(dropped, s.DroppedGrants...)
	age := common.GetTimestamp() - loadedAt.Load()
	stale := age > maxStaleSeconds()
	out["dropped_grants"] = dropped
	out["managed_groups"] = managed
	out["enforced_groups"] = enforced
	out["snapshot_age_secs"] = age
	out["snapshot_version"] = s.Version
	out["snapshot_stale"] = stale
	// 陈旧**必须**进 needs_attention。本模块刻意选择了"陈旧不丢弃",
	// 因此陈旧是唯一一个「清单在生效、但内容可能是错的」状态:
	// 撤销已经保存成功,本节点却仍按旧清单放行。说好用来兜底的告警
	// 如果不出现在健康面板上,那条设计选择就没有任何补偿。
	out["needs_attention"] = stale || len(dropped) > 0
	return out
}

// maybeRefresh 是快照的自维护入口:读路径上只做一次 atomic 比较,
// 到期才通过 HotAsync 触发一次异步重载 —— 绝不在请求线程里回源。
//
// 为什么不用后台协程:快照是"每个节点各自持有"的进程内缓存,lease.Run 只会让
// 一个节点刷新,其余节点的清单永远陈旧。CAS 推进下次刷新时间既避免了并发重复
// 加载,也让无流量的节点零开销。
func maybeRefresh() {
	now := common.GetTimestamp()
	next := nextRefreshAt.Load()
	if now < next {
		return
	}
	if !nextRefreshAt.CompareAndSwap(next, now+cacheSeconds()) {
		return // 别的请求已经抢到刷新权
	}
	hotAsync("groupmatrix.snapshot_refresh", func(ctx context.Context) error {
		return reloadCtx(ctx)
	})
}

// reload 用冷路径预算重载快照,供管理端写入后刷新与启动预热使用。
func reload() error {
	ctx, cancel := guard.ColdContext(context.Background())
	defer cancel()
	return reloadCtx(ctx)
}

// InvalidateAndReload 让本节点立刻用上新清单,不等下一个刷新周期。
// 其它节点靠周期刷新感知(最多晚 cache_seconds)。
func InvalidateAndReload() error {
	nextRefreshAt.Store(0)
	return reload()
}

// reloadCtx 重新构建快照。
//
// 失败时**不动 current**:保留上一次成功的快照(见 activeSnapshot 的降级说明)。
//
// ctx 必须一路挂到 GORM 语句上:guard 承诺的超时只对 WithContext(ctx) 的语句生效。
// 漏接的后果不只是这条语句慢 —— 刷新由 relay 流量触发,很容易占满仅有的几个
// hot worker 长达 readTimeout,这期间返佣事件会把队列填满并溢出丢弃。
func reloadCtx(ctx context.Context) error {
	gdb := db.Get()
	if gdb == nil {
		refreshFails.Add(1)
		return db.ErrNotReady
	}
	gdb = gdb.WithContext(ctx)

	var scopes []Scope
	if err := gdb.Order("user_group asc").Find(&scopes).Error; err != nil {
		refreshFails.Add(1)
		return err
	}

	limit := cfg().MaxGrants
	if limit <= 0 {
		limit = 2000
	}
	var grants []Grant
	if err := gdb.Order("id asc").Limit(limit + 1).Find(&grants).Error; err != nil {
		refreshFails.Add(1)
		return err
	}
	if len(grants) > limit {
		// 截断而不是全量加载:上限存在的意义就是"每个节点每个周期要拉的行数有界"。
		// 但截断会让一部分授权静默消失,而消失的表现是用户 403 —— 必须喊出来。
		grants = grants[:limit]
		common.SysError(fmt.Sprintf(
			"qianye/groupmatrix: 清单行数超过 group_matrix.max_grants(%d),按 id 升序只加载前 %d 条,"+
				"其余授权不会生效 —— 相关用户会在请求时收到「无权访问 X 分组」", limit, limit))
	}

	current.Store(buildSnapshot(scopes, grants))
	loadedAt.Store(common.GetTimestamp())
	return nil
}

// buildSnapshot 把数据库行编译成只读视图。
//
// 编译期对每个 grant 的模型分组再跑一次 ratio_setting.ContainsGroupRatio:
// 分组可能在保存之后被从倍率表里删掉,只在保存时把关拦不住这种漂移。
// 不存在的一律**剔除**并告警 —— 留着它只会让 middleware/auth.go 在下一步
// 用「分组 %s 已被弃用」把用户挡掉,而矩阵页上那一格看起来是通的。
func buildSnapshot(scopes []Scope, grants []Grant) *Snapshot {
	s := &Snapshot{
		Scopes:        make(map[string]Scope, len(scopes)),
		Grants:        make(map[string]map[string]struct{}, len(scopes)),
		DroppedGrants: make([]string, 0),
	}
	for _, sc := range scopes {
		if sc.UserGroup == "" {
			// 空用户分组是匿名口径,永远走上游(见 Resolve 的契约 (b))。
			// 让它出现在 Scopes 里只会制造一个永远不会被读到的配置项。
			continue
		}
		if sc.Mode != ModeShadow && sc.Mode != ModeEnforce {
			common.SysError(fmt.Sprintf(
				"qianye/groupmatrix: 用户分组 %q 的 mode=%q 非法(可选 shadow|enforce),"+
					"按 shadow 处理(不生效)", sc.UserGroup, sc.Mode))
			sc.Mode = ModeShadow
		}
		s.Scopes[sc.UserGroup] = sc
		// 被接管但零授权时这里必须是**空 map 而不是缺键**:
		// 空清单(一个模型分组都不许用)是合法且危险的配置,必须可表达。
		s.Grants[sc.UserGroup] = map[string]struct{}{}
		if sc.UpdatedAt > s.Version {
			s.Version = sc.UpdatedAt
		}
	}
	for _, g := range grants {
		set, ok := s.Grants[g.UserGroup]
		if !ok {
			// 有授权行却没有 scope 行:该用户分组仍是 inherit,授权不生效。
			// 这是删 scope 行(L3 回退)之后的正常残留,不告警。
			continue
		}
		if g.ModelGroup == autoGroup {
			// auto 的可选性由 Scope.AllowAuto 控制,不走 grants。
			continue
		}
		if !ratio_setting.ContainsGroupRatio(g.ModelGroup) {
			s.DroppedGrants = append(s.DroppedGrants, g.UserGroup+"/"+g.ModelGroup)
			continue
		}
		set[g.ModelGroup] = struct{}{}
		if g.UpdatedAt > s.Version {
			s.Version = g.UpdatedAt
		}
	}
	if len(s.DroppedGrants) > 0 {
		sort.Strings(s.DroppedGrants)
		// 限频:快照每 cache_seconds 重建一次,不限频会把这条刷成背景噪声,
		// 而背景噪声与告警缺席是同一种失败。
		if n := droppedWarns.Add(1); n == 1 || n%200 == 0 {
			common.SysError(fmt.Sprintf(
				"qianye/groupmatrix: %d 条清单项的模型分组已不在分组倍率表里,已从快照剔除(累计告警 %d 次):%v —— "+
					"矩阵页上这些格子看起来是通的,实际会被上游的「分组已被弃用」挡掉",
				len(s.DroppedGrants), n, s.DroppedGrants))
		}
	}
	return s
}
