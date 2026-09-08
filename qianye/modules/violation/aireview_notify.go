package violation

import (
	"context"
	"fmt"
	"html"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/qianye/guard"
	"github.com/QuantumNous/new-api/service"
)

// aireview_notify.go —— 「AI 审核判出违规就给用户发一封邮件」。
//
// 项目方原话:「审核渠道这里,加一个是否发送邮件给用户,增加一个邮件模板,邮件支持 html」。
//
// ═══════════════════ 三条边界,顺序就是它们被检查的顺序 ═══════════════════
//
//	① 只有 AI 审核这条路会发         开关挂在审核渠道上,本地词表/正则规则没有渠道。
//	② 影子命中恒不发                 影子的契约是「不扣费,不封号,不记录违规次数」,
//	                                 而一封"你违规了"的邮件是执行里最外露的一种。
//	③ 每小时每人有条数上限           复用 service.CheckNotificationLimit —— 一个
//	                                 写脚本的人能在一分钟里撞几百次违规,而每一次
//	                                 都从站点的 SMTP 账号发一封信出去,后果是发件
//	                                 域被拉黑,不是"用户收到太多邮件"。
//
// 发件本身走 common.SendEmail:它的信头恒是 `Content-Type: text/html`,所以
// "支持 HTML" 在传输层已经成立,这里要做的只是**别把模板转义掉**。

const (
	// maxViolationEmailSubjectRunes / maxViolationEmailBodyRunes 是模板的长度闸。
	// 标题按 RFC 5322 的实践上限留余量(它还要经 Base64 编码进信头);
	// 正文这一格是一封邮件,不是一个页面 —— 上限存在的意义是挡住"把整站 HTML
	// 粘进来"这种用法,而不是精确控制字节数。
	maxViolationEmailSubjectRunes = 120
	maxViolationEmailBodyRunes    = 20000

	// violationNotifyKind 是这类通知在 service.CheckNotificationLimit 里的桶名。
	// 独立一个桶而不是复用 dto.NotifyType*:与额度告警共用计数会让"额度快用完了"
	// 这种用户真正需要的提醒,被一串违规邮件挤掉。
	violationNotifyKind = "qy_violation"
)

// defaultViolationEmailSubject / defaultViolationEmailBody 是两格留空时用的内置模板。
//
// 内置那一份刻意**不**写站点名之外的任何品牌信息,也不放外链:它会被发给一个
// 刚刚被拦下来的人,而这类邮件最容易被当成钓鱼。信息给全(什么时候、哪个模型、
// 判成哪一类、还差几次处置),剩下的交给运营自己改模板。
const defaultViolationEmailSubject = `【{{site_name}}】你的一次请求被安全策略拦下`

const defaultViolationEmailBody = `<div style="font-family:-apple-system,BlinkMacSystemFont,'Segoe UI',Roboto,'Helvetica Neue',Arial,sans-serif;font-size:14px;line-height:1.7;color:#1f2328;max-width:560px">
  <p>{{username}} 你好,</p>
  <p>你在 <strong>{{time}}</strong> 的一次请求被本站的内容安全策略判定为违规,已被拦截。</p>
  <table cellpadding="6" cellspacing="0" style="border-collapse:collapse;margin:16px 0;font-size:13px">
    <tr><td style="color:#59636e">违规类型</td><td><strong>{{category}}</strong></td></tr>
    <tr><td style="color:#59636e">使用模型</td><td>{{model}}</td></tr>
    <tr><td style="color:#59636e">判定说明</td><td>{{reason}}</td></tr>
    <tr><td style="color:#59636e">当前计数</td><td>{{hit_count}} / {{threshold}}(还差 {{remaining}} 次触发处置)</td></tr>
    <tr><td style="color:#59636e">请求编号</td><td><code>{{request_id}}</code></td></tr>
  </table>
  <p>请在后续使用中避开此类内容。若你认为这次判定有误,可以登录站点,在「我的违规记录」里对这条记录提交申诉。</p>
  <p style="color:#59636e;font-size:12px">这封邮件由系统自动发出,请勿直接回复。</p>
</div>`

// emailNotice 是一次命中要发的那封邮件的模板,在**命中当时**从审核渠道抄下来。
//
// 抄一份而不是在落库时回查快照:落库跑在异步 worker 上,期间管理员完全可以
// 改掉甚至删掉那个渠道,而按新配置去发一封几秒前那次命中的邮件是错的 ——
// 与 persistRecord 里"分组取记录里冻结的那一个"是同一条口径。
type emailNotice struct {
	Subject string
	Body    string
}

// aiChannelEmailNotice 取某个审核渠道配的邮件模板,没开开关或渠道找不到时返回 nil。
//
// 与 aiChannelBlockMessage 并排:两者都是"这一次是谁判的"那一端的信息,
// 都走快照、都在渠道刚被删掉时安静地退化成"不做这件事"。
func aiChannelEmailNotice(rt *aiRuntime, channelId int64) *emailNotice {
	ch := rt.channelById(channelId)
	if ch == nil || !ch.NotifyEmail {
		return nil
	}
	return &emailNotice{Subject: ch.EmailSubject, Body: ch.EmailBody}
}

// violationEmailVars 是模板里那些 {{占位符}} 的取值。
//
// 字段全是字符串:渲染只做一次字面替换,而"计数未知"这一档必须能表达成 "—"
// —— 用 int 的话它只能是 0,而 0 与"这条命中根本没计数"是两回事。
type violationEmailVars struct {
	SiteName  string
	Username  string
	UserId    string
	Time      string
	Model     string
	Group     string
	Category  string
	Reason    string
	RuleName  string
	Blocked   string
	Message   string
	RequestId string

	HitCount  string
	Threshold string
	Remaining string
	Banned    string
}

// violationEmailVarKeys 是模板可用的全部占位符,界面上那张说明表也读它。
//
// 导出成一个有序切片而不是让前端自己抄一份:抄本会在下一次加占位符时过期,
// 而过期的表现是运营照着界面写了一个永远不会被替换的 {{xxx}}。
var violationEmailVarKeys = []string{
	"site_name", "username", "user_id", "time", "model", "group",
	"category", "reason", "rule", "blocked", "block_message", "request_id",
	"hit_count", "threshold", "remaining", "banned",
}

func (v violationEmailVars) pairs() map[string]string {
	return map[string]string{
		"site_name": v.SiteName, "username": v.Username, "user_id": v.UserId,
		"time": v.Time, "model": v.Model, "group": v.Group,
		"category": v.Category, "reason": v.Reason, "rule": v.RuleName,
		"blocked": v.Blocked, "block_message": v.Message, "request_id": v.RequestId,
		"hit_count": v.HitCount, "threshold": v.Threshold,
		"remaining": v.Remaining, "banned": v.Banned,
	}
}

// renderViolationEmail 把模板渲染成一封可以直接发的邮件。
//
// ═══════════════ 转义的方向:模板可信,值不可信 ═══════════════
//
// 模板是管理员在后台写的 HTML,**原样输出**——那正是"邮件支持 html"这句话的
// 全部含义。而替换进去的值里有用户名、模型名、审核模型给的判定理由,
// 它们全部来自站外:不转义的话,一个把用户名改成 `<img src=x onerror=...>`
// 的人就能往本站发出的每一封违规邮件里注入内容。
//
// 标题不转义、只去掉换行:它进的是信头,HTML 实体在那里会被原样显示成
// `&lt;`,而换行是信头注入的载体。
func renderViolationEmail(tmplSubject, tmplBody string, vars violationEmailVars) (subject, body string) {
	subjectTmpl := strings.TrimSpace(tmplSubject)
	if subjectTmpl == "" {
		subjectTmpl = defaultViolationEmailSubject
	}
	bodyTmpl := strings.TrimSpace(tmplBody)
	if bodyTmpl == "" {
		bodyTmpl = defaultViolationEmailBody
	}
	pairs := vars.pairs()
	subject = substituteViolationVars(subjectTmpl, pairs, false)
	subject = strings.NewReplacer("\r", " ", "\n", " ").Replace(subject)
	return subject, substituteViolationVars(bodyTmpl, pairs, true)
}

// substituteViolationVars 做 {{key}} 的字面替换。escape 为真时值先过 HTML 转义。
//
// 刻意不用 text/template:那一套会把模板里的 `{{if}}`、`{{range}}` 变成可用语法,
// 于是后台的一个文本框变成了一门语言的入口 —— 而它渲染的是要发给用户的邮件,
// 语法错误只会在发件那一刻变成一封空信。逐键 Replace 的能力边界一眼看得到底:
// 认得的键被换掉,不认得的 {{xxx}} 原样留着(运营立刻看得出自己写错了)。
func substituteViolationVars(tmpl string, pairs map[string]string, escape bool) string {
	out := tmpl
	for _, key := range violationEmailVarKeys {
		value := pairs[key]
		if value == "" {
			// 取不到值时渲染成 "—" 而不是留空。留空的表现是收件人看到
			//   <tr><td>判定说明</td><td></td></tr>
			// 这样一个空格子 —— 它和"模板坏了"长得一模一样,而这封信是发给一个
			// 刚被拦下、正打算判断"这是不是钓鱼"的人的。四个计数占位符早就是这么
			// 做的(violationEmailUnknown),这里只是把同一条口径铺到全部键上。
			//
			// 2026-09-07 端到端实跑时撞到:AI 规则没填「对用户公示的理由」,于是
			// {{reason}} 那一行在真发出去的信里是空的。
			value = violationEmailUnknown
		}
		if escape {
			value = html.EscapeString(value)
		}
		out = strings.ReplaceAll(out, "{{"+key+"}}", value)
	}
	return out
}

// notifyViolationEmail 是发件那一步的调度入口,由 persistRecord 在两个出口调用。
//
// st 为 nil 表示这次命中没有推进计数(影子除外——影子根本走不到这里),
// 四个计数占位符渲染成 "—"。这一档必须存在:一条"只拦不计数"的规则同样会
// 拦下用户,而他一样需要知道自己被拦了。
//
// 整段跑在 guard.HotAsync 上:SMTP 是一次跨网络的同步调用,几百毫秒到几秒,
// 而调用方是落库 worker —— 在那里等一次 SMTP 握手会把违规记录的写入拖垮。
func notifyViolationEmail(rec *Record, st *counterState) {
	task, ok := violationEmailTaskOf(rec, st)
	if !ok {
		return
	}
	// ctx 在这一段没有去处:SMTP 与取邮箱两步都是同步 API,拿不到取消信号。
	// 收下它只是为了满足 HotAsync 的签名。
	guard.HotAsync("violation.notify_email", func(context.Context) error {
		return sendViolationEmail(task)
	})
}

// violationEmailTask 是一次发件作业:发给谁、用哪份模板、填哪些值。
type violationEmailTask struct {
	UserId int
	Notice emailNotice
	Vars   violationEmailVars
}

// violationEmailTaskOf 回答「这次命中要不要发邮件、发什么」,三道闸全在这里。
//
// 单独一个函数不是为了缩短 notifyViolationEmail,而是因为那三道闸是这个功能
// 最重要的不变量,而它们**只能在这里被直接测到**:上面那层是 guard.HotAsync,
// 测试环境里扩展库不可用、队列作业根本不会执行,断言会永远为真。
// 这与 persistRecord 从 persist 里被提出来是同一条理由。
//
//	notice == nil   判这次命中的渠道没开这个开关(本地规则也永远走这一条)
//	Shadow          影子模式的契约里没有"给用户发信"这一项
//	UserId <= 0     取不到收件人,后面每一步都是白跑
func violationEmailTaskOf(rec *Record, st *counterState) (violationEmailTask, bool) {
	if rec == nil || rec.notice == nil || rec.Shadow || rec.UserId <= 0 {
		return violationEmailTask{}, false
	}
	return violationEmailTask{
		UserId: rec.UserId,
		Notice: *rec.notice,
		Vars:   violationEmailVarsOf(rec, st),
	}, true
}

// sendViolationEmail 是发件本体:取收件地址 → 过条数闸 → 渲染 → 发。
//
// 每一步失败都只告警不重试。这封邮件是**通知**,不是账本:重试会让一次 SMTP
// 抖动变成一串重复的信,而收件人对同一次违规收到三封的困惑,比没收到更大。
func sendViolationEmail(task violationEmailTask) error {
	email, err := model.GetUserEmail(task.UserId)
	if err != nil {
		return fmt.Errorf("取用户邮箱失败: %w", err)
	}
	if strings.TrimSpace(email) == "" {
		// 没绑邮箱不是错误:本站允许纯用户名注册。
		return nil
	}
	allowed, err := service.CheckNotificationLimit(task.UserId, violationNotifyKind)
	if err != nil {
		return fmt.Errorf("违规通知条数闸检查失败: %w", err)
	}
	if !allowed {
		return nil
	}
	subject, body := renderViolationEmail(task.Notice.Subject, task.Notice.Body, task.Vars)
	if err := common.SendEmail(subject, email, body); err != nil {
		return fmt.Errorf("违规通知邮件发送失败: %w", err)
	}
	return nil
}

// violationEmailVarsOf 把记录与计数状态摊成模板取值。
func violationEmailVarsOf(rec *Record, st *counterState) violationEmailVars {
	vars := violationEmailVars{
		SiteName:  common.SystemName,
		Username:  rec.Username,
		UserId:    strconv.Itoa(rec.UserId),
		Time:      time.Unix(rec.CreatedAt, 0).Format("2006-01-02 15:04:05"),
		Model:     rec.ModelName,
		Group:     rec.UsingGroup,
		Category:  rec.CategoryName,
		Reason:    rec.PublicReason,
		RuleName:  rec.RuleName,
		Blocked:   violationEmailBool(rec.Blocked),
		Message:   rec.BlockedReason,
		RequestId: rec.RequestId,
		// 未计数那一档的四个 "—":理由见 notifyViolationEmail 的注释。
		HitCount:  violationEmailUnknown,
		Threshold: violationEmailUnknown,
		Remaining: violationEmailUnknown,
		Banned:    violationEmailBool(false),
	}
	// 用户看得到的"类型"应当是公示标题;没公示的类型退回内部名,总好过空白。
	if rec.CategoryPublicTitle != "" {
		vars.Category = rec.CategoryPublicTitle
	}
	if st == nil {
		return vars
	}
	hit, threshold := nearestViolationLine(*st)
	vars.HitCount = strconv.Itoa(hit)
	if threshold <= 0 {
		// 两条线都没设阈值 = 这次命中只记录、不会累积到处置。
		vars.Threshold = violationEmailUnlimited
		vars.Remaining = violationEmailUnlimited
	} else {
		remaining := threshold - hit
		if remaining < 0 {
			remaining = 0
		}
		vars.Threshold = strconv.Itoa(threshold)
		vars.Remaining = strconv.Itoa(remaining)
	}
	banned, _ := anyReached(*st)
	vars.Banned = violationEmailBool(banned)
	return vars
}

const (
	violationEmailUnknown   = "—"
	violationEmailUnlimited = "不限"
)

func violationEmailBool(v bool) string {
	if v {
		return "是"
	}
	return "否"
}

// nearestViolationLine 挑出"离处置最近的那条线"的 (当前次数, 阈值)。
//
// 与用户端公示的 nearestThresholdLine 同一条口径:处置由 anyReached 判定,
// 语义是 OR,所以用户真正会被处置的时点由**最先到达的那条线**决定。只报账号
// 总量线的话,"账号线 10、这一类 3"这种再普通不过的配置会在邮件里写"还差 8 次",
// 而他下一次命中就被封了。
//
// 与那个函数分开写是因为可用的输入不同:这里手上只有本次命中所属的那**一个**
// 类型(counterState.Category),而公示页要扫全部类型。共用一个函数就得在这里
// 造一份只有一个元素的类型表,那比两句 if 更难读。
func nearestViolationLine(st counterState) (hit, threshold int) {
	account, accountOn := st.HitCount, st.Policy.Threshold > 0
	category, categoryOn := st.CatHitCount, st.Category.Enabled && st.Category.Threshold > 0
	switch {
	case accountOn && categoryOn:
		if st.Category.Threshold-category < st.Policy.Threshold-account {
			return category, st.Category.Threshold
		}
		return account, st.Policy.Threshold
	case categoryOn:
		return category, st.Category.Threshold
	case accountOn:
		return account, st.Policy.Threshold
	}
	// 一条线都没设阈值:次数仍然照实报,阈值报 0 让调用方渲染成"不限"。
	return account, 0
}

// validateViolationEmailTemplate 是两格模板的写入闸。
func validateViolationEmailTemplate(subject, body string) error {
	if n := utf8.RuneCountInString(subject); n > maxViolationEmailSubjectRunes {
		return fmt.Errorf("邮件标题过长(%d 字,上限 %d 字)—— 它要经 Base64 编码进邮件信头",
			n, maxViolationEmailSubjectRunes)
	}
	if n := utf8.RuneCountInString(body); n > maxViolationEmailBodyRunes {
		return fmt.Errorf("邮件正文过长(%d 字,上限 %d 字)", n, maxViolationEmailBodyRunes)
	}
	return nil
}
