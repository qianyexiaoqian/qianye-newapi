package model

import "strings"

// 幂等键里**客户端可控的那一段**的规范化。
//
// # 为什么必须有这一层
//
// 幂等键住在 varchar 列上,而"两个键相不相等"最终是**数据库的排序规则**说了算,
// 不是 Go 说了算。而这个仓库支持的三种方言在这件事上不一致:
//
//   - MySQL   列继承库默认排序规则。8.0 是 utf8mb4_0900_ai_ci、5.7 是
//     utf8mb4_general_ci —— 两者都**大小写不敏感**,8.0 还**重音不敏感**。
//   - PostgreSQL / SQLite  varchar 按字节比较,大小写敏感。
//
// 代码里从来没有指定过 collation(qianye/db/db.go 只往 DSN 补 charset=utf8mb4),
// 所以同一份二进制在两种官方支持的部署上对**同一对资金请求给出相反的答案**:
// 先发 client_request_id="abc" 再发 "ABC"(业务上是两笔全新购买),
// MySQL 上第二笔被当成重放**静默吞掉**(HTTP 200、返回上一张票的回执、实扣 0),
// PostgreSQL 上正常出票并扣款。实测两方言逐字复现过。
//
// # 为什么是"折叠"而不是"拒绝"或"改哈希"
//
// 折叠到小写之后,三方言一律按 MySQL 的语义走 —— 也就是**生产那一支的语义
// 一个字节都不变**(MySQL 的 CI 排序规则本来就已经把这两个键当成同一个,
// 存量行在 CI 比较下仍然命中),而 PostgreSQL / SQLite 跟上来。
// 换成哈希会改变落库格式、让存量行全部失配;换成"拒绝大写"会让一个大写 UUID
// 这种完全合理的客户端取值吃 400。折叠是这三条里唯一不产生回归的。
//
// 字符集同时收紧到 [A-Za-z0-9_-]:重音不敏感这一维**折叠救不了**
// ('café' 与 'cafe' 在 MySQL 上仍是同一个键),只能靠不让非 ASCII 进来。
// 这也正是 transfer 的 buildIdemKey 早就在做的事,这里把同一条规则挪到
// 一处定义,让抽奖与提现两条路也跟上 —— 它们此前只挡长度。
//
// 返回 ok=false 表示这一段不能当幂等键用,调用方按各自的参数错误处理。
func NormalizeIdemClientKey(raw string) (string, bool) {
	id := strings.TrimSpace(raw)
	if id == "" {
		return "", false
	}
	var b strings.Builder
	b.Grow(len(id))
	for _, r := range id {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9', r == '-', r == '_':
			b.WriteRune(r)
		case r >= 'A' && r <= 'Z':
			b.WriteRune(r - 'A' + 'a')
		default:
			return "", false
		}
	}
	return b.String(), true
}

// IsCollationNeutralIdemKey 断言一个**完整**的幂等键在三方言上比较结果一致。
//
// 判据:只含 [a-z0-9_:.#-]。这一集合里没有任何一对字符会被大小写不敏感或
// 重音不敏感的排序规则判成相等,所以 utf8mb4_0900_ai_ci、utf8mb4_general_ci、
// utf8mb4_bin 与 PostgreSQL/SQLite 的按字节比较对它给出同一个答案。
//
// 服务端自己生成的前缀(TR/LS/LT/LE… 单号)刻意**不**参与这条判据 ——
// 它们是定形的,不会因为大小写产生第二种写法。这个函数只用来给测试当判据,
// 断言经过 NormalizeIdemClientKey 之后客户端那一段确实落在安全集合里。
func IsCollationNeutralIdemKey(key string) bool {
	for _, r := range key {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9',
			r == '-', r == '_', r == ':', r == '.', r == '#':
		default:
			return false
		}
	}
	return key != ""
}
