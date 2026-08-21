package model

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/driver/postgres"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/pkg/gormdialect"
)

// ddlRecorder 抓 GORM 实际发出去的每一条语句。
//
// 为什么不复用 migrationSQLRecorder:那个只认 "…TABLE" 开头的语句,而本守卫要
// 覆盖的空转 DDL 里有 CREATE INDEX / COMMENT ON 这些形态,漏掉它们就会得到一份
// "看起来全绿其实什么都没测到"的报告。
type ddlRecorder struct {
	mu         sync.Mutex
	statements []string
}

func (r *ddlRecorder) LogMode(logger.LogLevel) logger.Interface { return r }
func (r *ddlRecorder) Info(context.Context, string, ...any)     {}
func (r *ddlRecorder) Warn(context.Context, string, ...any)     {}
func (r *ddlRecorder) Error(context.Context, string, ...any)    {}

func (r *ddlRecorder) Trace(_ context.Context, _ time.Time, sql func() (string, int64), _ error) {
	statement, _ := sql()
	r.mu.Lock()
	r.statements = append(r.statements, statement)
	r.mu.Unlock()
}

func (r *ddlRecorder) reset() {
	r.mu.Lock()
	r.statements = nil
	r.mu.Unlock()
}

// ddlPrefixes 是"改变库结构"的语句前缀。只列 DDL:迁移里的 UPDATE/INSERT
// (auth_version 回填、Telegram 身份回填)本来就该在第二次运行时保持幂等地重发,
// 它们不是本守卫要抓的东西。
var ddlPrefixes = []string{
	"CREATE TABLE", "ALTER TABLE", "DROP TABLE", "RENAME TABLE",
	"CREATE INDEX", "CREATE UNIQUE INDEX", "DROP INDEX",
	"COMMENT ON",
}

func (r *ddlRecorder) schemaChanges() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	changes := make([]string, 0)
	for _, statement := range r.statements {
		normalized := strings.ToUpper(strings.TrimSpace(statement))
		for _, prefix := range ddlPrefixes {
			if strings.HasPrefix(normalized, prefix) {
				changes = append(changes, statement)
				break
			}
		}
	}
	return changes
}

// TestMigrateDBIsIdempotent 守卫主库迁移的幂等性:migrateDB 跑第二遍必须一条 DDL
// 都不发。
//
// 这条不变量是运维可见的 —— 不幂等意味着每次进程重启都对着生产库重放一批
// ALTER TABLE。在 PostgreSQL 上 ALTER COLUMN … TYPE 会重写整张表并取 ACCESS
// EXCLUSIVE 锁,logs 这种表上足以让站点在启动期间不可用。
//
// SQLite 永远跑;MySQL / PostgreSQL 需要 TEST_MYSQL_DSN / TEST_POSTGRES_DSN
// 指向一个**可丢弃**的库,没配就 SKIP。
func TestMigrateDBIsIdempotent(t *testing.T) {
	tests := []struct {
		name      string
		dbType    common.DatabaseType
		env       string
		dialector func(t *testing.T, dsn string) gorm.Dialector
	}{
		{
			name:   "sqlite",
			dbType: common.DatabaseTypeSQLite,
			dialector: func(t *testing.T, _ string) gorm.Dialector {
				// 必须与 chooseDB 用同一个 dialector,否则守卫测的不是生产路径。
				return gormdialect.OpenSQLite(filepath.Join(t.TempDir(), "migrate-idempotency.db"))
			},
		},
		{
			name:   "mysql",
			dbType: common.DatabaseTypeMySQL,
			env:    "TEST_MYSQL_DSN",
			dialector: func(_ *testing.T, dsn string) gorm.Dialector {
				// 必须与 chooseDB 用同一个 dialector,否则守卫测的不是生产路径。
				return gormdialect.OpenMySQL(dsn)
			},
		},
		{
			name:   "postgres",
			dbType: common.DatabaseTypePostgreSQL,
			env:    "TEST_POSTGRES_DSN",
			dialector: func(_ *testing.T, dsn string) gorm.Dialector {
				// 必须与 chooseDB 用同一个 dialector,否则守卫测的不是生产路径。
				return gormdialect.NewPostgres(postgres.Config{DSN: dsn, PreferSimpleProtocol: true})
			},
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			var dsn string
			if test.env != "" {
				dsn = strings.TrimSpace(os.Getenv(test.env))
				if dsn == "" {
					t.Skip(test.env + " is not configured")
				}
			}

			recorder := &ddlRecorder{}
			db, err := gorm.Open(test.dialector(t, dsn), &gorm.Config{Logger: recorder})
			require.NoError(t, err)
			if sqlDB, dbErr := db.DB(); dbErr == nil {
				t.Cleanup(func() { _ = sqlDB.Close() })
			}

			previousDB, previousType := DB, common.MainDatabaseType()
			t.Cleanup(func() {
				DB = previousDB
				common.SetMainDatabaseType(previousType)
				InitCol()
			})
			DB = db
			common.SetMainDatabaseType(test.dbType)
			InitCol()

			require.NoError(t, migrateDB(), "first migration must succeed")

			recorder.reset()
			require.NoError(t, migrateDB(), "second migration must succeed")
			assert.Empty(t, recorder.schemaChanges(),
				"a second migrateDB() must not emit any DDL on %s", test.name)
		})
	}
}
