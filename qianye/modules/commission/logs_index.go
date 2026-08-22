package commission

// logs_index.go —— 日消费明细依赖的那两条覆盖索引的补建。
//
// # 两条索引,两个提问方向
//
//	idx_qy_logs_daily_consume  (type, created_at, user_id, quota)
//	    "这段时间里,每个人各花了多少"  —— 主表,按时间开区间、按人聚合
//	idx_qy_logs_token_daily    (user_id, type, created_at, token_id, quota)
//	    "这个人,这段时间里每天/每把密钥各花了多少" —— 按人等值、再按时间开区间
//
// 两条不能合并,因为**等值列必须排在范围列前面**才能把范围收窄。主表的等值列
// 是 type(基数 3),下钻的等值列是 user_id;把下钻挂到第一条上,优化器只能用
// (type, created_at) 那段前缀把整段时间的行全部扫一遍再逐行比 user_id。
//
// # 第二条为什么带上 token_id —— 以及旧的 idx_qy_logs_user_daily 为什么退役
//
// 它原本是 (user_id, type, created_at, quota)。密钥页的「今日消耗」要按
// **token_id** 分组,而 token_id 不在里面 —— 备份库实测同一条查询
// 3865/3967/4071 ms(Extra: Using index condition; Using MRR),21 万行逐行回表。
// 把 token_id 补进去之后是 231/249/433 ms(Extra: Using where; Using index)。
//
// 加宽之后旧的那条就是**纯冗余的前缀**:按天下钻用到的四列一列不少地包含在
// 新索引里。实测(同一台机器,31 天下钻,区间内 53 万行,缓冲池已热):
//
//	只用旧索引(IGNORE 新的)  306 / 289 / 270 ms
//	只用新索引(IGNORE 旧的)  281 / 275 / 267 ms
//
// 也就是说多出来的那一列在扫描上量不出代价,而留着旧索引要在一张 447 万行、
// 已经挂了 15 条二级索引的表上多养一棵 B+ 树。所以它进 retiredLogsIndexes,
// 由 ensureLogsIndexes 在**确认新索引已经就绪之后**才删 —— 顺序反过来会出现
// 一段两条索引都没有的窗口,而那段时间里按天下钻要跑 6.5 秒。
//
// 建索引本身很贵:备份库实测 CREATE INDEX 这一条耗时 498 秒。这正是它跑在
// 后台租约里、而不是启动路径上的理由。
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
// 上面这组数字量的是加宽之前的 (user_id, type, created_at, quota)。加宽到
// 五列之后按天下钻的实测见文件头那一段,两者在同一台机器上量不出差别。
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
	{name: logsTokenDailyIndex, cols: []string{"user_id", "type", "created_at", "token_id", "quota"}},
}

// retiredLogsIndex 是一条「已被某个超集索引取代」的旧索引。
//
// supersededBy 不是文档,是**删除的前置条件**:只有当那条超集索引真的建好了,
// 这一条才允许被删。反过来做会开出一段两条都没有的窗口 —— 那段时间里按天
// 下钻要跑 6.5 秒,而且没有任何一处会说明为什么。
type retiredLogsIndex struct {
	name         string
	supersededBy string
}

// retiredLogsIndexes 是要在补建任务里顺手清掉的历史索引。
//
// 只清**本模块自己建过**的那些。上游 model.Log 的 gorm tag 索引一条都不动:
// 它们由 AutoMigrate 负责,这里删掉下次启动又会建回来,变成每次重启一次
// 空转 DDL。
var retiredLogsIndexes = []retiredLogsIndex{
	// (user_id, type, created_at, quota) —— 逐位是 idx_qy_logs_token_daily 的前缀。
	// 加宽的理由与两者的实测对照见文件头。
	{name: logsUserDailyIndex, supersededBy: logsTokenDailyIndex},
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
	return logsIndexDDLFor(common.LogDatabaseType(), spec)
}

// logsIndexDDLFor 是 logsIndexDDL 的纯函数内核,方言由参数给出而不是读全局。
//
// ─────────────── PostgreSQL 为什么必须 CONCURRENTLY ───────────────
//
// 普通的 CREATE INDEX 在 PG 上对目标表取 SHARE 锁,**整段构建期间该表的
// 全部写入排队**。而 logs 的写入就在每一次 relay 消费日志的路径上
// (model/log.go 的 createLog 是裸的 LOG_DB.Create,连 WithContext 都没有,
// 全仓也没有 statement_timeout / lock_timeout),被挡住的 INSERT 是无限等待,
// 不是报错返回 —— 表现是升级后一段时间里所有请求挂住,而日志里只有一行
// 「开始补建」。
//
// 备份库实测(PG 17.6,logs 300 万行 / 797 MB):非 CONCURRENTLY 建索引 6421 ms,
// 而正好跨过构建窗口的那一条单行 INSERT 耗时 6288 ms(基线 98~155 ms)。
// DROP 那一半更凶:DROP INDEX 要 ACCESS EXCLUSIVE,它排队等一个 25 秒的普通
// SELECT 时,后到的 INSERT 被挡在它后面 22067 ms —— 停摆时长不由建索引快慢
// 封顶,而由 logs 上最慢的并发读封顶。
//
// MySQL 8 走 online DDL 不挡 DML(同一条索引实测建了 15253 ms,707 次并发
// INSERT 最差 220 ms,无一次阻塞),SQLite 单写者本来就没有这个问题。所以
// CONCURRENTLY 只加在 PG 这一支上。
//
// 代价有二,都在下面兜住了:
//   - CONCURRENTLY 不能跑在事务块里 —— GORM 的 Exec 走自动提交,不包事务;
//   - 中途失败会留下一条 indisvalid=false 的**无效索引**,HasIndex 照样返回
//     true。所以 PG 上的就绪判据是 logsIndexUsableSQL 而不是 HasIndex,
//     无效的那条会先被 DROP 掉再重建(见 ensureLogsIndexes)。
//
// 列名 type / group 这类保留字在三种方言下的引号不同,所以逐个方言拼,
// 不共用一份字符串。
func logsIndexDDLFor(dbType common.DatabaseType, spec *logsIndexSpec) (string, bool) {
	var quote func(string) string
	create := "CREATE INDEX "
	switch dbType {
	case common.DatabaseTypeClickHouse:
		return "", false
	case common.DatabaseTypePostgreSQL:
		quote = func(s string) string { return `"` + s + `"` }
		// IF NOT EXISTS 让重试幂等:CONCURRENTLY 失败之后留下的那条无效索引
		// 由 ensureLogsIndexes 先删掉,这里只兜并发节点同时补建的情形。
		create = "CREATE INDEX CONCURRENTLY IF NOT EXISTS "
	default:
		// MySQL 与 SQLite 都用反引号(SQLite 为兼容 MySQL 接受反引号)。
		quote = func(s string) string { return "`" + s + "`" }
	}
	quoted := make([]string, 0, len(spec.cols))
	for _, col := range spec.cols {
		quoted = append(quoted, quote(col))
	}
	return create + spec.name + " ON " + quote("logs") +
		" (" + strings.Join(quoted, ", ") + ")", true
}

// logsIndexDropDDLFor 给出删索引的语句;第二个返回值为 false 表示这个方言
// 没有专用写法,交给 GORM Migrator().DropIndex。
//
// PG 上同样要 CONCURRENTLY:普通 DROP INDEX 取 ACCESS EXCLUSIVE,它一旦排队
// 等一个慢查询,后到的 logs 写入全部堵在它后面(实测 22 秒)。
func logsIndexDropDDLFor(dbType common.DatabaseType, name string) (string, bool) {
	if dbType != common.DatabaseTypePostgreSQL {
		return "", false
	}
	return "DROP INDEX CONCURRENTLY IF EXISTS " + name, true
}

// logsIndexUsableSQL 是 PG 上"这条索引能不能真的被优化器用"的判据。
//
// CREATE INDEX CONCURRENTLY 中途失败会留下 indisvalid=false 的索引:它在
// pg_class 里存在(HasIndex 返回 true),但查询规划器不会用它。只看 HasIndex
// 的话,补建任务从此认为已经建好,而报表永远是慢的 —— 一个既没有告警、
// 也没有自愈路径的状态。
const logsIndexUsableSQL = `SELECT COUNT(*) FROM pg_class c
JOIN pg_index i ON i.indexrelid = c.oid
WHERE c.relname = ? AND i.indisvalid AND i.indisready`

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
		spec.ready.Store(logsIndexPresentAndUsable(ctx, spec.name))
	}
}

// logsIndexPresentAndUsable 回答"这条索引现在在,而且能被优化器用吗"。
//
// 非 PG 上两者等价,一次 HasIndex 就够。PG 上不等价:CONCURRENTLY 建到一半
// 失败会留下一条 indisvalid=false 的索引,HasIndex 说"在",而查询规划器不用
// 它。把那种状态报成就绪,补建任务就再也不会重建它。
func logsIndexPresentAndUsable(ctx context.Context, name string) bool {
	if model.LOG_DB == nil {
		return false
	}
	db := model.LOG_DB.WithContext(ctx)
	if !db.Migrator().HasIndex(&model.Log{}, name) {
		return false
	}
	if !common.UsingLogDatabase(common.DatabaseTypePostgreSQL) {
		return true
	}
	var usable int64
	if err := db.Raw(logsIndexUsableSQL, name).Scan(&usable).Error; err != nil {
		// 判不出来时按"不可用"处理会让补建任务反复重跑一条昂贵的 DDL,
		// 所以这里按"在"处理并留痕:索引确实存在,最坏也只是慢,而反复
		// CONCURRENTLY 重建是在 hot path 上反复加锁。
		common.SysError("qianye/commission: 无法判断 " + name + " 是否有效: " + err.Error())
		return true
	}
	return usable > 0
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
		// PG 上「不就绪」有两种:根本没有,或者上一次 CONCURRENTLY 建到一半
		// 失败留下的无效索引。后者必须先删掉 —— IF NOT EXISTS 看到同名的它
		// 会直接跳过,于是永远建不出一条可用的。
		if common.UsingLogDatabase(common.DatabaseTypePostgreSQL) &&
			model.LOG_DB.WithContext(ctx).Migrator().HasIndex(&model.Log{}, spec.name) {
			common.SysLog("qianye/commission: logs 上的 " + spec.name +
				" 是一条无效索引(上一次并发建索引没跑完),先删掉再重建")
			if dropDDL, ok := logsIndexDropDDLFor(common.LogDatabaseType(), spec.name); ok {
				if err := model.LOG_DB.WithContext(ctx).Exec(dropDDL).Error; err != nil {
					common.SysError("qianye/commission: 删除无效的 " + spec.name + " 失败: " + err.Error())
					continue
				}
			}
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
	dropRetiredLogsIndexes(ctx)
}

// dropRetiredLogsIndexes 删掉已被超集取代的历史索引。
//
// 判据是 droppableRetiredLogsIndexes,单独拆出来是因为「什么时候才允许删」
// 才是这段代码唯一会出事的地方:早一步删就是一段没有索引的窗口。
//
// 删不掉不影响任何功能 —— 多一条冗余索引只是多花磁盘,而且下一次心跳还会再试。
func dropRetiredLogsIndexes(ctx context.Context) {
	if model.LOG_DB == nil {
		return
	}
	if common.UsingLogDatabase(common.DatabaseTypeClickHouse) {
		// 本模块从来没在 ClickHouse 上建过这些索引(见 logsIndexDDL),
		// 自然也没有要删的。
		return
	}
	migrator := model.LOG_DB.WithContext(ctx).Migrator()
	for _, name := range droppableRetiredLogsIndexes(logsIndexReady) {
		if !migrator.HasIndex(&model.Log{}, name) {
			continue
		}
		common.SysLog("qianye/commission: logs 上的 " + name + " 已被超集索引取代,开始删除")
		// PG 走 DROP INDEX CONCURRENTLY:普通 DROP 要 ACCESS EXCLUSIVE,一旦
		// 排队等一个慢查询,后到的 logs 写入(消费日志,在 relay 的 hot path 上)
		// 全部堵在它后面。备份库实测那段停摆是 22 秒,与索引大小无关。
		var dropErr error
		if dropDDL, ok := logsIndexDropDDLFor(common.LogDatabaseType(), name); ok {
			dropErr = model.LOG_DB.WithContext(ctx).Exec(dropDDL).Error
		} else {
			dropErr = migrator.DropIndex(&model.Log{}, name)
		}
		if dropErr != nil {
			common.SysError("qianye/commission: 删除 " + name + " 失败: " + dropErr.Error())
			continue
		}
		common.SysLog("qianye/commission: " + name + " 已删除")
	}
}

// droppableRetiredLogsIndexes 返回此刻**允许删**的历史索引名。
//
// ready 是「某条索引现在在不在」的判据(生产传 logsIndexReady,测试可以传别的)。
// 超集没就绪时一条都不返回 —— 这就是"先建后删"那条顺序,写成纯函数是为了
// 让它有一个不依赖数据库的测试落点。
func droppableRetiredLogsIndexes(ready func(string) bool) []string {
	out := make([]string, 0, len(retiredLogsIndexes))
	for _, retired := range retiredLogsIndexes {
		if !ready(retired.supersededBy) {
			continue
		}
		out = append(out, retired.name)
	}
	return out
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
