package model

import (
	"fmt"

	"gorm.io/gorm"
	"gorm.io/gorm/schema"
)

// 本文件定义扩展库里三种二进制列的跨方言类型。
//
// # 为什么不能继续写 gorm:"type:blob" 这类标签
//
// `type:` 标签是**逐字**写进 DDL 的,它绕过驱动的类型映射。blob / mediumblob /
// varbinary(N) 全是 MySQL 专有拼写,PostgreSQL 上 AutoMigrate 会直接报
// `type "mediumblob" does not exist` —— 建表整体失败,不是降级。
//
// # 为什么不干脆把标签删掉、让驱动自己选
//
// 那会改掉 MySQL 现有的列类型:gorm.io/driver/mysql 对无 size 的 []byte 发的是
// longblob,对有 size 的发 varbinary(size)。存量库上这等于每次启动都
// ALTER TABLE ... MODIFY COLUMN,正是 qianye/migrate_idempotency_test.go 盯着的
// 那类空转 DDL,而且是在资金表上取排他元数据锁。
//
// 所以走 GORM 的 GormDBDataType 接口:MySQL 侧逐字保持原样,其他方言各自给出
// 等价类型。三个类型而不是一个,是因为 MySQL 侧的三种拼写有真实差异
// (blob 64KB / mediumblob 16MB / varbinary 定长上界),合并会改变存量列。
//
// 三个类型的底层都是 []byte,与 []byte 之间可以直接赋值和传参(Go 的可赋值性
// 规则:两者底层类型相同且至少一方是未命名类型),调用点无需改动。

// Blob 对应 MySQL 的 blob(最大 64KB)。
type Blob []byte

// MediumBlob 对应 MySQL 的 mediumblob(最大 16MB)。
type MediumBlob []byte

// VarBinary 对应 MySQL 的 varbinary(N),N 取字段的 `gorm:"size:N"`。
//
// 没写 size 时回落到 blob 而不是 varbinary(255):默认长度是一个会静默截断
// 密文的取值,而这几列存的都是 AEAD 密文,截断即不可解密。
type VarBinary []byte

func (Blob) GormDBDataType(db *gorm.DB, _ *schema.Field) string {
	return binaryColumnType(db, "blob")
}

func (MediumBlob) GormDBDataType(db *gorm.DB, _ *schema.Field) string {
	return binaryColumnType(db, "mediumblob")
}

func (VarBinary) GormDBDataType(db *gorm.DB, field *schema.Field) string {
	if field != nil && field.Size > 0 {
		return binaryColumnType(db, fmt.Sprintf("varbinary(%d)", field.Size))
	}
	return binaryColumnType(db, "blob")
}

// binaryColumnType 把 MySQL 的拼写翻成当前方言。
//
// PostgreSQL 只有一种变长二进制类型 bytea,它没有长度上界也不接受长度修饰
// (`bytea(16)` 是语法错误),因此三种 MySQL 拼写在 PostgreSQL 上都折成 bytea。
// 长度上界的丢失是可接受的:这几列的写入方全是固定长度的 nonce 或有界密文,
// 上界由应用层保证,数据库侧的长度检查从来不是它们的唯一防线。
//
// SQLite 只有 BLOB 一种储存类,同理。
func binaryColumnType(db *gorm.DB, mysqlType string) string {
	if db == nil || db.Dialector == nil {
		return mysqlType
	}
	switch db.Dialector.Name() {
	case "postgres":
		return "bytea"
	case "sqlite", "sqlite3":
		return "blob"
	default:
		return mysqlType
	}
}
