package gormdialect

// SQLite 不支持 ALTER COLUMN,驱动的 AlterColumn 走 recreateTable:
// CREATE TABLE t__temp → INSERT SELECT → DROP TABLE t → RENAME。所以一次"列需要
// 变更"的误判,代价不是一条空转 ALTER,而是**整张表连同全部索引重建一遍**。
// 修之前 migrateDB() 第二遍会重建 users / external_identity_claims / top_ups /
// models / vendors / two_fas / checkins / subscription_orders /
// user_oauth_bindings / perf_metrics 这 10 张表,每次进程启动都来一遍。
//
// 成因只有一个:驱动是靠**正则解析 sqlite_master 里的建表与建索引 SQL 文本**
// 还原列信息的(glebarez/sqlite@v1.9.0 ddlmod.go)。它遍历 sqlite_master 每一行,
// 遇到 `CREATE [UNIQUE] INDEX … ON t(a, b)` 就对括号里的**每一列**执行
//
//	c.UniqueValue = (第二个单词 == "UNIQUE")
//
// 两个方向都错:
//
//   - 复合唯一索引 uk(a, b) 把 a、b **各自**标成唯一列。而 gorm 的
//     schema.ParseIndexes 只对单列唯一索引置 field.Unique,两边永远不等。
//     external_identity_claims、user_oauth_bindings、perf_metrics、models、
//     vendors、checkins 六张表是这么中的。
//
//   - 这是**赋值**不是"或等于"。列定义里写了 UNIQUE、同时又建了一条普通索引的列
//     (`gorm:"unique;index"`),普通索引那一行会把前面解析出来的 true 冲回 false。
//     users.username、top_ups.trade_no、subscription_orders.trade_no、
//     two_fas.user_id 四列是这么中的。
//
// 修法是不去猜 DDL 文本,改问 SQLite 自己:PRAGMA index_list / index_info 是权威
// 来源,而且对"列定义里的 UNIQUE"(SQLite 自动建的 sqlite_autoindex_*,origin='u')
// 与"显式 CREATE UNIQUE INDEX"(origin='c')一视同仁。判据与 gorm 置 field.Unique
// 的判据对齐:**恰好一列**的唯一索引才算这一列唯一。

import (
	"database/sql"

	"github.com/glebarez/sqlite"
	"gorm.io/gorm"
	"gorm.io/gorm/migrator"
	"gorm.io/gorm/schema"
)

// OpenSQLite 包装 sqlite.Open,除 Migrator 之外的行为与上游驱动完全一致。
func OpenSQLite(dsn string) gorm.Dialector {
	return sqliteDialector{Dialector: sqlite.Open(dsn).(*sqlite.Dialector)}
}

type sqliteDialector struct {
	*sqlite.Dialector
}

func (d sqliteDialector) Migrator(db *gorm.DB) gorm.Migrator {
	upstream, ok := d.Dialector.Migrator(db).(sqlite.Migrator)
	if !ok {
		return d.Dialector.Migrator(db)
	}
	return sqliteMigrator{Migrator: upstream}
}

type sqliteMigrator struct {
	sqlite.Migrator
}

// ColumnTypes 用 PRAGMA 重算列的唯一性,其余字段原样透传。
func (m sqliteMigrator) ColumnTypes(value interface{}) ([]gorm.ColumnType, error) {
	columnTypes, err := m.Migrator.ColumnTypes(value)
	if err != nil {
		return columnTypes, err
	}

	table, err := m.tableName(value)
	if err != nil || table == "" {
		return columnTypes, err
	}
	uniqueColumns := m.singleColumnUniqueIndexColumns(table)

	// 上游把 migrator.ColumnType 以**值**放进切片,拿不到指针,只能整项替换。
	for i, columnType := range columnTypes {
		column, ok := columnType.(migrator.ColumnType)
		if !ok {
			continue
		}
		column.UniqueValue = sql.NullBool{Bool: uniqueColumns[column.NameValue.String], Valid: true}
		columnTypes[i] = column
	}
	return columnTypes, nil
}

// MigrateColumn 在交给上游之前先按"值相等"归一化默认值,见 defaultvalue.go。
func (m sqliteMigrator) MigrateColumn(value interface{}, field *schema.Field, columnType gorm.ColumnType) error {
	return m.Migrator.MigrateColumn(value, field, withNormalizedDefault(field, columnType))
}

func (m sqliteMigrator) tableName(value interface{}) (string, error) {
	var table string
	err := m.RunWithValue(value, func(stmt *gorm.Statement) error {
		table = stmt.Table
		return nil
	})
	return table, err
}

// sqliteIndexRow 是 PRAGMA index_list 的一行。
//
// `unique` 是 SQL 关键字,必须在查询里改名再扫。
type sqliteIndexRow struct {
	Name     string `gorm:"column:name"`
	IsUnique bool   `gorm:"column:is_unique"`
	Origin   string `gorm:"column:origin"`
}

// singleColumnUniqueIndexColumns 返回"被单列唯一索引覆盖"的列名集合。
//
// origin='pk' 的索引排除在外:主键那一列在 gorm 的比较里本来就被 field.PrimaryKey
// 短路。origin='u'(列定义或表级 UNIQUE 约束自动建的)与 origin='c'
// (显式 CREATE UNIQUE INDEX)都算。
func (m sqliteMigrator) singleColumnUniqueIndexColumns(table string) map[string]bool {
	columns := make(map[string]bool)

	var indexes []sqliteIndexRow
	if err := m.DB.Raw(
		`SELECT name, "unique" AS is_unique, origin FROM pragma_index_list(?)`, table,
	).Scan(&indexes).Error; err != nil {
		// 读不到就退回上游行为,不要因为这一层让迁移失败。
		return columns
	}

	for _, index := range indexes {
		if !index.IsUnique || index.Origin == "pk" {
			continue
		}
		var names []sql.NullString
		if err := m.DB.Raw(`SELECT name FROM pragma_index_info(?)`, index.Name).Scan(&names).Error; err != nil {
			continue
		}
		// 恰好一列才算;表达式索引的列名是 NULL,同样不算。
		if len(names) != 1 || !names[0].Valid {
			continue
		}
		columns[names[0].String] = true
	}
	return columns
}
