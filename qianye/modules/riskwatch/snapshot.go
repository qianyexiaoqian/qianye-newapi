package riskwatch

import (
	"context"
	"sync/atomic"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/qianye/config"
	"github.com/QuantumNous/new-api/qianye/db"
	"github.com/QuantumNous/new-api/qianye/groupname"
	"github.com/QuantumNous/new-api/qianye/guard"
)

// snapshot.go —— 监听任务的进程内快照。
//
// 热路径上每一次请求都要回答"这一条在不在某个监听任务的作用域里",查库不可接受。
// 快照整体不可变、每次刷新整份替换,读侧是一次 atomic load,没有锁。
//
// 代价是新建 / 停止一个任务最多晚 snapshot_seconds 生效。这对取证完全可以接受,
// 而唯一一条**不能**靠快照兑现的承诺 —— "抽满 N 条自动停止" —— 也不靠它:
// 名额由落库时的原子 UPDATE 预留(见 store.persist),快照里的 captured 只用来
// 让一个明显已满的任务提前退出,少发一次异步作业。

// watchTask 是一个任务在热路径上需要的**全部**信息。
//
// 刻意不直接放 Task:那上面有 Note、Name、审计字段,它们不参与任何判定,却会
// 让每次刷新多复制一份字符串到每个节点的常驻内存里。更重要的是,压成这个结构
// 之后"热路径会读哪几格"变成一件看一眼就知道的事。
type watchTask struct {
	Id int64

	// 作用域三格。零值 = 不限。
	UserId int
	// Group 已经过 groupname.Normalize:扩展库的 varchar 列是大小写不敏感的,
	// 而 Go 的字符串比较是敏感的 —— 这个"存储不敏感、代码敏感"的中间态会让
	// 配在 VIP 上的监听任务盯不住 users.group 为 vip 的那个人。判据整包在
	// qianye/groupname,不在这里另写一套。
	Group string
	// Model 不折叠大小写:模型名的源头是用户请求体里的字符串,平台侧
	// gpt-4 与 GPT-4 是两个不同的模型名(渠道映射表也按精确匹配查),
	// 折叠它会让一个监听任务盯上一个管理员没打算盯的模型。
	Model string

	SampleBps  int
	MaxRecords int
	// Captured 是**快照时刻**的已抓条数,只用于提前退出,不是闸门(见文件头)。
	Captured int

	StartsAt int64
	EndsAt   int64

	// RetentionDays 已经解析过 nil:这里存的是最终生效的天数,0 = 永久保留。
	RetentionDays int
}

// alive 判断这个任务此刻是否还该抓。
//
// 三个条件都用本节点的时钟,而不是在 SQL 里判:快照的口径必须与热路径判定
// 的口径来自同一个时钟,否则一个任务会在"库说没到期、本节点说到期了"之间反复。
func (t *watchTask) alive(now int64) bool {
	if t.StartsAt > 0 && now < t.StartsAt {
		return false
	}
	if t.EndsAt > 0 && now >= t.EndsAt {
		return false
	}
	if t.MaxRecords > 0 && t.Captured >= t.MaxRecords {
		return false
	}
	return true
}

// matches 是作用域闸。纯内存比较:没有分配、没有加锁、没有随机数。
//
// 它必须排在抽样**之前**。反过来(先摇骰子、抽中之后再判在不在作用域内)会让
// 界面上那个"10%"变成"作用域内的 10% 乘以一个谁也说不出来的数",而记录概率是
// 这个功能唯一的成本闸门,它必须是字面意思。这条顺序与 AI 审核的作用域闸同源。
func (t *watchTask) matches(userId int, group, model string) bool {
	if t.UserId != 0 && t.UserId != userId {
		return false
	}
	if t.Group != "" && t.Group != group {
		return false
	}
	if t.Model != "" && t.Model != model {
		return false
	}
	return true
}

// snapshot 是一整份不可变的任务清单。
type snapshot struct {
	tasks []*watchTask
}

var (
	current       atomic.Pointer[snapshot]
	nextRefreshAt atomic.Int64
	// emptySnapshot 让读侧永远拿得到一个非 nil 的清单,省掉每个调用点一次判空。
	emptySnapshot = &snapshot{}
)

// Snapshot 返回当前快照,永不为 nil。
func Snapshot() *snapshot {
	if s := current.Load(); s != nil {
		return s
	}
	return emptySnapshot
}

// maybeRefresh 由热路径调用:到期才通过 HotAsync 触发一次异步重载。
//
// 为什么不用后台协程:快照是"每个节点各自持有"的进程内缓存,lease.Run 只会让
// 一个节点刷新,其余节点的任务清单永远陈旧;而裸 goroutine 违反模块约定。
// CAS 推进下次刷新时间既避免了并发重复加载,也让无流量的节点零开销。
//
// 已知耦合:guard.HotAsync 的入队闸判的是**主扩展库**是否可用。主库挂掉而
// 存储节点好好的那一刻,快照会停止刷新(已有快照继续生效)。这与 AI 审核明细
// 的落库是同一条耦合,接受它是因为另一条路要么是自建 goroutine 池(第二套
// 无界并发),要么是把 HotAsync 的闸门按 Flag 拆开 —— 而那会让每一个调用点
// 都要回答"我该用哪个闸"。
func maybeRefresh() {
	now := common.GetTimestamp()
	next := nextRefreshAt.Load()
	if now < next {
		return
	}
	every := int64(config.Get().RiskWatch.SnapshotSeconds)
	if every <= 0 {
		every = 30
	}
	if !nextRefreshAt.CompareAndSwap(next, now+every) {
		return // 别的请求已经抢到刷新权
	}
	guard.HotAsync("riskwatch.snapshot_refresh", func(ctx context.Context) error {
		return reloadCtx(ctx)
	})
}

// Reload 用冷路径预算重载快照,供管理端写入后立即生效与启动预热使用。
//
// 管理端每次写完任务都调它:靠 snapshot_seconds 自然到期的话,管理员点完
// "停止"之后最多还会再抓 30 秒 —— 而"我明明停了它还在记"是这个功能最不该有的
// 一句用户反馈。
func Reload() error {
	ctx, cancel := guard.ColdContext(context.Background())
	defer cancel()
	// 立即重载之后把下一次自然刷新也推后一个周期:否则刚重载完的快照会在
	// 下一个请求到来时又被重载一次。
	if err := reloadCtx(ctx); err != nil {
		return err
	}
	every := int64(config.Get().RiskWatch.SnapshotSeconds)
	if every <= 0 {
		every = 30
	}
	nextRefreshAt.Store(common.GetTimestamp() + every)
	return nil
}

// reloadCtx 重新构建快照。ctx 必须一路挂到 GORM 语句上 —— guard.HotAsync 承诺的
// hot_async_timeout_ms 只对 WithContext(ctx) 过的语句生效,漏接会让一条慢查询
// 一直等到驱动层 readTimeout,期间它占着仅有的几个 hot worker。
func reloadCtx(ctx context.Context) error {
	gdb := db.Watch()
	if gdb == nil {
		return db.ErrNotReady
	}
	// 上限用 max_active_tasks:它本来就是"同时能有多少个任务在跑"的闸门,
	// 再单独定义一个查询上限就是同一个数的第二份拷贝。0(不限)时给一个
	// 足够大的硬顶,免得一次配置失误把整张表拉进每个节点的内存。
	limit := config.Get().RiskWatch.MaxActiveTasks
	if limit <= 0 || limit > maxSnapshotTasks {
		limit = maxSnapshotTasks
	}
	rows, err := activeTasks(ctx, gdb, limit)
	if err != nil {
		return err
	}

	defaultRetention := config.Get().RiskWatch.RetentionDays
	tasks := make([]*watchTask, 0, len(rows))
	for i := range rows {
		r := rows[i]
		retention := defaultRetention
		if r.RetentionDays != nil {
			retention = *r.RetentionDays
		}
		tasks = append(tasks, &watchTask{
			Id:            r.Id,
			UserId:        r.TargetUserId,
			Group:         groupname.Normalize(r.TargetGroup),
			Model:         r.TargetModel,
			SampleBps:     r.SampleBps,
			MaxRecords:    r.MaxRecords,
			Captured:      r.Captured,
			StartsAt:      r.StartsAt,
			EndsAt:        r.EndsAt,
			RetentionDays: retention,
		})
	}
	current.Store(&snapshot{tasks: tasks})
	return nil
}

// maxSnapshotTasks 是快照的硬顶,与 max_active_tasks 无关 —— 后者可以配成 0
// (不限),而"不限"不该等于"把整张任务表读进每个节点的常驻内存"。
const maxSnapshotTasks = 500
