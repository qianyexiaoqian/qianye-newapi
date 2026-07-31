// Package db 管理千夜扩展的独立 MySQL 连接。
//
// 与主库的关系(见 qianye/docs/design-00-foundation.md §2):
//   - 完全独立的 *gorm.DB,不复用 model.DB / model.LOG_DB
//   - 绝不调用 common.SetMainDatabaseType / SetLogDatabaseType —— 那是全局单例,
//     会污染 model.initCol() 对列名引号(反引号 vs 双引号)的判断,进而搞坏原项目
//     所有拼接 SQL
//   - 不复用 SQL_MAX_IDLE_CONNS 等主库环境变量,连接池参数一律来自扩展自己的 YAML
package db

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"log"
	"os"
	"strings"
	"sync/atomic"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/qianye/config"

	"gorm.io/driver/mysql"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
	gormlogger "gorm.io/gorm/logger"
)

var (
	handle atomic.Pointer[gorm.DB]

	// 熔断状态。新库故障时热路径必须放行,不能拖垮主业务。
	healthy    atomic.Bool
	failStreak atomic.Int32
	openUntil  atomic.Int64 // 熔断打开至该 unix 秒

	lastPingMs atomic.Int64
	lastPingAt atomic.Int64
)

// ErrNotReady 表示扩展库尚未初始化或已不可用。
var ErrNotReady = errors.New("qianye: 扩展数据库不可用")

// Init 建立连接并做一次带超时的 Ping。
//
// 启动期连不上视为配置错误,返回 error 让主程序 FatalLog —— DSN 写错就该立刻炸,
// 而不是带着一个永远不可用的扩展跑起来。运行期断连才走熔断 + fail-open。
func Init(cfg config.Database) error {
	dsn := normalizeDSN(cfg.DSN)

	gcfg := &gorm.Config{
		PrepareStmt: true,
		Logger: gormlogger.New(
			log.New(os.Stdout, "[QY-DB] ", log.LstdFlags),
			gormlogger.Config{
				SlowThreshold:             time.Duration(cfg.SlowThresholdMs) * time.Millisecond,
				LogLevel:                  parseLogLevel(cfg.LogLevel),
				IgnoreRecordNotFoundError: true,
				Colorful:                  false,
			},
		),
		// 单条语句不自动包事务:扩展的写入要么本身在显式事务里,
		// 要么是幂等的单条 upsert,默认事务只会平添开销。
		SkipDefaultTransaction: true,
	}

	gdb, err := gorm.Open(mysql.Open(dsn), gcfg)
	if err != nil {
		return fmt.Errorf("qianye: 连接扩展数据库失败: %w", err)
	}
	sqlDB, err := gdb.DB()
	if err != nil {
		return fmt.Errorf("qianye: 获取扩展数据库连接池失败: %w", err)
	}
	sqlDB.SetMaxIdleConns(cfg.MaxIdleConns)
	sqlDB.SetMaxOpenConns(cfg.MaxOpenConns)
	sqlDB.SetConnMaxLifetime(time.Duration(cfg.ConnMaxLifetimeSeconds) * time.Second)
	sqlDB.SetConnMaxIdleTime(time.Duration(cfg.ConnMaxIdleTimeSeconds) * time.Second)

	ctx, cancel := context.WithTimeout(context.Background(),
		time.Duration(cfg.ConnectTimeoutSeconds)*time.Second)
	defer cancel()
	start := time.Now()
	if err := sqlDB.PingContext(ctx); err != nil {
		_ = sqlDB.Close()
		return fmt.Errorf("qianye: 扩展数据库 Ping 失败(请检查 database.dsn 与网络): %w", err)
	}
	lastPingMs.Store(time.Since(start).Milliseconds())
	lastPingAt.Store(common.GetTimestamp())

	handle.Store(gdb)
	healthy.Store(true)
	failStreak.Store(0)
	openUntil.Store(0)
	common.SysLog("qianye: 扩展数据库已连接")
	return nil
}

// Get 返回 GORM 句柄。未初始化时返回 nil,调用方应先用 Available() 判断。
func Get() *gorm.DB { return handle.Load() }

// Available 表示扩展库当前可用(已连接、健康、且熔断未打开)。
func Available() bool {
	if handle.Load() == nil {
		return false
	}
	if common.GetTimestamp() < openUntil.Load() {
		return false
	}
	return healthy.Load()
}

// MarkFailure 由所有扩展库读写的调用点在拿到 error 时调用。
//
// 只有连接级错误才计入熔断:唯一键冲突、记录不存在这类业务错误是正常现象,
// 计入的话会让正常的幂等冲突把熔断打开。
func MarkFailure(err error) {
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
	if failStreak.Add(1) >= threshold {
		openSecs := config.Get().Runtime.BreakerOpenSeconds
		if openSecs <= 0 {
			openSecs = 30
		}
		openUntil.Store(common.GetTimestamp() + int64(openSecs))
		healthy.Store(false)
		common.SysError("qianye: 扩展数据库熔断已打开: " + err.Error())
	}
}

// MarkSuccess 重置连续失败计数。
func MarkSuccess() { failStreak.Store(0) }

// LockForUpdate 给查询加行锁。
//
// 扩展库固定是 MySQL,不需要像 model.lockForUpdate 那样做 SQLite 分支;
// 也不能复用它 —— 那个函数判断的是主库类型,主库若为 SQLite 会静默不加锁。
func LockForUpdate(tx *gorm.DB) *gorm.DB {
	return tx.Clauses(clause.Locking{Strength: "UPDATE"})
}

// Close 关闭连接池。
//
// 刻意不挂进 main.go 的 defer(省一行改动预算):进程退出由操作系统回收连接,
// 后台任务持有的租约会按 TTL 自然过期被其他节点接管。
func Close() error {
	gdb := handle.Load()
	if gdb == nil {
		return nil
	}
	sqlDB, err := gdb.DB()
	if err != nil {
		return err
	}
	handle.Store(nil)
	healthy.Store(false)
	return sqlDB.Close()
}

// Stats 返回连接池与熔断状态,供管理端健康面板展示。
func Stats() map[string]any {
	m := map[string]any{
		"available":        Available(),
		"breaker_open_until": openUntil.Load(),
		"fail_streak":      failStreak.Load(),
		"last_ping_ms":     lastPingMs.Load(),
		"last_ping_at":     lastPingAt.Load(),
	}
	gdb := handle.Load()
	if gdb == nil {
		m["connected"] = false
		return m
	}
	m["connected"] = true
	if sqlDB, err := gdb.DB(); err == nil {
		s := sqlDB.Stats()
		m["open_conns"] = s.OpenConnections
		m["in_use"] = s.InUse
		m["idle"] = s.Idle
		m["wait_count"] = s.WaitCount
		m["max_open"] = s.MaxOpenConnections
	}
	return m
}

// normalizeDSN 补齐 MySQL DSN 的必要参数。
//
// parseTime 照抄 model/main.go 的做法:不加的话 GORM 无法把 DATETIME 扫进 time.Time。
// charset 直接要求 utf8mb4 —— 扩展库是我们自建的,不必像上游那样做兼容性探测。
func normalizeDSN(dsn string) string {
	dsn = strings.TrimSpace(dsn)
	add := func(d, kv string) string {
		if strings.Contains(d, "?") {
			return d + "&" + kv
		}
		return d + "?" + kv
	}
	if !strings.Contains(dsn, "parseTime") {
		dsn = add(dsn, "parseTime=true")
	}
	if !strings.Contains(dsn, "charset") && !strings.Contains(dsn, "collation") {
		dsn = add(dsn, "charset=utf8mb4")
	}
	return dsn
}

func parseLogLevel(s string) gormlogger.LogLevel {
	switch s {
	case config.LogLevelSilent:
		return gormlogger.Silent
	case config.LogLevelError:
		return gormlogger.Error
	case config.LogLevelInfo:
		return gormlogger.Info
	default:
		return gormlogger.Warn
	}
}

// isConnLevelError 判断错误是否属于"连接坏了"而非"业务不满足"。
func isConnLevelError(err error) bool {
	if errors.Is(err, sql.ErrConnDone) ||
		errors.Is(err, context.DeadlineExceeded) ||
		errors.Is(err, context.Canceled) {
		return true
	}
	msg := strings.ToLower(err.Error())
	for _, s := range []string{
		"invalid connection",
		"bad connection",
		"connection refused",
		"connection reset",
		"broken pipe",
		"no such host",
		"i/o timeout",
		"too many connections",
		"server shutdown",
		"driver: bad connection",
		"can't connect",
		"lost connection",
	} {
		if strings.Contains(msg, s) {
			return true
		}
	}
	return false
}
