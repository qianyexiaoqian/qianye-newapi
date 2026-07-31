# 需求3:钱包页 UI 改造

# 需求 3 — 钱包页 UI 改造(选项卡 / 详情截断 / 订阅弹窗)设计

## 0. 前置结论:Base UI `Tabs.Panel` 的 `keepMounted` 已验证

`node_modules` 未安装(`web/node_modules` 不存在),无法读本地源码,改为三路交叉验证:

| 证据 | 结论 |
|---|---|
| `web/bun.lock:184` | 锁定版本 **`@base-ui/react@1.6.0`**(`package.json:22` 声明 `^1.6.0`) |
| Base UI 官方 Tabs API Reference | `Tabs.Panel` **存在** `keepMounted: boolean`,**默认 `false`** |
| 上游源码 `packages/react/src/tabs/panel/TabsPanel.tsx` | `const { ..., keepMounted = false, ... } = componentProps` → `const shouldRender = keepMounted \|\| mounted; if (!shouldRender) return null` |

**确定结论(可直接实施)**:

1. `keepMounted` 在 1.6.0 **确实支持**,默认 `false`。
2. `keepMounted={false}`(默认)时,非激活面板 **`return null`,整棵子树卸载**。GAPS.md §四④ 的担忧成立:切 Tab 会重新触发 `SubscriptionPlansCard` 的 `fetchPlans()` + `fetchSelfSubscriptionFull()`,且 `SubscriptionPurchaseDialog`(挂在 `subscription-plans-card.tsx:636`)随之卸载。
3. `keepMounted` 时,面板保留在 DOM,并被打上原生 **`hidden` + `inert` + `tabIndex=-1`**(源码 `{ 'aria-labelledby', hidden, id, role:'tabpanel', tabIndex: open ? 0 : -1, inert: inertValue(!open), 'data-index': index }`)。组件不卸载、state 保留、`useEffect` 不重跑。
4. `hidden`/`inert` 只作用于面板子树;项目 `Dialog` 走 `DialogPortal` 渲染到 `document.body`(`web/src/components/dialog.tsx:72` → `@/components/ui/dialog` 的 `DialogContent`),**不受 `inert` 影响**。
5. 1.0.0-rc.0 有一条 `Breaking change: Fixed Panel keepMounted behavior in Tabs`,该修复早于 1.6.0,**当前版本行为即上文所述**,无需再兼容旧语义。

**采用方案:两个 `TabsContent` 都加 `keepMounted`。** 附带收益:两张卡在首屏同时挂载,**网络行为与今天的双栏布局完全一致**(今天两张卡就是并排同时渲染并各自取数),不引入新请求也不减少请求 —— 零回归。

> 备选(不采用,仅备案):若未来 Base UI 移除 `keepMounted`,退路是把 `plans`/`activeSubscriptions`/`billingPreference` 三个 state 和 `fetchPlans`/`fetchSelfSubscription` 提升到 `Wallet` 层,`SubscriptionPlansCard` 退化为纯展示组件,`SubscriptionPurchaseDialog` 与其余 4 个 Dialog 并列挂在 `Wallet` 的 `<>` 顶层(`index.tsx:355-387` 那一段)。该退路要动 `SubscriptionPlansCard` 的 props 接口,成本明显更高,故当前不做。

---

## 1. 选项卡改造

### 1.1 结构

`Wallet` 内容区从「`WalletStatsCard` + 双栏 grid + `AffiliateRewardsCard`」改为「`WalletStatsCard` + Tabs(添加资金 / 订阅套餐) + `AffiliateRewardsCard`」。

`WalletStatsCard`(余额/用量/请求数)和 `AffiliateRewardsCard`(推广)**留在 Tabs 之外**,两个 Tab 都能看到 —— 它们是账户级信息,塞进任一 Tab 都是错的。

```
SectionPageLayout.Content
└─ div.mx-auto.max-w-7xl.flex-col.gap-4
   ├─ WalletStatsCard                       ← 不动
   ├─ Tabs (value=activeTab)                ← 替换原 line 293-340 的 grid
   │  ├─ TabsList  (showSubscriptionPanel 为 false 时整体隐藏)
   │  │  ├─ TabsTrigger 'funds'  <WalletCards/> t('Add Funds')
   │  │  └─ TabsTrigger 'plans'  <Crown/>       t('Subscription Plans')   ← 条件渲染
   │  ├─ TabsContent 'funds' keepMounted → div#wallet-add-funds > RechargeFormCard (props 一字不改)
   │  └─ TabsContent 'plans' keepMounted → SubscriptionPlansCard (props 一字不改)
   └─ AffiliateRewardsCard                   ← 不动
```

### 1.2 Tab 可见性门控(处理 GAPS 提到的闪烁)

`SubscriptionPlansCard` 有两套不一致的判据:
- `subscription-plans-card.tsx:192` `isAvailable = loading || plans.length > 0 || hasAny` → 通过 `onAvailabilityChange` 回调上报
- `subscription-plans-card.tsx:256` `if (plans.length === 0 && !hasAny) return null`

**不改这两处判据**,而是利用「`isAvailable` 含 `loading`」这个特性:加载期间 Tab 就存在(避免首屏 TabsList 从无到有的抖动),加载结束若无套餐则 Tab 消失,同时 `SubscriptionPlansCard` 也正好 `return null`,两者语义对齐。

单 Tab 时隐藏整条 `TabsList`(只剩一个孤零零的 Tab 很怪),页面视觉退化成今天的单栏布局。

### 1.3 移动端

`TabsList` 在 `<sm` 用 `grid w-full grid-cols-2` 撑满;`≥sm` 用 `sm:inline-flex sm:w-auto` 收成内容宽度。
注意 `tabsListVariants`(`tabs.tsx:43`)自带 `w-fit` 与 `group-data-horizontal/tabs:h-8`,`w-full` 需要靠 `cn()` 后置覆盖(`cn` = tailwind-merge,后写的胜出),`h-8` 在 `text-xs` + `size-3.5` 图标下够用,不必覆盖。

---

## 2. 套餐详情截断修复

### 2.1 三处 `truncate` 的确切修改

#### (a) 套餐卡片标题 + 副标题 —— 主犯

`web/src/features/wallet/components/subscription-plans-card.tsx:559-568`

**修改前**
```tsx
                      <div className='min-w-0'>
                        <h4 className='truncate font-semibold'>
                          {plan.title || t('Subscription Plans')}
                        </h4>
                        {plan.subtitle && (
                          <p className='text-muted-foreground truncate text-xs'>
                            {plan.subtitle}
                          </p>
                        )}
                      </div>
```

**修改后**
```tsx
                      <div className='min-w-0'>
                        <h4 className='font-semibold [overflow-wrap:anywhere]'>
                          {plan.title || t('Subscription Plans')}
                        </h4>
                        {plan.subtitle && (
                          <Tooltip>
                            <TooltipTrigger
                              render={
                                <p className='text-muted-foreground mt-0.5 line-clamp-3 cursor-help text-xs leading-relaxed whitespace-pre-line [overflow-wrap:anywhere]' />
                              }
                            >
                              {plan.subtitle}
                            </TooltipTrigger>
                            <TooltipContent className='max-w-xs whitespace-pre-line'>
                              {plan.subtitle}
                            </TooltipContent>
                          </Tooltip>
                        )}
                      </div>
```

要点:
- `truncate` 去掉;`h4` 改为可换行 + `[overflow-wrap:anywhere]` 处理超长无空格串(纯英文 key/URL)。
- 副标题用 `line-clamp-3` 而非完全放开:卡片在 grid 里,一张卡的副标题写 2000 字会把整行卡片撑到几屏高。**完整内容由订阅弹窗承担**(用户原话就是"立即订阅弹窗…可以显示套餐完整内容")。
- `whitespace-pre-line` 保留管理员输入的换行(`\n`),但折叠多余空格;`line-clamp-*` 与 `white-space: pre-line` 可共存(clamp 走 `-webkit-box`)。
- `Tooltip` / `TooltipTrigger` / `TooltipContent` 该文件 **line 43-47 已 import**,零新增 import。`render={<p/>}` 是 Base UI 的组合方式,项目在 `subscription-plans-card.tsx:486` 已有先例。
- 移动端无 hover,Tooltip 失效 —— 这正是弹窗必须展示全文的原因。
- **不需要动外层**:`CardContent`(line 557)是 `flex h-full flex-col`,benefits 容器(line 587)是 `flex-1`,卡片高度自适应;grid 默认 `align-items: stretch`,同一行的卡片按最高的对齐,按钮仍在底部对齐。

> 若确认要"卡片里也完整展示",单行改动:把 `line-clamp-3` 删掉即可,其余不变。

#### (b) 立即订阅弹窗里的标题 200px 硬截断

`web/src/features/subscriptions/components/dialogs/subscription-purchase-dialog.tsx:279-281`

**修改前**
```tsx
            <span className='max-w-[200px] truncate text-sm font-medium'>
              {plan.title}
            </span>
```
**修改后**(见 §3 的整块重构,标题移入新的头部区块)
```tsx
            <span className='min-w-0 text-right text-sm font-medium [overflow-wrap:anywhere]'>
              {plan.title}
            </span>
```

#### (c) 管理端套餐表格

`web/src/features/subscriptions/components/subscriptions-columns.tsx:54-62`

**修改前**
```tsx
            <div className='max-w-full min-w-0'>
              <div className='truncate font-medium'>{plan.title}</div>
              {plan.subtitle && (
                <div className='text-muted-foreground truncate text-xs'>
                  {plan.subtitle}
                </div>
              )}
            </div>
```
**修改后**
```tsx
            <div className='max-w-full min-w-0'>
              <div className='font-medium [overflow-wrap:anywhere]'>
                {plan.title}
              </div>
              {plan.subtitle && (
                <div className='text-muted-foreground line-clamp-2 text-xs whitespace-pre-line [overflow-wrap:anywhere]'>
                  {plan.subtitle}
                </div>
              )}
            </div>
```
同时把 `size: 200`(line 64)提到 `size: 280`。该列 `meta.mobileTitle: true`,`MobileCardList` 复用同一 cell,**改一处桌面 + 移动端同时修好**。

### 2.2 后端 `varchar(255)` 要不要改

`model/subscription.go:150`
```go
Subtitle string `json:"subtitle" gorm:"type:varchar(255);default:''"`
```

事实核查(已 grep 确认):全仓 Go 侧对 `Subtitle` 只有两处引用 —— `model/subscription.go:150` 定义、`controller/subscription.go:286` 透传更新,**没有任何长度校验**。超 255 时 MySQL 严格模式报错(`Data too long`),非严格模式静默截断。前端 `plan-form.ts:29` `subtitle: z.string().optional()` 也无 `.max()`。

**结论:MVP 不改后端。**

- MySQL 的 `varchar(255)` 在 utf8mb4 下是 **255 个字符**(不是字节),中文可写 255 字,做"套餐详细内容备注"够用。
- 改 `varchar(255)` → `text` 需要占用 D3 预算(40 行里的 1 行)+ AutoMigrate 会对 `subscription_plans` 做一次表拷贝式 ALTER,收益/风险不成正比,风险等级**中**。

**若确认需要富文本/多段落/特性列表(二期),走新库扩展表,原项目零改动**(与独立库约束一致):

```go
// qianye/model/plan_ext.go  —— 归属 qy 后端模块,此处仅给接口契约
type QyPlanExt struct {
    Id          int64  `gorm:"primaryKey;autoIncrement"`
    PlanId      int    `gorm:"uniqueIndex:uk_qy_plan_ext_plan;not null"` // 主库 subscription_plans.id,跨库软引用
    Description string `gorm:"type:text"`                                // 长文本说明，纯文本
    Highlights  string `gorm:"type:json"`                                // []string 卖点列表
    Enabled     bool   `gorm:"default:true"`
    CreatedAt   int64
    UpdatedAt   int64
}
```
- `GET /api/qy/subscription/plan-ext`(UserAuth,只读)→ `{ success, data: [{ plan_id, description, highlights }] }`
- 前端在 `SubscriptionPlansCard` 里多发一次请求,按 `plan_id` merge;`description` 优先于 `plan.subtitle`。
- **降级**:该接口 4xx/5xx/超时一律吞掉,回落到 `plan.subtitle`,不阻塞套餐渲染(与"配置缺失即静默禁用"一致)。
- 若要渲染 Markdown,**必须走 `@/components/ui/markdown` 的 `Markdown` 组件**,严禁 `dangerouslySetInnerHTML`(管理员可控字段仍是 stored-XSS 面)。

**当下的前端保护(零后端成本,建议做)**:`plan-form.ts:29` 加 `.max(255, t('qy_plan_subtitle_too_long'))`,让管理员在表单侧拿到明确报错,而不是保存后被 DB 静默截断。

---

## 3. 立即订阅弹窗增强

### 3.1 现状字段盘点

| 弹窗行号 | 已展示 |
|---|---|
| 275-282 | `t('Plan Name')` → `plan.title`(**200px 截断**) |
| 283-291 | `t('Validity Period')` → `formatDuration` |
| 292-299 | `t('Reset Period')`(仅非 No Reset) |
| 300-308 | `t('Plan Quota')` → `total_amount>0 ? formatQuota : Unlimited` |
| 309-316 | `t('Upgrade Group')` → `<GroupBadge>`(条件) |
| 317-321 | `t('Amount Due')` → 硬编码 `$${price}` |
| 324-331 | 超限告警(仅 `limitReached`) |
| 333-364 | 余额区:Required / Available / 告警 / Pay with Balance |
| 366-440 | Stripe / Creem / Waffo Pancake / 易支付 |

**缺失(按重要度)**:
1. `plan.subtitle` —— **完全不渲染**(全文 0 次引用),这是用户抱怨的直接原因
2. `plan.downgrade_group` —— 到期降级到哪个组,决定"到期后还能不能用",必须展示
3. `plan.allow_wallet_overflow` —— 配额用尽后能否继续吃钱包余额,直接影响购买决策
4. 已购次数 `purchaseCount/purchaseLimit` —— 只在超限时以红色 Alert 出现,未超限时不告知
5. `plan.currency` 被忽略,`$` 硬编码
6. 无到期日预览(只有"1 个月",没有"预计到 2026-08-30")
7. `plan.id` 不可见(客服对单困难)
8. `plan.allow_balance_pay` 只用于禁用按钮,没有正面说明

### 3.2 目标结构

```
Dialog  (contentClassName: sm:max-w-lg，原为 sm:max-w-md)
└─ div.space-y-3
   ├─ ① 套餐头部块  bg-muted/50 rounded-lg border p-3
   │   ├─ 标题行:  h3 {plan.title}  +  StatusBadge #{plan.id}
   │   └─ 说明段:  {subtitle || ext.description}  whitespace-pre-line  完整不截断
   │              空时 → t('qy_plan_no_description')
   ├─ ② 事实清单块  bg-muted/50 rounded-lg border p-3  (dl 两列)
   │   ├─ Validity Period      formatDuration
   │   ├─ Reset Period         formatResetPeriod        (条件)
   │   ├─ Plan Quota           formatQuota / Unlimited
   │   ├─ qy_plan_expiry_preview  预计到期日 (dayjs 计算)          ★新增
   │   ├─ Upgrade Group        <GroupBadge>             (条件)
   │   ├─ Downgrade Group      <GroupBadge>             (条件) ★新增
   │   ├─ qy_plan_wallet_overflow  允许/不允许           ★新增
   │   └─ qy_plan_purchased    {count}/{limit} 或 {count} 次      ★新增
   ├─ ③ Separator + Amount Due   formatPlanPrice(plan)  ★带币种
   ├─ ④ 超限 Alert                                       (保持原样 324-331)
   ├─ ⑤ 余额支付块                                        (保持原样 333-364)
   └─ ⑥ 支付方式区                                        (保持原样 366-440)
```

**弹窗高度**:`components/dialog.tsx:97-101` 的 body 已是 `overflow-y-auto` + `max-h-[calc(100vh-14rem)]`,内容变长自动滚动,**`dialog.tsx` 一行都不用改**。

### 3.3 新增共享文件(消除重复 + 修 React key bug)

**新建** `web/src/features/subscriptions/lib/plan-facts.ts`(纯新文件,零合并冲突):

```ts
import type { TFunction } from 'i18next'

import dayjs from '@/lib/dayjs'
import { formatQuota } from '@/lib/format'

import { formatDuration, formatResetPeriod } from './format'
import type { SubscriptionPlan } from '../types'

export interface PlanFact {
  id: string        // 稳定 key —— 修掉 subscription-plans-card.tsx:588 用文案当 key 的缺陷
  label: string
  value: string
}

const CURRENCY_SYMBOLS: Record<string, string> = {
  USD: '$', CNY: '¥', EUR: '€', GBP: '£', JPY: '¥', HKD: 'HK$',
}

/** price_amount 是 decimal(10,6)；toFixed(2) 会把 $0.005 显示成 $0.01 */
function trimAmount(raw: number): string {
  if (!Number.isFinite(raw) || raw < 0) return '0.00'
  const fixed2 = raw.toFixed(2)
  if (Math.abs(Number(fixed2) - raw) < 1e-9) return fixed2
  return raw.toFixed(6).replace(/0+$/, '')
}

export function formatPlanPrice(plan: Partial<SubscriptionPlan>): string {
  const code = (plan?.currency || 'USD').toUpperCase()
  const amount = trimAmount(Number(plan?.price_amount || 0))
  const symbol = CURRENCY_SYMBOLS[code]
  return symbol ? `${symbol}${amount}` : `${amount} ${code}`
}

/** 客户端预估到期日；后端为准，仅作购买前参考 */
export function formatPlanExpiryPreview(plan: Partial<SubscriptionPlan>): string {
  const unit = plan?.duration_unit || 'month'
  const base = dayjs()
  const end =
    unit === 'custom'
      ? base.add(Number(plan?.custom_seconds || 0), 'second')
      : base.add(Number(plan?.duration_value || 1), unit)
  return end.format('YYYY-MM-DD HH:mm')
}

export function buildPlanFacts(
  plan: Partial<SubscriptionPlan>,
  t: TFunction,
  opts: { includeGroups?: boolean; purchaseCount?: number } = {}
): PlanFact[] {
  const total = Number(plan?.total_amount || 0)
  const limit = Number(plan?.max_purchase_per_user || 0)
  const reset = formatResetPeriod(plan, t)
  const count = Number(opts.purchaseCount || 0)

  const facts: (PlanFact | null)[] = [
    { id: 'duration', label: t('Validity Period'), value: formatDuration(plan, t) },
    reset !== t('No Reset')
      ? { id: 'reset', label: t('Quota Reset'), value: reset }
      : null,
    {
      id: 'quota',
      label: t('Total Quota'),
      value: total > 0 ? formatQuota(total) : t('Unlimited'),
    },
    { id: 'expiry', label: t('qy_plan_expiry_preview'), value: formatPlanExpiryPreview(plan) },
    {
      id: 'overflow',
      label: t('qy_plan_wallet_overflow'),
      value:
        plan?.allow_wallet_overflow === false
          ? t('qy_plan_wallet_overflow_blocked')
          : t('qy_plan_wallet_overflow_allowed'),
    },
    limit > 0
      ? {
          id: 'limit',
          label: t('qy_plan_purchased'),
          value: `${count} / ${limit}`,
        }
      : null,
    opts.includeGroups && plan?.upgrade_group
      ? { id: 'upgrade', label: t('Upgrade Group'), value: plan.upgrade_group }
      : null,
    opts.includeGroups && plan?.downgrade_group
      ? { id: 'downgrade', label: t('Downgrade Group'), value: plan.downgrade_group }
      : null,
  ]

  return facts.filter((f): f is PlanFact => f !== null)
}
```

`dayjs.add(value, unit)` 的 `unit` 需要 `'year'|'month'|'day'|'hour'`,恰好与 `duration_unit` 的非 custom 取值一一对应,类型上加一次断言即可。

**卡片侧**(`subscription-plans-card.tsx`)用 `includeGroups: true` 把分组当字符串塞进 benefits 行;**弹窗侧**用 `includeGroups: false`,分组单独用 `<GroupBadge>` 渲染。

---

## 4. Tab 状态与 URL 同步

### 4.1 Search schema

`web/src/routes/_authenticated/wallet/index.tsx`

**修改前(line 24-26 / 33-36)**
```tsx
const walletSearchSchema = z.object({
  show_history: z.boolean().optional(),
})
...
function RouteComponent() {
  const { show_history } = Route.useSearch()
  return <Wallet initialShowHistory={show_history} />
}
```
**修改后**
```tsx
const walletSearchSchema = z.object({
  show_history: z.boolean().optional(),
  tab: z.enum(WALLET_TAB_VALUES).optional().catch('funds'),
})
...
function RouteComponent() {
  const { show_history, tab } = Route.useSearch()
  return <Wallet initialShowHistory={show_history} initialTab={tab} />
}
```
line 22 后加一行 `import { WALLET_TAB_VALUES } from '@/features/wallet/constants'`。

`WALLET_TAB_VALUES` 加在 `web/src/features/wallet/constants.ts` 末尾(line 67 之后):
```ts
/**
 * Wallet page tabs (also used as `?tab=` search param values)
 */
export const WALLET_TAB_VALUES = ['funds', 'plans'] as const

export type WalletTab = (typeof WALLET_TAB_VALUES)[number]

export const DEFAULT_WALLET_TAB: WalletTab = 'funds'
```
zod 是 **v4**(`package.json:78` `^4.4.3`),`z.enum()` 接受 `readonly [string, ...string[]]`,`as const` 数组可直接传;`.optional().catch('funds')` 语义:`undefined` 透传,非法值兜底为 `'funds'`。项目 `redemption-codes/index.tsx` 已有 `z.enum(REDEMPTION_FILTER_VALUES)` 的先例。

### 4.2 双向同步与默认值

```tsx
const walletRoute = getRouteApi('/_authenticated/wallet/')   // 模块级常量，参照 redemptions-table.tsx:47
...
const navigate = walletRoute.useNavigate()
const [activeTab, setActiveTab] = useState<WalletTab>(
  props.initialTab ?? DEFAULT_WALLET_TAB
)

// URL → state（浏览器前进/后退、外链）
useEffect(() => {
  const next = props.initialTab ?? DEFAULT_WALLET_TAB
  setActiveTab((prev) => (prev === next ? prev : next))
}, [props.initialTab])

// state → URL（默认 tab 写 undefined，保持 URL 干净）
const handleTabChange = useCallback(
  (value: string) => {
    if (value !== 'funds' && value !== 'plans') return
    setActiveTab(value)
    void navigate({
      search: (prev) => ({
        ...prev,
        tab: value === DEFAULT_WALLET_TAB ? undefined : value,
      }),
      replace: true,
    })
  },
  [navigate]
)

// 套餐不可用时把用户弹回充值 Tab
useEffect(() => {
  if (!showSubscriptionPanel && activeTab === 'plans') {
    handleTabChange(DEFAULT_WALLET_TAB)
  }
}, [showSubscriptionPanel, activeTab, handleTabChange])
```

- 默认 Tab **恒为 `funds`**,不做"有订阅就默认 plans"的智能判断 —— 分享出去的链接必须对所有人是同一个页面。
- `replace: true`:两个 Tab 之间反复切换不污染浏览历史,返回键回到上一个页面而不是上一个 Tab。
- 深链 `?tab=plans` 但该站没配套餐 → 先显示、加载完发现无套餐 → 静默弹回 `funds` 且用 `replace` 抹掉参数,不产生历史项。

### 4.3 ⚠️ 必须一并修掉的 `replaceState` 冲突

`web/src/features/wallet/index.tsx:132-137` 现有代码:
```tsx
  useEffect(() => {
    if (props.initialShowHistory) {
      setBillingDialogOpen(true)
      window.history.replaceState({}, '', window.location.pathname)
    }
  }, [props.initialShowHistory])
```
`window.location.pathname` **不含 query**,这行会把整个 search 抹掉,连同新加的 `tab=plans` 一起。而且它绕过了 TanStack Router,路由内部的 search state 不会同步更新,造成"URL 没了参数但 `Route.useSearch()` 还有值"的错位。

**必须改为**:
```tsx
  useEffect(() => {
    if (props.initialShowHistory) {
      setBillingDialogOpen(true)
      void navigate({
        search: (prev) => ({ ...prev, show_history: undefined }),
        replace: true,
      })
    }
  }, [props.initialShowHistory, navigate])
```
这既修好了 tab 参数被吞的问题,也顺手修掉了一个既有缺陷。

### 4.4 `#wallet-add-funds` 锚点

grep 全仓只有定义处一处(`features/wallet/index.tsx:300`),**无任何内部链接引用**,但可能被外部文档/公告引用。改造后该 `div` 移入 `TabsContent value='funds'`,由于 `keepMounted` 会保留在 DOM;若用户带 `?tab=plans#wallet-add-funds` 进来,锚点元素是 `hidden` 的,浏览器不会滚动。属可接受的边界情况,**保留 `id` 与 `scroll-mt-4` 不变**,不额外处理。

---

## 5. 逐文件确切改动清单

> 纯前端,不计入后端 40 行预算。所有新文件必须带 AGPL 版权头(`bun run copyright` 自动加)。

| # | 文件 | 类型 | 精确位置 | 改动 | 冲突风险 |
|---|---|---|---|---|---|
| F1 | `web/src/routes/_authenticated/wallet/index.tsx` | 改 | line 22 后 +1 import;line 24-26 schema +1 行;line 33-36 改 2 行 | Tab 深链 | **低**(全文 37 行,上游极少动) |
| F2 | `web/src/features/wallet/constants.ts` | 改 | line 67 之后追加 | `WALLET_TAB_VALUES` / `WalletTab` / `DEFAULT_WALLET_TAB` | **低**(纯追加) |
| F3 | `web/src/features/wallet/index.tsx` | 改 | ① line 19 前插 2 个 import(`@tanstack/react-router`、`lucide-react`);② line 22 后插 `@/components/ui/tabs`;③ line 35 的 `./constants` import 追加 3 个符号;④ line 58-60 `WalletProps` +1 字段;⑤ line 62 后加模块级 `walletRoute` 常量;⑥ line 81 后 +1 state;⑦ **line 132-137 整块替换**(§4.3);⑧ line 278-283 后 +3 个 hook;⑨ **line 293-340 整块替换**为 Tabs | 选项卡主改造 | **中**(钱包页是上游高频页;但 line 301-331 的 28 个 `RechargeFormCard` props **一字不动**,原样搬进 `TabsContent`,上游若新增 prop 只会在这一块产生行级冲突,`git merge` 可自动合) |
| F4 | `web/src/features/wallet/components/subscription-plans-card.tsx` | 改 | ① line 54 import 追加 `buildPlanFacts`;② line 399 `max-h-64` → `max-h-72 sm:max-h-[26rem]`;③ line 526 grid 列数;④ **line 537-549** benefits 数组 → `buildPlanFacts(plan, t, { includeGroups: true, purchaseCount: count })`;⑤ **line 559-568** 去 truncate + Tooltip;⑥ **line 587-597** `key={label}` → `key={fact.id}` | 截断修复 + key 修复 | **中** |
| F5 | `web/src/features/subscriptions/components/dialogs/subscription-purchase-dialog.tsx` | 改 | ① line 19 lucide import 追加 `Info`;② line 41-49 相对 import 追加 `./plan-facts` 的 3 个函数;③ line 268 `sm:max-w-md` → `sm:max-w-lg`;④ **line 274-322 整块替换**为 §3.2 的 ①②③ | 弹窗增强 | **中** |
| F6 | `web/src/features/subscriptions/lib/plan-facts.ts` | **新建** | — | §3.3 全文 | **无** |
| F7 | `web/src/features/subscriptions/lib/index.ts` | 改 | 追加 `export * from './plan-facts'` | barrel 导出(`formatDuration` 就是从这里导出的) | **低** |
| F8 | `web/src/features/subscriptions/components/subscriptions-columns.tsx` | 改 | line 54-62 去 truncate;line 64 `size: 200` → `280` | 管理端表格 + 移动卡片 | **低** |
| F9 | `web/src/features/subscriptions/lib/plan-form.ts` | 改 | line 29 加 `.max(255, …)` | 防 DB 静默截断 | **低** |
| F10 | `web/src/features/subscriptions/components/subscriptions-mutate-drawer.tsx` | 改(**条件**) | line 310-313 `<Input>` → `<Textarea rows={3}>`;顶部加 `@/components/ui/textarea` import | 仅当后端放开长度才做 | **低** |
| F11 | `web/src/i18n/locales/{en,zh,zh-TW,ja,fr,ru,vi}.json` ×7 | 改 | 各追加 §7 的键 | 文案 | **高频但纯追加** |
| F12 | `web/src/routeTree.gen.ts` | 自动 | — | 插件重写(search schema 变更) | **高但可重生成**,冲突时删掉重跑 `bun run build` |

**总计:改 10 个既有前端文件 + 新建 1 个 + 7 个 locale。**(F10 条件性,F1/F2 是纯追加)

### F3 的关键片段(替换 line 293-340)

```tsx
            <Tabs
              value={activeTab}
              onValueChange={handleTabChange}
              className='gap-4'
            >
              {showSubscriptionPanel && (
                <TabsList className='grid w-full grid-cols-2 sm:inline-flex sm:w-auto'>
                  <TabsTrigger value='funds' className='gap-1.5 px-3'>
                    <WalletCards className='size-3.5' />
                    {t('Add Funds')}
                  </TabsTrigger>
                  <TabsTrigger value='plans' className='gap-1.5 px-3'>
                    <Crown className='size-3.5' />
                    {t('Subscription Plans')}
                  </TabsTrigger>
                </TabsList>
              )}

              <TabsContent value='funds' keepMounted className='outline-none'>
                <div id='wallet-add-funds' className='scroll-mt-4'>
                  <RechargeFormCard
                    {/* 原 line 302-330 的 28 个 props 原样搬入，一字不改 */}
                  />
                </div>
              </TabsContent>

              <TabsContent value='plans' keepMounted className='outline-none'>
                <SubscriptionPlansCard
                  topupInfo={topupInfo}
                  onAvailabilityChange={handleSubscriptionAvailabilityChange}
                  userQuota={user?.quota}
                  onPurchaseSuccess={fetchUser}
                />
              </TabsContent>
            </Tabs>
```

### 验收命令(AGENTS.md 强制)

```
cd web && bun run typecheck        # tsgo -b，必须 0 error
cd web && bun run lint             # 对改动文件必须 0 error
cd web && bun run format
cd web && bun run copyright:check  # 新文件版权头
cd web && bun run i18n:sync        # 会重写全部 7 个 locale
```
注意 `.oxlintrc.json` 的 `no-nested-ternary: error` —— `buildPlanFacts` 里的三元不要嵌套;`props` 非必要不解构(`WalletProps` 已按此写法)。

---

## 6. 并发、边界与异常

| 场景 | 处理 |
|---|---|
| **切 Tab 重复取数** | `keepMounted` 保证 `SubscriptionPlansCard` 不卸载,`useEffect(…, [fetchPlans, fetchSelfSubscription])`(line 153-160)只跑一次。两个 fetch 函数都是 `useCallback([], …)`,依赖恒定,不会二次触发 |
| **订阅弹窗被卸载** | `keepMounted` 保证 `purchaseOpen`/`selectedPlan` 状态存活;另 `Dialog` 走 Portal,不受面板 `hidden`/`inert` 影响 |
| **Tab 消失时用户正停在该 Tab** | `useEffect` 检测 `!showSubscriptionPanel && activeTab==='plans'` → `handleTabChange('funds')` + `replace` |
| **`onAvailabilityChange` 抖动** | `handleSubscriptionAvailabilityChange` 是 `useCallback([], …)`(line 278-283),稳定;`isAvailable` 变化时 `setShowSubscriptionPanel` 同值不触发 rerender(React 自带 bailout) |
| **URL ↔ state 死循环** | `setActiveTab((prev) => prev === next ? prev : next)` 提前返回;`tab` 为默认值时写 `undefined`,`props.initialTab ?? DEFAULT` 归一化后两侧永远等价 |
| **`?tab=` 传非法值** | zod `.catch('funds')` 兜底,不会抛异常打断路由 |
| **`show_history` + `tab` 同时存在** | §4.3 改为 router navigate 的 `search` updater,只清 `show_history`,`tab` 保留 |
| **`plan.subtitle` 是管理员可控字符串** | 作为 React text child 渲染,自动转义,**无 XSS**。禁止改成 `dangerouslySetInnerHTML`;若二期上 Markdown,走 `@/components/ui/markdown` |
| **超长无空格串(URL / base64)撑破布局** | 所有放开截断的地方统一加 `[overflow-wrap:anywhere]` |
| **`price_amount` 是 `decimal(10,6)`** | 现有 `Number(...).toFixed(2)` 把 `$0.005` 显示成 `$0.01`。`trimAmount()` 改为「能被 2 位精确表示就用 2 位,否则最多 6 位去尾零」,与后端 decimal 精度对齐 |
| **`price_amount` 为 `NaN`/负数** | `trimAmount` 里 `!Number.isFinite(raw) \|\| raw < 0` → `'0.00'`,不渲染 `NaN` |
| **余额判定的前后端口径** | 弹窗 `balanceCost = Math.ceil(price * quotaPerUnit)`(line 106-109)是**前端预估**,后端 `SubscriptionRequestBalancePay` 才是权威。**不动这段逻辑**,只保证后端返回的 `res.message` 原样 toast(现有 line 244-250 已如此)。绝不能因为前端算出"余额够"就跳过后端错误提示 |
| **`total_amount` 为 0** | 语义是"不限量",已有 `total_amount > 0 ? formatQuota : t('Unlimited')` 分支,`buildPlanFacts` 沿用 |
| **`max_purchase_per_user` 为 0** | 语义是"不限次",`limit > 0` 才生成 `qy_plan_purchased` 事实项 |
| **`purchaseCount` 为 `undefined`** | `Number(opts.purchaseCount \|\| 0)` 归零 |
| **到期日预览跨月/跨年** | `dayjs().add(1,'month')` 对 1月31日 → 2月28/29日 自动钳位,与后端可能的算法有差异 → 文案标注"预计",不作为承诺 |
| **`custom_seconds` 极大值** | `dayjs.add(seconds,'second')` 内部走毫秒数,`custom_seconds` 是 int64;若超过 `Number.MAX_SAFE_INTEGER/1000` 会得到 `Invalid Date` → `formatPlanExpiryPreview` 需在返回前 `end.isValid() ? end.format(...) : '-'` |
| **金额精度约定** | 本模块**不做任何金额加减**,只做展示格式化。所有 quota↔money 换算保持沿用现有 `formatQuota`(`@/lib/format`)与 `currency.quotaPerUnit`,不引入新的算法 |
| **`Tabs` 的 `onValueChange` 值类型** | Base UI `Tab.Value` 是宽类型,项目既有写法有两种(`usage-logs/index.tsx:111` 直接 `(scope: string)`、`secure-verification-dialog.tsx:142` `(value) => ... as X`)。本设计用 `(value: string)` + 白名单判断,两边都兼容且防御非法值 |

---

## 7. i18n 新增文案清单

复用已有键(全部 7 个语言包已存在,**不新增**):
`Add Funds` (en.json:181) / `Subscription Plans` (:4338) / `Subscribe Now` (:4330) / `My Subscriptions` / `Wallet` / `Validity Period` / `Quota Reset` / `Reset Period` / `Total Quota` / `Plan Quota` / `Unlimited` / `Upgrade Group` (:4858) / **`Downgrade Group` (:1442)** / `Amount Due` (:346) / `Order History` (:3174) / `Purchase Limit` (:3573) / `No plans available` (:2959) / `Plan Subtitle` (:3366) / `Purchase limit reached` / `Insufficient balance` / `Pay with Balance` / `Limit Reached` / `Recommended`

新增(按 D9 约定用 `qy_` 前缀 + **下划线扁平键**,严禁点号 —— i18next `keySeparator` 默认为 `'.'` 会被当成嵌套):

| key | zh | en |
|---|---|---|
| `qy_plan_details` | 套餐说明 | Plan Details |
| `qy_plan_no_description` | 该套餐暂无说明 | No description for this plan |
| `qy_plan_expiry_preview` | 预计到期 | Estimated Expiry |
| `qy_plan_wallet_overflow` | 配额用尽后 | After Quota Runs Out |
| `qy_plan_wallet_overflow_allowed` | 可继续使用钱包余额 | Falls back to wallet balance |
| `qy_plan_wallet_overflow_blocked` | 不可使用钱包余额 | Wallet fallback disabled |
| `qy_plan_purchased` | 已购买 | Purchased |
| `qy_plan_balance_pay_blocked` | 该套餐不支持余额购买 | Balance payment not supported |
| `qy_plan_subtitle_too_long` | 套餐说明最多 255 个字符 | Plan description is limited to 255 characters |
| `qy_wallet_tab_funds_desc` | 充值、兑换码与订单记录 | Top up, redeem codes and order history |
| `qy_wallet_tab_plans_desc` | 订阅套餐与我的订阅 | Subscription plans and my subscriptions |
| `qy_sub_expiring_soon` | 即将到期 | Expiring soon |
| `qy_sub_expiring_in_days` | {{count}} 天后到期 | Expires in {{count}} days |
| `qy_plan_compare` | 对比套餐 | Compare plans |
| `qy_plan_empty_title` | 暂无可订阅的套餐 | No subscription plans yet |
| `qy_plan_empty_desc` | 管理员尚未配置套餐,可先使用「添加资金」充值 | The administrator has not configured any plan. Use Add Funds to top up. |

流程:
1. 在 `en.json` + `zh.json` 手写全部键;
2. `bun run i18n:sync` 自动补齐 `zh-TW / ja / fr / ru / vi`(缺失项用 en 值填充,并写进 `_reports/*.untranslated.json`);
3. 这些键**全部以 `t('字面量')` 形式出现在组件里**,`static-keys.ts` **不需要登记**;唯独若把 Tab 元数据抽成常量数组(`{ value, labelKey, icon }`),`labelKey` 就必须登记进 `web/src/i18n/static-keys.ts`。本设计**不抽常量数组**,直接内联 `t('Add Funds')`/`t('Subscription Plans')`,避开这个坑。
4. `qy_` 键在任一 locale 缺失时,i18next 会回退到 `en`;若 `en` 也缺,会**原样渲染 `qy_plan_details` 这种裸 key**,肉眼可见地丑。所以 `en.json` 必须先补齐再提交。

---

## 8. 我建议补充的(用户没提)

标注为**建议**,可按优先级裁剪。

### 8.1 空态(建议 P0)
`SubscriptionPlansCard` 现在在无套餐时直接 `return null`(line 256-258),导致 Tab 消失。这是合理的。但另一个空态被忽略了:**有套餐但用户一个订阅都没有**时,line 517-521 只有一句灰色小字 `t('Subscribe to a plan for model access')`。建议换成 `Empty` 组件(`@/components/ui/empty`,已存在,导出 `Empty/EmptyHeader/EmptyMedia/EmptyTitle/EmptyDescription/EmptyContent`):

```tsx
<Empty className='border py-8'>
  <EmptyHeader>
    <EmptyMedia variant='icon'><Crown /></EmptyMedia>
    <EmptyTitle>{t('No Active')}</EmptyTitle>
    <EmptyDescription>{t('Subscribe to a plan for model access')}</EmptyDescription>
  </EmptyHeader>
</Empty>
```

### 8.2 加载态(建议 P0)
现有 loading 骨架(line 238-254)是 `grid-cols-1 sm:grid-cols-2 xl:grid-cols-3` 三块 `h-48`,而实际内容区(line 526)改成新的列数后两者不匹配,切换瞬间会跳。**建议把骨架的 grid 类名与 line 526 保持一致**,并把骨架数量从固定 3 改为 3(保留),高度从 `h-48` 提到 `h-56`(因为副标题现在占 3 行)。

### 8.3 当前套餐到期提醒(建议 P1)
`subscription-plans-card.tsx` 已算出 `remainDays`(line 224-229),但只在 `isActive` 时显示 `{{count}} days remaining`,没有任何视觉预警。建议:

- `remainDays <= 7` → 该条订阅卡片加 `border-amber-500/50`,状态徽章旁加 `<StatusBadge variant='warning'>{t('qy_sub_expiring_soon')}</StatusBadge>`;
- **在 Tab 触发器上挂角标**:任一订阅 `remainDays <= 7` 时,`TabsTrigger value='plans'` 内加一个 `size-1.5 rounded-full bg-amber-500` 的圆点,让用户在充值 Tab 也能看到提醒。这需要 `SubscriptionPlansCard` 通过 `onAvailabilityChange` 之外的新回调上报,或把回调签名扩成 `(state: { available: boolean; expiringSoon: boolean }) => void` —— **会改 props 接口**,建议单独排期,不进本次 MVP。

### 8.4 套餐对比(建议 P2)
`plans` 超过 3 个时,卡片式很难横向比较。建议 `≥xl` 断点提供「表格视图」切换(复用 `StaticDataTable`),行=特性、列=套餐。属于增量功能,不阻塞本次。

### 8.5 "Order History" 在订阅 Tab 不可达(建议 P1)
账单历史按钮目前在 `RechargeFormCard` 的 `TitledCard action` 槽(`recharge-form-card.tsx:204-216`,且已有 `onOpenBilling ? … : null` 的条件),用户在订阅 Tab 时看不到。两个选项:
- **(a) 推荐**:把它移到页面级 `SectionPageLayout.Actions`(`wallet/index.tsx:288` 之后加一个 `<SectionPageLayout.Actions>` 槽),同时 `RechargeFormCard` 的 `onOpenBilling` prop **不传**(line 320 删掉这一行),让卡片自己渲染 `null`。改动量:钱包页 +6 行 / -1 行,`recharge-form-card.tsx` **零改动**(它已有 null 分支)。
- (b) 不动,接受该按钮只在充值 Tab 可见。

### 8.6 错误文案(建议 P1)
`fetchPlans`(line 133-135)和 `fetchSelfSubscription`(line 148-150)都是 `catch { }` 静默吞异常 —— 网络故障时用户看到的是"没有套餐",而不是"加载失败"。建议加 `plansError` state,失败时在 Tab 内渲染 `ErrorState`(`@/components/*` 已有 `error-state`)+ 重试按钮,而不是伪装成空态。这个和「Tab 消失」逻辑有交互:**加载失败时 `isAvailable` 应保持 `true`**,否则 Tab 会因网络抖动而消失,用户以为功能被下线了。

### 8.7 防刷 / 限频(建议 P2)
`handleRefresh`(line 162-169)的刷新按钮只有 `disabled={refreshing}`,松开后可以连点。建议加 3 秒冷却(`useRef` 记时间戳),避免用户狂点打 `/api/subscription/self`。

### 8.8 审计与可观测(建议 P2)
Tab 切换、`Subscribe Now` 点击、弹窗关闭方式(支付 / 取消)目前无任何埋点。若后续要评估"订阅弹窗改造是否提升转化",需要在 `handleTabChange` 与 `setPurchaseOpen(true)` 处留埋点钩子。本次不引入依赖,只**建议预留一个 `onWalletEvent?: (name: string, payload?: object) => void` 的可选 prop 占位**。

### 8.9 无障碍(建议 P1)
- `TabsList` 需要 `aria-label`(Base UI 不会自动生成):`<TabsList aria-label={t('Wallet')}>`。
- 副标题的 `Tooltip` 只对鼠标有效,键盘用户 focus 到 `<p>` 上不会触发(`<p>` 不可聚焦)。若要严格 a11y,`TooltipTrigger` 的 `render` 应换成 `<button type='button' className='text-left …'/>`,但这会在卡片里引入嵌套按钮(外层还有 Subscribe Now)。**建议保持 `<p>`,并接受 Tooltip 是纯鼠标增强 —— 完整内容的无障碍路径是弹窗**,这也是本设计把全文放进弹窗的核心理由。

### 8.10 合并流程文档(建议 P0)
在 fork 的合并流程文档里写死三条(与 GAPS.md 一致):
1. `web/src/routeTree.gen.ts` 冲突 → **直接删掉,重跑 `bun run build`**,不手动解冲突。
2. `web/src/i18n/locales/*.json` 冲突 → 保留双方,跑 `bun run i18n:sync` 归一化。
3. `web/src/features/wallet/index.tsx` 的 `RechargeFormCard` props 块(改造后位于 `TabsContent value='funds'` 内)是**上游最可能变动的区域**;合并时以上游 props 列表为准,只保留我们的 `<TabsContent>` 包裹层。
