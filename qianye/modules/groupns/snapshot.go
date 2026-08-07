package groupns

// snapshot.go —— 两张登记表的只读内存视图。
//
// 形状与 qianye/modules/groupmatrix/snapshot.go 完全一致(atomic.Pointer 整体替换、
// 读路径只做一次比较、到期由 HotAsync 在别的 goroutine 上回源)。刻意复用同一个
// 手法而不是发明第二种:默认模型分组的解析挂在 middleware/auth.go 上,
// 与那份清单在同一条热路径、同一个失败模型下,两套生命周期只会多一份要维护的推理。
//
// ══════════════════ 降级方向:永久沿用最后一份成功快照 ══════════════════
//
// 与 groupmatrix 同向。丢弃这份快照的唯一去处是"全部用户分组退回 inherit",
// 而 inherit 恰好等于上游行为 —— 听起来安全,但它会让**已经配好 pin 的用户分组**
// 的空分组令牌在扩展库抖动的那几秒里集体回到 503(那正是 pin 要修的东西)。
// 陈旧的可见性保留更安全,陈旧只告警。

import (
	"context"
	"fmt"
	"sync/atomic"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/qianye/config"
	"github.com/QuantumNous/new-api/qianye/db"
	"github.com/QuantumNous/new-api/qianye/guard"
)

// Snapshot 是两张登记表的只读视图。**调用方禁止修改它内部的任何 map。**
type Snapshot struct {
	// Version 取两张表里最大的 updated_at,只用于展示与排障。
	Version int64
	// UserGroups 是 name → 登记行。缺键 = 未登记(fail-open,不遮断任何东西)。
	UserGroups map[string]UserGroup
	// ModelGroups 同上。
	ModelGroups map[string]ModelGroup
	// PinnedGroups 是 default_mode == pin 的用户分组数,只用于健康面板。
	PinnedGroups int
	// DenyGroups 是 default_mode == deny 的用户分组数。
	DenyGroups int
}

var (
	current       atomic.Pointer[Snapshot]
	loadedAt      atomic.Int64
	nextRefreshAt atomic.Int64
	refreshFails  atomic.Int64
	staleWarns    atomic.Int64
	// fallbackRequests 统计「因为快照从未加载成功而按 inherit 放行」的次数。
	// 回落可以接受,回落无声不行。
	fallbackRequests atomic.Int64
	// pinRejects 统计 pin 校验失败(配置自相矛盾)导致 500 的次数。
	pinRejects atomic.Int64
)

// hotAsync 抽成变量只为让测试能同步执行它。生产默认必须是 guard.HotAsync。
var hotAsync = guard.HotAsync

func cfg() config.GroupNamespace { return config.Get().GroupNamespace }

// enabled 是 L1 kill switch,**刻意不看扩展库是否可用**:
// 一个需要数据库活着才能关掉的开关,在数据库出事时正好关不掉。
func enabled() bool { return config.Enabled() && cfg().Enabled }

// defaultResolveOn 是「默认模型分组解析」的独立子开关。
//
// 它与 group_matrix.enabled **刻意不共用**:那个开关拉下是为了让*收紧*失效,
// 而这个开关拉下的方向相反 —— 已经配了 pin 的用户分组的空分组令牌会当场回到 503。
// 让一个"放松"和一个"收紧"共用一个闸,拉闸的人一定会误伤其中一侧。
func defaultResolveOn() bool { return enabled() && cfg().DefaultModelGroupOn() }

func cacheSeconds() int64 {
	if n := int64(cfg().CacheSeconds); n > 0 {
		return n
	}
	return 30
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

func activeSnapshot() *Snapshot {
	s := current.Load()
	if s == nil {
		return nil
	}
	if age := common.GetTimestamp() - loadedAt.Load(); age > maxStaleSeconds() {
		if n := staleWarns.Add(1); n == 1 || n%1000 == 0 {
			common.SysError(fmt.Sprintf(
				"qianye/groupns: 分组登记快照已陈旧 %d 秒(上限 %d),累计告警 %d 次 —— "+
					"快照仍在生效(丢弃会让已配 pin 的用户分组的空分组令牌当场回到 503),"+
					"但最近一次登记改动可能还没在本节点生效,请检查扩展库健康状态",
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

// maybeRefresh 是快照的自维护入口:读路径上只做一次 atomic 比较,
// 到期才通过 HotAsync 触发一次异步重载 —— 绝不在请求线程里回源。
func maybeRefresh() {
	nowTs := common.GetTimestamp()
	next := nextRefreshAt.Load()
	if nowTs < next {
		return
	}
	if !nextRefreshAt.CompareAndSwap(next, nowTs+cacheSeconds()) {
		return
	}
	hotAsync("groupns.snapshot_refresh", reloadCtx)
}

func reload() error {
	ctx, cancel := guard.ColdContext(context.Background())
	defer cancel()
	return reloadCtx(ctx)
}

// InvalidateAndReload 让本节点立刻用上新登记,不等下一个刷新周期。
// 其它节点靠周期刷新感知(最多晚 cache_seconds)。
func InvalidateAndReload() error {
	nextRefreshAt.Store(0)
	return reload()
}

// reloadCtx 重建快照。失败时**不动 current**,保留上一次成功的快照。
//
// ctx 必须一路挂到 GORM 语句上:guard 承诺的超时只对 WithContext(ctx) 的语句生效。
func reloadCtx(ctx context.Context) error {
	gdb := db.Get()
	if gdb == nil {
		refreshFails.Add(1)
		return db.ErrNotReady
	}
	gdb = gdb.WithContext(ctx)

	var userGroups []UserGroup
	if err := gdb.Order("sort_order asc, name asc").Find(&userGroups).Error; err != nil {
		refreshFails.Add(1)
		return err
	}
	var modelGroups []ModelGroup
	if err := gdb.Order("sort_order asc, name asc").Find(&modelGroups).Error; err != nil {
		refreshFails.Add(1)
		return err
	}

	current.Store(buildSnapshot(userGroups, modelGroups))
	loadedAt.Store(common.GetTimestamp())
	return nil
}

// buildSnapshot 把数据库行编译成只读视图。
//
// 非法 default_mode 一律按 inherit 处理并告警:未知配置必须退化成"今天的行为",
// 而不是让一个手改数据库的错字把整组用户挡在门外(deny)或指到一个不存在的池子(pin)。
func buildSnapshot(userGroups []UserGroup, modelGroups []ModelGroup) *Snapshot {
	s := &Snapshot{
		UserGroups:  make(map[string]UserGroup, len(userGroups)),
		ModelGroups: make(map[string]ModelGroup, len(modelGroups)),
	}
	for _, ug := range userGroups {
		if ug.Name == "" {
			// 空用户分组是匿名口径,永远走上游。让它出现在表里只会制造一个
			// 永远不会被读到的配置项。
			continue
		}
		if !ValidDefaultMode(ug.DefaultMode) {
			common.SysError(fmt.Sprintf(
				"qianye/groupns: 用户分组 %q 的 default_mode=%q 非法(可选 inherit|pin|deny),"+
					"按 inherit 处理(= 上游原行为)", ug.Name, ug.DefaultMode))
			ug.DefaultMode = DefaultModeInherit
		}
		if ug.DefaultMode == DefaultModePin && ug.DefaultModelGroup == "" {
			common.SysError(fmt.Sprintf(
				"qianye/groupns: 用户分组 %q 是 pin 但没有默认模型分组,按 inherit 处理", ug.Name))
			ug.DefaultMode = DefaultModeInherit
		}
		switch ug.DefaultMode {
		case DefaultModePin:
			s.PinnedGroups++
		case DefaultModeDeny:
			s.DenyGroups++
		}
		s.UserGroups[ug.Name] = ug
		if ug.UpdatedAt > s.Version {
			s.Version = ug.UpdatedAt
		}
	}
	for _, mg := range modelGroups {
		if mg.Name == "" {
			continue
		}
		s.ModelGroups[mg.Name] = mg
		if mg.UpdatedAt > s.Version {
			s.Version = mg.UpdatedAt
		}
	}
	return s
}

// Health 是 /admin/health 里本模块那一段。
func Health() map[string]any {
	out := map[string]any{
		"enabled":               enabled(),
		"default_resolve_on":    defaultResolveOn(),
		"snapshot_loaded":       false,
		"refresh_fails":         refreshFails.Load(),
		"fallback_requests":     fallbackRequests.Load(),
		"stale_warns":           staleWarns.Load(),
		"pin_rejects":           pinRejects.Load(),
		"user_groups":           0,
		"model_groups":          0,
		"pinned_groups":         0,
		"deny_groups":           0,
		"snapshot_age_secs":     int64(0),
		"snapshot_version":      int64(0),
		"needs_attention":       false,
		"unloaded_behaviour":    "登记快照未加载,全部用户分组按 inherit 解析(= 上游原行为:UsingGroup = users.group)",
		"registry_is_fail_open": true,
	}
	s := current.Load()
	if s == nil {
		out["needs_attention"] = enabled()
		return out
	}
	age := common.GetTimestamp() - loadedAt.Load()
	stale := age > maxStaleSeconds()
	out["snapshot_loaded"] = true
	out["user_groups"] = len(s.UserGroups)
	out["model_groups"] = len(s.ModelGroups)
	out["pinned_groups"] = s.PinnedGroups
	out["deny_groups"] = s.DenyGroups
	out["snapshot_age_secs"] = age
	out["snapshot_version"] = s.Version
	out["snapshot_stale"] = stale
	// pin_rejects > 0 是**配置自相矛盾**:保存时已经硬拦过 has_route 与可选清单,
	// 运行时还能撞到就是不该发生的状态,每一次都对应一个 500。
	out["needs_attention"] = stale || pinRejects.Load() > 0
	return out
}

// ResetForTest 清空进程内状态。仅供测试,避免用例之间互相污染。
func ResetForTest() {
	current.Store(nil)
	loadedAt.Store(0)
	nextRefreshAt.Store(0)
	refreshFails.Store(0)
	staleWarns.Store(0)
	fallbackRequests.Store(0)
	pinRejects.Store(0)
}

// SetSnapshotForTest 直接发布一份快照,跳过数据库。
// 默认模型分组的解析是纯内存判定,单测不该为了验证它去装一个扩展库。
func SetSnapshotForTest(userGroups []UserGroup, modelGroups []ModelGroup) {
	current.Store(buildSnapshot(userGroups, modelGroups))
	loadedAt.Store(common.GetTimestamp())
	nextRefreshAt.Store(common.GetTimestamp() + cacheSeconds())
}
