package db

import (
	"github.com/QuantumNous/new-api/qianye/config"

	"gorm.io/gorm"
)

// 风控预警库 —— 监听记录的**必选**独立存储节点。
//
// # 为什么它必选,而台账库可选
//
// 台账库解决的是"这张表比邻居大三个数量级"。风控预警解决的是另一件事:
// 它存的是被抽中请求的**完整上下文**(归一化后的整段提示词),而抽多少、
// 抽多久由运营在管理端临时决定 —— 一个"永久监听 + 100% 概率"的任务,一天就能
// 写进几十 GB。这个量既不由代码决定,也不由部署时的配置决定,而是由某个管理员
// 下午三点点的那一下决定。
//
// 把它放进任何一个已有的库都会得到同一个结局:某次监管操作把磁盘写满,而跟它
// 一起躺下的是佣金账本、两阶段资金单,或者整个网关的日志表。所以这个功能强制
// 要求一个**单独配置的存储节点**:没有 risk_watch.database.dsn,模块整个不注册,
// 表不建、路由不挂、热路径上一次判断都不做。
//
// # 因此它没有回落
//
// Watch() 在没配的时候返回 nil,而不是像 Log() 那样回落到主扩展库。回落在这里
// 是错的:它会让"忘了配存储节点"这件事表现成"功能正常,数据默默写进了主库",
// 而这正是这一段配置存在的全部理由要防的事。
//
// # 它挂掉不会拖累任何人
//
// 写入全部走 guard.HotAsync 且先判 WatchAvailable();读取只在管理端。
// 库挂了的后果是这段时间的监听记录丢失(会打日志),relay、资金、主库全都不受影响 ——
// 风控预警是观察者,不是闸门。
var watchStore = &secondary{
	label:        "风控预警",
	logPrefix:    "[QY-WATCHDB] ",
	connectedLog: "qianye: 风控预警存储节点已连接(监听记录将写入该库)",
	breakerLog:   "qianye: 风控预警存储节点熔断已打开(监听记录将暂时丢弃,relay 与主库不受影响): ",
	recoveredLog: "qianye: 风控预警存储节点已恢复",
}

// InitWatch 建立风控预警库连接。没配 dsn 时不做任何事,模块自己会保持关闭。
func InitWatch(cfg config.Database) error {
	if !config.Get().RiskWatch.On() {
		return nil
	}
	return watchStore.open(cfg)
}

// WatchConnected 表示存储节点这一轮真的连上了。
//
// 它与 config 里的 RiskWatch.On() 是两个问题:后者问"配了吗",
// 前者问"连上了吗"。启动期连不上会直接 FatalLog,所以正常运行时两者相等;
// 分开是为了让 Close() 之后与单测里的半初始化状态有确定答案。
func WatchConnected() bool { return watchStore.connected() }

// Watch 返回风控预警库句柄。**没配时返回 nil,不回落主库**(见本文件头)。
func Watch() *gorm.DB { return watchStore.bare() }

// WatchAvailable 表示监听记录此刻可写。
func WatchAvailable() bool { return watchStore.usable() }

// MarkWatchFailure 由风控预警库读写的调用点在拿到 error 时调用。
func MarkWatchFailure(err error) { watchStore.markFailure(err) }

// StartWatchHealthLoop 周期性探测存储节点并在恢复后闭合熔断。没配时直接返回。
func StartWatchHealthLoop() { watchStore.startHealthLoop() }

// SetWatchHandleForTest 直接替换存储节点句柄,返回还原函数。**仅测试使用。**
//
// 存在的理由:风控预警的核心不变量(抽满 max_records 自动停止)只活在一条带
// 条件的原子 UPDATE 里,证明它必须真的跑一个数据库。而句柄住在 secondary 的
// 私有字段上 —— 别的包用 //go:linkname 够得着 watchStore 这个变量,却够不着
// 它的类型,于是拿不到那个字段。
//
// 与 groupns.ResetResiduesForTest 同一档:名字里写死 ForTest,让"生产代码里
// 出现了它"这件事在 review 时一眼可见。
func SetWatchHandleForTest(gdb *gorm.DB) func() {
	prev := watchStore.handle.Swap(gdb)
	prevHealthy := watchStore.healthy.Swap(gdb != nil)
	return func() {
		watchStore.handle.Store(prev)
		watchStore.healthy.Store(prevHealthy)
	}
}

// WatchStats 返回存储节点的连接池与熔断状态,供管理端健康面板展示。
//
// 没配时只回 {"configured": false}:这一格在面板上要能一眼答出"风控预警页面
// 打不开是因为没配存储节点",而不是显示一组全零的连接池读数。
func WatchStats() map[string]any {
	if !watchStore.connected() {
		return map[string]any{"configured": false}
	}
	m := watchStore.stats()
	m["configured"] = true
	return m
}
