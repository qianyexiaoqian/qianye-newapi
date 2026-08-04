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

import { qyDelete, qyGet, qyPost, qyPut } from '../../lib/api'
import { qyKeys } from '../../lib/query-keys'
import type {
  QyApiAddress,
  QyApiAddressList,
  QyApiAddressUpsert,
} from './types'

export function qyAdminApiAddressesQuery() {
  return queryOptions({
    queryKey: qyKeys.adminApiAddresses(),
    queryFn: () => qyGet<QyApiAddressList>('/admin/api-addresses'),
  })
}

export function qyCreateApiAddress(body: QyApiAddressUpsert) {
  return qyPost<QyApiAddress>('/admin/api-addresses', body)
}

export function qyUpdateApiAddress(id: number, body: QyApiAddressUpsert) {
  return qyPut<QyApiAddress>(`/admin/api-addresses/${id}`, body)
}

export function qyDeleteApiAddress(id: number) {
  return qyDelete<{ id: number }>(`/admin/api-addresses/${id}`)
}

/**
 * 整表重排。
 *
 * 发的是**完整**的 id 顺序而不是「把第 3 条移到第 1 位」：后端据此做一次乐观锁
 * （提交的 id 集合必须与库里当前完全一致），有人在你编辑期间加了或删了一条时
 * 直接 409，而不是把他新加的那条静默挤到末尾。
 */
export function qyReorderApiAddresses(ids: number[]) {
  return qyPost<QyApiAddressList>('/admin/api-addresses/reorder', { ids })
}
