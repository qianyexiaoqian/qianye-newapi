package riskwatch

import (
	"context"
	"fmt"
	"runtime"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/qianye/config"
	"github.com/QuantumNous/new-api/qianye/db"
)

// gc.go —— 两个后台任务:清理过期记录、把跑完的任务停掉。
//
// 两者都跑在扩展库的租约保护下(lease.Run),多节点部署不会双跑。注意租约表
// 住在**主扩展库**里,而这两个任务操作的是存储节点 —— 这是本模块唯一一处
// 同时依赖两个库的地方,也是唯一一处允许这样的地方:租约是"谁来跑"的协调,
// 不是业务数据,它与被清理的行之间没有任何事务关系。

// PruneCaptures 按 expires_at 滚动清理过期记录。
//
// # 为什么按 expires_at 而不是回头读任务的保留期
//
// 保留期在落库那一刻就折算成了绝对时刻(见 Capture.ExpiresAt)。这让清理成为
// 一条**带索引的范围删除**,而另一条路(每轮把任务表读进内存、按 task_id 分组
// 逐组删)会随着任务数增长变成一次全表扫描 —— 在一个正在被热路径写入的库上。
//
// 代价是改任务保留期时要一次性重写已有行的这一列,那由管理端接口负责
// (见 api_admin.go 的 rewriteExpiry),是一次有边界的、管理员触发的操作。
//
// expires_at = 0 表示永久保留,永远不会落进 WHERE 里。
//
// # 为什么是"先取一批主键再按主键删"
//
// DELETE ... ORDER BY / LIMIT 是 MySQL 专有扩展,PostgreSQL 直接语法错误,
// 而这里出错只会被写进日志 —— 那会让这张最大的表在 PG 部署上静默地永远不清理。
// 违规检测的两个清理任务顶上记着同一条教训。
func PruneCaptures(ctx context.Context) {
	gdb := db.Watch()
	if gdb == nil {
		return
	}
	batch := config.Get().RiskWatch.GCBatchSize
	if batch <= 0 {
		batch = 2000
	}
	now := common.GetTimestamp()

	deleted := int64(0)
	for {
		// 失去租约后立刻停手:被别的节点接管之后还在写就是双跑。
		if ctx.Err() != nil {
			return
		}
		var ids []int64
		if err := gdb.WithContext(ctx).Model(&Capture{}).
			Where("expires_at > 0 AND expires_at <= ?", now).
			Order("id").Limit(batch).Pluck("id", &ids).Error; err != nil {
			db.MarkWatchFailure(err)
			common.SysError("qianye/riskwatch: 清理过期监听记录失败: " + err.Error())
			return
		}
		if len(ids) == 0 {
			break
		}
		res := gdb.WithContext(ctx).Where("id IN ?", ids).Delete(&Capture{})
		if res.Error != nil {
			db.MarkWatchFailure(res.Error)
			common.SysError("qianye/riskwatch: 清理过期监听记录失败: " + res.Error.Error())
			return
		}
		deleted += res.RowsAffected
		if len(ids) < batch {
			break
		}
		// 每批之间主动让出:这个库同时在被热路径写入,一轮不间断的批量删除
		// 会把它的 IO 全部占住,而丢失的正是这段时间里该抓的记录。
		runtime.Gosched()
	}
	if deleted > 0 {
		common.SysLog(fmt.Sprintf("qianye/riskwatch: 已清理过期监听记录 %d 条", deleted))
	}
}

// SweepWindows 把时间窗已经结束的运行中任务改成 expired。
//
// # 它不是"让任务停止抓取"的那道闸
//
// 停止抓取由内存快照负责(watchTask.alive 按本节点时钟判窗口),而且是立即的。
// 这个任务解决的是**状态可见性**:没有它,一个昨天到期的任务会在管理端一直
// 显示"运行中" —— 而管理员据此以为还在抓,直到发现记录数三天没动。
//
// 一个零流量的站点上,那个任务甚至永远不会被热路径看到一眼。所以这件事必须由
// 后台任务做,不能挂在"下一次命中时顺手改掉"上。
//
// 判据用的是数据库那一侧的当前时间参数(由本节点传入),与快照的判据是同一个
// 语义(now >= ends_at)。两者的时钟可能差几秒,后果只是状态晚几秒翻,
// 而不是"抓了不该抓的" —— 后者由快照那一侧兜住。
func SweepWindows(ctx context.Context) {
	gdb := db.Watch()
	if gdb == nil {
		return
	}
	if ctx.Err() != nil {
		return
	}
	now := common.GetTimestamp()
	res := gdb.WithContext(ctx).Table((&Task{}).TableName()).
		Where("status = ? AND ends_at > 0 AND ends_at <= ?", StatusRunning, now).
		Updates(map[string]any{
			"status":         StatusExpired,
			"stopped_at":     now,
			"stopped_reason": ReasonWindowEnd,
			"updated_at":     now,
		})
	if res.Error != nil {
		db.MarkWatchFailure(res.Error)
		common.SysError("qianye/riskwatch: 结束到期监听任务失败: " + res.Error.Error())
		return
	}
	if res.RowsAffected > 0 {
		common.SysLog(fmt.Sprintf("qianye/riskwatch: 已结束到期的监听任务 %d 个", res.RowsAffected))
		// 快照里那些任务已经因为窗口过期而 alive=false,重载只是让它们不再
		// 占着清单。失败不影响正确性,所以不回报错误。
		_ = reloadCtx(ctx)
	}
}
