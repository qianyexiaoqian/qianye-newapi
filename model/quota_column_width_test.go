package model

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/driver/postgres"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/pkg/gormdialect"
)

// TestQuotaColumnsAre64BitOnEveryDialect 钉住 common.MaxQuota 那条推导的前提。
//
// # 它替换掉的是一句查得出来是假的话
//
// common/quota_math.go 原先把 MaxQuota 定成 math.MaxInt32,理由写的是「额度列
// (user/token/log)在数据库里是 32 位整数」,AGENTS.md 也照抄了这一句。三个方言
// 上实测都不是:MySQL 与 PostgreSQL 建出 bigint,SQLite 建出 INTEGER(8 字节)。
//
// 成因是 `gorm:"type:int"` 这个标签**不是**在指定 SQL 类型:GORM 把它映射到
// 通用的 schema.Int 种类(gorm/schema/field.go 的 `case Bool, Int, Uint, ...`),
// 具体 SQL 类型仍由方言按 field.Size 推,而 Go 的 int 在 64 位构建上 Size=64。
// 也就是说那个标签是个空操作,三个方言一律建 64 位列。
//
// 这条测试用两种互补的方式证同一件事,缺一不可:
//
//   - 列类型名:直接读回 AutoMigrate 建出来的东西,这是"标签没生效"的直接证据;
//   - 往返写读 common.MaxQuota:类型名是元数据,而这一步是行为 —— 列若真是
//     32 位,写 8796093022208 要么报错要么截断,读回来绝不会等于原值。
//
// SQLite 永远跑;MySQL / PostgreSQL 需要 TEST_MYSQL_DSN / TEST_POSTGRES_DSN
// 指向一个**可丢弃**的库,没配就 SKIP。
func TestQuotaColumnsAre64BitOnEveryDialect(t *testing.T) {
	// 三个方言各自的 64 位整数类型名。列在这里而不是"只要不是 int"是刻意的:
	// 一份白名单会在有人把列改成 mediumint 时报错,而黑名单不会。
	wide := map[common.DatabaseType]map[string]bool{
		common.DatabaseTypeSQLite:     {"integer": true},
		common.DatabaseTypeMySQL:      {"bigint": true},
		common.DatabaseTypePostgreSQL: {"int8": true, "bigint": true},
	}

	for _, tc := range []struct {
		name      string
		dbType    common.DatabaseType
		env       string
		dialector func(t *testing.T, dsn string) gorm.Dialector
	}{
		{
			name:   "sqlite",
			dbType: common.DatabaseTypeSQLite,
			dialector: func(t *testing.T, _ string) gorm.Dialector {
				// 必须与 chooseDB 用同一个 dialector,否则测的不是生产路径。
				return gormdialect.OpenSQLite(filepath.Join(t.TempDir(), "quota-width.db"))
			},
		},
		{
			name:   "mysql",
			dbType: common.DatabaseTypeMySQL,
			env:    "TEST_MYSQL_DSN",
			dialector: func(_ *testing.T, dsn string) gorm.Dialector {
				return gormdialect.OpenMySQL(dsn)
			},
		},
		{
			name:   "postgres",
			dbType: common.DatabaseTypePostgreSQL,
			env:    "TEST_POSTGRES_DSN",
			dialector: func(_ *testing.T, dsn string) gorm.Dialector {
				return gormdialect.NewPostgres(postgres.Config{DSN: dsn, PreferSimpleProtocol: true})
			},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var dsn string
			if tc.env != "" {
				dsn = strings.TrimSpace(os.Getenv(tc.env))
				if dsn == "" {
					t.Skip(tc.env + " is not configured")
				}
			}

			db, err := gorm.Open(tc.dialector(t, dsn), &gorm.Config{Logger: logger.Discard})
			require.NoError(t, err)
			if sqlDB, dbErr := db.DB(); dbErr == nil {
				t.Cleanup(func() { _ = sqlDB.Close() })
			}

			prevDB, prevLog := DB, LOG_DB
			prevMain, prevLogType := common.MainDatabaseType(), common.LogDatabaseType()
			t.Cleanup(func() {
				DB, LOG_DB = prevDB, prevLog
				common.SetMainDatabaseType(prevMain)
				common.SetLogDatabaseType(prevLogType)
				InitCol()
			})
			DB, LOG_DB = db, db
			common.SetMainDatabaseType(tc.dbType)
			common.SetLogDatabaseType(tc.dbType)
			InitCol()

			// 全新空库上跑一次真正的迁移 —— 存量库可能被历史 ALTER 改过,
			// 只有新建出来的列才能证明"模型标签今天建出来的是什么"。
			require.NoError(t, migrateDB())
			require.NoError(t, migrateLOGDB())

			for _, col := range []struct{ table, column string }{
				{"users", "quota"},
				{"users", "used_quota"},
				{"tokens", "remain_quota"},
				{"tokens", "used_quota"},
				{"logs", "quota"},
			} {
				types, err := db.Migrator().ColumnTypes(col.table)
				require.NoError(t, err)
				found := false
				for _, ct := range types {
					if ct.Name() != col.column {
						continue
					}
					found = true
					got := strings.ToLower(ct.DatabaseTypeName())
					assert.True(t, wide[tc.dbType][got],
						"%s.%s 建出来的是 %s —— MaxQuota 的推导以「额度列是 64 位」为前提,"+
							"这一列不是的话那条推导整个不成立", col.table, col.column, got)
				}
				assert.True(t, found, "%s.%s 必须存在", col.table, col.column)
			}

			// 行为侧:把上界原样写进去再读回来。列若真是 32 位,这一步过不去。
			//
			// 前后各清一次,而且用 t.Cleanup 兜住失败退出。
			// MySQL / PostgreSQL 的 DSN 指向的是一个**可复用**的库(没有任何脚本
			// 负责在每次运行前重建它),不清理的表现是:第二遍跑同一条用例必然红,
			// 而红在主键重复上、报错却落在那句"列若真是 32 位,这一步过不去"的断言
			// 附近 —— 这是最容易让人以为"守卫坏了,关掉算了"的形状。
			// 同族的 model/ability_case_match_test.go 与 schema_migration_idempotency_test.go
			// 早就是这么写的,这里只是跟上。
			require.NoError(t, db.Exec("DELETE FROM users WHERE id = ?", 910001).Error)
			t.Cleanup(func() { _ = db.Exec("DELETE FROM users WHERE id = ?", 910001).Error })
			require.NoError(t, db.Exec(
				"INSERT INTO users (id, username, password, quota, used_quota, request_count, aff_count, aff_quota, aff_history, inviter_id, role, status) "+
					"VALUES (?, ?, ?, ?, ?, 0, 0, 0, 0, 0, 1, 1)",
				910001, "qy-quota-width", "x", common.MaxQuota, common.MaxQuota).Error)
			var back struct{ Quota, UsedQuota int64 }
			require.NoError(t, db.Raw("SELECT quota, used_quota FROM users WHERE id = ?", 910001).Scan(&back).Error)
			assert.EqualValues(t, common.MaxQuota, back.Quota,
				"users.quota 必须原样存下 common.MaxQuota,截断/报错都说明列不够宽")
			assert.EqualValues(t, common.MaxQuota, back.UsedQuota)
			require.NoError(t, db.Exec("DELETE FROM users WHERE id = ?", 910001).Error)
		})
	}
}
