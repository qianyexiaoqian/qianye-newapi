package planentitlement

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

// Snapshot 是「套餐 → 解锁的模型分组 + 余额使用范围」的只读内存视图,也就是
// 设计里的**第一层快照**。
//
// 它同时服务三个消费者,而三者的运行环境天差地别:
//
//	权限解析     middleware/auth.go 的热路径(每个带令牌分组的请求一次)
//	余额过滤     model.PreConsumeUserSubscription 的**主库事务内、且候选行已持
//	             FOR UPDATE 锁**(由订阅侧的 hook 接过去)
//	管理端/对账  冷路径
//
// 因此它必须是**纯内存、零 I/O、零锁**:整体替换(atomic.Pointer),读端只做一次
// Load 加 map 查找。正在使用旧快照的请求持有引用,不会读到半成品。
// **调用方禁止修改它内部的任何 map。**
type Snapshot struct {
	// Version 取 grants/policies 里最大的 updated_at,只用于展示与排障。
	Version int64
	// Bind 是 planId → 解锁的模型分组集合。**缺键 ≡ 空集**(该套餐没配过解锁)。
	Bind map[int]map[string]struct{}
	// Scope 是 planId → 余额使用范围。**缺键 ≡ universal**(= 上游行为)。
	Scope map[int]string
	// Dropped 记录编译期被剔除的绑定项(模型分组已从分组倍率表消失)。
	// 它进 /admin/health 与管理端红色横幅,不参与判定。
	Dropped []string
}

var (
	current atomic.Pointer[Snapshot]
	// loadedAt 刻意放在 Snapshot 之外:Snapshot 一旦发布就是只读的,
	// 在里面就地改时间戳会与并发读构成数据竞争。
	loadedAt      atomic.Int64
	nextRefreshAt atomic.Int64
	refreshFails  atomic.Int64
	staleWarns    atomic.Int64
	droppedWarns  atomic.Int64
)

// hotAsync 是异步刷新的派发口。抽成变量只为让测试能同步执行它 ——
// 生产默认必须是 guard.HotAsync(有界队列 + 独立 worker + panic 拦截)。
var hotAsync = guard.HotAsync

func cfg() config.PlanEntitlement { return config.Get().PlanEntitlement }

// enabled 是本模块的 kill switch。
//
// 它**刻意不看扩展库是否可用**:即使扩展库挂了、快照是坏的,读 YAML 快照的
// 那次 atomic load 也走得到。一个需要数据库活着才能关掉的开关,在数据库出事时
// 正好关不掉。
//
// 与 group_matrix.enabled 分开是有意的:那个开关一旦被拉下,是为了让**收紧**
// 立刻失效;而套餐解锁被拉下的方向相反 —— 付了钱的用户当场少掉一批分组。
// 让一个"放松"功能和一个"收紧"功能共用一个开关,拉闸的人一定会误伤其中一侧。
func enabled() bool { return config.Enabled() && cfg().On() }

func cacheSeconds() int64 {
	n := int64(cfg().CacheSeconds)
	if n <= 0 {
		n = 30
	}
	return n
}

func maxStaleSeconds() int64 {
	n := int64(cfg().UserMaxStaleSeconds)
	if n <= 0 {
		n = 300
	}
	return n
}

// activeSnapshot 返回当前生效的快照,从未成功加载过时返回 nil。
//
// ══════════════ 降级方向:沿用最后一份成功快照 ══════════════
//
// 与 groupmatrix 同向、与 grouppricing 反向。判据是「丢弃之后会发生什么」:
//
//	丢弃 → 全部套餐退化为"未配置解锁 + universal" → 与上游逐位一致。
//
// 也就是说丢弃是安全的方向,但它会让**已付款用户**的解锁分组当场消失(403),
// 而陈旧期内他仍按该模型分组的正确倍率付费、平台不损失一分钱。
// 因此:能服务的旧数据要服务,陈旧只告警。
func activeSnapshot() *Snapshot {
	s := current.Load()
	if s == nil {
		return nil
	}
	if age := common.GetTimestamp() - loadedAt.Load(); age > maxStaleSeconds() {
		if n := staleWarns.Add(1); n == 1 || n%1000 == 0 {
			common.SysError(fmt.Sprintf(
				"qianye/planentitlement: 套餐解锁快照已陈旧 %d 秒(上限 %d),累计告警 %d 次 —— "+
					"快照仍在生效(刻意的:丢弃会让已付款用户的解锁分组当场消失),"+
					"但最近一次套餐绑定改动可能还没在本节点生效,请检查扩展库健康状态",
				age, maxStaleSeconds(), n))
		}
	}
	return s
}

// Current 是给同进程其它模块(订阅侧的余额范围过滤、对账、健康面板)的只读入口。
//
// **一次请求只 Load 一次、然后把这个指针传下去**:事务内的候选过滤与事务外的
// 钱包回退判定必须读同一份快照,否则它们可能跨了一次刷新,给出互相矛盾的结论。
// 从未成功加载过时返回 nil,调用方按各自的 fail 方向退化。
func Current() *Snapshot { return activeSnapshot() }

// BalanceScope 返回一个套餐的余额使用范围。缺行 / 快照未加载一律 universal。
func (s *Snapshot) BalanceScope(planId int) string {
	if s == nil {
		return ScopeUniversal
	}
	if v, ok := s.Scope[planId]; ok && v == ScopeRestricted {
		return ScopeRestricted
	}
	return ScopeUniversal
}

// BoundGroups 返回一个套餐解锁的模型分组,按名字排序的**副本**。
// 给管理端与用户端展示用;热路径不要调它(它每次都分配)。
func (s *Snapshot) BoundGroups(planId int) []string {
	if s == nil {
		return make([]string, 0)
	}
	set := s.Bind[planId]
	out := make([]string, 0, len(set))
	for g := range set {
		out = append(out, g)
	}
	sort.Strings(out)
	return out
}

// Binds 判定某个套餐是否解锁了某个模型分组。
func (s *Snapshot) Binds(planId int, modelGroup string) bool {
	if s == nil || modelGroup == "" {
		return false
	}
	_, ok := s.Bind[planId][modelGroup]
	return ok
}

// AnyBindings 表示全站是否有**任何**套餐配过解锁绑定。
//
// 这是「上线当天零行为变化、零新增 I/O」的结构性判据:它为 false 时,
// 权限解析在触碰 per-user 缓存之前就返回上游指针,一次 I/O 都不会发生。
func (s *Snapshot) AnyBindings() bool { return s != nil && len(s.Bind) > 0 }

// CandidateUsable 判定「这条订阅在本次请求的模型分组上能不能出资」。
//
// ═══════════ 调用环境:主库事务内 + 候选行已持 FOR UPDATE 锁 ═══════════
//
// 订阅侧会把它接到 model.QySubscriptionCandidateUsable 上,那个 hook 跑在
// PreConsumeUserSubscription 的候选循环里 —— 事务已开、行锁已加。因此这里
// **只允许 atomic.Pointer.Load + map 查找**:禁止任何 I/O、任何锁、任何 context
// 等待。在持锁状态下查另一个数据库,等于把扩展库的延迟直接叠加到主库锁持有
// 时间上,扩展库抖一下就能把整站计费拖排队。
//
// fail 方向是 **return true(不跳过)**:快照没加载、套餐没配过、usingGroup 为空
// 一律逐位退化为上游行为。最坏结果是仅限套餐被当成通用花掉 —— 钱还在用户自己的
// 池子里,可事后对账;而 fail-closed 会卡住一个已上锁的扣款事务。
func CandidateUsable(planId int, usingGroup string) bool {
	return Current().CandidateUsable(planId, usingGroup)
}

// CandidateUsable 是上面那个包级函数的快照方法形态。
// 一次请求内先 Current() 一次、再对每个候选调它,保证同一批候选读的是同一份快照。
func (s *Snapshot) CandidateUsable(planId int, usingGroup string) bool {
	if s == nil || usingGroup == "" {
		return true
	}
	if !enabled() {
		// kill switch:拉下 plan_entitlement.enabled 之后,余额过滤必须立刻停止
		// 生效,而不是等快照自然过期。一个要等缓存过期才生效的开关,在出事的那
		// 一刻正好来不及。
		return true
	}
	if !balanceScopeEnforced.Load() {
		// 订阅侧还没把这个判定接到扣费路径上。此时必须恒为"可用" ——
		// 展示侧读的是同一个函数,不这样做的话界面会声称"这笔余额在这里用不了",
		// 而扣费照样从这张套餐里扣。见 seams.go 的 balanceScopeEnforced。
		return true
	}
	if s.BalanceScope(planId) != ScopeRestricted {
		return true
	}
	return s.Binds(planId, usingGroup)
}

// SnapshotView 是给管理端与健康面板的只读视图。
// 第二个返回值为 false 表示**从未**成功加载过。
func SnapshotView() (*Snapshot, bool) {
	s := current.Load()
	return s, s != nil
}

// Health 是 /admin/health 里本模块那一段。
func Health() map[string]any {
	s := current.Load()
	out := map[string]any{
		"enabled":          enabled(),
		"snapshot_loaded":  s != nil,
		"refresh_fails":    refreshFails.Load(),
		"stale_warns":      staleWarns.Load(),
		"bound_plans":      0,
		"restricted_plans": 0,
		"dropped_bindings": []string{},
		"snapshot_version": int64(0),
		// fail-closed 计数:第二层缓存冷未命中且加载失败时,我们把用户**当成
		// 没有解锁**处理。它的表现是"少数付费用户偶发 403",与"分组真的没配"
		// 长得一模一样 —— 不出这个计数器就永远查不出来。非 0 即需要关注。
		"entitlement_fail_closed": failClosed.Load(),
		// 余额使用范围此刻算不算数。false 时「仅限」只是一份配置,扣费路径没有
		// 过滤 —— 有仅限套餐却是 false,说明订阅侧的接线没做完(见 seams.go)。
		"balance_scope_enforced": balanceScopeEnforced.Load(),
		"user_cache":             userCacheStats(),
		"needs_attention":        false,
		"unloaded_behaviour":     "套餐解锁快照未加载,当前所有套餐按「无解锁 + 余额通用」处理(与上游逐位一致)",
	}
	if s == nil {
		out["needs_attention"] = enabled() && failClosed.Load() > 0
		return out
	}
	restricted := 0
	for _, v := range s.Scope {
		if v == ScopeRestricted {
			restricted++
		}
	}
	dropped := make([]string, 0, len(s.Dropped))
	dropped = append(dropped, s.Dropped...)
	out["bound_plans"] = len(s.Bind)
	out["restricted_plans"] = restricted
	out["dropped_bindings"] = dropped
	out["snapshot_version"] = s.Version
	out["snapshot_age_secs"] = common.GetTimestamp() - loadedAt.Load()
	// 三种需要人来看一眼的状态:
	//   剔除过绑定      运营删掉了一个仍被套餐引用的模型分组(restricted 时那笔
	//                   余额会变成花不掉的死钱,直到到期作废)
	//   fail-closed     有付费用户因为读不到套餐权限而被当成无解锁处理
	//   配了仅限却没生效 管理端与用户端都显示着「仅限」,而扣费路径没有过滤
	out["needs_attention"] = len(dropped) > 0 || failClosed.Load() > 0 ||
		(restricted > 0 && !balanceScopeEnforced.Load())
	return out
}

// maybeRefresh 是快照的自维护入口:读路径上只做一次 atomic 比较,
// 到期才通过 HotAsync 触发一次异步重载 —— 绝不在请求线程里回源。
//
// 为什么不用后台协程:快照是"每个节点各自持有"的进程内缓存,lease.Run 只会让
// 一个节点刷新,其余节点的绑定永远陈旧。
func maybeRefresh() {
	now := common.GetTimestamp()
	next := nextRefreshAt.Load()
	if now < next {
		return
	}
	if !nextRefreshAt.CompareAndSwap(next, now+cacheSeconds()) {
		return // 别的请求已经抢到刷新权
	}
	hotAsync("planentitlement.snapshot_refresh", func(ctx context.Context) error {
		return reloadCtx(ctx)
	})
}

// reload 用冷路径预算重载快照,供管理端写入后刷新与启动预热使用。
func reload() error {
	ctx, cancel := guard.ColdContext(context.Background())
	defer cancel()
	return reloadCtx(ctx)
}

// InvalidateAndReload 让本节点立刻用上新绑定,不等下一个刷新周期。
// 其它节点靠周期刷新感知(最多晚 cache_seconds)。
func InvalidateAndReload() error {
	nextRefreshAt.Store(0)
	return reload()
}

// reloadCtx 重新构建快照。失败时**不动 current**(保留上一次成功的快照)。
//
// ctx 必须一路挂到 GORM 语句上:guard 承诺的超时只对 WithContext(ctx) 的语句生效。
//
// 整表读而不是增量:这两张表的行数是「配过解锁的套餐数 × 每个套餐的分组数」,
// 数量级是几十到几百。为它引入一张版本表与增量比对,换来的 IO 节省可以忽略,
// 而"版本号没自增所以新绑定没生效"是一类全新的、静默的失败。
func reloadCtx(ctx context.Context) error {
	gdb := db.Get()
	if gdb == nil {
		refreshFails.Add(1)
		return db.ErrNotReady
	}
	gdb = gdb.WithContext(ctx)

	grants := make([]PlanGrant, 0, 64)
	if err := gdb.Order("plan_id asc, sort_order asc, id asc").Find(&grants).Error; err != nil {
		refreshFails.Add(1)
		db.MarkFailure(err)
		return err
	}
	policies := make([]PlanBalancePolicy, 0, 16)
	if err := gdb.Order("plan_id asc").Find(&policies).Error; err != nil {
		refreshFails.Add(1)
		db.MarkFailure(err)
		return err
	}
	db.MarkSuccess()

	current.Store(buildSnapshot(grants, policies))
	loadedAt.Store(common.GetTimestamp())
	return nil
}

// buildSnapshot 把数据库行编译成只读视图。
//
// 编译期对每个绑定再跑一次 ratio_setting.ContainsGroupRatio:模型分组可能在保存
// 之后被从倍率表里删掉,只在保存时把关拦不住这种漂移。不存在的一律**剔除**并
// 告警 —— 留着它只会让 middleware/auth.go 在下一步用「分组 %s 已被弃用」把用户
// 挡掉,而管理端那一行看起来是通的。剔除同时保证了不变量 I4:
// 被解析出的模型分组集合恒为 options.GroupRatio 键集合的子集,于是上游
// GetGroupRatio 的 fail-open(找不到静默按 1.0 计费)在本模块控制的路径上不可达。
func buildSnapshot(grants []PlanGrant, policies []PlanBalancePolicy) *Snapshot {
	s := &Snapshot{
		Bind:    make(map[int]map[string]struct{}, len(policies)+8),
		Scope:   make(map[int]string, len(policies)),
		Dropped: make([]string, 0),
	}
	for _, g := range grants {
		if g.PlanId <= 0 || g.ModelGroup == "" || g.ModelGroup == autoGroup {
			continue
		}
		if !ratio_setting.ContainsGroupRatio(g.ModelGroup) {
			s.Dropped = append(s.Dropped, fmt.Sprintf("plan:%d/%s", g.PlanId, g.ModelGroup))
			continue
		}
		set, ok := s.Bind[g.PlanId]
		if !ok {
			set = make(map[string]struct{}, 4)
			s.Bind[g.PlanId] = set
		}
		set[g.ModelGroup] = struct{}{}
		if g.UpdatedAt > s.Version {
			s.Version = g.UpdatedAt
		}
	}
	for _, p := range policies {
		if p.PlanId <= 0 {
			continue
		}
		scope := p.BalanceScope
		if scope != ScopeUniversal && scope != ScopeRestricted {
			// 脏值倒向 universal(与上游一致),而不是倒向"余额突然用不了了"。
			common.SysError(fmt.Sprintf(
				"qianye/planentitlement: 套餐 %d 的 balance_scope=%q 非法(可选 universal|restricted),"+
					"按 universal 处理(与上游行为一致)", p.PlanId, p.BalanceScope))
			scope = ScopeUniversal
		}
		s.Scope[p.PlanId] = scope
		if p.UpdatedAt > s.Version {
			s.Version = p.UpdatedAt
		}
	}
	if len(s.Dropped) > 0 {
		sort.Strings(s.Dropped)
		// 限频:快照每 cache_seconds 重建一次,不限频会把这条刷成背景噪声,
		// 而背景噪声与告警缺席是同一种失败。
		if n := droppedWarns.Add(1); n == 1 || n%200 == 0 {
			common.SysError(fmt.Sprintf(
				"qianye/planentitlement: %d 条套餐解锁绑定指向的模型分组已不在分组倍率表里,"+
					"已从快照剔除(累计告警 %d 次):%v —— 这些套餐的用户拿不到该分组,"+
					"而仅限(restricted)套餐的余额会变成花不掉的死钱直到到期作废",
				len(s.Dropped), n, s.Dropped))
		}
	}
	return s
}
