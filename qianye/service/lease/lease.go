// Package lease 提供基于扩展库的分布式租约,用于后台任务的跨节点互斥。
//
// 为什么必须有:common.IsMasterNode 只是 NODE_TYPE != "slave" 这个环境变量,
// 不是租约。多节点部署时每个节点都可能被配成 master,佣金结算、充值扫描这类任务
// 就会同时跑,造成重复返佣、重复扣费。
//
// 所有时间比较都用**库端**的当前时间,而不是 Go 端的时间 ——
// 节点之间的时钟漂移会让基于本地时间的租约失效判断出错。
// 具体表达式按方言渲染(db.NowEpochSQL),三家的取整方向已对齐到"截断到秒"。
package lease

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/qianye/config"
	"github.com/QuantumNous/new-api/qianye/db"
	qymodel "github.com/QuantumNous/new-api/qianye/model"

	"github.com/bytedance/gopkg/util/gopool"
)

// ErrLeaseLost 表示租约已被其他节点接管。持有者收到它必须立即停止工作。
var ErrLeaseLost = errors.New("qianye: 租约已丢失")

var (
	holderOnce sync.Once
	holderID   string
)

// Holder 返回本进程的租约持有者标识。
//
// 单用 NodeName 不够:同一台机器上跑多个实例会重名,导致互相误判为自己持有。
func Holder() string {
	holderOnce.Do(func() {
		node := common.NodeName
		if node == "" {
			node = "unknown"
		}
		holderID = fmt.Sprintf("%s:%s", node, common.GetUUID()[:8])
	})
	return holderID
}

// Acquire 尝试获取租约。返回 (是否获得, fence, error)。
//
// fence 是单调递增的任期号:老持有者若卡在 GC 或网络分区中,恢复后它手里的 fence
// 已经过期,续租和写入都会失败,因此不会双跑写脏数据。
func Acquire(name string, ttlSeconds int) (bool, int64, error) {
	gdb := db.Get()
	if gdb == nil {
		return false, 0, db.ErrNotReady
	}
	holder := Holder()

	// 先 UPDATE 抢占过期租约,撞不到行才 INSERT。
	//
	// 顺序很要紧。原先是"先 INSERT、撞唯一键再 UPDATE",而表里那一行在首次运行
	// 之后就一直存在 —— 于是**每个任务每个周期都必然制造一次唯一键冲突**。
	// 在 PrepareStmt 开启的连接上(qianye/db 默认开),一条预编译语句执行失败会
	// 让 GORM 把它从缓存里作废,同一条连接上紧随其后的语句可能拿到
	// "statement is closed",整轮任务被打掉。多个任务在同一个周期对齐点一起跑时
	// 尤其明显 —— 被打掉的包括 twophase.compensate 与 withdraw.reconcile,
	// 那是资金中间态的收尾。
	//
	// 换成"先 UPDATE"之后,稳态路径一条 UPDATE 就结束,零冲突;
	// INSERT 只在表里确实没有这一行时执行一次,那是真正的首次运行。
	now := db.NowEpochSQL(gdb)
	res := gdb.Exec(`UPDATE qy_task_leases
		SET holder = ?, fence = fence + 1,
		    lease_until = `+now+`+?, acquired_at = `+now+`, updated_at = `+now+`
		WHERE name = ? AND lease_until < `+now,
		holder, ttlSeconds, name)
	if res.Error != nil {
		db.MarkFailure(res.Error)
		return false, 0, res.Error
	}
	if res.RowsAffected == 0 {
		// 两种可能:别人正持有(常态),或这一行还不存在(首次运行)。
		// 用条件插入区分:撞键说明是前者,不算错误。
		err := gdb.Exec(`INSERT INTO qy_task_leases
			(name, holder, fence, lease_until, acquired_at, updated_at)
			VALUES (?, ?, 1, `+now+`+?, `+now+`, `+now+`)`,
			name, holder, ttlSeconds).Error
		if err == nil {
			return true, 1, nil
		}
		if isDuplicateKey(err) {
			return false, 0, nil // 别的节点正持有
		}
		db.MarkFailure(err)
		return false, 0, err
	}

	var fence int64
	if err := gdb.Raw(`SELECT fence FROM qy_task_leases WHERE name = ?`, name).Scan(&fence).Error; err != nil {
		db.MarkFailure(err)
		return false, 0, err
	}
	return true, fence, nil
}

// Renew 续租。必须同时匹配 holder 与 fence 且租约未过期,否则返回 ErrLeaseLost。
func Renew(name string, fence int64, ttlSeconds int) error {
	gdb := db.Get()
	if gdb == nil {
		return db.ErrNotReady
	}
	now := db.NowEpochSQL(gdb)
	res := gdb.Exec(`UPDATE qy_task_leases
		SET lease_until = `+now+`+?, updated_at = `+now+`
		WHERE name = ? AND holder = ? AND fence = ? AND lease_until >= `+now,
		ttlSeconds, name, Holder(), fence)
	if res.Error != nil {
		db.MarkFailure(res.Error)
		return res.Error
	}
	if res.RowsAffected == 0 {
		return ErrLeaseLost
	}
	return nil
}

// Release 主动释放租约,让其他节点能立即接管而不必等 TTL 到期。
func Release(name string, fence int64) error {
	gdb := db.Get()
	if gdb == nil {
		return db.ErrNotReady
	}
	return gdb.Exec(`UPDATE qy_task_leases SET lease_until = 0, updated_at = `+db.NowEpochSQL(gdb)+`
		WHERE name = ? AND holder = ? AND fence = ?`, name, Holder(), fence).Error
}

// List 返回全部租约状态,供管理端健康面板展示。
func List() ([]qymodel.TaskLease, error) {
	gdb := db.Get()
	if gdb == nil {
		return nil, db.ErrNotReady
	}
	var rows []qymodel.TaskLease
	err := gdb.Order("name asc").Find(&rows).Error
	return rows, err
}

// Run 启动一个受租约保护的周期任务。
//
// fn 收到的 ctx 会在续租失败时立刻 cancel。fn 必须在每个批次开头检查 ctx.Err(),
// 确保失去租约后不再写库 —— 否则被接管后仍在写就是双跑。
func Run(name string, interval time.Duration, fn func(ctx context.Context)) {
	gopool.Go(func() {
		ttl := config.Get().Runtime.LeaseTTLSeconds
		renewEvery := config.Get().Runtime.LeaseRenewSeconds
		if ttl <= 0 {
			ttl = 60
		}
		if renewEvery <= 0 || renewEvery*2 >= ttl {
			renewEvery = ttl / 3
		}

		ticker := time.NewTicker(interval)
		defer ticker.Stop()

		for range ticker.C {
			if !db.Available() {
				continue
			}
			ok, fence, err := Acquire(name, ttl)
			if err != nil {
				common.SysError(fmt.Sprintf("qianye: 任务 %s 获取租约失败: %v", name, err))
				continue
			}
			if !ok {
				continue // 别的节点在跑
			}
			runOnce(name, fence, ttl, renewEvery, fn)
		}
	})
}

func runOnce(name string, fence int64, ttl, renewEvery int, fn func(ctx context.Context)) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// 续租协程:一旦失去租约立刻 cancel,让 fn 尽快停手。
	done := make(chan struct{})
	gopool.Go(func() {
		t := time.NewTicker(time.Duration(renewEvery) * time.Second)
		defer t.Stop()
		for {
			select {
			case <-done:
				return
			case <-t.C:
				if err := Renew(name, fence, ttl); err != nil {
					if errors.Is(err, ErrLeaseLost) {
						common.SysError(fmt.Sprintf("qianye: 任务 %s 的租约已被接管,正在停止本节点的执行", name))
					}
					cancel()
					return
				}
			}
		}
	})

	defer func() {
		close(done)
		if r := recover(); r != nil {
			common.SysError(fmt.Sprintf("qianye: 后台任务 %s 发生 panic(已拦截): %v", name, r))
		}
		// 正常结束时主动释放,缩短其他节点的接管延迟。
		if ctx.Err() == nil {
			_ = Release(name, fence)
		}
	}()

	fn(ctx)
}

// isDuplicateKey 判断错误是否为唯一索引冲突。
//
// 三家方言的报错文本各不相同,判据统一收在 db.IsDuplicateKey ——
// 本地各抄一份的后果不是报错而是静默改变控制流:这里把"撞键"当作
// "别的节点正持有租约"(返回 false, nil),漏判会变成把它当真错误往上抛。
func isDuplicateKey(err error) bool { return db.IsDuplicateKey(err) }
