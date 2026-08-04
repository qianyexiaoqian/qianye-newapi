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
	"github.com/QuantumNous/new-api/qianye/httpq"

	"github.com/gin-gonic/gin"
)

// QyLotteryEntryShown 回答"前端要不要渲染娱乐入口"。
//
// 默认只读 YAML;lottery 模块注册时把它换成"YAML + qy_settings 运营覆盖"的
// 合并结果。做成 hook 变量而不是让本包 import 模块,是为了保持 controller 对
// 各模块零依赖 —— 而必须做这一层的理由是:管理端的「在前端显示」开关写的是
// qy_settings,本端点若只读 YAML,运营在界面上关掉入口之后前台照旧显示,
// 那正是本仓反复出现的"以为改了其实没改"。
var QyLotteryEntryShown = func() bool { return config.Get().Lottery.EntryShown() }

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
			"lottery":      cfg.Lottery.Enabled,
			"ticket":       cfg.Ticket.Enabled,
			// group_matrix 只用于**隐藏入口**:管理端页面与上游分组倍率页里那条
			// 指路提示都要看它。不下发的话,功能没开的站点会在上游那一页上
			// 出现一条指向 404 的链接和一句不成立的断言(「下面这些规则整体
			// 不再生效」),而实际上一条都没被接管、规则全部照常生效。
			"group_matrix": cfg.GroupMatrix.Enabled,
		},
		// 娱乐功能的展示开关。show_entry 关掉之后接口仍然可用:已参与的用户
		// 必须还能查自己的记录与已结束活动的证据链,那正是"历史公正查询"。
		"lottery": gin.H{
			"show_entry":   QyLotteryEntryShown(),
			"proof_public": cfg.Lottery.ProofOpen(),
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

// listPaging 是本包所有列表接口的分页口径:?p= / ?page_size=,默认 20、上限 100。
//
// 解析、上界与 offset 换算全部在 qianye/httpq —— 这套逻辑曾经在仓库里有七份
// 各自漂移的拷贝,收敛的理由见该包的包注释。
var listPaging = httpq.Spec{}

// requireCore 是管理端接口的统一前置判断。
func requireCore(c *gin.Context) bool { return guard.RequireAPI(c, guard.FlagCore) }
