package gormdialect

import (
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm/schema"
)

type uniqueContractProbe struct {
	ID       uint
	ByTag    string `gorm:"unique"`
	ByIndex  string `gorm:"uniqueIndex"`
	ByPlain  string `gorm:"index"`
	ByCombo1 string `gorm:"uniqueIndex:uk_combo"`
	ByCombo2 string `gorm:"uniqueIndex:uk_combo"`
}

// TestFieldUniqueComesFromTheTagNotTheIndex 钉住两个方言装饰器共同依赖的那条 gorm 契约:
// **field.Unique 只由 `unique` 标签决定,`uniqueIndex` 不置它**。
//
// # 为什么需要一条不连库的守卫
//
// gorm v1.25.2 的 schema.ParseIndexes 里有一行 `index.Fields[0].Field.Unique = true`,
// 把单列 uniqueIndex 也算成 field.Unique。两个装饰器当年正是照着那条写的:让
// ColumnType.Unique() 把唯一索引也报成 true,否则 migrateDB 第二遍空转 ALTER。
//
// v1.25.12 把那一行**删了**,并把唯一性比较独立成 migrator.MigrateColumnUnique,
// 注释写死 "By default, ColumnType's Unique is not affected by UniqueIndex"。
// 契约反过来之后,旧覆写从"消除噪音"变成"制造错误":
//
//	PostgreSQL  第二次启动 migrateDB 直接失败 ——
//	            ERROR 42704 constraint "uni_tokens_key" of relation "tokens" does not exist
//	SQLite      Drop/CreateConstraint 走 recreateTable,每次启动重建十几张表
//
// 两处都只有连上真库跑第二遍迁移才看得见,而 TestMigrateDBIsIdempotent 在没配
// TEST_MYSQL_DSN / TEST_POSTGRES_DSN 时是 SKIP —— 也就是说升级 gorm 的那个人
// 在本机是全绿的。这条守卫不连库,直接问 gorm 自己,所以它在任何一次 `go test ./...`
// 上都会说话。
//
// 它红了不代表 gorm 有 bug,代表**两个装饰器的前提没了**:去 postgres.go /
// sqlite.go 重新对一遍 ColumnType.Unique() 该报什么。
func TestFieldUniqueComesFromTheTagNotTheIndex(t *testing.T) {
	parsed, err := schema.Parse(&uniqueContractProbe{}, &sync.Map{}, schema.NamingStrategy{})
	require.NoError(t, err)

	byName := make(map[string]*schema.Field, len(parsed.Fields))
	for _, field := range parsed.Fields {
		byName[field.Name] = field
	}

	require.Contains(t, byName, "ByTag")
	assert.True(t, byName["ByTag"].Unique,
		"`unique` 标签必须置 field.Unique —— 这是 ColumnType.Unique() 要对齐的那一侧")

	require.Contains(t, byName, "ByIndex")
	assert.False(t, byName["ByIndex"].Unique,
		"单列 `uniqueIndex` **不得**置 field.Unique(gorm v1.25.12 起)。\n"+
			"一旦它又变成 true,postgres.go / sqlite.go 里"+
			"「ColumnType.Unique() 只反映 UNIQUE 约束」的前提就不成立了,\n"+
			"而后果只在真库上第二次迁移时才看得见:PG 报 42704 起不来、SQLite 每次启动重建表。")

	require.Contains(t, byName, "ByPlain")
	assert.False(t, byName["ByPlain"].Unique, "普通 index 当然不是唯一")

	require.Contains(t, byName, "ByCombo1")
	assert.False(t, byName["ByCombo1"].Unique, "复合唯一索引的任何一列都不是列级唯一")
	assert.False(t, byName["ByCombo2"].Unique, "复合唯一索引的任何一列都不是列级唯一")
}
