# 模型广场与分组可见性 BUG

# 模型广场 + 分组可见性 — 勘察报告

## 一、前端「模型广场」(Model Square / Pricing)

### 路由挂载
- `web/src/routes/pricing/index.tsx:39` — `createFileRoute('/pricing/')`,`component: Pricing`。`beforeLoad` 调 `getFreshModuleAccess('pricing')`(`web/src/lib/nav-modules.ts`),模块被关或需登录时 redirect。
- `web/src/routes/pricing/$modelId/index.tsx` — 单模型详情页。
- 路由是 **TanStack 文件式路由**,`web/src/routeTree.gen.ts` 自动生成 → 新增页面只需新建文件,**零改动原有文件**。

### 组件树(全部在 `web/src/features/pricing/`)
```
index.tsx  (export function Pricing)            ← 页面容器
├─ hooks/use-pricing-data.ts   usePricingData() ← 唯一数据源, GET /api/pricing
├─ hooks/use-filters.ts        useFilters()
├─ components/pricing-sidebar.tsx  (分组/厂商/标签 筛选器, 显示 groupRatios)
├─ components/pricing-toolbar.tsx  (同上, 移动端)
├─ components/model-card-grid.tsx → model-card.tsx      ← 卡片视图
├─ components/pricing-table.tsx   → pricing-columns.tsx ← 表格视图
└─ components/model-details.tsx   (ModelDetailsDrawer)  ← 详情抽屉
   ├─ model-details-api.tsx        (含 buildRateLimits 按分组编造限流)
   ├─ model-details-performance.tsx(GET /api/perf-metrics, 按分组展示)
   └─ GroupPricingSection / AutoGroupChain / ModelBackendProviderSection
```

### 展示的内容
| 内容 | 位置 |
|---|---|
| 模型名/图标/厂商/标签/端点 | `model-card.tsx`, `pricing-columns.tsx` |
| **分组名 badge** | `model-card.tsx:60,82,251-253` (`primaryGroup`);`pricing-columns.tsx:396-407`(`enable_groups` 列);`model-details.tsx:449,468-472`(`ModelBackendProviderSection` 的 Groups 单元格) |
| **分组倍率** | `pricing-sidebar.tsx` `groupRatios` + `formatGroupRatio()`(`x1.5`);`model-details.tsx` `GroupPricingSection`(每分组一行价格 + `{ratio}x`) |
| 自动分组链 | `model-details.tsx:787-812` `AutoGroupChain` |
| 分组性能 | `model-details-performance.tsx:~170` 按 `group.group` 渲染 `<GroupBadge>` |
| 分组限流 | `model-details-api.tsx:670` `buildRateLimits(props.model)` → `lib/mock-stats.ts:809-810` 用 `enable_groups` |

分组标签统一组件:`web/src/components/group-badge.tsx` → `GroupBadge({group, ratio, label})`。

---

## 二、后端接口

### 主接口 `GET /api/pricing`
- 注册:`router/api-router.go:34`
  ```go
  apiRouter.GET("/pricing", middleware.HeaderNavModuleAuth("pricing"), controller.GetPricing)
  ```
  `middleware/header_nav.go:104 HeaderNavModuleAuth` 默认 `RequireAuth=false` → 走 `TryUserAuth()`,**匿名可访问**。
- Handler:`controller/pricing.go:36 func GetPricing(c *gin.Context)`(完整代码见下)
- 辅助:`controller/pricing.go:12 func filterPricingByUsableGroups(pricing []model.Pricing, usableGroup map[string]string) []model.Pricing`

### 返回 DTO
`controller/pricing.go:67-76`:
```go
c.JSON(200, gin.H{
    "success":            true,
    "data":               pricing,                        // []model.Pricing
    "vendors":            model.GetVendors(),             // []model.PricingVendor
    "group_ratio":        groupRatio,                     // map[string]float64
    "usable_group":       usableGroup,                    // map[string]string (group -> desc)
    "supported_endpoint": model.GetSupportedEndpointMap(),
    "auto_groups":        service.GetUserAutoGroup(group), // []string
    "pricing_version":    "a42d372ccf0b5dd13ecf71203521f9d2",
})
```

`model.Pricing` 结构体定义在 `model/pricing.go:18-39`,关键字段:
```go
type Pricing struct {
    ModelName              string   `json:"model_name"`
    ...
    ModelRatio             float64  `json:"model_ratio"`
    ModelPrice             float64  `json:"model_price"`
    CompletionRatio        float64  `json:"completion_ratio"`
    EnableGroup            []string `json:"enable_groups"`   // ★ 泄漏点
    SupportedEndpointTypes []constant.EndpointType `json:"supported_endpoint_types"`
    ...
}
```
`EnableGroup` 的来源:`model/pricing.go:261-270, 358-363` — 由 `abilities` 表聚合出「该模型在哪些分组下有可用渠道」,**是全量分组集合,与用户无关**。

### 其他相关接口
| 路由 | 文件:行 | 鉴权 | 泄漏风险 |
|---|---|---|---|
| `GET /api/user/groups` | `router/api-router.go:80` → `controller/group.go:26 GetUserGroups` | **无中间件**(在 `selfRoute` 之外),匿名可打 | 返回 `{group: {ratio, desc}}`,按 `userUsableGroups` 过滤;匿名时 userGroup="" → 返回全部配置的可用分组名+倍率 |
| `GET /api/user/self/groups` | `router/api-router.go:88` | UserAuth | 同上 |
| `GET /api/group/` | `router/api-router.go:308-310` → `controller/group.go:14 GetGroups` | AdminAuth | 安全 |
| `GET /api/perf-metrics`、`/summary` | `router/api-router.go:36-39` → `controller/perf_metrics.go:14,38` | `HeaderNavModulePublicOrUserAuth("pricing")`,默认匿名 | **★ 泄漏**,见第四节 |
| `GET /api/ratio_config` | `router/api-router.go:57` → `controller/ratio_config.go:11` | 无鉴权(默认开关关闭) | 只暴露 model_ratio 等,**不含分组** |

---

## 三、分组(group)的定义与可见性

### 3.1 现有的分组数据结构(项目已有)

**A. `setting/ratio_setting/group_ratio.go`** — 分组倍率与特殊可用分组
```go
// :30-34
type GroupRatioSetting struct {
    GroupRatio              *types.RWMap[string, float64]            `json:"group_ratio"`
    GroupGroupRatio         *types.RWMap[string, map[string]float64] `json:"group_group_ratio"`
    GroupSpecialUsableGroup *types.RWMap[string, map[string]string]  `json:"group_special_usable_group"`
}
var groupRatioSetting GroupRatioSetting          // :36
config.GlobalConfig.Register("group_ratio_setting", &groupRatioSetting)  // :51
```
关键函数:
- `GetGroupRatioCopy() map[string]float64` (:62) — **全站分组的事实清单**
- `ContainsGroupRatio(name string) bool` (:66)
- `GetGroupRatio(name string) float64` (:79)
- `GetGroupGroupRatio(userGroup, usingGroup string) (float64, bool)` (:88)
- `GetGroupRatioSetting() *GroupRatioSetting` (:54)

默认值:`defaultGroupRatio = {"default":1,"vip":1,"svip":1}` (:12-16)

**B. `setting/user_usable_group.go`** — 用户可用分组白名单 + 显示名
```go
var userUsableGroups = map[string]string{        // :10-13   group -> 中文显示名
    "default": "默认分组",
    "vip":     "vip分组",
}
func GetUserUsableGroupsCopy() map[string]string      // :16
func UserUsableGroups2JSONString() string             // :27
func UpdateUserUsableGroupsByJSONString(string) error // :38
func GetUsableGroupDescription(groupName string) string // :46
```
持久化 key:`common.OptionMap["UserUsableGroups"]`(`model/option.go:148`,写回 `model/option.go:554-555`)。

**C. `setting/auto_group.go`** — 自动分组链
```go
var autoGroups = []string{"default"}   // :7-9
var DefaultUseAutoGroup = false        // :11
func ContainsAutoGroup(group string) bool  // :13
func GetAutoGroups() []string              // :35
```

**D. 用户所属分组**:`model/user.go:98` `Group string \`json:"group" gorm:"type:varchar(64);default:'default'"\``,读取入口 `model/user.go:1167 func GetUserGroup(id int, fromDB bool) (group string, err error)`。

### 3.2 「隐藏分组」——**项目里不存在这个概念**
全仓 `grep -rni "hidden.*group|group.*hidden|invisible|visible" --include=*.go` 无任何分组可见性相关命中(只有 rate-limit / claude handler 里的无关注释)。

**没有** `GroupRatio` 之外的分组元数据表:没有 groups 表、没有显示名字段(除了 `userUsableGroups` 的 value 当描述用)、没有 `is_public` / `sort` / `color` 等。分组是「散落在 3 个 map 里的字符串 key」,不是实体。

现有最接近「隐藏」的机制是:**不把分组写进 `UserUsableGroups`**,或用 `GroupSpecialUsableGroup` 的 `-:` 前缀按用户分组移除:
```go
// service/group.go:18-29
if strings.HasPrefix(specialGroup, "-:") {      // 移除分组
    groupToRemove := strings.TrimPrefix(specialGroup, "-:")
    delete(groupsCopy, groupToRemove)
} else if strings.HasPrefix(specialGroup, "+:") { // 添加分组
    ...
}
```

### 3.3 用户可见/可用分组的判定(现有函数)
`service/group.go`:
```go
func GetUserUsableGroups(userGroup string) map[string]string           // :11  ★核心
func GroupInUserUsableGroups(userGroup, groupName string) bool         // :40
func GetUserAutoGroup(userGroup string) []string                       // :46
func GetGroupsEnabledModels(groups []string) []string                  // :58
func GetUserGroupRatio(userGroup, group string) float64                // :75
```
`GetUserUsableGroups` 逻辑:`userUsableGroups` 全量副本 → 叠加 `GroupSpecialUsableGroup[userGroup]` 的 ±覆盖 → 若用户自身分组不在里面,补一条 `groupsCopy[userGroup] = "用户分组"` (:33-35)。
**注意匿名用户**:`userGroup == ""` → 直接返回全量 `userUsableGroups`。

---

## 四、BUG 定位:后端未按分组可见性过滤

### ★ 根因 1(主因):`Pricing.EnableGroup` 原样下发,从未按可用分组裁剪

`controller/pricing.go:20-32`:
```go
filtered := make([]model.Pricing, 0, len(pricing))
for _, item := range pricing {
    if common.StringsContains(item.EnableGroup, "all") {
        filtered = append(filtered, item)     // ← 第 23 行:整条 item 原样 append
        continue
    }
    for _, group := range item.EnableGroup {
        if _, ok := usableGroup[group]; ok {
            filtered = append(filtered, item) // ← 第 28 行:整条 item 原样 append
            break
        }
    }
}
```
**`filterPricingByUsableGroups` 只决定「这个模型要不要显示」,完全没有重写 `item.EnableGroup`。** 只要某模型在 `default` 分组下可用(用户可见),整条记录就被放行,而它的 `EnableGroup` 里同时携带着 `svip`、`internal`、任何运营方想隐藏的分组名,原样序列化成 `enable_groups` 发给前端。

**精确泄漏行:`controller/pricing.go:23` 与 `controller/pricing.go:28`**(等价地说:缺少一步 `item.EnableGroup = 交集(item.EnableGroup, usableGroup)`)。

前端因此直接把隐藏分组名画出来:
- `web/src/features/pricing/components/model-card.tsx:60 → 82 → 251-253`
- `web/src/features/pricing/components/pricing-columns.tsx:396-407`
- `web/src/features/pricing/components/model-details.tsx:449 → 468-472`
- `web/src/features/pricing/lib/mock-stats.ts:312, 810`(按隐藏分组编造限流/性能行)

反过来,`model-details.tsx` 的 `GroupPricingSection` 用 `getAvailableGroups()`(`lib/model-helpers.ts:29-40`,`Object.keys(usableGroup) ∩ enable_groups`)做了二次过滤,所以**价格表不漏**——这恰好证明了原作者的意图是「按 usableGroup 过滤」,只是卡片/表格/详情头部三处漏做,而根子在后端没在 DTO 层裁剪。

### 根因 1 的副作用:倍率也间接泄漏
`web/src/features/pricing/lib/model-helpers.ts:60-95 getDisplayGroupRatio()` 遍历 `model.enable_groups` 取 **最小倍率** 作为卡片显示价。虽然 `group_ratio` map 已被后端裁剪(见下),`groupRatio[隐藏分组]` 会是 `undefined` 而被跳过,所以价格本身不会串;但一旦将来有人补上 `group_ratio`,就会直接串价。

### 根因 2:`/api/perf-metrics` 按 GroupRatio 而非用户可用分组过滤
`controller/perf_metrics.go`:
```go
// :22
activeGroups := append(lo.Keys(ratio_setting.GetGroupRatioCopy()), "auto")
// :76-82
func filterActiveGroups(groups []perfmetrics.GroupResult) []perfmetrics.GroupResult {
    activeRatios := ratio_setting.GetGroupRatioCopy()
    return lo.Filter(groups, func(g perfmetrics.GroupResult, _ int) bool {
        _, ok := activeRatios[g.Group]
        return ok || g.Group == "auto"
    })
}
```
`GroupResult.Group` (`pkg/perf_metrics/types.go:36` `json:"group"`) 会把 **所有在 GroupRatio 里配置过的分组名** 返回,包括隐藏分组;前端 `model-details-performance.tsx` 直接 `<GroupBadge group={row.group}>` 渲染。此外 `GetPerfMetrics` 的 `c.Query("group")` (:57) 未做归属校验,可主动探测任意分组数据。该路由默认匿名可访问(`router/api-router.go:36`)。

### 已经正确过滤的部分(不是 bug,别重复改)
`controller/pricing.go:60-65`:
```go
// check groupRatio contains usableGroup
for group := range ratio_setting.GetGroupRatioCopy() {
    if _, ok := usableGroup[group]; !ok {
        delete(groupRatio, group)   // ✅ group_ratio 已按可用分组裁剪
    }
}
```
`usable_group` 字段 = `service.GetUserUsableGroups(group)`,本身就是白名单,✅ 安全。

### 次要问题
`router/api-router.go:80` `userRoute.GET("/groups", controller.GetUserGroups)` 挂在 `selfRoute`(带 `UserAuth`)**之外**,匿名可直接拿到全量 `userUsableGroups` 的分组名 + 倍率。虽然这些是「公开可用分组」,但如果新需求要求「未登录不看倍率」,这里也要收口。

---

## 五、倍率数据下发路径汇总

| 字段 | 来源 | 是否过滤 |
|---|---|---|
| `data[].model_ratio` / `model_price` / `completion_ratio` / `cache_ratio` / ... | `model/pricing.go:376-402`,`ratio_setting.GetModelRatio` 等 | 与分组无关 |
| **`group_ratio: map[string]float64`** | `controller/pricing.go:40-43` 全量 `GetGroupRatioCopy()`,再 `:49-54` 用 `GetGroupGroupRatio` 覆写成用户专属倍率,最后 `:61-65` 按 `usableGroup` 删键 | ✅ 已过滤 |
| **`usable_group: map[string]string`** | `service.GetUserUsableGroups(group)` | ✅ 白名单 |
| **`data[].enable_groups: []string`** | `model/pricing.go:361` `EnableGroup: groups.Items()`(abilities 聚合) | ❌ **未过滤 → 泄漏分组名** |
| `auto_groups: []string` | `service.GetUserAutoGroup(group)`(`service/group.go:46`,已与 usableGroup 取交) | ✅ |
| perf-metrics `groups[].group` | `controller/perf_metrics.go:22,77` | ❌ 只按 GroupRatio 过滤 |

---

## 六、最小修复方案

### 修复 A(必须,后端一处):裁剪 `EnableGroup`
文件 `controller/pricing.go`,函数 `filterPricingByUsableGroups`(:12-34)。

**关键陷阱**:`model.GetPricing()` 返回的是包级缓存切片 `model/pricing.go:49 var pricingMap []Pricing`,`for _, item := range pricing` 里 `item` 是结构体值拷贝,但 `item.EnableGroup` 的 slice header 指向**共享底层数组**。绝不能就地 `item.EnableGroup = item.EnableGroup[:n]` 或原地覆写,必须新分配切片,否则会污染全局缓存(1 分钟 TTL 内所有请求)。

```go
func filterPricingByUsableGroups(pricing []model.Pricing, usableGroup map[string]string) []model.Pricing {
	if len(pricing) == 0 {
		return pricing
	}
	if len(usableGroup) == 0 {
		return []model.Pricing{}
	}

	filtered := make([]model.Pricing, 0, len(pricing))
	for _, item := range pricing {
		// 新建切片,不能复用底层数组(pricingMap 是全局缓存)
		visible := make([]string, 0, len(item.EnableGroup))
		hasAll := false
		for _, g := range item.EnableGroup {
			if g == "all" {
				hasAll = true
				continue
			}
			if _, ok := usableGroup[g]; ok {
				visible = append(visible, g)
			}
		}
		if !hasAll && len(visible) == 0 {
			continue
		}
		if hasAll {
			visible = append(visible, "all")
		}
		item.EnableGroup = visible
		filtered = append(filtered, item)
	}
	return filtered
}
```
> 语义保持:原逻辑 `common.StringsContains(item.EnableGroup, "all")` 直接放行,新逻辑保留 `all` 这个哨兵值。若确认 `all` 不会真的出现在 abilities 里(`model/pricing.go:269 groups.Add(ability.Group)` 只加真实分组),可以简化掉 `hasAll` 分支。

**上游冲突风险:低。** 这是单文件单函数、约 20 行的替换,函数是 new-api 自有且近期稳定;合并上游时最坏是一个小 hunk 冲突,人工 3 分钟可解。

### 修复 B(建议,后端一处):perf-metrics 按用户可用分组过滤
`controller/perf_metrics.go:22` 与 `:76-82`,把 `ratio_setting.GetGroupRatioCopy()` 换成 `service.GetUserUsableGroups(userGroup)`,`userGroup` 从 `c.Get("id")` → `model.GetUserCache` 取(与 `controller/pricing.go:38-56` 同款写法);并在 `GetPerfMetrics` 里校验 `c.Query("group")` 属于该 map。冲突风险同样低(单文件 ~15 行)。

### 修复 C(前端,可选/兜底):三处渲染改用 `getAvailableGroups`
若不想动后端(或想双保险),把
- `model-card.tsx:60`
- `pricing-columns.tsx:399`
- `model-details.tsx:449`

的 `model.enable_groups` 换成 `getAvailableGroups(model, usableGroup)`(`lib/model-helpers.ts:29`)。**但这需要把 `usableGroup` 透传进 card / column 工厂,改动面反而比后端大,且是「客户端过滤」——数据仍在网络响应里,不算真正修复。** 推荐以修复 A 为准,前端不动。

---

## 【扩展点建议】

### 需要新建 vs 需要改动

**必须新建(全部是新文件,零冲突)**
1. `setting/group_visibility/`(新包)—— 隐藏分组元数据。定义如:
   ```go
   type GroupMeta struct {
       Name        string  // 分组 key,对应 GroupRatio 的 key
       DisplayName string  // 对外显示名(替代裸 key,顺便解决"看到分组名"的诉求)
       Hidden      bool    // 隐藏:模型广场/倍率/分组名全不下发
       Whitelist   []string // 仅这些用户分组可见(可选)
       Sort        int
   }
   func IsGroupHidden(name string) bool
   func VisibleGroupsFor(userGroup string) map[string]struct{}
   func DisplayNameOf(name string) string
   ```
   数据存**独立 MySQL 库**,由**独立 YAML** 配置连接串,启动时加载进内存 map(参考 `types.RWMap` + `setting/config` 的 Register 模式,但**不要**注册进 `config.GlobalConfig`,那会走原库的 options 表)。
2. `controller/ext_*.go` / `router/ext-router.go`(新文件)—— 新功能的管理接口 + 路由注册函数 `SetExtRouter(router *gin.Engine)`。
3. `web/src/routes/<新页面>/index.tsx` + `web/src/features/<新功能>/`(新目录)—— TanStack 文件式路由自动收录到 `routeTree.gen.ts`(该文件是生成物,不算手改)。

**必须改动的原有文件(最小集合,共 4 个)**

| 文件 | 改动 | 行数量级 | 冲突风险 |
|---|---|---|---|
| `controller/pricing.go` | 修复 A:重写 `filterPricingByUsableGroups`(:12-34);另在 `GetPricing` 的 `:61-65` 循环里补一句「隐藏分组也删」 | ~20 行 | 低 |
| `service/group.go` | 在 `GetUserUsableGroups`(:11-38)`return groupsCopy` 前加 **一个** hook:`ext.FilterHiddenGroups(userGroup, groupsCopy)`。**这是杠杆最大的一处** —— `GetUserUsableGroups` 是 `GetPricing`、`GetUserGroups`、`GetUserAutoGroup`、`GroupInUserUsableGroups` 的共同上游,改这一处就同时堵住 `/api/pricing` 的 `usable_group`+`group_ratio`、`/api/user/groups`、`auto_groups` 全部出口 | 2-3 行 | **极低** |
| `router/main.go:15-18` | `SetRouter` 里加一行 `SetExtRouter(router)` | 1 行 | **极低**(该函数几乎不动) |
| `main.go` | 启动时加一行 `ext.Init()`(读 YAML、连独立 MySQL、AutoMigrate 新表) | 1 行 | 低 |

可选第 5 处:`controller/perf_metrics.go`(修复 B)。

### 挂载点评估:「改一处挂多少功能」

- **`service/group.go:37` 的 `return groupsCopy`** —— 分组可见性的**唯一咽喉**。一个 hook 覆盖 4 个 handler、6 个下游函数。强烈建议所有分组可见性逻辑都收敛到这里,而不是在每个 controller 里各写一遍。
- **`router/main.go:15` `SetRouter`** —— 后端所有新路由的唯一挂载点,加一行 `SetExtRouter(router)` 后,新功能的所有 API 都在新文件里定义,永不碰 `router/api-router.go`(那是 300+ 行、上游高频改动的文件,**务必避开**)。
- **`web/src/routes/`** —— 前端零挂载成本(文件式路由)。但**导航菜单**不是文件式的:`web/src/lib/nav-modules.ts:24-42` 的 `HeaderNavModule` / `DEFAULT_HEADER_NAV_MODULES` 是硬编码字面量,新增顶栏入口需要改这里 + 后端 `middleware/header_nav.go` 的 module 名(该 middleware 本身是通用的 `HeaderNavModuleAuth(module string)`,只读 `common.OptionMap["HeaderNavModules"]` 的 JSON,**新增 module 名无需改 Go 代码**,只需在配置 JSON 里加 key + 前端加一行)。
- **`middleware/header_nav.go:104 HeaderNavModuleAuth(module string)`** —— 已是参数化的通用中间件,新页面可直接复用,不用新写鉴权。
- **`GroupBadge`(`web/src/components/group-badge.tsx`)** —— 全站分组标签唯一出口,如果要做「显示名替代裸 key」「隐藏分组灰掉」,改这一个组件即可覆盖模型广场卡片/表格/详情/性能四处渲染。

### 与「独立数据库」约束的配合提示
`setting/ratio_setting/group_ratio.go:51` 的 `config.GlobalConfig.Register("group_ratio_setting", ...)` 和 `model/option.go:146-148` 的 `common.OptionMap` 都会把配置写回**原项目的 options 表**。新功能的分组元数据**不要**走这两条路,应完全走新包的独立 GORM 连接,否则会污染原库并在上游合并时产生迁移冲突。
