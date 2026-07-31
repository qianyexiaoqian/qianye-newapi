// Package qianye 是千夜扩展的入口。
//
// 扩展的全部新功能(余额划转、邀请返佣、提现、日志增强、分组可见性修复、
// 可用率监控、违规检测)都活在这个包及其子包里,数据存放在独立的 MySQL,
// 通过独立的 YAML 配置。上游代码中只保留极少量的单行挂载点。
//
// 三个挂载点(见 main.go):
//   - InitResources() 尾部  → Init()
//   - StartSystemTaskRunner() 之后 → StartBackgroundTasks()
//   - router.SetRouter() 之前 → RegisterRoutes(server)
package qianye

import (
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/qianye/config"
	"github.com/QuantumNous/new-api/qianye/db"
	qymodel "github.com/QuantumNous/new-api/qianye/model"
	"github.com/QuantumNous/new-api/qianye/module"
	"github.com/QuantumNous/new-api/qianye/service/lease"
	"github.com/QuantumNous/new-api/qianye/service/twophase"
)

// Init 初始化扩展。
//
// 挂载点必须在 InitResources() 尾部,因为这里依赖:
//   - godotenv.Load 已执行(QIANYE_CONFIG 等环境变量可用)
//   - common.InitEnv 已执行(IsMasterNode 已赋值,迁移才能正确 gate)
//   - model.InitDB / InitLogDB 已完成(扩展要读主库用户与日志)
//   - Redis、i18n 已就绪
//
// 返回 error 的场景仅限配置错误与启动期连不上数据库,此时主程序会 FatalLog ——
// 配置写错就该立刻炸,而不是带着一个永远不可用的扩展跑起来。
//
// 找不到配置文件不是错误:扩展静默禁用,主程序行为与上游完全一致。
func Init() error {
	if err := config.Load(); err != nil {
		return err
	}
	if !config.Enabled() {
		return nil
	}

	// 配置项自检:把"定义了、有默认值、写进了 YAML,却没有任何代码读它"的开关
	// 打成启动告警。这类缺陷已经出现过四次(C1/C2/OLD-1/OLD-2),而且对运维完全
	// 不可见 —— 改完 YAML 重启,日志一切正常,闸门却是空的。不阻断启动:
	// 空闸门本身不造成资损,让主程序起不来才会。
	config.LogFieldConsumerCheck()

	if err := db.Init(config.Get().Database); err != nil {
		return err
	}
	// 缺表自检在 db.Migrate 内部完成,但它的结论是分级的(见 db.Migrate 的契约):
	//   - 本节点刚亲自跑完 AutoMigrate 却仍缺表 → 返回 error,在此 FatalLog;
	//     那是本节点自己的 bug(模型漏登记 / DDL 没生效),重启多少次都一样。
	//   - 从节点、auto_migrate=false、另一节点正在迁移 → 绝不返回缺表 error。
	//     这些节点结构性地建不出表,让整台网关(含全部上游 relay 流量)退出
	//     既修不好 schema,又会在主节点尚未迁移完的窗口里把从节点打进重启循环。
	tables := allTables()
	if err := db.Migrate(tables...); err != nil {
		return err
	}
	// 上面那条"不阻断"换来的可见性在这里补齐:确认缺表时扩展进入 schema 降级态,
	// 后台每分钟点名一次缺哪张表,主节点/DBA 把表建出来后自动解除,无需重启本节点。
	// 启动时那一行日志会被滚走,这个循环不会。
	db.StartSchemaRecheck(tables...)

	// 主库探针表。它与资金变更写在同一个主库事务里,是判定
	// "主库副作用是否已生效"的唯一权威依据。
	if config.Get().TwoPhase.OutboxEnabled() {
		if err := model.QyEnsureFundOutbox(); err != nil {
			return err
		}
	}

	db.StartHealthLoop()

	// 给上游包里的 hook 变量赋值。此刻早于任何 HTTP 请求与后台协程,
	// 因此不存在并发读写窗口。
	for _, m := range module.All() {
		m.InstallHooks()
	}

	common.SysLog("qianye: 扩展初始化完成")
	return nil
}

// allTables 汇总地基表与各模块表。
func allTables() []any {
	tables := qymodel.FoundationTables()
	for _, m := range module.All() {
		tables = append(tables, m.Tables()...)
	}
	return tables
}

// StartBackgroundTasks 启动后台常驻任务。
//
// 挂载点在 main.go 的 service.StartSystemTaskRunner() 之后 ——
// 该位置晚于 InitResources(),因此 Init() 必然已经执行完毕。
//
// 所有任务都跑在扩展库的租约保护下,多节点部署时不会双跑。
func StartBackgroundTasks() {
	if !config.Enabled() {
		return
	}
	// 双保险:即使挂载顺序被改动导致本函数先于 Init 执行,也不会 nil panic。
	if db.Get() == nil {
		common.SysError("qianye: 扩展库尚未就绪,后台任务未启动(请检查 main.go 中的挂载顺序)")
		return
	}
	if !config.Get().Runtime.BackgroundOn() {
		common.SysLog("qianye: runtime.background_enabled 为 false,后台任务未启动")
		return
	}

	lease.Run("twophase.compensate", twophase.Interval(), twophase.Compensate)
	lease.Run("twophase.prune_outbox", 6*time.Hour, twophase.PruneOutbox)

	for _, m := range module.All() {
		m.StartTasks()
	}

	if config.Get().Runtime.ConfigReloadSeconds > 0 {
		config.WatchReload()
	}
	common.SysLog("qianye: 后台任务已启动")
}

// Close 释放扩展持有的资源。
//
// 刻意不挂进 main.go 的 defer(节省改动预算):进程退出时操作系统会回收连接,
// 后台任务持有的租约会按 TTL 自然过期被其他节点接管。
func Close() error { return db.Close() }
