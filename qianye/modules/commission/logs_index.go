package commission

// logs_index.go —— 日消费明细依赖的那条覆盖索引的补建。
//
// # 为什么不写进 model.Log 的 gorm tag
//
// 写 tag 最省事,AutoMigrate 会在三种数据库上各自建好、也天然幂等。但它会在
// **启动时**同步建索引:备份库 447 万行实测建这条索引要 68 秒,而 logs 是站点
// 里最大的一张表,真实生产还要更久。把 68 秒(或更久)加进启动路径,换来的是
// 每次滚动升级都多一段没人预期的不可用。
//
// 所以补建挪到启动之后的后台协程里,并且:
//
//   - 走 lease:多节点同时 CREATE INDEX 在 MySQL 上是两条互相等待的 DDL;
//   - 先 HasIndex 再建:这条判断很便宜,而重复建索引在各方言下的报错各不相同;
//   - 建不出来不影响任何功能:接口那边带 context 截止时间自己保护自己,
//     index_ready 会在管理端如实显示 false。
//
// # ClickHouse
//
// 日志库可以是 ClickHouse(common.DatabaseTypeClickHouse)。它是列存,
// 这条 (type, created_at) 过滤 + GROUP BY user_id 的聚合本来就是它的强项,
// 而 B-Tree 二级索引这个概念在那边根本不成立(它只有 skip index,语法完全不同)。
// 直接跳过 —— 不是"暂不支持",是那边不需要。

import (
	"context"
	"strings"
	"sync/atomic"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/qianye/service/lease"

	"github.com/bytedance/gopkg/util/gopool"
)

// logsIndexReady 缓存最近一次探测结果,供接口把 index_ready 下发给管理端。
//
// 它只是个显示位,不参与任何判据 —— 判据是查询自己的 context 超时。
// 必须是原子的:写在 lease 的后台协程上,读在每一条 HTTP 请求上。
var logsIndexReady atomic.Bool

func logsDailyConsumeIndexReady() bool { return logsIndexReady.Load() }

// logsDailyConsumeIndexDDL 给出当前日志库方言下的建索引语句。
// 第二个返回值为 false 表示这个方言不需要(或不支持)这条索引。
//
// 列名 type / group 这类保留字在三种方言下的引号不同,所以逐个方言拼,
// 不共用一份字符串。
func logsDailyConsumeIndexDDL() (string, bool) {
	cols := []string{"type", "created_at", "user_id", "quota"}
	var quote func(string) string
	switch {
	case common.UsingLogDatabase(common.DatabaseTypeClickHouse):
		return "", false
	case common.UsingLogDatabase(common.DatabaseTypePostgreSQL):
		quote = func(s string) string { return `"` + s + `"` }
	default:
		// MySQL 与 SQLite 都用反引号(SQLite 为兼容 MySQL 接受反引号)。
		quote = func(s string) string { return "`" + s + "`" }
	}
	quoted := make([]string, 0, len(cols))
	for _, col := range cols {
		quoted = append(quoted, quote(col))
	}
	return "CREATE INDEX " + logsDailyConsumeIndex + " ON " + quote("logs") +
		" (" + strings.Join(quoted, ", ") + ")", true
}

// probeLogsDailyConsumeIndex 只读地探测索引在不在,并更新 index_ready。
//
// 它不需要租约:HasIndex 是一次元数据查询,每个节点各探各的没有任何副作用。
// 拆出来是因为 index_ready 会直接显示在管理端上 —— 如果只在租约任务里更新,
// 那么**没抢到租约的节点**、以及抢到租约的节点在第一次 tick 之前(最长 5 分钟),
// 都会把一条实际存在的索引报成"未就绪",在界面上就是一条每次重启必现、
// 而且没有任何办法验证真假的红色告警。
func probeLogsDailyConsumeIndex(ctx context.Context) bool {
	if model.LOG_DB == nil {
		return false
	}
	if _, supported := logsDailyConsumeIndexDDL(); !supported {
		// ClickHouse 不需要这条索引,直接报告就绪,免得管理端一直显示告警。
		logsIndexReady.Store(true)
		return true
	}
	ok := model.LOG_DB.WithContext(ctx).Migrator().HasIndex(&model.Log{}, logsDailyConsumeIndex)
	logsIndexReady.Store(ok)
	return ok
}

// ensureLogsDailyConsumeIndex 探测索引是否存在,不存在就补建一次。
//
// 它每次心跳都跑,但常态下只做一次 HasIndex(元数据查询,毫秒级):
// 做成一次性的话,DBA 中途删掉索引就再也没人补回来,而那正是这张报表
// 从秒级掉到分钟级的唯一原因。
func ensureLogsDailyConsumeIndex(ctx context.Context) {
	if probeLogsDailyConsumeIndex(ctx) {
		return
	}
	ddl, supported := logsDailyConsumeIndexDDL()
	if !supported {
		return
	}
	common.SysLog("qianye/commission: logs 缺少 " + logsDailyConsumeIndex +
		" 覆盖索引,开始补建(大表可能耗时数十秒到数分钟)")
	if err := model.LOG_DB.WithContext(ctx).Exec(ddl).Error; err != nil {
		common.SysError("qianye/commission: 补建 " + logsDailyConsumeIndex + " 失败: " + err.Error())
		return
	}
	logsIndexReady.Store(true)
	common.SysLog("qianye/commission: " + logsDailyConsumeIndex + " 补建完成")
}

// startLogsIndexMaintenance 挂上补建任务。
//
// 启动时先只读探一次,让 index_ready 立刻反映真相(见 probe 上的注释);
// 补建本身走租约,心跳 5 分钟。lease.Run 是**等第一次 tick 才跑**的,所以
// 这个数同时也是"全新部署之后多久这张报表才快起来"。心跳每次都探,是为了
// DBA 中途删掉索引时能自己补回来 —— 做成一次性的话,那之后就再也没人补了。
func startLogsIndexMaintenance() {
	gopool.Go(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		probeLogsDailyConsumeIndex(ctx)
	})
	lease.Run("commission.logs_index", 5*time.Minute, func(ctx context.Context) {
		// 建索引本身可能跑很久,不能受心跳周期的 ctx 限制;但也不能无限久,
		// 所以给一个明确的上界。
		c, cancel := context.WithTimeout(ctx, 30*time.Minute)
		defer cancel()
		ensureLogsDailyConsumeIndex(c)
	})
}
