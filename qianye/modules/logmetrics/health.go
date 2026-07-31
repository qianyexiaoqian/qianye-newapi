package logmetrics

import (
	"net/http"

	"github.com/QuantumNous/new-api/qianye/config"
	"github.com/QuantumNous/new-api/qianye/guard"

	"github.com/gin-gonic/gin"
)

// Health 是本模块的上线自检端点(GET /api/qy/admin/log-metrics/health)。
//
// 存在理由:两个 hook 都是静默 fail-open 的,出错时没有任何用户可见症状。
// 没有这个端点,「两列一直是空的」到底是「没人用思考模型」还是「hook 根本没生效」
// 无法区分,只能靠猜。判读方式:
//
//   - counters.total == 0        → hook 没被调用。检查 qianye/modules.go 的 blank
//     import、config 里 log_metrics 两个开关、以及扩展是否 enabled
//   - total > 0 且 miss == total → hook 正常,只是确实没人用思考模型
//   - panic > 0                  → 探测逻辑遇到了预期外的数据结构,需要排查
//   - anomaly 占比高             → 上游 usage 自相矛盾,缓存率会带警示图标
//
// 计数器是进程内累计值,多节点各报各的,不聚合也不落库。
func Health(c *gin.Context) {
	// 与其余扩展管理端接口保持一致的降级语义。
	// 注意:本模块本身不依赖扩展库,扩展库故障时两列数据照常写入 ——
	// 此处返回 503 只代表「自检接口不可用」,不代表功能失效。
	if !guard.RequireAPI(c, guard.FlagLogMetrics) {
		return
	}
	cfg := config.Get().LogMetrics

	c.JSON(http.StatusOK, gin.H{
		"success": true,
		"message": "",
		"data": gin.H{
			"log_version":     LogVersion,
			"hooks_installed": hooksInstalled.Load(),
			"installed_at":    installedAt.Load(),
			"columns": gin.H{
				"show_reasoning_effort": cfg.ReasoningColumn(),
				"show_cache_ratio":      cfg.CacheRatioColumn(),
				"enable_filter":         cfg.EnableFilter,
			},
			"counters": gin.H{
				"total":           statTotal.Load(),
				"from_relay_info": statFromRelayInfo.Load(),
				"from_request":    statFromRequest.Load(),
				"miss":            statMiss.Load(),
				"cache_basis":     statCacheBasis.Load(),
				"cache_anomaly":   statAnomaly.Load(),
				"panic":           statPanic.Load(),
			},
			// 下发阈值与枚举,让前端不必再维护一份可能漂移的副本。
			"budget_levels": gin.H{
				"minimal": BudgetMinimalMax,
				"low":     BudgetLowMax,
				"medium":  BudgetMediumMax,
				"high":    BudgetHighMax,
			},
			"levels": []string{
				LevelNone, LevelMinimal, LevelLow, LevelMedium, LevelHigh, LevelMax, LevelAuto,
			},
		},
	})
}
