# 需求5+6:分组泄漏修复 + 模型可用率监控

# 需求 5 / 需求 6 设计章节

---

## 第一部分 · 需求 5：修复无权（隐藏）分组泄漏

### 5.1 精确根因

#### 泄漏点 A（主因）— `controller/pricing.go:12-34`

```go
// controller/pricing.go:20-32  ← 现状
filtered := make([]model.Pricing, 0, len(pricing))
for _, item := range pricing {
    if common.StringsContains(item.EnableGroup, "all") {
        filtered = append(filtered, item)      // ★ :23  整条 item 原样放行
        continue
    }
    for _, group := range item.EnableGroup {
        if _, ok := usableGroup[group]; ok {
            filtered = append(filtered, item)  // ★ :28  整条 item 原样放行
            break
        }
    }
}
```

`filterPricingByUsableGroups` 只回答了「**这个模型要不要显示**」，从未回答「**这个模型的哪些分组要显示**」。只要某模型在 `default` 下有可用渠道，整条 `Pricing` 就被放行，而它的 `EnableGroup`（来自 `model/pricing.go:361 EnableGroup: groups.Items()`，由 `abilities` 聚合，**与用户完全无关的全量分组集合**）原样序列化成 `enable_groups` 下发。

**精确泄漏行：`controller/pricing.go:23` 与 `controller/pricing.go:28`。**

前端渲染出口（无需修改，后端裁剪后自动干净）：

| 文件:行 | 表现 |
|---|---|
| `web/src/features/pricing/components/model-card.tsx:60 → :82 → :251-253` | `primaryGroup = groups[0]`，**`Set.Items()` 是 map 迭代顺序 → 每次定价缓存刷新（1 分钟 TTL）随机换一个分组名**，用户多刷几次就能枚举出全部隐藏分组 |
| `web/src/features/pricing/components/pricing-columns.tsx:396-407` | `Groups` 列用 `BadgeListCell` 把 `enable_groups` **全部**渲染成 `GroupBadge` |
| `web/src/features/pricing/components/model-details.tsx:445-472` | `ModelBackendProviderSection` 的 `Groups` 单元格全量渲染 |
| `web/src/features/pricing/lib/mock-stats.ts:312, 810` | 按隐藏分组编造限流/性能行 |

反证：`model-details.tsx` 的 `GroupPricingSection` 用 `getAvailableGroups()`（`lib/model-helpers.ts:29-40`，`Object.keys(usableGroup) ∩ enable_groups`）做了二次过滤，所以**价格表不漏**——说明原作者的意图就是「按 usableGroup 裁剪」，只是卡片/表格/详情头部三处漏做，根子在后端 DTO 没裁。

倍率的间接泄漏：`getDisplayGroupRatio()`（`model-helpers.ts:60-95`）遍历 `enable_groups` 取 `group_ratio` 最小值。当前 `group_ratio` 已被 `controller/pricing.go:61-65` 裁剪过，隐藏分组取到 `undefined` 被跳过，**今天不串价**；但这是运气，不是设计——一旦有人补全 `group_ratio`（例如为了修「auto 分组价格」），最低价立刻串到隐藏分组的倍率上。裁剪 `EnableGroup` 同时消除这个隐患。

#### ⚠️ 必须新分配 slice —— 比 GAPS 描述的更严重

`model.GetPricing()` 返回包级缓存切片 `pricingMap`（`model/pricing.go:49, 66-78`）。`for _, item := range pricing` 里 `item` 是**结构体值拷贝**，但 `item.EnableGroup` 的 slice header 指向**共享底层数组**。

而且不止 `pricingMap` 一个引用者：

```go
// model/pricing.go:417-425
modelEnableGroupsLock.Lock()
modelEnableGroups = make(map[string][]string)
for _, p := range pricingMap {
    modelEnableGroups[p.ModelName] = p.EnableGroup   // ★ 与 pricingMap 共享同一底层数组
    modelQuotaTypeMap[p.ModelName] = p.QuotaType
}
modelEnableGroupsLock.Unlock()
```

`modelEnableGroups` 被 `model.GetModelEnableGroups()` 读取，供**管理端** `controller/model_meta.go:209` 使用。所以任何原地 `item.EnableGroup = item.EnableGroup[:n]` 或原地覆写，会在 1 分钟 TTL 内**同时污染模型广场缓存和管理端模型元数据**，且是并发读写（`GetPricing` 无读锁保护返回值）→ data race。

**硬性规则：必须 `make([]string, 0, n)` 新建切片，只赋给 `item`（值拷贝），不得触碰 `pricing[i].EnableGroup`。**

#### 泄漏点 B — `controller/perf_metrics.go:22` 与 `:76-82`

```go
// :22   GetPerfMetricsSummary
activeGroups := append(lo.Keys(ratio_setting.GetGroupRatioCopy()), "auto")

// :76-82  GetPerfMetrics 的后置过滤
func filterActiveGroups(groups []perfmetrics.GroupResult) []perfmetrics.GroupResult {
    activeRatios := ratio_setting.GetGroupRatioCopy()
    return lo.Filter(groups, func(g perfmetrics.GroupResult, _ int) bool {
        _, ok := activeRatios[g.Group]
        return ok || g.Group == "auto"
    })
}
```

过滤基准用的是 **`GroupRatio` 全量表**（全站分组事实清单），不是用户可用分组白名单。`GroupResult.Group`（`pkg/perf_metrics/types.go:36`）会把隐藏分组名返回，前端 `model-details-performance.tsx:269` 直接 `<GroupBadge group={perf.group} size='sm' />` 渲染。

附带：`GetPerfMetrics` 的 `Group: c.Query("group")`（`:57`）**没有归属校验**，可主动探测任意分组的延迟/成功率/TPS——这比被动泄漏名称更严重，等于把隐藏分组的运营数据开放查询。该路由 `router/api-router.go:36` 默认 `HeaderNavModulePublicOrUserAuth("pricing")` → `TryUserAuth()` → **匿名可打**。

#### 泄漏点 C（不改，仅说明）— `router/api-router.go:80`

`userRoute.GET("/groups", controller.GetUserGroups)` 挂在 `selfRoute`（`UserAuth`）**之外**，匿名可访问：`c.GetInt("id")` = 0 → `GetUserGroup(0)` 报错 → `userGroup = ""` → `service.GetUserUsableGroups("")` 返回**全量 `userUsableGroups` 白名单**（`service/group.go:13` 的 `if userGroup != ""` 直接跳过特殊分组处理）。

这**不是本 BUG**：`userUsableGroups` 是运营方主动配置的「面向用户的公开可用分组」，隐藏分组按定义就不该进这张表。列在此处仅为闭环说明，**默认不改**（改了会破坏未登录用户在模型广场看价格的能力）。若客户确实要求「未登录不看倍率」，见 §5.6 建议 3。

#### 明确不是 BUG 的部分（别重复改）

- `controller/pricing.go:60-65` — `group_ratio` 已按 `usableGroup` 删键。✅
- `controller/pricing.go:72` — `usable_group` = `service.GetUserUsableGroups(group)`，本身就是白名单。✅
- `controller/pricing.go:74` — `auto_groups` = `service.GetUserAutoGroup(group)`（`service/group.go:46-55` 已与 usableGroup 取交）。✅
- `controller/model_meta.go:209/258/305` — `modelsRoute` 挂 `middleware.AdminAuth()`（`router/api-router.go:344`），管理员看全量分组是正确的。✅

---

### 5.2 `service/group.go:37` 为什么**不改**

GAPS 说 `service/group.go:37` 杠杆最大、一次堵住 4 个 handler。**在 D2（不新建分组可见性体系）下这个结论不成立：**

`GetUserUsableGroups()` 的返回值**本身就是白名单**（`setting.GetUserUsableGroupsCopy()` 副本 ± `GroupSpecialUsableGroup` 覆盖），里面**不存在需要被过滤掉的隐藏分组**。在这里加 `FilterHiddenGroups` hook 是一个空操作。

真正的泄漏在**不消费这个白名单**的两个下游（`Pricing.EnableGroup` 来自 `abilities`、`perf_metrics.group` 来自 `GroupRatio`）——它们绕过了白名单，所以必须在各自的出口裁剪。

> **保留结论**：`service/group.go:37 return groupsCopy` 之前仍是未来若要做「真·隐藏分组体系」（方案 b：`Hidden` 标记 + 显示名 + 白名单）时的唯一咽喉挂载点。本期 **0 行改动**，写入设计文档备案。

---

### 5.3 最小改动集（后端 3 处调用行 + 2 个新文件）

#### 改动 5-①　`controller/pricing.go:59`（改 1 行，冲突风险 **低**）

```go
// 原:
	pricing = filterPricingByUsableGroups(pricing, usableGroup)
// 改为:
	pricing = qyFilterPricingByUsableGroups(pricing, usableGroup)
```

原 `filterPricingByUsableGroups`（:12-34）**保留不动**（它继续使用 `common.StringsContains`，因此 `common` import 不会变成未使用，零 import churn；Go 允许未被调用的包级函数）。上游若改动该函数体，我们的 diff 不参与冲突。

#### 新文件　`controller/qy_pricing_visibility.go`（纯新增，零冲突）

```go
package controller

import "github.com/QuantumNous/new-api/model"

// qyFilterPricingByUsableGroups 在原 filterPricingByUsableGroups 的「行过滤」之外，
// 追加「列裁剪」：把每条 Pricing 的 EnableGroup 与用户可用分组求交集，
// 消除无权分组名称经 /api/pricing 的 enable_groups 字段泄漏。
//
// 内存安全约束（务必保留）：
//   model.GetPricing() 返回包级缓存 pricingMap，且 model/pricing.go:422 的
//   modelEnableGroups[p.ModelName] 与之共享同一底层数组。因此必须新分配切片，
//   严禁原地截断/覆写 item.EnableGroup 的底层数组。
func qyFilterPricingByUsableGroups(pricing []model.Pricing, usableGroup map[string]string) []model.Pricing {
	if len(pricing) == 0 {
		return pricing
	}
	if len(usableGroup) == 0 {
		return []model.Pricing{}
	}

	filtered := make([]model.Pricing, 0, len(pricing))
	for _, item := range pricing { // item 是值拷贝
		hasAll := false
		visible := make([]string, 0, len(item.EnableGroup)) // ★ 新分配，不复用底层数组
		for _, g := range item.EnableGroup {
			if g == "all" { // 通配哨兵：保留放行语义
				hasAll = true
				continue
			}
			if _, ok := usableGroup[g]; ok {
				visible = append(visible, g)
			}
		}
		if !hasAll && len(visible) == 0 {
			continue // 该模型对本用户完全不可用 → 整条不下发（与原行为一致）
		}
		if hasAll {
			// "all" 表示任意分组可用：把用户自己的可用分组补全，既不泄漏也不丢展示
			for g := range usableGroup {
				if !qyContains(visible, g) {
					visible = append(visible, g)
				}
			}
		}
		sort.Strings(visible) // ★ 顺序稳定化：消除 Set.Items() 的 map 随机序，
		                      //   同时修掉 model-card.tsx:82 primaryGroup 每次刷新乱跳
		item.EnableGroup = visible
		filtered = append(filtered, item)
	}
	return filtered
}

func qyContains(ss []string, s string) bool {
	for _, v := range ss {
		if v == s {
			return true
		}
	}
	return false
}
```

> **`all` 分支的语义变更**：原逻辑把 `"all"` 原样下发，前端 `getAvailableGroups()` 做交集后得到空数组，分组区块直接不渲染。新逻辑把 `"all"` 展开为「用户自己的可用分组」——既零泄漏（这些分组用户本来就在 `usable_group` 里看得到），又让 UI 正常显示。若确认 `abilities.group` 不可能等于 `"all"`（`model/pricing.go:269 groups.Add(ability.Group)` 只写真实分组名，`"all"` 只可能是管理员真把某个分组命名为 `all`），可整段删掉 `hasAll` 分支简化。
>
> **`sort.Strings` 是顺带修的第二个 bug**：卡片的 `primaryGroup` 依赖数组第 0 项，而 `types.Set.Items()`（`types/set.go`）遍历 map 返回随机序，导致同一模型在不同请求里显示不同分组名。排序后稳定。

#### 改动 5-②　`controller/perf_metrics.go:22`（改 1 行，冲突风险 **低**）

```go
// 原:
	activeGroups := append(lo.Keys(ratio_setting.GetGroupRatioCopy()), "auto")
// 改为:
	activeGroups := qyVisibleGroupKeys(c)
```

#### 改动 5-③　`controller/perf_metrics.go:68`（改 1 行，冲突风险 **低**）

```go
// 原:
	result.Groups = filterActiveGroups(result.Groups)
// 改为:
	result.Groups = qyFilterVisiblePerfGroups(c, result.Groups)
```

原 `filterActiveGroups`（:76-82）**保留不动** → `lo` 与 `ratio_setting` 两个 import 仍被使用，零 import churn。

`GetPerfMetrics` 的 `Group: c.Query("group")`（:57）**不需要第三处改动**：探测未授权分组时，`Query` 只会返回该分组的数据，随后被 `qyFilterVisiblePerfGroups` 整条剔除 → `groups: []`。攻击者拿不到任何数据，也无法通过「有无数据」侧信道判断分组是否存在（不存在的分组同样返回空）。

#### 新文件　`controller/qy_perf_visibility.go`（纯新增，零冲突）

```go
package controller

import (
	perfmetrics "github.com/QuantumNous/new-api/pkg/perf_metrics"
	"github.com/QuantumNous/new-api/service"

	"github.com/gin-gonic/gin"
)

// qyUsableGroupsOf 取当前请求者的可用分组白名单。
// TryUserAuth/UserAuth 均经 setDashboardAuthContext（middleware/auth.go:194-207）
// 写入 c.Set("group", user.Group)，匿名时为空串 → 退化为公开白名单，
// 与 controller/pricing.go:44-58 的行为完全一致。
func qyUsableGroupsOf(c *gin.Context) map[string]string {
	return service.GetUserUsableGroups(c.GetString("group"))
}

// qyVisibleGroupKeys 替代 GetPerfMetricsSummary 中的全量 GroupRatio keys。
// 保留 "auto" 伪分组：它是字面量而非真实分组名，不构成信息泄漏，
// 且保持与上游一致的汇总口径（见 pkg/perf_metrics/metrics.go:62-64 的空组兜底）。
func qyVisibleGroupKeys(c *gin.Context) []string {
	usable := qyUsableGroupsOf(c)
	keys := make([]string, 0, len(usable)+1)
	for g := range usable {
		keys = append(keys, g)
	}
	return append(keys, "auto")
}

// qyFilterVisiblePerfGroups 替代 filterActiveGroups，把过滤基准从
// 「全站 GroupRatio」换成「当前用户可用分组白名单」。
func qyFilterVisiblePerfGroups(c *gin.Context, groups []perfmetrics.GroupResult) []perfmetrics.GroupResult {
	usable := qyUsableGroupsOf(c)
	out := make([]perfmetrics.GroupResult, 0, len(groups))
	for _, g := range groups {
		if _, ok := usable[g.Group]; ok || g.Group == "auto" {
			out = append(out, g)
		}
	}
	return out
}
```

> **注意 `GetPerfMetricsSummary` 的 `groups == nil` 语义**：`model.GetPerfMetricsSummaryBucketsAll`（`model/perf_metric.go:104-109`）对 `groups != nil && len(groups) == 0` 直接返回空。`qyVisibleGroupKeys` 永远至少含 `"auto"`，不会退化成 `nil`（`nil` 会变成「不过滤」= 全量泄漏）。**这是必须保住的不变量，写进单测。**

#### 行为变更清单（部署前必须知会运营）

| 场景 | 修复前 | 修复后 |
|---|---|---|
| 匿名访问 `/api/pricing` | `enable_groups` 含全部分组名 | 只含 `userUsableGroups` 白名单内的分组 |
| 匿名访问 `/api/perf-metrics/summary` | 汇总全站 `GroupRatio` 所有分组的样本 | 只汇总白名单分组 + `auto` → **成功率数值会变** |
| 已登录用户访问 `/api/perf-metrics?model=X&group=<无权组>` | 返回该组完整性能数据 | 返回 `groups: []` |
| 卡片 `primaryGroup` | 每次缓存刷新随机变 | 字典序稳定 |

第 2 条是唯一可能引发「数字对不上」工单的变更——建议与需求 6 的新看板同批上线，并在发布说明里写明「模型广场可用率现按你有权的分组统计」。

#### 冲突风险汇总

| 文件 | 改动 | 行数 | 冲突风险 | 理由 |
|---|---|---|---|---|
| `controller/pricing.go` | :59 调用名替换 | 1 | **低** | 单行调用点；上游改的是函数体，不是这一行 |
| `controller/perf_metrics.go` | :22 赋值行替换 | 1 | **低** | 同上 |
| `controller/perf_metrics.go` | :68 赋值行替换 | 1 | **低** | 同上 |
| `controller/qy_pricing_visibility.go` | 新文件 | — | **零** | `qy` 前缀，与上游天然不重名 |
| `controller/qy_perf_visibility.go` | 新文件 | — | **零** | 同上 |

**需求 5 合计：2 个原项目后端文件、3 行（全部是「替换单个调用表达式」，不是插入新逻辑）。**

---

### 5.4 前端：**0 改动**

后端 DTO 裁剪后：
- `model-card.tsx:60/82/251` 拿到的 `enable_groups` 已是交集 → 自动干净且稳定；
- `pricing-columns.tsx:399` 的 `Groups` 列自动干净；
- `model-details.tsx:449` 的 `ModelBackendProviderSection` 自动干净；
- `mock-stats.ts:312/810` 编造的限流/性能行自动只覆盖有权分组；
- `model-details-performance.tsx:269` 的 `GroupBadge` 自动干净。

**明确不做前端过滤**：那是客户端过滤，数据仍在网络响应里（打开 DevTools 就能看），不算修复；而且需要把 `usableGroup` 透传进 card 工厂/column 工厂，改动面反而更大。

---

### 5.5 是否应该提 PR 给上游 — **应该，强烈建议，且分两个 PR**

这是一个**信息泄漏级缺陷**（CWE-200 Exposure of Sensitive Information），不是产品偏好：任何配置了内部/合作方专属分组的部署，分组名称、该分组下有哪些模型、以及（`/api/perf-metrics`）该分组的延迟与成功率，都对**匿名访客**开放。上游 new-api 的用户基数大，值得回馈。

#### PR-1（先提，风险低、纯 bug fix）：`fix(pricing): do not leak unauthorized group names via enable_groups`

- 范围：只改 `controller/pricing.go` 的 `filterPricingByUsableGroups`（直接重写函数体，不用我们的 `qy` 包装层）。
- 正文要点：
  1. **复现**：配置一个不在 `UserUsableGroups` 里的分组（如 `internal`），给它挂一个渠道，该渠道的模型同时在 `default` 下可用 → 匿名 `curl /api/pricing` 即可在 `data[].enable_groups` 里看到 `internal`。
  2. **影响**：分组名 + 分组下的模型清单泄漏；卡片 `primaryGroup` 因 map 随机序会轮流展示各隐藏分组名，可低成本枚举。
  3. **证据引用作者本意**：`web/src/features/pricing/lib/model-helpers.ts:29-40 getAvailableGroups()` 已经在客户端做交集，说明「按 usableGroup 裁剪」本就是设计意图，后端漏做。
  4. **修复**：交集裁剪 + **必须新分配 slice**（附 `model/pricing.go:422 modelEnableGroups` 与 `pricingMap` 共享底层数组的说明，这是 reviewer 最容易忽略的点，也是这个 PR 最有价值的信息）。
  5. **附带修复**：`sort.Strings` 稳定化，解决 `primaryGroup` 抖动。
- 附一个表驱动单测 `controller/pricing_test.go`：含「不得修改入参底层数组」的断言（跑 `-race`）。
- 预期 review 争议点：`"all"` 哨兵的处理。建议在 PR 里问「`abilities.group` 是否可能为 `all`」，给出两个 patch 变体让 maintainer 选。

#### PR-2（PR-1 合入后再提）：`fix(perf-metrics): scope group breakdown to the caller's usable groups`

- 范围：`controller/perf_metrics.go` 的 `:22` 与 `filterActiveGroups`。
- 额外强调 `c.Query("group")` 无归属校验 → 可主动探测任意分组运营数据。
- **必须在 PR 描述里明写 breaking-ish 行为变更**：匿名/低权用户看到的 `success_rate` 数值会变（因为分母口径收窄）。这是 maintainer 最可能卡住的地方，主动说明并提供 `PERF_METRICS_PUBLIC_ALL_GROUPS` 环境变量兜底开关，能显著提高合入概率。

#### 提 PR 的流程建议

1. 两个 PR 都**不带 `qy` 前缀、不带我们的包装层**——上游不会接受为下游 fork 定制的间接层。
2. 我们 fork 里保留包装层（`qyFilterPricingByUsableGroups`），这样：**上游合入后，我们只需把 `:59` 那行改回 `filterPricingByUsableGroups` 并删掉新文件，零冲突退场**。这正是包装层设计的价值。
3. 安全类问题按 new-api 的 `SECURITY.md`（若有）先私下报告；无 `SECURITY.md` 时，可直接开 PR 但**不在标题写 "security/leak"**，正文用中性措辞描述，避免在修复合入前放大攻击面。

---

## 第二部分 · 需求 6：模型可用率监控页（按分组）

### 6.1 数据源抉择（GAPS §三.2(5) 逐条清算）

用户原话：「新增一个模型可用率监控页面（**根据使用记录统计**）按照分组显示每个分组下的模型可用率，当前模型广场是这个模型的全部整体很不准确。」

拆成两个诉求：
- **D-a**：要**按分组**的可用率（现在模型广场卡片上的 `ModelPerfBadge` 用的是 `/api/perf-metrics/summary`，它对所有分组做 `SUM` 汇总 → 一个高流量的 `default` 分组会把 `svip` 的真实状况完全淹没，这就是「整体很不准确」）。
- **D-b**：要一个**独立的监控页**（不是藏在模型详情抽屉里的一块）。

#### 三条候选路线逐条分析

| 路线 | 分子/分母来源 | 致命问题 | 结论 |
|---|---|---|---|
| **R1 `logs` 表** | `type=2` 成功 / `type=5` 失败 | ① `ERROR_LOG_ENABLED` 默认 **`false`**（`common/init.go:196`）→ 分母只剩成功，可用率恒等于 100%；② 强制打开会给 LOG_DB 增加与失败量等量的写入，且 LOG_DB 可能是 ClickHouse；③ `logs` 无 `(group, model, created_at, type)` 覆盖索引（`model/log.go:60-79`），大表上按分组×模型聚合会全表扫；④ 软失败（`other.stream_status.status=="error"`、`totalTokens==0`）混在 `type=2` 里，判别要 JSON 解析 TEXT 列，走不了索引 | **否决** |
| **R2 主库 `perf_metrics`** | `success_count / request_count`，天然带 `(model_name, group, bucket_ts)` 唯一索引 | ① 只有 `success bool`，**没有任何失败原因维度** → 用户 4xx、额度不足、限流、上游 5xx、客户端断开全部混为「失败」，可用率口径不可配置；② 无 `channel_id`；③ 受管理员可关的 `perf_metrics_setting.Enabled` 与 `RetentionDays` 支配，数据随时可能被清；④ 没有跨全部模型的「按 group 分组」查询函数（`GetPerfMetricsSummaryBucketsAll` 只 `GROUP BY model_name, bucket_ts`，**把 group 维度聚掉了**）；⑤ 数据在**主库**，与「独立库」约束冲突 | **降级为兜底/对账源** |
| **R3 自建采样 + 独立库预聚合** | 自己在 relay 采样点分类记账 | 需要 1 个新 hook | ✅ **主方案** |

#### 关于「不能 join」这条

`perf_metrics` 在主库、`logs` 在 LOG_DB（可能 ClickHouse）——**这个约束在 R3 下自动消失**，因为我们根本不 join：采样发生在**进程内存**里，`(group, model, outcome)` 三元组在同一个 `RelayInfo` 上就齐了，落到独立 MySQL 的预聚合表。**跨库 join 是伪问题，只有在事后拼日志时才存在。**

#### 关于「`perf_metrics` 是端到端可用率」

这**恰恰是用户想要的口径**。用户视角的「这个模型在这个分组下能不能用」＝端到端（重试全部失败才算不可用）。渠道级/attempt 级健康度是**运维视角**，属于另一个需求（「哪个渠道挂了」），本期作为管理员选项、默认关闭（§6.4.4）。

#### 关于「`ERROR_LOG_ENABLED` 默认 false」「两类日志 group 语义对不上」

R3 完全绕开。顺带澄清 GAPS 的一处不精确：`processChannelError`（`controller/relay.go:376`）的 `c.GetString("group")` 与 `constant.ContextKeyUsingGroup` **是同一个 key `"group"`**（`constant/context_key.go:52`），`middleware/auth.go:475` 在 TokenAuth 里写入的就是 token/user group。真正的偏差不是「`user.Group` vs `UsingGroup`」，而是：

> **context 里的 `group` 是「auto 解析前」的值（可能就是字面量 `"auto"`），而 `relayInfo.UsingGroup` 会被 `relay/helper/price.go:52-55 HandleGroupRatio` 改写成「auto 解析后的真实分组」。两者在 auto 分组场景下必然不一致。**

R3 统一取 `relayInfo.UsingGroup`（解析后），与 consume log、与 `perf_metrics` 完全同源，三方数字可对账。

#### 最终方案（可落地）

> **主数据源 = 自建采样（R3）**，hook 在 `pkg/perf_metrics/RecordRelaySample` 尾部（1 行），拿到 `*RelayInfo` 全量上下文（含 `LastError`、`StreamStatus`、`ChannelMeta`）→ 内存分类聚合 → 定期 flush 到独立 MySQL 的 `qy_avail_bucket`。
> **兜底/对账源 = 主库 `perf_metrics`（R2）**，通过纯新文件 `model/qy_export.go` 导出一个 `GROUP BY model_name, group` 的查询：
> ① 新库不可用时页面降级为只读兜底（`source: "perf_metrics_fallback"`，标 `degraded: true`）；
> ② 上线首日历史回填；
> ③ 每日自动对账（我们的 `counted` 与 `perf_metrics.request_count` 偏差 > 阈值 → 告警，用于发现 hook 回归）。
> **`logs` 表 = 不用。** 仅在管理员手动触发「深度排查」时按 `request_id` 反查明细（不做统计）。

---

### 6.2 采样点（精确 hook）

#### 唯一必需 hook — `pkg/perf_metrics/metrics.go`

**为什么不是 GAPS 推荐的 `metrics.go:76`（`Record()` 内）**：`Record(sample Sample)` 只拿得到 `Sample{Model, Group, LatencyMs, TtftMs, HasTtft, Success, OutputTokens, GenerationMs}`——**一个裸 bool，没有 error**，无法做任何口径分类。而 `RecordRelaySample(info, success, outputTokens)` 拿得到 `*relaycommon.RelayInfo`，里面有：

- `info.OriginModelName`、`info.UsingGroup`（**auto 解析后**）、`info.UserGroup`、`info.TokenGroup`
- `info.LastError *types.NewAPIError`（`controller/relay.go:233` 在失败时赋值，`:228` 在成功时清空）→ `StatusCode` / `GetErrorCode()` / `GetErrorType()`
- `info.StreamStatus *StreamStatus` → `EndReason`（`timeout`/`client_gone`/`scanner_error`/`eof`/`panic`/`ping_fail`）、`TotalErrorCount()` → **软失败判别，这是比 `perf_metrics` 更强的能力**
- `info.ChannelMeta`（指针嵌入，**可能为 nil**）→ `ChannelId` / `ChannelType`
- `info.StartTime` / `FirstResponseTime` / `IsStream` / `RelayFormat` / `RetryIndex` / `IsChannelTest` / `IsPlayground`

**改动 6-①　`pkg/perf_metrics/metrics.go` import 块（+1 行）**

```go
	qyhook "github.com/QuantumNous/new-api/qianye/hook"
```

**改动 6-②　`pkg/perf_metrics/metrics.go:55`（`RecordRelaySample` 函数体末尾，`Record(Sample{...})` 的 `})` 之后、函数 `}` 之前，+1 行）**

```go
func RecordRelaySample(info *relaycommon.RelayInfo, success bool, outputTokens int64) {
	if info == nil {
		return
	}
	...
	Record(Sample{ ... })                       // :45-54  原有
	qyhook.OnRelaySample(info, success, outputTokens) // ← :55  新增唯一 1 行
}
```

**冲突风险：低。** `pkg/perf_metrics` 是新包、改动频率低；插入点是函数最后一行，上游若在 `Record()` 前后加逻辑不会与这一行冲突。

**采样覆盖面（必须写进文档的局限）**：`RecordRelaySample` 只有 3 个调用点——
- `controller/relay.go:249` — `Relay()` 重试循环结束仍失败（**覆盖所有同步 relay 格式的失败**）
- `service/text_quota.go:541` — 文本/主路径成功
- `service/quota.go:383` — 音频路径成功

因此 **Midjourney / Suno / 视频等异步 task 路径不产生样本**。这与模型广场现有可用率的覆盖面**完全一致**，两个页面数字可比。要覆盖 task 路径需另加 hook，本期不做，页面上对这些模型显示「无数据」而非 0%。

#### 为什么**不做** `controller/relay.go:360 processChannelError` 的 attempt 级 hook（本期）

- **收益**：拿到 `channelError.ChannelId` + 每次尝试的错误 → 渠道级健康度、真实 attempt 成功率。
- **成本**：`controller/relay.go` 是上游最高频改动文件之一（**高冲突**）；attempt 级分母与端到端分母不是一回事，两套数字并列会造成用户困惑；且需求 7（违规检测）已经要占用同一文件的 defer 块预算。
- **决策**：**本期不加**。设计上预留 `qyhook.OnRelayAttempt(c, channelErr, apiErr)` 的空接口与表结构（`qy_avail_channel_bucket`，默认不建表），二期开关打开时只需在 `controller/relay.go:236`（`processChannelError(...)` 调用之后）插 1 行。

  > 若客户明确要「哪个渠道挂了」，用**已有的**渠道自动禁用机制（`service/channel.go:19 DisableChannel` + `common.ChannelStatusAutoDisabled`）+ 渠道测试（`controller/channel-test.go`）更直接，不必重造 attempt 级采样。

#### 打通 model 包私有能力 — 新文件 `model/qy_export.go`（纯新增，零冲突）

`model/perf_metric.go` 现有的查询函数**全都把 group 维度聚掉了**，且 `commonGroupCol`（`model/main.go:33-48` 的方言转义变量）是包私有的。必须在 `model` 包内新建文件导出：

```go
package model

// 需求 6：可用率监控。为 qianye 扩展导出「按 (model, group, bucket) 的 perf_metrics 聚合」，
// 用于 ① 新库不可用时的降级兜底 ② 上线首日历史回填 ③ 每日对账。
// 命名统一 Qy 前缀，规避上游未来同名符号冲突。

type QyPerfGroupBucket struct {
	ModelName    string `json:"model_name"`
	Group        string `json:"group"`
	BucketTs     int64  `json:"bucket_ts"`
	RequestCount int64  `json:"request_count"`
	SuccessCount int64  `json:"success_count"`
	TotalLatencyMs int64 `json:"total_latency_ms"`
}

// QyGetPerfMetricsByGroup 返回主库 perf_metrics 按 (model_name, group, bucket_ts) 的原始行。
// groups 为 nil 表示不过滤；len(groups)==0 直接返回空（与 GetPerfMetricsSummaryAll 语义一致）。
func QyGetPerfMetricsByGroup(startTs, endTs int64, groups []string) ([]QyPerfGroupBucket, error) {
	var rows []QyPerfGroupBucket
	q := DB.Model(&PerfMetric{}).
		Select("model_name, " + commonGroupCol + " as `group`, bucket_ts, request_count, success_count, total_latency_ms").
		Where("bucket_ts >= ? AND bucket_ts <= ?", startTs, endTs)
	if groups != nil {
		if len(groups) == 0 {
			return rows, nil
		}
		q = q.Where(commonGroupCol+" IN ?", groups)
	}
	return rows, q.Order("bucket_ts ASC").Find(&rows).Error
}

// QyGetGroupEnabledModels 暴露「某分组下有可用渠道的模型清单」，
// 用于区分「有渠道但无流量（无数据）」与「该分组根本没有这个模型（不适用）」。
func QyGetGroupEnabledModels(group string) []string { return GetGroupEnabledModels(group) }
```

> `as \`group\`` 的反引号在 PostgreSQL 下会出问题——实际实现里用 `commonGroupCol + " as qy_group"` 并把结构体字段 tag 改成 `gorm:"column:qy_group"`，避开方言差异。

#### 包依赖与**导入环**约束（硬性，实现时必须遵守）

```
service ──→ pkg/perf_metrics ──→ qianye/hook ──→ relay/common
                    └─────────→ model
```

`service` 已经 import `pkg/perf_metrics`（`service/text_quota.go:541`），因此：

- ✅ `qianye/hook` **只能**是零业务依赖的叶子包，仅 import `relay/common`。
- ✅ `qianye/availability`（真实实现）可 import `common`、`model`、`relay/common`、`relaykit/types`、`qianye/config`、`qianye/db`。
- ❌ `qianye/availability` **绝对禁止** import `service`、`controller`、`middleware`、`pkg/perf_metrics`（会成环）。
- ✅ `qianye/controller`（HTTP 层）可以 import `service`（它不被 `pkg/perf_metrics` 反向依赖）。

`qianye/hook/hook.go`：

```go
package hook

import relaycommon "github.com/QuantumNous/new-api/relay/common"

// RelaySampleFunc 在 qianye.Init() 中由 availability 包注册。
// 未注册（扩展未启用 / 配置缺失）时为 nil，OnRelaySample 直接返回 —— 零开销、零副作用。
type RelaySampleFunc func(info *relaycommon.RelayInfo, success bool, outputTokens int64)

var relaySample atomic.Pointer[RelaySampleFunc]

func RegisterRelaySample(f RelaySampleFunc) { relaySample.Store(&f) }

func OnRelaySample(info *relaycommon.RelayInfo, success bool, outputTokens int64) {
	f := relaySample.Load()
	if f == nil || *f == nil || info == nil {
		return
	}
	defer func() { _ = recover() }() // ★ 热路径绝不允许因扩展 panic 影响 relay
	(*f)(info, success, outputTokens)
}
```

**这个 `recover` + `atomic.Pointer` 的组合就是「优雅降级」在需求 6 上的落地：配置缺失 → 从不注册 → 一个 atomic load + nil 判断，纳秒级；实现里出任何 panic → 被吞掉并计入内部错误计数器，relay 无感。**

---

### 6.3 可用率口径定义（分子 / 分母 / 排除项）

#### 6.3.1 结果分类（`Outcome`）

每个端到端样本被分入且仅分入一类：

| Outcome | 判定条件（按顺序短路） | 默认计入分母 | 默认计入分子 |
|---|---|---|---|
| `success` | `success == true` 且未被判为 `soft_fail` | ✅ | ✅ |
| `soft_fail` | `success == true` 但 `info.StreamStatus.EndReason ∈ {timeout, scanner_error, eof, panic, ping_fail}` 或 `TotalErrorCount() > 0` | ✅ | ❌ |
| `client_gone` | `success == true` 但 `EndReason == client_gone`；或错误码为客户端断开 | ❌ **排除** | ❌ |
| `no_channel` | `LastError.GetErrorCode() == ErrorCodeGetChannelFailed` | ✅ | ❌ |
| `timeout` | `LastError` 的 code ∈ `{channel:response_time_exceeded}`，或 `errors.Is(err, context.DeadlineExceeded)`，或 `EndReason == timeout` | ✅ | ❌ |
| `rate_limit` | `LastError.StatusCode == 429` | ✅（可关） | ❌ |
| `upstream_error` | `LastError.StatusCode >= 500`，或 code ∈ `{do_request_failed, bad_response*, empty_response, read_response_body_failed, aws_invoke_error, channel:invalid_key, channel:no_available_key, ...}`，或 `types.IsChannelError(err) == true` | ✅ | ❌ |
| `client_error` | `LastError.StatusCode ∈ [400,499]` 且 code ∈ `{invalid_request, bad_request_body, convert_request_failed, count_token_failed, sensitive_words_detected, access_denied, prompt_blocked, model_not_found}` | ❌ **排除**（可开） | ❌ |
| `quota_error` | code ∈ `{insufficient_user_quota, pre_consume_token_quota_failed}` | ❌ **硬排除** | ❌ |
| `violation` | `service.IsViolationFeeCode(code)` 语义（违规拦截，`ErrorCodeViolationFeeGrokCSAM` 及前缀 `violation_fee.`） | ❌ **硬排除** | ❌ |
| `internal_error` | 其余（`gen_relay_info_failed`、`json_marshal_failed`、`query_data_error`、`update_data_error` …） | ✅ | ❌ |

**可用率 = `success_count / counted_count`**，其中 `counted_count = Σ(计入分母的 outcome)`。被排除的样本**既不在分子也不在分母**，但**必须落库计数**（`excluded_*` 列），否则页面上「为什么这个模型只有 3 次请求」无法解释。

#### 6.3.2 口径的三个硬规则（不做开关）

1. **额度不足 / 令牌预扣失败永不计入**——这是用户钱包问题，不是模型可用性。且 `service/billing_session.go` 的这类错误已带 `types.ErrOptionWithNoRecordErrorLog()`，上游本就认为它不该进错误统计。
2. **违规拦截永不计入**——与需求 7 的 `other.violation_fee == true` 硬排除口径保持一致，理由相同：属于策略命中，不是不可用。
3. **`info.IsChannelTest == true` 的样本永不计入**——`controller/channel-test.go` 的渠道测试会走完整 relay 并计费（`:499-510`），管理员点一次「测试所有渠道」会瞬间灌入几百条样本，把真实业务可用率冲垮。**这是 `perf_metrics` 现在就存在的污染，我们不能继承。**（同理建议排除 `info.IsPlayground`，做成开关，默认**计入**——playground 是真实用户流量。）

#### 6.3.3 可配置开关（YAML，`qianye.yaml`）

```yaml
availability:
  enabled: true                       # 总开关；false 时 hook 不注册，零开销
  bucket_seconds: 300                 # 采样桶粒度，默认 5 分钟
  flush_interval_seconds: 60          # 落库间隔
  retention_days: 15                  # 5 分钟桶保留期
  rollup_enabled: true                # 是否生成小时级 rollup
  rollup_retention_days: 180
  max_series: 20000                   # 内存 (group,model) 基数上限，防脏模型名打爆内存

  # ——— 口径开关（分母）———
  count_soft_stream_failure: true     # 流中途断裂/超时但已返回部分内容 → 算不可用
  count_timeout: true                 # 超时算不可用
  count_no_channel: true              # 该分组下无可用渠道 → 算不可用（这正是「分组下模型不可用」）
  count_rate_limit: false             # 429 默认不算（多为用户自身并发超限）
  count_client_error_4xx: false       # 用户 4xx 默认不算
  count_internal_error: true          # 本平台内部错误算不可用
  count_playground: true              # playground 流量计入
  # 以下两项无开关，永远排除：quota_error / violation / channel_test / client_gone

  # ——— 展示 ———
  expose_request_count_to_user: false # 普通用户只看可用率与量级分档，不看精确请求数
  min_samples_for_display: 20         # 样本 < N 时可用率标记为「样本不足」，不给具体数字
  channel_dimension_enabled: false    # 渠道维度（末次尝试口径，语义有限），默认关
```

**口径指纹**：把上述 `count_*` 开关序列化后取 8 位哈希作为 `definition_id`，随每次查询返回。前端在页面顶部展示「口径 v-a3f9c210」并可展开看具体规则；口径变更时旧桶不会被重算（历史数据用当时的口径），页面在时间轴上画一条口径变更竖线。**没有这个东西，任何人半年后都无法解释历史曲线的跳变。**

---

### 6.4 表结构（独立 MySQL，`qianye/model/`）

金额精度：**本模块不涉及任何金额**，全部是计数器与毫秒累加和，统一 `BIGINT`（`int64`），无 decimal 需求。

#### 6.4.1 主表 `qy_avail_bucket`（5 分钟粒度预聚合）

```go
package qymodel

// QyAvailBucket 是「分组 × 模型 × 时间桶」的可用率预聚合行。
// 采用预聚合而非原始采样表：单节点 QPS 上千时原始表每天上亿行，
// 而 (group, model) 的基数通常 < 500，5 分钟桶 → 每天最多 500*288 = 14.4 万行。
type QyAvailBucket struct {
	Id       int64 `gorm:"primaryKey;autoIncrement" json:"id"`

	// —— 维度（唯一约束三元组）——
	BucketTs  int64  `gorm:"not null;uniqueIndex:uk_qy_avail_dim,priority:1;index:idx_qy_avail_ts_group,priority:1" json:"bucket_ts"`
	GroupName string `gorm:"column:group_name;type:varchar(64);not null;uniqueIndex:uk_qy_avail_dim,priority:2;index:idx_qy_avail_ts_group,priority:2" json:"group"`
	ModelName string `gorm:"type:varchar(128);not null;uniqueIndex:uk_qy_avail_dim,priority:3" json:"model_name"`

	// —— 核心计数（分子/分母）——
	ReqTotal     int64 `gorm:"not null;default:0" json:"req_total"`     // 全部样本，含被排除的
	ReqCounted   int64 `gorm:"not null;default:0" json:"req_counted"`   // ★ 分母
	SuccessCount int64 `gorm:"not null;default:0" json:"success_count"` // ★ 分子

	// —— 失败原因分解（全部落库，含被排除类，用于解释「为什么分母比总数小」）——
	FailSoftStream int64 `gorm:"not null;default:0" json:"fail_soft_stream"`
	FailNoChannel  int64 `gorm:"not null;default:0" json:"fail_no_channel"`
	FailTimeout    int64 `gorm:"not null;default:0" json:"fail_timeout"`
	FailUpstream   int64 `gorm:"not null;default:0" json:"fail_upstream"`
	FailRateLimit  int64 `gorm:"not null;default:0" json:"fail_rate_limit"`
	FailInternal   int64 `gorm:"not null;default:0" json:"fail_internal"`
	ExcClientError int64 `gorm:"not null;default:0" json:"exc_client_error"`
	ExcQuota       int64 `gorm:"not null;default:0" json:"exc_quota"`
	ExcViolation   int64 `gorm:"not null;default:0" json:"exc_violation"`
	ExcClientGone  int64 `gorm:"not null;default:0" json:"exc_client_gone"`
	ExcChannelTest int64 `gorm:"not null;default:0" json:"exc_channel_test"`

	// —— 性能辅助（只累加成功样本，避免超时样本把均值拉飞）——
	LatencySumMs   int64 `gorm:"not null;default:0" json:"latency_sum_ms"`
	LatencyCount   int64 `gorm:"not null;default:0" json:"latency_count"`
	TtftSumMs      int64 `gorm:"not null;default:0" json:"ttft_sum_ms"`
	TtftCount      int64 `gorm:"not null;default:0" json:"ttft_count"`
	OutputTokens   int64 `gorm:"not null;default:0" json:"output_tokens"`
	GenerationMs   int64 `gorm:"not null;default:0" json:"generation_ms"`

	// —— 口径与运维 ——
	DefinitionId string `gorm:"type:varchar(16);not null;default:''" json:"definition_id"` // 写入时的口径指纹
	UpdatedAt    int64  `gorm:"not null;default:0;index:idx_qy_avail_updated" json:"updated_at"`
}

func (QyAvailBucket) TableName() string { return "qy_avail_bucket" }
```

**字段存在理由（逐条）**

| 字段 | 为什么必须有 |
|---|---|
| `BucketTs / GroupName / ModelName` | 需求本体的三个维度；组成唯一索引 → flush 走 `ON DUPLICATE KEY UPDATE col = col + ?` 累加 upsert，天然幂等，多节点并发 flush 安全 |
| `ReqTotal` vs `ReqCounted` | 两者的差就是「被口径排除的量」。没有 `ReqTotal`，页面无法回答「我明明打了 1000 次，为什么只统计了 300 次」 |
| `SuccessCount` | 分子 |
| `Fail*` 六项 | 失败归因是「可用率低了怎么办」的唯一抓手；纯 bool 的 `perf_metrics` 做不到 |
| `Exc*` 五项 | 让排除项**可解释、可审计**；`ExcChannelTest` 单列是为了向管理员证明「你点的渠道测试没污染统计」 |
| `LatencySum/Count` 分离 | 只对成功样本累加，`Count ≠ ReqCounted`，必须独立计数才能算正确均值 |
| `Ttft*` / `OutputTokens` / `GenerationMs` | 与上游 `perf_metrics` 同构，使新页面能同时展示 TTFT/TPS，也让对账逐列可比 |
| `DefinitionId` | 口径变更后历史桶不重算，前端据此在图上标注变更点 |
| `UpdatedAt` | rollup 任务的增量游标；同时是「这个桶是否还在被写」的判据 |

**MySQL 索引长度核算**（utf8mb4，InnoDB DYNAMIC）：`uk_qy_avail_dim` = 8(bigint) + 64×4 + 128×4 = **776 字节** < 3072，安全；即便是老的 767 字节 COMPACT 格式也只差一点，因此 `ModelName` 用 `varchar(128)`（与上游 `perf_metrics.model_name` 一致）而非 191/255。

**索引集合**
- `uk_qy_avail_dim (bucket_ts, group_name, model_name)` UNIQUE — upsert 键
- `idx_qy_avail_ts_group (bucket_ts, group_name)` — 主查询：`WHERE bucket_ts BETWEEN ? AND ? AND group_name IN (?)`，前缀完全命中
- `idx_qy_avail_updated (updated_at)` — rollup 增量扫描
- **不建** `model_name` 单列索引：矩阵查询永远带时间范围，单列索引不会被选中，白白增加写放大

#### 6.4.2 小时级 rollup 表 `qy_avail_bucket_hour`

结构与 `qy_avail_bucket` **完全相同**（同一个 Go struct，`TableName()` 不同，用 GORM 的 `Table("qy_avail_bucket_hour")` 复用），只是 `BucketTs` 对齐到整点。

**存在理由**：5 分钟桶 15 天 ≈ 500×288×15 = 216 万行，查 30 天趋势要扫百万行；小时桶 180 天 = 500×24×180 = 216 万行但查 30 天只扫 36 万行。查询路由规则：

| 请求时间跨度 | 读表 | 返回粒度 |
|---|---|---|
| ≤ 6 小时 | `qy_avail_bucket` + 内存热桶 | 5 分钟 |
| ≤ 48 小时 | `qy_avail_bucket` | 5 分钟或按需降采样到 15/30 分钟 |
| > 48 小时 | `qy_avail_bucket_hour` | 1 小时 |

#### 6.4.3 错误码明细表 `qy_avail_error`（可选，默认开）

```go
type QyAvailError struct {
	Id        int64  `gorm:"primaryKey;autoIncrement"`
	BucketTs  int64  `gorm:"not null;uniqueIndex:uk_qy_avail_err,priority:1;index:idx_qy_avail_err_ts"`
	GroupName string `gorm:"column:group_name;type:varchar(64);not null;uniqueIndex:uk_qy_avail_err,priority:2"`
	ModelName string `gorm:"type:varchar(128);not null;uniqueIndex:uk_qy_avail_err,priority:3"`
	ErrorCode string `gorm:"type:varchar(64);not null;uniqueIndex:uk_qy_avail_err,priority:4"` // types.ErrorCode
	StatusCode int   `gorm:"not null;default:0;uniqueIndex:uk_qy_avail_err,priority:5"`
	Count     int64  `gorm:"not null;default:0"`
}
func (QyAvailError) TableName() string { return "qy_avail_error" }
```

基数受控：`types.ErrorCode` 是**枚举常量**（`relaykit/types/error.go:39-88`，约 30 个），`StatusCode` 有限。仍需**白名单化**：只接受已知 `ErrorCode` 常量集合内的值，未知值统一归入 `"other"`——否则上游若引入动态错误码，会把表打爆。保留期与 5 分钟桶相同（15 天）。

#### 6.4.4 渠道维度表 `qy_avail_channel_bucket`（默认**不启用**）

结构 = `qy_avail_bucket` + `ChannelId int` 加入唯一索引。**只在 `channel_dimension_enabled: true` 时 AutoMigrate 与写入。**

**语义警告（必须在管理端 UI 上原样显示）**：本表的 `ChannelId` 取自 `info.ChannelMeta.ChannelId`，是**最后一次尝试的渠道**，不是 attempt 级归因。一个请求重试 3 个渠道最终失败，只会记在第 3 个渠道头上；前 2 个渠道的失败**不计**。因此它可以用来发现「哪个渠道是最后的稻草」，**不能**用来算渠道成功率。真正的 attempt 级需要 §6.2 里说明的二期 hook。

#### 6.4.5 后台任务租约表（复用扩展公共设施）

```go
type QyTaskLease struct {
	Name      string `gorm:"type:varchar(64);primaryKey"`
	Holder    string `gorm:"type:varchar(128);not null"`   // hostname + pid
	ExpireAt  int64  `gorm:"not null;index"`
	UpdatedAt int64  `gorm:"not null;default:0"`
}
func (QyTaskLease) TableName() string { return "qy_task_lease" }
```

抢锁 SQL（**新库自建租约，不写主库**，遵守架构第 8 条）：

```sql
INSERT INTO qy_task_lease (name, holder, expire_at, updated_at)
VALUES (?, ?, ?, ?)
ON DUPLICATE KEY UPDATE
  holder    = IF(expire_at < VALUES(expire_at) - ?, VALUES(holder), holder),
  expire_at = IF(holder  = VALUES(holder), VALUES(expire_at), expire_at),
  updated_at= VALUES(updated_at);
-- 随后 SELECT holder 校验是否为自己
```

本模块需要租约的任务：`qy_avail_rollup`、`qy_avail_cleanup`、`qy_avail_reconcile`。
**`qy_avail_flush` 不需要租约**——每个节点持有自己的内存桶，必须各自 flush；累加 upsert 保证多节点结果正确合并。

> 该表由扩展的基础设施模块统一提供，本设计只声明使用的任务名，不重复建表。

---

### 6.5 完整 API 清单（前缀 `/api/qy/`）

路由在 `qianye/router.go` 的 `RegisterRoutes(*gin.Engine)` 里注册，挂载于 `main.go:195` 之后、`router.SetRouter` 之前（避开 gzip/Cache/static.Serve 全局中间件污染）。

公共中间件：`middleware.GlobalAPIRateLimit()` + `middleware.RouteTag("qy")`。

| # | Method | Path | 权限 | 说明 |
|---|---|---|---|---|
| 1 | GET | `/api/qy/availability/meta` | `TryUserAuth()` | 口径定义 + 可见分组 + 时间范围选项 |
| 2 | GET | `/api/qy/availability/matrix` | `TryUserAuth()` | **主看板**：分组 × 模型可用率矩阵 |
| 3 | GET | `/api/qy/availability/series` | `TryUserAuth()` | 单 (group, model) 或单 model 全分组的时序 |
| 4 | GET | `/api/qy/availability/errors` | `TryUserAuth()` | 单 (group, model) 的失败原因分布 |
| 5 | GET | `/api/qy/admin/availability/matrix` | `AdminAuth()` | 全分组矩阵（含隐藏分组、含 `auto` 未归属、含全部排除项计数、含精确请求数） |
| 6 | GET | `/api/qy/admin/availability/channels` | `AdminAuth()` | 渠道维度（`channel_dimension_enabled` 为 true 时） |
| 7 | POST | `/api/qy/admin/availability/backfill` | `AdminAuth()` + `CriticalRateLimit()` | 从主库 `perf_metrics` 回填历史 |
| 8 | GET | `/api/qy/admin/availability/reconcile` | `AdminAuth()` | 与主库 `perf_metrics` 的对账报告 |
| 9 | POST | `/api/qy/admin/availability/flush` | `AdminAuth()` | 强制立即 flush 本节点内存桶（排障） |

> **为什么用 `TryUserAuth()` 而非 `UserAuth()`**：与 `/api/pricing`、`/api/perf-metrics` 保持一致——匿名访客拿到「公开白名单分组」的可用率，登录用户拿到自己的可用分组。这样这份数据未来可以直接嵌进公开的模型广场。若客户要求必须登录，把这 4 个端点换成 `middleware.UserAuth()` 即可，前端零改动。

#### 端点 1 `GET /api/qy/availability/meta`

请求：无参数

```jsonc
{
  "success": true,
  "data": {
    "enabled": true,
    "degraded": false,                 // true = 新库不可用，正在用 perf_metrics 兜底
    "definition_id": "a3f9c210",
    "definition": {
      "numerator":   "success",
      "denominator": ["success","soft_fail","no_channel","timeout","upstream_error","internal_error"],
      "excluded":    ["client_error","quota_error","violation","client_gone","channel_test"],
      "notes_i18n_key": "qy_avail_definition_note"
    },
    "groups": [ { "group": "default", "desc": "默认分组", "ratio": 1.0 } ],
    "bucket_seconds": 300,
    "max_range_hours": 720,
    "min_samples_for_display": 20,
    "expose_request_count": false,
    "coverage_note_i18n_key": "qy_avail_coverage_note"   // 「不含 MJ/视频等异步任务」
  }
}
```

#### 端点 2 `GET /api/qy/availability/matrix` ★核心

请求 query：

| 参数 | 类型 | 默认 | 说明 |
|---|---|---|---|
| `hours` | int | 24 | 与 `start_ts/end_ts` 二选一，上限 720 |
| `start_ts` / `end_ts` | int64 | — | Unix 秒 |
| `groups` | string | 全部可见 | 逗号分隔；服务端**强制与可见白名单取交集** |
| `models` | string | 全部 | 逗号分隔，最多 200 个 |
| `q` | string | — | 模型名模糊匹配（服务端过滤，非 SQL LIKE，在聚合后的内存结果上做） |
| `sort` | enum | `availability_asc` | `availability_asc\|availability_desc\|requests_desc\|model_asc` |
| `page` / `page_size` | int | 1 / 50 | 按**模型**分页（分组是列，不分页） |

响应：

```jsonc
{
  "success": true,
  "data": {
    "start_ts": 1753800000, "end_ts": 1753886400,
    "bucket_seconds": 300, "granularity": "5m",
    "source": "qy",                  // "qy" | "perf_metrics_fallback"
    "degraded": false,
    "definition_id": "a3f9c210",
    "groups": [ {"group":"default","desc":"默认分组","ratio":1.0},
                {"group":"vip","desc":"vip分组","ratio":0.8} ],
    "models": ["gpt-5","claude-opus-4-5"],
    "total_models": 137,
    "cells": [
      {
        "group": "default", "model": "gpt-5",
        "availability": 99.42,          // null = 无数据 / 样本不足
        "state": "ok",                  // "ok"|"degraded"|"down"|"low_sample"|"no_data"|"not_offered"
        "counted": null,                // expose_request_count=false 时为 null
        "counted_band": "1k-10k",
        "success": null, "failed": null, "excluded_total": null,
        "top_reason": "upstream_error",
        "avg_latency_ms": 1830, "avg_ttft_ms": 420, "avg_tps": 41.2,
        "spark": [99.8,99.5,98.9,99.4,99.6,99.4],   // 定长 12 点降采样
        "has_channel": true             // 该分组下 abilities 里是否存在该模型
      }
    ],
    "overall": { "availability": 99.31, "counted_band": "10k+", "worst_group": "vip" }
  }
}
```

**`state` 的六态**——空态设计的核心，避免「0% 恐慌」：

| state | 条件 | UI |
|---|---|---|
| `not_offered` | `has_channel == false` 且无样本 | 灰色斜杠格，「该分组未提供此模型」 |
| `no_data` | `has_channel == true` 且 `req_total == 0` | 浅灰点，「时段内无请求」 |
| `low_sample` | `0 < counted < min_samples_for_display` | 浅色 + 问号，「样本不足（n<20）」，**不显示百分比** |
| `ok` | `availability >= 99.0` | 绿 |
| `degraded` | `95.0 <= availability < 99.0` | 黄 |
| `down` | `availability < 95.0` | 红 |

阈值 `ok_threshold` / `degraded_threshold` 进 YAML，默认 99.0 / 95.0。

#### 端点 3 `GET /api/qy/availability/series`

query：`model`（必填）、`group`（可选，缺省=返回所有可见分组各一条线）、`hours`/`start_ts`/`end_ts`、`granularity`（`auto|5m|1h`）

```jsonc
{ "success": true, "data": {
  "model_name": "gpt-5", "granularity": "5m", "definition_id": "a3f9c210",
  "series": [
    { "group": "default",
      "points": [ {"ts":1753800000,"availability":99.4,"counted":812,"success":807,
                   "avg_latency_ms":1780,"avg_ttft_ms":410} ] } ],
  "definition_changes": [ {"ts":1753840000,"from":"a3f9c210","to":"b71e04dd"} ]
}}
```

#### 端点 4 `GET /api/qy/availability/errors`

query：`model`（必填）、`group`（必填）、`hours`/`start_ts`/`end_ts`、`limit`（默认 10）

```jsonc
{ "success": true, "data": {
  "model_name":"gpt-5", "group":"vip", "counted": 4210, "failed": 61,
  "reasons": [
    {"category":"upstream_error","error_code":"bad_response_status_code","status_code":503,
     "count":38,"share":62.3,"counted_in_denominator":true},
    {"category":"timeout","error_code":"channel:response_time_exceeded","status_code":504,
     "count":15,"share":24.6,"counted_in_denominator":true},
    {"category":"client_error","error_code":"invalid_request","status_code":400,
     "count":9,"share":0,"counted_in_denominator":false}
  ]
}}
```

#### 端点 5 `GET /api/qy/admin/availability/matrix`

与端点 2 同形，差异：
- 不做分组白名单裁剪（管理员看全部，包括隐藏分组和 `auto` 未归属行）；
- `counted` / `success` / `failed` / `excluded_*` 全部返回精确值；
- 额外返回每个 cell 的完整 `Exc*` 分解；
- 额外 query `include_unresolved=true` 时把 `group == "auto"` 的未归属样本单列一行。

#### 端点 7 `POST /api/qy/admin/availability/backfill`

```jsonc
// req
{ "start_ts": 1751208000, "end_ts": 1753800000, "overwrite": false }
// resp
{ "success": true, "data": { "rows_read": 42130, "rows_written": 41988, "skipped_existing": 142,
  "note": "回填数据来自主库 perf_metrics，仅含 req_total/counted/success，无失败原因分解" } }
```

回填写入时 `DefinitionId = "legacy"`，前端在这段时间轴上标注「历史数据，口径为上游端到端成功率」。

#### 端点 8 `GET /api/qy/admin/availability/reconcile`

```jsonc
{ "success": true, "data": {
  "window_hours": 24,
  "rows": [ {"group":"default","model":"gpt-5",
             "qy_req_total":18422,"perf_request_count":18455,"delta":-33,"delta_pct":-0.18,
             "qy_success":18311,"perf_success":18344,"status":"ok"} ],
  "threshold_pct": 2.0, "abnormal_count": 0 }}
```

`delta` 的合理来源：桶边界抖动、`IsChannelTest` 被我们排除而 `perf_metrics` 计入。**偏差持续 > 2% 说明 hook 掉了或被上游改动破坏，是回归探针。**

---

### 6.6 关键流程（编号步骤，标出事务边界 / 加锁点 / 幂等键 / 失败回滚）

#### 流程 A — 采样（热路径，**绝对无 IO、无锁竞争**）

1. relay 结束 → `perfmetrics.RecordRelaySample(info, success, tokens)` → 原有 `Record(...)` → **新增第 55 行** `qyhook.OnRelaySample(info, success, tokens)`。
2. `hook` 层：`atomic.Pointer` load，nil 则立即返回（未启用时开销 ≈ 1 次原子读）。有 `defer recover()`。
3. `availability.OnRelaySample`：
   1. **前置排除**：`info.IsChannelTest` → 只累加 `ExcChannelTest` 并返回；`!cfg.CountPlayground && info.IsPlayground` → 同理。
   2. 取维度：`model = info.OriginModelName`（空则丢弃）、`group = info.UsingGroup`（空则记为 `"unknown"`，**不兜底成 `default`**——这是 `pkg/perf_metrics/metrics.go:62-64` 的污染源，我们不继承）。
   3. `classify(info, success)` → `Outcome`（纯函数，无 IO，见 §6.3.1）。
   4. `key = {bucketTs: now - now%bucketSeconds, group, model}`。
   5. `hotBuckets.LoadOrStore(key, &atomicCounters{})` → 对应字段 `atomic.Int64.Add(1)`。**无互斥锁**。
   6. 若 outcome 是失败类且 `cfg.ErrorDetailEnabled`：`errBuckets.LoadOrStore({key, errorCode, statusCode})` → `Add(1)`；**错误码不在白名单则归 `"other"`**。
   7. 成功样本额外累加 `LatencySumMs/LatencyCount/TtftSumMs/TtftCount/OutputTokens/GenerationMs`。
4. **基数保护**：`hotBuckets` 维护一个 `atomic.Int64` 计数；超过 `max_series` 时新 key 一律丢弃并累加 `droppedSeries` 计数器（暴露在端点 9 的响应里）。
5. **事务边界：无。加锁点：无（全 `sync.Map` + `atomic`）。幂等键：不适用（纯累加）。失败回滚：不适用。**

#### 流程 B — flush（每 `flush_interval_seconds`，**每节点独立执行，无租约**）

1. 计算 `currentBucket = align(now)`。
2. `hotBuckets.Range` 遍历**全部**桶（**包括当前未完成桶**）：
   1. `drained := counters.drain()`（`atomic.Swap(0)`，与并发采样安全）。
   2. `drained.ReqTotal == 0` → 若 `bucketTs < currentBucket - 2*bucketSeconds` 则 `hotBuckets.Delete(key)`；`continue`。
   3. 构造 `QyAvailBucket` → 执行**累加 upsert**：
      ```sql
      INSERT INTO qy_avail_bucket (bucket_ts, group_name, model_name, req_total, ..., definition_id, updated_at)
      VALUES (...)
      ON DUPLICATE KEY UPDATE
        req_total = req_total + VALUES(req_total),
        req_counted = req_counted + VALUES(req_counted),
        success_count = success_count + VALUES(success_count),
        ... ,
        definition_id = VALUES(definition_id),
        updated_at = VALUES(updated_at);
      ```
      **幂等键 = 唯一索引 `(bucket_ts, group_name, model_name)`。累加语义使得「同一节点重复 flush 同一份 drained 数据」不会发生（drain 已清零），而「多节点同时 flush」结果正确合并。**
   4. **失败回滚**：upsert 报错 → `counters.addCounters(drained)` 把数据加回内存（照抄 `pkg/perf_metrics/flush.go:53-57` 的模式），累加 `flushFailures`，**不重试、不阻塞**，下一轮 tick 自然重试。
      - 若连续失败次数 > `max_flush_retry_rounds`（默认 30，即约 30 分钟）且内存桶总量 > 上限，**丢弃最旧的桶**并打错误日志——防止新库长期挂掉时 OOM。
3. **事务边界**：每行一个独立语句，**不用事务**。理由：单行累加 upsert 本身原子；包在事务里会拉长锁持有时间、增加死锁面，而我们不需要跨行一致性。
4. **加锁点**：无应用层锁；DB 层是行锁（唯一索引定位），不同 (group, model) 之间无冲突。
5. 错误明细表 `qy_avail_error` 同法 flush。

> **与上游 `pkg/perf_metrics/flush.go` 的关键差异**：上游 `flushCompletedBuckets`（:26-31）**跳过当前未完成桶**，在 `bucket_time: "hour"` 默认配置下意味着 DB 里的数据最多滞后 1 小时。我们**flush 全部桶**——累加 upsert 让部分刷写完全正确，代价只是同一个桶被多次 UPDATE。这是本模块能做到「≤1 分钟新鲜度」的关键。

#### 流程 C — 查询（矩阵）

1. 解析并夹紧参数：`hours ∈ [1, 720]`；`page_size ≤ 200`；`models` 数量 ≤ 200。
2. **权限裁剪**（与需求 5 联动）：`visible := service.GetUserUsableGroups(c.GetString("group"))`；请求的 `groups` 与之取交集；交集为空 → 直接返回空矩阵（**不报错**，避免侧信道）。管理端点跳过此步。
3. 选表：跨度 > 48h → `qy_avail_bucket_hour`，否则 `qy_avail_bucket`。
4. **进程内缓存查询**：key = `hash(表名, startTs对齐, endTs对齐, sorted(groups), definition_id, 是否admin)`，TTL 30 秒，容量 200 条 LRU。命中直接返回。
   > 匿名可访问 + 聚合查询 = 必须缓存，否则是廉价的 DoS 面。
5. 未命中 → 一条 SQL：
   ```sql
   SELECT model_name, group_name,
          SUM(req_total) rt, SUM(req_counted) rc, SUM(success_count) sc,
          SUM(fail_no_channel) fnc, SUM(fail_timeout) ft, SUM(fail_upstream) fu,
          SUM(fail_soft_stream) fss, SUM(fail_rate_limit) frl, SUM(fail_internal) fi,
          SUM(exc_client_error) ece, SUM(exc_quota) eq, SUM(exc_violation) ev,
          SUM(exc_client_gone) ecg, SUM(exc_channel_test) ect,
          SUM(latency_sum_ms) ls, SUM(latency_count) lc,
          SUM(ttft_sum_ms) ts, SUM(ttft_count) tc,
          SUM(output_tokens) ot, SUM(generation_ms) gm
   FROM qy_avail_bucket
   WHERE bucket_ts >= ? AND bucket_ts <= ? AND group_name IN (?)
   GROUP BY model_name, group_name
   ```
   走 `idx_qy_avail_ts_group` 前缀。
6. **叠加本节点热桶**（未 flush 的部分）：`hotBuckets.Range` 内存合并，只在跨度 ≤6h 时做。
   > 多节点部署下这只补上**本节点**的最新 1 分钟，会造成节点间读数轻微不一致。可接受（≤1 分钟）；不接受的话把 `flush_interval_seconds` 调到 15 并关掉热桶合并（配置项 `merge_hot_buckets: true/false`）。
7. 计算 `availability = success / counted * 100`（`counted == 0` → `nil`），套 `state` 六态，套 `min_samples_for_display`。
8. 取 `models` 的 `has_channel`：`model.QyGetGroupEnabledModels(group)`（读主库 abilities 缓存，`model/model_extra.go` 是内存 map，零 IO）。
9. 排序 → 分页（**在内存里对模型维度分页**，因为要先算完可用率才能按可用率排序）。
10. 脱敏：非管理员且 `expose_request_count_to_user == false` → `counted/success/failed` 置 `null`，只给 `counted_band`。
11. **降级路径**：步骤 5 的 DB 报错 → 捕获，改走 `model.QyGetPerfMetricsByGroup(startTs, endTs, groups)`（主库），`source = "perf_metrics_fallback"`、`degraded = true`，失败原因分解全部为 0，前端显示黄条「明细数据源不可用，当前展示为上游汇总口径」。**非热路径，但这里选择 fail-soft 而非 503**，因为这是只读展示，给用户一个降级数字远好过白屏。若主库也失败 → 返回 503 + `qy_avail_unavailable`。

#### 流程 D — rollup（需要租约）

1. 抢租约 `qy_avail_rollup`，TTL 300s，抢不到 → 退出。
2. 读游标（新库 `qy_kv` 或 rollup 表最大 `bucket_ts`）：`lastHour`。
3. 对 `[lastHour, align_hour(now) - 1h]` 每个整点：
   ```sql
   INSERT INTO qy_avail_bucket_hour (...)
   SELECT <hourTs>, group_name, model_name, SUM(req_total), ... , MAX(definition_id), <now>
   FROM qy_avail_bucket
   WHERE bucket_ts >= <hourTs> AND bucket_ts < <hourTs>+3600
   GROUP BY group_name, model_name
   ON DUPLICATE KEY UPDATE req_total = VALUES(req_total), ...   -- ★ 覆盖而非累加
   ```
   **注意：rollup 用「覆盖」语义（`= VALUES(...)`），不是累加**——因为源数据是完整的一小时，重跑必须幂等。**这是与 flush 相反的语义，实现时最容易写错的一处。**
4. 只 rollup **已完成的整点**（`hourTs + 3600 <= now`），避免把半小时的数据固化。
5. 更新游标。租约续期。
6. 失败：不更新游标，下一轮重跑（覆盖语义保证安全）。

#### 流程 E — cleanup（需要租约）

1. 抢租约 `qy_avail_cleanup`（每天跑一次即可）。
2. `DELETE FROM qy_avail_bucket WHERE bucket_ts < ? LIMIT 5000` 循环，每批之间 `sleep 200ms`，最多 200 批/次。**分批 + 限速是硬要求**：一次 `DELETE` 百万行会长时间持有行锁并撑爆 binlog。
3. `qy_avail_error`、`qy_avail_bucket_hour` 同法（不同保留期）。

#### 流程 F — 对账（需要租约，每日）

1. 抢租约 `qy_avail_reconcile`。
2. 取过去 24h：我们的 `SUM(req_total)` by (group, model)  vs  `model.QyGetPerfMetricsByGroup` 的 `SUM(request_count)`。
3. `|delta| / perf > threshold_pct` 的行写入 `qy_avail_reconcile_log`，并在管理端点 8 暴露。
4. 连续 3 天异常 → `common.SysError` 告警（可选接邮件）。

---

### 6.7 并发与边界

#### 竞态

| 竞态 | 处理 |
|---|---|
| 采样 vs flush 的 drain | `atomic.Int64.Swap(0)`。**已知微小丢失窗口**：`drain()` 逐字段 Swap，若在字段之间恰好有并发 `Add`，该次样本的部分字段会落在旧批、部分落在新批。由于两批最终都会累加进同一 DB 行，**总量不丢**，只是极短时间内 `success ≤ counted` 可能不成立。查询侧用 `min(success, counted)` 兜底。 |
| 多节点同时 flush 同一 (bucket, group, model) | 累加 upsert，DB 行锁串行化，结果正确 |
| 多节点同时 rollup | 租约互斥；即便租约失效双跑，覆盖语义保证幂等 |
| `hotBuckets` 基数爆炸（脏 model 名） | `max_series` 硬上限 + 丢弃计数器；model/group 名长度截断到 128/64 |
| 查询缓存击穿 | `singleflight` 合并同 key 的并发查询 |
| 口径热变更 | 口径来自 YAML，只在启动加载；运行期不变。若做热更新，必须在切换时**先强制 flush 全部内存桶**再切 `definition_id`，否则一个桶内混两种口径 |

#### 边界条件

| 边界 | 行为 |
|---|---|
| `counted == 0` | `availability = null`，`state = no_data` / `not_offered`，**不输出 0%** |
| `0 < counted < min_samples` | `state = low_sample`，不输出百分比（避免 1/1 = 100% 或 0/1 = 0% 的误导） |
| `success > counted`（drain 竞态） | 查询侧 `success = min(success, counted)`，并记一次 `SysError` |
| `info.UsingGroup == ""` | 记为 `"unknown"` 分组，**不兜底成 `default`**；`unknown` 只对管理员可见 |
| `info.UsingGroup == "auto"` | 该请求在渠道选择阶段就失败、auto 未解析到具体分组。记为 `"auto"` 分组；**普通用户矩阵里不显示**（它不是可选分组），管理端 `include_unresolved=true` 时单列一行。**绝不摊到各分组** |
| `info.ChannelMeta == nil` | 渠道未选中就失败。`ChannelId = 0`，渠道维度表不写 |
| `info.OriginModelName == ""` | 整条丢弃，累加 `droppedNoModel` |
| 模型名超 128 字符 | 截断到 128 并累加 `truncatedModelName` 计数（不丢样本，因为唯一索引长度所限） |
| 时间跨度请求 > 720h | 夹紧到 720h，响应里回带实际使用的范围 |
| `page_size` 超限 | 夹紧到 200 |
| `groups` 交集为空 | 返回空矩阵 + `groups: []`，HTTP 200（**不返回 403**，防止用分组是否存在做侧信道） |
| 新库连不上（查询） | 降级到主库 `perf_metrics`；两者都挂 → 503 |
| 新库连不上（flush） | 数据回填内存，超上限后丢最旧；**relay 完全无感** |
| 扩展未启用 | hook 未注册；路由未注册 → `/api/qy/**` 走 404（`SetWebRouter` 的 NoRoute 会返回前端 index.html，**必须在 `RegisterRoutes` 里对 `/api/qy/*` 兜一个 404 JSON**，否则前端 axios 会拿到 HTML 报解析错误） |

#### 计数溢出

- 所有计数器 `int64`，`BIGINT`。5 分钟桶内 `req_total` 溢出需要 9.2×10^18 次请求，物理不可能。
- `LatencySumMs`：假设单请求 600 秒上限，5 分钟桶内即使 100 万请求，和 = 6×10^11，远小于 int64。
- **本模块不使用 `common.QuotaFromFloat` / `QuotaRound` / `QuotaFromDecimal`**——AGENTS.md 的约束针对 quota（int32）转换。可用率是 `float64` 百分比，仅用于展示，不参与任何计费。计算时先做 `int64` 除法保护（`if counted <= 0 { return nil }`），再 `float64(success)/float64(counted)*100`，最后 `math.Round(x*100)/100` 保留两位。
- **禁止**把可用率参与任何倍率/价格计算（防止未来有人拿它做动态定价，把 float 误差引入计费）。

---

### 6.8 前端页面

#### 文件清单

**新建（零冲突）**

| 文件 | 内容 |
|---|---|
| `web/src/routes/_authenticated/qy-availability/index.tsx` | 路由：`createFileRoute('/_authenticated/qy-availability/')`，`validateSearch` = zod（`range`/`groups`/`q`/`view`/`model`），**无 `beforeLoad`**（普通用户可见） |
| `web/src/features/qy-availability/index.tsx` | 页面入口 `export function QyAvailability()`，`SectionPageLayout` 骨架 |
| `web/src/features/qy-availability/api.ts` | `getAvailabilityMeta / getAvailabilityMatrix / getAvailabilitySeries / getAvailabilityErrors`，统一 `import { api } from '@/lib/api'` |
| `web/src/features/qy-availability/types.ts` | 与后端 DTO 对齐的 TS 类型 |
| `web/src/features/qy-availability/constants.ts` | `RANGE_OPTIONS`、`STATE_COLORS`、`AVAILABILITY_THRESHOLDS` |
| `web/src/features/qy-availability/hooks/use-availability-data.ts` | `useQuery` 封装，`queryKey: ['qy-availability','matrix', range, groups, q]`，`staleTime: 30_000`，`refetchInterval: 60_000` |
| `web/src/features/qy-availability/components/availability-toolbar.tsx` | 时间范围 Segmented + 分组 `multi-select` + 模型搜索 + 视图切换 Tabs |
| `web/src/features/qy-availability/components/availability-kpi-row.tsx` | 4 个 KPI 卡：整体可用率 / 最差分组 / 低于阈值的模型数 / 被口径排除的请求占比 |
| `web/src/features/qy-availability/components/availability-heatmap.tsx` | **主视图**：CSS Grid 热力图，行=模型、列=分组 |
| `web/src/features/qy-availability/components/availability-table.tsx` | 表格视图，复用 `@/components/data-table` 的 `useDataTable` + `DataTablePage` |
| `web/src/features/qy-availability/components/availability-trend-chart.tsx` | vchart 折线图（每分组一条线） |
| `web/src/features/qy-availability/components/availability-cell-drawer.tsx` | 点击单元格 → `Sheet`：该 (group, model) 时序 + 失败原因横向条形 + 口径说明 |
| `web/src/features/qy-availability/components/availability-definition-popover.tsx` | 口径说明浮层（分子/分母/排除项逐条列出） |
| `web/src/features/qy-availability/lib/format.ts` | `formatAvailability`、`stateOf`、`bandLabel` |
| `web/src/features/qy-availability/__tests__/format.test.ts` | 六态判定与格式化的单测 |

**修改原有文件（2 个）**

| 文件 | 改动 | 行数 |
|---|---|---|
| `web/src/hooks/use-sidebar-data.ts` | 在 `id: 'general'` 分组的 `items` 数组末尾插入一项 | +5 |
| `web/src/i18n/locales/{en,zh,zh-TW,fr,ja,ru,vi}.json` | 追加 `qy_avail_*` 键（或跑 `bun run i18n:sync`） | 追加 |

```tsx
// web/src/hooks/use-sidebar-data.ts，'general' 分组 items 末尾（约 :95 之后）
          {
            title: t('Model Availability'),
            url: '/qy-availability',
            icon: HeartPulse,
          },
```

> 副作用红利：`command-menu.tsx:51` 复用 `useSidebarData()`，⌘K 里自动出现，无需额外改动；`use-sidebar-config.ts:173-176` 对未映射 URL 默认可见，无需改。

#### 交互设计

**顶部工具栏**
- 时间范围：`1h / 6h / 24h / 7d / 30d`（Segmented），URL search 同步（`useTableUrlState` 风格）
- 分组多选（`@/components/multi-select`），选项来自端点 1 的 `groups`（**已是白名单**）
- 模型搜索框（防抖 300ms）
- 视图切换：`热力图 / 表格 / 趋势`（`@/components/ui/tabs`，`TabsList variant='line'`）
- 右侧：口径徽章 `口径 a3f9c210`，点击开 popover 说明分子分母与排除项；以及「数据截至 xx:xx」

**KPI 行**（4 张卡）
整体可用率（大字 + 与上一周期环比箭头）/ 最差分组（名称 + 数值）/ 低于 95% 的模型数 / 被排除请求占比（点击展开排除原因分布）

**热力图（主视图）——不用 vchart，用 CSS Grid**

理由：vchart 的 heatmap 需要额外注册图元、单元格点击/tooltip 定制成本高、SSR/主题切换时机复杂（见 `model-charts.tsx:76-95` 的 ThemeManager 异步加载样板）；而这里的诉求就是「一个可点击、可虚拟滚动的二维彩色表」，`div` + Tailwind 更可控、无额外依赖、移动端更好处理。

```
                 default   vip    svip   ...
gpt-5             99.4%   99.8%   —      
claude-opus-4-5   97.1%   99.9%   99.2%  
gemini-3-pro       n/a    98.3%   99.0%  
```
- 行虚拟化：模型多时用 `@tanstack/react-virtual`（项目已有依赖）
- 单元格颜色分档：`ok` = `bg-emerald-500/15 text-emerald-600`、`degraded` = `bg-amber-500/15`、`down` = `bg-red-500/15`、`low_sample` = `bg-muted text-muted-foreground` + `?`、`no_data` = 空心点、`not_offered` = `bg-transparent` + 斜杠
- **色盲友好**：颜色 + 图标双编码（✓ / ! / ✕ / ? / –），不只靠颜色
- 悬停 tooltip：可用率、样本量级、Top 失败原因
- 点击 → 打开抽屉
- 列头显示分组名 + `GroupBadge`（复用 `@/components/group-badge`，与全站一致）
- 移动端：列数多时横向滚动，`overflow-x: auto`，模型列 `sticky left-0`

**表格视图**
复用 `useDataTable` + `DataTablePage`。列：模型 / 分组 / 可用率（带状态点）/ 请求量级 / 主要失败原因 / 平均延迟 / TTFT / TPS / 12 点 sparkline。支持排序（可用率升序默认——**先看最烂的**）与分页。移动端自动走 `MobileCardList`。

**趋势视图（vchart）**
```tsx
import { VChart } from '@visactor/react-vchart'
import { useChartTheme } from '@/lib/use-chart-theme'
import { VCHART_OPTION } from '@/lib/vchart'

const { themeReady } = useChartTheme()   // 复用现成 hook，无需照抄 model-charts.tsx 的样板
...
{themeReady && <VChart spec={spec} option={VCHART_OPTION} />}
```
spec：`type: 'line'`、`seriesField: 'group'`、`xField: 'ts'`（time 轴）、`yField: 'availability'`、`axes[y].min` 动态（若全部 > 95 则设 `min: 90` 放大波动，否则 `min: 0`）、`crosshair` 开启、`tooltip` 显示各分组当刻可用率与样本数。口径变更点用 `markLine` 画竖线。

**单元格抽屉**（`Sheet`，`sideDrawerContentClassName`）
标题 = `模型名 · 分组Badge`；内容：可用率大数 + 状态、时序小图（同上 spec，单分组）、失败原因横向条形（区分「计入分母」与「已排除」两组，后者灰色并标注「不影响可用率」）、口径说明、「在使用日志中查看该模型」链接（跳 `/usage-logs/common?model=...`）。

**空态**
- 扩展未启用：整页 `EmptyState`，「模型可用率监控未启用，请联系管理员配置」
- 无任何数据：`EmptyState` + 「所选时间范围内没有请求记录」+ 快捷切到 7d
- `degraded == true`：页面顶部黄色 `Alert` 条，「明细数据源暂不可用，当前展示为上游汇总口径，失败原因分解不可用」

**i18n 键**（下划线扁平键，**不能用点号**）
`qy_avail_title` / `qy_avail_subtitle` / `qy_avail_range_1h` … / `qy_avail_state_ok` / `qy_avail_state_degraded` / `qy_avail_state_down` / `qy_avail_state_low_sample` / `qy_avail_state_no_data` / `qy_avail_state_not_offered` / `qy_avail_definition_title` / `qy_avail_definition_note` / `qy_avail_coverage_note` / `qy_avail_reason_upstream_error` … / `qy_avail_degraded_banner` / `qy_avail_empty_disabled` / `qy_avail_empty_no_data`

因为 `STATE_COLORS`、`RANGE_OPTIONS` 里的 label 是常量而非 `t('字面量')` 形式，**必须在 `web/src/i18n/static-keys.ts` 的 `STATIC_I18N_KEYS` 数组登记**（追加，不改现有行）。

**其他前端硬约束**（`web/AGENTS.md`）
- 每个新文件带 AGPL 版权头（`bun run copyright`）
- 组件 props **不解构**，直接 `props.xxx`
- 禁止 2 层以上嵌套三元 → `stateOf()` 用 `switch`/查表
- 改完必跑 `bun run typecheck` 与 `bun run lint`（零 error）
- `routeTree.gen.ts` 自动重写；合并上游冲突时直接删掉重跑 build

---

### 6.9 权限模型

| 维度 | 普通用户 / 匿名 | 管理员（`role >= 10`） |
|---|---|---|
| 可见分组 | `service.GetUserUsableGroups(user.Group)` 的交集（**与需求 5 的修复共用同一个函数，口径天然一致**） | 全部分组，含隐藏分组 |
| `auto` / `unknown` 未归属行 | 不可见 | `include_unresolved=true` 时可见 |
| 精确请求数 | 默认 `null`，只给 `counted_band` 量级分档 | 精确值 |
| 排除项分解 | 只给 `excluded_total` 占比 | 逐类 `Exc*` |
| 渠道维度 | 不可见 | `channel_dimension_enabled` 时可见 |
| 回填 / 对账 / 强制 flush | 无 | 有，且 `CriticalRateLimit()` |
| 路由守卫 | `/qy-availability` 无 `beforeLoad`（`_authenticated` 父路由保证登录） | 管理端功能在同一页内按 `useIsAdmin()` 条件渲染，不单独建页 |

**服务端强制**：所有分组裁剪在 handler 里做，**绝不依赖前端传什么就查什么**。`groups` 参数只用于「在可见集合内进一步收窄」，永远先 `∩ visible`。

**与需求 5 的联动**：`qianye/controller` 与 `controller/qy_perf_visibility.go` 都调用 `service.GetUserUsableGroups(c.GetString("group"))`。三个页面（模型广场、模型详情性能、新可用率页）共用一份可见性口径——**这正是把可见性收敛到单一函数的价值**。若客户日后要做真·隐藏分组体系（GAPS 的方案 b），只需在 `service/group.go:37` 加一个 hook，三处同时生效。

---

## 第三部分 · 原项目改动清单（需求 5 + 需求 6 总账）

### 后端

| # | 文件:行号 | 动作 | 确切内容 | 行数 | 冲突风险 |
|---|---|---|---|---|---|
| 1 | `controller/pricing.go:59` | **改 1 行** | `pricing = filterPricingByUsableGroups(pricing, usableGroup)` → `pricing = qyFilterPricingByUsableGroups(pricing, usableGroup)` | 1 | **低** |
| 2 | `controller/perf_metrics.go:22` | **改 1 行** | `activeGroups := append(lo.Keys(ratio_setting.GetGroupRatioCopy()), "auto")` → `activeGroups := qyVisibleGroupKeys(c)` | 1 | **低** |
| 3 | `controller/perf_metrics.go:68` | **改 1 行** | `result.Groups = filterActiveGroups(result.Groups)` → `result.Groups = qyFilterVisiblePerfGroups(c, result.Groups)` | 1 | **低** |
| 4 | `pkg/perf_metrics/metrics.go` import 块（:11-15 内） | **增 1 行** | `	qyhook "github.com/QuantumNous/new-api/qianye/hook"` | 1 | **低** |
| 5 | `pkg/perf_metrics/metrics.go:55` | **增 1 行** | `	qyhook.OnRelaySample(info, success, outputTokens)`（`RecordRelaySample` 的 `Record(Sample{...})` 之后、函数右花括号之前） | 1 | **低** |

**后端合计：3 个原项目文件、5 行。** 其中 3 行是「替换单个调用表达式」，2 行是纯新增。

**不改的文件（明确记录，避免他人重复动手）**
- `service/group.go` — **0 行**（D2 下该 hook 是空操作，见 §5.2；保留为未来方案 b 的挂载点）
- `router/api-router.go` — **0 行**（新路由全部走 `qianye.RegisterRoutes(server)`）
- `router/main.go` — **0 行**（路由挂在 `main.go:195` 之后）
- `main.go` — **0 行由本模块贡献**（`qianye.Init()` / `RegisterRoutes` / `StartBackgroundTasks` 三处由扩展基础设施模块统一贡献，本模块只在 `qianye.Init()` 内部注册自己的 hook 与后台任务）
- `model/perf_metric.go` — **0 行**（新查询走 `model/qy_export.go`）
- 全部前端 pricing 组件 — **0 行**

**新增文件（零冲突，不计入预算）**
| 文件 | 用途 |
|---|---|
| `controller/qy_pricing_visibility.go` | 需求 5 修复 A 实现 |
| `controller/qy_perf_visibility.go` | 需求 5 修复 B 实现 |
| `controller/qy_pricing_visibility_test.go` | 含 `-race` 下「不得修改入参底层数组」断言 |
| `model/qy_export.go` | 导出 `QyGetPerfMetricsByGroup` / `QyGetGroupEnabledModels`（需 `commonGroupCol` 私有变量） |
| `qianye/hook/hook.go` | 叶子 hook 包（**只能 import `relay/common`**） |
| `qianye/config/availability.go` | YAML 口径与开关 |
| `qianye/model/qy_avail_*.go` | 4 张表的 GORM 模型 |
| `qianye/availability/sample.go` | `OnRelaySample` + `classify` |
| `qianye/availability/aggregate.go` | `sync.Map` 热桶 + `atomicCounters` |
| `qianye/availability/flush.go` | flush / rollup / cleanup / reconcile |
| `qianye/availability/query.go` | 矩阵 / 时序 / 错误分布查询 + 30s 缓存 + singleflight |
| `qianye/controller/qy_availability.go` | 9 个 handler |
| `qianye/router.go` 内追加路由组 | 路由注册（由基础设施模块提供函数体） |

### 前端

| # | 文件 | 动作 | 行数 | 冲突风险 |
|---|---|---|---|---|
| 1 | `web/src/hooks/use-sidebar-data.ts` | 在 `id:'general'` 的 `items` 末尾插入 1 个对象 | +5 | **中**（纯数组追加，冲突易解） |
| 2 | `web/src/i18n/locales/*.json` ×7 | 追加 `qy_avail_*` 键 | 追加 | **高频但纯追加** |
| 3 | `web/src/i18n/static-keys.ts` | 追加常量键登记 | 追加 | 低 |
| 4 | `web/src/routeTree.gen.ts` | **自动重写** | — | 高（冲突时删掉重跑 build） |

**需求 5 前端 0 改动。**

---

## 第四部分 · 我建议补充的（用户未提，标注为「建议」）

**建议 1 — 口径可见性是这个功能成败的关键，不是锦上添花。**
可用率是最容易引发争议的指标。页面必须在**首屏**回答「99.4% 是怎么算出来的」。已在设计里落地为 `definition_id` + 口径 popover + `state` 六态 + `excluded_total` 展示。**如果为了赶工砍掉这部分，上线后会持续收到「为什么我这边失败了这里显示 100%」的工单。**

**建议 2 — SLO 告警与侧边栏红点。**
在 `qy_avail_slo` 表配置「(group, model, 阈值, 连续窗口数)」，后台任务（带租约）判定越界后：① 侧边栏「模型可用率」项挂 `badge`；② 管理员邮件（复用 `common` 的邮件能力）；③ 记录到 `qy_avail_incident` 表形成故障时间线。这是把「看板」升级成「监控」的关键一步，成本约 1 张表 + 1 个后台任务。

**建议 3 — `GET /api/user/groups` 的匿名收口（可选，1 行）。**
`router/api-router.go:80` 挂在 `UserAuth` 之外。当前泄漏的是「公开可用分组」，按定义不算隐藏分组泄漏。若客户要求「未登录不看倍率」，把该行移进 `selfRoute` 块即可（1 行移动，冲突风险中）。**默认不做**，因为会破坏未登录用户在模型广场看价格。

**建议 4 — 采样限流与脏数据防护。**
`max_series` 之外，再加：模型名/分组名的字符白名单（`[A-Za-z0-9._:@/-]`，超出则归入 `"invalid"` 桶）。理由：`abilities.group` 来自渠道配置的自由文本，一个手滑的多行粘贴就能造出上千个「分组」。

**建议 5 — 不要把精确请求数默认给普通用户。**
`expose_request_count_to_user: false` 是默认值的原因：`counted` 精确到个位，等于向所有用户（含竞品）公开平台各模型的真实调用量与增长曲线。这是商业敏感数据。给量级分档（`<100 / 100-1k / 1k-10k / 10k+`）足够支撑「这个数字可不可信」的判断。

**建议 6 — 上线前必须跑一次回填 + 对账。**
新库首日无数据，页面全空会被误判为「功能坏了」。上线流程：① 部署 → ② 调 `POST /api/qy/admin/availability/backfill`（回填近 30 天 `perf_metrics`）→ ③ 调 `GET /api/qy/admin/availability/reconcile` 确认偏差 < 2% → ④ 开放侧边栏入口。写进发布 checklist。

**建议 7 — 把「口径」和「覆盖面」的免责说明做成 i18n 文案，别硬编码。**
必须明说的两条：①「不含 Midjourney / 视频 / 音乐等异步任务」；②「统计的是端到端可用率：一次请求内部重试多个渠道，只要最终成功即计为成功」。这两句话能消掉一半的疑问工单。

**建议 8 — 审计口径变更。**
`availability.count_*` 任一开关变更时，在 `qy_audit_log`（扩展公共审计表）落一条：谁、何时、从什么改到什么、新旧 `definition_id`。理由：可用率一旦被用于对外 SLA 承诺，口径变更就是有法律含义的动作。

**建议 9 — P95 / P99 延迟（二期）。**
当前只有 `LatencySum/Count` → 只能算均值，而均值对长尾完全不敏感。二期加 12 个固定边界的直方图桶（100/200/500/1s/2s/5s/10s/30s/60s/120s/300s/+∞），12 个 `int64` 列，写入零额外成本，可算 P50/P95/P99。**不建议一期做**，会把表宽度从 25 列推到 37 列。

**建议 10 — 模型广场卡片徽章的分组化（可选，成本明确）。**
用户抱怨的「整体很不准确」根源是 `ModelPerfBadge` 用 `/api/perf-metrics/summary` 的跨分组汇总。彻底解决需要：`perf_metrics/summary` 支持 `?group=`（后端 1 处）+ `web/src/features/pricing/index.tsx` 把当前选中分组透传（1 处）+ `model-card-grid.tsx` / `model-perf-badge.tsx`（2 处）。**共 1 后端 + 3 前端原文件**。本期方案（独立页）已完整满足需求原文，此项列为可选增强，需另行批准预算。

**建议 11 — 错误文案统一。**
`qy_avail_unavailable`（503）：「可用率数据服务暂不可用，请稍后重试」；`qy_avail_disabled`（功能未启用）：「模型可用率监控未启用」；`qy_avail_range_clamped`（范围被夹紧）：「最长支持查询 30 天，已自动调整」。三条都走 i18n，不在后端拼中文字符串。

**建议 12 — 单测最小集（写进 DoD）。**
① `qyFilterPricingByUsableGroups`：交集正确 + **入参底层数组未被修改**（`-race`）+ `usableGroup` 为空返回空 + `"all"` 分支；
② `qyVisibleGroupKeys` 永不返回 `nil` 且永不返回空切片（否则 `GetPerfMetricsSummaryBucketsAll` 语义翻转成「不过滤」）；
③ `classify()` 的 11 种 outcome 表驱动测试；
④ flush 的累加 upsert 与 rollup 的覆盖 upsert 语义不能写反（各一个集成测试）。
