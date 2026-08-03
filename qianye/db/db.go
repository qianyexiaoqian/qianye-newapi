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
	"strconv"
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
	dsn := normalizeDSN(cfg)

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
	if err := registerOpProbe(gdb); err != nil {
		return fmt.Errorf("qianye: 注册扩展数据库访问探针失败: %w", err)
	}
	sqlDB, err := gdb.DB()
	if err != nil {
		return fmt.Errorf("qianye: 获取扩展数据库连接池失败: %w", err)
	}
	sqlDB.SetMaxIdleConns(cfg.MaxIdleConns)
	sqlDB.SetMaxOpenConns(cfg.MaxOpenConns)
	sqlDB.SetConnMaxLifetime(time.Duration(cfg.ConnMaxLifetimeSeconds) * time.Second)
	sqlDB.SetConnMaxIdleTime(time.Duration(cfg.ConnMaxIdleTimeSeconds) * time.Second)

	// 与 DSN 里的 timeout= 走同一个回落值:配置层的 0 表示"这个键没写过头"
	// 而不是"零秒",直接 WithTimeout(0) 会造出一个已经过期的 ctx,首次 Ping
	// 必然 DeadlineExceeded,而报出来的错只会说"请检查 dsn 与网络"。
	ctx, cancel := context.WithTimeout(context.Background(),
		time.Duration(effectiveSeconds(cfg.ConnectTimeoutSeconds, 5))*time.Second)
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
//
// 调用方必须确认"本次确实成功访问过扩展库"。纯内存操作绝不能调用它 ——
// 那会让熔断在"库可达但查询变慢"时永远打不开(见 WithOpProbe 的说明)。
func MarkSuccess() { failStreak.Store(0) }

// ───────────────────────── 扩展库访问探针 ─────────────────────────

type opProbeKey struct{}

type opProbe struct{ touched atomic.Bool }

// WithOpProbe 给 ctx 挂一个"本次是否真的执行过扩展库语句"的探针,
// 返回新的 ctx 与一个读取函数。
//
// 存在的理由(熔断可用性判据):MarkFailure 要求"连续 N 次失败"才打开熔断,
// 而 guard 的热路径 hook 里混着大量纯内存作业(可用率采样是 O(1) 内存累加,
// 必然返回 nil)。若不区分,采样成功就会把失败计数清零,而采样频率远高于
// 失败频率 —— 结果是扩展库"可达但慢"这一唯一重要场景下熔断永远打不开。
//
// 探针挂在 ctx 上而不是用全局计数器:全局计数器会被并发 worker 的语句污染,
// 无法归因到"本次这个 hook 到底有没有查库"。代价是只有 WithContext(ctx) 的
// 语句才会被记到 —— 这正好也是我们想要的:没接 ctx 的调用既没有超时保护,
// 也不该有资格给熔断投"健康"票。
func WithOpProbe(parent context.Context) (context.Context, func() bool) {
	p := &opProbe{}
	return context.WithValue(parent, opProbeKey{}, p), p.touched.Load
}

// noteOp 是注册到 GORM 所有语句回调上的探针钩子。
func noteOp(tx *gorm.DB) {
	if tx == nil || tx.Statement == nil || tx.Statement.Context == nil {
		return
	}
	if p, ok := tx.Statement.Context.Value(opProbeKey{}).(*opProbe); ok && p != nil {
		p.touched.Store(true)
	}
}

// registerOpProbe 把 noteOp 挂到六类语句的 After 回调上。
//
// 用 After 而不是 Before:失败的语句同样算"访问过库",调用方据此
// 才能把超时/报错正确归因给扩展库而不是当成纯内存作业。
func registerOpProbe(gdb *gorm.DB) error {
	cb := gdb.Callback()
	const name = "qianye:op_probe"
	for _, err := range []error{
		cb.Create().After("gorm:create").Register(name, noteOp),
		cb.Query().After("gorm:query").Register(name, noteOp),
		cb.Update().After("gorm:update").Register(name, noteOp),
		cb.Delete().After("gorm:delete").Register(name, noteOp),
		cb.Row().After("gorm:row").Register(name, noteOp),
		cb.Raw().After("gorm:raw").Register(name, noteOp),
	} {
		if err != nil {
			return err
		}
	}
	return nil
}

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
		"available":          Available(),
		"breaker_open_until": openUntil.Load(),
		"fail_streak":        failStreak.Load(),
		"last_ping_ms":       lastPingMs.Load(),
		"last_ping_at":       lastPingAt.Load(),
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
//
// timeout / readTimeout / writeTimeout 是驱动层的硬上界,不能省:
//   - connect_timeout_seconds 原先只用于 Init 的那一次 Ping,连接池后续新建连接
//     的 dial 完全没有超时,对端机器整机失联时会挂到 TCP 默认超时(分钟级);
//   - 没有 readTimeout 时,一条撞上行锁的语句会一直等到 MySQL 的
//     innodb_lock_wait_timeout(默认 50 秒)。即使调用方带了 ctx,ctx 取消只是
//     让 Go 侧放弃等待,连接仍被占着 —— 连接池会被慢查询逐个吃光。
//
// 注意 readTimeout 是"连接级"的,不是"语句级":它必须显著大于最慢的一次合法
// 后台操作(结算事务、批量清理),否则正常业务会被驱动层随机切断,且 MySQL
// 侧的事务处于未知状态。因此它是兜底闸门(默认 30 秒),精确的 200ms 热路径
// 上界由调用方的 ctx 负责,两者是不同层次的防线。
func normalizeDSN(cfg config.Database) string {
	dsn := strings.TrimSpace(cfg.DSN)
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
	// 已在 DSN 里显式写过的参数一律尊重运维的选择,不覆盖。
	if !strings.Contains(dsn, "timeout=") {
		dsn = add(dsn, "timeout="+secondsParam(cfg.ConnectTimeoutSeconds, 5))
	}
	if !strings.Contains(dsn, "readTimeout=") {
		dsn = add(dsn, "readTimeout="+secondsParam(cfg.ReadTimeoutSeconds, 30))
	}
	if !strings.Contains(dsn, "writeTimeout=") {
		dsn = add(dsn, "writeTimeout="+secondsParam(cfg.WriteTimeoutSeconds, 30))
	}
	return dsn
}

// migrationDSN 在业务 DSN 基础上去掉读写超时,供自动迁移专用。
//
// 迁移里的 GET_LOCK 等待与大表 DDL 天然会超过任何合理的连接级超时,
// 被驱动掐断的后果是启动失败或 DDL 停在半迁移态(MySQL 的 DDL 不可回滚)。
// 迁移的时间上界由 migrateTimeout 的 ctx 负责,不需要驱动层再插一手。
func migrationDSN(cfg config.Database) string {
	dsn := normalizeDSN(cfg)
	// 驱动把 0 解释为"永不超时",正是迁移需要的。
	dsn = replaceParam(dsn, "readTimeout", "0")
	return replaceParam(dsn, "writeTimeout", "0")
}

// replaceParam 覆盖 DSN 查询串里的某个参数;不存在则追加。
func replaceParam(dsn, key, value string) string {
	kv := key + "=" + value
	idx := strings.Index(dsn, key+"=")
	if idx < 0 {
		if strings.Contains(dsn, "?") {
			return dsn + "&" + kv
		}
		return dsn + "?" + kv
	}
	end := strings.IndexByte(dsn[idx:], '&')
	if end < 0 {
		return dsn[:idx] + kv
	}
	return dsn[:idx] + kv + dsn[idx+end:]
}

// effectiveSeconds 是这些超时项"配了 0 或负数算没配"的唯一口径。
//
// 非正值一律回落到默认值:0 在驱动语义里是"永不超时",那正是我们要消灭的状态;
// 而在 context.WithTimeout 里 0 恰好相反,是"立刻超时"。DSN 参数与首次 Ping 的
// ctx 必须走同一个值,否则同一个 0 会在两处变成两种极端,谁也对不上谁。
func effectiveSeconds(v, fallback int) int {
	if v <= 0 {
		return fallback
	}
	return v
}

// secondsParam 把秒数渲染成 go-sql-driver 认识的 duration 字符串。
func secondsParam(v, fallback int) string {
	return strconv.Itoa(effectiveSeconds(v, fallback)) + "s"
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
//
// 刻意**不把** context.DeadlineExceeded / Canceled 计入:那两个是调用方自己设的
// 预算到期,不是数据库的健康信号。热路径的预算只有几百毫秒,把它喂给熔断意味着
// 一次尾延迟抖动就能让连续失败数攒够阈值,进而把整个扩展熔断数十秒 —— 熔断期间
// 所有热路径事件被直接丢弃。判据被自己的预算污染,是比慢查询严重得多的故障。
//
// 真正的"连接坏了"由下面的驱动层错误串识别,其中包含驱动自身的 i/o timeout
// (readTimeout 触发),那才是连接确实不可用的信号。
func isConnLevelError(err error) bool {
	if errors.Is(err, context.DeadlineExceeded) || errors.Is(err, context.Canceled) {
		return false
	}
	if errors.Is(err, sql.ErrConnDone) {
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
