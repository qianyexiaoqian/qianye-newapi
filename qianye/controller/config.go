// Package controller 提供扩展的 HTTP 接口。
//
// 统一响应信封与上游一致:{"success":bool,"message":string,"data":any},
// 非 200 时额外带 "code" 供前端做 i18n 映射。
package controller

import (
	"net/http"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/qianye/config"
	"github.com/QuantumNous/new-api/qianye/db"
	"github.com/QuantumNous/new-api/qianye/guard"

	"github.com/gin-gonic/gin"
)

// GetConfig 是前端的引导端点。
//
// 刻意不走 guard.RequireAPI,并且永远返回 200:扩展被禁用时前端需要拿到
// {"enabled": false} 来静默隐藏所有入口,而不是收到 404 后满屏红色报错。
// 这就是"优雅降级"在前端侧的落点。
//
// 匿名可访问,因此只暴露开关与展示参数,绝不含 DSN、密钥、限额等敏感信息。
func GetConfig(c *gin.Context) {
	cfg := config.Get()
	if !cfg.Enabled {
		ok(c, gin.H{
			"enabled":   false,
			"available": false,
		})
		return
	}

	ok(c, gin.H{
		"enabled":   true,
		"available": db.Available(),
		"features": gin.H{
			"transfer":     cfg.Transfer.Enabled,
			"commission":   cfg.Commission.Enabled,
			"withdraw":     cfg.Withdraw.Enabled,
			"availability": cfg.Availability.Enabled,
			"violation":    cfg.Violation.Enabled,
		},
		"wallet": gin.H{
			"show_transfer_entry":   cfg.Wallet.TransferEntry(),
			"show_commission_entry": cfg.Wallet.CommissionEntry(),
			"show_withdraw_entry":   cfg.Wallet.WithdrawEntry(),
		},
		"log_metrics": gin.H{
			"show_reasoning_effort": cfg.LogMetrics.ReasoningColumn(),
			"show_cache_ratio":      cfg.LogMetrics.CacheRatioColumn(),
			"enable_filter":         cfg.LogMetrics.EnableFilter,
		},
		"withdraw_options": gin.H{
			"methods":          cfg.Withdraw.Methods,
			"fiat_currency":    cfg.Withdraw.FiatCurrency,
			"remark_max_runes": cfg.Withdraw.RemarkMaxRunes,
		},
		"transfer_options": gin.H{
			"min_quota":        cfg.Transfer.MinQuota,
			"max_per_tx_quota": cfg.Transfer.MaxPerTxQuota,
			"recipient_lookup": cfg.Transfer.RecipientLookup,
		},
	})
}

// ───────────────────────────── 响应辅助 ─────────────────────────────

func ok(c *gin.Context, data any) {
	c.JSON(http.StatusOK, gin.H{
		"success": true,
		"message": "",
		"data":    data,
	})
}

func fail(c *gin.Context, status int, code, msg string) {
	c.JSON(status, gin.H{
		"success": false,
		"code":    code,
		"message": msg,
	})
}

func badRequest(c *gin.Context, code, msg string) {
	fail(c, http.StatusBadRequest, code, msg)
}

func serverError(c *gin.Context, err error) {
	common.SysError("qianye: 接口处理失败: " + err.Error())
	fail(c, http.StatusInternalServerError, "qy_internal_error", "处理失败,请稍后重试")
}

// 分页参数。上限 100,防止管理端一次拉爆内存。
func pagination(c *gin.Context) (page, size int) {
	page = intQuery(c, "p", 1)
	if page < 1 {
		page = 1
	}
	size = intQuery(c, "page_size", 20)
	if size < 1 {
		size = 20
	}
	if size > 100 {
		size = 100
	}
	return page, size
}

func intQuery(c *gin.Context, key string, def int) int {
	v := c.Query(key)
	if v == "" {
		return def
	}
	n := 0
	for _, ch := range v {
		if ch < '0' || ch > '9' {
			return def
		}
		n = n*10 + int(ch-'0')
		if n > 1_000_000 {
			return def
		}
	}
	return n
}

// requireCore 是管理端接口的统一前置判断。
func requireCore(c *gin.Context) bool { return guard.RequireAPI(c, guard.FlagCore) }
