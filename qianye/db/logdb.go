package db

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/qianye/config"

	"github.com/bytedance/gopkg/util/gopool"
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
// # 它挂掉不会拖累任何人
//
// 台账写入全部走 guard.HotAsync,失败即丢一条日志;读取只在管理端。因此这里
// 的熔断比主库那一套简单:没有 fail-open 语义要维护,唯一目的是"库挂了之后
// 别让每一条异步作业都去撞一次 30 秒的连接超时",那会把仅有的几个 hot worker
// 全部堵死 —— 而那一刻主库、relay、资金路径全都是好的。
var (
	logHandle atomic.Pointer[gorm.DB]

	logHealthy    atomic.Bool
	logFailStreak atomic.Int32
	logOpenUntil  atomic.Int64 // 熔断打开至该 unix 秒

	logLastPingMs atomic.Int64
	logLastPingAt atomic.Int64

	logHealthOnce sync.Once
)

// InitLog 建立台账库连接。dsn 留空时不做任何事(不分家)。
//
// 与 Init 同一条纪律:启动期连不上视为配置错误,返回 error 让主程序 FatalLog。
// 运维显式填了一个 dsn 却连不上,几乎总是写错了 —— 静默降级回主库会让那张表
// 悄悄写到另一个库里,而两边都有一半数据是最难查的一种状态。
func InitLog(cfg config.Database) error {
	if !config.Get().LogDatabaseSeparate() {
		return nil
	}
	gdb, pingMs, err := openDatabase(cfg, "台账", "[QY-LOGDB] ")
	if err != nil {
		return err
	}
	logLastPingMs.Store(pingMs)
	logLastPingAt.Store(common.GetTimestamp())
	logHandle.Store(gdb)
	logHealthy.Store(true)
	logFailStreak.Store(0)
	logOpenUntil.Store(0)
	common.SysLog("qianye: 台账数据库已连接(AI 审核明细将写入该库)")
	return nil
}

// LogSeparate 表示台账表这一轮住在自己的库里。
func LogSeparate() bool { return logHandle.Load() != nil }

// LogHandle 返回台账库的**裸**句柄:没分家时为 nil。
//
// 只有迁移与健康探测该用它 —— 业务代码要的是 Log(),那一个带回落。
func LogHandle() *gorm.DB { return logHandle.Load() }

// Log 返回台账表该用的句柄:分家了就是台账库,没分家就是主库。
//
// 业务代码一律用它,不要自己判断分没分家:那等于把"零值 = 不分家"这条契约
// 抄到每一个调用点,而漏抄的那一处会在没分家的部署上写到 nil 句柄。
func Log() *gorm.DB {
	if gdb := logHandle.Load(); gdb != nil {
		return gdb
	}
	return Get()
}

// LogAvailable 表示台账表此刻可写。没分家时它就是 Available()。
func LogAvailable() bool {
	if logHandle.Load() == nil {
		return Available()
	}
	if common.GetTimestamp() < logOpenUntil.Load() {
		return false
	}
	return logHealthy.Load()
}

// MarkLogFailure 由台账库读写的调用点在拿到 error 时调用。
//
// 判据与 MarkFailure 逐字相同(只有连接级错误计入),但计数是**分开**的:
// 合用一个计数器会让台账库的一次批量清理超时把主库的熔断也顶开,而那一刻
// relay 与资金路径全都是好的 —— 那是把一个日志故障放大成一次风控停摆。
func MarkLogFailure(err error) {
	if logHandle.Load() == nil {
		MarkFailure(err)
		return
	}
	if err == nil || errors.Is(err, gorm.ErrRecordNotFound) {
		return
	}
	if !isConnLevelError(err) {
		return
	}
	threshold := int32(config.Get().Runtime.BreakerFailureThreshold)
	if threshold <= 0 {
		threshold = 5
	}
	if logFailStreak.Add(1) >= threshold {
		openSecs := config.Get().Runtime.BreakerOpenSeconds
		if openSecs <= 0 {
			openSecs = 30
		}
		logOpenUntil.Store(common.GetTimestamp() + int64(openSecs))
		logHealthy.Store(false)
		common.SysError("qianye: 台账数据库熔断已打开(审核明细将暂时丢弃,主库与 relay 不受影响): " + err.Error())
	}
}

// StartLogHealthLoop 周期性探测台账库并在恢复后闭合熔断。
//
// 与 StartHealthLoop 同一条理由:台账写入是异步的、熔断打开之后连一次尝试都
// 不会发出,没有这个循环熔断就再也关不上,表现是"审核日志从某一刻起永远是空的"。
func StartLogHealthLoop() {
	if logHandle.Load() == nil {
		return
	}
	logHealthOnce.Do(func() {
		gopool.Go(func() {
			interval := config.Get().Runtime.HealthIntervalSeconds
			if interval <= 0 {
				interval = 15
			}
			ticker := time.NewTicker(time.Duration(interval) * time.Second)
			defer ticker.Stop()
			for range ticker.C {
				probeLog()
			}
		})
	})
}

func probeLog() {
	gdb := logHandle.Load()
	if gdb == nil {
		return
	}
	sqlDB, err := gdb.DB()
	if err != nil {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()

	start := time.Now()
	if err := sqlDB.PingContext(ctx); err != nil {
		logHealthy.Store(false)
		common.SysError("qianye: 台账数据库健康探测失败: " + err.Error())
		return
	}
	logLastPingMs.Store(time.Since(start).Milliseconds())
	logLastPingAt.Store(common.GetTimestamp())

	// 与 markProbeHealthy 同口径:只有"不健康 → 健康"这一次转变才清零失败计数。
	// 无条件清零会让"可达但查询慢"这个唯一重要的场景下熔断永远打不开。
	if !logHealthy.Load() {
		logHealthy.Store(true)
		logFailStreak.Store(0)
		logOpenUntil.Store(0)
		common.SysLog("qianye: 台账数据库已恢复")
	}
}

// LogStats 返回台账库的连接池与熔断状态,供管理端健康面板展示。
//
// 没分家时返回 {"separate": false} 一个键就够了:再把主库那份数字抄一遍,
// 面板上会出现两组一模一样的读数,而它们其实是同一个连接池。
func LogStats() map[string]any {
	gdb := logHandle.Load()
	if gdb == nil {
		return map[string]any{"separate": false}
	}
	m := map[string]any{
		"separate":           true,
		"available":          LogAvailable(),
		"breaker_open_until": logOpenUntil.Load(),
		"fail_streak":        logFailStreak.Load(),
		"last_ping_ms":       logLastPingMs.Load(),
		"last_ping_at":       logLastPingAt.Load(),
		"connected":          true,
	}
	if sqlDB, err := gdb.DB(); err == nil {
		st := sqlDB.Stats()
		m["open_conns"] = st.OpenConnections
		m["in_use"] = st.InUse
		m["idle"] = st.Idle
		m["wait_count"] = st.WaitCount
		m["max_open"] = st.MaxOpenConnections
	}
	return m
}
