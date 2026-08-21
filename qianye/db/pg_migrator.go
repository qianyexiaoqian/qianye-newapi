package db

import (
	"regexp"
	"strings"

	"gorm.io/gorm"
	"gorm.io/gorm/migrator"
)

// 本文件解决一个只在 PostgreSQL 上出现的问题:**每次启动重发数百条空转 DDL**。
//
// # 现象
//
// 扩展库的 68 张表在 PostgreSQL 上建好之后,第二次 AutoMigrate 会再发
// 507 条 ALTER(实测,横跨 54 张表),而同一份模型在 MySQL 上是 0 条。
// 它们全部是空转:改完之后 schema 与改之前逐字节相同,下次启动照发不误。
//
// 代价与 MySQL 侧那 65 条完全一样,而且更重:每次启动在资金表上取
// ACCESS EXCLUSIVE 锁(PostgreSQL 的 ALTER COLUMN TYPE 会**重写整张表**,
// 不是 MySQL 8 的 INSTANT),把"扩展库迁移是空操作"这个判断彻底淹掉。
//
// # 三个根因(实测分类)
//
//  1. **231 条 SET DEFAULT** —— gorm.io/driver/postgres v1.5.2 把
//     information_schema.columns.column_default 原样取出,PostgreSQL 给的是
//     带类型转换的 `''::character varying`,而模型标签里是 `''`。
//     gorm 的 MigrateColumn 逐字比较这两个字符串,永远不相等 ⇒ alterColumn=true。
//
//  2. **244 条 ALTER COLUMN TYPE varchar(N) / char(N)** —— 上一条把
//     alterColumn 置真之后,驱动的 AlterColumn 会顺手把类型也一起发出来:
//     它比较的是 udt_name(`varchar`)与模型渲染出的 `varchar(64)`,
//     而 typeAliasMap 里没有 varchar 的条目,于是判定"类型也不一样"。
//     单独看它是良性的(长度本来就一致),但它是那 507 条里最贵的一半。
//
//  2b. 另有 8 条 ALTER COLUMN TYPE char(N) 来自同一个比对:PostgreSQL 报的
//     udt_name 是 bpchar,与模型渲染的 char(8)/char(64) 前缀怎么比都不等。
//     这一条**不在这里修**:定长 CHAR 列已经从模型里全部去掉(空串在两种方言上
//     读回来不一样,见 qianye/schema_crossdb_test.go),根因消失,再加一层
//     bpchar→char 别名就是一段永远走不到、也无法被测试杀死的防御代码。
//
//  3. **24 条 ADD CONSTRAINT ... UNIQUE** —— 驱动把"这一列上存在唯一索引"
//     报成 ColumnType.Unique()=true,而模型用的是 `uniqueIndex:` 标签
//     (field.Unique=false,唯一性由索引承担)。两者不等 ⇒ alterColumn=true,
//     并额外发一条 ADD CONSTRAINT。这一条不只是空转:约束名与既有索引同名,
//     真跑起来是"下一次启动直接报 already exists"的定时炸弹。
//
// # 为什么在这里修而不是升级驱动
//
// 驱动升级是另一条独立的决策(它同时影响主库),而这三处都不需要改驱动:
// GORM 允许换掉 Dialector 的 Migrator,而 ColumnTypes 返回的
// *migrator.ColumnType 的字段是导出的。在把列信息交给 gorm 的比较逻辑之前
// 做一次归一化,就把三个根因一起消掉,并且与驱动版本解耦 ——
// 将来驱动自己修好了,归一化会退化成无操作(没有 ::cast 可剥、Unique 本就没置位)。
//
// 归一化只作用于"要不要发 ALTER"这个判断,不改变建表、加列、建索引的任何行为。

// normalizedPGDialector 是 postgres.Dialector 的装饰器,只换掉 Migrator。
type normalizedPGDialector struct {
	gorm.Dialector
}

func (d normalizedPGDialector) Migrator(db *gorm.DB) gorm.Migrator {
	return normalizedPGMigrator{d.Dialector.Migrator(db)}
}

type normalizedPGMigrator struct {
	gorm.Migrator
}

// ColumnTypes 在驱动的结果之上做一次归一化。
//
// gorm 的 AutoMigrate 拿这份结果去和模型比对,决定是否 ALTER。
func (m normalizedPGMigrator) ColumnTypes(value any) ([]gorm.ColumnType, error) {
	cts, err := m.Migrator.ColumnTypes(value)
	if err != nil {
		return cts, err
	}
	for _, ct := range cts {
		mc, ok := ct.(*migrator.ColumnType)
		if !ok {
			continue
		}
		if mc.DefaultValueValue.Valid {
			mc.DefaultValueValue.String = normalizePGDefaultValue(mc.DefaultValueValue.String)
		}
		// 唯一性一律报"未知"(Valid=false),让 gorm 跳过这一项比较。
		//
		// 这与 MySQL 侧的现状一致(mysql 驱动同样不为 uniqueIndex 置这一位),
		// 因此"两种方言的 AutoMigrate 行为相同"这句话在这里是真的。
		// 代价是:给一个已存在的表的字段新加 `gorm:"unique"` 标签,
		// PostgreSQL 不会自动补建约束 —— 与 MySQL 完全一样,需要手工迁移。
		// 换来的是不再每次启动都往资金表上砸一条重名的 ADD CONSTRAINT。
		mc.UniqueValue.Valid = false
		mc.UniqueValue.Bool = false
	}
	return cts, nil
}

// pgDefaultCastSuffix 匹配 PostgreSQL 在 column_default 里附加的类型转换后缀。
//
// 形如 `<空串>::character varying`、`'gzip'::character varying`、`<空串>::bpchar`、
// `0::numeric`。类型名允许含空格(character varying / double precision)与
// 长度修饰(character varying(64)),所以不能只切到第一个空格。
var pgDefaultCastSuffix = regexp.MustCompile(`::[a-zA-Z_][a-zA-Z0-9_ ]*(\([0-9, ]*\))?$`)

// normalizePGDefaultValue 把 PostgreSQL 报出来的 column_default 折成
// GORM 存在 field.DefaultValue 里的那种形态。
//
// 两步,缺任何一步都比不相等:
//
//  1. **剥类型转换后缀**。PostgreSQL 报 `”::character varying`,标签里是 `”`。
//     只剥**末尾**的一次,且要求剩下的部分引号配平 —— 一个本身含 "::" 的
//     字符串常量(`'a::b'::character varying`)剥完是 `'a::b'`,再剥就切坏了。
//
//  2. **脱掉字符串字面量的单引号**。这一步不是可有可无的美化:GORM 在解析
//     `default:”` 标签时,对 String 类型的字段会执行 strings.Trim(v, "'"),
//     于是 field.DefaultValue 存的是**空串**而不是 `”`。
//     只做第 1 步的话比较的是 `”` vs `""`,依然不等 —— 那正是本轮实测里
//     231 条 SET DEFAULT 空转的直接原因。
//     这里刻意用与 GORM 同一个函数(Trim 而不是精确去一对引号),
//     不对称的写法会在带引号的默认值(`default:'gzip'`)上重新分叉。
//
// MySQL 侧不需要这一步:它的 information_schema 报的 column_default 本来
// 就是不带引号、不带转换的裸值,与 GORM 的存法天然一致。
func normalizePGDefaultValue(v string) string {
	trimmed := strings.TrimSpace(v)
	if loc := pgDefaultCastSuffix.FindStringIndex(trimmed); loc != nil {
		head := trimmed[:loc[0]]
		if strings.Count(head, "'")%2 == 0 {
			trimmed = head
		}
	}
	if len(trimmed) >= 2 && strings.HasPrefix(trimmed, "'") && strings.HasSuffix(trimmed, "'") {
		return strings.Trim(trimmed, "'")
	}
	return trimmed
}
