// Package model 定义千夜扩展在独立数据库中的地基表。
//
// 各功能模块的表定义在自己的 qianye/modules/<name>/ 包里,通过模块注册表汇总,
// 不要往本包里堆 —— 那样每加一个功能都要改这里,并行开发会不断冲突。
//
// 全局约定:
//   - 表名以 qy_ 前缀硬编码在 TableName() 里,不使用 GORM NamingStrategy
//     (两处都设会双重加前缀)
//   - 时间戳一律 int64 unix 秒并手工赋值,不用 autoCreateTime/autoUpdateTime:
//     GORM 对 int64 时间字段的单位推断跨版本不稳定
//   - 跨库无外键。与主库 users/logs 的关联全是软引用,禁止加 constraint tag
//   - 金额精度全局统一:额度整数 bigint、额度余数 decimal(30,10)、
//     法币 decimal(18,6)、汇率 decimal(18,8)、比例用 bps 整数
package model

// FoundationTables 返回地基表。模块表由 module.All() 汇总,见 bootstrap.go。
func FoundationTables() []any {
	return []any{
		&FundOrder{},
		&AuditLog{},
		&TaskLease{},
		&KV{},
		&Setting{},
	}
}
