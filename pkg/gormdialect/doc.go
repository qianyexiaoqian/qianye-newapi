// Package gormdialect 给三个 GORM 数据库驱动各包一层**只影响 AutoMigrate 比较阶段**
// 的修正,让 migrateDB() 跑第二遍时一条 DDL 都不发。
//
// # 问题
//
// AutoMigrate 用 Migrator().ColumnTypes() 读回列的现状,再逐项和模型标签算出来的
// 期望值比对(gorm.io/gorm/migrator.Migrator.MigrateColumn)。任何一项比不相等,
// 它就发一条 DDL 把列"改"成期望的样子 —— 而如果不相等的原因是**两边写法不同**
// 而不是**真的不一样**,这条 DDL 改完之后现状仍然是老样子,下次启动再来一遍。
//
// 修之前实测(migrateDB 第二遍):
//
//	PostgreSQL 63 条 ALTER,横跨 20 张表,42 个列
//	SQLite     10 张表整张重建(SQLite 没有 ALTER COLUMN,驱动走
//	           CREATE t__temp → INSERT SELECT → DROP TABLE t → RENAME)
//	MySQL      3 条 ALTER TABLE … MODIFY COLUMN
//
// 三个方言的成因互不相同,详见各自文件顶部的注释:postgres.go / sqlite.go /
// mysql.go。defaultvalue.go 里是三者共用的一条规则:数据库把默认值**存成了
// 另一种写法**(0.000000 之于 0、1 之于 true)时,按值相等而不是按字符串相等判断。
//
// # 边界
//
// 三个包装器都只改"读回来的现状"这一侧,不碰任何 DDL 生成逻辑,因此建出来的列
// 类型与上游驱动逐字节一致;它们也互不影响 —— 每个只在对应方言被选中时构造。
// 归一化都是"把现状翻译成模型标签的写法",不是"把差异抹平":列长度、精度、
// 可空性一律原样透传,真实的结构变更仍然会被 gorm 检出并执行。
//
// # 什么时候可以删掉它
//
// 这些都是上游 gorm 生态的缺陷,部分在新版本里已修:
//
//	postgres 默认值的类型转换后缀   —— gorm.io/driver/postgres v1.5.3 已修
//	单列 uniqueIndex 的 field.Unique —— gorm.io/gorm v1.25.12 已改成 field.UniqueIndex
//	char(N) / bpchar                 —— 到 v1.6.2 仍未修
//	SQLite 复合唯一索引              —— glebarez/sqlite v1.9.0 仍未修
//
// 但那条升级路径不是本包能替代的:gorm 核心把唯一约束的迁移挪进了新的
// MigrateColumnUnique 之后,mysql 驱动 v1.4.3 必须一起升到 v1.5.7+,否则它报的
// "唯一索引也算 unique" 会让新核心在每次启动时 DROP 掉我们所有的唯一索引。
// 也就是说升级面是"核心 + 三个驱动一起动",而且升完 char(N) 与 SQLite 这两条
// 仍然要靠本包。等哪天做整体升级,请连同 model.TestMigrateDBIsIdempotent 一起
// 复核,能删的文件再删。
package gormdialect
