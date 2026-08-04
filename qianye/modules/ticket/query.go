package ticket

import (
	"errors"
	"strings"

	"github.com/QuantumNous/new-api/qianye/db"
	"github.com/QuantumNous/new-api/qianye/httpq"
	qymodel "github.com/QuantumNous/new-api/qianye/model"

	"github.com/gin-gonic/gin"
	"gorm.io/gorm"
)

// listPaging 是本模块所有列表接口的分页口径:?p= / ?page_size=,默认 20、上限 100。
//
// 解析、上界与 offset 换算全部在 qianye/httpq —— 这套逻辑曾经在仓库里有七份
// 各自漂移的拷贝,收敛的理由见该包的包注释。绝不在这里自己写 strconv.Atoi。
var listPaging = httpq.Spec{}

// loadUserTicket 按 id 取一张**属于该用户**的工单。
//
// 越权判定写进 WHERE,不是取回来再比:`Take` 之后再 `if t.UserId != userId`
// 也能挡住,但那种写法一旦被重构成"先查再复用"就会漏,而漏掉的方向是
// 任意用户读任意工单。条件在 SQL 里,重构不掉。
//
// 只在**内部**寻址时用(比如已经从附件行拿到 ticket_id)。用户端的 HTTP 路径
// 一律走 loadUserTicketByNo —— 见那个函数的注释。
func loadUserTicket(userId int, id int64) (*Ticket, error) {
	var t Ticket
	err := db.Get().Where("id = ? AND user_id = ?", id, userId).Take(&t).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, errNotFound
	}
	if err != nil {
		db.MarkFailure(err)
		return nil, err
	}
	return &t, nil
}

// loadUserTicketByNo 按**业务单号**取一张属于该用户的工单。
//
// 用户端的三条寻址路径(详情 / 追加回复 / 关单)全部走这里,而不是自增 id:
// toUserView 刻意不下发 id(下发等于把主键空间交给客户端,也让"这个站点一共
// 有多少张工单"变成任何人都能读的数)。曾经的写法是路由收 :id、视图又把 id
// 抹成 0,于是用户点开自己的工单只会拿到 400 —— 详情、追加、关单三条路一起断,
// 而管理端(保留真实 id)完全正常,自测时极易漏掉。
//
// 归属判定同样写进 WHERE:单号是 TK + 时间戳 + crypto/rand,不可枚举,
// 但"不可枚举"从来不是授权。
func loadUserTicketByNo(userId int, no string) (*Ticket, error) {
	var t Ticket
	err := db.Get().Where("ticket_no = ? AND user_id = ?", no, userId).Take(&t).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, errNotFound
	}
	if err != nil {
		db.MarkFailure(err)
		return nil, err
	}
	return &t, nil
}

// loadTicket 按 id 取一张工单(管理端,无归属限制)。
func loadTicket(id int64) (*Ticket, error) {
	var t Ticket
	err := db.Get().Where("id = ?", id).Take(&t).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, errNotFound
	}
	if err != nil {
		db.MarkFailure(err)
		return nil, err
	}
	return &t, nil
}

// loadUserMessages 取一张工单里**对用户可见**的消息。
//
// `internal = ?` 写在 WHERE 里而不是取回来再过滤:这是内部备注唯一的边界。
// 取全再过滤的写法会让"忘了过滤"和"过滤器写错"都表现为把客服的内部判断
// 原样发给被投诉的那个用户,而任何测试都不会因此变红(接口照常 200)。
//
// 用 commonFalseVal 语义的裸 false:GORM 会按方言绑定参数,三种数据库通吃。
func loadUserMessages(ticketId int64) ([]Message, error) {
	rows := make([]Message, 0, 16)
	err := db.Get().Where("ticket_id = ? AND internal = ?", ticketId, false).
		Order("id asc").Find(&rows).Error
	if err != nil {
		db.MarkFailure(err)
		return nil, err
	}
	return rows, nil
}

// loadAllMessages 取一张工单的全部消息(管理端,含内部备注)。
func loadAllMessages(ticketId int64) ([]Message, error) {
	rows := make([]Message, 0, 16)
	err := db.Get().Where("ticket_id = ?", ticketId).Order("id asc").Find(&rows).Error
	if err != nil {
		db.MarkFailure(err)
		return nil, err
	}
	return rows, nil
}

// applyStatusFilter 支持逗号分隔的多状态筛选。
// 只接受已知状态,拒绝把任意字符串拼进 SQL 的可能。
func applyStatusFilter(q *gorm.DB, raw string) *gorm.DB {
	wanted := pickKnown(raw, knownStatus)
	if len(wanted) == 0 {
		return q
	}
	return q.Where("status IN ?", wanted)
}

// applyPriorityFilter 同上,针对等级。
func applyPriorityFilter(q *gorm.DB, raw string) *gorm.DB {
	wanted := pickKnown(raw, knownPriority)
	if len(wanted) == 0 {
		return q
	}
	return q.Where("priority IN ?", wanted)
}

// pickKnown 从逗号分隔的取值里挑出白名单内的那些。
//
// 全部非法时返回空切片,调用方据此**不加条件** —— 而不是返回一个空的 IN ()
// (那在三种数据库上的行为并不一致,MySQL 甚至是语法错误)。
func pickKnown(raw string, ok func(string) bool) []string {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return nil
	}
	out := make([]string, 0, 4)
	for _, s := range strings.Split(raw, ",") {
		s = strings.TrimSpace(s)
		if ok(s) {
			out = append(out, s)
		}
	}
	return out
}

// pathId 解析路径里的工单 id(管理端)。
func pathId(c *gin.Context) (int64, bool) { return httpq.PathInt64(c, "id") }

// pathTicketNo 解析路径里的业务单号(用户端)。
//
// 只做形状检查(非空、不超过列宽),不校验前缀与长度:单号的生成规则将来可能变,
// 而一个"看起来不像单号"的字符串走到 WHERE 里的结果本来就是 404。
func pathTicketNo(c *gin.Context) (string, bool) {
	no := strings.TrimSpace(c.Param("no"))
	if no == "" || len(no) > 64 {
		return "", false
	}
	return no, true
}

// actorOf 从 gin context 取操作者身份。
//
// 两个 key 都是上游认证中间件写进去的(middleware/auth.go),这里不重新解析
// token —— 多一处身份解析就多一处可能与认证中间件的判定不一致的地方。
func actorOf(c *gin.Context) (int, string) {
	return c.GetInt("id"), c.GetString("username")
}

// actorTypeOf 返回审计用的操作者类型。管理端路由恒为 admin。
func actorTypeOf(isAdmin bool) string {
	if isAdmin {
		return qymodel.ActorAdmin
	}
	return qymodel.ActorUser
}
