// Package model 定义千夜扩展在独立数据库中的全部表。
//
// 约定:
//   - 所有表名以 qy_ 前缀硬编码在 TableName() 里,不使用 GORM NamingStrategy ——
//     两处都设会双重加前缀
//   - 时间戳一律 int64 unix 秒并手工赋值,不用 autoCreateTime/autoUpdateTime:
//     GORM 对 int64 时间字段的单位推断跨版本不稳定
//   - 跨库无外键。与主库 users/logs 的关联全是软引用,禁止加 constraint tag
//   - 金额精度全局统一:额度整数 bigint、额度余数 decimal(30,10)、
//     法币 decimal(18,6)、汇率 decimal(18,8)、比例用 bps 整数
package model

// AllTables 返回需要自动迁移的全部模型。
//
// bootstrap 调用它并传给 db.Migrate,以保持 db 包不依赖 model 包(避免循环依赖)。
// 各业务模块的表在此追加。
func AllTables() []any {
	return []any{
		// 地基
		&FundOrder{},
		&AuditLog{},
		&TaskLease{},
		&KV{},
		&Setting{},
	}
}
