package gormdialect

import (
	"database/sql"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm/migrator"
	"gorm.io/gorm/schema"
)

func columnWithDefault(value string, valid bool) *migrator.ColumnType {
	return &migrator.ColumnType{
		NameValue:         sql.NullString{String: "col", Valid: true},
		DefaultValueValue: sql.NullString{String: value, Valid: valid},
	}
}

// TestWithNormalizedDefault 钉死这层归一化的两条边:同值必须归一(否则每次启动重发
// 一条空转 ALTER),**不同值必须原样透传**(否则这层会把一次真实的默认值变更吃掉,
// 表现是改了模型标签、重启、库里纹丝不动)。
func TestWithNormalizedDefault(t *testing.T) {
	tests := []struct {
		name         string
		dataType     schema.DataType
		fieldDefault string
		dbDefault    string
		dbValid      bool
		want         string
	}{
		{
			name:         "bool true stored as 1 is normalized to the tag spelling",
			dataType:     schema.Bool,
			fieldDefault: "true",
			dbDefault:    "1",
			dbValid:      true,
			want:         "true",
		},
		{
			name:         "bool false stored as 0 is normalized to the tag spelling",
			dataType:     schema.Bool,
			fieldDefault: "false",
			dbDefault:    "0",
			dbValid:      true,
			want:         "false",
		},
		{
			name:         "bool tag written as 1 keeps the database spelling when both mean true",
			dataType:     schema.Bool,
			fieldDefault: "1",
			dbDefault:    "true",
			dbValid:      true,
			want:         "1",
		},
		{
			name:         "postgres t/f spelling counts as boolean",
			dataType:     schema.Bool,
			fieldDefault: "false",
			dbDefault:    "f",
			dbValid:      true,
			want:         "false",
		},
		{
			name:         "bool with a genuinely different default is left alone",
			dataType:     schema.Bool,
			fieldDefault: "true",
			dbDefault:    "0",
			dbValid:      true,
			want:         "0",
		},
		{
			name:         "decimal scale padding is normalized to the tag spelling",
			dataType:     schema.Float,
			fieldDefault: "0",
			dbDefault:    "0.000000",
			dbValid:      true,
			want:         "0",
		},
		{
			name:         "decimal keeps equality across differing scales",
			dataType:     schema.Float,
			fieldDefault: "1.5",
			dbDefault:    "1.500000",
			dbValid:      true,
			want:         "1.5",
		},
		{
			name:         "decimal with a genuinely different value is left alone",
			dataType:     schema.Float,
			fieldDefault: "1",
			dbDefault:    "0.000000",
			dbValid:      true,
			want:         "0.000000",
		},
		{
			name:         "integer default that only looks similar is left alone",
			dataType:     schema.Int,
			fieldDefault: "1",
			dbDefault:    "100",
			dbValid:      true,
			want:         "100",
		},
		{
			name:         "non-numeric string defaults are never touched",
			dataType:     schema.String,
			fieldDefault: "default",
			dbDefault:    "guest",
			dbValid:      true,
			want:         "guest",
		},
		{
			name:         "empty tag default is left alone",
			dataType:     schema.String,
			fieldDefault: "",
			dbDefault:    "0.000000",
			dbValid:      true,
			want:         "0.000000",
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			field := &schema.Field{DataType: test.dataType, DefaultValue: test.fieldDefault, HasDefaultValue: true}
			got := withNormalizedDefault(field, columnWithDefault(test.dbDefault, test.dbValid))
			value, ok := got.DefaultValue()
			require.True(t, ok)
			assert.Equal(t, test.want, value)
		})
	}
}

// TestWithNormalizedDefaultKeepsAbsentDefaultAbsent 保证"库里根本没有默认值"这一
// 状态不会被这层伪造成"有默认值" —— 那会让 gorm 的 null↔default 分支彻底失灵。
func TestWithNormalizedDefaultKeepsAbsentDefaultAbsent(t *testing.T) {
	field := &schema.Field{DataType: schema.Bool, DefaultValue: "true", HasDefaultValue: true}
	got := withNormalizedDefault(field, columnWithDefault("", false))
	value, ok := got.DefaultValue()
	assert.False(t, ok)
	assert.Equal(t, "", value)
}

// TestColumnTypeWithDefaultForwardsEverythingElse 保证包装类型只换默认值:
// 其余方法(尤其是 ColumnType(),它的名字和内嵌字段撞过)必须透传原值。
func TestColumnTypeWithDefaultForwardsEverythingElse(t *testing.T) {
	original := &migrator.ColumnType{
		NameValue:         sql.NullString{String: "price_amount", Valid: true},
		DataTypeValue:     sql.NullString{String: "numeric", Valid: true},
		ColumnTypeValue:   sql.NullString{String: "numeric(10,6)", Valid: true},
		LengthValue:       sql.NullInt64{Int64: 10, Valid: true},
		NullableValue:     sql.NullBool{Bool: false, Valid: true},
		UniqueValue:       sql.NullBool{Bool: true, Valid: true},
		DefaultValueValue: sql.NullString{String: "0.000000", Valid: true},
	}
	wrapped := columnTypeWithDefault{columnTypeBase: original, defaultValue: "0"}

	assert.Equal(t, "price_amount", wrapped.Name())
	assert.Equal(t, "numeric", wrapped.DatabaseTypeName())
	columnType, ok := wrapped.ColumnType()
	assert.True(t, ok)
	assert.Equal(t, "numeric(10,6)", columnType)
	length, ok := wrapped.Length()
	assert.True(t, ok)
	assert.Equal(t, int64(10), length)
	nullable, ok := wrapped.Nullable()
	assert.True(t, ok)
	assert.False(t, nullable)
	unique, ok := wrapped.Unique()
	assert.True(t, ok)
	assert.True(t, unique)

	value, ok := wrapped.DefaultValue()
	assert.True(t, ok)
	assert.Equal(t, "0", value)
}

// TestStripPostgresDefaultCast 钉死 PG 默认值剥类型转换后缀的口径。
// 空串那一行是本包存在的直接理由:上游 v1.5.2 的正则在这一行上匹配不到,
// 于是 28 个默认值为空串的列每次启动都被判定成"默认值变了"。
func TestStripPostgresDefaultCast(t *testing.T) {
	tests := []struct {
		raw  string
		want string
	}{
		{raw: `''::character varying`, want: ``},
		{raw: `''::text`, want: ``},
		{raw: `'abc'::text`, want: `abc`},
		{raw: `'default'::character varying`, want: `default`},
		{raw: `0.000000`, want: `0.000000`},
		{raw: `true`, want: `true`},
		{raw: `100`, want: `100`},
	}
	for _, test := range tests {
		t.Run(test.raw, func(t *testing.T) {
			assert.Equal(t, test.want, stripPostgresDefaultCast(test.raw))
		})
	}
}
