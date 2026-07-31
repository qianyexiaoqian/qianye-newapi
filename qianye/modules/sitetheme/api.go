package sitetheme

import (
	"net/http"
	"sort"

	"github.com/QuantumNous/new-api/qianye/guard"
	qymodel "github.com/QuantumNous/new-api/qianye/model"
	"github.com/QuantumNous/new-api/qianye/service/audit"

	"github.com/gin-gonic/gin"
)

func handleGetSiteTheme(c *gin.Context) {
	if !guard.RequireAPI(c, guard.FlagCore) {
		return
	}
	preset, force := Current()
	presets := AllowedPresets()
	sort.Strings(presets)
	c.JSON(http.StatusOK, gin.H{
		"success": true,
		"data": gin.H{
			"default_preset":   preset,
			"force_preset":     force,
			"allowed_presets":  presets,
			"upstream_default": DefaultPreset,
		},
	})
}

type putSiteThemeRequest struct {
	DefaultPreset string `json:"default_preset"`
	ForcePreset   bool   `json:"force_preset"`
}

func handlePutSiteTheme(c *gin.Context) {
	if !guard.RequireAPI(c, guard.FlagCore) {
		return
	}
	var req putSiteThemeRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{
			"success": false, "code": "qy_invalid_param", "message": "请求格式错误",
		})
		return
	}

	before, beforeForce := Current()
	if err := save(req.DefaultPreset, req.ForcePreset, c.GetInt("id")); err != nil {
		if err == ErrUnknownPreset {
			c.JSON(http.StatusBadRequest, gin.H{
				"success": false, "code": "qy_unknown_preset",
				"message": "未知的主题预设:" + req.DefaultPreset,
			})
			return
		}
		c.JSON(http.StatusInternalServerError, gin.H{
			"success": false, "code": "qy_internal_error", "message": "保存失败,请稍后重试",
		})
		return
	}

	// 主题变更影响全站观感,且"谁在什么时候改的"事后需要可查。
	audit.Write(c, audit.Entry{
		Category:    qymodel.AuditCategoryConfig,
		Action:      "site_theme.update",
		ActorType:   qymodel.ActorAdmin,
		ActorUserId: c.GetInt("id"),
		ActorName:   c.GetString("username"),
		Result:      qymodel.ResultOK,
		BeforeSnap:  `{"default_preset":"` + before + `","force_preset":` + boolStr(beforeForce) + `}`,
		AfterSnap:   `{"default_preset":"` + req.DefaultPreset + `","force_preset":` + boolStr(req.ForcePreset) + `}`,
	})

	c.JSON(http.StatusOK, gin.H{
		"success": true,
		"data":    gin.H{"default_preset": req.DefaultPreset, "force_preset": req.ForcePreset},
	})
}
