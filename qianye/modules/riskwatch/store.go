package riskwatch

import (
	"context"
	"errors"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/qianye/db"

	"gorm.io/gorm"
)

// store.go —— 本模块与存储节点之间**唯一**的接触面。
//
// 全模块只有 handle() 一个取句柄的地方,而且它取的是 db.Watch() 而不是 db.Get()。
// 这条纪律比在别的模块里更硬:db.Watch() 在没配存储节点时返回 nil(它没有回落),
// 而 db.Get() 在任何启用了扩展的部署上都返回一个**可用的**句柄 —— 也就是说
// 用错句柄不会报错,只会把监听记录默默写进主扩展库,而管理端从存储节点读,
// 页面上永远是空的。收成一处之后这个漏点只可能出现在这个文件里。

// handle 取出一个已绑定 ctx 的存储节点句柄。
func handle(ctx context.Context) (*gorm.DB, error) {
	gdb := db.Watch()
	if gdb == nil {
		return nil, db.ErrNotReady
	}
	return gdb.WithContext(ctx), nil
}

// loadTask 读出一个任务。不存在时返回 errNotFound。
func loadTask(ctx context.Context, gdb *gorm.DB, id int64) (Task, error) {
	var row Task
	err := gdb.WithContext(ctx).Where("id = ?", id).Take(&row).Error
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return row, errNotFound
		}
		db.MarkWatchFailure(err)
		return row, err
	}
	return row, nil
}

// activeTasks 读出全部**可能生效**的任务,供内存快照使用。
//
// 判据只有 status:窗口与条数上限留给快照在内存里算(见 snapshot.go 的 alive)。
// 把它们也写进 WHERE 会让快照的口径依赖数据库的时钟,而热路径判定用的是本节点
// 的时钟 —— 两者相差几秒时,一个任务会在"库说没到期、本节点说到期了"之间反复。
func activeTasks(ctx context.Context, gdb *gorm.DB, limit int) ([]Task, error) {
	rows := make([]Task, 0, 16)
	err := gdb.WithContext(ctx).
		Where("status = ?", StatusRunning).
		Order("id asc").Limit(limit).Find(&rows).Error
	if err != nil {
		db.MarkWatchFailure(err)
		return nil, err
	}
	return rows, nil
}

// countActive 数出运行中的任务数,供 max_active_tasks 闸门使用。
func countActive(ctx context.Context, gdb *gorm.DB) (int64, error) {
	var n int64
	err := gdb.WithContext(ctx).Model(&Task{}).Where("status = ?", StatusRunning).Count(&n).Error
	if err != nil {
		db.MarkWatchFailure(err)
		return 0, err
	}
	return n, nil
}

// persist 在**一个事务里**预留名额并写下记录。
//
// # 为什么必须是一个事务
//
// "抽满 max_records 条自动停止"是运营在页面上写下的承诺,而它只能靠一条带条件的
// 原子 UPDATE 兑现:
//
//	UPDATE ... SET captured = captured + 1
//	WHERE id = ? AND status = 'running' AND (max_records = 0 OR captured < max_records)
//
// RowsAffected 为 0 就是"没抢到",此时这一条不落库。先 COUNT 再 INSERT 的写法在
// 多节点(甚至单节点多 worker)下必然超抓,而超抓的表现是"我明明设了 500 条,
// 库里有 623 条" —— 一个没有人能解释、也没有办法回滚的数字。
//
// 预留与插入必须同一个事务:先 UPDATE 后 INSERT 而 INSERT 失败,计数就会比真实
// 行数高,任务会提前停在一个"抽满了但看不到那么多条"的状态。两张表都在这个
// 存储节点上,所以这个事务是本地的 —— 它不跨库,也不碰主库(那正是
// module.WatchTabler 的硬约束要保住的东西)。
//
// 返回 false 表示名额没抢到(任务已满或已不在运行中),调用方据此把任务停掉。
func persist(ctx context.Context, gdb *gorm.DB, taskId int64, row *Capture) (bool, error) {
	reserved := false
	err := gdb.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		res := tx.Model(&Task{}).
			Where("id = ? AND status = ?", taskId, StatusRunning).
			Where("max_records = 0 OR captured < max_records").
			UpdateColumn("captured", gorm.Expr("captured + 1"))
		if res.Error != nil {
			return res.Error
		}
		if res.RowsAffected == 0 {
			return nil
		}
		reserved = true
		// 归属由**这里**写死,而不是让调用方在别处填一遍:两个地方各写一次的
		// 后果不是报错,是一条记录挂在另一个任务名下 —— 而它同时会让那个任务的
		// captured 与它实际拥有的行数对不上,两边都查不出来。
		row.TaskId = taskId
		return tx.Create(row).Error
	})
	if err != nil {
		db.MarkWatchFailure(err)
		return false, err
	}
	return reserved, nil
}

// finishTask 把一个任务标记成"已停止",并写下原因。
//
// 条件里的 status = running 是幂等闸:多个 worker 可能在同一秒发现同一个任务
// 抽满了,只有第一个改得动。它同时保证一个被管理员手动停掉的任务不会被这条
// 后到的自动逻辑改写成 finished —— 那会让"谁停的"这个问题失去答案。
func finishTask(ctx context.Context, gdb *gorm.DB, taskId int64, status, reason string) error {
	err := gdb.WithContext(ctx).Model(&Task{}).
		Where("id = ? AND status = ?", taskId, StatusRunning).
		Updates(map[string]any{
			"status":         status,
			"stopped_at":     common.GetTimestamp(),
			"stopped_reason": reason,
			"updated_at":     common.GetTimestamp(),
			"version":        gorm.Expr("version + 1"),
		}).Error
	if err != nil {
		db.MarkWatchFailure(err)
	}
	return err
}

// insertTask 写下一个新任务。
func insertTask(ctx context.Context, gdb *gorm.DB, row *Task) error {
	err := gdb.WithContext(ctx).Create(row).Error
	if err != nil {
		db.MarkWatchFailure(err)
	}
	return err
}
