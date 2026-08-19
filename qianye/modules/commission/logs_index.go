package commission

// logs_index.go —— 日消费明细依赖的那两条覆盖索引的补建。
//
// # 两条索引,两个提问方向
//
//	idx_qy_logs_daily_consume  (type, created_at, user_id, quota)
//	    "这段时间里,每个人各花了多少"  —— 主表,按时间开区间、按人聚合
//	idx_qy_logs_user_daily     (user_id, type, created_at, quota)
//	    "这个人,这段时间里每天各花了多少" —— 按天下钻,按人等值、再按时间开区间
//
// 两条不能合并,因为**等值列必须排在范围列前面**才能把范围收窄。主表的等值列
// 是 type(基数 3),下钻的等值列是 user_id;把下钻挂到第一条上,优化器只能用
// (type, created_at) 那段前缀把整段时间的行全部扫一遍再逐行比 user_id。
//
// 备份库实测(MySQL 8.0.28,logs 447 万行 / type=2 416 万行,缓冲池已热):
//
//	按天下钻,31 天,最重的那个用户(区间内 24 万行)
//	    只有第一条索引   6523 ms   ← 优化器改走 idx_user_id_id,12 万次回表
//	    加上第二条索引    163 ms   ← EXPLAIN 从 Using where 变成 Using index
//	    全量 150 天       237 ms
//	主表汇总(对照,加索引前后不变)
//	    1 天 193 ms / 31 天 356 ms
//
// 也就是说:**没有第二条索引的按天下钻是一条会把主库拖住的接口**,而不是
// "慢一点"。这正是把它写进补建任务、而不是"以后有空再加"的理由。
//
// # 为什么不写进 model.Log 的 gorm tag
//
// 写 tag 最省事,AutoMigrate 会在三种数据库上各自建好、也天然幂等。但它会在
// **启动时**同步建索引:备份库 447 万行实测建一条要 34~68 秒,而 logs 是站点
// 里最大的一张表,真实生产还要更久。把这段时间加进启动路径,换来的是每次
// 滚动升级都多一段没人预期的不可用。
//
// 所以补建挪到启动之后的后台协程里,并且:
//
//   - 走 lease:多节点同时 CREATE INDEX 在 MySQL 上是两条互相等待的 DDL;
//   - 先 HasIndex 再建:这条判断很便宜,而重复建索引在各方言下的报错各不相同;
//   - 建不出来不影响任何功能:接口那边带 context 截止时间自己保护自己,
//     index_ready 会在管理端如实显示 false。
//
// # 写入代价
//
// logs 上已经有 13 个二级索引。第一条的前缀 (type, created_at) 随时间单调递增,
// 新行永远落在 B+ 树最右边的少数几个叶页上;第二条以 user_id 打头,插入位置
// 随用户分散 —— 但这与表上**早就存在**的 idx_user_id_id / idx_logs_user_id
// 是同一种形状,不是新引入的一类代价。
//
// # ClickHouse
//
// 日志库可以是 ClickHouse(common.DatabaseTypeClickHouse)。它是列存,
// 这类过滤 + GROUP BY 的聚合本来就是它的强项,而 B-Tree 二级索引这个概念在
// 那边根本不成立(它只有 skip index,语法完全不同)。直接跳过 ——
// 不是"暂不支持",是那边不需要。

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

// logsIndexSpec 是一条待补建的覆盖索引。
//
// ready 缓存最近一次探测结果,供接口把 index_ready 下发给管理端。它只是个
// 显示位,不参与任何判据 —— 判据是查询自己的 context 超时。必须是原子的:
// 写在 lease 的后台协程上,读在每一条 HTTP 请求上。
type logsIndexSpec struct {
	name  string
	cols  []string
	ready atomic.Bool
}

// logsIndexSpecs 是本模块在 logs 上要求的全部覆盖索引。
//
// 用指针切片而不是值切片:元素里有 atomic.Bool,值切片的 range 会复制它,
// 复制出来的那一份的 Store 谁也看不见。
var logsIndexSpecs = []*logsIndexSpec{
	{name: logsDailyConsumeIndex, cols: []string{"type", "created_at", "user_id", "quota"}},
	{name: logsUserDailyIndex, cols: []string{"user_id", "type", "created_at", "quota"}},
}

func logsIndexSpecByName(name string) *logsIndexSpec {
	for _, spec := range logsIndexSpecs {
		if spec.name == name {
			return spec
		}
	}
	return nil
}

// logsIndexReady 回答"这条索引现在在不在"。名字不认识时返回 false ——
// 不认识的索引不可能是就绪的。
func logsIndexReady(name string) bool {
	spec := logsIndexSpecByName(name)
	return spec != nil && spec.ready.Load()
}

func logsDailyConsumeIndexReady() bool { return logsIndexReady(logsDailyConsumeIndex) }

// logsIndexDDL 给出当前日志库方言下的建索引语句。
// 第二个返回值为 false 表示这个方言不需要(或不支持)这条索引。
//
// 列名 type / group 这类保留字在三种方言下的引号不同,所以逐个方言拼,
// 不共用一份字符串。
func logsIndexDDL(spec *logsIndexSpec) (string, bool) {
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
	quoted := make([]string, 0, len(spec.cols))
	for _, col := range spec.cols {
		quoted = append(quoted, quote(col))
	}
	return "CREATE INDEX " + spec.name + " ON " + quote("logs") +
		" (" + strings.Join(quoted, ", ") + ")", true
}

// probeLogsIndexes 只读地探测每条索引在不在,并更新各自的 ready 位。
//
// 它不需要租约:HasIndex 是一次元数据查询,每个节点各探各的没有任何副作用。
// 拆出来是因为 index_ready 会直接显示在管理端上 —— 如果只在租约任务里更新,
// 那么**没抢到租约的节点**、以及抢到租约的节点在第一次 tick 之前(最长 5 分钟),
// 都会把一条实际存在的索引报成"未就绪",在界面上就是一条每次重启必现、
// 而且没有任何办法验证真假的红色告警。
func probeLogsIndexes(ctx context.Context) {
	if model.LOG_DB == nil {
		return
	}
	for _, spec := range logsIndexSpecs {
		if _, supported := logsIndexDDL(spec); !supported {
			// ClickHouse 不需要这些索引,直接报告就绪,免得管理端一直显示告警。
			spec.ready.Store(true)
			continue
		}
		spec.ready.Store(model.LOG_DB.WithContext(ctx).Migrator().HasIndex(&model.Log{}, spec.name))
	}
}

// ensureLogsIndexes 探测索引是否存在,不存在就逐条补建。
//
// 它每次心跳都跑,但常态下只做几次 HasIndex(元数据查询,毫秒级):
// 做成一次性的话,DBA 中途删掉索引就再也没人补回来,而那正是这张报表
// 从秒级掉到分钟级的唯一原因。
//
// 一条建失败不影响另一条:两条索引服务的是两个不同的提问方向,主表能用的时候
// 没有理由因为下钻那条建不出来而一起停摆。
func ensureLogsIndexes(ctx context.Context) {
	if model.LOG_DB == nil {
		return
	}
	probeLogsIndexes(ctx)
	for _, spec := range logsIndexSpecs {
		if spec.ready.Load() {
			continue
		}
		ddl, supported := logsIndexDDL(spec)
		if !supported {
			continue
		}
		common.SysLog("qianye/commission: logs 缺少 " + spec.name +
			" 覆盖索引,开始补建(大表可能耗时数十秒到数分钟)")
		if err := model.LOG_DB.WithContext(ctx).Exec(ddl).Error; err != nil {
			common.SysError("qianye/commission: 补建 " + spec.name + " 失败: " + err.Error())
			continue
		}
		spec.ready.Store(true)
		common.SysLog("qianye/commission: " + spec.name + " 补建完成")
	}
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
		probeLogsIndexes(ctx)
	})
	lease.Run("commission.logs_index", 5*time.Minute, func(ctx context.Context) {
		// 建索引本身可能跑很久,不能受心跳周期的 ctx 限制;但也不能无限久,
		// 所以给一个明确的上界。两条索引共用这个上界。
		c, cancel := context.WithTimeout(ctx, 60*time.Minute)
		defer cancel()
		ensureLogsIndexes(c)
	})
}
