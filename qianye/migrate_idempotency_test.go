package qianye

import (
	"context"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"gorm.io/gorm"
	glogger "gorm.io/gorm/logger"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/qianye/config"
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

			// 台账表必须一起数进来。它们从 allTables() 搬进 allLogTables() 之后,
			// 只跑前者就等于让 qy_violation_ai_review 悄悄掉出这条防线 ——
			// 而它恰恰是最新、字段最多、最可能带一条空转 DDL 的那张表。
			// 合成一份跑正好也是**没配 log_database 的部署**的真实形态。
			tables := append(allTables(), allLogTables()...)
			require.NotEmpty(t, tables)
			require.NotEmpty(t, allLogTables(), "台账表清单不该是空的")
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

// TestLogDatabaseMigratesOnItsOwnHandle 是台账库分家之后**唯一**能证明它真的
// 分开了的测试,只能在真库上跑。
//
// 它守的三条,每一条坏了都不会有任何报错:
//
//	台账表建在台账库里    句柄接错的话它们会建回主库,而管理端从台账库读 → 永远空表
//	主库表不会跑到台账库  两份清单串了会让资金表在台账库里也建一份,写入落哪边看运气
//	第二次迁移零 DDL      两个库各有一把迁移锁,锁名撞了会让其中一个每次启动都退化
//	                     成"另一节点正在迁移"的降级态,而降级态下它一张表都建不出来
//
// 需要两个**一次性**库的 DSN:
//
//	QY_TEST_MYSQL_MIGRATE_DSN + QY_TEST_MYSQL_LOG_MIGRATE_DSN
//	QY_TEST_PG_MIGRATE_DSN    + QY_TEST_PG_LOG_MIGRATE_DSN
//
// 缺任何一个就干净 SKIP —— sqlite 上验证不了这件事(扩展库压根不支持它)。
func TestLogDatabaseMigratesOnItsOwnHandle(t *testing.T) {
	cases := []struct{ name, mainEnv, logEnv string }{
		{"mysql", "QY_TEST_MYSQL_MIGRATE_DSN", "QY_TEST_MYSQL_LOG_MIGRATE_DSN"},
		{"postgres", "QY_TEST_PG_MIGRATE_DSN", "QY_TEST_PG_LOG_MIGRATE_DSN"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			mainDSN, logDSN := os.Getenv(tc.mainEnv), os.Getenv(tc.logEnv)
			if mainDSN == "" || logDSN == "" {
				t.Skipf("未同时设置 %s 与 %s,跳过", tc.mainEnv, tc.logEnv)
			}

			// 走真实的配置加载路径,不是手搓一个 Config:validateLogDatabase
			// (两个 dsn 不能相同、方言必须受支持)也是这条接线的一部分,
			// 绕过它就等于测了一条生产里不存在的路径。
			useTwoDatabaseConfig(t, mainDSN, logDSN)

			prevMaster := common.IsMasterNode
			common.IsMasterNode = true
			t.Cleanup(func() { common.IsMasterNode = prevMaster })

			require.NoError(t, qydb.Init(config.Get().Database))
			require.NoError(t, qydb.InitLog(config.Get().LogDatabase))
			t.Cleanup(func() { _ = qydb.Close() })
			require.True(t, qydb.LogSeparate(), "配了 dsn 就必须分家")

			mainTables, logTables := allTables(), allLogTables()
			require.NotEmpty(t, logTables)

			// 先把台账表从主库里删干净。
			//
			// 不是洁癖:同一个一次性库很可能刚被 TestExtensionAutoMigrateIsIdempotent
			// 用过,而那条测试**故意**把两份清单合起来迁(它验的是"没配
			// log_database 的部署"),于是主库里已经有一张 qy_violation_ai_review。
			// 不删的话下面那条"不该出现在主库里"的断言测的是历史残留,
			// 而不是这一轮 Migrate 的行为 —— 它会红,而根因与被测代码无关。
			for _, model := range logTables {
				require.NoError(t, qydb.Get().Migrator().DropTable(model))
			}

			require.NoError(t, qydb.Migrate(mainTables...))
			require.NoError(t, qydb.MigrateLog(logTables...))

			// ① 台账表在台账库里,不在主库里。
			for _, model := range logTables {
				name := tableNameFor(t, qydb.LogHandle(), model)
				assert.Truef(t, qydb.LogHandle().Migrator().HasTable(name),
					"%s 必须建在台账库里", name)
				assert.Falsef(t, qydb.Get().Migrator().HasTable(name),
					"%s 不该出现在主库里 —— 出现了就说明两份清单串了,"+
						"写入与读取会落到不同的库上", name)
			}

			// ② 主库表不会跑到台账库。抽一张就够:串清单是整体行为,不会只串一张。
			mainName := tableNameFor(t, qydb.Get(), mainTables[0])
			assert.True(t, qydb.Get().Migrator().HasTable(mainName))
			assert.Falsef(t, qydb.LogHandle().Migrator().HasTable(mainName),
				"%s 不该出现在台账库里", mainName)

			// ③ 第二次迁移零 DDL。锁名撞了的话这一步会静默退化成降级分支,
			//    表面上也"没有 DDL" —— 所以上面 ① 的建表断言必须排在它前面。
			require.NoError(t, qydb.MigrateLog(logTables...), "第二次台账库迁移必须成功")
			assert.False(t, qydb.SchemaIncomplete(),
				"迁移跑完还处于缺表降级态,说明台账库这一轮根本没建成表:%v",
				qydb.MissingTables())
		})
	}
}

// useTwoDatabaseConfig 写一份带 log_database 段的临时配置并加载它。
func useTwoDatabaseConfig(t *testing.T, mainDSN, logDSN string) {
	t.Helper()
	yaml := "enabled: true\n" +
		"database:\n  dsn: " + strconv.Quote(mainDSN) + "\n" +
		"log_database:\n  dsn: " + strconv.Quote(logDSN) + "\n"
	path := filepath.Join(t.TempDir(), "qianye.yaml")
	require.NoError(t, os.WriteFile(path, []byte(yaml), 0o600))
	t.Setenv(config.EnvConfigPath, path)
	require.NoError(t, config.Load())
	require.True(t, config.Get().LogDatabaseSeparate())
}

func tableNameFor(t *testing.T, gdb *gorm.DB, model any) string {
	t.Helper()
	stmt := &gorm.Statement{DB: gdb}
	require.NoError(t, stmt.Parse(model))
	return stmt.Table
}
