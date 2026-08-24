package httpq_test

import (
	"os"
	"testing"

	"github.com/glebarez/sqlite"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
	gormlogger "gorm.io/gorm/logger"

	qydb "github.com/QuantumNous/new-api/qianye/db"
	"github.com/QuantumNous/new-api/qianye/httpq"
)

// searchLikeRow 是一张只为这条判据存在的临时表。
type searchLikeRow struct {
	Id       int64  `gorm:"primaryKey;autoIncrement"`
	Username string `gorm:"type:varchar(64);not null;default:''"`
	Email    string `gorm:"type:varchar(64);not null;default:''"`
}

func (searchLikeRow) TableName() string { return "qy_searchlike_probe" }

// 运营检索必须在三种数据库上给出**同一个**结果集。
//
// 裸 `col LIKE ?` 做不到:MySQL 的 utf8mb4_*_ci 排序规则大小写不敏感、
// SQLite 的 LIKE 对 ASCII 天然不敏感,而 PostgreSQL 的 LIKE **大小写敏感**。
// 于是运营在返佣页搜 `QY-Alice` 在 MySQL 部署上命中、在 PG 部署上得到
// 200 + 空列表(与"这个账号不存在"不可分辨)—— 而那一页上有余额调整、
// 佣金冲正、绑定/解绑上下线的按钮。
//
// 同一条判据还必须转义 % 与 _:不转义时运营输入一个 `%` 等价于不筛选,
// 输入的 `_` 变成"任意单字符"命中一批不相关的行,而他会以为"就这些"。
//
// 三家的差异只有真库能抓 —— SQLite 与 MySQL 上大小写这一档天然通过。
//
//	QY_TEST_MYSQL_DSN / QY_TEST_PG_DSN
func TestSearchLikeGivesTheSameAnswerOnEveryDialect(t *testing.T) {
	type fixture struct {
		name string
		open func(*testing.T) *gorm.DB
	}
	fixtures := []fixture{{
		name: "sqlite",
		open: func(t *testing.T) *gorm.DB {
			g, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{Logger: gormlogger.Discard})
			require.NoError(t, err)
			sqlDB, err := g.DB()
			require.NoError(t, err)
			sqlDB.SetMaxOpenConns(1)
			t.Cleanup(func() { _ = sqlDB.Close() })
			return g
		},
	}}
	for _, env := range []struct{ name, key string }{
		{"mysql", "QY_TEST_MYSQL_DSN"}, {"postgres", "QY_TEST_PG_DSN"},
	} {
		dsn := os.Getenv(env.key)
		if dsn == "" {
			continue
		}
		fixtures = append(fixtures, fixture{name: env.name, open: func(t *testing.T) *gorm.DB {
			d, err := qydb.DialectorFor(dsn)
			require.NoError(t, err)
			g, err := gorm.Open(d, &gorm.Config{Logger: gormlogger.Discard})
			require.NoError(t, err)
			require.NoError(t, g.Exec("DROP TABLE IF EXISTS qy_searchlike_probe").Error)
			t.Cleanup(func() { _ = g.Exec("DROP TABLE IF EXISTS qy_searchlike_probe").Error })
			return g
		}})
	}

	seed := []searchLikeRow{
		{Username: "qy-case-alice", Email: "Alice@Example.COM"},
		{Username: "QY-CASE-BOB", Email: "bob@example.com"},
		{Username: "other_user", Email: "other@example.com"},
		{Username: "100%pure", Email: "pure@example.com"},
	}

	cases := []struct {
		name    string
		keyword string
		mode    httpq.LikeMatch
		want    []string // 期望命中的 username,顺序按 id
	}{
		{"前缀·小写输入", "qy-case-alice", httpq.MatchPrefix, []string{"qy-case-alice"}},
		{"前缀·大小写混排输入必须照样命中", "QY-Case-Alice", httpq.MatchPrefix, []string{"qy-case-alice"}},
		{"前缀·库里是大写、输入是小写", "qy-case-bob", httpq.MatchPrefix, []string{"QY-CASE-BOB"}},
		{"前缀·邮箱列同样折叠", "ALICE@EXAMPLE.COM", httpq.MatchPrefix, []string{"qy-case-alice"}},
		{"子串·大小写混排", "CASE-BOB", httpq.MatchContains, []string{"QY-CASE-BOB"}},
		// % 是通配符:不转义的话这一条会命中全部 4 行(等价于不筛选)。
		{"通配符 % 必须当字面量", "%", httpq.MatchContains, []string{"100%pure"}},
		// _ 是"任意单字符":不转义的话 other_user 之外还会命中别的行。
		{"通配符 _ 必须当字面量", "other_user", httpq.MatchPrefix, []string{"other_user"}},
		{"不该命中的输入就是不命中", "zzz-nobody", httpq.MatchContains, nil},
	}

	for _, fx := range fixtures {
		t.Run(fx.name, func(t *testing.T) {
			gdb := fx.open(t)
			require.NoError(t, gdb.AutoMigrate(&searchLikeRow{}))
			for i := range seed {
				row := seed[i]
				row.Id = 0
				require.NoError(t, gdb.Create(&row).Error)
			}
			for _, tc := range cases {
				t.Run(tc.name, func(t *testing.T) {
					expr, pattern := httpq.SearchLike(tc.keyword, tc.mode, "username", "email")
					var got []string
					require.NoError(t, gdb.Model(&searchLikeRow{}).
						Where(expr, pattern, pattern).
						Order("id").Pluck("username", &got).Error)
					if len(tc.want) == 0 {
						assert.Empty(t, got)
						return
					}
					assert.Equal(t, tc.want, got)
				})
			}
		})
	}
}
