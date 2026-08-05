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
import { qyGet, qyPost, qyPut } from '@/features/qy/lib/api'
import { api } from '@/lib/api'

import type {
  ApiResponse,
  DeletePlanRequest,
  DeletePlanResult,
  SetPlanSeatLimitResult,
  PlanRecord,
  PlanHoldersResult,
  PlanPayload,
  PlanUsage,
  PlansUsageResult,
  SubscriptionPlan,
  UserSubscriptionRecord,
  CreateUserSubscriptionRequest,
  ResetUserSubscriptionsRequest,
  ResetPlanSubscriptionsRequest,
  SubscriptionResetResult,
  SubscriptionPayResponse,
  SubscriptionPayRequest,
  SelfSubscriptionData,
} from './types'

// ============================================================================
// Admin Plan Management
// ============================================================================

export async function getAdminPlans(): Promise<ApiResponse<PlanRecord[]>> {
  const res = await api.get('/api/subscription/admin/plans')
  return res.data
}

// 返回的是**扁平的套餐对象**而不是 `{plan:...}`：后端 `AdminCreateSubscriptionPlan`
// 直接 `ApiSuccess(c, req.Plan)`。调用方要靠 `data.id` 才能接着写扩展库里的总名额，
// 所以这里按真实形状标注，不再沿用与响应对不上的 PlanRecord。
export async function createPlan(
  data: PlanPayload
): Promise<ApiResponse<SubscriptionPlan>> {
  const res = await api.post('/api/subscription/admin/plans', data)
  return res.data
}

export async function updatePlan(
  id: number,
  data: PlanPayload
): Promise<ApiResponse<PlanRecord>> {
  const res = await api.put(`/api/subscription/admin/plans/${id}`, data)
  return res.data
}

export async function patchPlanStatus(
  id: number,
  enabled: boolean
): Promise<ApiResponse> {
  const res = await api.patch(`/api/subscription/admin/plans/${id}`, {
    enabled,
  })
  return res.data
}

// ============================================================================
// Admin Plan Seats & Deletion (qy extension)
// ============================================================================

/**
 * 这三个接口挂在 qy 扩展自己的管理端路由下，不是上游 `/api/subscription/admin`：
 * 总名额是扩展库里的新数据，删除套餐则是上游根本没有的能力（上游连未挂路由的
 * 删除函数都没有）。走 `qyGet/qyPut/qyPost` 而不是裸 `api.*`，是为了继承 qy 的
 * 信封解包与失败分类 —— 扩展未启用时抛出的 QyError 带 `isHidden`，调用方据此
 * **静默隐藏入口**，而不是给管理员糊一脸 404。
 *
 * 路径与请求体必须与 `qianye/modules/subscription/subscription.go` 的路由逐字
 * 一致。拼错一个词的表现不是"报错"而是 404 → `kindFromStatus` 把无 code 的 404
 * 一律归类成 `disabled` → 调用方静默隐藏入口：运营看到的是"这个功能没有"，
 * 排查方向直接指反。后端有 `TestAdminRoutesMatchTheContractTheFrontendCalls`
 * 逐字锁着这三条路径。
 */
const QY_ADMIN_PLAN_PATH = '/admin/subscription/plans'

export function getPlanUsage(planId: number): Promise<PlanUsage> {
  return qyGet<PlanUsage>(`${QY_ADMIN_PLAN_PATH}/${planId}/usage`)
}

/**
 * 列表页一次拿全部套餐的占用人数。
 *
 * 路径是 `/admin/subscription/plans-usage` 而不是 `${QY_ADMIN_PLAN_PATH}/usage`：
 * 后者会与 `${QY_ADMIN_PLAN_PATH}/:plan_id/usage` 抢同一个路径段，是 gin 路由树里
 * "静态段与通配段互斥"的经典冲突，表现是**后端启动即 panic**。
 *
 * 逐个套餐调 `getPlanUsage` 就是 N+1 次请求，而且每一次都要重查套餐、重数订阅、
 * 重读一次扩展库；这里是固定 3 次查询。
 */
export function getPlansUsage(): Promise<PlansUsageResult> {
  return qyGet<PlansUsageResult>('/admin/subscription/plans-usage')
}

/**
 * 「当前人数」那个数字的下钻：具体是哪些人。
 *
 * 服务端分页（`p` / `page_size`，与扩展其余列表同一套参数名）。一个热门套餐
 * 可能有几百上千人，一次全量返回既拖慢管理端，也等于把一整份用户名清单塞进
 * 一个响应里。
 *
 * 返回的 `total` 与 {@link getPlansUsage} 那一列的 `used_seats` 是**同一个数**：
 * 后端两侧共用一个 WHERE，并且在同一次请求里只取一次时钟。前端因此可以放心地
 * 把这个 total 当作"点开之前看到的那个数字"来显示。
 */
export function getPlanHolders(
  planId: number,
  page: number,
  pageSize: number
): Promise<PlanHoldersResult> {
  return qyGet<PlanHoldersResult>(`${QY_ADMIN_PLAN_PATH}/${planId}/holders`, {
    p: page,
    page_size: pageSize,
  })
}

export function setPlanSeatLimit(
  planId: number,
  capacity: number
): Promise<SetPlanSeatLimitResult> {
  return qyPut<SetPlanSeatLimitResult>(
    `${QY_ADMIN_PLAN_PATH}/${planId}/seat-limit`,
    { capacity }
  )
}

/**
 * 删除走 POST `/…/delete` 而不是 `DELETE /…`：删除必须带 body（force 与强制时
 * 必填的 reason），而 DELETE 携带请求体在反向代理、CDN 与部分 HTTP 客户端上属于
 * "允许但没人保证"的灰色地带。丢 body 的表现是 reason 变空 → 400，排查起来完全
 * 指错方向。后端注释里写的是同一条理由。
 */
export function deletePlan(
  planId: number,
  data: DeletePlanRequest
): Promise<DeletePlanResult> {
  return qyPost<DeletePlanResult>(
    `${QY_ADMIN_PLAN_PATH}/${planId}/delete`,
    data
  )
}

// ============================================================================
// Admin User Subscription Management
// ============================================================================

export async function getUserSubscriptions(
  userId: number
): Promise<ApiResponse<UserSubscriptionRecord[]>> {
  const res = await api.get(
    `/api/subscription/admin/users/${userId}/subscriptions`
  )
  return res.data
}

export async function createUserSubscription(
  userId: number,
  data: CreateUserSubscriptionRequest
): Promise<ApiResponse<{ message?: string }>> {
  const res = await api.post(
    `/api/subscription/admin/users/${userId}/subscriptions`,
    data
  )
  return res.data
}

export async function invalidateUserSubscription(
  subId: number
): Promise<ApiResponse<{ message?: string }>> {
  const res = await api.post(
    `/api/subscription/admin/user_subscriptions/${subId}/invalidate`
  )
  return res.data
}

export async function deleteUserSubscription(
  subId: number
): Promise<ApiResponse> {
  const res = await api.delete(
    `/api/subscription/admin/user_subscriptions/${subId}`
  )
  return res.data
}

export async function resetUserSubscriptionsByPlan(
  userId: number,
  data: ResetUserSubscriptionsRequest
): Promise<ApiResponse<SubscriptionResetResult>> {
  const res = await api.post(
    `/api/subscription/admin/users/${userId}/subscriptions/reset`,
    data
  )
  return res.data
}

export async function resetPlanSubscriptions(
  planId: number,
  data: ResetPlanSubscriptionsRequest
): Promise<ApiResponse<SubscriptionResetResult>> {
  const res = await api.post(
    `/api/subscription/admin/plans/${planId}/subscriptions/reset`,
    data
  )
  return res.data
}

// ============================================================================
// User-facing Subscription Payment
// ============================================================================

export async function paySubscriptionStripe(
  data: SubscriptionPayRequest
): Promise<SubscriptionPayResponse> {
  const res = await api.post('/api/subscription/stripe/pay', data)
  return res.data
}

export async function paySubscriptionCreem(
  data: SubscriptionPayRequest
): Promise<SubscriptionPayResponse> {
  const res = await api.post('/api/subscription/creem/pay', data)
  return res.data
}

export async function paySubscriptionWaffoPancake(
  data: SubscriptionPayRequest
): Promise<SubscriptionPayResponse> {
  const res = await api.post('/api/subscription/waffo-pancake/pay', data)
  return res.data
}

export async function paySubscriptionBalance(
  data: SubscriptionPayRequest
): Promise<SubscriptionPayResponse> {
  const res = await api.post('/api/subscription/balance/pay', data)
  return res.data
}

// Mints a Pancake OnetimeProduct (see controller for the OnetimeProduct vs
// SubscriptionProduct rationale) using persisted creds + StoreID.
export async function createWaffoPancakeSubscriptionProduct(data: {
  name: string
  amount: string
}): Promise<
  ApiResponse<{ product_id: string; product_name: string; store_id: string }>
> {
  const res = await api.post(
    '/api/option/waffo-pancake/subscription-product',
    data
  )
  return res.data
}

// Returns the OnetimeProducts in the saved Pancake store; empty when the
// gateway isn't fully configured.
export async function listWaffoPancakeSubscriptionProductOptions(): Promise<
  ApiResponse<{
    store_id: string
    products: { id: string; name: string; status: string }[]
  }>
> {
  const res = await api.get(
    '/api/option/waffo-pancake/subscription-product-options'
  )
  return res.data
}

export async function paySubscriptionEpay(
  data: SubscriptionPayRequest & { payment_method: string }
): Promise<SubscriptionPayResponse & { url?: string }> {
  const res = await api.post('/api/subscription/epay/pay', data)
  return {
    ...res.data,
    url: res.data.url || (res as unknown as { url?: string }).url,
  }
}

// ============================================================================
// User Self Subscriptions
// ============================================================================

export async function getSelfSubscriptions(): Promise<
  ApiResponse<UserSubscriptionRecord[]>
> {
  const res = await api.get('/api/subscription/self')
  return res.data
}

export async function getSelfSubscriptionFull(): Promise<
  ApiResponse<SelfSubscriptionData>
> {
  const res = await api.get('/api/subscription/self')
  return res.data
}

export async function getPublicPlans(): Promise<ApiResponse<PlanRecord[]>> {
  const res = await api.get('/api/subscription/plans')
  return res.data
}

export async function updateBillingPreference(
  preference: string
): Promise<ApiResponse<{ billing_preference?: string }>> {
  const res = await api.put('/api/subscription/self/preference', {
    billing_preference: preference,
  })
  return res.data
}

export async function getGroups(): Promise<ApiResponse<string[]>> {
  const res = await api.get('/api/group')
  return res.data
}
