package qianye

import (
	"os"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"

	qydb "github.com/QuantumNous/new-api/qianye/db"
)

// 扩展库不得出现定长 CHAR 列。
//
// # 这是一条资金口径,不是风格洁癖
//
// 本仓所有 char(N) 列的合法取值里都包含空串:qy_commission_accrual.bucket_date
// (充值来源不填消费日桶)、各处 sha256 / payee_digest(未上传凭证、收款信息
// 已过保留期被清空)。定长 CHAR 遇到空串时:
//
//	MySQL       存储时补空格,**读取时剥掉**尾随空格 ⇒ Go 端拿到 ""
//	PostgreSQL  存储时补空格,**读取时原样返回**   ⇒ Go 端拿到 "        "
//
// 也就是说同一行数据在两种方言上会喂给业务代码两个不同的字符串。它至少沿三条
// 路径出去:接口响应(commission/api_user.go 的 bucket_date)、Go 端的 == "" 判断、
// 以及以该列做 map key 的日聚合。SQL 侧的比较不受影响(PostgreSQL 的 bpchar
// 比较忽略尾随空格),所以这个分叉在任何 SQL 断言下都是隐形的 —— 只有把值
// 取回 Go 才看得见。
//
// 判据打在**迁移出来的真实 schema** 上而不是源码文本上:模型标签可以绕开
// (自定义类型、Migrator 直接建表),而列的实际类型绕不开。
func TestExtensionSchemaHasNoFixedWidthCharColumns(t *testing.T) {
	cases := []struct {
		name  string
		env   string
		query string
	}{
		{
			name: "mysql", env: "QY_TEST_MYSQL_MIGRATE_DSN",
			query: `SELECT CONCAT(table_name, '.', column_name)
			        FROM information_schema.columns
			        WHERE table_schema = DATABASE() AND data_type = 'char'`,
		},
		{
			name: "postgres", env: "QY_TEST_PG_MIGRATE_DSN",
			// PostgreSQL 的 character(n) 在 pg_catalog 里叫 bpchar
			// (blank-padded char),information_schema 报的 data_type 是 "character"。
			query: `SELECT table_name || '.' || column_name
			        FROM information_schema.columns
			        WHERE table_schema = CURRENT_SCHEMA() AND data_type = 'character'`,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			dsn := os.Getenv(tc.env)
			if dsn == "" {
				t.Skipf("未设置 %s,跳过", tc.env)
			}
			dialector, err := qydb.DialectorFor(dsn)
			require.NoError(t, err)
			gdb, err := gorm.Open(dialector, &gorm.Config{})
			require.NoError(t, err)
			require.NoError(t, gdb.AutoMigrate(allTables()...))

			var cols []string
			require.NoError(t, gdb.Raw(tc.query).Scan(&cols).Error)
			assert.Empty(t, cols,
				"这些列是定长 CHAR,空串在 PostgreSQL 上会被补成空格再原样读出,"+
					"与 MySQL 得到的值不同;请改成 varchar(N)")
		})
	}
}

// TestEmptyStringRoundTripsIdenticallyAcrossDialects 是上一条的**理由**。
//
// 它把 "char(8) 会分叉、varchar(8) 不会" 这件事在真库上跑出来。没有它,
// 上面那条禁令读起来只是一条没有依据的规矩,下一个人有充分理由把它改回去。
func TestEmptyStringRoundTripsIdenticallyAcrossDialects(t *testing.T) {
	type row struct {
		K    int    `gorm:"column:k;primaryKey"`
		Fixd string `gorm:"column:fixd"`
		Vary string `gorm:"column:vary"`
	}
	for _, tc := range []struct{ name, env string }{
		{"mysql", "QY_TEST_MYSQL_MIGRATE_DSN"},
		{"postgres", "QY_TEST_PG_MIGRATE_DSN"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dsn := os.Getenv(tc.env)
			if dsn == "" {
				t.Skipf("未设置 %s,跳过", tc.env)
			}
			dialector, err := qydb.DialectorFor(dsn)
			require.NoError(t, err)
			gdb, err := gorm.Open(dialector, &gorm.Config{})
			require.NoError(t, err)

			const tbl = "qy_t_charpad"
			require.NoError(t, gdb.Exec("DROP TABLE IF EXISTS "+tbl).Error)
			require.NoError(t, gdb.Exec("CREATE TABLE "+tbl+
				" (k INTEGER PRIMARY KEY, fixd CHAR(8) NOT NULL, vary VARCHAR(8) NOT NULL)").Error)
			t.Cleanup(func() { _ = gdb.Exec("DROP TABLE IF EXISTS " + tbl).Error })
			require.NoError(t, gdb.Exec("INSERT INTO "+tbl+" (k, fixd, vary) VALUES (?, ?, ?)", 1, "", "").Error)

			var got row
			require.NoError(t, gdb.Table(tbl).Where("k = ?", 1).Take(&got).Error)

			assert.Equal(t, "", got.Vary, "varchar 在两种方言上都必须原样返回空串")
			if tc.name == "postgres" {
				assert.Equal(t, "        ", got.Fixd,
					"这正是禁用定长 CHAR 的理由:PostgreSQL 把空串补成 8 个空格再原样读出")
			} else {
				assert.Equal(t, "", got.Fixd, "MySQL 的 CHAR 读取时剥掉尾随空格,分叉在这一侧是隐形的")
			}
		})
	}
}
