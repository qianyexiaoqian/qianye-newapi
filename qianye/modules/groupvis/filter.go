package groupvis

import (
	"slices"
	"sort"

	"github.com/QuantumNous/new-api/model"
	perfmetrics "github.com/QuantumNous/new-api/pkg/perf_metrics"
)

const (
	// autoGroup 是「自动选择分组」的伪分组名。它是字面量而非某个真实分组的名称,
	// 因此保留它不构成信息泄漏;但它同时是 perf-metrics 汇总口径的一部分,
	// 剔除会让汇总数字与上游对不上。
	autoGroup = "auto"

	// wildcardGroup 是上游 filterPricingByUsableGroups 使用的通配哨兵,
	// 含义为「该模型对任意分组开放」。
	wildcardGroup = "all"
)

// filterPricing 把每条 Pricing 的 EnableGroup 裁剪为「与用户可用分组的交集」。
//
// ⚠️ 内存安全 —— 这是本文件唯一容易写错、且后果最严重的地方:
//
// model.GetPricing() 返回的是包级缓存切片 pricingMap 本身(没有做防御性拷贝),
// 而 model/pricing.go 刷新缓存时把 modelEnableGroups[p.ModelName] = p.EnableGroup,
// 让管理端的模型元数据与它共享同一个底层数组。因此:
//
//   - 原地截断 item.EnableGroup = item.EnableGroup[:n]
//   - 原地覆写 item.EnableGroup[i] = x
//   - 对 item.EnableGroup 直接 append(cap 有余量时会写进共享数组)
//
// 这三种写法都会把「某一个用户的可见范围」写进全局缓存 —— 在 1 分钟 TTL 内
// 所有用户和管理端都会看到被裁剪后的结果,而且是无锁并发写(GetPricing 对返回值
// 不加读锁),-race 下必然报 data race。
//
// 所以这里一律新分配切片:for range 拿到的 item 是结构体值拷贝,给它的 EnableGroup
// 赋一个全新的 slice header 不会触碰原切片,也不会触碰原底层数组。
func filterPricing(pricing []model.Pricing, usableGroup map[string]string, keepAuto bool) []model.Pricing {
	out := make([]model.Pricing, 0, len(pricing))
	for _, item := range pricing {
		visible := make([]string, 0, len(item.EnableGroup)) // ★ 新分配,绝不复用底层数组
		wildcard := false
		for _, g := range item.EnableGroup {
			switch {
			case g == wildcardGroup:
				wildcard = true
			case keepAuto && g == autoGroup:
				visible = append(visible, g)
			default:
				if _, ok := usableGroup[g]; ok {
					visible = append(visible, g)
				}
			}
		}
		if wildcard {
			// 通配模型对任意分组开放,展开成「用户自己的可用分组」:既零泄漏
			// (这些分组本来就在响应的 usable_group 字段里),又让前端的分组区块
			// 能正常渲染 —— 原样下发 "all" 会让前端交集算出空数组,分组信息全丢。
			for g := range usableGroup {
				if !slices.Contains(visible, g) {
					visible = append(visible, g)
				}
			}
		}
		if len(visible) == 0 {
			// 该模型对本用户完全不可用,整条不下发(与上游的行过滤语义一致)。
			continue
		}
		// 顺序稳定化。EnableGroup 源自 types.Set.Items(),是 map 迭代随机序,
		// 而前端卡片用 groups[0] 当主分组展示 —— 不排序的话同一个模型每次定价
		// 缓存刷新都会换一个分组名,用户多刷几次即可枚举出全部分组。
		sort.Strings(visible)
		item.EnableGroup = visible
		out = append(out, item)
	}
	return out
}

// filterGroupKeys 把 perf-metrics 汇总接口的分组白名单收窄为「候选 ∩ 用户可用分组」。
//
// 返回值永远非 nil:model.GetPerfMetricsSummaryBucketsAll 与
// perfmetrics.allowedGroupSet 都把 nil 解读为「不过滤」,一旦退化成 nil,
// 全站分组的样本会被重新汇总进来,泄漏原样回归。
// 空切片则是安全方向(明确表示「一个分组都不许看」),不会翻转语义。
func filterGroupKeys(candidates []string, usableGroup map[string]string, keepAuto bool) []string {
	out := make([]string, 0, len(candidates))
	seen := make(map[string]struct{}, len(candidates))
	for _, g := range candidates {
		if _, dup := seen[g]; dup {
			continue
		}
		if !visibleGroup(g, usableGroup, keepAuto) {
			continue
		}
		seen[g] = struct{}{}
		out = append(out, g)
	}
	// 上游无条件把 auto 拼进候选,理论上不会走到这里;保留兜底是为了让
	// 「keepAuto 时结果必然非空」成为本函数自身的不变量,而不是依赖调用方。
	if keepAuto {
		if _, ok := seen[autoGroup]; !ok {
			out = append(out, autoGroup)
		}
	}
	return out
}

// filterPerfGroups 剔除响应中用户无权查看的分组维度。
//
// 这同时堵住了 GetPerfMetrics 的 ?group= 主动探测:指定无权分组时上游会照常查出
// 该分组的延迟与成功率,经这里整条剔除后返回空数组;不存在的分组同样返回空,
// 因此也没有「有无数据」的侧信道。
func filterPerfGroups(groups []perfmetrics.GroupResult, usableGroup map[string]string, keepAuto bool) []perfmetrics.GroupResult {
	out := make([]perfmetrics.GroupResult, 0, len(groups))
	for _, g := range groups {
		if visibleGroup(g.Group, usableGroup, keepAuto) {
			out = append(out, g)
		}
	}
	return out
}

func visibleGroup(name string, usableGroup map[string]string, keepAuto bool) bool {
	if keepAuto && name == autoGroup {
		return true
	}
	_, ok := usableGroup[name]
	return ok
}
