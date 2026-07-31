# 钱包页面与订阅套餐 UI

勘察完成。以下是钱包/充值页面 + 订阅套餐 UI 的完整报告。

---

# 一、钱包页面(Wallet)整体结构

## 1.1 路由挂载点

**文件**: `C:\Users\Administrator\Desktop\qianye\qianye-newapi\web\src\routes\_authenticated\wallet\index.tsx` (全文仅 37 行)

```tsx
// line 22
import { Wallet } from '@/features/wallet'

// line 24-26
const walletSearchSchema = z.object({
  show_history: z.boolean().optional(),
})

// line 28-31
export const Route = createFileRoute('/_authenticated/wallet/')({
  component: RouteComponent,
  validateSearch: walletSearchSchema,
})

// line 33-36
function RouteComponent() {
  const { show_history } = Route.useSearch()
  return <Wallet initialShowHistory={show_history} />
}
```

这是**整个钱包页最小、最干净的挂载点**(见【扩展点建议】)。

## 1.2 页面主组件

**文件**: `web\src\features\wallet\index.tsx` (390 行)

签名: `export function Wallet(props: WalletProps)`,`interface WalletProps { initialShowHistory?: boolean }` (line 58-62)

**当前布局结构** (line 285-353):

```
SectionPageLayout
├─ SectionPageLayout.Title  → t('Wallet')            [line 288]
└─ SectionPageLayout.Content
   └─ div.mx-auto.flex.w-full.max-w-7xl.flex-col.gap-4 [line 290]
      ├─ <WalletStatsCard user loading />              [line 291]
      ├─ div (双栏 grid，showSubscriptionPanel 决定)    [line 293-299]
      │   ├─ div#wallet-add-funds.scroll-mt-4          [line 300]
      │   │   └─ <RechargeFormCard ... />              [line 301-331]  ← "添加资金"
      │   └─ <SubscriptionPlansCard ... />             [line 334-339]  ← "订阅套餐"
      └─ <AffiliateRewardsCard ... />                  [line 342-350]
+ 4 个 Dialog: PaymentConfirmDialog(355) / TransferDialog(368)
              / BillingHistoryDialog(376) / CreemConfirmDialog(381)
```

**关键的分栏条件类名** (line 293-299) —— 这就是要替换成 Tabs 的地方:

```tsx
<div
  className={
    showSubscriptionPanel
      ? 'grid gap-4 xl:grid-cols-[minmax(0,1.05fr)_minmax(360px,0.95fr)] xl:items-start'
      : 'grid gap-4'
  }
>
```

`showSubscriptionPanel` 状态定义在 line 81 (`useState(true)`),由 `handleSubscriptionAvailabilityChange` (line 278-283) 通过 `SubscriptionPlansCard` 的 `onAvailabilityChange` 回调驱动 —— **这个回调就是"套餐 Tab 是否要显示"的现成判据**。

## 1.3 页面状态清单 (line 64-81)

| 状态 | 归属 | 说明 |
|---|---|---|
| `user` / `userLoading` | 共用 | `getSelf()` 返回 `UserWalletData` |
| `topupAmount` / `selectedPreset` / `selectedPaymentMethod` / `selectedWaffoMethodIndex` / `paymentLoading` / `confirmDialogOpen` / `redemptionCode` / `creemDialogOpen` / `selectedCreemProduct` | **仅充值 Tab** | |
| `transferDialogOpen` / `billingDialogOpen` | 共用/推广 | |
| `showSubscriptionPanel` | 布局 | 见上 |

Hooks (line 83-110): `useStatus` / `useSystemConfig` / `useTopupInfo` / `usePayment` / `useAffiliate` / `useRedemption` / `useCreemPayment` / `useWaffoPayment` / `useWaffoPancakePayment`

---

# 二、"添加资金" UI

**文件**: `web\src\features\wallet\components\recharge-form-card.tsx` (565 行)

签名: `export function RechargeFormCard({...}: RechargeFormCardProps)` (line 86-114),props 接口在 line 56-84 (共 28 个 props)。

外层用 `TitledCard` (line 198-218):
```tsx
<TitledCard
  title={t('Add Funds')}                                  // line 199  中文="添加资金"
  description={t('Choose an amount and payment method')}   // line 200
  icon={<WalletCards className='h-4 w-4' />}
  iconTone='success'
  disableHoverEffect
  action={ /* Order History 按钮 → onOpenBilling */ }       // line 204-216
  contentClassName='space-y-4 sm:space-y-6'
>
```

内部区块:
| 区块 | 行号 | 说明 |
|---|---|---|
| 预设金额网格 | 224-282 | `grid grid-cols-2 ... md:grid-cols-4` |
| 自定义金额 + 应付金额 | 284-314 | |
| 支付方式(标准) | 316-396 | `topupInfo.pay_methods` |
| Waffo 支付方式 | 398-476 | |
| Creem 商品区 | 490-504 | 委托给 `CreemProductsSection` |
| 兑换码区 | 506-561 | |
| loading 骨架屏 | 146-195 | 提前 return |

子组件: `web\src\features\wallet\components\creem-products-section.tsx`

---

# 三、"订阅套餐" 部分

**文件**: `web\src\features\wallet\components\subscription-plans-card.tsx` (665 行) —— **这是本次改动的核心文件**

签名 (line 95-100):
```tsx
export function SubscriptionPlansCard({
  topupInfo, onAvailabilityChange, userQuota, onPurchaseSuccess,
}: SubscriptionPlansCardProps)
```
Props 接口 line 64-69:
```ts
interface SubscriptionPlansCardProps {
  topupInfo: TopupInfo | null
  onAvailabilityChange?: (available: boolean) => void
  userQuota?: number
  onPurchaseSuccess?: () => void | Promise<void>
}
```

## 3.1 数据流

| 层 | 位置 |
|---|---|
| 前端 API | `web\src\features\subscriptions\api.ts:222` `getPublicPlans()` → `GET /api/subscription/plans` |
| | `api.ts:215` `getSelfSubscriptionFull()` → `GET /api/subscription/self` |
| | `api.ts:227` `updateBillingPreference(preference)` → `PUT /api/subscription/self/preference` |
| 组件内取数 | `fetchPlans` (line 127-136)、`fetchSelfSubscription` (line 138-151)、`useEffect` 初始化 (line 153-160) |
| 本地状态 | `plans` (103)、`activeSubscriptions` (104)、`allSubscriptions` (107)、`billingPreference` (110)、`loading` (112)、`refreshing` (113)、`purchaseOpen` (115)、`selectedPlan` (116) |

## 3.2 前端类型

**文件**: `web\src\features\subscriptions\types.ts`

```ts
// line 25-47
export const subscriptionPlanSchema = z.object({
  id: z.number(),
  title: z.string(),
  subtitle: z.string().optional(),          // ← 唯一的"详细内容/备注"字段
  price_amount: z.number(),
  currency: z.string().default('USD'),
  duration_unit: z.enum(['year','month','day','hour','custom']),
  duration_value: z.number(),
  custom_seconds: z.number().optional(),
  quota_reset_period: z.enum(['never','daily','weekly','monthly','custom']),
  quota_reset_custom_seconds: z.number().optional(),
  enabled: z.boolean(),
  sort_order: z.number(),
  allow_balance_pay: z.boolean().optional().default(true),
  allow_wallet_overflow: z.boolean().optional().default(true),
  max_purchase_per_user: z.number(),
  total_amount: z.number(),
  upgrade_group: z.string().optional(),
  downgrade_group: z.string().optional(),
  stripe_price_id: z.string().optional(),
  creem_product_id: z.string().optional(),
  waffo_pancake_product_id: z.string().optional(),
})
export type SubscriptionPlan = z.infer<typeof subscriptionPlanSchema>   // line 49

export interface PlanRecord { plan: SubscriptionPlan }                  // line 51-53

// line 59-70
export const userSubscriptionSchema = z.object({
  id, user_id, plan_id, status, source?, start_time, end_time,
  amount_total, amount_used, next_reset_time?,
})
export interface UserSubscriptionRecord { subscription: UserSubscription }  // line 74-76

// line 141-145
export interface SelfSubscriptionData {
  billing_preference: string
  subscriptions: UserSubscriptionRecord[]        // 仅 active
  all_subscriptions: UserSubscriptionRecord[]    // 含过期
}
```

**重要**: 前端 `SubscriptionPlan` **没有** `description` / `remark` / `notes` / `features` 字段。所谓"套餐详细内容备注"在现有数据模型里**只有 `subtitle` 一个字段**(后台表单标签为 `t('Plan Subtitle')` = 中文"套餐副标题")。

## 3.3 "当前套餐" 卡片位置

不是独立组件,是 `subscription-plans-card.tsx` 内部的 **"My Subscriptions"** 区块 (line 271-522),中文 i18n = "我的订阅" (`zh.json:2794`)。

结构:
- 外框 `<div className='rounded-xl border p-3 sm:p-4'>` (line 271)
- 头部: 标题 + active/expired 计数 + 计费偏好 `<Select>` + 刷新按钮 (line 272-380)
- **订阅列表容器** (line 399):
  ```tsx
  <div className='max-h-64 space-y-3 overflow-y-auto pr-1'>
  ```
- 每条订阅卡片 (line 448-511),显示: 套餐标题+#id、状态徽章、剩余天数、到期时间、下次重置时间、`amount_used/amount_total`+剩余、使用百分比 `Progress`

`planTitleMap` (line 214-222) 用 `plans` 列表把 `plan_id` 反查标题 —— **后端 `/api/subscription/self` 不返回套餐详情**,只返回 `subscription` 对象。

## 3.4 可选套餐网格 (line 525-633)

```tsx
// line 526
<div className='grid grid-cols-1 gap-3 2xl:grid-cols-2 2xl:gap-4'>
```
每张卡 (line 552-626)。`benefits` 数组构造在 line 537-549:
```tsx
const benefits = [
  `${t('Validity Period')}: ${formatDuration(plan, t)}`,
  formatResetPeriod(plan, t) !== t('No Reset') ? `${t('Quota Reset')}: ...` : null,
  totalAmount > 0 ? `${t('Total Quota')}: ${formatQuota(totalAmount)}`
                  : `${t('Total Quota')}: ${t('Unlimited')}`,
  limit > 0 ? `${t('Purchase Limit')}: ${limit}` : null,
  plan.upgrade_group ? `${t('Upgrade Group')}: ${plan.upgrade_group}` : null,
].filter(Boolean) as string[]
```
渲染在 line 587-597。**注意 line 588 `benefits.map((label) => ... key={label})` 用文案当 key,重复文案会 React key 冲突。**

"立即订阅"按钮: line 613-622,`t('Subscribe Now')` = 中文"立即订阅",`onClick` 里 `setSelectedPlan(p); setPurchaseOpen(true)`。

---

# 四、⭐ "套餐详细内容备注显示不全" —— 精确定位

**根因是 3 处 `truncate`(即 `text-overflow: ellipsis; white-space: nowrap; overflow: hidden`) + 1 处 DB 长度限制 + 1 处完全不渲染。**

## 4.1 主犯：套餐卡片副标题单行截断

**`web\src\features\wallet\components\subscription-plans-card.tsx:563-567`**

```tsx
557  <CardContent className='flex h-full flex-col p-3.5 sm:p-4'>
558    <div className='mb-2 flex items-start justify-between gap-3'>
559      <div className='min-w-0'>
560        <h4 className='truncate font-semibold'>            ← 标题也被截断
561          {plan.title || t('Subscription Plans')}
562        </h4>
563        {plan.subtitle && (
564          <p className='text-muted-foreground truncate text-xs'>   ← ★ 备注被截断
565            {plan.subtitle}
566          </p>
567        )}
568      </div>
```

- **line 564 的 `truncate`** 强制单行 + 省略号。副标题再长也只显示一行。
- **line 559 的 `min-w-0`** 配合父级 `flex` 让宽度收缩到容器内,长文本无法撑开。
- **修复方向**: 把 line 564 的 `truncate` 换成 `line-clamp-3 whitespace-normal [overflow-wrap:anywhere]`,或彻底去掉截断改成完整多行 `whitespace-pre-wrap`;同时 line 560 的 `truncate` 也建议改为 `line-clamp-2`。
- 注意 line 557 的 `flex h-full flex-col` + line 587 的 `flex-1` 已经是弹性高度,**没有固定高度限制**,所以只要去掉 `truncate` 内容就能完整展开,不需要动外层。

## 4.2 从犯：管理端表格也截断

**`web\src\features\subscriptions\components\subscriptions-columns.tsx:54-62`**
```tsx
54  <div className='max-w-full min-w-0'>
55    <div className='truncate font-medium'>{plan.title}</div>
56    {plan.subtitle && (
57      <div className='text-muted-foreground truncate text-xs'>   ← 截断
58        {plan.subtitle}
59      </div>
60    )}
61  </div>
```
该列 `size: 200` (line 64)。管理员在列表里也看不全自己填的备注。

## 4.3 从犯：订阅弹窗里标题被硬性 200px 截断

**`web\src\features\subscriptions\components\dialogs\subscription-purchase-dialog.tsx:279-281`**
```tsx
279  <span className='max-w-[200px] truncate text-sm font-medium'>
280    {plan.title}
281  </span>
```

## 4.4 从犯：弹窗**完全不渲染** `subtitle`

`subscription-purchase-dialog.tsx` 全文没有任何 `plan.subtitle` 引用(已 grep 确认,`subtitle` 在该文件出现 0 次)。所以用户点"立即订阅"后**看不到任何套餐描述**。

## 4.5 数据库层限制

**`model\subscription.go:150`**
```go
Subtitle string `json:"subtitle" gorm:"type:varchar(255);default:''"`
```
`varchar(255)` —— 若"详细内容备注"需要长文本(多行、列点、Markdown),**255 字符根本不够**。表单侧也用的是单行 `<Input>` 而非 `<Textarea>`:
`subscriptions-mutate-drawer.tsx:303-318`
```tsx
name='subtitle'
<FormLabel>{t('Plan Subtitle')}</FormLabel>   // 中文"套餐副标题"
<Input {...field} placeholder={t('e.g. Suitable for light usage')} />
```
以及 zod schema `plan-form.ts:29` `subtitle: z.string().optional()` (无 max 限制,超 255 会被 DB 静默截断或报错)。

## 4.6 次要：我的订阅列表 16rem 高度限制

`subscription-plans-card.tsx:399` `<div className='max-h-64 space-y-3 overflow-y-auto pr-1'>` —— 订阅条数多时会出现内嵌滚动条,视觉上也是"显示不全"。

---

# 五、"立即订阅"弹窗

**文件**: `web\src\features\subscriptions\components\dialogs\subscription-purchase-dialog.tsx` (445 行)

签名: `export function SubscriptionPurchaseDialog(props: Props)` (line 71),Props 定义 line 56-69:
```ts
interface Props {
  open: boolean
  onOpenChange: (open: boolean) => void
  plan: PlanRecord | null
  enableStripe?: boolean
  enableCreem?: boolean
  enableWaffoPancake?: boolean
  enableOnlineTopUp?: boolean
  epayMethods?: PaymentMethod[]
  purchaseLimit?: number
  purchaseCount?: number
  userQuota?: number
  onPurchaseSuccess?: () => void | Promise<void>
}
```
调用点: `subscription-plans-card.tsx:636-662`

弹窗容器 (line 258-272):
```tsx
<Dialog
  open={props.open} onOpenChange={props.onOpenChange}
  title={<><Crown className='h-5 w-5' />{t('Purchase Subscription')}</>}
  contentClassName='max-sm:w-[calc(100vw-1.5rem)] sm:max-w-md'   // ← 最大宽度仅 md(28rem)
  titleClassName='flex items-center gap-2'
  contentHeight='auto'
  bodyClassName='space-y-4'
>
```

## 5.1 当前显示的字段 (信息块 line 274-322)

| 行号 | 标签 | 字段 |
|---|---|---|
| 275-282 | `t('Plan Name')` | `plan.title` (**max-w-[200px] truncate**) |
| 283-291 | `t('Validity Period')` | `formatDuration(plan, t)` |
| 292-299 | `t('Reset Period')` | `formatResetPeriod(plan, t)`(仅非 No Reset 时) |
| 300-308 | `t('Plan Quota')` | `total_amount > 0 ? formatQuota() : t('Unlimited')` |
| 309-316 | `t('Upgrade Group')` | `plan.upgrade_group`(条件) → `<GroupBadge>` |
| 318-321 | `t('Amount Due')` | `$${price}` |
| 324-331 | 购买上限告警 | 仅 `limitReached` 时 |
| 333-364 | 余额支付块 | `t('Required')` / `t('Available')` / 余额不足告警 / `t('Pay with Balance')` |
| 366-440 | 支付方式 | Stripe / Creem / Waffo Pancake 按钮 + 易支付 Select |

## 5.2 ❌ 弹窗中缺失的字段

1. **`plan.subtitle`** —— 完全没渲染(最关键)
2. **`plan.downgrade_group`** —— 到期后降级到哪个组,用户不知道
3. **`plan.max_purchase_per_user` / 已购次数** —— 只在超限时以 Alert 形式出现(line 324-331),未超限时不展示 `purchaseCount/purchaseLimit`
4. **`plan.allow_wallet_overflow`** —— "配额用尽后是否可继续用钱包余额",直接影响用户决策
5. **`plan.allow_balance_pay`** —— 只用于禁用按钮(line 111、342-347),没有正面说明
6. **`plan.currency`** —— 硬编码 `$` 前缀(line 320 `${price}`)
7. 到期时间预览(现在只有"多少个月",没有"到 xxxx-xx-xx")
8. `plan.id`

## 5.3 支付处理函数(供参考)

| 函数 | 行号 | API |
|---|---|---|
| `handlePayStripe` | 117-137 | `paySubscriptionStripe` |
| `handlePayCreem` | 139-159 | `paySubscriptionCreem` |
| `handlePayWaffoPancake` | 163-182 | `paySubscriptionWaffoPancake`(同页跳转) |
| `handlePayEpay` | 188-230 | `paySubscriptionEpay`(动态 form POST) |
| `handlePayBalance` | 232-256 | `paySubscriptionBalance` |

---

# 六、后端接口与模型

## 6.1 Model

**`model\subscription.go`**

```go
// line 146-191  套餐定义
type SubscriptionPlan struct {
    Id                      int     `json:"id"`
    Title                   string  `json:"title" gorm:"type:varchar(128);not null"`          // line 149
    Subtitle                string  `json:"subtitle" gorm:"type:varchar(255);default:''"`     // line 150 ★
    PriceAmount             float64 `json:"price_amount" gorm:"type:decimal(10,6);not null;default:0"`
    Currency                string  `json:"currency" gorm:"type:varchar(8);not null;default:'USD'"`
    DurationUnit            string  `json:"duration_unit" gorm:"type:varchar(16);not null;default:'month'"`
    DurationValue           int     `json:"duration_value" gorm:"type:int;not null;default:1"`
    CustomSeconds           int64   `json:"custom_seconds"`
    Enabled                 bool    `json:"enabled" gorm:"default:true"`
    SortOrder               int     `json:"sort_order"`
    AllowBalancePay         *bool   `json:"allow_balance_pay"`         // line 163
    AllowWalletOverflow     *bool   `json:"allow_wallet_overflow"`     // line 166
    StripePriceId           string  `json:"stripe_price_id"`
    CreemProductId          string  `json:"creem_product_id"`
    WaffoPancakeProductId   string  `json:"waffo_pancake_product_id"`
    MaxPurchasePerUser      int     `json:"max_purchase_per_user"`     // line 173
    UpgradeGroup            string  `json:"upgrade_group"`             // line 176
    DowngradeGroup          string  `json:"downgrade_group"`           // line 179
    TotalAmount             int64   `json:"total_amount"`              // line 182
    QuotaResetPeriod        string  `json:"quota_reset_period"`
    QuotaResetCustomSeconds int64   `json:"quota_reset_custom_seconds"`
    CreatedAt, UpdatedAt    int64
}
```
其它: `NormalizeDefaults()` (line 204-211)、`UserSubscription` (line 249-287)、`SubscriptionSummary { Subscription *UserSubscription }` (line 295-297)、`SubscriptionOrder` (line 213-224)、`SubscriptionPlanInfo { PlanId; PlanTitle }` (line 1470-1473)。

常量: `SubscriptionDuration{Year,Month,Day,Hour,Custom}` (line 19-25),`SubscriptionReset{Never,Daily,Weekly,Monthly,Custom}` (line 28-34)。

## 6.2 Controller

**`controller\subscription.go`**
```go
// line 18-20
type SubscriptionPlanDTO struct {
    Plan model.SubscriptionPlan `json:"plan"`      // 直接内嵌 model,无独立 DTO 字段裁剪
}

func GetSubscriptionPlans(c *gin.Context)          // line 32-49   GET /api/subscription/plans
func GetSubscriptionSelf(c *gin.Context)           // line 51-72   GET /api/subscription/self
func UpdateSubscriptionPreference(c *gin.Context)  // line 74-94
func SubscriptionRequestBalancePay(c *gin.Context) // line 96-114
func AdminListSubscriptionPlans(c *gin.Context)    // line 121-135
func AdminCreateSubscriptionPlan(c *gin.Context)   // line 141-214
func AdminUpdateSubscriptionPlan(c *gin.Context)   // line 216-...
```
`GetSubscriptionSelf` 返回 (line 67-71): `{"billing_preference", "subscriptions"(active), "all_subscriptions"(全部)}` —— **不含 plan 详情**。

`GetSubscriptionPlans` line 33-36: 若 `!operation_setting.IsPaymentComplianceConfirmed()` 直接返回空数组。

## 6.3 路由注册

**`router\api-router.go:155-184`**
```go
subscriptionRoute := apiRouter.Group("/subscription")     // line 156
subscriptionRoute.Use(middleware.UserAuth())              // line 157
{
    subscriptionRoute.GET("/plans", controller.GetSubscriptionPlans)              // 159
    subscriptionRoute.GET("/self", controller.GetSubscriptionSelf)                // 160
    subscriptionRoute.PUT("/self/preference", controller.UpdateSubscriptionPreference) // 161
    subscriptionRoute.POST("/balance/pay", middleware.CriticalRateLimit(), ...)   // 162
    ... epay/stripe/creem/waffo-pancake pay                                       // 163-166
}
subscriptionAdminRoute := apiRouter.Group("/subscription/admin")  // line 168
subscriptionAdminRoute.Use(middleware.AdminAuth())                // line 169
{ /* line 171-183 */ }
```

其它支付相关 controller 文件: `controller\subscription_payment_{creem,epay,stripe,waffo_pancake}.go`

---

# 七、项目现有 Tabs 组件

**文件**: `web\src\components\ui\tabs.tsx` (99 行) —— 基于 **Base UI**(不是 Radix)

```tsx
import { Tabs as TabsPrimitive } from '@base-ui/react/tabs'   // line 19

function Tabs({ className, orientation = 'horizontal', ...props }: TabsPrimitive.Root.Props)  // line 24
function TabsList({ className, variant = 'default', ...props }
    : TabsPrimitive.List.Props & VariantProps<typeof tabsListVariants>)                        // line 57
function TabsTrigger({ className, ...props }: TabsPrimitive.Tab.Props)                         // line 72
function TabsContent({ className, ...props }: TabsPrimitive.Panel.Props)                       // line 88

export { Tabs, TabsList, TabsTrigger, TabsContent, tabsListVariants }                          // line 98
```

`tabsListVariants` (line 42-55) 支持 `variant: 'default' | 'line'`。

## 7.1 现有用法示例 A —— 带 TabsContent 的完整用法(最贴近本次需求)

**`web\src\features\pricing\components\model-details.tsx:1152-1209`**
```tsx
<Tabs defaultValue='overview' className='gap-4'>
  <TabsList className='bg-muted/60 grid w-full grid-cols-3 gap-1 rounded-lg p-1 group-data-horizontal/tabs:h-auto'>
    {TAB_VALUES.map((value) => {
      const Icon = TAB_META[value].icon
      return (
        <TabsTrigger key={value} value={value}
          className='h-8 min-w-0 gap-1.5 rounded-md px-3 text-xs sm:text-sm'>
          <Icon className='size-3.5' />
          <span className='truncate'>{t(TAB_META[value].labelKey)}</span>
        </TabsTrigger>
      )
    })}
  </TabsList>

  <TabsContent value='overview' className='space-y-6 outline-none'> ... </TabsContent>
  <TabsContent value='performance' className='outline-none'> ... </TabsContent>
  <TabsContent value='api' className='outline-none'> ... </TabsContent>
</Tabs>
```

## 7.2 现有用法示例 B —— 受控模式

**`web\src\features\usage-logs\index.tsx:133-152`**
```tsx
<Tabs value={viewScope} onValueChange={handleViewScopeChange}>
  <TabsList>
    <TabsTrigger value='all'>{t('All')}</TabsTrigger>
    <TabsTrigger value='self'>{t('Only Mine')}</TabsTrigger>
  </TabsList>
</Tabs>
```

其它使用文件(共 19 个): `features/models/index.tsx:133`、`features/dashboard/index.tsx`、`features/profile/components/profile-settings-card.tsx`、`features/system-settings/integrations/payment-settings-section.tsx` 等。

**⚠️ 注意**: Base UI 的 `Tabs.Panel` 默认**不 keepMounted**,切换 Tab 会卸载非活动面板。全仓库 grep `keepMounted` 结果为 0(无先例)。这意味着:
- 切到"订阅套餐"Tab → `SubscriptionPlansCard` 挂载 → 触发 `fetchPlans()` + `fetchSelfSubscription()`;切回充值 Tab 再切回来会**重复请求**。
- `SubscriptionPurchaseDialog` 挂在 `SubscriptionPlansCard` 内部 (line 636),会随之卸载。
- 若要避免,需给 `<TabsContent>` 传 `keepMounted`(Base UI 支持该 prop),或把取数逻辑提到 `Wallet` 层。

---

# 八、其它相关组件/文件速查

| 用途 | 文件 | 关键行 |
|---|---|---|
| 钱包统计卡 | `features\wallet\components\wallet-stats-card.tsx` | `export function WalletStatsCard(props)` line 33 |
| 推广奖励卡 | `features\wallet\components\affiliate-rewards-card.tsx` | line 40 |
| 账单历史弹窗 | `features\wallet\components\dialogs\billing-history-dialog.tsx` | line 63 |
| 支付确认弹窗 | `features\wallet\components\dialogs\payment-confirm-dialog.tsx` | |
| 充值信息 hook | `features\wallet\hooks\use-topup-info.ts` | `export function useTopupInfo()` line 166 |
| 钱包类型 | `features\wallet\types.ts` | `TopupInfo` line 122-159、`UserWalletData` line 226-245 |
| 钱包常量 | `features\wallet\constants.ts` | `PAYMENT_TYPES` line 32-39 |
| 套餐格式化 | `features\subscriptions\lib\format.ts` | `formatDuration` line 25、`formatResetPeriod` line 47、`formatTimestamp` line 65 |
| 套餐表单 schema | `features\subscriptions\lib\plan-form.ts` | `getPlanFormSchema` line 26、`PLAN_FORM_DEFAULTS` line 58、`planToFormValues` line 80、`formValuesToPlanPayload` line 104 |
| 管理端套餐抽屉 | `features\subscriptions\components\subscriptions-mutate-drawer.tsx` | subtitle 字段 line 303-318 |
| 管理端套餐表格列 | `features\subscriptions\components\subscriptions-columns.tsx` | line 46-64 |
| 页面布局壳 | `components\layout\components\section-page-layout.tsx` | `SectionPageLayout` line 57,内容区滚动 line 100-108 |
| 卡片壳 | `components\ui\titled-card.tsx` | `TitledCard` line 48,props line 32-46 |
| 弹窗壳 | `components\dialog.tsx` | `Dialog` line 51,body 滚动容器 line 97-113(`max-h-[calc(100vh-14rem)]`) |
| Markdown 渲染 | `components\ui\markdown.tsx` | `export function Markdown(props)` line 747 |
| 侧边栏菜单注册 | `hooks\use-sidebar-data.ts` | `/wallet` 条目 line 106-110 |
| 侧边栏可见性映射 | `hooks\use-sidebar-config.ts` | `'/wallet': { section:'personal', module:'topup' }` line 108 |
| i18n 静态键登记 | `web\src\i18n\static-keys.ts` | `STATIC_I18N_KEYS` line 21 起,575 行 |
| i18n 语言包 | `web\src\i18n\locales\{zh,en,zh-TW,ja,fr,ru,vi}.json` | zh: "Add Funds"=181、"Plan Subtitle"=3366、"Subscribe Now"=4330、"Subscription Plans"=4338、"My Subscriptions"=2794、"Wallet"=5102 |
| i18n 同步脚本 | `web\scripts\sync-i18n.mjs` (`bun run i18n:sync`) | |

指向 `/wallet` 的现有链接(改路由 search 参数时需注意): `components\layout\components\mobile-drawer.tsx:126`、`components\profile-dropdown.tsx:111`、`features\dashboard\components\overview\overview-dashboard.tsx:512`、`features\dashboard\components\overview\summary-cards.tsx:347`、`lib\legacy-route.ts:82`(`/console/topup` → `/wallet`)。

---

# 九、【扩展点建议】

## 9.1 最干净的挂载点排序(从"几乎不碰原文件"到"直接改")

### 方案 A(推荐,上游冲突面最小)—— 只改路由文件 2 行

`web\src\routes\_authenticated\wallet\index.tsx` 全文 37 行,**是整个钱包页最小的原有文件**,上游改动概率极低。

```diff
- import { Wallet } from '@/features/wallet'          // line 22
+ import { WalletExt } from '@/features/wallet-ext'   // 新建目录

  const walletSearchSchema = z.object({
    show_history: z.boolean().optional(),
+   tab: z.enum(['funds', 'plans']).optional(),        // 新增:Tab 深链
  })

  function RouteComponent() {
-   const { show_history } = Route.useSearch()
-   return <Wallet initialShowHistory={show_history} />   // line 35
+   const s = Route.useSearch()
+   return <WalletExt initialShowHistory={s.show_history} initialTab={s.tab} />
  }
```

新建目录 `web\src\features\wallet-ext\`:
- `index.tsx` —— fork 自 `features\wallet\index.tsx`,把 line 293-340 的 grid 换成 Tabs 布局。可以直接**复用**原目录的所有子组件(`RechargeFormCard`、`WalletStatsCard`、`AffiliateRewardsCard`、各 Dialog、各 hook 都从 `@/features/wallet/...` 导入),只 fork 这一个编排文件。
- `subscription-plans-card.tsx` —— fork 自 `features\wallet\components\subscription-plans-card.tsx`,修复截断 + 增强。
- `subscription-purchase-dialog.tsx` —— fork 自 `features\subscriptions\components\dialogs\subscription-purchase-dialog.tsx`,补齐字段。

**改动原有文件总计: 1 个文件、约 4 行。**

### 方案 B(改动集中但触碰主文件)—— 直接改 3 个文件

| 文件 | 改动 | 行范围 |
|---|---|---|
| `features\wallet\index.tsx` | grid → Tabs;新增 `activeTab` state | 替换 line 293-340;line 81 附近加 state;line 19 补 import |
| `features\wallet\components\subscription-plans-card.tsx` | 去 truncate + 网格列数 + 补字段 | line 560、564、526、537-549 |
| `features\subscriptions\components\dialogs\subscription-purchase-dialog.tsx` | 补 subtitle 等字段 | line 268、274-322 之间插入 |

## 9.2 三项需求的精确改动清单

### 需求 1: "添加资金" / "订阅套餐" 拆成选项卡

**改**: `features\wallet\index.tsx`

1. **line 19-56 import 区**: 加 `import { Tabs, TabsList, TabsTrigger, TabsContent } from '@/components/ui/tabs'`
2. **line 81 附近**: `const [activeTab, setActiveTab] = useState<'funds'|'plans'>('funds')`
3. **替换 line 293-340** 整块:
```tsx
<Tabs value={activeTab} onValueChange={(v) => v && setActiveTab(v as 'funds'|'plans')} className='gap-4'>
  <TabsList className='bg-muted/60 grid w-full max-w-md grid-cols-2 gap-1 rounded-lg p-1 group-data-horizontal/tabs:h-auto'>
    <TabsTrigger value='funds' className='h-8 gap-1.5 rounded-md px-3 text-xs sm:text-sm'>
      <WalletCards className='size-3.5' />{t('Add Funds')}
    </TabsTrigger>
    {showSubscriptionPanel && (
      <TabsTrigger value='plans' className='h-8 gap-1.5 rounded-md px-3 text-xs sm:text-sm'>
        <Crown className='size-3.5' />{t('Subscription Plans')}
      </TabsTrigger>
    )}
  </TabsList>

  <TabsContent value='funds' keepMounted className='outline-none'>
    <div id='wallet-add-funds' className='scroll-mt-4'>
      <RechargeFormCard {...原 line 301-331 的 props 一字不改} />
    </div>
  </TabsContent>

  <TabsContent value='plans' keepMounted className='outline-none'>
    <SubscriptionPlansCard {...原 line 334-339 的 props} />
  </TabsContent>
</Tabs>
```
4. **line 288 `SectionPageLayout.Title`** 保持 `t('Wallet')` 不变。
5. **`showSubscriptionPanel` 语义变更**: 现在是"是否分两栏",改后是"是否显示套餐 Tab"。`handleSubscriptionAvailabilityChange` (line 278-283) 逻辑不用改;但注意 `SubscriptionPlansCard` 在 line 256-258 `if (plans.length === 0 && !hasAny) return null` 会自己返回 null,所以 Tab 触发器要用 `showSubscriptionPanel` 门控,否则会出现空 Tab。
6. **`keepMounted`**: 强烈建议加,否则每次切 Tab 都会重新 `getPublicPlans()` + `getSelfSubscriptionFull()`(见 §7 说明)。若不加,需要把 `SubscriptionPurchaseDialog` 从 `SubscriptionPlansCard` 内提到 `Wallet` 层。
7. **i18n**: `t('Add Funds')` / `t('Subscription Plans')` 两个 key 已存在于全部 7 个语言包,**不需要改 i18n 文件**。
8. **深链**: 若要支持 `/wallet?tab=plans`,改 `routes\_authenticated\wallet\index.tsx:24-26` 的 `walletSearchSchema`。

### 需求 2: 修复套餐详情截断

**改**: `features\wallet\components\subscription-plans-card.tsx`

| 行 | 现状 | 建议 |
|---|---|---|
| **564** | `className='text-muted-foreground truncate text-xs'` | `className='text-muted-foreground text-xs leading-relaxed whitespace-pre-wrap [overflow-wrap:anywhere]'`(完整展示)或 `line-clamp-3` + Tooltip 悬浮全文 |
| **560** | `className='truncate font-semibold'` | `className='font-semibold [overflow-wrap:anywhere]'` 或 `line-clamp-2` |
| **526** | `grid grid-cols-1 gap-3 2xl:grid-cols-2 2xl:gap-4` | 改 Tab 后卡片得到全宽,建议 `grid grid-cols-1 gap-3 sm:grid-cols-2 xl:grid-cols-3 2xl:grid-cols-4` |
| **399** | `max-h-64 space-y-3 overflow-y-auto pr-1` | 提高到 `max-h-96` 或改为 `md:max-h-none` |
| **588** | `benefits.map((label) => ... key={label})` | 改用 index 或结构化对象 `{id,label}` 做 key |
| **537-549** | `benefits` 数组 | 建议追加 `plan.downgrade_group`、`plan.allow_wallet_overflow`、`plan.allow_balance_pay` 三条 |

顺带(可选): `features\subscriptions\components\subscriptions-columns.tsx:57` 的 `truncate` 同样问题(管理端表格)。

**若"备注"要支持长文本/多行**,则必须改后端(3 处):
1. `model\subscription.go:150` `varchar(255)` → `type:text`(GORM AutoMigrate 会自动 ALTER)
2. `features\subscriptions\lib\plan-form.ts:29` schema 保持 optional(可加 `.max(2000)`)
3. `features\subscriptions\components\subscriptions-mutate-drawer.tsx:310` `<Input>` → `<Textarea>`(`@/components/ui/textarea` 已存在)

> **⚠️ 与"独立 MySQL 库"约束的冲突提示**: 若不想动上游 `model\subscription.go`,可以在**新库**里建一张扩展表(如 `plan_ext(plan_id, description TEXT, features JSON, ...)`),新增一个只读接口把扩展描述按 `plan_id` 合并进前端。这样后端零改动、前端只在 fork 出的卡片组件里多请求一次即可。这条路和"不改原项目文件"的整体约束最一致,建议架构师优先考虑。

### 需求 3: 增强"立即订阅"弹窗

**改**: `features\subscriptions\components\dialogs\subscription-purchase-dialog.tsx`

1. **line 268** `contentClassName='max-sm:w-[calc(100vw-1.5rem)] sm:max-w-md'` → `sm:max-w-lg`(内容变多需要更宽)
2. **line 279** 去掉 `max-w-[200px] truncate`,改 `text-right [overflow-wrap:anywhere]`
3. **在 line 282 之后插入** subtitle 展示块(不使用 `truncate`):
```tsx
{plan.subtitle && (
  <div className='border-t pt-2.5'>
    <span className='text-muted-foreground text-xs'>{t('Plan Details')}</span>
    <p className='mt-1 text-sm leading-relaxed whitespace-pre-wrap [overflow-wrap:anywhere]'>
      {plan.subtitle}
    </p>
  </div>
)}
```
4. **在 line 316 之后**(Upgrade Group 之后)插入 `downgrade_group`、`allow_wallet_overflow`、购买次数 `{props.purchaseCount}/{props.purchaseLimit}`(非超限时也显示)
5. `plan` 变量已在 line 85 解出(`const plan = props.plan?.plan`),所有字段可直接访问
6. `Dialog` 的 body 有 `max-h-[calc(100vh-14rem)]` + `overflow-y-auto`(`components\dialog.tsx:97-101`),内容变长会自动滚动,**不需要改 Dialog 组件**
7. 新增文案需要在 `web\src\i18n\locales\*.json` 7 个语言包补键(或跑 `bun run i18n:sync`);若文案来自常量而非 `t('字面量')`,还需登记到 `web\src\i18n\static-keys.ts`

## 9.3 后端扩展点(如需新增字段/新接口)

| 用途 | 挂载点 | 说明 |
|---|---|---|
| 新增用户侧只读接口 | `router\api-router.go:159` 之后,`subscriptionRoute` 组内 | 已挂 `middleware.UserAuth()`,加一行即可 |
| 新增管理侧接口 | `router\api-router.go:171` 之后,`subscriptionAdminRoute` 组内 | 已挂 `middleware.AdminAuth()` |
| 完全独立的新模块 | `router\api-router.go` 在 line 184 `}` 之后新增自己的 `Group("/xxx")` | **改 1 个文件 1 处,可挂载任意多新功能**——建议新增 `router\ext-router.go` 提供 `SetExtRouter(apiRouter *gin.RouterGroup)`,在 api-router.go 里只加一行调用 |
| 套餐 DTO 扩展 | `controller\subscription.go:18-20` `SubscriptionPlanDTO` | 目前只有 `Plan` 一个字段,加同级字段(如 `Ext *PlanExt \`json:"ext"\``)前端 `PlanRecord` 也只需加一个可选字段,向后兼容 |

---

## 十、需要注意的既有缺陷(顺手记录)

1. `subscription-plans-card.tsx:588` —— `key={label}` 用文案做 key,重复文案会冲突
2. `subscriptions\api.ts:208` 的 `getSelfSubscriptions()` 与 `:215` 的 `getSelfSubscriptionFull()` 打同一个 `/api/subscription/self`,返回类型却不同(`UserSubscriptionRecord[]` vs `SelfSubscriptionData`);`getSelfSubscriptions` 全仓库无调用方,类型是错的
3. `subscription-plans-card.tsx:190-192` 的 `isAvailable` 与 `:256` 的 `return null` 判据不一致(`isAvailable` 含 `loading`),Tab 化后要统一,否则 loading 期间 Tab 会闪烁
4. `subscription-purchase-dialog.tsx:320` 硬编码 `$` 符号,忽略 `plan.currency`
