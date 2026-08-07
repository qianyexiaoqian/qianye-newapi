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

import { qyGet, qyPut } from '../lib/api'
import { qyKeys } from '../lib/query-keys'
import type { QyPlanEntitlement, QyPlanEntitlementRequest } from './types'

/**
 * 套餐解锁与余额范围的取数（契约 D）。
 *
 * `staleTime: 0`：这一段配置直接决定「一笔请求从哪个池子里扣钱」。缓存住一份
 * 旧的解锁清单，运营会基于过期状态勾选，保存时把别人刚加的一项覆盖掉。
 */
export function qyPlanEntitlementQuery(planId: number, enabled: boolean) {
  return queryOptions({
    queryKey: qyKeys.adminPlanEntitlement(planId),
    queryFn: () => qyGetPlanEntitlement(planId),
    staleTime: 0,
    enabled,
  })
}

/**
 * 裸取数版本。
 *
 * 套餐编辑抽屉不能用上面那个 `queryOptions`：它的取数时机与表单的 open/reset
 * 生命周期绑在一起（与总名额那一格同一个形状），而 react-query 的缓存会让
 * "上一个套餐的解锁清单"在新套餐的抽屉里先渲染一帧 —— 那一帧里勾选框是**别的
 * 套餐**的状态，管理员在它上面点一下再保存，就把 A 的清单按到了 B 头上。
 */
export function qyGetPlanEntitlement(planId: number) {
  return qyGet<QyPlanEntitlement>(
    `/admin/subscription/plans/${planId}/entitlement`
  )
}

/** 写入解锁清单 + 余额使用范围。单事务，写审计。 */
export function qySavePlanEntitlement(
  planId: number,
  body: QyPlanEntitlementRequest
) {
  return qyPut<QyPlanEntitlement>(
    `/admin/subscription/plans/${planId}/entitlement`,
    body
  )
}
