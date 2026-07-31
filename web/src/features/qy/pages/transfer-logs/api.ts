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
import type { QyPage } from '../../lib/types'
import type {
  QyAdminTransferRecord,
  QyAdminTransferRecordsParams,
  QyTransferRecord,
  QyTransferRecordsParams,
} from './types'

/**
 * 我的划转流水。
 *
 * 空串参数会被剔除：axios 会把 `direction=''` 原样拼进 query string，
 * 而后端 `c.Query("direction")` 拿到空串走 default 分支 —— 行为虽然一样，
 * 但 queryKey 里多一个恒空的字段会让缓存条目莫名其妙地分裂。
 */
export function qyTransferRecordsQuery(params: QyTransferRecordsParams) {
  const query = pruneEmpty(params)
  return queryOptions({
    queryKey: qyKeys.transferRecords(query),
    queryFn: () => qyGet<QyPage<QyTransferRecord>>('/transfer/records', query),
  })
}

export function qyAdminTransferRecordsQuery(
  params: QyAdminTransferRecordsParams
) {
  const query = pruneEmpty(params)
  return queryOptions({
    queryKey: qyKeys.adminTransferRecords(query),
    queryFn: () =>
      qyGet<QyPage<QyAdminTransferRecord>>('/admin/transfer/records', query),
  })
}

/** 去掉空串与 undefined，保证同一组筛选条件永远映射到同一个 queryKey。 */
function pruneEmpty(params: Record<string, unknown>): Record<string, unknown> {
  const out: Record<string, unknown> = {}
  for (const [key, value] of Object.entries(params)) {
    if (value == null || value === '' || value === 0) continue
    out[key] = value
  }
  // 页码为 1 时上面的 `value === 0` 不会误伤，但 p 必须始终存在，
  // 否则后端会用默认值而前端的翻页条却以为自己在第 3 页。
  out.p = params.p
  out.page_size = params.page_size
  return out
}
