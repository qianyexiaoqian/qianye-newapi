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
	"database/sql"
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

	uniqueByIndex := m.singleColumnUniqueIndexColumns(value)
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
		if uniqueByIndex[column.NameValue.String] {
			column.UniqueValue = sql.NullBool{Bool: true, Valid: true}
		}
	}
	return columnTypes, nil
}

// MigrateColumn 在交给上游之前先按"值相等"归一化默认值,见 defaultvalue.go。
func (m postgresMigrator) MigrateColumn(value interface{}, field *schema.Field, columnType gorm.ColumnType) error {
	return m.Migrator.MigrateColumn(value, field, withNormalizedDefault(field, columnType))
}

// singleColumnUniqueIndexColumns 返回"被单列唯一索引覆盖"的列名集合。
//
// 主键索引排除在外:主键那一列在 gorm 的比较里本来就被 field.PrimaryKey 短路,
// 混进来只会让集合的含义变模糊。多列唯一索引也排除 —— gorm 的
// schema.ParseIndexes 同样只对单列唯一索引置 field.Unique,两边必须同口径。
func (m postgresMigrator) singleColumnUniqueIndexColumns(value interface{}) map[string]bool {
	columns := make(map[string]bool)
	indexes, err := m.GetIndexes(value)
	if err != nil {
		// 读不到索引就退回上游行为(全部 unique=false),不要因为这一层让迁移失败。
		return columns
	}
	for _, index := range indexes {
		if primary, ok := index.PrimaryKey(); ok && primary {
			continue
		}
		if unique, ok := index.Unique(); !ok || !unique {
			continue
		}
		names := index.Columns()
		if len(names) != 1 {
			continue
		}
		columns[names[0]] = true
	}
	return columns
}

// postgresDefaultCastPattern 剥掉 PG 默认值上的 `::type` 后缀,口径与
// gorm.io/driver/postgres v1.5.3+ 的 parseDefaultValueValue 一致。
var postgresDefaultCastPattern = regexp.MustCompile(`^(.*?)(?:::.*)?$`)

// stripPostgresDefaultCast 剥掉默认值末尾的类型转换:空串默认值剥成空串,
// 带引号的字面量剥成字面量本身('abc'::text -> abc)。
func stripPostgresDefaultCast(raw string) string {
	return strings.Trim(postgresDefaultCastPattern.ReplaceAllString(raw, "$1"), "'")
}
