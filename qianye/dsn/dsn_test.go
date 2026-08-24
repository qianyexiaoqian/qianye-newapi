package dsn_test

import (
	"testing"

	"github.com/QuantumNous/new-api/qianye/dsn"

	"github.com/stretchr/testify/assert"
)

// 扩展库拒绝 SQLite 的措辞里写着"为什么不支持"(资金路径依赖行锁),而这段
// 措辞只有在判据真的命中时才送得出去。判据以前只认三种前缀,于是最自然的
// 一种写法 —— 照抄上游 SQLITE_PATH 的 `./data/qy_ext.db` —— 一路穿到 mysql
// 驱动手里,运维拿到的是 `default addr for network './data' unknown`。
//
// 反方向同样重要:误伤一个能用的 MySQL / PostgreSQL DSN 会把可用配置拒掉,
// 所以下面两组必须一起断言。
func TestIsSQLiteCoversEveryWritingWithoutEatingRealDSNs(t *testing.T) {
	sqliteForms := []struct{ name, in string }{
		{"前缀 sqlite:", "sqlite:./data/qy.db"},
		{"前缀 file:", "file:./data/qy.db"},
		{"关键字 local", "local"},
		{"关键字 local 带参数", "local ?cache=shared"},
		{"相对路径(照抄 SQLITE_PATH 最常见的一种)", "./data/qy_ext.db"},
		{"相对路径不带 ./", "data/qy_ext.db"},
		{"上跳一级", "../data/qy_ext.db"},
		{"POSIX 绝对路径", "/var/lib/qianye/qy_ext.db"},
		{"Windows 盘符正斜杠", "C:/data/qy_ext.db"},
		{"Windows 盘符反斜杠", "C:\\data\\qy_ext.db"},
		{"带 SQLite 参数", "./data/qy_ext.db?_busy_timeout=30000"},
		{".sqlite 扩展名", "/srv/qy_ext.sqlite"},
		{".sqlite3 扩展名", "/srv/qy_ext.sqlite3"},
		{"家目录", "~/qy_ext.db"},
		{"裸文件名", "one-api.db"},
	}
	for _, tc := range sqliteForms {
		t.Run("是SQLite/"+tc.name, func(t *testing.T) {
			assert.True(t, dsn.IsSQLite(tc.in),
				"%q 必须被判成 SQLite,否则它会落到 mysql 驱动手里,"+
					"运维拿到的是一句网络地址解析错,而不是那段讲清了为什么不支持的措辞", tc.in)
		})
	}

	realDSNs := []struct{ name, in string }{
		{"MySQL 标准形态", "root:pw@tcp(127.0.0.1:3306)/qy_ext?charset=utf8mb4&parseTime=true"},
		{"MySQL 无密码", "root@tcp(127.0.0.1:3307)/qy_ext?charset=utf8mb4"},
		{"MySQL unix socket", "root:pw@unix(/var/run/mysqld/mysqld.sock)/qy_ext"},
		{"PostgreSQL URL", "postgres://qy:pw@127.0.0.1:5432/qy_ext?sslmode=disable"},
		{"PostgreSQL libpq 关键字", "host=127.0.0.1 port=5432 user=qy dbname=qy_ext sslmode=disable"},
		{"空串(由上一层报『不能为空』)", ""},
	}
	for _, tc := range realDSNs {
		t.Run("不是SQLite/"+tc.name, func(t *testing.T) {
			assert.False(t, dsn.IsSQLite(tc.in),
				"%q 是一个能用的 DSN,误判成 SQLite 会把可用配置整个拒掉", tc.in)
		})
	}
}
