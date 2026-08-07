/*
Copyright (C) 2023-2026 QuantumNous

This program is free software: you can redistribute it and/or modify
it under the terms of the GNU Affero General Public License as
published by the Free Software Foundation, either version 3 of the
License, or (at your option) any later version.

This program is distributed in the hope that it will be useful,
but WITHOUT ANY WARRANTY; without even the implied warranty of
MERCHANTABILITY or FITNESS FOR A PARTICULAR PURPOSE. See the
GNU Affero General Public License for more details.

You should have received a copy of the GNU Affero General Public License
along with this program. If not, see <https://www.gnu.org/licenses/>.

For commercial licensing, please contact support@quantumnous.com
*/
import type { TFunction } from 'i18next'
import { z } from 'zod'

import { parseQuotaFromDollars, quotaUnitsToDollars } from '@/lib/format'

import type { SubscriptionPlan, PlanPayload } from '../types'

export function getPlanFormSchema(t: TFunction) {
  return z.object({
    title: z.string().min(1, t('Please enter plan title')),
    // The column is varchar(255); without this the admin either gets an opaque
    // "Data too long" from MySQL or a silently truncated description.
    subtitle: z.string().max(255, t('qy_plan_subtitle_too_long')).optional(),
    price_amount: z.coerce.number().min(0, t('Please enter amount')),
    duration_unit: z.enum(['year', 'month', 'day', 'hour', 'custom']),
    duration_value: z.coerce.number().min(1),
    custom_seconds: z.coerce.number().min(0).optional(),
    quota_reset_period: z.enum([
      'never',
      'daily',
      'weekly',
      'monthly',
      'custom',
    ]),
    quota_reset_custom_seconds: z.coerce.number().min(0).optional(),
    enabled: z.boolean(),
    sort_order: z.coerce.number(),
    allow_balance_pay: z.boolean(),
    allow_wallet_overflow: z.boolean(),
    max_purchase_per_user: z.coerce.number().min(0),
    // 全站总名额（按人去重），与上面的"每人限购次数"是两个独立维度。
    // 它不是上游 subscription_plans 的列，只是搭表单的顺风车一起编辑，
    // 因此不会出现在 formValuesToPlanPayload 的结果里。
    max_total_users: z.coerce.number().min(0),
    total_amount: z.coerce.number().min(0),
    stripe_price_id: z.string().optional(),
    creem_product_id: z.string().optional(),
    waffo_pancake_product_id: z.string().optional(),
  })
}

export type PlanFormValues = z.infer<ReturnType<typeof getPlanFormSchema>>

export const PLAN_FORM_DEFAULTS: PlanFormValues = {
  title: '',
  subtitle: '',
  price_amount: 0,
  duration_unit: 'month',
  duration_value: 1,
  custom_seconds: 0,
  quota_reset_period: 'never',
  quota_reset_custom_seconds: 0,
  enabled: true,
  sort_order: 0,
  allow_balance_pay: true,
  allow_wallet_overflow: true,
  max_purchase_per_user: 0,
  max_total_users: 0,
  total_amount: 0,
  stripe_price_id: '',
  creem_product_id: '',
  waffo_pancake_product_id: '',
}

export function planToFormValues(plan: SubscriptionPlan): PlanFormValues {
  return {
    title: plan.title || '',
    subtitle: plan.subtitle || '',
    price_amount: Number(plan.price_amount || 0),
    duration_unit: plan.duration_unit || 'month',
    duration_value: Number(plan.duration_value || 1),
    custom_seconds: Number(plan.custom_seconds || 0),
    quota_reset_period: plan.quota_reset_period || 'never',
    quota_reset_custom_seconds: Number(plan.quota_reset_custom_seconds || 0),
    enabled: plan.enabled !== false,
    sort_order: Number(plan.sort_order || 0),
    allow_balance_pay: plan.allow_balance_pay !== false,
    allow_wallet_overflow: plan.allow_wallet_overflow !== false,
    max_purchase_per_user: Number(plan.max_purchase_per_user || 0),
    // 只能给占位 0：总名额在 qy 扩展库里，`SubscriptionPlan` 上没有这个字段。
    // 真值由弹窗单独请求回来后回填 —— 也就是说读取失败时这里的 0 **不是**"不限"，
    // 调用方必须自己挡住"没读到就照着 0 保存"，否则会悄悄抹掉已设的名额上限。
    max_total_users: 0,
    total_amount: quotaUnitsToDollars(Number(plan.total_amount || 0)),
    stripe_price_id: plan.stripe_price_id || '',
    creem_product_id: plan.creem_product_id || '',
    waffo_pancake_product_id: plan.waffo_pancake_product_id || '',
  }
}

export function formValuesToPlanPayload(values: PlanFormValues): PlanPayload {
  // 总名额必须在这里摘掉：上游 `AdminUpsertSubscriptionPlanRequest` 绑到
  // `model.SubscriptionPlan`，多出来的键会被 Go 静默丢弃 —— 表单显示"保存成功"
  // 而名额压根没落库。它由 setPlanSeatLimit 单独写进扩展库。
  const { max_total_users, ...planValues } = values
  return {
    plan: {
      ...planValues,
      price_amount: Number(values.price_amount || 0),
      currency: 'USD',
      duration_value: Number(values.duration_value || 0),
      custom_seconds: Number(values.custom_seconds || 0),
      quota_reset_period: values.quota_reset_period || 'never',
      quota_reset_custom_seconds:
        values.quota_reset_period === 'custom'
          ? Number(values.quota_reset_custom_seconds || 0)
          : 0,
      sort_order: Number(values.sort_order || 0),
      max_purchase_per_user: Number(values.max_purchase_per_user || 0),
      total_amount: parseQuotaFromDollars(Number(values.total_amount || 0)),
      // ── 升级分组 / 降级分组：显式清空，不是"忘了传" ──────────────────────
      //
      // 这两列在上游 `subscription_plans` 上仍然存在，而且**仍然在跑**：
      // `model.CreateUserSubscriptionFromPlanTx` 在 `upgrade_group != ''` 时会
      // 直接 `UPDATE users SET group = <upgrade_group>`，到期由
      // `ExpireDueSubscriptions` 再改回去。用户分组与模型分组分离之后，这正是
      // 要消灭的东西：买套餐只该多解锁几个**模型分组**，不该把人从一个用户分组
      // 搬到另一个用户分组（那会连带换掉他的可用范围、倍率与自动分组）。
      //
      // 上游 `AdminUpdateSubscriptionPlan` 用的是**显式 map 全量覆盖**，
      // `upgrade_group` / `downgrade_group` 恒在 map 里。所以这里"不传"与
      // "传空串"落库结果完全一样（Go 零值），区别只在读代码的人能不能看出来这是
      // 有意为之。写成显式空串：任何一次在新表单里保存套餐，都会把这两列清掉，
      // 从此该套餐不再改写 `users.group`。
      //
      // **没被重新保存过的存量套餐仍然会改写 users.group** —— 那不是这一行能
      // 解决的，所以列表页保留了一列专门把这批套餐标出来（见
      // subscriptions-columns.tsx 的 legacy_group_rewrite 列）。
      upgrade_group: '',
      downgrade_group: '',
    },
  }
}
