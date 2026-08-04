package ticket

import (
	"strings"
	"unicode/utf8"

	"github.com/QuantumNous/new-api/qianye/config"
)

// createRequest 是新建工单的请求体。
type createRequest struct {
	Title    string `json:"title"`
	Body     string `json:"body"`
	Priority string `json:"priority"`
	// AttachmentRefs 是先行上传拿到的图片标识。两步式(先传图拿 ref、再提交)
	// 是刻意的:提交那一步跑在一个要写三张表的事务里,把几 MiB 的图片解析、
	// 魔数校验与磁盘写入塞进去,等于让事务持有时间跟着用户的上行带宽走。
	AttachmentRefs []string `json:"attachment_refs"`
}

// replyRequest 是追加一条消息的请求体。
type replyRequest struct {
	Body           string   `json:"body"`
	AttachmentRefs []string `json:"attachment_refs"`
	// Internal 只在管理端有意义:内部备注不下发给用户。
	// 用户端的 handler 压根不读它(见 api_user.go),不是靠信任前端不传。
	Internal bool `json:"internal"`
}

// normalizedCreate 是校验通过之后的建单输入。
//
// 单独一个类型而不是把 createRequest 原地改干净:请求体是"客户端说了什么",
// 这个是"我们决定按什么办",两者混在一个结构里之后,后面每一处读到 Title
// 都要先回答"这是裁剪过的还是原始的"。
type normalizedCreate struct {
	Title    string
	Body     string
	Priority string
	Refs     []string
}

// acceptCreate 校验并归一化一次建单请求。
//
// 长度一律按 **rune** 计:MySQL 的 varchar(N) 按字符计,与 Go 的 rune 口径一致,
// emoji 都算 1。用 len() 会让一个满是中文的合法标题被判超长,而前端算的是
// 码点数 —— 两边口径不一致的表现是"前端说还能再打 20 个字,后端说超了"。
func acceptCreate(req createRequest) (normalizedCreate, error) {
	cfg := config.Get().Ticket
	out := normalizedCreate{}

	title := strings.TrimSpace(req.Title)
	if title == "" {
		return out, errTitleRequired
	}
	if utf8.RuneCountInString(title) > cfg.TitleMaxRunes {
		return out, errTitleTooLong
	}

	body, err := acceptBody(req.Body)
	if err != nil {
		return out, err
	}

	priority := strings.TrimSpace(req.Priority)
	if priority == "" {
		// 不填等于普通。刻意不报错:等级对用户是可选的表达,强制他选一个
		// 只会让所有人都选"紧急"。
		priority = PriorityNormal
	}
	if !knownPriority(priority) {
		return out, errPriorityInvalid
	}

	refs, err := acceptRefs(req.AttachmentRefs)
	if err != nil {
		return out, err
	}

	out.Title = title
	out.Body = body
	out.Priority = priority
	out.Refs = refs
	return out, nil
}

// acceptBody 校验一条消息正文。
//
// 正文是 Markdown **源码**,后端一个字符都不改写:不转义、不过滤标签、
// 不做"安全的 HTML 子集"。理由是净化必须只有一处 —— 前端渲染那一步
// (marked + DOMPurify)。后端再做一层的话,两层的白名单会各自漂移,
// 而"两处都以为对方会兜底"是 XSS 最常见的来源。
//
// 唯一的动作是 TrimSpace + 长度上限:前者让"只发了几个换行"被判成空,
// 后者是防滥用(正文是站点里唯一一条任何登录用户都能写入的长文本)。
func acceptBody(raw string) (string, error) {
	body := strings.TrimSpace(raw)
	if body == "" {
		return "", errBodyRequired
	}
	if utf8.RuneCountInString(body) > config.Get().Ticket.BodyMaxRunes {
		return "", errBodyTooLong
	}
	return body, nil
}

// acceptRefs 校验一条消息附带的图片标识列表。
//
// 去重在这里做,不在绑定那一步:同一个 ref 传两次的话,绑定是一条带条件的
// UPDATE,第二次会因为 bound 条件不再成立而报"图片不存在" —— 一个由客户端的
// 重复输入引起、却指向服务端状态的误导性错误。
func acceptRefs(raw []string) ([]string, error) {
	cfg := config.Get().Ticket
	if len(raw) == 0 {
		return nil, nil
	}
	if !cfg.ImageOn() {
		return nil, errImageDisabled
	}
	seen := make(map[string]bool, len(raw))
	out := make([]string, 0, len(raw))
	for _, ref := range raw {
		ref = strings.TrimSpace(ref)
		if ref == "" || seen[ref] {
			continue
		}
		seen[ref] = true
		out = append(out, ref)
	}
	if len(out) > cfg.ImageMaxPerMessage {
		return nil, errImageTooMany
	}
	return out, nil
}

// truncate 按 rune 截断,给入库前的兜底用。
//
// 校验器已经挡过一次,这里是第二道:快照姓名之类的值来自上游 context,
// 不受本模块的校验器管辖,而 MySQL 的 varchar 超长在严格模式下是**报错**、
// 非严格模式下是**静默截断** —— 两种都不该发生在一次工单回复上。
func truncate(s string, maxRunes int) string {
	if maxRunes <= 0 || utf8.RuneCountInString(s) <= maxRunes {
		return s
	}
	runes := []rune(s)
	return string(runes[:maxRunes])
}
