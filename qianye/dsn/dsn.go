// Package dsn 是"这段 DSN 是不是 SQLite"这条判据的**唯一**实现。
//
// # 为什么单独一个包
//
// 这条判据有两个读者,而它们不能互相 import:qianye/db 的 DetectDialect
// (决定挂哪个驱动)与 qianye/config 的 validateDatabase(决定拒不拒绝),
// 后者被前者 import,所以判据放在任何一边都会形成环,只能各写一份。
//
// 各写一份的代价已经出现过一次:两边都只认前缀(sqlite: / file: / local),
// 于是最自然的一种写法 —— 照抄上游 SQLITE_PATH 的相对路径 `./data/qy_ext.db`
// —— 三条都不匹配,又因为含 '/' 通过了"不像 MySQL DSN(缺少库名)"那一关,
// 最后落到 mysql 驱动手里。运维拿到的是一句 go-sql-driver 的网络地址解析错
// (`default addr for network './data' unknown`):里面没有 SQLite、没有行锁、
// 没有"不支持",而 validateDatabase 刻意写下的那整段"为什么不支持"一个字
// 都没送到。
//
// 提成一个叶子包之后,两个读者共用同一份判据,漂移这一类问题不再存在。
package dsn

import "strings"

// IsSQLite 判断一段 DSN 是否指向 SQLite。
//
// # SQLite 为什么必须被拒
//
// 扩展库承载资金 —— 佣金账本、两阶段资金单、提现、抽奖出款 —— 这些路径靠
// SELECT ... FOR UPDATE 的行锁串行化读改写。SQLite 没有行锁,LockForUpdate
// 只能退化成空操作;扩展的多节点租约与迁移互斥又假定多个进程共享同一个库,
// 那正是 SQLite 最不擅长的形态。所以这不是"没写适配",而是语义不成立。
func IsSQLite(dsn string) bool {
	lower := strings.ToLower(strings.TrimSpace(dsn))
	switch {
	case strings.HasPrefix(lower, "sqlite:"), strings.HasPrefix(lower, "file:"):
		return true
	case lower == "local", strings.HasPrefix(lower, "local "):
		return true
	default:
		return LooksLikeFilePath(lower)
	}
}

// LooksLikeFilePath 判断一段(已转小写的)DSN 是不是"裸文件路径"形态。
//
// # 判据为什么这么紧
//
// 误伤一个能用的 MySQL DSN(把它拒掉)比漏判更贵,所以要求三件事同时成立:
// 不含 '@'(MySQL DSN 的用户名分隔符)、不含 "tcp("(MySQL 的网络形式),
// 并且要么以路径记号开头(./ ../ / ~/ 或 Windows 盘符),要么以 SQLite
// 常见扩展名结尾(允许后面挂 ?参数,例如 ./data/x.db?_busy_timeout=30000)。
func LooksLikeFilePath(lower string) bool {
	s := strings.TrimSpace(lower)
	if s == "" || strings.Contains(s, "@") || strings.Contains(s, "tcp(") {
		return false
	}
	path := s
	if i := strings.IndexByte(path, '?'); i >= 0 {
		path = path[:i]
	}
	for _, ext := range []string{".db", ".sqlite", ".sqlite3"} {
		if strings.HasSuffix(path, ext) {
			return true
		}
	}
	for _, pfx := range []string{"./", "../", "/", "~/", ".\\", "..\\"} {
		if strings.HasPrefix(s, pfx) {
			return true
		}
	}
	// Windows 盘符:c:/... 或 c:\...(0x5c 是反斜杠)
	if len(s) >= 3 && s[1] == ':' && (s[2] == '/' || s[2] == 0x5c) &&
		s[0] >= 'a' && s[0] <= 'z' {
		return true
	}
	return false
}
