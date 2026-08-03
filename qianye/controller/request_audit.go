package controller

import (
	"strings"

	"github.com/QuantumNous/new-api/qianye/db"
	"github.com/QuantumNous/new-api/qianye/httpq"
	qymodel "github.com/QuantumNous/new-api/qianye/model"

	"github.com/gin-gonic/gin"
	"gorm.io/gorm"
)

// request_audit.go —— HTTP 请求台账(qy_request_audits)的只读查询。
//
// 与 audit-logs 分成两个接口而不是给一个接口加 type 参数:两张表的列不同、
// 筛选维度不同(这里要按 status_code / success / method 筛,那里要按
// trace_no / 金额 / result 筛),塞进一个接口只会得到一堆互相排斥的参数。

// likeEscape 是 LIKE 前缀匹配的转义字符。
//
// 刻意不用反斜杠:MySQL 会把字符串字面量里的 `'\'` 当成转义序列的开头,
// `ESCAPE '\'` 这段 SQL 在 MySQL 上直接是语法错误,而在 PostgreSQL/SQLite 上
// 没问题 —— 一个只在生产库(MySQL)炸、在测试库(SQLite)全绿的形状。
// `!` 在三种方言里都是普通字符。
const likeEscape = '!'

// escapeLikePrefix 把用户输入转义成安全的 LIKE 前缀模式。
//
// 必须转义 `_`:它在 LIKE 里是"任意单字符"通配符,而本项目的 action 里到处
// 是下划线(site_theme.update、group_rates.update)。不转义的话
// `site_theme` 会匹配到 `siteXtheme`,更要紧的是用户输入的 `%` 会变成
// 一个由调用方控制的通配符。
func escapeLikePrefix(v string) string {
	var b strings.Builder
	b.Grow(len(v) + 8)
	for _, r := range v {
		if r == likeEscape || r == '%' || r == '_' {
			b.WriteRune(likeEscape)
		}
		b.WriteRune(r)
	}
	b.WriteByte('%')
	return b.String()
}

// ApplyActionPrefix 给查询加上 action 的前缀匹配条件。
//
// # 为什么是前缀而不是精确匹配
//
// action 是分层命名的(withdraw.approve、withdraw.payee.create、
// admin.withdraw.approve.create),而排障时想问的问题永远是"提现这一块
// 都发生过什么",不是"恰好等于 withdraw.approve 的那些"。
// 精确匹配下,前端 placeholder 写着 `withdraw.approve` 诱导人输 `withdraw.`,
// 得到的是空列表 —— 而空列表看起来与"这段时间没有提现操作"完全一样。
//
// 导出并由两个列表接口共用:两张审计表的 action 是同一套命名体系,
// 匹配口径分成两份迟早会漂成"资金审计能按前缀查、请求台账不能"。
func ApplyActionPrefix(q *gorm.DB, action string) *gorm.DB {
	action = strings.TrimSpace(action)
	if action == "" {
		return q
	}
	return q.Where("action LIKE ? ESCAPE '!'", escapeLikePrefix(action))
}

// AdminListRequestAudits 分页查询 HTTP 请求台账。
func AdminListRequestAudits(c *gin.Context) {
	if !requireCore(c) {
		return
	}
	page, size := httpq.Paginate(c, listPaging)
	q := db.Get().Model(&qymodel.RequestAudit{})

	if v := c.Query("action"); v != "" {
		q = ApplyActionPrefix(q, v)
	}
	if v := c.Query("method"); v != "" {
		q = q.Where("method = ?", strings.ToUpper(v))
	}
	if v := c.Query("path"); v != "" {
		q = q.Where("path = ?", v)
	}
	if v := c.Query("actor_type"); v != "" {
		q = q.Where("actor_type = ?", v)
	}
	if v := c.Query("ip"); v != "" {
		q = q.Where("ip = ?", v)
	}
	if v := c.Query("request_id"); v != "" {
		q = q.Where("request_id = ?", v)
	}
	// success 三态:不传=全部,失败请求是这张表最有价值的切片
	// (越权探测与暴力枚举全是失败请求),因此它必须能被单独筛出来。
	switch c.Query("success") {
	case "true":
		q = q.Where("success = ?", true)
	case "false":
		q = q.Where("success = ?", false)
	}
	if v := httpq.Int(c, "status_code", 0); v > 0 {
		q = q.Where("status_code = ?", v)
	}
	if v := httpq.Int(c, "actor_user_id", 0); v > 0 {
		q = q.Where("actor_user_id = ?", v)
	}
	if v := httpq.Int(c, "target_user_id", 0); v > 0 {
		q = q.Where("target_user_id = ?", v)
	}
	if v := httpq.Int64(c, "start_ts", 0); v > 0 {
		q = q.Where("created_at >= ?", v)
	}
	if v := httpq.Int64(c, "end_ts", 0); v > 0 {
		q = q.Where("created_at <= ?", v)
	}

	var total int64
	if err := q.Count(&total).Error; err != nil {
		db.MarkFailure(err)
		serverError(c, err)
		return
	}
	// 下发给前端的数组一律显式初始化,理由见 qianye/json_array_guard_test.go。
	items := make([]qymodel.RequestAudit, 0, size)
	if err := q.Order("id desc").Offset(httpq.Offset(page, size)).Limit(size).Find(&items).Error; err != nil {
		db.MarkFailure(err)
		serverError(c, err)
		return
	}
	ok(c, gin.H{"items": items, "total": total, "p": page, "page_size": size})
}
