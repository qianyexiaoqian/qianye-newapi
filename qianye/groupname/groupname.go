// Package groupname 是千夜扩展里"分组名"这个键的唯一归一化口径。
//
// # 为什么要有这个包
//
// 扩展库里有多张以分组名为键的表(commission 的 qy_commission_group_rate、
// transfer 的 qy_transfer_group_rules),它们的列都是 varchar(64),而扩展库
// 固定是 MySQL:AutoMigrate 建表时列继承库默认排序规则,MySQL 5.7 是
// utf8mb4_general_ci、8.0 是 utf8mb4_0900_ai_ci —— **两者都大小写不敏感**。
// 而 Go 侧的查表(map 键、字符串 ==)是大小写敏感的精确匹配。
//
// 这个"代码敏感、存储不敏感"的中间态会静默出事:
//
//   - commission:给分组 VIP 配费率,ON DUPLICATE KEY 会命中已有的 vip 那一行,
//     而 group_name 不在 DoUpdates 里,于是那行仍叫 vip。管理端显示"VIP 已保存
//     50%",实际是 vip 的用户按 50% 返佣,审计日志指向一个库里不存在的分组名。
//   - transfer:一条 deny_all 规则挂在 VIP 上,而用户的 users.group 是 vip,
//     Go map 查不到 → 落到兜底规则 → 没有兜底就是完全不设防。一道资金闸门
//     可以这样静默失效。
//
// # 为什么选"代码跟上存储"(全链路折叠大小写),而不是"存储跟上代码"(COLLATE utf8mb4_bin)
//
// 两条路都能消掉中间态,选前者有三条理由:
//
//  1. **COLLATE 方案依赖一次必须跑到的 ALTER TABLE,而本扩展明确支持
//     auto_migrate=false 的部署**(由 DBA 手工建表)。那种部署下列的排序规则
//     永远不会被改,代码却已经按"存储是二进制比较"写好了注释 —— 缺陷原封不动
//     地留着,还多了一句声称已修复的注释。这正是本扩展反复栽的那个形状:
//     修复落在一条不保证执行的路径上。折叠大小写只依赖 Go 代码,进程起来就生效,
//     对已经建好的表、已经写进去的行同样立即生效。
//
//  2. **对资金闸门,折叠是 fail-closed 的那一侧。** deny_all 配在 vip 上时,
//     折叠让它同时覆盖 VIP/Vip/vip;二进制排序规则则让 VIP 逃出这条规则、
//     去撞兜底(没有兜底就是放行)。限制类规则宁可多盖一个,不能少盖一个。
//
//  3. **平台侧本来就没有"VIP 与 vip 是两个分组"这个能力。** 分组名的源头是
//     倍率配置的 map 键与 users.group,主库同样是 ci 排序规则的 varchar;
//     真让两个只差大小写的分组共存,只会让运维在任何一处配错都看不出来。
//     把它们折叠成一个,是把这个不可用的自由度显式关掉。
//
// # 残留边界(必须写下来,不能假装已经全清)
//
// 折叠只消掉了大小写这一维。MySQL 8.0 默认的 utf8mb4_0900_ai_ci 还是
// **重音不敏感**的:分组名 "café" 与 "cafe" 在存储层仍会撞唯一索引,而 Go 侧
// 认为它们不同。彻底消掉这一维仍然需要
//
//	ALTER TABLE <表> MODIFY COLUMN <列> varchar(64) COLLATE utf8mb4_bin NOT NULL;
//
// 建议 DBA 对上述两张表执行一次(从 ci/ai 转 bin 只会让唯一性变严,现存行在
// 旧排序规则下已经唯一,因此这条 ALTER 不可能报重复键)。在没执行之前,
// 只要不把分组命名成只差重音符号的两个词,判定与存储就是一致的。
//
// # 用法
//
//	Normalize  —— 比较键。去空白 + 折叠大小写,空串原样返回空串。
//	            写入侧用它,好让"分组名为空"仍然能被上层识别并拒绝。
//	Effective  —— 判定/计费口径。Normalize 之后把空串折叠成 Default。
//
// 两个模块共用同一份实现,而不是各写一份"看起来一样"的 TrimSpace:
// 同仓已经出现过三份各自演化的 normalizeGroup,其中出钱的那一份恰好是最宽松的。
package groupname

import "strings"

// Default 是主库 users.group 的列默认值。
//
// 历史行、以及被直接改过库的账号可能把 group 留成空串,而它们在业务上就是
// 默认分组的用户。不折叠的话,一条挂在 default 上的规则(限制或费率)对这批
// 账号完全不生效 —— 而它们恰恰是最可疑的那批。
const Default = "default"

// Normalize 把分组名归一成比较键:去掉两侧空白,折叠大小写。
//
// 空串原样返回空串:写入侧要靠这一点把"根本没填分组名"和"填了 default"
// 区分开,折叠在这里会让上层的空值校验静默失效。
func Normalize(g string) string {
	return strings.ToLower(strings.TrimSpace(g))
}

// Effective 是判定与计费用的分组口径:Normalize 之后空串折叠成 Default。
//
// 一切"这个用户按哪个分组算"的地方都必须走它,包括查规则表、比对名单、
// 以及冻结进流水的那个分组名 —— 冻结的值必须就是判定用的值,否则事后
// 拿着流水行复算会得出另一个结论。
func Effective(g string) string {
	if n := Normalize(g); n != "" {
		return n
	}
	return Default
}
