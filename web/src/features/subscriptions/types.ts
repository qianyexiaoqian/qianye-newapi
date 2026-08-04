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
import { z } from 'zod'

// ============================================================================
// Subscription Plan Schema & Types
// ============================================================================

export const subscriptionPlanSchema = z.object({
  id: z.number(),
  title: z.string(),
  subtitle: z.string().optional(),
  price_amount: z.number(),
  currency: z.string().default('USD'),
  duration_unit: z.enum(['year', 'month', 'day', 'hour', 'custom']),
  duration_value: z.number(),
  custom_seconds: z.number().optional(),
  quota_reset_period: z.enum(['never', 'daily', 'weekly', 'monthly', 'custom']),
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

export type SubscriptionPlan = z.infer<typeof subscriptionPlanSchema>

export interface PlanRecord {
  plan: SubscriptionPlan
}

// ============================================================================
// User Subscription Schema & Types
// ============================================================================

export const userSubscriptionSchema = z.object({
  id: z.number(),
  user_id: z.number(),
  plan_id: z.number(),
  status: z.string(),
  source: z.string().optional(),
  start_time: z.number(),
  end_time: z.number(),
  amount_total: z.number(),
  amount_used: z.number(),
  next_reset_time: z.number().optional(),
})

export type UserSubscription = z.infer<typeof userSubscriptionSchema>

export interface UserSubscriptionRecord {
  subscription: UserSubscription
}

// ============================================================================
// API Request/Response Types
// ============================================================================

export interface ApiResponse<T = unknown> {
  success: boolean
  message?: string
  data?: T
}

export interface PlanPayload {
  plan: Partial<SubscriptionPlan>
}

// ============================================================================
// Plan Seats & Deletion (qy extension)
// ============================================================================

/**
 * 套餐的名额占用与删除影响面（`GET /api/qy/admin/subscription/plans/{id}/usage`）。
 *
 * 字段名与后端 `qianye/modules/subscription/api_admin.go` 的 `planUsage` 逐字
 * 对应。这里对不上不会有任何编译错误 —— `qyGet<PlanUsage>` 是纯类型断言，
 * 没有运行时校验 —— 只会在界面上渲染成 undefined，而 `undefined > 0` 恒为
 * false，于是一个有 500 个活跃订阅的套餐会被显示成"可以安全删除"。
 *
 * 这些字段**不在**上游 `subscription_plans` 表上（扩展不给上游表加列），由 qy
 * 扩展库单独存放并现算，所以走 `/api/qy/...` 而不是 `/api/subscription/admin/...`。
 *
 * `used_seats` 与 `active_subscriptions` 是两个数，不要互相替代：前者按 user_id
 * 去重（名额是按人算的），后者是订阅行数。同一个人持有同套餐的两条 active 订阅时
 * 两者会不相等，而删除影响面要报的是"多少条订阅会被取消"。
 */
export interface PlanUsage {
  plan_id: number
  /** 全站总名额上限；0 = 不限。 */
  capacity: number
  /** 当前持有该套餐 active 订阅的去重用户数。 */
  used_seats: number
  /** 该套餐下 status='active' 的订阅行数。 */
  active_subscriptions: number
  /** 该套餐下仍未回调完成的订单数。 */
  pending_orders: number
  /** 上游的"每人限购次数"，一并回来只为在同一处对照，避免运营混淆两个上限。 */
  max_purchase_per_user: number
}

/**
 * 列表页的批量占用（`GET /api/qy/admin/subscription/plans-usage`）。
 *
 * `used_seats` 与 {@link PlanUsage.used_seats} 是**同一个数、同一条 SQL**（后端
 * `qianye/modules/subscription/holders.go` 的 `activeHolders`，闸门也从那里取数）：
 * 已激活且尚未到期的去重用户数。列表页与编辑弹窗显示的数字不许出现分歧 ——
 * "页面说还剩 1 个、点下去却被拒"这类问题全部来自两侧各算各的。
 *
 * 后端对**每一个**套餐都回一行，包括 0 人的那些。少一行在这里是 `undefined`，
 * 而 `undefined` 会被渲染成空白，与"这个套餐 0 人"长得完全不同却看不出区别。
 */
export interface PlanSeatUsage {
  plan_id: number
  /** 当前已激活且尚未到期的去重用户数。 */
  used_seats: number
  /** 全站总名额上限；0 = 不限。超卖时 used_seats 会大于它，这是可见且自愈的。 */
  capacity: number
}

export interface PlansUsageResult {
  plans: PlanSeatUsage[]
}

export interface SetPlanSeatLimitResult {
  plan_id: number
  capacity: number
  /** 值与库里相同时后端会短路：不写库、不写审计。 */
  changed: boolean
}

export interface DeletePlanRequest {
  /**
   * `false`：存在活跃订阅或待处理订单时后端必须拒绝并回报占用数量。
   * `true`：级联把活跃订阅置为 cancelled、待处理订单置为失败，再删套餐。
   */
  force: boolean
  /** 强制删除时必填（后端空则 400）；默认路径可留空。 */
  reason: string
}

/**
 * 删除结果。两个级联数字取的是**实际影响行数**而不是删除前测得的影响面 ——
 * 竞态下两者会不一致，而管理员要拿来判断"要人工退多少钱"的是前者。
 */
export interface DeletePlanResult {
  plan_id: number
  deleted: boolean
  forced: boolean
  cancelled_subscriptions: number
  failed_orders: number
  /** 套餐在本次请求之前就已经不存在（重试删除的幂等路径）。 */
  already_gone: boolean
}

export interface SubscriptionPayRequest {
  plan_id: number
  payment_method?: string
}

export interface SubscriptionPayResponse {
  success: boolean
  message?: string
  data?: {
    // Stripe-style hosted checkout link.
    pay_link?: string
    // Waffo Pancake / Creem hosted checkout URL.
    checkout_url?: string
    // Pancake-only: order metadata + self-service buyer session token,
    // surfaced for future flows (refund / cancel from new-api's own UI).
    session_id?: string
    expires_at?: number | string
    order_id?: string
    token?: string
    token_expires_at?: number | string
  }
  url?: string
}

export interface CreateUserSubscriptionRequest {
  plan_id: number
}

export interface ResetUserSubscriptionsRequest {
  plan_id: number
  advance_reset_time: boolean
}

export interface ResetPlanSubscriptionsRequest {
  advance_reset_time: boolean
}

export interface SubscriptionResetResult {
  plan_id: number
  matched_count: number
  reset_count: number
  user_count: number
  advance_reset_time: boolean
}

// ============================================================================
// Self Subscription Data (user-facing)
// ============================================================================

export interface SelfSubscriptionData {
  billing_preference: string
  subscriptions: UserSubscriptionRecord[]
  all_subscriptions: UserSubscriptionRecord[]
}

// ============================================================================
// Dialog Types
// ============================================================================

export type SubscriptionsDialogType =
  | 'create'
  | 'delete'
  | 'update'
  | 'toggle-status'
  | 'reset-subscriptions'
