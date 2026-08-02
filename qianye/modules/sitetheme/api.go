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
	beforeSnap := `{"default_preset":"` + before + `","force_preset":` + boolStr(beforeForce) + `}`
	afterSnap := `{"default_preset":"` + req.DefaultPreset + `","force_preset":` + boolStr(req.ForcePreset) + `}`
	if err := save(req.DefaultPreset, req.ForcePreset, c.GetInt("id")); err != nil {
		if err == ErrUnknownPreset {
			c.JSON(http.StatusBadRequest, gin.H{
				"success": false, "code": "qy_unknown_preset",
				"message": "未知的主题预设:" + req.DefaultPreset,
			})
			return
		}
		// 落库失败也要留痕:save 内部已经动过库的可能性存在,而只在成功路径
		// 写审计会让"改过但没成功"这一类完全不可见。未知预设那一条不写 ——
		// 它在写库之前就被挡下,库的状态一定没变,记下来只是噪音。
		audit.WriteConfigUpdate(c, audit.ConfigChange{
			Action: "site_theme.update",
			Result: qymodel.ResultFail,
			Reason: "保存站点主题失败: " + err.Error(),
			Before: beforeSnap,
			After:  afterSnap,
		})
		c.JSON(http.StatusInternalServerError, gin.H{
			"success": false, "code": "qy_internal_error", "message": "保存失败,请稍后重试",
		})
		return
	}

	// 主题变更影响全站观感,且"谁在什么时候改的"事后需要可查。
	audit.WriteConfigUpdate(c, audit.ConfigChange{
		Action: "site_theme.update",
		Result: qymodel.ResultOK,
		Before: beforeSnap,
		After:  afterSnap,
	})

	c.JSON(http.StatusOK, gin.H{
		"success": true,
		"data":    gin.H{"default_preset": req.DefaultPreset, "force_preset": req.ForcePreset},
	})
}
