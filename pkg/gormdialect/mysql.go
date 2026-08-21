package gormdialect

// MySQL 上,修之前 migrateDB() 第二遍会发 3 条 ALTER TABLE … MODIFY COLUMN:
//
//	custom_oauth_providers.enabled     boolean DEFAULT false   —— 库里存的是 tinyint(1) 的 0
//	subscription_plans.enabled         boolean DEFAULT true    —— 库里存的是 tinyint(1) 的 1
//	subscription_plans.price_amount    decimal(10,6) DEFAULT 0 —— 库里存的是 0.000000
//
// 三条都是同一个成因:模型标签写的默认值与 MySQL 实际存下来的写法不同,
// 而 gorm 是按字符串比的。这正是 AGENTS.md 里点名的 `gorm:"default:true"` 陷阱。
//
// 这里不动模型标签。改标签要么把布尔默认值挪进业务代码(改行为,且 Enabled 的
// 零值是 false —— 这两列的业务默认恰好一个 true 一个 false,挪出去等于把一个
// 零值陷阱换成两个),要么写成 `default:1`,而 AGENTS.md 明确要求这种替换必须
// 三个数据库都验过。按值相等来比,是不改任何列定义就能同时对上三个方言的做法。

import (
	"gorm.io/driver/mysql"
	"gorm.io/gorm"
	"gorm.io/gorm/schema"
)

// OpenMySQL 包装 mysql.Open,除 Migrator 之外的行为与上游驱动完全一致。
func OpenMySQL(dsn string) gorm.Dialector {
	return mysqlDialector{Dialector: mysql.Open(dsn).(*mysql.Dialector)}
}

type mysqlDialector struct {
	*mysql.Dialector
}

func (d mysqlDialector) Migrator(db *gorm.DB) gorm.Migrator {
	upstream, ok := d.Dialector.Migrator(db).(mysql.Migrator)
	if !ok {
		return d.Dialector.Migrator(db)
	}
	return mysqlMigrator{Migrator: upstream}
}

type mysqlMigrator struct {
	mysql.Migrator
}

// MigrateColumn 在交给上游之前先按"值相等"归一化默认值,见 defaultvalue.go。
func (m mysqlMigrator) MigrateColumn(value interface{}, field *schema.Field, columnType gorm.ColumnType) error {
	return m.Migrator.MigrateColumn(value, field, withNormalizedDefault(field, columnType))
}
