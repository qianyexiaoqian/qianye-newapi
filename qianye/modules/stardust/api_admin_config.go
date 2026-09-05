package stardust

import (
	"encoding/json" // 仅取 RawMessage 类型;编解码一律走 common.*
	"strings"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/middleware"
	"github.com/QuantumNous/new-api/qianye/config"
	"github.com/QuantumNous/new-api/qianye/guard"
	qymodel "github.com/QuantumNous/new-api/qianye/model"
	"github.com/QuantumNous/new-api/qianye/service/audit"
	"github.com/QuantumNous/new-api/setting/operation_setting"

	"github.com/gin-gonic/gin"
)

// api_admin_config.go —— 运营参数的读写(qy_settings, scope=stardust)。
//
// 形状照 lottery/api_admin_config.go:下发 effective + overrides + editable_keys +
// bounds + yaml_readonly 五段,前端按 editable_keys 渲染字段、按 bounds 做输入校验,
// **绝不自己抄一份区间** —— 两份区间迟早漂移成"界面允许、后端 400"。
//
// 与 lottery 的一处不同:校验全部在 settings.go 的 saveOverrides 里(白名单、区间、
// 合规门),这里不再判第二遍。同一条规则写两处的下场是"界面拒绝、后端放行"或反过来。

const configAuditAction = "stardust.config.update"

func init() {
	adminRouteInstallers = append(adminRouteInstallers, func(g *gin.RouterGroup) {
		g.GET("/stardust/config", handleGetConfig)
		// 三档比例决定平台会发出去多少星屑,写接口挂关键操作限流。
		g.PUT("/stardust/config", middleware.CriticalRateLimit(), handlePutConfig)
	})
}

// handleGetConfig 返回生效参数 + 运营覆盖 + 白名单 + 取值区间 + YAML 只读快照。
func handleGetConfig(c *gin.Context) {
	if !guard.RequireAPI(c, guard.FlagStardust) {
		return
	}
	ctx := c.Request.Context()
	overrides, err := loadOverrides(ctx)
	if err != nil {
		respondErr(c, err)
		return
	}
	set := effectiveCtx(ctx)
	cfg := config.Get().Stardust

	bounds := make(map[string]gin.H, len(editableKeys))
	for key, b := range Bounds() {
		bounds[key] = gin.H{"lo": b.Lo, "hi": b.Hi}
	}

	respondOK(c, gin.H{
		"effective":     settingsSnapshot(set),
		"overrides":     overrides,
		"editable_keys": EditableKeys(),
		"bounds":        bounds,
		// YAML 段:刻度、结算调度、两个排除开关与手调上限只能改文件后重启 ——
		// 那是一次看得见、留得下痕迹的动作。quota_per_unit 下发的是**实际生效**的刻度
		// (YAML 为 0 时取 common.QuotaPerUnit),运营要的是"1 星屑现在等于多少额度",
		// 不是"配置文件里写了几"。compliance_confirmed 让配置页能直说"邀请类三项
		// 现在为什么按 0 生效",而不是让运营对着一个写不进去的输入框猜。
		"yaml_readonly": gin.H{
			"quota_per_unit":               QuotaPerUnit(),
			"settle_delay_minutes":         cfg.SettleDelayMinutes,
			"settle_interval_seconds":      cfg.SettleIntervalSeconds,
			"exclude_subscription_consume": cfg.SubscriptionConsumeExcluded(),
			"exclude_manual_topup":         cfg.ManualTopupExcluded(),
			"max_manual_adjust":            cfg.MaxManualAdjust,
			"compliance_confirmed":         operation_setting.IsPaymentComplianceConfirmed(),
		},
	})
}

// handlePutConfig 保存一批运营参数(稀疏 patch,只传改动键)。
//
// 成功与失败都写审计:三档比例与注册奖决定平台会发出去多少星屑,"谁在什么时候把
// 比例调高了"必须可查,而被拒绝的那次(越界、合规未确认时的邀请类正值)同样是
// 重要信号。
func handlePutConfig(c *gin.Context) {
	if !guard.RequireAPI(c, guard.FlagStardust) {
		return
	}
	// 用 RawMessage 而不是 map[string]string:前端发字符串最安全,但工具、脚本
	// 与历史客户端习惯发数字或布尔,让它们直接 400 只会制造无谓的故障。
	var req map[string]json.RawMessage
	if err := c.ShouldBindJSON(&req); err != nil {
		audit.WriteConfigUpdate(c, audit.ConfigChange{
			Action: configAuditAction, Result: qymodel.ResultFail, Reason: "请求体解析失败",
		})
		respondErr(c, errBadRequest("请求参数不合法"))
		return
	}

	ctx := c.Request.Context()
	before := settingsSnapshot(effectiveCtx(ctx))
	if len(req) == 0 {
		respondOK(c, gin.H{"effective": before})
		return
	}
	// 取每个 JSON 标量的字面量:数字与布尔原样交给 saveOverrides(它认 0/1/true/false),
	// 字符串脱掉引号并去两侧空白。
	patch := make(map[string]string, len(req))
	for key, raw := range req {
		literal := strings.TrimSpace(string(raw))
		if len(literal) >= 2 && literal[0] == '"' && literal[len(literal)-1] == '"' {
			var text string
			if err := common.Unmarshal(raw, &text); err == nil {
				literal = strings.TrimSpace(text)
			}
		}
		patch[key] = literal
	}

	// saveOverrides 负责白名单、区间与合规门三道校验,成功后自己失效本进程快照;
	// 返回的业务错误(qy_sd_bad_setting / qy_sd_compliance_required)可直接回给管理员。
	if err := saveOverrides(ctx, patch, c.GetInt("id")); err != nil {
		audit.WriteConfigUpdate(c, audit.ConfigChange{
			Action: configAuditAction, Result: qymodel.ResultFail, Reason: err.Error(), Before: before,
		})
		respondErr(c, err)
		return
	}
	after := settingsSnapshot(effectiveCtx(ctx))
	audit.WriteConfigUpdate(c, audit.ConfigChange{
		Action: configAuditAction, Result: qymodel.ResultOK, Before: before, After: after,
	})
	respondOK(c, gin.H{"effective": after})
}
