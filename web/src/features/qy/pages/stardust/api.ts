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

import { qyGet } from '../../lib/api'
import { qyKeys } from '../../lib/query-keys'
import type { QyPage, QyPageParams } from '../../lib/types'
import type {
  QyStardustAccrualRow,
  QyStardustForecast,
  QyStardustLedgerParams,
  QyStardustLedgerRow,
  QyStardustMe,
} from './types'

/**
 * 星屑（用户端）取数。路径与契约 §2 逐字一致，`route-contract.test.ts` 会拿它们
 * 与后端路由清单对账 —— 后端路由落地之前它们在那条测试的 `MISS_EXEMPT` 里，
 * 落地之后请把那一条豁免删掉。
 */

export function qyStardustMeQuery() {
  return queryOptions({
    queryKey: qyKeys.stardustMe(),
    queryFn: () => qyGet<QyStardustMe>('/stardust/me'),
    staleTime: 15_000,
  })
}

export function qyStardustLedgerQuery(params: QyStardustLedgerParams) {
  return queryOptions({
    queryKey: qyKeys.stardustLedger(params),
    queryFn: () =>
      qyGet<QyPage<QyStardustLedgerRow>>('/stardust/ledger', params),
    staleTime: 15_000,
  })
}

export function qyStardustAccrualsQuery(params: QyPageParams) {
  return queryOptions({
    queryKey: qyKeys.stardustAccruals(params),
    queryFn: () =>
      qyGet<QyPage<QyStardustAccrualRow>>('/stardust/accruals', params),
    staleTime: 15_000,
  })
}

/**
 * 明日预计到账。
 *
 * `staleTime` 只有一分钟，而**真正的**一小时节流在服务端：这一条打回来的多半是
 * 后端缓存里那一份，代价是一次 200，换来的是"重新聚焦页面就能看到最新的那一份"。
 * 前端不自己按小时轮询——页面上写着数据截至时刻，看得见比偷偷刷新更有用。
 */
export function qyStardustForecastQuery() {
  return queryOptions({
    queryKey: qyKeys.stardustForecast(),
    queryFn: () => qyGet<QyStardustForecast>('/stardust/forecast'),
    staleTime: 60_000,
  })
}

/**
 * 手动刷新：请后端重算一份。
 *
 * 走 `?refresh=1` 而不是 `refetch()`——后者只会再拿一次缓存里那一份。服务端在
 * `refresh_after` 之前会原样退回旧的那一份（响应里的 `computed_at` 不变），
 * 界面据此把按钮按住，不需要在这里再算一次节流。
 */
export function qyStardustForecastRefresh() {
  return qyGet<QyStardustForecast>('/stardust/forecast', { refresh: 1 })
}
