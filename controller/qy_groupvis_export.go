package controller

// qy_groupvis_export.go —— 千夜扩展「分组可见性」模块与上游 controller 包的唯一耦合面。
//
// 这是纯新增文件,合并上游时冲突为 0。三个 hook 变量都带 no-op 默认实现,因此:
//   - 调用点是同包调用,连 import 都不用改,每处只加一行;
//   - 扩展未启用(甚至整个 qianye 目录被删掉)时行为与上游完全一致,无需 nil 判断。
//
// 赋值只在 qianye.Init() 里发生一次,早于 HTTP 监听,不存在并发读写窗口。
//
// 铁律:本文件禁止 import 任何 qianye/* 包 —— controller 是被扩展依赖的一方,
// 反向依赖会形成循环。

import (
	"github.com/QuantumNous/new-api/model"
	perfmetrics "github.com/QuantumNous/new-api/pkg/perf_metrics"
)

// QyGroupVisFilterPricing 在上游 filterPricingByUsableGroups 之后再做一次「列裁剪」:
// 把每条 Pricing 的 EnableGroup 与用户可用分组求交集。
//
// 上游那个函数只回答「这个模型要不要显示」,从未回答「这个模型的哪些分组要显示」,
// 于是 EnableGroup(由 abilities 聚合出的全站全量分组)被原样下发。
//
// usableGroup 由调用方(GetPricing)现成算好,这里不重复计算。
var QyGroupVisFilterPricing = func(pricing []model.Pricing, usableGroup map[string]string) []model.Pricing {
	return pricing
}

// QyGroupVisFilterGroupKeys 收窄 perf-metrics 汇总接口的分组白名单。
//
// 实现方必须保证返回值不为 nil:model.GetPerfMetricsSummaryBucketsAll 把 nil 解读为
// 「不过滤」,一旦退化成 nil,全站分组的样本会重新被汇总进来,泄漏原样回归。
//
// userId 与 userGroup 一起传:可用分组现在还包含**该用户买的套餐解锁的分组**,
// 只按用户分组算会把它们滤掉 —— 用户在令牌页选得到、在这一页却一条数据都看不到。
// 取不到时传 0,正好是匿名口径。
var QyGroupVisFilterGroupKeys = func(userId int, userGroup string, groups []string) []string {
	return groups
}

// QyGroupVisFilterPerfGroups 收窄 perf-metrics 明细接口返回的分组维度。
//
// 上游 filterActiveGroups 的过滤基准是 GroupRatio(全站分组事实清单)而非用户白名单,
// 因此 ?group=<无权分组> 能直接探测到该分组的延迟与成功率。
var QyGroupVisFilterPerfGroups = func(userId int, userGroup string, groups []perfmetrics.GroupResult) []perfmetrics.GroupResult {
	return groups
}
