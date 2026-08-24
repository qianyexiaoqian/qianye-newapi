// Package httpq 是 qianye 扩展**唯一**的 HTTP 查询参数解析实现。
//
// # 为什么必须只有一份
//
// 这套解析曾经在仓库里有七份拷贝(controller、availability、violation、
// commission、grouppricing、transfer、withdraw),全部从第一份复制而来,
// 随后各自漂移:
//
//   - 只有两份有整数上界,另外五份是裸 strconv.Atoi;
//   - 只有两份有页码上界,另外五份只夹住页长 —— 而只夹页长挡不住深翻页;
//   - 同一个 ?p=184467440737095518 在七份拷贝里有四种不同的结果。
//
// 后果不是风格问题。transfer 与 withdraw 是资金模块的用户端只读接口:
// 没有页码上界时 (page-1)*size 会整数溢出成负数,喂进 GORM 的 Offset()
// 轻则 SQL 报错 500,重则拿到非预期的结果集。availability 更直接 ——
// 溢出后的负数下标让 names[start:end] 直接 panic。
//
// # 为什么 offset 也在这里
//
// 危险的从来不是 page 与 size 这两个中间变量,而是最终打给数据库的
// (page-1)*size。只要还有一个调用点自己写这段算术,它就可能在拿到一个
// 没夹过的 page 时溢出。所以 Offset 也收进本包,调用方没有任何理由再自己写。
//
// qianye/httpq_guard_test.go 用 AST 把"只有这一份"钉死。
package httpq

import (
	"math"
	"strings"

	"github.com/gin-gonic/gin"
)

const (
	// MaxPage 是页码硬上界。
	//
	// 页码上限与页长上限缺一不可,它们挡的是两个不同的攻击面:
	// 页长上限挡"一次拉爆内存",页码上限挡深翻页。10000 页 × 100 条
	// 已经是 OFFSET 100 万,MySQL 会老老实实扫完再全部丢掉;
	// 再往上就是一条 URL 压住整个管理端数据库。
	MaxPage = 10000

	// MaxQueryInt 是通用整数查询参数的上界。
	//
	// 取 int32 上界而不是更小的值,是因为这些参数里有 user_id 和 Unix 时间戳:
	// 上界收得太紧会让筛选条件**静默失效** —— 解析回落到默认值 0,调用方的
	// `if v := Int(...); v > 0` 不成立,WHERE 根本没拼上去,接口返回的是
	// 未经筛选的全表,而前端只会觉得"筛选没生效"。
	//
	// 这不是假设:被收敛掉的 controller 那份 intQuery 上界是 100 万,而它
	// 同时被 start_ts / end_ts 复用 —— 任何真实 Unix 时间戳都大于 100 万,
	// 资金单与审计日志的时间范围筛选一直是死的。
	MaxQueryInt = math.MaxInt32

	defaultPageKey  = "p"
	defaultSizeKey  = "page_size"
	fallbackSize    = 20
	fallbackMaxSize = 100
)

// Spec 描述一个列表接口的分页口径。
//
// 零值就是扩展里最常见的那一套:?p= / ?page_size=,默认每页 20 条、上限 100 条。
// 只有确实不同的接口才需要显式给字段 —— availability 的看板用 ?page=,
// 默认 50、上限 200。这些差异是前端在依赖的,收敛时逐个核对过,一个都没动。
type Spec struct {
	PageKey     string // 页码参数名,空则 "p"
	SizeKey     string // 页长参数名,空则 "page_size"
	DefaultSize int    // 缺省页长,<=0 则 20
	MaxSize     int    // 页长上限,<=0 则 100
}

// Paginate 解析分页参数,返回值恒满足 1 <= page <= MaxPage 且 1 <= size <= MaxSize。
//
// 页长越界(> MaxSize)回落默认值而不是夹到上限:被收敛的七份拷贝里六份
// 是这个口径,少数派那一份(controller)的 URL 只有管理端在发,且前端
// 从不发大于 100 的页长。
func Paginate(c *gin.Context, spec Spec) (page, size int) {
	pageKey := spec.PageKey
	if pageKey == "" {
		pageKey = defaultPageKey
	}
	sizeKey := spec.SizeKey
	if sizeKey == "" {
		sizeKey = defaultSizeKey
	}
	maxSize := spec.MaxSize
	if maxSize <= 0 {
		maxSize = fallbackMaxSize
	}
	defSize := spec.DefaultSize
	if defSize <= 0 {
		defSize = fallbackSize
	}
	if defSize > maxSize {
		defSize = maxSize
	}

	page = Int(c, pageKey, 1)
	if page < 1 {
		page = 1
	}
	if page > MaxPage {
		page = MaxPage
	}

	size = Int(c, sizeKey, defSize)
	if size < 1 || size > maxSize {
		size = defSize
	}
	return page, size
}

// Offset 把页码与页长换算成 SQL OFFSET,是 (page-1)*size 在整个扩展里的唯一实现。
func Offset(page, size int) int {
	if page < 1 || size < 1 {
		return 0
	}
	if page > MaxPage {
		page = MaxPage
	}
	// 先升到 int64 再相乘:Paginate 出来的 page/size 不可能溢出,但 Offset 是
	// 导出函数,收的是裸整数,而 32 位构建上 int 只有 32 位。
	off := int64(page-1) * int64(size)
	if off < 0 {
		return 0
	}
	if off > MaxQueryInt {
		return MaxQueryInt
	}
	return int(off)
}

// Slice 按页切出 items 的一段。
//
// 这里的判断不是 Paginate 的冗余:切片下标越界是 panic 而不是错误返回,
// 防线必须贴着 items[start:end] 那一行放。Slice 收的是裸整数,
// 下一个调用方未必先调过 Paginate。
func Slice[T any](items []T, page, size int) []T {
	if page < 1 || size < 1 {
		return []T{}
	}
	start := Offset(page, size)
	if start >= len(items) {
		return []T{}
	}
	end := start + size
	// end < start 只可能来自加法溢出(调用方给了一个没夹过的 size)。
	if end > len(items) || end < start {
		end = len(items)
	}
	return items[start:end]
}

// Int 解析一个非负十进制整数查询参数。
//
// 缺失、非纯数字、负号、超过 MaxQueryInt 一律回落 def。刻意不做截断:
// 截断会把"用户要第 2^40 页"悄悄变成"给你第 10000 页",而回落到默认值
// 至少是调用方本来就要处理的那个取值。页码的截断由 Paginate 单独负责,
// 因为只有它知道 MaxPage 这个更紧的上界。
func Int(c *gin.Context, key string, def int) int {
	v, ok := parseDigits(c.Query(key), MaxQueryInt)
	if !ok {
		return def
	}
	return int(v)
}

// Int64 解析一个非负十进制 int64 查询参数(时间戳、金额下限一类)。
func Int64(c *gin.Context, key string, def int64) int64 {
	v, ok := parseDigits(c.Query(key), math.MaxInt64)
	if !ok {
		return def
	}
	return v
}

// PathInt64 解析路径参数里的正整数 ID(/:id、/:userId 一类),第二个返回值表示是否合法。
//
// 语义与它要取代的那四份私有拷贝(violation / grouppricing / transfer / withdraw
// 各一份 `strconv.ParseInt(c.Param(key), 10, 64)` + `<= 0` 判断)逐条对齐:
// 非数字、负号、前导加号、超过 MaxInt64、以及 0 一律返回 false。
// 差别只在上界是解析的一部分,而不是解析完再补救。
func PathInt64(c *gin.Context, key string) (int64, bool) {
	v, ok := parseDigits(c.Param(key), math.MaxInt64)
	if !ok || v <= 0 {
		return 0, false
	}
	return v, true
}

// parseDigits 只接受纯十进制数字串,并且在超过 max 的那一位上就失败,
// 而不是先算出一个回绕过的数再去补救。
//
// 不用 strconv.Atoi 的理由就是上界:Atoi 的上界是 MaxInt64,
// 184467440737095518 能被它成功解析 —— 而这正是四份拷贝里页码溢出的来源。
// 上界必须是解析的一部分。
func parseDigits(s string, max int64) (int64, bool) {
	if s == "" {
		return 0, false
	}
	var n int64
	for i := 0; i < len(s); i++ {
		ch := s[i]
		if ch < '0' || ch > '9' {
			return 0, false
		}
		d := int64(ch - '0')
		if n > (max-d)/10 {
			return 0, false
		}
		n = n*10 + d
	}
	return n, true
}

// SearchLike 把一段运营输入的关键词编译成一条**跨库口径一致**的 LIKE 条件。
//
// 返回的 expr 里每一个 %s 位置由调用方给出列名,pattern 是已经转义好的模式串。
// 用法:
//
//	expr, pattern := httpq.SearchLike(kw, httpq.MatchPrefix, "username", "email")
//	q = q.Where(expr, pattern, pattern)
//
// # 为什么必须折叠大小写
//
// 裸 `col LIKE ?` 在三种数据库上给出**三种**结果:MySQL 的 utf8mb4_*_ci 排序
// 规则大小写不敏感,SQLite 的 LIKE 对 ASCII 天然不敏感,而 PostgreSQL 的 LIKE
// **大小写敏感**。于是同一个关键词在 PG 部署上搜不到、在 MySQL 上搜得到,
// 两边都返回 200。运营在返佣页搜 `QY-Alice` 得到"查无此人",而这一页上有
// 余额调整、佣金冲正、绑定/解绑上下线的按钮 —— 搜不到的直接后果是他改去猜 id,
// 或者认定账号不存在。上游 model/user.go 的 SearchUsers 早就写成
// LOWER(col) LIKE ?,并且有 model/search_case_pg_test.go 专门盯着;
// qianye 侧这几处是同一约定的漏改点。
//
// LOWER() 三种数据库都有,且对 ASCII 语义一致。代价是用不上 col 上的普通索引 ——
// 与"同一个词在两种部署上给出不同答案"相比,这个代价必须付;真需要索引的站点
// 可以自己建 LOWER(col) 的表达式索引(PG)或生成列索引(MySQL 8)。
//
// # 为什么转义符显式写成 '!'
//
// `%` 与 `_` 是 LIKE 的通配符。不转义的话运营输入的 `%` 会命中全表、
// 单号里的 `_` 会当成"任意单字符"命中一批不相关的行,而他会以为"就这些"。
// 默认转义符是反斜杠,但反斜杠在 MySQL 的 NO_BACKSLASH_ESCAPES 与
// PostgreSQL 的 standard_conforming_strings 下含义不同,只有显式 ESCAPE
// 才在三种数据库上一致。
func SearchLike(keyword string, mode LikeMatch, cols ...string) (string, string) {
	kw := strings.ToLower(strings.TrimSpace(keyword))
	pattern := likeEscaper.Replace(kw) + "%"
	if mode == MatchContains {
		pattern = "%" + pattern
	}
	parts := make([]string, 0, len(cols))
	for _, col := range cols {
		parts = append(parts, "LOWER("+col+") LIKE ? ESCAPE '!'")
	}
	return strings.Join(parts, " OR "), pattern
}

// LikeMatch 选择前缀匹配还是子串匹配。
//
// 前缀是默认:`%kw%` 用不上索引,而运营是边打字边查的。子串留给"单号/标题里
// 找一段"这种确实需要的场景。
type LikeMatch int

const (
	// MatchPrefix 只匹配开头。
	MatchPrefix LikeMatch = iota
	// MatchContains 匹配任意位置。
	MatchContains
)

var likeEscaper = strings.NewReplacer("!", "!!", "%", "!%", "_", "!_")
