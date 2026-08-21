package qianye

import (
	"context"
	"os"
	"strings"
	"testing"
	"time"

	"gorm.io/gorm"
	glogger "gorm.io/gorm/logger"

	qydb "github.com/QuantumNous/new-api/qianye/db"

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
// 判据只能在真库上跑(sqlite 验证不了方言归一化):
// 设 QY_TEST_MYSQL_MIGRATE_DSN / QY_TEST_PG_MIGRATE_DSN 指向**一次性**库即可,
// 不设就干净 SKIP。两种方言都必须过 —— PostgreSQL 是扩展库受支持的第二种部署,
// 而"空转 DDL"这个缺陷完全是方言归一化差异造成的,MySQL 干净不代表 PG 干净。
func TestExtensionAutoMigrateIsIdempotent(t *testing.T) {
	cases := []struct {
		name string
		env  string
	}{
		{name: "mysql", env: "QY_TEST_MYSQL_MIGRATE_DSN"},
		{name: "postgres", env: "QY_TEST_PG_MIGRATE_DSN"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			dsn := os.Getenv(tc.env)
			if dsn == "" {
				t.Skipf("未设置 %s,跳过(sqlite 上验证不了方言归一化)", tc.env)
			}

			// 必须走 db.DialectorFor —— 那是生产建句柄的唯一入口。
			// 直接 postgres.Open 会绕开列信息归一化装饰器,验证的就不是生产行为。
			dialector, err := qydb.DialectorFor(dsn)
			require.NoError(t, err)
			var stmts []string
			gdb, err := gorm.Open(dialector, &gorm.Config{Logger: migrateSQLRecorder{&stmts}})
			require.NoError(t, err)

			tables := allTables()
			require.NotEmpty(t, tables)
			require.NoError(t, gdb.AutoMigrate(tables...), "首次迁移必须成功")

			stmts = stmts[:0]
			require.NoError(t, gdb.AutoMigrate(tables...), "第二次迁移必须成功")

			// CREATE INDEX / CREATE UNIQUE INDEX 必须一起数进来。
			//
			// 判据原先只认 ALTER TABLE / CREATE TABLE / DROP,于是漏掉了一整类
			// 空转 DDL:PostgreSQL 上索引名是 schema 级唯一的,一个被**另一张表**
			// 占用的索引名会让 GORM 的 `CREATE INDEX IF NOT EXISTS` 既建不出索引、
			// 也不报错,每次启动再徒劳发一遍。实测这条洞让 qy_avail_bucket_hour
			// 缺三条索引(含唯一索引)、rollup 恒报 42P10 的真缺陷带着这条
			// 自称守着它的测试 PASS 了整整一轮 —— 索引 DDL 与上面那三类一样
			// 会取排他元数据锁、写进 binlog、淹掉真正的意外 DDL,没有理由被放行。
			ddlPrefixes := []string{
				"ALTER TABLE",
				"CREATE TABLE",
				"CREATE INDEX",
				"CREATE UNIQUE INDEX",
				"DROP ",
			}
			var ddl []string
			for _, s := range stmts {
				up := strings.ToUpper(strings.TrimSpace(s))
				for _, prefix := range ddlPrefixes {
					if strings.HasPrefix(up, prefix) {
						ddl = append(ddl, s)
						break
					}
				}
			}
			assert.Empty(t, ddl,
				"第二次 AutoMigrate 不允许发出任何 DDL;每一条都是每次重启都会重发的空转,"+
					"它们会在资金表上取排他元数据锁、写进 binlog,并把真正的意外 DDL 淹掉")
		})
	}
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
