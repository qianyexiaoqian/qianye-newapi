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
import { queryOptions } from '@tanstack/react-query'

import { getAdminPlans } from '@/features/subscriptions/api'

import { qyDelete, qyGet, qyPut } from '../../lib/api'
import { qyKeys } from '../../lib/query-keys'
import type {
  QyStardustAdminConfig,
  QyStardustConfigPatch,
  QyStardustConfigSaveResult,
  QyStardustGroupRate,
  QyStardustGroupRateDeleteResult,
  QyStardustGroupRateInput,
  QyStardustGroupRatesView,
  QyStardustPlanOption,
  QyStardustPlanReward,
  QyStardustPlanRewardInput,
} from './types'

/**
 * 星屑配置（管理端）。路径与契约 §3 逐字一致；后端路由落地之前它们在
 * `route-contract.test.ts` 的 `MISS_EXEMPT` 里，落地之后请把那一条豁免删掉。
 *
 * 保存成功后 `invalidateQueries({ queryKey: qyKeys.all })`：货币名一改，
 * 引导端点（`/config`）下发的 `stardust.name` 也变了，所有星屑金额的单位都要跟着换。
 */

export function qyAdminStardustConfigQuery() {
  return queryOptions({
    queryKey: qyKeys.adminStardustConfig(),
    queryFn: () => qyGet<QyStardustAdminConfig>('/admin/stardust/config'),
    staleTime: 15_000,
  })
}

/** 稀疏 PUT：只传改动键。后端回保存之后的生效值。 */
export function updateQyStardustConfig(
  patch: QyStardustConfigPatch
): Promise<QyStardustConfigSaveResult> {
  return qyPut<QyStardustConfigSaveResult>('/admin/stardust/config', patch)
}

export function qyAdminStardustGroupRatesQuery() {
  return queryOptions({
    queryKey: qyKeys.adminStardustGroupRates(),
    queryFn: () =>
      qyGet<QyStardustGroupRatesView>('/admin/stardust/group-rates'),
    staleTime: 15_000,
  })
}

/** 分组名必须在册，否则 400 `qy_sd_group_unknown`。回写入后的整行。 */
export function putQyStardustGroupRate(
  group: string,
  body: QyStardustGroupRateInput
): Promise<QyStardustGroupRate> {
  return qyPut<QyStardustGroupRate>(
    `/admin/stardust/group-rates/${encodeURIComponent(group)}`,
    body
  )
}

export function deleteQyStardustGroupRate(
  group: string
): Promise<QyStardustGroupRateDeleteResult> {
  return qyDelete<QyStardustGroupRateDeleteResult>(
    `/admin/stardust/group-rates/${encodeURIComponent(group)}`
  )
}

export function qyAdminStardustPlanRewardQuery(planId: number) {
  return queryOptions({
    queryKey: qyKeys.adminStardustPlanReward(planId),
    queryFn: () =>
      qyGet<QyStardustPlanReward>(`/admin/stardust/plan-rewards/${planId}`),
    staleTime: 15_000,
    enabled: planId > 0,
  })
}

/** `sources ⊆ {order, balance, admin, redemption}`，否则 400 `qy_sd_bad_source`。 */
export function putQyStardustPlanReward(
  planId: number,
  body: QyStardustPlanRewardInput
): Promise<QyStardustPlanReward> {
  return qyPut<QyStardustPlanReward>(
    `/admin/stardust/plan-rewards/${planId}`,
    body
  )
}

/** 删掉之后该套餐回到默认口径；响应就是那份默认视图（`exists=false`）。 */
export function deleteQyStardustPlanReward(
  planId: number
): Promise<QyStardustPlanReward> {
  return qyDelete<QyStardustPlanReward>(
    `/admin/stardust/plan-rewards/${planId}`
  )
}

/**
 * 套餐清单，给「套餐返还」编辑器的下拉用。
 *
 * 走上游的 `getAdminPlans`（`/api/subscription/admin/plans`）而不是新开一条 qy
 * 接口：套餐住在主库、由上游管理，这里只需要「id → 标题」。它不是 qy 路径，
 * 所以不进 `route-contract.test.ts` 的对账。
 *
 * `retry: false`：这只是输入辅助 —— 拉不到时运营仍然可以手填 plan_id，
 * 反复重试只会让下拉一直转圈。
 */
export function qyAdminStardustPlanListQuery() {
  return queryOptions({
    queryKey: qyKeys.adminStardustPlanList(),
    queryFn: async (): Promise<QyStardustPlanOption[]> => {
      const res = await getAdminPlans()
      if (!res.success) throw new Error(res.message ?? 'plans unavailable')
      return (res.data ?? []).map((record) => ({
        id: record.plan.id,
        title: record.plan.title,
        enabled: record.plan.enabled,
      }))
    },
    staleTime: 5 * 60_000,
    retry: false,
  })
}
