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
