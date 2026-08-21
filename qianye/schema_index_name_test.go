package qianye

import (
	"sort"
	"strings"
	"sync"
	"testing"

	"gorm.io/gorm/schema"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// schema_index_name_test.go —— 扩展库的索引名必须在整个 schema 内唯一。
//
// 这条判据不是风格洁癖,它守的是一个只在 PostgreSQL/SQLite 上现形、
// 而且**完全静默**的缺陷:
//
//	MySQL      索引名是「每表」独立的  → 两张表用同一个名字,各建一份,相安无事
//	PostgreSQL 索引名是「schema 级」的  → 第二张表建不出来
//	SQLite     索引名是「库级」的       → 同上
//
// 而 GORM 的 AutoMigrate 在 PostgreSQL 上发的是
// `CREATE [UNIQUE] INDEX IF NOT EXISTS <name> ON <table> (...)`:名字已被
// **另一张表**占用时,这条语句既不建索引也不报错,只是静默地什么都没做,
// 并且每次启动都再发一遍(空转 DDL)。
//
// 实测后果(qy_avail_bucket_hour,匿名内嵌 Bucket 时连索引名标签一起继承):
// 小时表的三条索引一条都没建出来,其中包括 (bucket_ts,group_name,model_name)
// 唯一索引;rollupHour 的 `ON CONFLICT (这三列) DO UPDATE` 于是恒报
// `42P10 there is no unique or exclusion constraint matching the ON CONFLICT
// specification`,PG 上小时表永远是 0 行,而 availability 的查询侧规定
// 跨度 > 48 小时一律读小时表 —— 「7 天 / 30 天」可用率视图永久无数据。
// MySQL 侧一切正常,所以这条缺陷在 MySQL 上跑一万次也发现不了。
//
// 判据刻意做在 schema 解析层而不是真库上:它不需要任何 DSN,永远会跑,
// 而且能同时覆盖三种方言的部署 —— 真库判据(TestExtensionAutoMigrateIsIdempotent)
// 只在设了 DSN 时才跑。
func TestExtensionIndexNamesAreSchemaUnique(t *testing.T) {
	tables := allTables()
	require.NotEmpty(t, tables)

	cache := &sync.Map{}
	namer := schema.NamingStrategy{}

	// 索引名 → 申领它的表名集合。
	owners := map[string][]string{}
	for _, model := range tables {
		parsed, err := schema.Parse(model, cache, namer)
		require.NoErrorf(t, err, "解析模型失败: %T", model)
		for name := range parsed.ParseIndexes() {
			owners[name] = append(owners[name], parsed.Table)
		}
	}
	require.NotEmpty(t, owners, "一条索引都没解析出来,判据本身失效了")

	var collisions []string
	for name, tableNames := range owners {
		unique := map[string]struct{}{}
		for _, table := range tableNames {
			unique[table] = struct{}{}
		}
		if len(unique) < 2 {
			continue
		}
		names := make([]string, 0, len(unique))
		for table := range unique {
			names = append(names, table)
		}
		sort.Strings(names)
		collisions = append(collisions, name+" ← "+strings.Join(names, ", "))
	}
	sort.Strings(collisions)

	assert.Empty(t, collisions,
		"同一个索引名被多张表申领:MySQL 上各建一份看不出问题,但 PostgreSQL 与 SQLite "+
			"的索引名是 schema/库级唯一的,第二张表的索引会被 CREATE INDEX IF NOT EXISTS "+
			"静默吞掉(连唯一约束一起丢),依赖它的 ON CONFLICT 会恒报 42P10。"+
			"修法:索引标签不要硬编码名字,写成 `index:,composite:xxx` 让 GORM 按 idx_<表名>_xxx 派生")
}
