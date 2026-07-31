// Package availability 实现「按分组的模型可用率监控」。
//
// 用户的原话是:模型广场展示的是一个模型的**全站汇总**可用率,很不准确。
// 根因是上游 perf_metrics 的汇总口径把分组维度整个聚掉了 —— 一个高流量的
// default 分组会把 svip 的真实状况完全淹没,而这两个分组背后往往是不同的渠道。
//
// # 为什么自建采样,而不是复用现成的数据
//
//	logs 表:ERROR_LOG_ENABLED 默认 false(common/init.go),失败日志根本不写,
//	         分母只剩成功样本,可用率恒等于 100%;而且 logs 在 LOG_DB(可能是
//	         ClickHouse),与主库的 perf_metrics 不能 join。
//	perf_metrics:只有一个裸 success bool,用户 4xx、额度不足、限流、上游 5xx、
//	         客户端断开全部混为「失败」,口径不可配;没有 channel_id;
//	         而且受管理员可关的 perf_metrics_setting 支配,数据随时可能被清。
//
// 自建采样让「跨库不能 join」这个约束自动消失:采样发生在进程内存里,
// (group, model, outcome) 三元组在同一个 RelayInfo 上就齐了,不存在拼接问题。
//
// # 数据流
//
//	relay 线程  → pkg/perf_metrics.QyOnRelaySample(同包 hook,上游只加一行)
//	            → 同步分类(纯内存,不持有 RelayInfo)
//	            → guard.HotAsync → sync.Map + atomic 累加
//	flush 协程  → 每 flush_interval 把内存热桶累加 upsert 进 qy_avail_bucket(每节点各自跑)
//	rollup 任务 → 整点汇总进 qy_avail_bucket_hour(租约互斥,覆盖语义)
//	cleanup 任务→ 按保留期分批限速删除(租约互斥)
//	查询        → DB 聚合 + 本节点热桶叠加 → 六态判定 → 分组白名单裁剪
//
// 可用率口径(分子、分母、排除项与理由)见 outcome.go 顶部的长注释,
// 它同时通过 /availability/matrix 的 definition 字段下发给前端展示。
package availability

import (
	"github.com/QuantumNous/new-api/middleware"
	"github.com/QuantumNous/new-api/qianye/module"

	"github.com/gin-gonic/gin"
)

type Mod struct{ module.Base }

func (Mod) Name() string { return "availability" }

// Tables 无条件返回:功能开关关闭时也建表,否则打开开关必须重启才能落库。
func (Mod) Tables() []any { return []any{&Bucket{}, &HourBucket{}} }

func (Mod) InstallHooks() { installHooks() }

// RegisterUserRoutes 挂载只读看板接口。
//
// 两个接口都是聚合查询,挂搜索限流:它们比单点读贵得多,而路由组本身
// 只有全局 API 限流,不足以拦住一个刷新循环。
func (Mod) RegisterUserRoutes(g *gin.RouterGroup) {
	g.GET("/availability/matrix", middleware.SearchRateLimit(), getMatrix)
	g.GET("/availability/series", middleware.SearchRateLimit(), getSeries)
}

func (Mod) RegisterAdminRoutes(g *gin.RouterGroup) {
	g.GET("/availability/stats", adminStats)
}

func (Mod) StartTasks() { startTasks() }

func init() { module.Register(Mod{}) }
