package commission

// cachesync.go —— 五把进程内缓存的**跨节点**失效通道。
//
// # 为什么必须有这个文件
//
// 本模块有五把只在本进程里失效的缓存:
//
//	inviterCache    邀请关系 + 上线分组   TTL InviterCacheSecs(默认 300s)
//	settingsCache   运营参数              TTL 60s
//	blockedSet      拉黑名单              TTL 60s
//	groupRateCache  分组费率表            TTL 60s
//	fiatRateCache   分组法币折算比例表    TTL 60s
//
// 它们各自都有一个立即失效的函数(invalidateXxx),每一条写路径也都老老实实
// 调了 —— 但那个函数是 Go 函数调用,**只清收到这次写操作的那一个进程**。
// 多节点是本仓的一等公民(lease.Run、qy_fund_orders.node_name、
// api_admin.go 的「多节点部署里在另一个节点上改的」都写着这件事),于是:
//
//   - 管理员在 node A 把某人从 1% 档改到 12% 档,node B 在最长 300 秒里
//     继续按 1% 计佣,而费率是**逐笔冻结**进 qy_commission_accrual 的,
//     本模块语义明确「改比例不追溯」—— 那 300 秒里发错的每一分钱都追不回来。
//     反方向(降档)则是平台按高档多付。
//   - 风控在 node A 按下拉黑,node B 在最长 60 秒里继续给这条关系计佣落账,
//     而拉黑发生的那一刻正是最不能有宽限期的时刻。
//   - 官方补救手段 POST /commission/cache/invalidate **本身也是节点本地的**,
//     在 node A 上点它救不了 node B。
//
// # 为什么是「扩展库里的一张失效流水 + 秒级轮询」
//
// 没有 pubsub:全仓没有任何跨节点广播通道,而 Redis 不是必配(演示站就没配),
// 把资金正确性押在一个可选组件上是错的。pkg/cachex.HybridCache 在开了 Redis 时
// 确实是跨节点一致的,但它每一次读都要走一次网络 —— inviter.go 第 60 行
// 显式拒绝了它,理由(给每次消费加一次往返)今天依然成立。
//
// 所以走「写一行失效流水,各节点秒级轮询并在本地重放」:
//
//   - 每个节点每 cacheSyncInterval 打一条 `id > 游标` 的索引查询,表里常年只有
//     几十行(超过保留期就删),代价与 60 秒 TTL 的整表回源不在一个量级。
//   - 失效窗口从 300s/60s 收敛到一个轮询周期,并且**与 TTL 无关** ——
//     TTL 仍然是兜底,不再是唯一的收敛手段。
//   - 通道只传「谁失效了」,不传值。传值就要考虑乱序、部分失败与两个节点
//     同时写的先后,而失效是幂等的:重放两次与重放一次结果相同。
//
// # 残留窗口
//
// 一个轮询周期(cacheSyncInterval)。这不是零 —— 要做到零得在每一次计佣时
// 回一次库,那正是这些缓存存在的理由。取舍点写死在这里:2 秒的错价窗口,
// 换掉 300 秒的错价窗口。
//
// # 零值口径
//
// 游标 0 = 本进程还没读过这张表。启动时把它对齐到 MAX(id) 而不是 0:
// 重放历史失效只会把刚建好的缓存全部清空一遍(正确但浪费),而对齐到当前
// 末尾之后,本节点只会看到「我起来之后别人做的改动」,那正是需要的语义。

import (
	"context"
	"sync"
	"sync/atomic"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/qianye/db"
	"github.com/QuantumNous/new-api/qianye/guard"
	"github.com/QuantumNous/new-api/qianye/service/lease"
)

// CacheInvalidation 是一条跨节点失效流水。
//
// 只追加、不更新;超过保留期由任意节点删除(DELETE 是幂等的,不必抢租约)。
type CacheInvalidation struct {
	Id   int64  `json:"id" gorm:"primaryKey;autoIncrement"`
	Kind string `json:"kind" gorm:"type:varchar(24);not null"`
	// TargetId 是这一类失效的目标。0 表示「整类全清」——
	// 对 inviter 是清空全部邀请关系缓存(分组批量迁移走这一支),
	// 对其余四类没有更细的粒度,恒为 0。
	TargetId int64 `json:"target_id" gorm:"not null;default:0"`
	// Node 是写这一行的进程标识(lease.Holder,含进程内随机后缀)。
	// 用来跳过自己写的行 —— 本地缓存在写这一行之前就已经清过了。
	Node      string `json:"node" gorm:"type:varchar(160);not null;default:''"`
	CreatedAt int64  `json:"created_at" gorm:"not null;default:0;index:idx_qy_cci_created"`
}

func (CacheInvalidation) TableName() string { return "qy_commission_cache_invalidation" }

const (
	cacheKindInviter   = "inviter"
	cacheKindSettings  = "settings"
	cacheKindBlocked   = "blocked"
	cacheKindGroupRate = "group_rate"
	cacheKindFiatRate  = "fiat_rate"
	// cacheKindAll 是「五把全清」。只在发布队列溢出时用:那时已经不知道
	// 丢掉的是哪几条,唯一安全的动作是让所有节点全部重读。
	cacheKindAll = "all"
)

const (
	// cacheSyncInterval 是各节点重放失效流水的周期,也就是**残留错价窗口**。
	//
	// 刻意做成常量而不是 YAML 旋钮:这个值决定的是资金正确性的收敛速度,
	// 不是运维口味。同一轮审计里已经出现过一个「热载改不动、运维以为改了」的
	// 缓存 TTL 旋钮,再加一个可以被调大的资金窗口是往同一个坑里走。
	cacheSyncInterval = 2 * time.Second
	// cacheSyncBatch 是单轮重放的行数上限。取不满说明追上了。
	cacheSyncBatch = 500
	// cacheSyncRetentionSec 是流水的保留期。它只需要覆盖「一个节点最长离线
	// 多久还能靠重放追上」——超过这个时长的离线节点重启时会把游标对齐到
	// 表尾,并且它的缓存本来也是空的。
	cacheSyncRetentionSec = int64(3600)
	// cacheSyncPruneEvery 是每多少轮清理一次过期流水。
	cacheSyncPruneEvery = 300
)

type cacheInvalidationMsg struct {
	Kind   string
	Target int64
}

var (
	cacheSyncStartOnce sync.Once
	// cacheSyncOn 决定 publishInvalidation 是否真的写库。
	//
	// 默认 false:单元测试、离线工具、以及扩展库还没就绪的启动早期,
	// 五个 invalidateXxx 仍然只做本地失效,一行流水都不写。
	cacheSyncOn       atomic.Bool
	cacheSyncCh       = make(chan cacheInvalidationMsg, 256)
	cacheSyncOverflow atomic.Bool
	cacheSyncCursor   atomic.Int64

	cacheSyncPublished = newCounter()
	cacheSyncApplied   = newCounter()
	// cacheSyncFailed 是「本节点的失效没能广播出去」的次数。它不为零意味着
	// 其它节点在这段时间里可能仍按旧费率计佣,是需要盯的信号。
	cacheSyncFailed = newCounter()
)

// startCacheSync 启动跨节点失效通道。由 StartTasks 调用,每个节点都要跑
// —— 它刻意**不走 lease**:租约保证的是"只有一个节点跑",而这里要的恰恰
// 是"每一个节点都在听"。
func startCacheSync() {
	cacheSyncStartOnce.Do(func() {
		cacheSyncCursor.Store(latestInvalidationId())
		go cacheSyncWriteLoop()
		go cacheSyncPollLoop()
		cacheSyncOn.Store(true)
	})
}

// publishInvalidation 把一次本地失效广播给其它节点。
//
// 绝不阻塞调用方:调用点包括 model.QyOnUserGroupChanged(它挂在主库改分组的
// 事务收尾处)与计佣热路径上的 ensureRelation。写库交给后台协程,队列满了就
// 退化成一条 all —— 宁可让所有节点多读一次库,也不能丢掉一次失效。
func publishInvalidation(kind string, target int) {
	if !cacheSyncOn.Load() {
		return
	}
	select {
	case cacheSyncCh <- cacheInvalidationMsg{Kind: kind, Target: int64(target)}:
	default:
		cacheSyncOverflow.Store(true)
	}
}

func cacheSyncWriteLoop() {
	for msg := range cacheSyncCh {
		// 溢出过就先补一条"全清":此时已经不知道丢了哪几条。
		if cacheSyncOverflow.Swap(false) {
			writeInvalidation(cacheInvalidationMsg{Kind: cacheKindAll})
		}
		writeInvalidation(msg)
	}
}

func writeInvalidation(msg cacheInvalidationMsg) {
	gdb := db.Get()
	if gdb == nil {
		cacheSyncFailed.Add(1)
		return
	}
	ctx, cancel := guard.ColdContext(context.Background())
	defer cancel()
	row := CacheInvalidation{
		Kind:      msg.Kind,
		TargetId:  msg.Target,
		Node:      lease.Holder(),
		CreatedAt: common.GetTimestamp(),
	}
	if err := gdb.WithContext(ctx).Create(&row).Error; err != nil {
		db.MarkFailure(err)
		cacheSyncFailed.Add(1)
		warnf("缓存失效广播写入失败(其它节点可能仍在按旧配置计佣) kind=%s target=%d: %v",
			msg.Kind, msg.Target, err)
		return
	}
	cacheSyncPublished.Add(1)
}

func cacheSyncPollLoop() {
	ticker := time.NewTicker(cacheSyncInterval)
	defer ticker.Stop()
	round := 0
	for range ticker.C {
		applyRemoteInvalidations()
		round++
		if round%cacheSyncPruneEvery == 0 {
			pruneInvalidations()
		}
	}
}

// latestInvalidationId 取流水表当前的末尾 id。读不到时返回 0 —— 那会让本节点
// 把已有流水重放一遍,结果是把刚建好的缓存清空,正确但多读几次库。
func latestInvalidationId() int64 {
	gdb := db.Get()
	if gdb == nil {
		return 0
	}
	ctx, cancel := guard.ColdContext(context.Background())
	defer cancel()
	var maxId *int64
	if err := gdb.WithContext(ctx).Model(&CacheInvalidation{}).
		Select("MAX(id)").Scan(&maxId).Error; err != nil {
		db.MarkFailure(err)
		return 0
	}
	if maxId == nil {
		return 0
	}
	return *maxId
}

// applyRemoteInvalidations 重放游标之后的失效流水。
//
// 游标只在成功应用之后才推进:一次读失败下一轮从同一处重来,失效可以迟到,
// 但不可以丢 —— 丢一条就是一段无人知晓的错价窗口。
func applyRemoteInvalidations() {
	gdb := db.Get()
	if gdb == nil {
		return
	}
	ctx, cancel := guard.ColdContext(context.Background())
	defer cancel()
	var rows []CacheInvalidation
	if err := gdb.WithContext(ctx).
		Where("id > ?", cacheSyncCursor.Load()).
		Order("id asc").Limit(cacheSyncBatch).Find(&rows).Error; err != nil {
		db.MarkFailure(err)
		return
	}
	self := lease.Holder()
	for _, r := range rows {
		// 自己写的行不必重放:本地缓存在写这一行之前就清过了。
		if r.Node != self {
			applyInvalidation(r)
			cacheSyncApplied.Add(1)
		}
		if r.Id > cacheSyncCursor.Load() {
			cacheSyncCursor.Store(r.Id)
		}
	}
}

// applyInvalidation 把一条流水落到本进程的缓存上。
//
// 一律调 *Local 形式:调带广播的那一个会把别人的失效再广播回去,两个节点
// 互相放大成一条永不停止的流水。
func applyInvalidation(r CacheInvalidation) {
	switch r.Kind {
	case cacheKindInviter:
		invalidateInviterLocal(int(r.TargetId))
	case cacheKindSettings:
		invalidateSettingsLocal()
	case cacheKindBlocked:
		invalidateBlockedLocal()
	case cacheKindGroupRate:
		invalidateGroupRatesLocal()
	case cacheKindFiatRate:
		invalidateFiatRatesLocal()
	case cacheKindAll:
		invalidateInviterLocal(0)
		invalidateSettingsLocal()
		invalidateBlockedLocal()
		invalidateGroupRatesLocal()
		invalidateFiatRatesLocal()
	default:
		// 未来版本新增的类别。本节点认不出来,但"有东西变了"这件事是确定的,
		// 全清是唯一安全的处置 —— 静默忽略等于在滚动升级期间留一段错价窗口。
		invalidateInviterLocal(0)
		invalidateSettingsLocal()
		invalidateBlockedLocal()
		invalidateGroupRatesLocal()
		invalidateFiatRatesLocal()
	}
}

// pruneInvalidations 删掉早于保留期的流水。
//
// 不抢租约:DELETE 是幂等的,多个节点同时删只是重复劳动。也不按 id 删 ——
// 各节点的游标不同,按时间删才能保证"还没被最慢的节点读到"的行不被删掉
// (保留期 1 小时远大于轮询周期)。
func pruneInvalidations() {
	gdb := db.Get()
	if gdb == nil {
		return
	}
	ctx, cancel := guard.ColdContext(context.Background())
	defer cancel()
	if err := gdb.WithContext(ctx).
		Where("created_at < ?", common.GetTimestamp()-cacheSyncRetentionSec).
		Delete(&CacheInvalidation{}).Error; err != nil {
		db.MarkFailure(err)
	}
}

// cacheSyncStats 供管理端健康面板判断跨节点失效通道是否真的在工作。
//
// enabled=false 意味着本进程只做本地失效 —— 多节点部署下那正是钱会发错的状态。
func cacheSyncStats() map[string]any {
	return map[string]any{
		"enabled":          cacheSyncOn.Load(),
		"interval_seconds": int64(cacheSyncInterval / time.Second),
		"cursor":           cacheSyncCursor.Load(),
		"published":        cacheSyncPublished.Load(),
		"applied":          cacheSyncApplied.Load(),
		"failed":           cacheSyncFailed.Load(),
	}
}
