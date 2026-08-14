package planentitlement

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"sync"
	"sync/atomic"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/model"

	"github.com/samber/hot"
	"golang.org/x/sync/singleflight"
)

// ══════════════════════════ 第二层:userId → 活跃套餐 ══════════════════════════
//
// 第一层(snapshot.go)是全站配置,可以整份塞进内存。第二层不行:它是
// 「这个人此刻持有哪些套餐」,随用户数增长且需要 per-user 新鲜度,周期性全表扫
// 是错的形状。所以它是一份带 TTL 的 per-node 缓存 + singleflight 回源。
//
// 刻意不用 pkg/cachex.HybridCache(与 commission/inviter.go 同一个判断):
// 它在启用 Redis 时会走网络,而 Get 的 Redis 分支自带 2 秒硬编码超时 ——
// 那条路挂在 middleware/auth.go 上,Redis 抖一下就是全站 2 秒。而且本层需要
// **serve-stale**(TTL 过期后仍要能拿到上一份成功结果),那是 TTL 缓存表达不了的。
//
// 代价说清楚:失效只发生在**本节点**。用户刚买完套餐,其它节点最多滞后一个
// 负结果 TTL(userCacheSeconds/4)才认得这笔购买。负结果 TTL 更短正是为了压住
// 这个场景 —— 刚买套餐的人此前几乎必然是"零活跃订阅"那一档。

const (
	// userCacheCapacity 是 per-user 缓存的容量上限。
	// 超出后按 LRU 淘汰:淘汰只会让下一次请求重新回源一次,不会给出错误答案。
	userCacheCapacity = 200000
	// loadBudget 是**冷未命中时同步回源**的硬上界。
	//
	// 它比 runtime.hot_path_timeout_ms(默认 200ms)更紧,而且刻意不做成配置项:
	// 这条查询挂在每一个带令牌分组的 relay 请求的鉴权阶段,是全站最不能等的位置。
	// 一次带索引的 user_subscriptions 单点查询在健康的库上是亚毫秒级,50ms 已经
	// 是"主库出问题了"的量级 —— 那时正确的动作是放弃本次解锁(fail-closed),
	// 而不是把 relay 线程继续押在上面。
	loadBudget = 50 * time.Millisecond
)

// userEntry 是一条 per-user 解锁快照。
//
// PlanIds 为空是**正常且必须被缓存**的值:绝大多数用户没有任何订阅,不做负缓存
// 的话每个请求都要回一次主库,等于给主库加上与 relay QPS 等量的读压力。
type userEntry struct {
	PlanIds  []int
	LoadedAt int64
	// FundedPlanIds 是 PlanIds 中**此刻仍有余额**的那些(amount_total <= 0 视为
	// 不限量)。它与 PlanIds 在**同一次**查询里取出,零额外查询、零额外往返 ——
	// 这是「套餐耗尽闸门」能挂在钱包出资决策上而不引入新 I/O 的全部原因。
	//
	// 精度说明:它与 model.PreConsumeUserSubscription 事务内的真实余额之间有一个
	// 缓存周期的滞后,与 ExpiresAt 承担的滞后同量级。因此它只用于**放行/影子**
	// 判定的宽松侧(不确定即视为有余额),绝不用来代替事务内的扣减校验。
	FundedPlanIds []int
	// OverflowPlanIds 是 PlanIds 中 allow_wallet_overflow=true 的那些。
	//
	// 它回答的是「这张套餐的额度用尽之后,运营允不允许继续用钱包余额付」,与
	// FundedPlanIds 在**同一次**查询里取出,零额外查询。
	//
	// 口径必须是**每张订阅各自**的:曾经的用户级聚合(任一活跃订阅 O=0 就封锁
	// 全部钱包出资)会让一张与本次模型分组毫无关系的套餐把钱包一起封掉。
	OverflowPlanIds []int
	// ExpiresAt 是这批订阅里**最早**的 end_time(没有订阅时为 0)。
	//
	// 缓存条目里只放 planIds 的话,一条订阅自然到期之后,本节点在整个新鲜期
	// (默认 60 秒)里仍然会把它算进解锁集合 —— 用户继续持有他已经失去的模型分组。
	// 本站有 GroupRatio=0 的免费分组,那 60 秒就是零成本调用。而这个时间戳在
	// 回源时是顺手拿到的,判定只是一次整数比较,所以没有理由不带上它。
	//
	// 它只覆盖**自然到期**。管理员作废单条订阅(status→cancelled)不改 end_time,
	// 那一档由 InvalidateUser 覆盖(见 modules/subscription 的删除与名额路径)。
	ExpiresAt int64
}

var (
	userCacheOnce sync.Once
	userCache     *hot.HotCache[int, userEntry]
	userSF        singleflight.Group
	// userRefreshInFlight 是 serve-stale 的在途去重标记(见 scheduleUserRefresh)。
	userRefreshInFlight sync.Map

	userCacheHit  atomic.Int64
	userCacheMiss atomic.Int64
	// failClosed 统计「冷未命中 + 回源失败 → 按无解锁处理」的次数。
	// 它的表现是付费用户偶发 403,与"分组真的没配"长得一模一样;
	// 不出这个计数器就永远查不出来。
	failClosed atomic.Int64
	// failClosedWarns 是上面那条的限频日志计数。
	failClosedWarns atomic.Int64
)

func userCacheSeconds() int64 {
	n := int64(cfg().UserCacheSeconds)
	if n <= 0 {
		n = 60
	}
	return n
}

// negativeCacheSeconds 是「这个人没有任何活跃套餐」这一档的缓存时长。
//
// 取正结果的 1/4:刚买完套餐的用户此前几乎必然处于这一档,而"买完之后多久能用"
// 是最容易变成工单的一个数字。正结果不需要这么急 —— 它已经能用了。
func negativeCacheSeconds() int64 {
	n := userCacheSeconds() / 4
	if n < 1 {
		n = 1
	}
	return n
}

func getUserCache() *hot.HotCache[int, userEntry] {
	userCacheOnce.Do(func() {
		// hot 的 TTL 取 max_stale:在这段时间内条目仍然留在缓存里,由 LoadedAt
		// 自己判定"新鲜"还是"陈旧"。把 hot 的 TTL 设成新鲜期就没法 serve-stale 了 ——
		// 条目一过期就消失,刷新失败时只能退化成"无解锁",而那正是要避免的方向。
		userCache = hot.NewHotCache[int, userEntry](hot.LRU, userCacheCapacity).
			WithTTL(time.Duration(maxStaleSeconds()) * time.Second).
			WithJanitor().
			Build()
	})
	return userCache
}

// InvalidateUser 让某个用户的解锁快照立刻失效。userId <= 0 表示全部清空。
//
// 调用点:管理端改绑定 / 作废订阅之后,以及订阅创建事务里那个已有的名额闸门。
// **是失效不是预热**:在一个已打开的主库事务里做同步预热,会把扩展库与主库的
// 延迟一起压进那个事务。
func InvalidateUser(userId int) {
	if userId <= 0 {
		getUserCache().Purge()
		return
	}
	getUserCache().Delete(userId)
}

// activePlanIds 返回该用户当前持有的活跃套餐 id。
//
// 第二个返回值是「这个答案可信吗」:false 表示回源失败且没有可用的旧结果,
// 调用方必须按 **fail-closed** 处理(视为无解锁)。无法区分「没有套餐」与
// 「读失败」,失败时放行等于让付费解锁变成免费。
//
// 三档取值,顺序即语义:
//
//	新鲜(age < user_cache_seconds)         直接用
//	陈旧(age < user_max_stale_seconds)     直接用 + 异步刷新(serve-stale)。
//	                                        用户已付款,而且陈旧期内他仍按该模型
//	                                        分组的正确倍率付费,平台不损失一分钱。
//	缺失 / 超过 max_stale                    同步回源(singleflight + 50ms 硬超时),
//	                                        失败即 fail-closed。
func activePlanIds(userId int) ([]int, bool) {
	if userId <= 0 {
		return nil, true
	}
	now := common.GetTimestamp()
	if e, found, err := getUserCache().Get(userId); err == nil && found {
		age := now - e.LoadedAt
		fresh := userCacheSeconds()
		if len(e.PlanIds) == 0 {
			fresh = negativeCacheSeconds()
		}
		// 这批订阅里已经有一条自然到期了 —— 缓存里的结论此刻就是错的。
		// 新鲜与陈旧两条分支都必须让开:serve-stale 服务的是"数据可能有点旧",
		// 而这里是"数据确定是错的",继续给出去等于让用户白拿一段已经失去的权限
		// (本站有 GroupRatio=0 的免费分组,那就是零成本调用)。
		expired := e.ExpiresAt > 0 && now >= e.ExpiresAt
		if !expired {
			if age < fresh {
				userCacheHit.Add(1)
				return e.PlanIds, true
			}
			if age < maxStaleSeconds() {
				// serve-stale:先把旧答案给出去,刷新交给队列。
				userCacheHit.Add(1)
				scheduleUserRefresh(userId)
				return e.PlanIds, true
			}
		}
	}

	userCacheMiss.Add(1)
	ctx, cancel := context.WithTimeout(context.Background(), loadBudget)
	defer cancel()
	ids, err := loadActivePlanIds(ctx, userId)
	if err != nil {
		noteFailClosed(userId, err)
		return nil, false
	}
	return ids, true
}

// scheduleUserRefresh 给某个用户排**一个**后台刷新作业。
//
// ═══════════ 为什么不能直接 hotAsync ═══════════
//
// 陈旧窗口(user_cache_seconds → user_max_stale_seconds)可以长达数分钟,而这条
// 路径挂在每一个带令牌分组的 relay 请求上。直接派发的话,窗口内该用户的**每一次**
// 请求都往 guard 的有界热队列(默认 4096)里塞一个作业:主库一变慢,单次刷新耗时
// 上升 → 堆积量正比于 QPS × 刷新耗时 → 队列填满 → guard 走 default 分支丢弃。
// 被丢的可能是 commission 事件,而丢弃是"用户该拿的钱没拿到"的唯一路径 ——
// 放大最严重的时刻恰好是数据库最不健康的时刻。
//
// singleflight 挡不住这个:它只合并**并发**的 Do,队列里串行取出的作业各打一次库,
// 而且第一个作业完成后 LoadedAt 已经刷新,后面排队的全是纯浪费。
//
// 所以用 CAS 做在途标记(与 snapshot.go / groupmatrix 的 maybeRefresh 同一个手法):
// 同一个用户在同一时刻最多有一个作业在队列里。作业跑完(成功或失败)才清标记 ——
// 失败也清,否则一次瞬时错误会让这个用户此后再也不刷新,一路陈旧到 max_stale。
func scheduleUserRefresh(userId int) {
	if _, busy := userRefreshInFlight.LoadOrStore(userId, struct{}{}); busy {
		return
	}
	hotAsync("planentitlement.user_refresh", func(ctx context.Context) error {
		defer userRefreshInFlight.Delete(userId)
		_, err := loadActivePlanIds(ctx, userId)
		return err
	})
}

// loadActivePlanIds 回主库读一次并写回缓存。
//
// 判据必须与 model.PreConsumeUserSubscription 的候选查询**逐字相同**
// (status='active' AND end_time > now):解锁看得见、扣费却取不到这条订阅,
// 或者反过来,都会变成说不清的工单。上游那条候选查询就是这两个条件。
//
// singleflight 防击穿:同一个用户的并发请求(多条流式连接是常态)只打一次主库。
func loadActivePlanIds(ctx context.Context, userId int) ([]int, error) {
	if userId <= 0 {
		return nil, nil
	}
	if model.DB == nil {
		return nil, errors.New("main database is not ready")
	}
	v, err, _ := userSF.Do(strconv.Itoa(userId), func() (any, error) {
		// 顺手把 end_time 也取出来:候选按 end_time asc 排,第一条就是最早到期的
		// 那一条,它决定这份结论什么时候必然失效(见 userEntry.ExpiresAt)。
		rows := make([]struct {
			PlanId              int   `gorm:"column:plan_id"`
			EndTime             int64 `gorm:"column:end_time"`
			AmountTotal         int64 `gorm:"column:amount_total"`
			AmountUsed          int64 `gorm:"column:amount_used"`
			NextResetTime       int64 `gorm:"column:next_reset_time"`
			AllowWalletOverflow bool  `gorm:"column:allow_wallet_overflow"`
		}, 0, 4)
		err := model.DB.WithContext(ctx).Model(&model.UserSubscription{}).
			Select("plan_id", "end_time", "amount_total", "amount_used", "next_reset_time", "allow_wallet_overflow").
			Where("user_id = ? AND status = ? AND "+model.SubscriptionActiveEndTimeSQL, userId, statusActive, common.GetTimestamp()).
			Order("end_time asc, id asc").
			Find(&rows).Error
		if err != nil {
			return nil, err
		}
		ids := make([]int, 0, len(rows))
		funded := make([]int, 0, len(rows))
		overflow := make([]int, 0, len(rows))
		expiresAt := int64(0)
		nowTs := common.GetTimestamp()
		for _, row := range rows {
			ids = append(ids, row.PlanId)
			if row.AllowWalletOverflow {
				overflow = append(overflow, row.PlanId)
			}
			// amount_total <= 0 是上游的"不限量"口径(见 model.PreConsumeUserSubscription
			// 的 `if sub.AmountTotal > 0` 分支),不是"额度为零"。
			//
			// next_reset_time 已过 ⇒ **视为有余额**。上游的周期重置是懒执行的:
			// 只在 PreConsumeUserSubscription 的事务内(maybeResetUserSubscriptionWithPlanTx)
			// 或 1 分钟一跳的 master-node 后台任务里才把 amount_used 归零。因此在重置
			// 到期点之后、重置真正落库之前,一张刚续期的套餐会被这里判成"已耗尽" ——
			// 而 funded=false 是钱包出资闸门唯一会拒绝的那一档的必要条件(还要叠上
			// 「用户分组不含它」与「allow_wallet_overflow=0」),判错就是一次误拒。
			// 窗口 = 后台任务的 ≤1min 抖动 + per-user 缓存;IsMasterNode 误配时窗口无上界。
			resetDue := row.NextResetTime > 0 && row.NextResetTime <= nowTs
			if row.AmountTotal <= 0 || row.AmountUsed < row.AmountTotal || resetDue {
				funded = append(funded, row.PlanId)
			}
			if expiresAt == 0 || row.EndTime < expiresAt {
				expiresAt = row.EndTime
			}
		}
		getUserCache().Set(userId, userEntry{
			PlanIds: ids, FundedPlanIds: funded, OverflowPlanIds: overflow,
			LoadedAt: common.GetTimestamp(), ExpiresAt: expiresAt,
		})
		return ids, nil
	})
	if err != nil {
		return nil, err
	}
	ids, _ := v.([]int)
	return ids, nil
}

// noteFailClosed 记一次 fail-closed 并限频告警。
//
// 文案必须说清是**读取失败**而不是"无权访问":这两句话指向完全不同的排查方向,
// 而这条路径的表现就是"少数用户偶发 403"。上游 middleware/auth.go 的用户可见
// 文案我们不改(那是上游行,而且改它买不到更多信息),可归因由这条日志与
// /admin/health 的 entitlement_fail_closed 计数器承担。
func noteFailClosed(userId int, err error) {
	failClosed.Add(1)
	if n := failClosedWarns.Add(1); n == 1 || n%100 == 0 {
		common.SysError(fmt.Sprintf(
			"qianye/planentitlement: 用户 %d 的套餐解锁读取失败,本次按「无解锁」处理"+
				"(该用户可能收到「无权访问 X 分组」,但真正的原因是套餐权限暂时读取失败),"+
				"累计 %d 次: %v", userId, n, err))
	}
}

// resetCaches 清空两层缓存并让下一次读取重新回源。仅测试使用。
//
// 必须连 sync.Once 一起重置:per-user 缓存的 TTL 是建 hot 实例时从配置读的,
// 不重置 Once 的话,后面的用例改了 user_max_stale_seconds 也拿不到新的 TTL,
// 于是"陈旧降级"那条分支永远测不到 —— 全绿,而实现体可以整段写反。
func resetCaches() {
	current.Store(nil)
	loadedAt.Store(0)
	nextRefreshAt.Store(0)
	refreshFails.Store(0)
	staleWarns.Store(0)
	droppedWarns.Store(0)
	failClosed.Store(0)
	failClosedWarns.Store(0)
	userCacheHit.Store(0)
	userCacheMiss.Store(0)
	userCacheOnce = sync.Once{}
	userCache = nil
	userRefreshInFlight = sync.Map{}
}

// userCacheStats 供健康面板判断这层缓存是不是真的在起作用。
func userCacheStats() map[string]any {
	hits, misses := userCacheHit.Load(), userCacheMiss.Load()
	total := hits + misses
	rate := 0.0
	if total > 0 {
		rate = float64(hits) / float64(total)
	}
	return map[string]any{
		"size":            getUserCache().Len(),
		"capacity":        userCacheCapacity,
		"hits":            hits,
		"misses":          misses,
		"hit_rate":        rate,
		"fresh_seconds":   userCacheSeconds(),
		"stale_seconds":   maxStaleSeconds(),
		"negative_second": negativeCacheSeconds(),
	}
}

// activeFundedPlanIds 返回该用户**此刻仍有余额**的活跃套餐 id。
//
// 它不发任何新查询:余额与 planIds 在同一次回源里取出(见 userEntry.FundedPlanIds),
// 这里只是把已经在缓存里的那一半读出来。先调 activePlanIds 是为了复用它那三档
// 新鲜度判定(新鲜 / serve-stale / 冷回源),不重写第二份。
//
// 第二个返回值为 false 表示"这个答案不可信"。调用方(套餐耗尽闸门)的 fail 方向
// 与本文件其余部分**相反**:那里不确定即视为**有余额**(放行)。理由是那条路径
// 拒绝的是已付款用户的一次请求,而 fail-closed 会让他在缓存抖动时突然被挡;
// 而本文件其余部分的 fail-closed 保护的是"读失败不要变成免费解锁"。
func activeFundedPlanIds(userId int) ([]int, bool) {
	if _, ok := activePlanIds(userId); !ok {
		return nil, false
	}
	e, found, err := getUserCache().Get(userId)
	if err != nil || !found {
		return nil, false
	}
	return e.FundedPlanIds, true
}

// activeOverflowPlanIds 返回该用户活跃订阅中 allow_wallet_overflow=true 的套餐 id。
//
// 与 activeFundedPlanIds 同源、同一次回源、零额外查询。第二个返回值为 false 表示
// 「这个答案不可信」;调用方(钱包出资闸门)在那一档必须按**允许回退**处理 ——
// 读不到运营开关就把用户的钱包挡死,是一次缓存抖动变成一次付费用户 403。
func activeOverflowPlanIds(userId int) ([]int, bool) {
	if _, ok := activePlanIds(userId); !ok {
		return nil, false
	}
	e, found, err := getUserCache().Get(userId)
	if err != nil || !found {
		return nil, false
	}
	return e.OverflowPlanIds, true
}
