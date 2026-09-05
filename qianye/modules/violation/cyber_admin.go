package violation

// cyber_admin.go —— cyber 会话屏蔽设置的管理端读写。
//
// 与 adminGetAISetting / adminPutAISetting 完全同构:单行表,GET 缺行回默认值
// (不 404),PUT 走"归一校验 → Save → bumpRuleVersion + reload(true) → 审计"。
// 落进**同一份规则快照**,所以写后必须 bump 版本号,否则 reloadCtx 的版本比对
// 会把这次改动短路掉(与 afterAIChange 同一条理由)。

import (
	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/qianye/config"
	"github.com/QuantumNous/new-api/qianye/db"
	"github.com/QuantumNous/new-api/qianye/guard"
	qymodel "github.com/QuantumNous/new-api/qianye/model"
	"github.com/QuantumNous/new-api/qianye/service/audit"

	"github.com/gin-gonic/gin"
)

// cyberSettingReq 是 PUT 的请求体。刻意只收管理员能配的四项,Id / 时间戳 /
// 操作者由服务端填 —— 让请求体带 Id 等于给了一条"改别的行"的口子。
type cyberSettingReq struct {
	Enabled        bool   `json:"enabled"`
	GroupScope     string `json:"group_scope"`
	GroupScopeMode string `json:"group_scope_mode"`
	TTLSeconds     int    `json:"ttl_seconds"`
	TriggerCodes   string `json:"trigger_codes"`
	CountTowardBan bool   `json:"count_toward_ban"`
	CategoryId     int64  `json:"category_id"`
}

func adminGetCyberSetting(c *gin.Context) {
	if !guard.RequireAPI(c, guard.FlagViolation) {
		return
	}
	var row CyberSetting
	if err := db.Get().Where("id = ?", 1).Take(&row).Error; err != nil {
		// 设置行还没建时回默认值而不是 404:界面要能显示"默认是什么",
		// 而 404 只会让表单空着,分不清"没配"与"配成了空"。
		row = CyberSetting{Id: 1, GroupScopeMode: GroupScopeInclude,
			TTLSeconds: cyberDefaultTTLSeconds, TriggerCodes: cyberDefaultTriggerText}
	}
	snap := Snapshot()
	respond(c, gin.H{
		"setting": row,
		// 「还原默认过滤内容」按钮要填回的内容:出厂触发过滤规则。
		"default_trigger_codes": cyberDefaultTriggerText,
		// effective 是快照里**真正生效**的那一份,不是这张表单的回显:
		// 设置存了 enabled=true 但 YAML violation.enabled=false 时,表单显示"开"、
		// 实际不生效。没有这一段,那个差别看不见。
		"effective": gin.H{
			"active":      snap.cyber != nil,
			"module_on":   config.Get().Violation.Enabled,
			"default_ttl": cyberDefaultTTLSeconds,
			"max_ttl":     cyberMaxTTLSeconds,
		},
	})
}

func adminPutCyberSetting(c *gin.Context) {
	if !guard.RequireAPI(c, guard.FlagViolation) {
		return
	}
	var req cyberSettingReq
	if err := c.ShouldBindJSON(&req); err != nil {
		badRequest(c, "请求体解析失败")
		return
	}
	gdb := db.Get()
	if gdb == nil {
		internalError(c, db.ErrNotReady)
		return
	}
	var before CyberSetting
	_ = gdb.Where("id = ?", 1).Take(&before).Error

	now := common.GetTimestamp()
	row := CyberSetting{
		Id: 1, Enabled: req.Enabled,
		GroupScope: req.GroupScope, GroupScopeMode: req.GroupScopeMode,
		TTLSeconds: req.TTLSeconds, TriggerCodes: req.TriggerCodes,
		CountTowardBan: req.CountTowardBan, CategoryId: req.CategoryId,
		CreatedAt: now, UpdatedAt: now, UpdatedBy: c.GetInt("id"),
	}
	if err := validateCyberSetting(&row); err != nil {
		writeCyberSettingAudit(c, qymodel.ResultFail, before, row, err)
		badRequest(c, err.Error())
		return
	}
	if before.CreatedAt > 0 {
		row.CreatedAt = before.CreatedAt
	}
	if err := gdb.Save(&row).Error; err != nil {
		writeCyberSettingAudit(c, qymodel.ResultFail, before, row, err)
		internalError(c, err)
		return
	}
	// 与 afterAIChange 同一套:落进共享快照,必须 bump 版本号再强制重载,
	// 否则 reloadCtx 的版本比对会把这次改动短路掉。
	bumpRuleVersion()
	if err := reload(true); err != nil {
		common.SysError("qianye/violation: cyber 会话屏蔽设置变更后重载失败: " + err.Error())
	}
	writeCyberSettingAudit(c, qymodel.ResultOK, before, row, nil)

	snap := Snapshot()
	respond(c, gin.H{
		"setting":               row,
		"default_trigger_codes": cyberDefaultTriggerText,
		"effective": gin.H{
			"active":    snap.cyber != nil,
			"module_on": config.Get().Violation.Enabled,
		},
	})
}

func writeCyberSettingAudit(c *gin.Context, result string, before, after CyberSetting, err error) {
	reason := ""
	if err != nil {
		reason = truncate("失败: "+err.Error(), 512)
	}
	audit.Write(c, audit.Entry{
		Category:    qymodel.AuditCategoryViolation,
		Action:      "cyber_setting_update",
		ActorType:   qymodel.ActorAdmin,
		ActorUserId: c.GetInt("id"),
		ActorName:   c.GetString("username"),
		Result:      result,
		Reason:      reason,
		BeforeSnap:  common.MapToJsonStr(cyberSettingAuditSnap(before)),
		AfterSnap:   common.MapToJsonStr(cyberSettingAuditSnap(after)),
	})
}

func cyberSettingAuditSnap(s CyberSetting) map[string]any {
	return map[string]any{
		"enabled":          s.Enabled,
		"group_scope":      s.GroupScope,
		"group_scope_mode": s.GroupScopeMode,
		"ttl_seconds":      s.TTLSeconds,
		"trigger_codes":    s.TriggerCodes,
		"count_toward_ban": s.CountTowardBan,
		"category_id":      s.CategoryId,
	}
}
