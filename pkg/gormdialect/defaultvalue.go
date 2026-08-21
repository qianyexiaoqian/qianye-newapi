package gormdialect

import (
	"strings"

	"github.com/shopspring/decimal"
	"gorm.io/gorm"
	"gorm.io/gorm/schema"
)

// columnTypeWithDefault 换掉一个 gorm.ColumnType 的默认值,其余方法原样透传。
//
// 之所以不去改 migrator.ColumnType 的字段:各驱动往切片里放的有的是值有的是指针,
// 包一层接口是唯一对两者都成立的写法。
type columnTypeWithDefault struct {
	// 这里不能直接内嵌 gorm.ColumnType:内嵌字段名就叫 ColumnType,会把接口自己的
	// ColumnType() 方法遮住,结果是包装类型反而不再满足 gorm.ColumnType。
	// 起个别名让字段名换掉,方法照常提升。
	columnTypeBase
	defaultValue string
}

type columnTypeBase interface{ gorm.ColumnType }

func (c columnTypeWithDefault) DefaultValue() (string, bool) { return c.defaultValue, true }

// withNormalizedDefault 在"数据库存的默认值与模型标签写的是同一个值、只是写法不同"
// 时,把现状改写成模型的写法,让 gorm 的字符串比较能对上。值不同则原样返回。
//
// 两条规则:
//
//	布尔:MySQL 把 `default:true` 存成 tinyint(1) 的 1,读回来就是 "1";
//	      PG 存成 true。两边只要表示同一个真值,就报模型那一侧的写法。
//	数值:decimal(10,6) 上的 `default:0`,MySQL 与 PG 都存成 "0.000000"。
//	      用 decimal 精确比较,不用 float,避免把 0.1 这类字面量比出假不等。
//
// 只在两边都不为空时才动;空默认值有它自己的语义(null ↔ 有默认值),
// 交给 gorm 原本的分支判断。
func withNormalizedDefault(field *schema.Field, columnType gorm.ColumnType) gorm.ColumnType {
	current, ok := columnType.DefaultValue()
	if !ok || current == "" || field.DefaultValue == "" {
		return columnType
	}
	if current == field.DefaultValue {
		return columnType
	}

	if field.DataType == schema.Bool {
		currentTruth, currentOK := parseBoolLiteral(current)
		wantTruth, wantOK := parseBoolLiteral(field.DefaultValue)
		if currentOK && wantOK && currentTruth == wantTruth {
			return columnTypeWithDefault{columnTypeBase: columnType, defaultValue: field.DefaultValue}
		}
		return columnType
	}

	currentNumber, currentErr := decimal.NewFromString(current)
	wantNumber, wantErr := decimal.NewFromString(field.DefaultValue)
	if currentErr == nil && wantErr == nil && currentNumber.Equal(wantNumber) {
		return columnTypeWithDefault{columnTypeBase: columnType, defaultValue: field.DefaultValue}
	}
	return columnType
}

// parseBoolLiteral 认三种写法:1/0、true/false、t/f(PG 的 information_schema 会用 t/f)。
func parseBoolLiteral(value string) (bool, bool) {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "1", "true", "t":
		return true, true
	case "0", "false", "f":
		return false, true
	default:
		return false, false
	}
}
