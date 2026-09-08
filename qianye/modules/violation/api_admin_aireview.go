package violation

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/qianye/db"
	"github.com/QuantumNous/new-api/qianye/guard"
	"github.com/QuantumNous/new-api/qianye/httpq"
	qymodel "github.com/QuantumNous/new-api/qianye/model"
	"github.com/QuantumNous/new-api/qianye/service/audit"

	"github.com/gin-gonic/gin"
	"github.com/shopspring/decimal"
	"gorm.io/gorm"
)

// AI 审核的管理端接口。
//
// # 这一页上的密钥永远是单向的
//
// 写:明文 api_key 从表单进来 → 立刻加密 → 只有密文入库。
// 读:接口下发的是 has_key + key_hint(尾 4 位),**没有任何一条路径能把明文取回**。
// 改:不传 api_key 字段 = 保持原密钥不变;传空串 = 显式清除。
//     这两者必须能分开,否则"改一下模型名"就会把密钥抹掉 —— 而抹掉之后
//     它不可恢复,运营得回去找第三方重新签发。

// ───────────────────────────── 渠道 ─────────────────────────────

// aiChannelWritableColumns 是渠道编辑接口**写回的列清单**。
//
// # 为什么是白名单而不是 Save/Updates 全字段
//
// 密钥那三列由 applyAIChannelKey 单独落库(它要加密、要算掩码、要记绑定地址),
// 全字段写回会拿 row 里的旧值把刚落好的密文再盖一遍;而"请求里没传 api_key"
// 那一档 row 上的密文是空的 —— 全字段写回等于每次编辑都静默清空密钥。
//
// # 加了新列必须同时加进这里
//
// 漏掉的表现**没有任何报错**:接口 200、响应体里那个字段是新值(它来自内存里的
// row),刷新之后变回旧值。2026-09-07 加通知邮件那三列时就漏了一次,而它是
// 在真站点上点了一次保存才被发现的。TestAIChannelWritableColumnsCoverEveryEditableField
// 从模型反射出列名与这份清单对账,把这一类遗漏钉在编译-测试期。
var aiChannelWritableColumns = []string{
	"name", "base_url", "model", "protocol", "risk_name", "group_name", "prompt",
	"block_message", "notify_email", "email_subject", "email_body",
	"guard_controversial", "guard_categories", "guard_elevate",
	"timeout_ms", "weight", "enabled",
	"price_in_per_m", "price_out_per_m", "remark", "updated_at", "updated_by",
}

// aiChannelUpsertReq 是渠道的新建/编辑入参。
//
// 单价走字符串:JSON number 在前端是 float64,0.1 往返一次会变成
// 0.10000000000000001,而它会被乘进成本统计(与规则里的金额字段同一条理由)。
type aiChannelUpsertReq struct {
	Name    string `json:"name"`
	BaseUrl string `json:"base_url"`
	Model   string `json:"model"`
	// ApiKey 是**指针**,这是本结构体唯一一处不得改成值类型的地方:
	//   nil   → 请求里没有这个字段 → 保持原密钥
	//   ""    → 请求里显式传了空串 → 清除密钥
	//   "sk-" → 换成新密钥
	// 值类型会把前两者折成同一个空串,于是每次编辑都会静默清掉密钥。
	ApiKey *string `json:"api_key"`
	// Protocol 空串 = json_prompt(提示词 + JSON),这是零值档也是出厂行为。
	// 见 aireview_guard.go。
	Protocol string `json:"protocol"`
	// RiskName 只在 protocol = granite_guardian 时有意义,空串 = harm。
	// Granite 一次只审一种风险,取值见 graniteRisks(aireview_granite.go)。
	RiskName string `json:"risk_name"`
	// GuardControversial 只在 protocol = qwen3guard 时有意义,空串 = safe。
	// 取值 safe / sensitive / unsafe,见 aireview_guard.go。
	GuardControversial string `json:"guard_controversial"`
	// GuardCategories 是启用的九类子集。**空数组 = 九类全启用**,不是"一个都不启用"
	// —— 零值方向的完整理由写在 AIChannel.GuardCategories 上。
	//
	// 数组而不是逗号串:前端画的是九个复选框,让它自己拼一次 CSV 只会多一处
	// 分隔符约定要两边对齐。落库时由 canonicalGuardCategoryCSV 归一。
	GuardCategories []string `json:"guard_categories"`
	// GuardElevate 是 sensitive 档下"命中即拦截"的敏感类别。空数组 = 参考实现的三类。
	GuardElevate []string `json:"guard_elevate"`
	// Group 是审核渠道分组;空串是合法值(= 未分组),不是"属于所有分组"。
	Group string `json:"group"`
	// Prompt 是这个渠道的审核提示词。空 = 用内置默认(defaultAIPrompt)。
	// 与渠道密钥那三态不同,它**不是**指针:表单每次整段提交,而空串是一个
	// 有意义的取值(回到内置默认),没有"不动它"这一档。
	Prompt string `json:"prompt"`
	// BlockMessage 是这个渠道判违规、规则又要拦截时返回给用户的那句话。
	// 空 = 不覆盖,沿用规则自己的那一份。
	BlockMessage string `json:"block_message"`
	// NotifyEmail 是"这个渠道判违规就给用户发邮件"的开关;两格模板留空 = 用内置默认。
	// 与 Prompt 同一档:表单整段提交,空串是有意义的取值(回到内置默认),
	// 没有"不动它"那一态,所以不是指针。
	NotifyEmail  bool   `json:"notify_email"`
	EmailSubject string `json:"email_subject"`
	EmailBody    string `json:"email_body"`
	TimeoutMs    int    `json:"timeout_ms"`
	Weight       int    `json:"weight"`
	Enabled      bool   `json:"enabled"`
	PriceInPerM  string `json:"price_in_per_m"`
	PriceOutPerM string `json:"price_out_per_m"`
	Remark       string `json:"remark"`
}

func (r *aiChannelUpsertReq) apply(dst *AIChannel) error {
	in, err := parseDecimal(r.PriceInPerM)
	if err != nil {
		return fmt.Errorf("输入单价不是合法数值: %q", r.PriceInPerM)
	}
	out, err := parseDecimal(r.PriceOutPerM)
	if err != nil {
		return fmt.Errorf("输出单价不是合法数值: %q", r.PriceOutPerM)
	}
	dst.Name = r.Name
	dst.BaseUrl = r.BaseUrl
	dst.Model = r.Model
	dst.Protocol = r.Protocol
	dst.Group = strings.TrimSpace(r.Group)
	// 提示词的归一放在这里(而不是各 handler 里):这是渠道唯一的写入路径,
	// 放这儿意味着以后新增任何一条写入路径都自动带上"逐字等于内置默认 → 存空串"。
	// 那条折叠是为了让站点跟随 defaultAIPrompt 的后续加固,完整理由见 aireview_prompt.go。
	dst.Prompt = normalizeAIPrompt(r.Prompt)
	dst.BlockMessage = strings.TrimSpace(r.BlockMessage)
	dst.NotifyEmail = r.NotifyEmail
	// 标题 Trim、正文只 TrimRight:HTML 模板的首行缩进是作者写的排版,
	// 而尾部空白纯属编辑器留下的。两格都留空即回落内置默认(见 renderViolationEmail)。
	dst.EmailSubject = strings.TrimSpace(r.EmailSubject)
	dst.EmailBody = strings.TrimRight(r.EmailBody, " \t\r\n")
	dst.RiskName = r.RiskName
	dst.GuardControversial = r.GuardControversial
	dst.GuardCategories = strings.Join(r.GuardCategories, ",")
	dst.GuardElevate = strings.Join(r.GuardElevate, ",")
	dst.TimeoutMs = r.TimeoutMs
	dst.Weight = r.Weight
	dst.Enabled = r.Enabled
	dst.PriceInPerM = in
	dst.PriceOutPerM = out
	dst.Remark = r.Remark
	return validateAIChannel(dst)
}

// aiChannelView 是下发给前端的形态。**它是白名单,不是 AIChannel 的别名** ——
// 直接下发行结构体的话,今天加一列密文明天就会跟着出去,而 json:"-" 是很容易
// 在下一次改动里被顺手删掉的一个 tag。这里逐字段列出来,加列不会自动泄漏。
type aiChannelView struct {
	Id      int64  `json:"id"`
	Name    string `json:"name"`
	BaseUrl string `json:"base_url"`
	Model   string `json:"model"`
	// Protocol 恒是归一后的取值(json_prompt / qwen3guard),**不下发空串**:
	// 前端拿空串去填一个下拉框会得到"未选择",而库里的空串含义是明确的
	// json_prompt。让界面显示"未选择"等于把一个确定的配置画成半配好的。
	Protocol string `json:"protocol"`
	// RiskName 在非 Granite 渠道上恒为空串(写入侧已清空)。**这里不做
	// 「空串下发成 harm」的补齐** —— 与 Protocol 那一格刻意相反:协议的空串
	// 是历史遗留、含义要靠归一才明确,而这一格的空串本身就是运营选的
	// "默认(harm)",把它画成显式选中的 harm 会让"没动过"与"选了 harm"
	// 在界面上无法区分,而这两者在审计差异页是不同的事。
	RiskName string `json:"risk_name"`
	// GuardControversial 在 json_prompt 渠道上恒为空串(写入侧已清空),
	// 前端据此决定要不要画那一格。
	GuardControversial string `json:"guard_controversial"`
	// GuardCategories / GuardElevate 恒是数组(可能为空),**永不为 null**:
	// 前端拿 null 去 .map 会白屏,而本仓已经为此栽过一次(nil_array_json_test.go)。
	GuardCategories []string `json:"guard_categories"`
	GuardElevate    []string `json:"guard_elevate"`
	HasKey          bool     `json:"has_key"`
	KeyHint         string   `json:"key_hint"`
	// KeyBoundElsewhere 为真 = 这个渠道的地址被改过,而已存密钥是写给旧地址的,
	// 于是它既不会被送到新地址,渠道也不会参与审核。**必须下发**:不下发的话
	// 界面上这一行看起来配得好好的(有密钥、启用中),而它实际上一次都不会被调用,
	// 而 AI 审核失败的方向是放行。下发的是一个布尔,不泄漏那个旧地址之外的任何东西。
	KeyBoundElsewhere bool `json:"key_bound_elsewhere"`
	// Group / Prompt / BlockMessage 原样回显:提示词从设置页搬来之后,
	// 渠道表单是它唯一的编辑入口,不回显就没法在原有基础上改。
	// 提示词可能有几千字,而这个接口本来就是"打开渠道表单"才调的。
	Group        string `json:"group"`
	Prompt       string `json:"prompt"`
	BlockMessage string `json:"block_message"`
	// NotifyEmail / EmailSubject / EmailBody 原样回显。两格模板可能有几千字,
	// 与 Prompt 同一条理由:渠道表单是它们唯一的编辑入口,不回显就没法在原有基础上改。
	NotifyEmail  bool   `json:"notify_email"`
	EmailSubject string `json:"email_subject"`
	EmailBody    string `json:"email_body"`
	// PromptSource 是界面上「默认 / 已自定义」那个标记的唯一来源。前端不能靠
	// "文本是不是空"自己判断:预填内置默认之后输入框永远非空,那样每个渠道
	// 看起来都是"已自定义"。见 aireview_prompt.go。
	PromptSource string `json:"prompt_source"`
	TimeoutMs    int    `json:"timeout_ms"`
	Weight       int    `json:"weight"`
	Enabled      bool   `json:"enabled"`
	PriceInPerM  string `json:"price_in_per_m"`
	PriceOutPerM string `json:"price_out_per_m"`
	Remark       string `json:"remark"`
	UpdatedAt    int64  `json:"updated_at"`
}

// splitGuardCategoryCSV 把库里那一列读成接口要下发的数组。
// 空串 → 空数组(不是 nil),理由见 aiChannelView.GuardCategories。
func splitGuardCategoryCSV(csv string) []string {
	out := make([]string, 0, len(guardAllCategories))
	if strings.TrimSpace(csv) == "" {
		return out
	}
	for _, part := range strings.Split(csv, ",") {
		if part = strings.TrimSpace(part); part != "" {
			out = append(out, part)
		}
	}
	return out
}

func toAIChannelView(ch AIChannel) aiChannelView {
	return aiChannelView{
		Id: ch.Id, Name: ch.Name, BaseUrl: ch.BaseUrl, Model: ch.Model,
		Protocol:           normalizeAIProtocol(ch.Protocol),
		Group:              ch.Group,
		Prompt:             ch.Prompt,
		BlockMessage:       ch.BlockMessage,
		NotifyEmail:        ch.NotifyEmail,
		EmailSubject:       ch.EmailSubject,
		EmailBody:          ch.EmailBody,
		PromptSource:       aiPromptSource(ch.Prompt),
		RiskName:           ch.RiskName,
		GuardControversial: ch.GuardControversial,
		GuardCategories:    splitGuardCategoryCSV(ch.GuardCategories),
		GuardElevate:       splitGuardCategoryCSV(ch.GuardElevate),
		HasKey:             ch.HasKey(), KeyHint: ch.KeyHint,
		KeyBoundElsewhere: ch.KeyBoundElsewhere(),
		TimeoutMs:         ch.TimeoutMs, Weight: ch.Weight, Enabled: ch.Enabled,
		PriceInPerM: ch.PriceInPerM.String(), PriceOutPerM: ch.PriceOutPerM.String(),
		Remark: ch.Remark, UpdatedAt: ch.UpdatedAt,
	}
}

func adminListAIChannels(c *gin.Context) {
	if !guard.RequireAPI(c, guard.FlagViolation) {
		return
	}
	var rows []AIChannel
	if err := db.Get().Order("id asc").Find(&rows).Error; err != nil {
		internalError(c, err)
		return
	}
	views := make([]aiChannelView, 0, len(rows))
	for _, r := range rows {
		views = append(views, toAIChannelView(r))
	}
	respond(c, gin.H{
		"items": views,
		// key_configured 让界面能在密钥配置缺失时给出**能照着做的**提示,
		// 而不是等运营点了保存才收到一个 400。
		"key_configured": aiKeyConfigured(),
		// guard_catalog 是护栏模型那 9 个固定类别与本站违规类型的对照表。
		//
		// 它必须由后端下发:前端硬编码一份的话,那一份与 guardCategoryKeys
		// 是两份必须手工保持一致的事实,而漏改的表现是界面上写着会落到 A、
		// 实际落到 B。名字与渠道上那一格(guard_categories = 启用子集)分开,
		// 两者是不同层级的东西,同名会让人以为改一个能影响另一个。
		"guard_catalog": aiGuardCategoryView(Snapshot().aiVocab),
		// guard_elevate_default 是 sensitive 档留空时真正生效的那三类。
		// 不下发它的话,界面上"留空 = 默认"是一句没人验证得了的话。
		"guard_elevate_default": guardDefaultElevated,
	})
}

// aiGuardCategoryView 把护栏模型的固定类别表 join 上本站的类型闭集。
//
// `present` 是这张表的全部价值:护栏模型的类别改不动(训练时钉死),所以
// "本站有没有对应类型"决定了这一类的判定会落到一个真类型上还是落进兜底。
// 只给映射不给这一位的话,界面会把一个必然落兜底的类别画得和落对了的一样,
// 而两者的差别正是运营要不要去新建一个类型。
func aiGuardCategoryView(v aiVocabulary) []gin.H {
	rows := guardCategoryMappings(v)
	out := make([]gin.H, 0, len(rows))
	for _, m := range rows {
		out = append(out, gin.H{
			// id 是复选框的 value(九类的 snake_case id),label 是官方展示名。
			"id":      m.Id,
			"label":   m.Label,
			"guard":   m.Label,
			"key":     m.Key,
			"present": m.Present,
		})
	}
	return out
}

func adminCreateAIChannel(c *gin.Context) {
	if !guard.RequireAPI(c, guard.FlagViolation) {
		return
	}
	var req aiChannelUpsertReq
	if err := c.ShouldBindJSON(&req); err != nil {
		badRequest(c, "请求体解析失败")
		return
	}
	row := AIChannel{CreatedAt: common.GetTimestamp()}
	if err := req.apply(&row); err != nil {
		writeAIReviewAudit(c, "ai_channel_create", qymodel.ResultFail, nil, &row, err)
		badRequest(c, err.Error())
		return
	}
	row.UpdatedAt = row.CreatedAt
	row.UpdatedBy = c.GetInt("id")

	gdb := db.Get()
	// 密钥在**拿到 id 之后**才能封:AAD 绑的是渠道 id(见 aiChannelAAD),
	// 而 id 要等 Create 之后才存在。所以先插一行不带密钥的,再回填 ——
	// 中间失败的后果是一条没有密钥的渠道,运营再填一次即可;
	// 反过来(先封再插)会得到一条永远解不开的密文,因为 AAD 里的 id 是猜的。
	if err := gdb.Create(&row).Error; err != nil {
		writeAIReviewAudit(c, "ai_channel_create", qymodel.ResultFail, nil, &row, err)
		internalError(c, err)
		return
	}
	if req.ApiKey != nil && strings.TrimSpace(*req.ApiKey) != "" {
		if err := applyAIChannelKey(gdb, &row, *req.ApiKey); err != nil {
			writeAIReviewAudit(c, "ai_channel_create", qymodel.ResultFail, nil, &row, err)
			badRequest(c, err.Error())
			return
		}
	}
	afterAIChange(c, "ai_channel_create", nil, &row, nil)
	respond(c, toAIChannelView(row))
}

func adminUpdateAIChannel(c *gin.Context) {
	if !guard.RequireAPI(c, guard.FlagViolation) {
		return
	}
	id, ok := pathInt64(c, "id")
	if !ok {
		badRequest(c, "id 非法")
		return
	}
	var req aiChannelUpsertReq
	if err := c.ShouldBindJSON(&req); err != nil {
		badRequest(c, "请求体解析失败")
		return
	}
	gdb := db.Get()
	var row AIChannel
	if err := gdb.Where("id = ?", id).Take(&row).Error; err != nil {
		notFound(c)
		return
	}
	before := row
	if err := req.apply(&row); err != nil {
		writeAIReviewAudit(c, "ai_channel_update", qymodel.ResultFail, &before, &row, err)
		badRequest(c, err.Error())
		return
	}
	row.UpdatedAt = common.GetTimestamp()
	row.UpdatedBy = c.GetInt("id")

	// ApiKey 为 nil = 表单没碰这一格 = 保持原密钥。见 aiChannelUpsertReq 的说明。
	//
	// **改地址就只是改地址**:这里不因为 base_url 变了而动密钥的任何一列。
	// 曾经有过一条「改地址即清空密钥」的规则,已撤回 —— 保存一个字段不该有副作用,
	// 而清空是不可恢复的。
	//
	// 那条规则堵的越权路径没有放着不管,只是换了一层:密钥被绑在它写入时的那个
	// 地址上(AIChannel.KeyEndpoint),地址与绑定不一致时这把密钥一律不出站。
	// 于是"改地址 → 试跑"这条链的第一步照样成功,第二步拿不到密钥。
	// 完整理由与攻击链写在 AIChannel.KeyEndpoint 上。
	if req.ApiKey != nil {
		if err := applyAIChannelKey(gdb, &row, *req.ApiKey); err != nil {
			writeAIReviewAudit(c, "ai_channel_update", qymodel.ResultFail, &before, &row, err)
			badRequest(c, err.Error())
			return
		}
	}
	if row.KeyBoundElsewhere() {
		// 只在日志里说一句,不改变这次保存的结果:保存成功了,只是这把密钥
		// 从现在起不会被送到新地址上。不说的话,运营会看到"渠道配得好好的、
		// 审核却不走它",而 AI 审核失败的方向是放行。
		common.SysLog(fmt.Sprintf(
			"qianye/violation: AI 审核渠道 %d 的地址由 %q 改为 %q,而已存密钥是写给 %q 的 —— "+
				"该密钥不会被发往新地址,渠道将被跳过,请在表单里重填一次密钥(操作人 %d)",
			row.Id, before.BaseUrl, row.BaseUrl, row.KeyEndpoint, c.GetInt("id")))
	}
	if err := gdb.Model(&AIChannel{}).Where("id = ?", row.Id).
		Select(aiChannelWritableColumns).
		Updates(&row).Error; err != nil {
		writeAIReviewAudit(c, "ai_channel_update", qymodel.ResultFail, &before, &row, err)
		internalError(c, err)
		return
	}
	afterAIChange(c, "ai_channel_update", &before, &row, nil)
	respond(c, toAIChannelView(row))
}

func adminDeleteAIChannel(c *gin.Context) {
	if !guard.RequireAPI(c, guard.FlagViolation) {
		return
	}
	id, ok := pathInt64(c, "id")
	if !ok {
		badRequest(c, "id 非法")
		return
	}
	gdb := db.Get()
	var row AIChannel
	if err := gdb.Where("id = ?", id).Take(&row).Error; err != nil {
		notFound(c)
		return
	}
	// 还有作用域策略指着它时直接拒绝。
	//
	// 删掉一个被指定的渠道,那几档从此每一次都走 no_channel —— 不审核、
	// 直接放行,而界面上它们看起来配得好好的。这是本模块最典型的静默失效形状,
	// 而它在这里可以被一次 400 完全挡住:运营要么先把那几档改回「不指定」,
	// 要么换一个渠道,两种都是一次显式的、有审计的动作。
	//
	// 不做成"删除时自动把那几档改回不指定":那等于替运营决定"发给谁都行",
	// 而指定渠道的理由往往正是"只能发给这几个"。
	//
	// 引用检查在 Go 里筛而不是写一句 WHERE:清单列是 CSV,而 CSV 的包含匹配
	// 在三家数据库上写法各异(`FIND_IN_SET` 是 MySQL 专有,LIKE '%,3,%' 会把
	// 13 当成 3),而本仓的跨库约束是硬的。策略表是个位数量级的行,读回来筛
	// 既准确又省一整套方言分支 —— 与 migrateAIChannelKeyEndpoint 同一条理由。
	var scopes []AIScope
	if err := gdb.Order("id asc").Limit(maxAIScopes * 4).Find(&scopes).Error; err != nil {
		internalError(c, err)
		return
	}
	names := make([]string, 0, 4)
	for _, sc := range scopes {
		for _, cid := range sc.ChannelIds {
			if cid == id {
				names = append(names, sc.Name)
				break
			}
		}
	}
	if len(names) > 0 {
		err := fmt.Errorf("渠道「%s」还被 %d 条 AI 审核作用域策略指定着(%s)—— "+
			"删掉它会让这几档少一个可用渠道,清单里只有它的那几档从此每次都走"+
			"「无可用渠道」并直接放行(不会回落到其它渠道)。"+
			"请先把它从这些策略的渠道清单里去掉",
			row.Name, len(names), strings.Join(names, "、"))
		writeAIReviewAudit(c, "ai_channel_delete", qymodel.ResultFail, &row, nil, err)
		badRequest(c, err.Error())
		return
	}
	// 硬删而不是软删:这一行的价值全部在密钥里,而密钥恰恰是最该真正消失的东西。
	// 历史审核明细(qy_violation_ai_review)冗余了 channel_name,删掉渠道不会
	// 让成本账目失去可读性 —— 那正是那一列冗余存在的理由。
	if err := gdb.Delete(&AIChannel{}, "id = ?", id).Error; err != nil {
		writeAIReviewAudit(c, "ai_channel_delete", qymodel.ResultFail, &row, nil, err)
		internalError(c, err)
		return
	}
	afterAIChange(c, "ai_channel_delete", &row, nil, nil)
	respond(c, gin.H{"deleted": true})
}

// applyAIChannelKey 加密并落库一个渠道密钥。plain 为空串表示清除。
//
// 单独一段而不是并进 Updates:密钥列(密文 + nonce + 版本 + 掩码)必须四列
// 一起写。分开写会产生"新密文配旧 nonce"这种解不开的组合,而它不会报错 ——
// 表现是那个渠道从此静默跳过,也就是"AI 审核悄悄少了一个渠道"。
func applyAIChannelKey(gdb *gorm.DB, row *AIChannel, plain string) error {
	plain = strings.TrimSpace(plain)
	if plain == "" {
		row.KeyNonce, row.KeyCipher, row.KeyVersion, row.KeyHint = nil, nil, 0, ""
		// 绑定跟着密钥一起清:没有密钥就没有可绑的东西,留着一个陈旧的地址
		// 只会在下一次写入前误导排障。
		row.KeyEndpoint = ""
	} else {
		if !aiKeyConfigured() {
			return fmt.Errorf("尚未配置 violation.ai_review_key,无法保存密钥 —— " +
				"密钥必须加密落库,本站绝不明文存储。请在 qianye 配置文件里填入 " +
				"`openssl rand -base64 32` 生成的 32 字节 base64 密钥后重启;" +
				"不需要鉴权的自建审核服务可以留空不填密钥")
		}
		nonce, cipher, version, err := sealAIKey(plain, aiChannelAAD(row.Id))
		if err != nil {
			return fmt.Errorf("密钥加密失败: %v", err)
		}
		row.KeyNonce, row.KeyCipher, row.KeyVersion = nonce, cipher, version
		row.KeyHint = maskAIKey(plain)
		// 绑定记的是**此刻**这一行的地址。更新时 row 已经带着新 base_url,
		// 所以"改地址的同时重填密钥"是一步到位的:运营明确表达了"这把密钥
		// 就是给新地址的",而他能重填就说明他知道那把密钥。
		row.KeyEndpoint = strings.TrimSpace(row.BaseUrl)
	}
	return gdb.Model(&AIChannel{}).Where("id = ?", row.Id).
		Select("key_nonce", "key_cipher", "key_version", "key_hint", "key_endpoint").
		Updates(map[string]any{
			"key_nonce": row.KeyNonce, "key_cipher": row.KeyCipher,
			"key_version": row.KeyVersion, "key_hint": row.KeyHint,
			"key_endpoint": row.KeyEndpoint,
		}).Error
}

// adminTestAIChannel 用一段**固定的良性文本**跑一次真实调用。
//
// 良性而不是违规样本:这个按钮回答的是"地址、密钥、模型名对不对、通不通",
// 不是"模型判得准不准"。拿违规样本去试,一个把它判成 clean 的模型会让运营
// 以为渠道坏了,而实际上它工作得好好的。
func adminTestAIChannel(c *gin.Context) {
	if !guard.RequireAPI(c, guard.FlagViolation) {
		return
	}
	id, ok := pathInt64(c, "id")
	if !ok {
		badRequest(c, "id 非法")
		return
	}
	var row AIChannel
	if err := db.Get().Where("id = ?", id).Take(&row).Error; err != nil {
		notFound(c)
		return
	}
	url := chatCompletionsURL(row.BaseUrl)
	if url == "" {
		badRequest(c, "地址为空")
		return
	}
	// 密钥绑在它写入时的那个地址上。地址被改过之后,这个按钮**不会**把已存密钥
	// 送到新地址 —— 那正是"改地址 + 点试跑"那条越权路径的第二步。
	// 完整理由与攻击链写在 AIChannel.KeyEndpoint 上。
	if row.KeyBoundElsewhere() {
		badRequest(c, fmt.Sprintf(
			"这个渠道的密钥是写给 %q 的,而当前地址是 %q。已存密钥不会被发往新地址 —— "+
				"密钥对本页是只写的,否则改一下地址再点试跑就能把它取走。"+
				"请在编辑表单里重填一次密钥后再试。",
			row.KeyEndpoint, row.BaseUrl))
		return
	}
	key, err := openAIKey(row.KeyNonce, row.KeyCipher, aiChannelAAD(row.Id), row.KeyVersion)
	if err != nil {
		respond(c, gin.H{"outcome": OutcomeNoChannel, "message": "渠道密钥无法解密:" + err.Error()})
		return
	}
	protocol := normalizeAIProtocol(row.Protocol)
	rt := &aiChannelRT{
		Id: row.Id, Name: row.Name, URL: url, Model: row.Model, APIKey: key,
		Protocol:  protocol,
		Guard:     guardPolicyFromChannel(row),
		RiskName:  normalizeGraniteRisk(row.RiskName),
		TimeoutMs: row.TimeoutMs, Weight: 1,
		PriceInPerM: row.PriceInPerM, PriceOutPerM: row.PriceOutPerM,
	}
	ctx, cancel := context.WithTimeout(c.Request.Context(), aiTestHardCeilingMs*time.Millisecond)
	defer cancel()
	timeout := aiTestTimeoutMs(protocol, row.TimeoutMs)
	// 连通性测试必须用**线上同一份**提示词与类型清单:用一份精简版试通了、
	// 线上那份因为类型清单太长被上游截断,是查不出来的。
	// (护栏协议下这一份根本不会被发出去,见 aiRequestPayload —— 照样渲染,
	// 是为了让这一段代码只有一条路径。)
	vocab := Snapshot().aiVocab
	// 提示词取**这个渠道自己**存的那一份(2026-09-06 起它住在渠道上)。
	// 用别处的一份试通了、线上发的是另一份,是查不出来的 —— 而这正是
	// 连通性试跑存在的意义。
	out := callAIChannel(ctx, rt, renderAIPrompt(row.Prompt, vocab),
		aiTestProbeText, timeout, vocab, true)
	body := gin.H{
		"protocol": protocol,
		"outcome":  out.Outcome,
		"violated": out.Violated,
		"category": out.Category,
		// 连通性测试上最值得看的一格:模型回了一个类型清单外的名字。
		// 那说明提示词与类型表脱节,而它在正常流量里只会表现为"计数落在未分类"。
		"raw_category": out.RawCategory,
		"confidence":   out.Confidence.String(),
		"reason":       out.Reason,
		"latency_ms":   out.LatencyMs,
		"timeout_ms":   timeout,
		"tokens":       gin.H{"prompt": out.PromptTokens, "completion": out.CompletionTokens, "total": out.TotalTokens},
		"cost_usd":     out.CostUsd.String(),
		"priced":       out.Priced,
		// raw_response 是上游**原样**回的那一段(截断到 aiRawSampleRunes)。
		//
		// 没有它,协议对不上时管理端只能看到一个 bad_json,而 bad_json 的三种
		// 成因(地址指到了别的服务 / 协议选错了 / 这个部署的输出格式与官方
		// 示例不同)在界面上长得完全一样。护栏模型这条路尤其需要:官方没有
		// 给出 OpenAI 兼容端点上的字段级规格,真机对不上时唯一的办法就是
		// 照着这一段调 aireview_guard.go 里的正则。
		//
		// 隐私上是干净的:这一次送审的是下面那句写死的良性文本,响应里不可能
		// 有任何用户内容。热路径永远不填这一格(captureRaw=false)。
		"raw_response": out.rawSample,
	}
	if out.Outcome == OutcomeTimeout && isGuardProtocol(protocol) {
		// 本地部署的护栏模型**首次调用要先把权重加载进显存**,秒级甚至十几秒
		// 都正常,而第二次通常在一百毫秒内。不说这一句的话,第一次试跑的超时
		// 看起来与"地址填错了"完全一样,而正确的下一步是再点一次。
		body["hint"] = "cold_start"
	}
	respond(c, body)
}

const (
	// aiTestProbeText 是试跑用的那段良性文本。写死而不是让管理端传:
	// 传进来的那一段会被原样发往第三方,而这个按钮是管理端可达的 —— 那就
	// 成了一条"拿本站当代理往外发任意文本"的通道。
	aiTestProbeText = "今天天气不错,帮我写一封请假邮件。"

	// aiTestHardCeilingMs 是试跑这一次请求的硬上界。它同时兜住"渠道超时填了
	// 30000 + 冷启动"这种组合不会把一个管理端请求挂满半分钟。
	aiTestHardCeilingMs = 40000

	// aiTestGuardColdStartMs 是护栏渠道试跑时的**下限**预算。
	//
	// 三条护栏协议一视同仁(isGuardProtocol):冷启动是**本地部署**的性质,
	// 不是某一个模型的性质 —— Granite 与 Llama Guard 同样是挂在 Ollama /
	// vLLM 上的本地权重。早先只放宽 qwen3guard 是个疏漏,症状是一个配得
	// 完全正确的 Granite 渠道第一次试跑必然红。
	//
	// 本地 Ollama 首次调用要加载模型,0.6B 在冷盘上十几秒是常态。而护栏渠道
	// 的正常配置恰恰是一个很小的 timeout_ms(热态 100ms 就够),于是照搬
	// 生产预算去试跑,第一次必然超时 —— 一个"配得完全正确的渠道,按一下
	// 测试永远是红的"。
	//
	// 只放宽**试跑**,生产预算一个字节不动:两者回答的问题不同。试跑问的是
	// "地址、模型名、协议对不对",生产问的是"这一次审核值不值得等"。
	aiTestGuardColdStartMs = 30000
)

// aiTestTimeoutMs 算这一次试跑的预算。
//
// json_prompt 沿用原口径(渠道超时,没填则用转发前审核的硬上限):云端通用
// 模型没有冷启动这回事,放宽只会让一个真的连不上的地址多挂几秒。
func aiTestTimeoutMs(protocol string, channelTimeoutMs int) int {
	timeout := channelTimeoutMs
	if timeout <= 0 {
		timeout = maxPreTimeoutMs
	}
	if isGuardProtocol(protocol) && timeout < aiTestGuardColdStartMs {
		timeout = aiTestGuardColdStartMs
	}
	return timeout
}

// ───────────────────────────── 设置 ─────────────────────────────

// aiSettingReq 刻意**没有** sample_rate_bps:全局抽样率已经下线,
// 送不送审只由作用域策略表回答。多留一个被忽略的字段,下一个人照着它写前端
// 时会得到一个"填了、保存成功、完全没用"的输入框。
type aiSettingReq struct {
	Enabled             bool `json:"enabled"`
	PreTimeoutMs        int  `json:"pre_timeout_ms"`
	AsyncTimeoutMs      int  `json:"async_timeout_ms"`
	MaxInputChars       int  `json:"max_input_chars"`
	ThirdPartyNoticeAck bool `json:"third_party_notice_ack"`
	// 审核日志三格。**没有**"不传即保持原值"这一档:整张表单是一次性提交的,
	// 少传一个字段就该按它的零值走 —— 而零值会被 validateAISetting 挡下,
	// 于是"前端漏传"表现为一次显式的 400,而不是一次静默的行为变更。
	LogContent bool `json:"log_content"`
	// LogContentViolationFull 是**指针**:三态列不能在这一层被折成两态。
	//   nil  → 请求里没这个字段(老前端 / 脚本)→ 保持库里原值
	//   true / false → 运维做过决定,原样写下去
	// 值类型会把"没传"读成"显式关掉",于是一个还没更新的前端每保存一次设置,
	// 就把违规行的完整留存悄悄关一次。
	LogContentViolationFull *bool `json:"log_content_violation_full"`
	LogContentMaxChars      int   `json:"log_content_max_chars"`
	LogRetentionDays        int   `json:"log_retention_days"`
}

func adminGetAISetting(c *gin.Context) {
	if !guard.RequireAPI(c, guard.FlagViolation) {
		return
	}
	var row AISetting
	if err := db.Get().Where("id = ?", 1).Take(&row).Error; err != nil {
		// 设置行还没建时回默认值而不是 404:界面要能显示"默认是什么",
		// 而 404 只会让表单空着,分不清"没配"与"配成了空"。
		row = AISetting{Id: 1, PreTimeoutMs: 1500, AsyncTimeoutMs: 8000,
			MaxInputChars: defaultAIMaxInputChars, LogContent: true,
			LogContentViolationFull: boolPtr(true),
			LogContentMaxChars:      defaultAIReviewContentChars,
			LogRetentionDays:        defaultAIReviewRetentionDays}
	}
	// 存量行(启动期迁移还没跑到,或跑失败了)那两格是 0,而 0 在表单上会被
	// 读成"不留内容 / 不保留",两句话都不对 —— 实际生效的是默认值。把生效值
	// 填回去,表单显示的就是真的。它只影响这一次回显,不写库。
	if row.LogContentMaxChars <= 0 {
		row.LogContentMaxChars = defaultAIReviewContentChars
	}
	row.LogRetentionDays = effectiveAIReviewRetentionDays(row.LogRetentionDays)
	// 三态列在**回显**这一层折成两态是对的:表单上那个开关只有开与关,
	// 而 NULL 生效起来就是 true。折在这里、不折在库里,是为了让下一次保存
	// 写下一个显式值 —— 从此这一行再也不依赖默认。
	if row.LogContentViolationFull == nil {
		row.LogContentViolationFull = boolPtr(true)
	}
	snap := Snapshot()
	vocab := snap.aiVocab
	respond(c, gin.H{
		"setting": row,
		// 提示词整块搬去渠道了(2026-09-06),这里只剩**渲染素材**:
		// default_prompt 是渠道表单预填与「恢复默认」要用的全文,
		// categories / category_block 让渠道表单能就地做同一套对账与预览。
		// 少了它们,渠道表单就得自己再抄一份类型清单 —— 而抄出来的那一份
		// 会在类型表变动时静默过期。
		"default_prompt": defaultAIPrompt,
		"categories":     vocab.keyList(),
		"category_block": vocab.categoryBlock(),
		// category_details 让界面把 key 与"给 AI 的判定说明有没有填"摆在一起。
		// 只给一串 key 的话,运营看不出哪几类是裸奔的(模型只拿到一个英文单词)。
		"category_details": aiCategoryDetails(vocab),
		"key_configured":   aiKeyConfigured(),
		// effective 是**快照里真正生效的那一份**,不是这张表单的回显。
		// 两者不同的场合很实在:抽样率填了 30% 但一个渠道都没启用时,
		// 表单显示 30%、实际生效是"完全不跑"。没有这一段,那个差别看不见。
		"effective": gin.H{
			"active":            snap.ai != nil,
			"channels":          aiChannelCount(snap),
			"pre_rules":         snap.hasAIPrompt,
			"post_async_rules":  snap.hasAIAsync,
			"pre_timeout_hint":  fmt.Sprintf("转发前审核会给被抽中的请求增加最多 %d 毫秒延迟", clampInt(row.PreTimeoutMs, minAITimeoutMs, maxPreTimeoutMs)),
			"max_pre_timeout":   maxPreTimeoutMs,
			"max_async_timeout": maxAsyncTimeoutMs,
			// 台账库分没分家。界面据此说明"审核日志写在独立库"还是"与扩展库同库"——
			// 没有它,运维改完 log_database.dsn 无法确认它到底生效没有,而这一段
			// 配置的失败模式(拼错 dsn → 启动失败)之外还有一种更安静的:
			// 段名写错 → 整段被忽略 → 一切照旧。
			"log_db_separate": db.LogSeparate(),
			"log_content_range": gin.H{
				"min": minAIReviewContentChars, "max": maxAIReviewContentChars,
			},
			"max_log_retention_days": maxAIReviewRetentionDays,
		},
	})
}

func aiChannelCount(s *snapshot) int {
	if s == nil || s.ai == nil {
		return 0
	}
	return len(s.ai.Channels)
}

// aiCategoryDetails 是类型闭集的管理端视图。
//
// **正面清单**,与 userCategoryView 同一条纪律:这里只有 key / name /
// has_guidance,没有 Remark、没有公示文案。复用 Category 并靠 json tag 挑字段
// 是负面清单,下一次加一列内部字段时它会默认漏出去。
//
// 只给 has_guidance 而不给判定说明原文:这个接口是"类型清单概览",判定说明
// 要改就去违规类型页改,在两个页面各放一份可编辑的同一段文本必然漂移。
func aiCategoryDetails(v aiVocabulary) []gin.H {
	out := make([]gin.H, 0, len(v.Defs))
	for _, d := range v.Defs {
		out = append(out, gin.H{
			"key":            d.Key,
			"name":           d.Name,
			"has_guidance":   strings.TrimSpace(d.Guidance) != "",
			"guidance_runes": len([]rune(d.Guidance)),
			"is_fallback":    d.Key == v.FallbackKey,
		})
	}
	return out
}

func adminPutAISetting(c *gin.Context) {
	if !guard.RequireAPI(c, guard.FlagViolation) {
		return
	}
	var req aiSettingReq
	if err := c.ShouldBindJSON(&req); err != nil {
		badRequest(c, "请求体解析失败")
		return
	}
	gdb := db.Get()
	var before AISetting
	_ = gdb.Where("id = ?", 1).Take(&before).Error

	now := common.GetTimestamp()
	row := AISetting{
		Id: 1, Enabled: req.Enabled,
		PreTimeoutMs: req.PreTimeoutMs, AsyncTimeoutMs: req.AsyncTimeoutMs,
		MaxInputChars:           req.MaxInputChars,
		ThirdPartyNoticeAck:     req.ThirdPartyNoticeAck,
		LogContent:              req.LogContent,
		LogContentViolationFull: req.LogContentViolationFull,
		LogContentMaxChars:      req.LogContentMaxChars,
		LogRetentionDays:        req.LogRetentionDays,
		CreatedAt:               now, UpdatedAt: now, UpdatedBy: c.GetInt("id"),
	}
	if err := validateAISetting(&row); err != nil {
		writeAISettingAudit(c, qymodel.ResultFail, before, row, err)
		badRequest(c, err.Error())
		return
	}
	if before.CreatedAt > 0 {
		row.CreatedAt = before.CreatedAt
	}
	// 请求里没带这个字段时保持原值(见 aiSettingReq 的说明)。Save 写全字段,
	// 不补这一句的话 nil 会把库里的显式值抹成 NULL,行为上等于"被重置成默认"。
	if row.LogContentViolationFull == nil {
		row.LogContentViolationFull = before.LogContentViolationFull
	}
	if err := gdb.Save(&row).Error; err != nil {
		writeAISettingAudit(c, qymodel.ResultFail, before, row, err)
		internalError(c, err)
		return
	}
	afterAIChange(c, "", nil, nil, func() { writeAISettingAudit(c, qymodel.ResultOK, before, row, nil) })
	// 提示词的回显三件套(source / 对账 / 预览)跟着那一列搬去渠道接口了。
	// 这一页保存之后没有别的东西需要当场回读 —— 剩下的全是数值与开关,
	// 它们保存成功就是生效。
	respond(c, gin.H{"setting": row})
}

// ───────────────────────────── 明细与成本 ─────────────────────────────

// aiReviewQuery 组装审核明细的筛选条件。
//
// 列表与详情共用它是为了让两者的可见范围永远一致 —— 详情按 id 直取,
// 不共用的话"列表里筛不出来的行,拿 id 照样点得开"。
//
// 每一个筛法都对应界面上一个具体的问题,这不是把列都加一遍:
//
//	group / model    「哪个分组的哪个模型在被审」—— 这张表存在的第一个问题
//	phase            「转发前(同步、会拦)还是转发后(异步、只记录)」
//	user_id          「这次审核是谁触发的」
//	violated         「只看判了违规的那些」
//	outcome          排障:timeout 找网络、bad_json 找提示词、no_channel 找配置
//	request_id       从使用日志的一条请求跳过来对照
//
// group 与 model 是**精确**匹配,不是模糊搜索:两者都是闭集里的取值
// (分组名、模型名),界面上给的是下拉而不是输入框。模糊匹配在几百万行上
// 会退化成全表扫,而它换来的能力(输入 gpt 匹配一批模型)对"这个模型被审了
// 多少次"这个问题是有害的 —— 数出来的是一批模型的合计。
func aiReviewQuery(c *gin.Context) *gorm.DB {
	q := db.Log().Model(&AIReview{})
	if v := strings.TrimSpace(c.Query("outcome")); v != "" {
		q = q.Where("outcome = ?", v)
	}
	if v := strings.TrimSpace(c.Query("phase")); v != "" {
		q = q.Where("phase = ?", v)
	}
	if v := strings.TrimSpace(c.Query("group")); v != "" {
		q = q.Where("using_group = ?", v)
	}
	if v := strings.TrimSpace(c.Query("model")); v != "" {
		q = q.Where("model_name = ?", v)
	}
	if v := strings.TrimSpace(c.Query("request_id")); v != "" {
		q = q.Where("request_id = ?", v)
	}
	// 按**审核渠道**筛。挂了两个以上渠道的站点,"哪个渠道在误判 / 哪个渠道一直超时"
	// 是这一页最常被问的问题之一,而 channel_id 早就在行上,只是没人能筛。
	// 用 id 而不是名字:渠道可以改名,而历史明细里冗余的是改名前那一份
	// (见 adminDeleteAIChannel 的说明),按名字筛会漏掉改名之前的全部记录。
	if v := httpq.Int64(c, "channel_id", 0); v > 0 {
		q = q.Where("channel_id = ?", v)
	}
	// 三态:不传 = 全部,1 = 只看判违规的,0 = 只看判未违规的。
	// 布尔用占位符传 Go 的 bool,不写字面量 0/1 —— PostgreSQL 的 boolean
	// 不接受整数字面量(runRetentionGC 顶上记着同一条教训)。
	switch strings.TrimSpace(c.Query("violated")) {
	case "1", "true":
		q = q.Where("violated = ?", true)
	case "0", "false":
		q = q.Where("violated = ?", false)
	}
	if v := httpq.Int(c, "user_id", 0); v > 0 {
		q = q.Where("user_id = ?", v)
	}
	if v := httpq.Int64(c, "start", 0); v > 0 {
		q = q.Where("created_at >= ?", v)
	}
	if v := httpq.Int64(c, "end", 0); v > 0 {
		q = q.Where("created_at <= ?", v)
	}
	return q
}

func adminListAIReviews(c *gin.Context) {
	if !guard.RequireAPI(c, guard.FlagViolation) {
		return
	}
	page, size := httpq.Paginate(c, listPaging)
	q := aiReviewQuery(c)
	var total int64
	if err := q.Count(&total).Error; err != nil {
		internalError(c, err)
		return
	}
	rows := make([]AIReview, 0, size)
	// 列表**不带内容**。它是一个 text 列,一页 20 行就是几十 KB 的无用传输,
	// 而列表上根本放不下一段千字文本 —— 界面显示的是 content_chars(有没有、
	// 多长),点开某一行才去详情接口取那一段。
	//
	// 用 Omit 而不是 Select 一串列名:后者要在这里维护一份与模型同步的白名单,
	// 而下一个人加列时不会想到回来改它,结果是新列在列表上永远缺失。
	if err := q.Omit("content").Order("id desc").
		Offset(httpq.Offset(page, size)).Limit(size).Find(&rows).Error; err != nil {
		internalError(c, err)
		return
	}
	respond(c, gin.H{"items": rows, "total": total, "page": page, "page_size": size})
}

// adminGetAIReview 是单条审核明细的详情,**含送审内容**。
//
// 单独一条路由而不是在列表上加一个 with_content 参数:内容是这一页上唯一一段
// 用户原文(已脱敏、已截断),它该有自己的访问点,审计与限流才有得挂。
// 一个开关参数则会让"谁在批量拉取用户内容"与"谁在看一条记录"长得一模一样。
func adminGetAIReview(c *gin.Context) {
	if !guard.RequireAPI(c, guard.FlagViolation) {
		return
	}
	id, ok := httpq.PathInt64(c, "id")
	if !ok {
		badRequest(c, "记录 id 非法")
		return
	}
	var row AIReview
	if err := db.Log().Where("id = ?", id).Take(&row).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			// 保留期到了就是查不到,这不是异常。文案要说清楚是"过期了"而不是
			// "你没权限"或"系统坏了" —— 三天的窗口很短,这一定会被撞上。
			respondFail(c, http.StatusNotFound, "qy_vio_not_found",
				"审核明细不存在或已超过保留期")
			return
		}
		internalError(c, err)
		return
	}
	respond(c, gin.H{
		"item": row,
		// truncated 让界面能明确地说"这段是截断过的",而不是让人对着一段
		// 突然结束的文本猜。判据是两个数不等 —— 只看有没有 ...[truncated]...
		// 标记会被一段正好包含那几个字的用户内容骗过去。
		"truncated": row.ContentChars > utf8.RuneCountInString(row.Content),
		// 内容留存**当前**开着吗。一行空内容有两种成因(那时没开 / 那次没内容),
		// 而运营最常问的是"为什么这条没有内容"。给出当前开关,至少能把
		// "整个功能没开"这一种当场排除掉。
		"log_content_enabled": aiLogContentEnabled(),
	})
}

// aiLogContentEnabled 读当前的内容留存开关。
//
// 走快照而不是查库:这是详情页每次打开都要问一次的东西,而快照里就有。
// 快照为 nil(AI 审核整体关闭)时答 false —— 那时确实不会再写任何内容。
func aiLogContentEnabled() bool {
	rt := Snapshot().ai
	return rt != nil && rt.LogContent
}

// adminAIReviewStats 是成本可见性的那一页。
//
// 它回答四个问题,缺一个都不算"成本可见":
//   - 跑了多少次(按结局分)——含全部失败,那是 fail-open 是否在静默吞掉一切的答案
//   - 花了多少 token
//   - 花了多少钱
//   - 有多少次调用的花费**算不准**(链上有渠道没填单价)—— 没有这个数,一个
//     $0 的总额会被当成"没花钱",而它可能是"全站都没填单价";一个偏低的正数
//     则会被当成真值,那是混价重试链的形状
func adminAIReviewStats(c *gin.Context) {
	if !guard.RequireAPI(c, guard.FlagViolation) {
		return
	}
	days := httpq.Int(c, "days", 7)
	if days < 1 {
		days = 1
	}
	if days > 90 {
		days = 90
	}
	from := common.GetTimestamp() - int64(days)*86400

	type row struct {
		Outcome    string          `json:"outcome"`
		Cnt        int64           `json:"count"`
		PromptTok  int64           `json:"prompt_tokens"`
		CompTok    int64           `json:"completion_tokens"`
		TotalTok   int64           `json:"total_tokens"`
		CostUsdSum decimal.Decimal `json:"cost_usd"`
	}
	// 空结果必须是 [] 而不是 null:前端对着 null 调 .map 会整页白屏。
	rows := make([]row, 0, 8)
	// GROUP BY outcome:每一种失败各自的次数是排障的全部信息(见 Outcome 的说明)。
	if err := db.Log().Model(&AIReview{}).
		Select("outcome, COUNT(*) AS cnt, COALESCE(SUM(prompt_tokens),0) AS prompt_tok, "+
			"COALESCE(SUM(completion_tokens),0) AS comp_tok, COALESCE(SUM(total_tokens),0) AS total_tok, "+
			"COALESCE(SUM(cost_usd),0) AS cost_usd_sum").
		Where("created_at >= ?", from).Group("outcome").Scan(&rows).Error; err != nil {
		internalError(c, err)
		return
	}

	var totalCnt, totalTok int64
	totalCost := decimal.Zero
	for _, r := range rows {
		totalCnt += r.Cnt
		totalTok += r.TotalTok
		totalCost = totalCost.Add(r.CostUsdSum)
	}
	// 「花费算不准」的调用数。判据有两条腿,缺一条就会漏掉一整类:
	//
	//	cost_usd <= 0    确实发出去了(有 token)却一分钱都没算出来 —— 整条链都没单价。
	//	cost_unknown     算出来了,但已知是**下界**:链上有一次产生了 token 却没单价。
	//
	// 只有第一条腿时,混价链(一个有单价的渠道 + 一个没有的,两次都产生了 token)
	// 的 cost_usd 是正数,于是它不会被算进来 —— 界面把一个偏低的数字当成准确值
	// 展示,而"这个月比预算省了 40%"没有人会来查。见 AIReview.CostUnknown。
	var unpriced int64
	if err := db.Log().Model(&AIReview{}).
		Where("created_at >= ? AND total_tokens > 0 AND (cost_usd <= 0 OR cost_unknown = ?)", from, true).
		Count(&unpriced).Error; err != nil {
		internalError(c, err)
		return
	}
	var violated int64
	if err := db.Log().Model(&AIReview{}).
		Where("created_at >= ? AND violated = ?", from, true).Count(&violated).Error; err != nil {
		internalError(c, err)
		return
	}

	respond(c, gin.H{
		"days":           days,
		"by_outcome":     rows,
		"total_calls":    totalCnt,
		"total_tokens":   totalTok,
		"total_cost_usd": totalCost.Round(6).String(),
		"violated_calls": violated,
		// unpriced_calls > 0 时界面必须提示"成本被低估",而不是显示一个漂亮的小数字。
		"unpriced_calls": unpriced,
	})
}

// ───────────────────────────── 审计与刷新 ─────────────────────────────

// afterAIChange 是渠道写动作的收尾:bump 规则版本 → 重载快照 → 写审计。
//
// 必须 bump 规则版本:AI 配置与规则装在**同一份快照**里,而 reloadCtx 在版本
// 未变时直接返回。不 bump 的话,新增/删除渠道要等到下一次有人改规则才生效 ——
// 而中间这段时间界面显示"已启用",线上用的还是旧渠道池。
//
// extra 是设置接口用的钩子(它的审计快照形状与渠道不同)。传 nil 时走渠道审计。
func afterAIChange(c *gin.Context, action string, before, after *AIChannel, extra func()) {
	bumpRuleVersion()
	if err := reload(true); err != nil {
		common.SysError("qianye/violation: AI 审核配置变更后重载失败: " + err.Error())
	}
	if extra != nil {
		extra()
		return
	}
	writeAIReviewAudit(c, action, qymodel.ResultOK, before, after, nil)
}

// writeAIReviewAudit 是渠道全部写动作的唯一审计出口,成功与失败同一出口。
//
// 一个出口而不是每个 handler 各写一段:漏写的那一次一定是失败分支,
// 而失败分支恰恰是要查的 ——「我改了三次密钥都没生效」只能靠失败审计回答。
//
// **快照里绝不能出现密钥的任何部分**,连密文都不行:审计表会被导出、会被
// 管理端整行下发,而密文一旦离开服务器,轮换密钥就再也补不回来了。
// 这里只写 has_key + key_hint —— 后者本来就是给人看的掩码。
func writeAIReviewAudit(c *gin.Context, action, result string, before, after *AIChannel, err error) {
	reason := ""
	if err != nil {
		reason = truncate("失败: "+err.Error(), 512)
	}
	traceNo := ""
	if after != nil {
		traceNo = fmt.Sprintf("ai_channel:%d", after.Id)
	} else if before != nil {
		traceNo = fmt.Sprintf("ai_channel:%d", before.Id)
	}
	audit.Write(c, audit.Entry{
		Category:    qymodel.AuditCategoryViolation,
		Action:      action,
		ActorType:   qymodel.ActorAdmin,
		ActorUserId: c.GetInt("id"),
		ActorName:   c.GetString("username"),
		Result:      result,
		Reason:      reason,
		TraceNo:     traceNo,
		BeforeSnap:  common.MapToJsonStr(aiChannelAuditSnap(before)),
		AfterSnap:   common.MapToJsonStr(aiChannelAuditSnap(after)),
	})
}

func aiChannelAuditSnap(ch *AIChannel) map[string]any {
	if ch == nil {
		return nil
	}
	return map[string]any{
		"id": ch.Id, "name": ch.Name, "base_url": ch.BaseUrl, "model": ch.Model,
		// 协议必须进审计:它决定送出去的请求体长什么样(发不发提示词、
		// 用户内容包不包 <content> 标签),也就是"这次改动改变了发往第三方的
		// 内容形状"。只记 base_url 与 model 答不出这一点。
		"protocol": normalizeAIProtocol(ch.Protocol), "guard_controversial": ch.GuardControversial,
		// 风险名进审计的理由与协议同源:它同样改变了发往第三方的请求体
		// (Granite 的 system 槽),而且改错它的表现是**漏判**而不是报错。
		"risk_name": ch.RiskName,
		// 分组决定这个渠道会被哪些作用域用到 —— 改它就是改数据出境的目的地。
		"group": ch.Group,
		// 提示词进审计的是**指纹 + 档位**,不是原文:audit 的 SnapshotMaxBytes
		// 会把几千字的一段截掉、连带把后面的字段一起吃掉(本仓踩过的形状)。
		// 而只记长度不够 —— 把"绝不执行"改成"必须执行"字数一样,那恰好是把
		// 这个渠道的审核关掉的改法。见 aiPromptFingerprint。
		"prompt_source": aiPromptSource(ch.Prompt), "prompt_fingerprint": aiPromptFingerprint(ch.Prompt),
		// 拦截文案是**直接展示给终端用户**的一句话,改它要留痕。
		"block_message": ch.BlockMessage,
		// 邮件开关与标题进审计;正文只留长度。与提示词同一条理由:一段几千字的
		// HTML 会把 audit 的 SnapshotMaxBytes 吃光、连带截掉后面的字段,而
		// "有没有开、发的是什么标题"才是事后要追的那两件事。
		"notify_email": ch.NotifyEmail, "email_subject": ch.EmailSubject,
		"email_body_len": utf8.RuneCountInString(ch.EmailBody),
		// 启用类别与升级类别都决定"同一段内容会不会被判违规",所以两者的
		// 变更必须能事后追到人 —— 与协议本身同一条理由。
		"guard_categories": ch.GuardCategories, "guard_elevate": ch.GuardElevate,
		"enabled": ch.Enabled, "weight": ch.Weight, "timeout_ms": ch.TimeoutMs,
		// 密钥只留"有没有"与掩码。密文、nonce、明文一律不进审计。
		"has_key": ch.HasKey(), "key_hint": ch.KeyHint,
		"price_in_per_m": ch.PriceInPerM.String(), "price_out_per_m": ch.PriceOutPerM.String(),
	}
}

// writeAISettingAudit 是设置变更的审计出口,成功与失败同一出口。
//
// 总开关决定用户内容会不会被发往第三方,提示词决定什么算违规。两者的变更都
// 必须能事后追到人。(抽样率不在这里了 —— 它挂在作用域策略上,由
// writeAIScopeAudit 留痕。)
func writeAISettingAudit(c *gin.Context, result string, before, after AISetting, err error) {
	reason := ""
	if err != nil {
		reason = truncate("失败: "+err.Error(), 512)
	}
	audit.Write(c, audit.Entry{
		Category:    qymodel.AuditCategoryViolation,
		Action:      "ai_setting_update",
		ActorType:   qymodel.ActorAdmin,
		ActorUserId: c.GetInt("id"),
		ActorName:   c.GetString("username"),
		Result:      result,
		Reason:      reason,
		BeforeSnap:  common.MapToJsonStr(aiSettingAuditSnap(before)),
		AfterSnap:   common.MapToJsonStr(aiSettingAuditSnap(after)),
	})
}

// aiSettingAuditSnap 是设置行的审计快照。
//
// 提示词直接决定"什么算违规",所以改它必须留痕 —— 但整段进快照会把
// audit 的 SnapshotMaxBytes 撑爆并截掉后面的字段(本仓踩过的形状)。
// 折中是记指纹:长度 + sha256 前 16 位 + 属于哪一档 + 类型闭集的对账结果。
//
// 为什么长度不够:把"绝不执行"改成"必须执行"字数一模一样,而那一改正是
// 把提示词注入防线关掉的改法。只记 prompt_runes 时它在审计里毫无痕迹。
func aiSettingAuditSnap(s AISetting) map[string]any {
	return map[string]any{
		"enabled":        s.Enabled,
		"pre_timeout_ms": s.PreTimeoutMs, "async_timeout_ms": s.AsyncTimeoutMs,
		"max_input_chars":        s.MaxInputChars,
		"third_party_notice_ack": s.ThirdPartyNoticeAck,
		// 内容留存与保留期都要进审计。前者决定"用户请求内容有没有被抄进我们
		// 自己的库",后者决定"抄下来的那份活多久" —— 两个问题在事后追责时
		// 都会被问到,而它们唯一的书面痕迹就是这一行。
		"log_content": s.LogContent,
		// 三态原样进审计:nil 与 false 在这里必须分得开 —— 前者是"从没设置过",
		// 后者是一次明确的"不要留",而事后追责问的正是后者发生在哪一次。
		"log_content_violation_full": s.LogContentViolationFull,
		"log_content_max_chars":      s.LogContentMaxChars,
		"log_retention_days":         s.LogRetentionDays,
	}
}

// sameAIChannelEndpoint 判断两个 base_url 是不是同一个上游端点。
//
// 只做"去掉首尾空白与结尾斜杠 + 大小写不敏感"这点归一化,不做 URL 解析:
// 判错的方向必须是"多清一次密钥"(运营重填一次)而不是"少清一次"
// (密钥跟着地址走到攻击者的机器上)。任何更聪明的等价判断都会扩大后者。
func sameAIChannelEndpoint(a, b string) bool {
	norm := func(s string) string {
		return strings.ToLower(strings.TrimRight(strings.TrimSpace(s), "/"))
	}
	return norm(a) == norm(b)
}
