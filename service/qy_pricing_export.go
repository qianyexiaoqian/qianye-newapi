package service

import (
	relaycommon "github.com/QuantumNous/new-api/relay/common"
)

// qy_pricing_export.go —— 千夜扩展「模型按分组单独定价」在**结算侧**的唯一挂载点。
//
// 与 relay/helper/qy_pricing_export.go 的分工:那三个挂载点覆盖计价链路
// (PriceData 的产出),本文件只补一处它们够不到的地方 ——
// service/task_billing.go 的 RecalculateTaskQuotaByTokens。
//
// ─────────────────────── 为什么这一处必须单独挂 ───────────────────────
//
// Task 类模型(视频、MJ 等)的扣费分两步:
//
//	预扣  relay/helper.ModelPriceHelperPerCall  → 走 PriceData,已被分组价覆盖
//	结算  service.RecalculateTaskQuotaByTokens  → 直接读 ratio_setting,**不走 PriceData**
//
// 结算这一步拿到实际 token 数后重算金额,再与预扣额补扣/退还。它不经过
// RelayInfo,也不经过 PriceData,所以 relay/helper 的三个挂载点全都够不到。
//
// 后果不是"分组价少覆盖了一条路径"这么轻:预扣按分组价(比如 5 折)、结算按全局价,
// 差额结算会把用户扣到全局价,**分组折扣在任务类模型上等于不存在,而且是以补扣的
// 形式发生的** —— 用户先看到便宜的预扣,再被追扣一笔。这是 AGENTS.md 计费不变量
// 里"预扣与结算必须同口径"直接针对的情形。
//
// ─────────────────────── 为什么签名不带 RelayInfo ───────────────────────
//
// 这条路径跑在任务轮询协程上,RelayInfo 早就随请求结束了。分组从
// task.Group 取(为空时回落 users.group),与预扣当时写进任务的分组同源。
//
// 代价:影子模式的差额统计在这条路径上不落账(note 需要 RelayInfo)。这是有意的 ——
// 影子模式下本函数返回入参原值,与上游行为逐位一致,不产生任何差额;真实模式下
// 才生效。少一条影子统计记录,换不来任何风险。
//
// 默认实现是恒等函数,扩展未安装/未启用时与上游逐位一致,调用点无需任何判断。

// QyGroupTaskRatio 覆盖 Task 异步差额结算所用的模型倍率。
//
// 入参 ratio 是 ratio_setting.GetModelRatio 的返回值(调用点已确认它 > 0);
// group 是本次任务计价所用的分组。返回值语义与入参相同。
//
// 实现方必须与 relay/helper.QyGroupModelRatio 用同一份规则、同一个影子开关,
// 否则预扣与结算又会各按各的口径走 —— 那正是这个挂载点要消灭的东西。
var QyGroupTaskRatio = func(group, modelName string, ratio float64) float64 {
	return ratio
}

// QyGroupTieredSettle 覆盖「阶梯表达式计价」在**结算侧**的分组乘数。
//
// ─────────────────── 为什么这一处也必须单独挂 ───────────────────
//
// 阶梯计价的扣费同样分两步,而两步走的是**两条不同的代码路径**:
//
//	预扣  relay/helper.modelPriceHelperTiered → QyGroupTieredQuota(已覆盖)
//	结算  service.TryTieredSettle → billingexpr.ComputeTieredQuotaWithRequest
//	      ← 从 snap.ExprString **重跑表达式**,只乘 snap.GroupRatio
//
// 结算这一步不读 PriceData、不经过 relay/helper,所以那三个计价挂载点够不到它。
// 后果与 Task 路径那一处完全同形:给分组配了折扣时,预扣按折扣价、结算按原价,
// 差额以**追扣**落到用户头上 —— 用户先看到便宜的预扣,再被补一刀。
// 这正是 AGENTS.md「预扣与结算必须同口径」直指的情形。
//
// grouppricing 包注释里那句「结算侧读 PriceData,所以覆盖三个计价点即覆盖全部扣费」
// 对 tiered 分支**不成立** —— 这条已在该包注释里更正。
//
// ─────────────────── 作用在 before-group 而不是最终值 ───────────────────
//
// 入参是 ActualQuotaBeforeGroup(尚未乘分组倍率的浮点值),与预扣侧
// QyGroupTieredQuota 拿到的是同一个量纲。调用方拿返回值重跑
// `QuotaRoundChecked(before × GroupRatio)` —— 与 billingexpr 内部逐字一致。
//
// 不直接乘最终的 ActualQuotaAfterGroup:那是已经取整过的 int,再乘一次会引入
// 与预扣侧不同的第二次舍入,两边差一个 quota 的账最难查。
//
// 默认实现是恒等函数,扩展未安装/未启用时与上游逐位一致。
var QyGroupTieredSettle = func(info *relaycommon.RelayInfo, quotaBeforeGroup float64) float64 {
	return quotaBeforeGroup
}
