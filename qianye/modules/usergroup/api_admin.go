package usergroup

import (
	"errors"
	"net/http"
	"strings"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/qianye/guard"
	qymodel "github.com/QuantumNous/new-api/qianye/model"
	"github.com/QuantumNous/new-api/qianye/service/audit"

	"github.com/gin-gonic/gin"
)

// adminGetConfig 返回当前配置 + 可选分组清单。
//
// 下拉选项由后端给,不由前端自己拼:能不能选一个分组取决于它在不在分组倍率
// 表里,而那张表只有后端看得到。前端自己列一遍必然会和后端的校验口径漂移,
// 表现为「下拉里能选,保存时报不存在」。
func adminGetConfig(c *gin.Context) {
	if !guard.RequireAPI(c, guard.FlagCore) {
		return
	}
	configured := currentDefaultGroup()
	options, probeOK := listGroupOptions()

	respond(c, gin.H{
		// 运营配置的原始值,"" 表示未配置。
		"default_group": configured,
		// 此刻真正会落到新用户身上的分组。配置失效时它等于 fallback_group,
		// 前端据此可以直接把「配了但没生效」这件事显示出来。
		"effective_group": effectiveGroup(configured),
		// 配置值当前是否仍然有效。false + default_group 非空 = 分组被删了。
		"configured_valid": configured == "" || groupExists(configured),
		// 不配置(或配置失效)时上游数据库默认值兜底出来的分组。
		"fallback_group": upstreamDefaultGroup,
		"groups":         options,
		// abilities 探测是否成功。false 时 has_channels 一律是"不确定"而非"没有"。
		"channels_probe_ok": probeOK,
	})
}

func effectiveGroup(configured string) string {
	if configured != "" && groupExists(configured) {
		return configured
	}
	return upstreamDefaultGroup
}

type putConfigRequest struct {
	// DefaultGroup 为空串表示取消配置,回到上游默认行为。
	DefaultGroup *string `json:"default_group"`
}

// adminPutConfig 保存默认分组。
//
// 必写审计:这一项决定此后所有新用户能不能用模型,事故复盘时「谁在什么时候
// 把它从 default 改成 vip」是第一个要查的东西。
func adminPutConfig(c *gin.Context) {
	if !guard.RequireAPI(c, guard.FlagCore) {
		return
	}
	var req putConfigRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		badRequest(c, "qy_invalid_param", "请求格式错误")
		return
	}
	if req.DefaultGroup == nil {
		badRequest(c, "qy_invalid_param", "缺少 default_group")
		return
	}

	target := strings.TrimSpace(*req.DefaultGroup)
	switch err := validateDefaultGroup(target); {
	case err == nil:
	case errors.Is(err, errGroupEmptyIsClear):
		// 清空是合法操作,target 保持空串继续往下走。
	default:
		badRequest(c, "qy_usergroup_invalid", err.Error()+": "+target)
		return
	}

	before := currentDefaultGroup()
	if before == target {
		respond(c, gin.H{"default_group": target, "effective_group": effectiveGroup(target)})
		return
	}

	operatorId := c.GetInt("id")
	if err := writeDefaultGroup(target, operatorId); err != nil {
		internalError(c, err)
		return
	}

	audit.Write(c, audit.Entry{
		Category:    qymodel.AuditCategoryConfig,
		Action:      "usergroup.default.update",
		ActorType:   qymodel.ActorAdmin,
		ActorUserId: operatorId,
		ActorName:   c.GetString("username"),
		Result:      qymodel.ResultOK,
		Reason:      "修改新用户默认分组",
		BeforeSnap:  snapshot(before),
		AfterSnap:   snapshot(target),
	})

	respond(c, gin.H{"default_group": target, "effective_group": effectiveGroup(target)})
}

func snapshot(group string) string {
	if group == "" {
		group = "(未配置,回落到 " + upstreamDefaultGroup + ")"
	}
	return group
}

// ───────────────────────────── 响应辅助 ─────────────────────────────
//
// 与扩展其余部分保持同一个信封:{success, message, data},失败时额外带 code
// 供前端做 i18n 映射,不把中文写死在前端。

func respond(c *gin.Context, data any) {
	c.JSON(http.StatusOK, gin.H{"success": true, "message": "", "data": data})
}

func badRequest(c *gin.Context, code, msg string) {
	c.JSON(http.StatusBadRequest, gin.H{"success": false, "code": code, "message": msg})
}

func internalError(c *gin.Context, err error) {
	common.SysError("qianye/usergroup: 接口处理失败: " + err.Error())
	c.JSON(http.StatusInternalServerError, gin.H{
		"success": false, "code": "qy_internal_error", "message": "处理失败,请稍后重试",
	})
}
