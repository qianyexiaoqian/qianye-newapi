package gormdialect

// PostgreSQL 上,修之前 migrateDB() 第二遍会发 63 条 ALTER,横跨 20 张表 42 个列。
// 42 个列的触发分支只有三种:
//
//  1. 唯一性(10 列)。驱动的 ColumnTypes 从 information_schema.table_constraints
//     取 UNIQUE,而 gorm 为 `uniqueIndex` 标签建的是唯一**索引** —— PG 的唯一索引
//     不进 table_constraints。于是 columnType.Unique() 恒为 false,而 gorm v1.25.2
//     的 schema.ParseIndexes 会把单列 uniqueIndex 的 field.Unique 置为 true,
//     两边永远不等。MySQL 驱动是从 SHOW INDEX 取的、唯一索引算数,所以 MySQL 上
//     没有这个问题;这里做的就是让 PG 与 MySQL 同口径。
//
//  2. 默认值(28 列)。PG 的 information_schema.columns.column_default 对
//     `default:''` 的列返回 `''::character varying`。驱动 v1.5.2 用正则
//     `'?(.*)\b'?:+[\w\s]+$` 剥这个后缀,对空串匹配不上(`''` 与 `::` 之间不构成
//     \b 词边界),于是原样返回,与模型侧的 "" 永远不等。上游 v1.5.3 换成了
//     parseDefaultValueValue,下面按同样口径实现。
//
//  3. 类型名(4 列)。驱动取 udt_name 作 DatabaseTypeName,PG 对 `char(64)` 报
//     `bpchar`;模型标签写的是 `char(64)`,前缀比不上,typeAliasMap 里也没有
//     bpchar 条目(到 v1.6.2 仍然没有)。
//
// 另外 subscription_plans.price_amount 走 defaultvalue.go 那条值相等规则:
// 模型写 `default:0`,PG 存成 `0.000000`。
//
// 这三类判定一旦成立,PG 驱动的 AlterColumn 还会**额外**重发一条
// `ALTER COLUMN … TYPE varchar(N)` —— 因为它自己的同类型判断是拿
// DatabaseTypeName()("varchar") 去比 DataTypeOf()("varchar(64)")。所以 63 条里
// 大部分 TYPE 语句是上面三类误判的连带产物,不是独立成因。

import (
	"regexp"
	"strings"

	"gorm.io/driver/postgres"
	"gorm.io/gorm"
	"gorm.io/gorm/migrator"
	"gorm.io/gorm/schema"
)

// NewPostgres 包装 postgres.New,除 Migrator 之外的行为与上游驱动完全一致。
func NewPostgres(config postgres.Config) gorm.Dialector {
	return postgresDialector{Dialector: postgres.New(config).(*postgres.Dialector)}
}

type postgresDialector struct {
	*postgres.Dialector
}

// Migrator 返回带归一化的 Migrator。
//
// 注意传给上游的是内嵌的 *postgres.Dialector 而不是本类型:上游驱动内部有
// `m.Dialector.(Dialector)` 这样的类型断言(GetRows 用它决定要不要塞
// QueryExecModeSimpleProtocol),断言的是它自己的类型,套一层就断言不到了。
// 而 gorm 自己的 DB.Migrator() 走 db.Dialector.Migrator(db),拿到的仍是本类型,
// 因此 gorm 内部所有 m.DB.Migrator() 的再分发都会命中下面的覆写。
func (d postgresDialector) Migrator(db *gorm.DB) gorm.Migrator {
	upstream, ok := d.Dialector.Migrator(db).(postgres.Migrator)
	if !ok {
		return d.Dialector.Migrator(db)
	}
	return postgresMigrator{Migrator: upstream}
}

type postgresMigrator struct {
	postgres.Migrator
}

// ColumnTypes 修正上面第 1、2、3 三类现状读取。
func (m postgresMigrator) ColumnTypes(value interface{}) ([]gorm.ColumnType, error) {
	columnTypes, err := m.Migrator.ColumnTypes(value)
	if err != nil {
		return columnTypes, err
	}

	for _, columnType := range columnTypes {
		column, ok := columnType.(*migrator.ColumnType)
		if !ok {
			continue
		}
		if strings.EqualFold(column.DataTypeValue.String, "bpchar") {
			// 只换类型名,LengthValue 原样保留,gorm 的 size 分支照常工作,
			// char(32) → char(64) 这种真实变更仍然会被检出。
			column.DataTypeValue.String = "char"
		}
		if column.DefaultValueValue.Valid {
			column.DefaultValueValue.String = stripPostgresDefaultCast(column.DefaultValueValue.String)
		}
	}
	return columnTypes, nil
}

// MigrateColumn 在交给上游之前先按"值相等"归一化默认值,见 defaultvalue.go。
func (m postgresMigrator) MigrateColumn(value interface{}, field *schema.Field, columnType gorm.ColumnType) error {
	return m.Migrator.MigrateColumn(value, field, withNormalizedDefault(field, columnType))
}

// ── 曾经这里还有第 4 类修正:唯一性(singleColumnUniqueIndexColumns) ──
//
// 它把「被单列唯一**索引**覆盖的列」也报成 ColumnType.Unique()==true,理由是
// gorm v1.25.2 的 schema.ParseIndexes 会为单列 uniqueIndex 置 field.Unique
// (schema/index.go:70 `index.Fields[0].Field.Unique = true`),两边不同口径就会
// 让 migrateDB 第二遍空转 ALTER。
//
// **gorm v1.25.12 把那一行删了**,契约整个反过来:field.Unique 现在只由
// `unique` 标签决定,而新独立出来的 migrator.MigrateColumnUnique 明确写着
// "By default, ColumnType's Unique is not affected by UniqueIndex"。
// 于是旧的覆写从"消除噪音"变成"制造错误":
//
//	columnType.Unique()==true(索引) && field.Unique==false(无 unique 标签)
//	  → DropConstraint(value, "uni_tokens_key")
//	  → PG 上那是一条**索引**不是约束 → ERROR 42704 constraint does not exist
//
// 后果不是多几条空转 DDL,是**第二次启动直接迁移失败**(第一次建表能过)。
// 实测 PostgreSQL 16.4:migrateDB 第二遍 `constraint "uni_tokens_key" of relation
// "tokens" does not exist`。SQLite 侧是同一条契约变更的另一种表现(那边
// Drop/CreateConstraint 走 recreateTable,于是每次启动重建十几张表),
// 修法也同形 —— 见 sqlite.go 里 origin='u' 那一段。
//
// 所以这里不再覆写唯一性:上游 PG 驱动从 information_schema.table_constraints
// 取 UNIQUE(唯一索引本来就不进那张表),得到的正是 v1.25.12 想要的语义。

// postgresDefaultCastPattern 剥掉 PG 默认值上的 `::type` 后缀,口径与
// gorm.io/driver/postgres v1.5.3+ 的 parseDefaultValueValue 一致。
var postgresDefaultCastPattern = regexp.MustCompile(`^(.*?)(?:::.*)?$`)

// stripPostgresDefaultCast 剥掉默认值末尾的类型转换:空串默认值剥成空串,
// 带引号的字面量剥成字面量本身('abc'::text -> abc)。
func stripPostgresDefaultCast(raw string) string {
	return strings.Trim(postgresDefaultCastPattern.ReplaceAllString(raw, "$1"), "'")
}
