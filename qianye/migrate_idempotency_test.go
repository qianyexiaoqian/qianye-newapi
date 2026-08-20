package qianye

import (
	"context"
	"os"
	"strings"
	"testing"
	"time"

	"gorm.io/driver/mysql"
	"gorm.io/gorm"
	glogger "gorm.io/gorm/logger"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// migrate_idempotency_test.go —— 扩展库的 AutoMigrate 必须是幂等的。
//
// AGENTS.md 里有一条明确禁令:不要用 GORM 的布尔 default 标签,因为 MySQL 与
// PostgreSQL 对布尔默认值的归一化不同,AutoMigrate 会在每次重启时反复
// ALTER TABLE。这条禁令原先只写在文档里,没有任何东西执行它 —— 实测扩展库每次
// 启动重发 **65 条** `ALTER TABLE ... MODIFY COLUMN`,横跨 25 张资金表:
//
//	35 条来自 `gorm:"not null;default:false"`(GORM 发 `boolean ... DEFAULT false`,
//	   库里实际是 `tinyint(1) ... DEFAULT 0`,永远比不相等)
//	30 条来自 `type:decimal(P,S);not null;default:0`(GORM 发 `DEFAULT '0'`,
//	   库里实际是 `DEFAULT 0.00000000`,同样永远比不相等)
//
// 代价不是性能(MySQL 8 走 INSTANT,25 万行约 12ms),而是三件事:每次启动在 25 张
// 资金表上各取一次排他元数据锁(本机 lock_wait_timeout 是 31536000 秒,遇到长事务
// 会挂住并把排队在后面的查询一起挂住)、每次启动往 binlog 写 65 个空转 DDL、
// 以及最实际的一条 —— "扩展库 AutoMigrate 是空操作"这个判断被噪音淹没,
// 真混进一条意外 DDL(比如有人改了某个资金列的类型或宽度)无法与噪音区分。
//
// 讽刺的是仓内唯一一处显式援引这条禁令的规避注释(apiaddr/model.go)自己写的是
// `default:false`,而它恰恰在那 65 条里 —— 规避手法本身是无效的。
//
// 判据只能在真 MySQL 上跑(扩展库只支持 MySQL,见 qianye/db/db.go):
// 设 QY_TEST_MYSQL_MIGRATE_DSN 指向一个**一次性**库即可。不设就干净 SKIP。
func TestExtensionAutoMigrateIsIdempotent(t *testing.T) {
	dsn := os.Getenv("QY_TEST_MYSQL_MIGRATE_DSN")
	if dsn == "" {
		t.Skip("未设置 QY_TEST_MYSQL_MIGRATE_DSN,跳过(扩展库只支持 MySQL,无法在 sqlite 上验证方言归一化)")
	}

	var stmts []string
	gdb, err := gorm.Open(mysql.Open(dsn), &gorm.Config{Logger: migrateSQLRecorder{&stmts}})
	require.NoError(t, err)

	tables := allTables()
	require.NotEmpty(t, tables)
	require.NoError(t, gdb.AutoMigrate(tables...), "首次迁移必须成功")

	stmts = stmts[:0]
	require.NoError(t, gdb.AutoMigrate(tables...), "第二次迁移必须成功")

	var ddl []string
	for _, s := range stmts {
		up := strings.ToUpper(strings.TrimSpace(s))
		if strings.HasPrefix(up, "ALTER TABLE") ||
			strings.HasPrefix(up, "CREATE TABLE") ||
			strings.HasPrefix(up, "DROP ") {
			ddl = append(ddl, s)
		}
	}
	assert.Empty(t, ddl,
		"第二次 AutoMigrate 不允许发出任何 DDL;每一条都是每次重启都会重发的空转,"+
			"它们会在资金表上取排他元数据锁、写进 binlog,并把真正的意外 DDL 淹掉")
}

type migrateSQLRecorder struct{ stmts *[]string }

func (r migrateSQLRecorder) LogMode(glogger.LogLevel) glogger.Interface  { return r }
func (migrateSQLRecorder) Info(context.Context, string, ...interface{})  {}
func (migrateSQLRecorder) Warn(context.Context, string, ...interface{})  {}
func (migrateSQLRecorder) Error(context.Context, string, ...interface{}) {}
func (r migrateSQLRecorder) Trace(_ context.Context, _ time.Time, fc func() (string, int64), _ error) {
	sql, _ := fc()
	*r.stmts = append(*r.stmts, sql)
}
