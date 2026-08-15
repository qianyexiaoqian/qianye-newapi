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
	// GrantNotes 是按格备注:用户分组 → 模型分组 → 说明文案。
	//
	// **只收非空备注**,而且刻意与 Grants 分开而不是把后者改成 map[string]string:
	// 成员资格是鉴权判据,备注是文案。合并成一张 map 之后,每一处
	// `_, ok := s.Grants[ug][mg]` 都要重新证明它读的是"在不在"而不是"写了没写",
	// 而那种改写正是把 `granted && note==""` 静默读成"不可用"的形状。
	// 分开之后 Grants 的类型不变,全部既有判定一个字都不用动。
	GrantNotes map[string]map[string]string
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
		"enabled":           enabled(),
		"snapshot_loaded":   s != nil,
		"refresh_fails":     refreshFails.Load(),
		"fallback_requests": fallbackRequests.Load(),
		"stale_warns":       staleWarns.Load(),
		"max_stale_seconds": maxStaleSeconds(),
		"write_guard_on":    cfg().WriteGuardOn(),
		"dropped_grants":    []string{},
		"managed_groups":    0,
		"enforced_groups":   0,
		"snapshot_age_secs": int64(0),
		"snapshot_version":  int64(0),
		// 套餐解锁此刻是否生效(开关在 plan_entitlement 段,这里只是把它与可选清单
		// 的状态摆在同一屏)。它关掉时套餐照常扣费、照常显示余额,唯一的表现是
		// 用户拿不到他买的模型分组 —— 那种失败在日志里没有任何形状,只能在这里报。
		"subscription_unlock_on": PlanUnlockEnabled(),
		// scoped_groups 是**已经显式设定过范围**的用户分组数。
		//
		// 新口径下 0 是完全正常的状态(全站都未设定范围 = 全部模型分组可用),
		// 所以它不进 needs_attention;它回答的是另一个问题:"这个模块此刻到底
		// 在管着谁"。没有这个数字,一次误删 scope 行与"本来就没配过"看起来一样。
		"scoped_groups": 0,
		// 已摘出 AutoMigrate 但数据仍留在扩展库里的表。
		//
		// 报它是为了回答一个必然会被问到的问题:"扩展库里怎么有一张不在任何
		// Tables() 里的表,是不是漏迁移了"。第二反应通常是"删掉试试",而那一步
		// 不可逆。清单与手工 DROP 语句见 qianye/docs/retired_tables.md。
		"retired_tables":     []string{"qy_group_seen", "qy_group_write_denies"},
		"needs_attention":    s == nil && enabled(),
		"unloaded_behaviour": "分组可选清单未加载,当前按上游全局白名单放行(读侧恒等返回,写侧校验一并放行)",
	}
	if s == nil {
		return out
	}
	// 有 scope 行就是生效,所以这三个键是同一个数:managed_groups 是"这个模块在管谁"、
	// enforced_groups 是"谁此刻真的被限制着"、scoped_groups 是"谁显式设过范围",
	// shadow 下线之后三个问题的答案合并了。**刻意不在这里再判一次 sc.Mode** ——
	// 快照里的 Mode 已由 buildSnapshot 归一化,判它只会把已经下线的第三档写回读取路径,
	// 而"读侧不看 mode、别处看 mode"正是本模块修过的那种分叉。三个键都留着:
	// 健康面板与孤儿基线面板都在读,合并成一个键换不来任何东西。
	scoped := len(s.Scopes)
	dropped := make([]string, 0, len(s.DroppedGrants))
	dropped = append(dropped, s.DroppedGrants...)
	age := common.GetTimestamp() - loadedAt.Load()
	stale := age > maxStaleSeconds()
	out["dropped_grants"] = dropped
	out["managed_groups"] = scoped
	out["enforced_groups"] = scoped
	out["scoped_groups"] = scoped
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
		GrantNotes:    make(map[string]map[string]string),
		DroppedGrants: make([]string, 0),
	}
	for _, sc := range scopes {
		if sc.UserGroup == "" {
			// 空用户分组是匿名口径,永远走上游(见 Resolve 的契约 (b))。
			// 让它出现在 Scopes 里只会制造一个永远不会被读到的配置项。
			continue
		}
		// mode 列已收敛成单一取值:**行存在即生效**。
		//
		// 存量的 shadow 行由 migrateShadowScopesToEnforce 在启动时改写,这里
		// 只兜住"迁移还没跑到 / 有人手改了库"这两种情况。方向选生效而不是不生效:
		// 不生效正是刚刚被下线的那个形状(配好了、保存成功、什么都没发生),
		// 而生效至少是可见的 —— 运营会立刻看到分组变了。
		if sc.Mode != ModeEnforce {
			common.SysError(fmt.Sprintf(
				"qianye/groupmatrix: 用户分组 %q 的 mode=%q 不是 enforce(shadow 已下线),"+
					"按生效处理", sc.UserGroup, sc.Mode))
			sc.Mode = ModeEnforce
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
		if g.Note != "" {
			// 只给写过备注的格子建二层 map:绝大多数格子没有备注,
			// 而热路径每次 enforce 解析都要走一次 GrantNotes[ug] —— 缺键即回落,零分配。
			notes, ok := s.GrantNotes[g.UserGroup]
			if !ok {
				notes = map[string]string{}
				s.GrantNotes[g.UserGroup] = notes
			}
			notes[g.ModelGroup] = g.Note
		}
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
					"「用户分组」页上这些格子看起来是通的,实际会被上游的「分组已被弃用」挡掉",
				len(s.DroppedGrants), n, s.DroppedGrants))
		}
	}
	return s
}

// migrateShadowScopesToEnforce 把存量的 shadow 行改写成 enforce。
//
// ═══════════════════════ 为什么这次迁移必须做,而且必须在启动时做 ═══════════════════════
//
// shadow 的语义是「清单已配、但一个字节都不生效」。它下线之后,读取侧不再判 mode ——
// 也就是说那些行**从这次升级起自动开始生效**。留着 mode='shadow' 这个值不改,
// 库里就会有一批"写着影子、实际在生效"的行:任何人去查库都会得到与线上相反的结论,
// 而这类不一致正是排障时最贵的东西。
//
// 迁移只改 mode 一个字段,grants 一条都不动 —— 那份清单本来就是运营亲手勾的,
// 这次升级要做的恰恰是让它开始生效。
//
// 只在主节点跑:从节点同时改会互相覆盖同样的值,除了噪声没有别的效果。
func migrateShadowScopesToEnforce() {
	gdb := db.Get()
	if gdb == nil {
		return
	}
	res := gdb.Model(&Scope{}).Where("mode = ?", ModeShadow).
		Updates(map[string]any{"mode": ModeEnforce})
	if res.Error != nil {
		// 不阻断启动:快照编译那一步也会把非 enforce 的行按生效处理(见上面),
		// 所以行为已经是对的,这里失败只影响"库里的值与行为是否一致"。
		common.SysError("qianye/groupmatrix: shadow 范围迁移失败(行为不受影响,但库里的 mode 值会与实际不符): " +
			res.Error.Error())
		return
	}
	if res.RowsAffected > 0 {
		common.SysLog(fmt.Sprintf(
			"qianye/groupmatrix: 已把 %d 档用户分组的可用范围由「影子」改为立即生效 —— "+
				"这些档此前配好了清单但一个字节都没生效,现在它们的模型分组清单开始起作用",
			res.RowsAffected))
	}

	// 存量行的 allow_auto 一并抬成允许。
	//
	// 它当初是由 defaultAllowAuto 从**上游全局白名单里有没有 auto 键**推出来的,
	// 而那份白名单在本站是空的 —— 于是每一档都被自动禁掉了 auto,运营从来没做过
	// 这个决定。它是一个推导出来的artifact,不是一次选择,所以可以安全地纠正。
	//
	// auto 不放宽任何权限:令牌的 auto 候选仍会被这一档的可选清单过滤一遍。
	autoRes := gdb.Model(&Scope{}).Where("allow_auto = ?", false).
		Updates(map[string]any{"allow_auto": true})
	if autoRes.Error != nil {
		common.SysError("qianye/groupmatrix: allow_auto 迁移失败: " + autoRes.Error.Error())
		return
	}
	if autoRes.RowsAffected > 0 {
		common.SysLog(fmt.Sprintf(
			"qianye/groupmatrix: 已为 %d 档用户分组打开「允许 auto」—— "+
				"此前它是由一份空的全局白名单推导出来的,并非运营的选择;"+
				"用户从此可以自定义令牌的分组顺序并按序故障转移",
			autoRes.RowsAffected))
	}
}
