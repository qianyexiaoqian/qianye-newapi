package violation

import (
	"context"
	"fmt"
	"strings"

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
	ApiKey       *string `json:"api_key"`
	TimeoutMs    int     `json:"timeout_ms"`
	Weight       int     `json:"weight"`
	Enabled      bool    `json:"enabled"`
	PriceInPerM  string  `json:"price_in_per_m"`
	PriceOutPerM string  `json:"price_out_per_m"`
	Remark       string  `json:"remark"`
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
	Id           int64  `json:"id"`
	Name         string `json:"name"`
	BaseUrl      string `json:"base_url"`
	Model        string `json:"model"`
	HasKey       bool   `json:"has_key"`
	KeyHint      string `json:"key_hint"`
	TimeoutMs    int    `json:"timeout_ms"`
	Weight       int    `json:"weight"`
	Enabled      bool   `json:"enabled"`
	PriceInPerM  string `json:"price_in_per_m"`
	PriceOutPerM string `json:"price_out_per_m"`
	Remark       string `json:"remark"`
	UpdatedAt    int64  `json:"updated_at"`
}

func toAIChannelView(ch AIChannel) aiChannelView {
	return aiChannelView{
		Id: ch.Id, Name: ch.Name, BaseUrl: ch.BaseUrl, Model: ch.Model,
		HasKey: ch.HasKey(), KeyHint: ch.KeyHint,
		TimeoutMs: ch.TimeoutMs, Weight: ch.Weight, Enabled: ch.Enabled,
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
	})
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
	if req.ApiKey != nil {
		if err := applyAIChannelKey(gdb, &row, *req.ApiKey); err != nil {
			writeAIReviewAudit(c, "ai_channel_update", qymodel.ResultFail, &before, &row, err)
			badRequest(c, err.Error())
			return
		}
	}
	// Select 显式列出要写的列:Save/Updates 全字段写回会把上面刚算好的
	// KeyCipher/KeyNonce 再覆盖一次(applyAIChannelKey 已经落过库了),
	// 而 nil 密钥 + 全字段写回 = 静默清空密钥。
	if err := gdb.Model(&AIChannel{}).Where("id = ?", row.Id).
		Select("name", "base_url", "model", "timeout_ms", "weight", "enabled",
			"price_in_per_m", "price_out_per_m", "remark", "updated_at", "updated_by").
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
	}
	return gdb.Model(&AIChannel{}).Where("id = ?", row.Id).
		Select("key_nonce", "key_cipher", "key_version", "key_hint").
		Updates(map[string]any{
			"key_nonce": row.KeyNonce, "key_cipher": row.KeyCipher,
			"key_version": row.KeyVersion, "key_hint": row.KeyHint,
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
	key, err := openAIKey(row.KeyNonce, row.KeyCipher, aiChannelAAD(row.Id), row.KeyVersion)
	if err != nil {
		respond(c, gin.H{"outcome": OutcomeNoChannel, "message": "渠道密钥无法解密:" + err.Error()})
		return
	}
	rt := &aiChannelRT{
		Id: row.Id, Name: row.Name, URL: url, Model: row.Model, APIKey: key,
		TimeoutMs: row.TimeoutMs, Weight: 1,
		PriceInPerM: row.PriceInPerM, PriceOutPerM: row.PriceOutPerM,
	}
	ctx, cancel := context.WithTimeout(c.Request.Context(), 15000*1000000)
	defer cancel()
	timeout := row.TimeoutMs
	if timeout <= 0 {
		timeout = maxPreTimeoutMs
	}
	out := callAIChannel(ctx, rt, defaultAIPrompt, "今天天气不错,帮我写一封请假邮件。", timeout)
	respond(c, gin.H{
		"outcome":    out.Outcome,
		"violated":   out.Violated,
		"category":   out.Category,
		"confidence": out.Confidence.String(),
		"latency_ms": out.LatencyMs,
		"tokens":     gin.H{"prompt": out.PromptTokens, "completion": out.CompletionTokens, "total": out.TotalTokens},
		"cost_usd":   out.CostUsd.String(),
		"priced":     out.Priced,
	})
}

// ───────────────────────────── 设置 ─────────────────────────────

type aiSettingReq struct {
	Enabled             bool   `json:"enabled"`
	SampleRateBps       int    `json:"sample_rate_bps"`
	PreTimeoutMs        int    `json:"pre_timeout_ms"`
	AsyncTimeoutMs      int    `json:"async_timeout_ms"`
	Prompt              string `json:"prompt"`
	MaxInputChars       int    `json:"max_input_chars"`
	ThirdPartyNoticeAck bool   `json:"third_party_notice_ack"`
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
			MaxInputChars: defaultAIMaxInputChars}
	}
	snap := Snapshot()
	respond(c, gin.H{
		"setting": row,
		// default_prompt 一起下发,界面上"恢复默认"才有东西可填,
		// 而不是让运营去源码里抄一段提示词。
		"default_prompt": defaultAIPrompt,
		"categories":     sortedAICategories(),
		"key_configured": aiKeyConfigured(),
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
		},
	})
}

func aiChannelCount(s *snapshot) int {
	if s == nil || s.ai == nil {
		return 0
	}
	return len(s.ai.Channels)
}

func sortedAICategories() []string {
	out := make([]string, 0, len(aiCategories))
	for k := range aiCategories {
		out = append(out, k)
	}
	return dedupe(out)
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
		Id: 1, Enabled: req.Enabled, SampleRateBps: req.SampleRateBps,
		PreTimeoutMs: req.PreTimeoutMs, AsyncTimeoutMs: req.AsyncTimeoutMs,
		Prompt: req.Prompt, MaxInputChars: req.MaxInputChars,
		ThirdPartyNoticeAck: req.ThirdPartyNoticeAck,
		CreatedAt:           now, UpdatedAt: now, UpdatedBy: c.GetInt("id"),
	}
	if err := validateAISetting(&row); err != nil {
		writeAISettingAudit(c, qymodel.ResultFail, before, row, err)
		badRequest(c, err.Error())
		return
	}
	if before.CreatedAt > 0 {
		row.CreatedAt = before.CreatedAt
	}
	if err := gdb.Save(&row).Error; err != nil {
		writeAISettingAudit(c, qymodel.ResultFail, before, row, err)
		internalError(c, err)
		return
	}
	afterAIChange(c, "", nil, nil, func() { writeAISettingAudit(c, qymodel.ResultOK, before, row, nil) })
	respond(c, row)
}

// ───────────────────────────── 明细与成本 ─────────────────────────────

func adminListAIReviews(c *gin.Context) {
	if !guard.RequireAPI(c, guard.FlagViolation) {
		return
	}
	page, size := httpq.Paginate(c, listPaging)
	q := db.Get().Model(&AIReview{})
	if v := strings.TrimSpace(c.Query("outcome")); v != "" {
		q = q.Where("outcome = ?", v)
	}
	if v := strings.TrimSpace(c.Query("phase")); v != "" {
		q = q.Where("phase = ?", v)
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
	var total int64
	if err := q.Count(&total).Error; err != nil {
		internalError(c, err)
		return
	}
	rows := make([]AIReview, 0, size)
	if err := q.Order("id desc").Offset(httpq.Offset(page, size)).Limit(size).Find(&rows).Error; err != nil {
		internalError(c, err)
		return
	}
	respond(c, gin.H{"items": rows, "total": total, "page": page, "page_size": size})
}

// adminAIReviewStats 是成本可见性的那一页。
//
// 它回答四个问题,缺一个都不算"成本可见":
//   - 跑了多少次(按结局分)——含全部失败,那是 fail-open 是否在静默吞掉一切的答案
//   - 花了多少 token
//   - 花了多少钱
//   - 有多少次调用**算不出钱**(渠道没填单价)—— 没有这个数,一个 $0 的总额
//     会被当成"没花钱",而它可能是"全站都没填单价"
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
	if err := db.Get().Model(&AIReview{}).
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
	// 「算不出钱」的调用数:确实发出去了(有 token)却一分钱都没算出来。
	var unpriced int64
	if err := db.Get().Model(&AIReview{}).
		Where("created_at >= ? AND total_tokens > 0 AND cost_usd <= 0", from).
		Count(&unpriced).Error; err != nil {
		internalError(c, err)
		return
	}
	var violated int64
	if err := db.Get().Model(&AIReview{}).
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
		"enabled": ch.Enabled, "weight": ch.Weight, "timeout_ms": ch.TimeoutMs,
		// 密钥只留"有没有"与掩码。密文、nonce、明文一律不进审计。
		"has_key": ch.HasKey(), "key_hint": ch.KeyHint,
		"price_in_per_m": ch.PriceInPerM.String(), "price_out_per_m": ch.PriceOutPerM.String(),
	}
}

// writeAISettingAudit 是设置变更的审计出口,成功与失败同一出口。
//
// 抽样率与总开关是这一页最重的两个数字:前者直接决定花多少钱,后者决定
// 用户内容会不会被发往第三方。两者的变更都必须能事后追到人。
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

func aiSettingAuditSnap(s AISetting) map[string]any {
	return map[string]any{
		"enabled": s.Enabled, "sample_rate_bps": s.SampleRateBps,
		"pre_timeout_ms": s.PreTimeoutMs, "async_timeout_ms": s.AsyncTimeoutMs,
		"max_input_chars":        s.MaxInputChars,
		"third_party_notice_ack": s.ThirdPartyNoticeAck,
		// 提示词只留长度与是否自定义:它可能有几千字,整段进审计会把
		// SnapshotMaxBytes 撑爆并截掉后面的字段(那正是本仓踩过的形状)。
		"prompt_customized": strings.TrimSpace(s.Prompt) != "",
		"prompt_runes":      len([]rune(s.Prompt)),
	}
}
