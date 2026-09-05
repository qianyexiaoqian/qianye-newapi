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
import type {
  QyStardustAdjustInput,
  QyStardustAdjustResult,
  QyStardustAdminAccrualRow,
  QyStardustAdminAccrualsParams,
  QyStardustAdminLedgerParams,
  QyStardustAdminLedgerRow,
  QyStardustBalanceRow,
  QyStardustBalancesParams,
  QyStardustLedgerCheck,
  QyStardustSettleRerunResult,
  QyStardustSettleStatus,
} from './types'

/**
 * 星屑账本（管理端）。路径与契约 §3 逐字一致；后端路由落地之前它们在
 * `route-contract.test.ts` 的 `MISS_EXEMPT` 里，落地之后请把那一条豁免删掉。
 */

/**
 * 手调（RootActionGate + 关键操作限流）。
 *
 * 409 `qy_idem_key_conflict` = 同一个弹窗改了金额又提交，账本上执行的是上一次；
 * 成功后 `invalidateQueries({ queryKey: qyKeys.all })`。
 */
export function adjustQyStardust(
  body: QyStardustAdjustInput
): Promise<QyStardustAdjustResult> {
  return qyPost<QyStardustAdjustResult>('/admin/stardust/adjust', body)
}

export function qyAdminStardustBalancesQuery(params: QyStardustBalancesParams) {
  return queryOptions({
    queryKey: qyKeys.adminStardustBalances(params),
    queryFn: () =>
      qyGet<QyPage<QyStardustBalanceRow>>('/admin/stardust/balances', params),
    staleTime: 15_000,
  })
}

export function qyAdminStardustLedgerQuery(
  params: QyStardustAdminLedgerParams
) {
  return queryOptions({
    queryKey: qyKeys.adminStardustLedger(params),
    queryFn: () =>
      qyGet<QyPage<QyStardustAdminLedgerRow>>('/admin/stardust/ledger', params),
    staleTime: 15_000,
  })
}

export function qyAdminStardustAccrualsQuery(
  params: QyStardustAdminAccrualsParams
) {
  return queryOptions({
    queryKey: qyKeys.adminStardustAccruals(params),
    queryFn: () =>
      qyGet<QyPage<QyStardustAdminAccrualRow>>(
        '/admin/stardust/accruals',
        params
      ),
    staleTime: 15_000,
  })
}

/** 账本体检（I0 / I1 / I2 三条恒等式 + 暂缓桶积龄）。只读，不改任何一行。 */
export function qyAdminStardustLedgerCheckQuery() {
  return queryOptions({
    queryKey: qyKeys.adminStardustLedgerCheck(),
    queryFn: () => qyGet<QyStardustLedgerCheck>('/admin/stardust/ledger-check'),
    staleTime: 15_000,
  })
}

/** 结算调度状态：上一跑 / 下次结算时刻 / 目标日。 */
export function qyAdminStardustSettleStatusQuery() {
  return queryOptions({
    queryKey: qyKeys.adminStardustSettleStatus(),
    queryFn: () =>
      qyGet<QyStardustSettleStatus>('/admin/stardust/settle/status'),
    staleTime: 15_000,
  })
}

/** 重跑某一天的结算。`day` 是桶日 `YYYYMMDD`，只对 computed / held 桶生效。 */
export function rerunQyStardustSettle(
  day: string
): Promise<QyStardustSettleRerunResult> {
  return qyPost<QyStardustSettleRerunResult>('/admin/stardust/settle/rerun', {
    day,
  })
}
