package db

import (
	"fmt"
	"hash/fnv"
	"strings"

	"gorm.io/driver/mysql"
	"gorm.io/driver/postgres"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// Dialect 是扩展库当前使用的 SQL 方言。
//
// # 为什么扩展库需要方言层
//
// 扩展库长期只支持 MySQL,而 GORM 那一层的代码其实早就是方言中立的 ——
// 全部扩展模块的 Go 测试本来就跑在 glebarez/sqlite 上。真正 MySQL-only 的面
// 只有四类:驱动选择、迁移互斥(MySQL GET_LOCK)、少量手写 SQL(UNIX_TIMESTAMP、
// ON DUPLICATE KEY UPDATE、DELETE ... LIMIT、information_schema 里的
// DATABASE()),以及几个 MySQL 专有的列类型标签(blob / mediumblob / varbinary)。
//
// 本文件把这四类收进一处,让 PostgreSQL 成为受支持的部署选项。
//
// # SQLite 的定位
//
// SQLite 在这里是**测试方言**,不是受支持的部署方言:扩展库承载资金
// (佣金账本、两阶段资金单、提现、抽奖出款),这些路径依赖 SELECT ... FOR UPDATE
// 的行锁语义,而 SQLite 没有行锁 —— LockForUpdate 只能退化成空操作。
// 因此 DialectorFor 不接受 SQLite DSN(见 config.validateDatabase),
// 但所有方言分支都必须给出正确的 SQLite 渲染,否则现存的单元测试会跑在
// 一条与生产不同的代码路径上,那比不支持更危险。
type Dialect string

const (
	DialectMySQL    Dialect = "mysql"
	DialectPostgres Dialect = "postgres"
	DialectSQLite   Dialect = "sqlite"
)

// DetectDialect 从 DSN 判断方言。
//
// MySQL 的 DSN 没有 scheme 前缀(user:pass@tcp(host:port)/db),所以它是兜底;
// PostgreSQL 认两种写法:URL 形式(postgres:// / postgresql://)与
// libpq 关键字形式(host=... user=... dbname=...)。关键字形式必须认,
// 因为 gorm.io/driver/postgres 的文档示例用的就是它。
func DetectDialect(dsn string) Dialect {
	s := strings.TrimSpace(dsn)
	lower := strings.ToLower(s)
	switch {
	case strings.HasPrefix(lower, "postgres://"), strings.HasPrefix(lower, "postgresql://"):
		return DialectPostgres
	case strings.HasPrefix(lower, "sqlite:"), strings.HasPrefix(lower, "file:"),
		lower == "local", strings.HasPrefix(lower, "local "):
		return DialectSQLite
	case isLibpqKeywordDSN(lower):
		return DialectPostgres
	default:
		return DialectMySQL
	}
}

// isLibpqKeywordDSN 判断 DSN 是否为 libpq 的 "key=value 空格分隔" 形式。
//
// 判据取 host= / dbname= / user= 这三个键之一出现在**词首**。只看 "包含 =" 是不够的:
// MySQL DSN 的查询串里也全是 =(charset=utf8mb4&parseTime=true),那会把 MySQL
// 误判成 PostgreSQL,而报出来的错是一句无从下手的 "cannot parse dsn"。
func isLibpqKeywordDSN(lower string) bool {
	for _, tok := range strings.Fields(lower) {
		switch {
		case strings.HasPrefix(tok, "host="),
			strings.HasPrefix(tok, "dbname="),
			strings.HasPrefix(tok, "user="),
			strings.HasPrefix(tok, "port="):
			return true
		}
	}
	return false
}

// DialectorFor 按 DSN 前缀分派驱动。
//
// 刻意不接 SQLite:见 Dialect 的文档注释。config 层会先拒掉 SQLite DSN,
// 这里再兜一次,免得将来有人绕过 config 直接调 Init。
//
// 导出是给迁移幂等性回归用的:那条判据必须走**生产同一条**建句柄路径,
// 否则它验证的是一个裸 postgres.Open,而生产用的是带列信息归一化的装饰器,
// 两者的 AutoMigrate 行为完全不同(见 pg_migrator.go)。
func DialectorFor(dsn string) (gorm.Dialector, error) {
	switch d := DetectDialect(dsn); d {
	case DialectPostgres:
		return normalizedPGDialector{postgres.Open(dsn)}, nil
	case DialectMySQL:
		return mysql.Open(dsn), nil
	default:
		return nil, fmt.Errorf("qianye: 扩展库不支持方言 %q(资金路径依赖行锁,SQLite 无法提供)", d)
	}
}

// DialectOf 从活的 GORM 句柄读方言。
//
// 优先从句柄读而不是从配置读,是因为全部扩展模块的单元测试都往 handle 里塞
// 一个 sqlite 句柄:按配置判断的话,测试会把 MySQL 分支的 SQL 发给 SQLite,
// 于是"方言分支写错了"这类缺陷在测试里永远看不见。
func DialectOf(tx *gorm.DB) Dialect {
	if tx == nil || tx.Dialector == nil {
		return DialectMySQL
	}
	switch tx.Dialector.Name() {
	case "postgres":
		return DialectPostgres
	case "sqlite", "sqlite3":
		return DialectSQLite
	default:
		return DialectMySQL
	}
}

// Dialect 返回当前扩展库句柄的方言。未初始化时回落到 MySQL(默认与主推)。
func CurrentDialect() Dialect { return DialectOf(Get()) }

// NowEpochSQL 返回"库端当前 Unix 秒"的方言表达式。
//
// 库端取时间是租约模块的硬要求:节点之间的时钟漂移会让基于 Go 端时间的
// 租约失效判断出错(见 service/lease)。三家的写法与取整方向必须逐一对齐:
//
//   - MySQL:UNIX_TIMESTAMP() 取语句开始时间,向下截断到秒。
//   - PostgreSQL:用 clock_timestamp() 而不是 now()/CURRENT_TIMESTAMP ——
//     后两者是**事务开始**时间,在一个多语句事务里恒定不变,与 MySQL 的
//     语句级语义不同;而 ::bigint 是四舍五入,会把 x.7 秒进位成 x+1 秒,
//     所以必须先 FLOOR 才能与 MySQL 的截断对齐(租约到期判定差 1 秒就是
//     一次错误的接管)。
//   - SQLite:strftime('%s') 本身就是截断到秒的 UTC。
func NowEpochSQL(tx *gorm.DB) string {
	switch DialectOf(tx) {
	case DialectPostgres:
		return "FLOOR(EXTRACT(EPOCH FROM clock_timestamp()))::bigint"
	case DialectSQLite:
		return "CAST(strftime('%s','now') AS INTEGER)"
	default:
		return "UNIX_TIMESTAMP()"
	}
}

// UpsertHead 渲染 "INSERT ... VALUES ..." 之后的冲突子句头部,
// 让调用方可以用同一份赋值列表覆盖三种方言。
//
// MySQL 的 ON DUPLICATE KEY UPDATE 不接受冲突列清单(它对**任意**唯一键生效),
// PostgreSQL 与 SQLite 则必须显式给出冲突目标。调用方因此只需要写一次赋值列表。
//
// # 赋值列表里的列引用必须带表名限定
//
// 三家对"SET 右侧的裸列名指谁"的解析并不一致:PostgreSQL 会因为目标表与
// excluded 伪表同名而直接报 `column reference "x" is ambiguous`,语句整条失败。
// 带表名限定(`tbl.col` = 冲突前的旧值)在 MySQL / PostgreSQL / SQLite 上
// 都合法且语义相同,是唯一的三家交集写法。
func UpsertHead(tx *gorm.DB, table string, conflictCols ...string) string {
	if DialectOf(tx) == DialectMySQL {
		return "ON DUPLICATE KEY UPDATE"
	}
	return "ON CONFLICT (" + strings.Join(conflictCols, ", ") + ") DO UPDATE SET"
}

// LockForUpdate 给查询加行锁。
//
// SQLite 分支显式跳过,与 model.lockForUpdate 对主库 SQLite 的处理同口径。
// 实测:测试用的 glebarez/sqlite 自己就会把 clause.Locking 整条丢掉(渲染出来
// 的 SQL 里没有 FOR UPDATE),所以这一跳在当前依赖下是冗余的 —— 保留它是因为
// 那是驱动的实现细节而不是契约,换成 mattn/go-sqlite3 就会变成语法错误,
// 而这条路径上的调用方大多把错误当成"这一行不存在"处理。
// 也正因为如此,单测无法用"去掉 SQLite 分支"这个变异把它杀掉;
// 能被杀掉的是另一个方向:LockForUpdate 对 MySQL/PostgreSQL 必须真的加上
// FOR UPDATE(见 TestLockForUpdateSkipsOnlySQLite)。
//
// 不能复用 model.lockForUpdate:那个函数判断的是**主库**类型,主库是 SQLite
// 而扩展库是 MySQL 时它会静默不加锁。
func LockForUpdate(tx *gorm.DB) *gorm.DB {
	if DialectOf(tx) == DialectSQLite {
		return tx
	}
	return tx.Clauses(clause.Locking{Strength: "UPDATE"})
}

// IsDuplicateKey 判断错误是否为唯一索引冲突。
//
// 三家的报错文本完全不同,漏掉任何一家的后果都不是"报错",而是**静默改变
// 控制流**:扩展里所有把"撞唯一键"当成幂等命中的地方(租约首次插入、
// 两阶段资金单落单、提现建单)都会把冲突当成真错误往上抛,于是幂等重试
// 变成了失败。
//
//	MySQL      Error 1062 (23000): Duplicate entry '...' for key '...'
//	PostgreSQL ERROR: duplicate key value violates unique constraint "..." (SQLSTATE 23505)
//	SQLite     UNIQUE constraint failed: tbl.col  /  constraint failed
//
// 用文本匹配而不是驱动错误类型断言:三个驱动的错误类型互不兼容,类型断言
// 需要把三个驱动都编译进来(SQLite 不进生产依赖),而这些文本是各自的稳定契约。
func IsDuplicateKey(err error) bool {
	if err == nil {
		return false
	}
	msg := strings.ToLower(err.Error())
	for _, s := range []string{
		"duplicate entry",          // MySQL
		"error 1062",               // MySQL
		"duplicate key",            // PostgreSQL: duplicate key value violates unique constraint
		"sqlstate 23505",           // PostgreSQL
		"23505",                    // PostgreSQL(部分驱动只带裸 SQLSTATE)
		"unique constraint failed", // SQLite
	} {
		if strings.Contains(msg, s) {
			return true
		}
	}
	return false
}

// advisoryLockKey 把迁移锁名折成 PostgreSQL 咨询锁需要的 int64 键。
//
// PostgreSQL 的咨询锁只认整型键,没有 MySQL GET_LOCK 的字符串键。
// 用 FNV-1a 64 而不是随手取个常量:锁名将来可能新增(比如按模块分锁),
// 一个确定性的哈希函数让"锁名 → 键"这一步不需要维护一张手工映射表。
//
// 哈希碰撞的后果是两个不同的锁互斥,不是丢锁 —— 方向安全。
func advisoryLockKey(name string) int64 {
	h := fnv.New64a()
	_, _ = h.Write([]byte(name))
	return int64(h.Sum64()) // #nosec G115 —— 位模式重解释,咨询锁键只要求确定性
}
