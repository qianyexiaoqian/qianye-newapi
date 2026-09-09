package db

import (
	"github.com/QuantumNous/new-api/qianye/config"

	"gorm.io/gorm"
)

// 台账库 —— 高频只读明细的**可选**独立数据库。
//
// # 它解决的是一个体量问题,不是一个功能问题
//
// 住在这里的表(目前只有 qy_violation_ai_review)有两条与资金表完全不同的性质:
//
//	行数正比于**被抽中的请求数**,不是成交笔数 —— 两者可以差三到四个数量级
//	每小时按保留期批量删一遍 —— 大批量删除在 InnoDB 上留碎片、在 PostgreSQL 上
//	攒死元组等 autovacuum
//
// 把这两条压在佣金账本、两阶段资金单旁边毫无必要:那些表靠 SELECT ... FOR UPDATE
// 串行化读改写,最不该跟一条清理任务抢 IO。备份口径也不同 —— 资金要能按时间点
// 恢复,一份滚动三天的审核台账不要。
//
// # 零值方向:留空 = 不分家
//
// log_database.dsn 留空时 Log() 返回主库句柄,台账表跟着 allTables() 一起迁移,
// 与本文件存在之前**逐字节一致**。升级不要求任何部署去准备一个新库。
//
// 这一条是它与风控预警库(见 watchdb.go)唯一的、也是全部的分歧:那个库没有
// 回落,没配就整个功能不注册。连接、熔断、健康探测这些机制两者共用一份
// (见 secondary)。
//
// # 它挂掉不会拖累任何人
//
// 台账写入全部走 guard.HotAsync,失败即丢一条日志;读取只在管理端。
var logStore = &secondary{
	label:        "台账",
	logPrefix:    "[QY-LOGDB] ",
	connectedLog: "qianye: 台账数据库已连接(AI 审核明细将写入该库)",
	breakerLog:   "qianye: 台账数据库熔断已打开(审核明细将暂时丢弃,主库与 relay 不受影响): ",
	recoveredLog: "qianye: 台账数据库已恢复",
}

// InitLog 建立台账库连接。dsn 留空时不做任何事(不分家)。
func InitLog(cfg config.Database) error {
	if !config.Get().LogDatabaseSeparate() {
		return nil
	}
	return logStore.open(cfg)
}

// LogSeparate 表示台账表这一轮住在自己的库里。
func LogSeparate() bool { return logStore.connected() }

// LogHandle 返回台账库的**裸**句柄:没分家时为 nil。
//
// 只有迁移与健康探测该用它 —— 业务代码要的是 Log(),那一个带回落。
func LogHandle() *gorm.DB { return logStore.bare() }

// Log 返回台账表该用的句柄:分家了就是台账库,没分家就是主库。
//
// 业务代码一律用它,不要自己判断分没分家:那等于把"零值 = 不分家"这条契约
// 抄到每一个调用点,而漏抄的那一处会在没分家的部署上写到 nil 句柄。
func Log() *gorm.DB {
	if gdb := logStore.bare(); gdb != nil {
		return gdb
	}
	return Get()
}

// LogAvailable 表示台账表此刻可写。没分家时它就是 Available()。
func LogAvailable() bool {
	if !logStore.connected() {
		return Available()
	}
	return logStore.usable()
}

// MarkLogFailure 由台账库读写的调用点在拿到 error 时调用。
//
// 没分家时转发给主库那一套 —— 那时两者本来就是同一个连接池,
// 分开计数只会让主库的故障被记两遍、却哪一遍都不到阈值。
func MarkLogFailure(err error) {
	if !logStore.connected() {
		MarkFailure(err)
		return
	}
	logStore.markFailure(err)
}

// StartLogHealthLoop 周期性探测台账库并在恢复后闭合熔断。没分家时直接返回 ——
// 那时 Log() 就是主库句柄,StartHealthLoop 已经在探它了。
func StartLogHealthLoop() { logStore.startHealthLoop() }

// LogStats 返回台账库的连接池与熔断状态,供管理端健康面板展示。
//
// 没分家时返回 {"separate": false} 一个键就够了:再把主库那份数字抄一遍,
// 面板上会出现两组一模一样的读数,而它们其实是同一个连接池。
func LogStats() map[string]any {
	if !logStore.connected() {
		return map[string]any{"separate": false}
	}
	m := logStore.stats()
	m["separate"] = true
	return m
}
