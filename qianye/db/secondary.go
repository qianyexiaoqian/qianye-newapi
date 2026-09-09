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

// secondary 是"扩展的第二个库"这件事本身。
//
// # 为什么是一个类型而不是两份代码
//
// 扩展现在有两个附属库,它们的存在理由完全不同 ——
//
//	台账库(log_database)   可选。行数正比于被抽中的请求数,每小时批量删一遍,
//	                        不配就跟着主扩展库走,与它存在之前逐字节一致。
//	风控预警库(risk_watch) 必选。监听记录是逐条的完整上下文,体量与保留期都由
//	                        运营临时决定;没配这个库,整个功能不注册。
//
// —— 但它们的**运行期机制**逐字相同:建连接、连接池、熔断计数、健康探测循环、
// 恢复后闭合熔断、给健康面板出一份读数。抄第二份的代价不是重复,而是漂移:
// 熔断阈值、"只有不健康 → 健康这一次转变才清零失败计数"这类判据一旦分家,
// 症状是"其中一个库挂了之后永远不再恢复",而它没有任何报错。
//
// 所以机制在这里只有一份,两个库各自持有一个实例,差异全部留在包装函数里
// (回落主库 / 不回落、可选 / 必选)。
//
// # 它们都不允许拖垮任何人
//
// 写入一律走 guard.HotAsync,失败即丢一条日志;读取只在管理端。因此这里的熔断
// 比主库那一套简单:没有 fail-open 语义要维护,唯一目的是"库挂了之后别让每一条
// 异步作业都去撞一次 30 秒的连接超时",那会把仅有的几个 hot worker 全部堵死 ——
// 而那一刻主库、relay、资金路径全都是好的。
type secondary struct {
	// label 是日志与错误信息里的库名(「台账」「风控预警」)。
	label string
	// logPrefix 是这个库的 GORM 日志前缀,必须与其它库不同,
	// 否则慢查询日志里分不出是谁慢。
	logPrefix string
	// connectedLog / breakerLog / recoveredLog 是三条状态日志的正文。
	// 措辞逐库不同,因为它们要说清"这个库挂了之后**站上会发生什么**",
	// 而两个库的答案不一样。
	connectedLog string
	breakerLog   string
	recoveredLog string

	handle     atomic.Pointer[gorm.DB]
	healthy    atomic.Bool
	failStreak atomic.Int32
	openUntil  atomic.Int64 // 熔断打开至该 unix 秒

	lastPingMs atomic.Int64
	lastPingAt atomic.Int64

	healthOnce sync.Once
}

// open 建立连接。调用方负责判断这一段配置是不是空的。
//
// 与 Init 同一条纪律:启动期连不上视为配置错误,返回 error 让主程序 FatalLog。
// 运维显式填了一个 dsn 却连不上,几乎总是写错了 —— 静默降级会让数据悄悄写到
// 另一个库里(或者干脆不写),而"两边都有一半"是最难查的一种状态。
func (s *secondary) open(cfg config.Database) error {
	gdb, pingMs, err := openDatabase(cfg, s.label, s.logPrefix)
	if err != nil {
		return err
	}
	s.lastPingMs.Store(pingMs)
	s.lastPingAt.Store(common.GetTimestamp())
	s.handle.Store(gdb)
	s.healthy.Store(true)
	s.failStreak.Store(0)
	s.openUntil.Store(0)
	common.SysLog(s.connectedLog)
	return nil
}

// bare 返回裸句柄:没连接时为 nil。
func (s *secondary) bare() *gorm.DB { return s.handle.Load() }

// connected 表示这个库这一轮真的连上了。
func (s *secondary) connected() bool { return s.handle.Load() != nil }

// usable 表示此刻可以往这个库写。没连接时恒为 false —— 调用方要的回落语义
// (台账库有、风控库没有)由包装函数决定,不在这里猜。
func (s *secondary) usable() bool {
	if !s.connected() {
		return false
	}
	if common.GetTimestamp() < s.openUntil.Load() {
		return false
	}
	return s.healthy.Load()
}

// markFailure 由读写这个库的调用点在拿到 error 时调用。
//
// 判据与主库的 MarkFailure 逐字相同(只有连接级错误计入),但**计数是各算各的**:
// 合用一个计数器会让附属库的一次批量清理超时把主库的熔断也顶开,而那一刻
// relay 与资金路径全都是好的 —— 那是把一个日志故障放大成一次风控停摆。
func (s *secondary) markFailure(err error) {
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
	if s.failStreak.Add(1) < threshold {
		return
	}
	openSecs := config.Get().Runtime.BreakerOpenSeconds
	if openSecs <= 0 {
		openSecs = 30
	}
	s.openUntil.Store(common.GetTimestamp() + int64(openSecs))
	s.healthy.Store(false)
	common.SysError(s.breakerLog + err.Error())
}

// startHealthLoop 周期性探测并在恢复后闭合熔断。
//
// 与 StartHealthLoop 同一条理由:附属库的写入是异步的、熔断打开之后连一次尝试
// 都不会发出,没有这个循环熔断就再也关不上,表现是"这张表从某一刻起永远是空的"。
func (s *secondary) startHealthLoop() {
	if !s.connected() {
		return
	}
	s.healthOnce.Do(func() {
		gopool.Go(func() {
			interval := config.Get().Runtime.HealthIntervalSeconds
			if interval <= 0 {
				interval = 15
			}
			ticker := time.NewTicker(time.Duration(interval) * time.Second)
			defer ticker.Stop()
			for range ticker.C {
				s.probe()
			}
		})
	})
}

func (s *secondary) probe() {
	gdb := s.handle.Load()
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
		s.healthy.Store(false)
		common.SysError("qianye: " + s.label + "数据库健康探测失败: " + err.Error())
		return
	}
	s.lastPingMs.Store(time.Since(start).Milliseconds())
	s.lastPingAt.Store(common.GetTimestamp())

	// 与 markProbeHealthy 同口径:只有"不健康 → 健康"这一次转变才清零失败计数。
	// 无条件清零会让"可达但查询慢"这个唯一重要的场景下熔断永远打不开。
	if !s.healthy.Load() {
		s.healthy.Store(true)
		s.failStreak.Store(0)
		s.openUntil.Store(0)
		common.SysLog(s.recoveredLog)
	}
}

// stats 返回连接池与熔断状态,供管理端健康面板展示。
func (s *secondary) stats() map[string]any {
	gdb := s.handle.Load()
	if gdb == nil {
		return map[string]any{"connected": false}
	}
	m := map[string]any{
		"connected":          true,
		"available":          s.usable(),
		"breaker_open_until": s.openUntil.Load(),
		"fail_streak":        s.failStreak.Load(),
		"last_ping_ms":       s.lastPingMs.Load(),
		"last_ping_at":       s.lastPingAt.Load(),
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

// close 释放连接。没连接时是空操作。
func (s *secondary) close() error {
	gdb := s.handle.Load()
	if gdb == nil {
		return nil
	}
	sqlDB, err := gdb.DB()
	if err != nil {
		return err
	}
	s.handle.Store(nil)
	s.healthy.Store(false)
	return sqlDB.Close()
}
