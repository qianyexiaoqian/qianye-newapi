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
    duration_unit: z.enum([
      'year',
      'month',
      'day',
      'hour',
      'custom',
      'permanent',
    ]),
    // 永久档的输入框不显示，值仍然留在表单里（默认 1），所以 min(1) 不会挡住
    // 它；真正落库的值由 formValuesToPlanPayload 按档位归零。
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
    no_quota: z.boolean(),
    // 购买后 / 到期回退的用户分组。空串是**有语义的取值**（不改 / 回退原组），
    // 不是"没填"，所以这里不做 min(1) 之类的必填校验。
    upgrade_group: z.string(),
    downgrade_group: z.string(),
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
  no_quota: false,
  upgrade_group: '',
  downgrade_group: '',
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
    no_quota: plan.no_quota === true,
    upgrade_group: plan.upgrade_group || '',
    downgrade_group: plan.downgrade_group || '',
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
  const pureProduct = values.no_quota === true
  return {
    plan: {
      ...planValues,
      price_amount: Number(values.price_amount || 0),
      currency: 'USD',
      // 永久档没有时长可言，落 0。后端的 `duration_value <= 0` 兜底只对**非**
      // 永久/自定义档补 1，所以 0 会原样留在库里 —— 列表页才不会把一个永久套餐
      // 显示成「1 个月」，而那是一个看起来完全正常的假到期日。
      duration_value:
        values.duration_unit === 'permanent'
          ? 0
          : Number(values.duration_value || 0),
      custom_seconds: Number(values.custom_seconds || 0),
      quota_reset_period: values.quota_reset_period || 'never',
      quota_reset_custom_seconds:
        values.quota_reset_period === 'custom'
          ? Number(values.quota_reset_custom_seconds || 0)
          : 0,
      sort_order: Number(values.sort_order || 0),
      max_purchase_per_user: Number(values.max_purchase_per_user || 0),
      no_quota: pureProduct,
      // 纯商品归零总额度：这一格在界面上已经不显示了，把上一次的旧值继续提交
      // 只会在库里留下一个谁都看不见、却会在关掉纯商品那一刻突然复活的数字。
      // 归零本身不会变成"不限额度"——纯商品由 no_quota 判定，购买时后端按
      // `model.PureProductAmountTotal` 钉成 1 个 quota 单位。
      total_amount: pureProduct
        ? 0
        : parseQuotaFromDollars(Number(values.total_amount || 0)),
      // ── 购买后 / 到期回退的用户分组 ────────────────────────────────────
      //
      // 这两列由 `model.CreateUserSubscriptionFromPlanTx`（购买时
      // `UPDATE users SET group = <upgrade_group>`）与 `ExpireDueSubscriptions`
      // （到期改回去）消费，是「把套餐当用户组商品卖」这条路子的全部实现。
      //
      // 空串**是取值而不是缺省**：upgrade 空 = 购买不动用户分组；downgrade 空 =
      // 到期回退到购买前的原分组（购买那一刻快照进 `prev_user_group`）。上游
      // `AdminUpdateSubscriptionPlan` 是显式 map 全量覆盖，两列恒在 map 里，
      // 所以这里传什么就是什么，包括把已配的分组清回空。
      upgrade_group: (values.upgrade_group || '').trim(),
      downgrade_group: (values.downgrade_group || '').trim(),
    },
  }
}
