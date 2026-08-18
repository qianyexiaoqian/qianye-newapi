package controller

import (
	"strings"
	"unicode/utf8"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/middleware"
	"github.com/QuantumNous/new-api/qianye/db"
	qymodel "github.com/QuantumNous/new-api/qianye/model"
	"github.com/QuantumNous/new-api/qianye/service/audit"

	"github.com/gin-gonic/gin"
	"gorm.io/gorm/clause"
)

// restricted_notice.go —— 受限账号公告:管理员可配的一段标题 + Markdown 正文,
// 只对**受限账号**下发,渲染在受限态的顶部说明条与落地页上。
//
// # 它取代了什么
//
// 受限态在此之前只有一条写死的说明(前端 i18n 键 qy_restricted_appeal_hint /
// qy_restricted_no_channel)。项目方原话:「增加一个针对禁用用户的展示公告的配置。
// 禁用用户登录后显示这个公告。」—— 不同站点要说的话本来就不一样:申诉走 QQ 群
// 还是邮箱、封禁政策链接在哪、客服在几点到几点。写死的那一条对每个站点都只有
// 一半是对的,而运营改不了它。
//
// # 为什么住在 qianye/controller 而不是某个业务模块
//
// 「受限账号」不是任何一个业务模块的概念,它是会话鉴权链上的一档身份
// (middleware/restricted_user.go)。管理员在上游用户管理页上把任何一个账号
// 置为 disabled 都会产生这个状态,与 violation / ticket 两个模块开没开完全无关。
// 把这段文案挂进 violation 模块的话,violation.enabled=false 的站点会得到
// 「受限状态照常发生,但解释它的那段话连配都配不了」—— 而那正是本仓反复出现的
// 「功能在,入口没了」。所以它与 /health、/version、/session-stats 同一档:
// 不属于任何模块,直接挂在 qianye/router.go 上,不受任何 feature flag 影响。
//
// # 存储
//
// 复用既有的 qy_settings(scope="restricted"),不新建表:三个标量凑不成一张表,
// 而 qy_settings 正是"运营可在管理端修改的配置项"这个概念的落点(见 qymodel.Setting)。
// 因此本文件不带任何迁移 —— 表在 qianye/model/tables.go 里早就建好了。
//
// # 消毒边界:后端一个字符都不改写
//
// 正文是 Markdown **源码**,与工单正文同一口径(见 modules/ticket/validate.go
// 的 acceptBody):不转义、不过滤标签、不做"安全的 HTML 子集"。净化只有一处 ——
// 前端 components/ui/markdown 的 marked → DOMPurify 那一步,而且这段内容必须走
// **untrusted 档**(与工单同一份白名单)。
//
// 这里的信任边界与站点公告(console_setting.announcements)不同,不能混用:
// 站点公告是"管理员写、管理员和用户都看",历史上走的是宽档(放行 form / input /
// style / 外链图片);这一段是"管理员写、**给正在申诉的用户看**",而受限用户恰恰
// 是最容易被一个假登录框骗走口令的人(他刚被封,正在找出口)。宽档下的
// `<form action="https://evil.example">请验证身份以解除限制</form>` 会渲染在
// 每一个受限用户的首屏上。所以复用工单那份显式白名单,不复用公告那份。
const (
	restrictedNoticeScope      = "restricted"
	restrictedNoticeKeyEnabled = "notice_enabled"
	restrictedNoticeKeyTitle   = "notice_title"
	restrictedNoticeKeyBody    = "notice_body"
)

// 长度上限,按 **rune** 计(与工单同一口径:MySQL 的字符计数与 Go 的 rune 一致,
// 前端 [...str].length 也是码点数,三边不会打架)。
//
// 上限的真正作用是挡住静默截断:qy_settings.v 是 TEXT,MySQL 上是 65535 **字节**,
// 超长在非严格模式下**静默截断**、在严格模式下报错 —— 本仓踩过这个坑(见 ticket
// 的 truncate 注释)。4000 个 rune 最坏情况(全 emoji,4 字节/码点)是 16000 字节,
// 标题 120 rune 最坏 480 字节,合起来离 65535 还有三倍余量,因此任何合法输入都
// 不可能触到列宽。超限一律 400 拒绝,**绝不截断**:一段被拦腰截断的申诉指引比
// 没有更糟,用户会照着半句话去做。
const (
	RestrictedNoticeTitleMaxRunes = 120
	RestrictedNoticeBodyMaxRunes  = 4000
)

// restrictedNotice 是解析后的公告。
//
// Enabled 的含义是**「现在真的要显示这段公告」**,不是「库里那个开关的值」:
// 开关为真但标题或正文为空时它一律为 false(见 normalize)。理由见 normalize。
type restrictedNotice struct {
	Enabled   bool   `json:"enabled"`
	Title     string `json:"title"`
	Body      string `json:"body"`
	UpdatedAt int64  `json:"updated_at"`
	UpdatedBy int    `json:"updated_by"`
}

// normalize 把「库里存了什么」收敛成「现在要不要显示、显示什么」。
//
// 开关为真却没有内容时判为关闭,而不是显示一个空公告 —— 这是本需求里唯一一条
// 不能违反的展示约束:公告关掉/没配/配坏了,受限用户看到的必须还是那条固定文案,
// **不能变成空白**。空白的后果是一个刚被封号的人打开控制台,看到一块什么都没写的
// 卡片,然后去发工单问"我这是怎么了" —— 而工单正是这段公告本来要替我们回答的东西。
//
// 写入侧已经挡过一次(enabled=true 时标题与正文必填),这里是第二道:qy_settings
// 是可以被人手工 UPDATE 的,而"有人把 notice_body 清空了"必须表现为回落固定文案,
// 不能表现为空白。
func (n restrictedNotice) normalize() restrictedNotice {
	n.Title = strings.TrimSpace(n.Title)
	n.Body = strings.TrimSpace(n.Body)
	if n.Title == "" || n.Body == "" {
		n.Enabled = false
	}
	return n
}

// loadRestrictedNotice 从扩展库读出当前公告。
//
// 读不到(扩展库不可用、表刚建、一行都没有)时返回零值 + error,由调用方决定
// 是回落固定文案(用户端)还是 503(管理端)。
func loadRestrictedNotice() (restrictedNotice, error) {
	gdb := db.Get()
	if gdb == nil {
		return restrictedNotice{}, db.ErrNotReady
	}
	var rows []qymodel.Setting
	if err := gdb.Where("scope = ?", restrictedNoticeScope).Find(&rows).Error; err != nil {
		db.MarkFailure(err)
		return restrictedNotice{}, err
	}

	out := restrictedNotice{}
	for _, row := range rows {
		switch row.K {
		case restrictedNoticeKeyEnabled:
			// 零值口径:这一行**不存在**与它的值是 "false" 含义相同 —— 都表示
			// "没开"。刻意写成 `== "true"` 而不是 `!= "false"`:滚动升级期间的
			// 空串、DBA 手工插的行、将来某天多出来的取值,一律按"没开"处理,
			// 因为这两个方向的失败代价不对称(误开 = 全体受限用户看到一段
			// 半成品文案;误关 = 回落到那条一直都在的固定文案)。
			out.Enabled = row.V == "true"
		case restrictedNoticeKeyTitle:
			out.Title = row.V
		case restrictedNoticeKeyBody:
			out.Body = row.V
		}
		if row.UpdatedAt > out.UpdatedAt {
			out.UpdatedAt = row.UpdatedAt
			out.UpdatedBy = row.OperatorId
		}
	}
	return out.normalize(), nil
}

// UserRestrictedNotice 下发受限账号公告。GET /api/qy/restricted-notice
//
// # 只有受限账号拿得到内容
//
// 判据是 middleware.IsRestrictedUser —— 会话鉴权链在放行一个受限账号时打的标记,
// 正常账号身上永远没有它。正常账号请求这条路径会拿到 200 + enabled:false + 空串,
// 而不是公告内容。
//
// 这一层不是可有可无的洁癖:前端在正常账号身上根本不渲染受限横幅,所以"漏给
// 正常用户"只可能通过接口本身发生 —— 而这段文案的形状恰恰是「你的账号已被限制」,
// 任何一次误发都会让一个没有任何问题的用户以为自己被封了,然后去发工单。
// 把判据做在服务端,前端就算写错也漏不出去。
//
// # 为什么不走 requireCore
//
// 与 AdminVersion / UserSessionStats 同一理由,但更强:扩展库不可用时这个端点
// 必须回 200 + enabled:false,让前端**回落到那条固定文案**。回 503 的表现是
// 受限落地页上多一块红色报错,而用户此刻要的是"我该去哪申诉",不是我们的
// 数据库状态。读失败与"没配置"在展示上是同一件事,那就让它们走同一条出口。
func UserRestrictedNotice(c *gin.Context) {
	if !middleware.IsRestrictedUser(c) {
		ok(c, restrictedNotice{})
		return
	}
	notice, err := loadRestrictedNotice()
	if err != nil {
		// 不告警:扩展库的可用性由 /health 与 db 包自己的熔断告警负责,
		// 在这里每个受限用户每次刷新都写一行只会把真信号淹掉。
		ok(c, restrictedNotice{})
		return
	}
	if !notice.Enabled {
		ok(c, restrictedNotice{})
		return
	}
	ok(c, notice)
}

// AdminGetRestrictedNotice 回读当前公告与两条长度上限。GET /api/qy/admin/restricted-notice
//
// 上限随内容一起下发,而不是让前端各写一份常量:前端那份一旦漂移,运营会在
// 一个说"还能再打 500 个字"的输入框里写完,然后被后端 400 顶回来。
func AdminGetRestrictedNotice(c *gin.Context) {
	if !requireCore(c) {
		return
	}
	notice, err := loadRestrictedNotice()
	if err != nil {
		serverError(c, err)
		return
	}
	ok(c, gin.H{
		"enabled":          notice.Enabled,
		"title":            notice.Title,
		"body":             notice.Body,
		"updated_at":       notice.UpdatedAt,
		"updated_by":       notice.UpdatedBy,
		"title_max_runes":  RestrictedNoticeTitleMaxRunes,
		"body_max_runes":   RestrictedNoticeBodyMaxRunes,
		"markdown_profile": "untrusted",
	})
}

// restrictedNoticeRequest 是公告写入的请求体。
//
// Enabled 用值类型而不是 *bool:这里没有"不传即保持原样"的语义 —— 表单三个字段
// 一起提交、一起落盘,允许部分更新只会造出"我明明关掉了,它还在显示"
// (前端漏传 enabled)这种没人能自查的状态。
type restrictedNoticeRequest struct {
	Enabled bool   `json:"enabled"`
	Title   string `json:"title"`
	Body    string `json:"body"`
}

// AdminPutRestrictedNotice 写入公告。PUT /api/qy/admin/restricted-notice
//
// 成功与失败共用同一条审计出口:被长度闸拒绝的那次同样要留痕 ——
// 「有人正在试图往每个受限用户的首屏塞一段 40KB 的文本」与成功的那次同等重要。
func AdminPutRestrictedNotice(c *gin.Context) {
	if !requireCore(c) {
		return
	}
	before, err := loadRestrictedNotice()
	if err != nil {
		serverError(c, err)
		return
	}

	var req restrictedNoticeRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		writeRestrictedNoticeAudit(c, before, restrictedNotice{}, "请求体解析失败: "+err.Error())
		badRequest(c, "qy_invalid_body", "请求体格式不正确")
		return
	}

	after := restrictedNotice{
		Enabled: req.Enabled,
		Title:   strings.TrimSpace(req.Title),
		Body:    strings.TrimSpace(req.Body),
	}
	if msg := validateRestrictedNotice(after); msg != "" {
		writeRestrictedNoticeAudit(c, before, after, msg)
		badRequest(c, "qy_invalid_notice", msg)
		return
	}

	gdb := db.Get()
	if gdb == nil {
		writeRestrictedNoticeAudit(c, before, after, db.ErrNotReady.Error())
		serverError(c, db.ErrNotReady)
		return
	}
	operator := c.GetInt("id")
	now := common.GetTimestamp()
	enabled := "false"
	if after.Enabled {
		enabled = "true"
	}
	rows := []qymodel.Setting{
		{Scope: restrictedNoticeScope, K: restrictedNoticeKeyEnabled, V: enabled, OperatorId: operator, UpdatedAt: now},
		{Scope: restrictedNoticeScope, K: restrictedNoticeKeyTitle, V: after.Title, OperatorId: operator, UpdatedAt: now},
		{Scope: restrictedNoticeScope, K: restrictedNoticeKeyBody, V: after.Body, OperatorId: operator, UpdatedAt: now},
	}
	// 三行一条语句写完。分三次写的话,中途失败会留下"开关已经打开、正文还是上一版"
	// 这种半成状态 —— 而它对外的表现是每个受限用户都读到一段过期的申诉指引。
	if err := gdb.Clauses(clause.OnConflict{
		Columns:   []clause.Column{{Name: "scope"}, {Name: "k"}},
		DoUpdates: clause.AssignmentColumns([]string{"v", "operator_id", "updated_at"}),
	}).Create(&rows).Error; err != nil {
		db.MarkFailure(err)
		writeRestrictedNoticeAudit(c, before, after, err.Error())
		serverError(c, err)
		return
	}

	after.UpdatedAt = now
	after.UpdatedBy = operator
	writeRestrictedNoticeAudit(c, before, after, "")
	ok(c, after.normalize())
}

// validateRestrictedNotice 返回空串表示通过,否则是给运营看的拒绝理由。
func validateRestrictedNotice(n restrictedNotice) string {
	if utf8.RuneCountInString(n.Title) > RestrictedNoticeTitleMaxRunes {
		return "公告标题过长"
	}
	if utf8.RuneCountInString(n.Body) > RestrictedNoticeBodyMaxRunes {
		return "公告正文过长"
	}
	// 开着却没内容 = 受限用户看到一块空白卡片。这是本需求唯一的硬约束,
	// 在写入侧就挡掉,而不是等读取侧的 normalize 兜底 —— 兜底会让运营
	// 保存成功、界面显示"已启用"、线上却仍是固定文案,又一处"以为改了其实没改"。
	if n.Enabled && (n.Title == "" || n.Body == "") {
		return "启用公告时标题与正文都必须填写"
	}
	return ""
}

// writeRestrictedNoticeAudit 落一条配置变更审计,成功与失败同一出口。
//
// 它登记在 qianye/audit_coverage_guard_test.go 的 auditWriteFuncs 里 ——
// 埋点没有调用者依赖,是重构里最容易被"顺手清掉"的死代码。
func writeRestrictedNoticeAudit(c *gin.Context, before, after restrictedNotice, failReason string) {
	change := audit.ConfigChange{
		Action: "restricted_notice.update",
		Before: before,
		After:  after,
	}
	if failReason != "" {
		change.Result = qymodel.ResultFail
		change.Reason = failReason
	}
	audit.WriteConfigUpdate(c, change)
}
