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

import { qyGet, qyPost } from '../../lib/api'
import { qyKeys } from '../../lib/query-keys'
import type { QyPage } from '../../lib/types'
import type { QyLotActivityBrief } from '../lottery/types'
import type {
  QyWheelListParams,
  QyWheelMySpin,
  QyWheelSpinInput,
  QyWheelSpinPageParams,
  QyWheelSpinResult,
} from './types'

/**
 * 星屑转盘取数。
 *
 * 列表走大厅同一条路由 `GET /lottery/activities`，但**不带 `lane`**、带
 * `draw_mode=wheel`：后端对 lane 缺省时按 draw_mode 过滤，而大厅三个 lane 都
 * 不含转盘；两个参数一起给是 400（`qy_lot_bad_draw_mode`）。
 */

/** 转盘页一页的条数。卡片上多一列"各档剩余"，比大厅卡片更高。 */
export const QY_WHEEL_PAGE_SIZE = 12

/** 「我的转动」一页的条数。 */
export const QY_WHEEL_SPINS_PAGE_SIZE = 10

export function qyWheelActivitiesQuery(params: QyWheelListParams) {
  const query = { ...params, draw_mode: 'wheel' }
  return queryOptions({
    queryKey: qyKeys.wheelActivities(query),
    queryFn: () =>
      qyGet<QyPage<QyLotActivityBrief>>('/lottery/activities', query),
    staleTime: 15_000,
  })
}

/**
 * 转一次。
 *
 * 成功后调用方 `invalidateQueries({ queryKey: qyKeys.all })`：余额、活动详情上的
 * 各档库存、我的转动都变了。同一个 `client_request_id` 原样重放拿回的是原来
 * 那一转（`replayed: true`），不会再摇、不会再扣。
 */
export function spinQyWheel(
  actNo: string,
  body: QyWheelSpinInput
): Promise<QyWheelSpinResult> {
  return qyPost<QyWheelSpinResult>(
    `/lottery/activities/${encodeURIComponent(actNo)}/spins`,
    body
  )
}

export function qyWheelMySpinsQuery(
  actNo: string,
  params: QyWheelSpinPageParams
) {
  return queryOptions({
    queryKey: qyKeys.wheelMySpins(actNo, params),
    queryFn: () =>
      qyGet<QyPage<QyWheelMySpin>>(
        `/lottery/activities/${encodeURIComponent(actNo)}/spins/me`,
        params
      ),
    staleTime: 10_000,
    enabled: actNo !== '',
  })
}
