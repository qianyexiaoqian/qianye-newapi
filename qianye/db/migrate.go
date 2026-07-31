package db

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/qianye/config"

	"github.com/bytedance/gopkg/util/gopool"
	"gorm.io/driver/mysql"
	"gorm.io/gorm"
)

// migrateLockName 是 MySQL 命名锁的键。GET_LOCK 是连接级的,
// 因此必须固定在同一条 *sql.Conn 上获取和释放。
const migrateLockName = "qy_schema_migrate"

// migrateLockTimeoutSeconds 是等待其他节点让出迁移锁的时长。
// 超时不是错误 —— 说明别的节点正在迁移,本节点跳过即可。
const migrateLockTimeoutSeconds = 30

// migrateTimeout 是整个迁移过程的预算。DDL 在大表上很慢,给足。
const migrateTimeout = 30 * time.Minute

// openMigrationConn 打开一条迁移专用连接。
//
// 独立于业务连接池,DSN 里去掉了读写超时(见 migrationDSN)。
func openMigrationConn() (*sql.DB, error) {
	return openMigrationConnWith(config.Get().Database)
}

// openMigrationConnWith 是 openMigrationConn 的可测形态:配置由调用方传入,
// 不依赖进程级的 config 单例。
func openMigrationConnWith(cfg config.Database) (*sql.DB, error) {
	sqlDB, err := sql.Open("mysql", migrationDSN(cfg))
	if err != nil {
		return nil, fmt.Errorf("qianye: 打开迁移专用连接失败: %w", err)
	}
	// 必须允许两条连接,不能是一条。
	//
	// GET_LOCK 是连接级的,所以它固定占用一条(Migrate 里那个 conn 一直持有到
	// 函数返回)。AutoMigrate 会从同一个池再要一条来跑 DDL —— 池上限设成 1
	// 的话,DDL 会永远等那条被锁占着的连接,进程静默卡死在迁移阶段:
	// 数据库端看不到任何语句,日志停在"扩展数据库已连接"之后没有下文。
	//
	// 锁与 DDL 分处两条连接是正确的:GET_LOCK 的作用是跨节点互斥,
	// 只要有人持有即可,并不要求 DDL 跑在持锁的那条连接上。
	sqlDB.SetMaxOpenConns(2)
	sqlDB.SetMaxIdleConns(2)
	return sqlDB, nil
}

// Migrate 让扩展库的 schema 达到可用状态,并核对结果。
//
// 契约刻意是**非对称**的,取决于本节点这一轮有没有建表的资格:
//
//   - 本节点亲自跑完了 AutoMigrate —— 返回 nil 即代表 models 对应的每一张表
//     此刻都真实存在。此时"缺表"是一个自相矛盾的结论(刚建过却没有),
//     根因必然在本节点自己(模型没登记进 allTables()、DDL 没真正生效),
//     返回 error 阻断启动是对的:它是本节点能修、也只有本节点该修的问题。
//
//   - 本节点无权或无机会建表(从节点 / auto_migrate=false / 迁移锁被别人持有)——
//     **永不因为缺表返回 error**。这三类节点结构性地不可能自己把表建出来,
//     让主程序 FatalLog 既修不好 schema,又把整台网关(含全部上游 relay 流量)
//     拖下线并进入重启回退;而它看到的缺表很可能只是"另一节点此刻正在跑 DDL"
//     这个秒级到分钟级的中间态(见 autoMigrate 里关于大表 ADD COLUMN 的注释),
//     或是 DBA 下一分钟就会执行的那几条 CREATE TABLE。
//     扩展的第一原则是绝不成为主程序的单点故障。
//
// 非对称不等于把自检删掉。缺表在这一侧的结论只是从"杀进程"换成了"降级 + 持续喊":
// SysError 逐张列出表名、SchemaIncomplete() 置位、StartSchemaRecheck 周期性复查
// 并在表被建出来后自动解除。一次性的启动日志会滚走,每分钟一条的错误日志不会 ——
// 就"别让故障拖到第一个请求才暴露"这个目的而言,它并不弱于 FatalLog。
func Migrate(models ...any) error {
	gdb := Get()
	if gdb == nil {
		return ErrNotReady
	}
	if len(models) == 0 {
		return nil
	}
	err := runAutoMigrate(gdb, models)
	switch {
	case err == nil:
		return verifyTables(gdb, models)
	case errors.Is(err, errNotSchemaOwner):
		noteMissingTables(gdb, models, err)
		return nil
	default:
		return err
	}
}

// errNotSchemaOwner 表示本节点这一轮没有执行扩展库迁移。
//
// 三条早退分支共用同一个哨兵是刻意的:对缺表自检而言它们完全等价 ——
// 本节点看到的表清单不由自己负责,也无法靠自己修好,因此结论只能是降级。
// 之前只有"抢锁失败"那一条拿得到豁免,而从节点在第一道门就返回裸 nil,
// 于是走进了严格分支,缺表直接 FatalLog 整个网关(M10)。
// 具体原因用 %w 包在外层,只影响日志措辞,不影响判定。
var (
	errNotSchemaOwner = errors.New("本节点这一轮没有执行扩展库迁移")

	errNodeIsSlave = fmt.Errorf("原因:本节点是从节点,表由主节点建(%w)", errNotSchemaOwner)

	errAutoMigrateOff = fmt.Errorf("原因:database.auto_migrate 为 false,表由 DBA 手工建(%w)", errNotSchemaOwner)

	// errMigrationInProgress:多 master 部署下同时启动,只有一个节点该跑 DDL。
	errMigrationInProgress = fmt.Errorf("原因:另一节点此刻正持有迁移锁,表清单是中间态(%w)", errNotSchemaOwner)
)

// runAutoMigrate 是 autoMigrate 的可替换入口,生产路径永远是 autoMigrate。
//
// 与 tableLister 同一个理由,只是更硬:"另一 master 已持有迁移锁"与
// "本节点刚亲自跑完 DDL"这两条分支必须有一个真实的多节点 MySQL 才能自然到达,
// 上一轮因此只能写"这两条是推理保证、无自动化测试覆盖"。
// 有了这个接缝,Migrate 的四条出口全部可以从函数入口驱动。
var runAutoMigrate = autoMigrate

// autoMigrate 执行扩展库的自动迁移。
//
// 返回 nil 只有一种含义:本节点刚刚亲自把 models 的 DDL 跑完了。
// 三条"本节点没建表"的分支一律返回包着 errNotSchemaOwner 的错误,
// 由 Migrate 统一按降级处理 —— 它们不是失败,但也不能被当成"表已就绪"。
//
// 两道门缺一不可:
//  1. common.IsMasterNode —— 但它只是 NODE_TYPE != "slave" 这个环境变量,
//     多个节点完全可能都被配成 master;
//  2. MySQL GET_LOCK —— 真正的跨节点互斥。没有它,多 master 并发 AutoMigrate
//     会在 MySQL 上互相锁表甚至死锁。
func autoMigrate(gdb *gorm.DB, models []any) error {
	if !common.IsMasterNode {
		common.SysLog("qianye: 从节点,跳过扩展库自动迁移")
		return errNodeIsSlave
	}
	if !config.Get().Database.ShouldAutoMigrate() {
		common.SysLog("qianye: database.auto_migrate 为 false,跳过扩展库自动迁移")
		return errAutoMigrateOff
	}

	// 迁移必须走独立的、不受 readTimeout 约束的连接。
	//
	// 业务连接池的 DSN 里带 readTimeout(默认 30 秒),那是给热路径兜底用的:
	// 它是驱动层"每次读结果包"的硬 deadline,与 ctx 无关,也不区分语句类型。
	// 而迁移里有两类必然超过它的读:
	//  1. SELECT GET_LOCK(name, 30) —— 并发启动的另一个 master 抢锁时会阻塞满
	//     30 秒,与 readTimeout 恰好相等,谁先触发不确定。readTimeout 先到就变成
	//     错误,主程序 FatalLog,而正确行为是"另一节点在迁移,本节点跳过"后正常启动。
	//  2. 大表 ADD COLUMN —— 千万行的 DDL 超过 30 秒是常态。被驱动掐断后
	//     MySQL 的 DDL 不可回滚,表会停在半迁移态。
	migDB, err := openMigrationConn()
	if err != nil {
		return err
	}
	defer migDB.Close()

	ctx, cancel := context.WithTimeout(context.Background(), migrateTimeout)
	defer cancel()

	// GET_LOCK / RELEASE_LOCK 必须打在同一条连接上,否则释放会作用到别的连接。
	conn, err := migDB.Conn(ctx)
	if err != nil {
		return fmt.Errorf("qianye: 获取迁移专用连接失败: %w", err)
	}
	defer conn.Close()

	var got sql.NullInt64
	if err := conn.QueryRowContext(ctx, "SELECT GET_LOCK(?, ?)",
		migrateLockName, migrateLockTimeoutSeconds).Scan(&got); err != nil {
		return fmt.Errorf("qianye: 获取迁移锁失败: %w", err)
	}
	if !got.Valid || got.Int64 != 1 {
		common.SysLog("qianye: 另一节点正在执行扩展库迁移,本节点跳过")
		return errMigrationInProgress
	}
	defer func() {
		if _, err := conn.ExecContext(ctx, "SELECT RELEASE_LOCK(?)", migrateLockName); err != nil {
			common.SysError("qianye: 释放迁移锁失败: " + err.Error())
		}
	}()

	// DDL 也必须跑在迁移专用池上,不能用业务池的 gdb。
	//
	// 之前只把 GET_LOCK 挪到了专用连接、AutoMigrate 仍走业务池 —— 修了一半,
	// 而"锁不会超时"恰恰掩盖了"DDL 会超时"这个更严重的问题。
	migGorm, err := gorm.Open(mysql.New(mysql.Config{Conn: migDB}), &gorm.Config{
		// 迁移不需要预编译语句缓存,而且 DDL 走 PrepareStmt 在部分 MySQL
		// 版本上有兼容问题。
		PrepareStmt: false,
		Logger:      gdb.Logger,
	})
	if err != nil {
		return fmt.Errorf("qianye: 初始化迁移专用会话失败: %w", err)
	}
	if err := migGorm.WithContext(ctx).AutoMigrate(models...); err != nil {
		return fmt.Errorf("qianye: 扩展库自动迁移失败: %w", err)
	}
	common.SysLog(fmt.Sprintf("qianye: 扩展库迁移完成,共 %d 张表", len(models)))
	return nil
}

// ───────────────────────────── 缺表自检 ─────────────────────────────

// schemaCheckTimeout 是自检那一条 information_schema 查询的预算。
// 它读的是元数据、不扫业务表,15 秒足够;设上界只是不让启动卡在一条挂住的查询上。
const schemaCheckTimeout = 15 * time.Second

// tableLister 读出扩展库当前已有的表名。
//
// 抽成变量只有一个目的:让缺表自检的回归测试能驱动完整的 Migrate 调用链
// (包括从节点、auto_migrate=false 这两条早退分支)而不需要一个真实的 MySQL。
// 生产路径永远是 queryTableNames。
var tableLister = queryTableNames

func queryTableNames(gdb *gorm.DB) ([]string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), schemaCheckTimeout)
	defer cancel()
	var names []string
	// 用 Pluck 而不是 Raw+Scan 到结构体:information_schema 的列名在不同 MySQL
	// 版本里大小写不一致(TABLE_NAME / table_name),按列名映射会静默扫不出东西,
	// 那正好会伪装成"所有表都缺失"。Pluck 按列序号取值,不受此影响。
	if err := gdb.WithContext(ctx).
		Table("information_schema.tables").
		Where("table_schema = DATABASE()").
		Pluck("table_name", &names).Error; err != nil {
		return nil, err
	}
	return names, nil
}

// 为什么必须有缺表自检:auto_migrate=false 是被明确支持的部署方式(由 DBA 手工建表),
// 从节点则根本不执行迁移。这两种部署下漏建一张表,启动日志一切正常,而划转创建、
// 额度预览、可用率看板会在第一个请求上全线 500 —— 故障现场与根因之间隔着
// 一整个部署周期。auto_migrate=true 时这一步同样要跑:那是对刚跑完的迁移的验证,
// AutoMigrate 返回 nil 不等于表都建出来了(比如模型被 allTables() 漏登记)。
//
// 无论走哪条分支,输出都必须逐个列出缺失的表名。运维拿到"有表缺失"这四个字
// 什么也做不了,他需要知道建哪几张。

// checkTables 核对 models 对应的表是否真的存在。
//
// 第二个返回值是本次自检"有没有跑成功",绝不能与"表齐全"混为一谈:
// information_schema 读不出来是**不知道**,不是"确认缺表"。
// db.Init 刚刚 Ping 成功过,此刻读不出元数据更像瞬时抖动 —— 据此把扩展打成
// 缺表降级态,只会让一次抖动关掉全部功能。
func checkTables(gdb *gorm.DB, models []any) (missing []string, checked bool) {
	existing, err := tableLister(gdb)
	if err != nil {
		MarkFailure(err)
		common.SysError("qianye: 扩展库缺表自检未能执行(information_schema 读取失败),已跳过本次自检: " + err.Error())
		return nil, false
	}
	return missingTables(gdb, models, existing), true
}

// verifyTables 是"本节点刚亲自跑完 AutoMigrate"那一条路径上的核对。
//
// 只有这条路径把缺表判成 error(进而由 bootstrap 冒泡到 FatalLog):AutoMigrate
// 刚刚返回 nil 却还缺表,是本节点自己的自相矛盾,不是别人的中间态,也不会因为
// 多等一会儿而自愈。其余三条分支见 noteMissingTables。
func verifyTables(gdb *gorm.DB, models []any) error {
	missing, checked := checkTables(gdb, models)
	if !checked {
		return nil
	}
	if len(missing) == 0 {
		schemaMissing.Store(nil)
		common.SysLog(fmt.Sprintf("qianye: 扩展库缺表自检通过,共核对 %d 张表", len(models)))
		return nil
	}
	missingList := missing
	schemaMissing.Store(&missingList)
	return fmt.Errorf("qianye: 扩展库缺少 %d 张表: %s —— "+
		"本节点刚执行完自动迁移仍然缺表,请检查这些模型是否登记进了 allTables(),"+
		"以及 DDL 是否真的在 database.dsn 指向的库上生效",
		len(missing), strings.Join(missing, ", "))
}

// noteMissingTables 是"本节点无权建表"那三条分支上的核对。
//
// 它没有返回值,因为这条路径上不存在"阻断启动"这个选项(见 Migrate 的契约)。
// 确认缺表的后果是:置位降级态 + 一条点名到表的错误日志 + 交给
// StartSchemaRecheck 周期性复查。
func noteMissingTables(gdb *gorm.DB, models []any, reason error) {
	missing, checked := checkTables(gdb, models)
	if !checked {
		return
	}
	if len(missing) == 0 {
		schemaMissing.Store(nil)
		common.SysLog(fmt.Sprintf("qianye: 扩展库缺表自检通过,共核对 %d 张表", len(models)))
		return
	}
	missingList := missing
	schemaMissing.Store(&missingList)
	common.SysError(fmt.Sprintf(
		"qianye: 扩展库缺少 %d 张表: %s —— %s,无法自行建表,因此不阻断主程序启动。"+
			"扩展进入 schema 降级态,每 %s 复查一次,表被建出来后自动解除;"+
			"若这不是滚动升级的中间态,请让主节点完成迁移或让 DBA 按上列表名建表",
		len(missing), strings.Join(missing, ", "), reason.Error(), schemaRecheckInterval))
}

// ─────────────────────── schema 降级态与后台复查 ───────────────────────

// schemaMissing 保存最近一次**成功执行**的自检确认缺失的表名;nil 表示 schema 完整。
// 自检自身失败时刻意保持原值不动 —— 那次自检什么都没证明。
var schemaMissing atomic.Pointer[[]string]

// schemaRecheckInterval 是降级态下的复查周期。
// 声明成 var 只为让回归测试能把它调小,生产路径永远是这个默认值。
var schemaRecheckInterval = time.Minute

var schemaRecheckOnce sync.Once

// SchemaIncomplete 表示扩展库当前处于"确认缺表"的降级态。
func SchemaIncomplete() bool { return schemaMissing.Load() != nil }

// MissingTables 返回确认缺失的表名副本(schema 完整时为 nil)。
func MissingTables() []string {
	p := schemaMissing.Load()
	if p == nil {
		return nil
	}
	return append([]string(nil), *p...)
}

// StartSchemaRecheck 在降级态下起一个后台协程周期性复查表清单。
//
// 它是"缺表不再杀进程"之后,缺表仍然能被发现的那一半:启动时那一条日志会被
// 后续日志滚走,而这个循环每分钟点名一次缺哪张表,直到表真的出现。
// 主节点跑完迁移(或 DBA 补完 DDL)后降级态自动解除,不需要重启本节点。
//
// schema 完整时不起协程,因此正常部署下这里零开销。
func StartSchemaRecheck(models ...any) {
	if len(models) == 0 || !SchemaIncomplete() {
		return
	}
	schemaRecheckOnce.Do(func() {
		gopool.Go(func() {
			ticker := time.NewTicker(schemaRecheckInterval)
			defer ticker.Stop()
			for range ticker.C {
				if recheckSchema(models) {
					return
				}
			}
		})
	})
}

// recheckSchema 复查一次,返回 schema 是否已经完整(完整则调用方停止轮询)。
func recheckSchema(models []any) bool {
	gdb := Get()
	if gdb == nil {
		return false
	}
	missing, checked := checkTables(gdb, models)
	if !checked {
		return false
	}
	if len(missing) == 0 {
		schemaMissing.Store(nil)
		common.SysLog("qianye: 扩展库缺失的表已全部建出,schema 降级态解除")
		return true
	}
	missingList := missing
	schemaMissing.Store(&missingList)
	common.SysError(fmt.Sprintf("qianye: 扩展库仍缺少 %d 张表: %s —— 依赖这些表的扩展功能持续不可用",
		len(missing), strings.Join(missing, ", ")))
	return false
}

// missingTables 返回 models 中在 existing 里找不到的表名,已排序。
//
// 表名按精确匹配比对:GORM 生成的表名一律小写,而 MySQL 在 lower_case_table_names=0
// (Linux 默认)下表名是大小写敏感的。放宽成忽略大小写会让 "QY_Foo" 通过自检,
// 而后续所有查询都会因为找不到 "qy_foo" 而失败 —— 那正是自检要挡住的场景。
func missingTables(gdb *gorm.DB, models []any, existing []string) []string {
	have := make(map[string]struct{}, len(existing))
	for _, name := range existing {
		have[name] = struct{}{}
	}
	missing := make([]string, 0, len(models))
	for _, m := range models {
		name := tableNameOf(gdb, m)
		if name == "" {
			// 连表名都解析不出来,说明模型定义本身有问题,同样必须喊出来,
			// 绝不能因为"没法比对"就当作通过。
			missing = append(missing, fmt.Sprintf("%T(表名解析失败)", m))
			continue
		}
		if _, ok := have[name]; !ok {
			missing = append(missing, name)
		}
	}
	sort.Strings(missing)
	return missing
}

// tableNameOf 解析模型对应的表名,与 AutoMigrate 用的是同一套 GORM schema 解析,
// 因此自检核对的表名与迁移建出来的表名必然同源。
func tableNameOf(gdb *gorm.DB, model any) string {
	stmt := &gorm.Statement{DB: gdb}
	if err := stmt.Parse(model); err != nil {
		return ""
	}
	return stmt.Table
}

// TableCount 统计扩展库中 qy_ 前缀的表数量,供健康面板核对迁移结果。
func TableCount() int64 {
	gdb := Get()
	if gdb == nil {
		return 0
	}
	var n int64
	err := gdb.Raw(`SELECT COUNT(*) FROM information_schema.tables
		WHERE table_schema = DATABASE() AND table_name LIKE 'qy\_%'`).Scan(&n).Error
	if err != nil {
		MarkFailure(err)
		return 0
	}
	return n
}
