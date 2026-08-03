package subscription

import (
	"context"
	"fmt"
	"sync"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/qianye/config"
	"github.com/QuantumNous/new-api/qianye/db"
	"github.com/QuantumNous/new-api/qianye/guard"

	"gorm.io/gorm/clause"
)

// cacheSeconds 是名额配置的进程内缓存周期。
//
// 刻意不起后台协程去刷:购买是低频事件,惰性刷新足够,而每个节点多一个裸
// goroutine 是纯负担。管理端保存后会直接把新值写进本节点缓存,不必等这 30 秒;
// 其他节点最多滞后 30 秒(残余风险见 gate.go)。
const cacheSeconds = 30

// failureBackoffSeconds 是一次回源失败之后的负缓存周期。
//
// 没有它,「扩展库可达但慢」这一窄带会把购买链路拖垮:一条打爆预算的 SELECT
// 让 ok=false、cachedAt 不被推进,于是**下一次购买必然再查一次**,而每一次
// 购买都攥着一个已经打开的主库事务(闸门跑在 CreateUserSubscriptionFromPlanTx
// 内部)。促销时的购买洪峰会逐个撞上同一条慢查询,把主库连接池一起吃掉。
//
// 取 5 秒而不是 cacheSeconds:失败期间要的是「别每次都回源」,而不是
// 「半分钟内都不许恢复」—— 扩展库恢复之后至多 5 秒名额闸门就重新生效。
const failureBackoffSeconds = 5

var (
	cacheMu sync.Mutex
	// cached 是 planId → capacity,只收录 capacity > 0 的行。
	// nil 表示缓存从未成功填充过(冷启动或历次读取都失败)。
	cached map[int]int
	// cachedAt 为 0 表示缓存从未成功填充过。
	cachedAt int64
	// retryAt 是负缓存到期时间戳:在此之前不再回源,直接沿用 cached。
	retryAt int64
	// cacheEpoch 是快照的代次,每次管理端写入或重置都自增。
	//
	// 查库挪出临界区之后,「SELECT 返回」与「写回缓存」之间出现了一个窗口:
	// 管理员刚好在这中间保存了新名额(storeCapacity 已经把新值写进缓存),
	// 在途的旧快照会把它静默盖掉,此后 30 秒的购买全部按旧名额放行。
	// 代次让写回方能发现「我读的那一版已经作废了」并丢弃本次结果。
	cacheEpoch uint64
)

// capacityOf 返回某个套餐的全站总名额,0 表示不限。
func capacityOf(planId int) int { return currentCapacities()[planId] }

// currentCapacities 返回名额配置的快照,必要时先回源。
//
// 返回的 map 是**只读快照**:cached 一律整体替换、绝不原地改写(见
// storeCapacity),因此调用方拿到手之后不会再被别的 goroutine 改动,
// 可以安全地在锁外遍历。
//
// 读取失败时返回上一次成功读到的快照(冷启动时即"全部不限"),绝不返回错误 ——
// 调用方在主库事务里,没有任何合理的错误处理方式,只能 fail-open。
//
// 查库这一步刻意放在 cacheMu 的临界区之外,与 usergroup.currentDefaultGroup
// 同形:持锁查库时,一条慢 SELECT 会把全进程所有购买串在这把互斥锁上,而每一个
// 排队者都攥着一个已打开的主库事务。锁只用来读/写快照。
func currentCapacities() map[int]int {
	now := common.GetTimestamp()

	cacheMu.Lock()
	if (cachedAt > 0 && now-cachedAt < cacheSeconds) || now < retryAt {
		snap := cached
		cacheMu.Unlock()
		return snap
	}
	epoch := cacheEpoch
	cacheMu.Unlock()

	fresh, ok := readAllSeats()

	cacheMu.Lock()
	defer cacheMu.Unlock()
	if !ok {
		retryAt = common.GetTimestamp() + failureBackoffSeconds
		return cached
	}
	// 本次在途期间管理端已经写入了新值,缓存里那份比手上这份新。
	if cacheEpoch != epoch {
		return cached
	}
	cached, cachedAt, retryAt = fresh, common.GetTimestamp(), 0
	return cached
}

// storeCapacity 在管理端写入成功后直接刷新本节点缓存。
//
// 比「失效缓存等下次惰性重读」更好的地方在于:重读可能因为扩展库瞬时抖动而
// 失败,那时会退回上一次快照,表现为「名额已经改小了但还在继续放人进来」。
//
// 写时复制而不是原地改:currentCapacities 会把 map 引用带出临界区,
// 原地改写就是一个货真价实的 map 并发读写(Go 运行时直接 fatal,不是 panic,
// recover 都接不住)。
func storeCapacity(planId, capacity int) {
	cacheMu.Lock()
	next := make(map[int]int, len(cached)+1)
	for k, v := range cached {
		next[k] = v
	}
	if capacity > 0 {
		next[planId] = capacity
	} else {
		delete(next, planId)
	}
	cached = next
	cacheEpoch++
	cacheMu.Unlock()
}

// resetCache 让下一次读取重新回源。仅测试使用。
func resetCache() {
	cacheMu.Lock()
	cached, cachedAt, retryAt = nil, 0, 0
	cacheEpoch++
	cacheMu.Unlock()
}

// warmCapacities 在 Init() 阶段预热一次缓存。
//
// 预热的价值在于让进程起来之后的**第一次**购买就走在正确的名额上:缓存冷的时候
// currentCapacities 会同步回源一次,那一次如果恰好赶上扩展库慢,闸门就 fail-open
// 放行了 —— 而这次放行发生在一个已打开的主库事务里,代价比启动时多一条 SELECT
// 高得多。
func warmCapacities() {
	if n := len(currentCapacities()); n > 0 {
		common.SysLog(fmt.Sprintf("qianye/subscription: 已加载 %d 个套餐的全站总名额配置", n))
	}
}

// readAllSeats 从扩展库整表回源。第二个返回值表示「本次真的读到了」——
// 一行都没有算读到了,读失败不算。
//
// 整表读而不是按 planId 单点读:这张表的行数等于"配了名额的套餐数",是个位数
// 到几十行的量级。一次读全部换来的是"每 30 秒一条 SELECT",而按需单点读会让
// 每个套餐各有一份独立的缓存周期与各自的击穿窗口 —— 复杂度高一个数量级,
// 省下的 IO 却可以忽略。
//
// # 为什么不走 guard.Hot
//
// guard.Hot 在扩展库不可用时会调 recordSkip,而那个计数器的语义是「用户该拿的
// 佣金永久丢失了」,健康面板据此告警。名额闸门回落到"放行"不属于这一类:
// 没有任何东西丢失,下一次购买会重新尝试。把它记进同一个计数器会让真正的资金
// 丢失被淹没在噪音里。这里需要的只是 guard.Hot 的另外三样能力 —— 硬超时、
// 吞 panic、熔断联动 —— 逐条自己接上(panic 在调用方 gateSeat 里接)。
func readAllSeats() (map[int]int, bool) {
	if !guard.Available() {
		return nil, false
	}
	gdb := db.Get()
	if gdb == nil {
		return nil, false
	}

	// ctx 必须一路透传给 GORM:少接这一处,扩展库卡住时这条语句会一直等到
	// MySQL 的 innodb_lock_wait_timeout(默认 50 秒),而它正压在订阅创建的
	// 主库事务里 —— 支付回调会连带卡死 50 秒。
	ctx, cancel := context.WithTimeout(context.Background(), loadBudget())
	defer cancel()

	var rows []PlanSeat
	if err := gdb.WithContext(ctx).Find(&rows).Error; err != nil {
		db.MarkFailure(err)
		return nil, false
	}
	db.MarkSuccess()

	out := make(map[int]int, len(rows))
	for _, r := range rows {
		if r.Capacity > 0 {
			out[r.PlanId] = r.Capacity
		}
	}
	return out, true
}

// loadBudget 复用 runtime.hot_path_timeout_ms:它的语义正是「一个 hook 允许
// 阻塞调用方多久」,而本 hook 阻塞的是订阅创建事务(其中一条是支付回调),
// 比 relay 更不能久等。不新增配置项 —— 多一个开关就多一个「定义了却没人读」
// 的风险面。
func loadBudget() time.Duration {
	ms := config.Get().Runtime.HotPathTimeoutMs
	if ms <= 0 {
		ms = 200
	}
	return time.Duration(ms) * time.Millisecond
}

// writeCapacity 落一条名额配置。调用方负责写审计。
func writeCapacity(ctx context.Context, planId, capacity, operatorId int) error {
	gdb := db.Get()
	if gdb == nil {
		return db.ErrNotReady
	}
	now := common.GetTimestamp()
	row := PlanSeat{
		PlanId:    planId,
		Capacity:  capacity,
		UpdatedBy: operatorId,
		CreatedAt: now,
		UpdatedAt: now,
	}
	err := gdb.WithContext(ctx).Clauses(clause.OnConflict{
		Columns:   []clause.Column{{Name: "plan_id"}},
		DoUpdates: clause.AssignmentColumns([]string{"capacity", "updated_by", "updated_at"}),
	}).Create(&row).Error
	if err != nil {
		db.MarkFailure(err)
		return err
	}
	storeCapacity(planId, capacity)
	return nil
}

// deleteCapacities 删除一批套餐的名额配置行。
//
// 同时服务两个调用点:删除套餐后的收尾清理,以及列表接口发现孤儿行时的自愈
// (见 api_admin.go)。返回删除行数供审计与日志。
func deleteCapacities(ctx context.Context, planIds []int) (int64, error) {
	if len(planIds) == 0 {
		return 0, nil
	}
	gdb := db.Get()
	if gdb == nil {
		return 0, db.ErrNotReady
	}
	res := gdb.WithContext(ctx).Where("plan_id IN ?", planIds).Delete(&PlanSeat{})
	if res.Error != nil {
		db.MarkFailure(res.Error)
		return 0, res.Error
	}
	for _, id := range planIds {
		storeCapacity(id, 0)
	}
	return res.RowsAffected, nil
}
