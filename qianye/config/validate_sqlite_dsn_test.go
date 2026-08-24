package config

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// 扩展库拒绝 SQLite 时,拒绝的**措辞**本身是这条判据的产出物之一:
// validateDatabase 的文档注释写着"只写『仅支持 MySQL』会让人以为是适配
// 工作量问题,于是下一个人把 sqlite 驱动接上去,而资金串行化在那一刻静默失效"。
//
// 而这段措辞以前只在三种前缀(sqlite: / file: / local)上送得出去。最自然的
// 一种写法 —— 照抄上游 SQLITE_PATH 的相对路径 `./data/qy_ext.db` —— 三条都不
// 匹配,又因为含 '/' 通过了"不像 MySQL DSN(缺少库名)"那一关,最后落到 mysql
// 驱动手里:运维拿到的是 `default addr for network './data' unknown`,
// 里面既没有 SQLite,也没有行锁,也没有"不支持"。
//
// 本用例同时钉住两个方向:该拒的要拒且要说清理由,能用的 DSN 一个都不许误伤。
func TestValidateDatabaseRejectsEverySQLiteWriting(t *testing.T) {
	base := func(dsn string) *Database {
		return &Database{DSN: dsn, MaxIdleConns: 4, MaxOpenConns: 16, LogLevel: LogLevelWarn}
	}

	t.Run("拒绝并说清理由", func(t *testing.T) {
		for _, in := range []string{
			"sqlite:./data/qy.db",
			"file:./data/qy.db",
			"local",
			"./data/qy_ext.db",
			"data/qy_ext.db",
			"/var/lib/qianye/qy_ext.db",
			"C:/data/qy_ext.db",
			"./data/qy_ext.db?_busy_timeout=30000",
			"one-api.db",
		} {
			err := validateDatabase(base(in))
			require.Error(t, err, "%q 必须被拒", in)
			msg := err.Error()
			assert.Contains(t, msg, "SQLite", "%q 的拒绝措辞里必须点名 SQLite", in)
			assert.Contains(t, msg, "行锁",
				"%q 的拒绝措辞里必须说出理由(资金路径依赖行锁)—— "+
					"只说『不支持』会让下一个人以为是适配工作量问题", in)
		}
	})

	t.Run("能用的 DSN 一个都不许误伤", func(t *testing.T) {
		for _, in := range []string{
			"root:pw@tcp(127.0.0.1:3306)/qy_ext?charset=utf8mb4&parseTime=true",
			"root@tcp(127.0.0.1:3307)/qy_ext?charset=utf8mb4&parseTime=true&loc=Local",
			"postgres://qy:pw@127.0.0.1:5432/qy_ext?sslmode=disable",
		} {
			assert.NoError(t, validateDatabase(base(in)), "%q 是能用的 DSN", in)
		}
	})
}
