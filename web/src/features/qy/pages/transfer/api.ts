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
import type {
  QyTransferCreateRequest,
  QyTransferCreated,
  QyTransferLimits,
  QyTransferPreview,
  QyTransferPreviewRequest,
} from './types'

/**
 * 划转限额。
 *
 * `staleTime: 0` 是刻意的：`remaining_daily_quota` / `cooldown_until` 会随
 * 用户自己的操作变化，缓存住会让"刚转完还显示能转 3 次"。
 */
export function qyTransferLimitsQuery() {
  return queryOptions({
    queryKey: qyKeys.transferLimits(),
    queryFn: () => qyGet<QyTransferLimits>('/transfer/limits'),
    staleTime: 0,
  })
}

/**
 * 收款人预校验。
 *
 * 做成 mutation 而不是 query：它在后端会写一条 `qy_transfer_lookup_logs`
 * 反枚举审计，react-query 的自动重取（窗口聚焦、重连）会凭空刷出一堆解析记录。
 */
export function qyPreviewTransfer(body: QyTransferPreviewRequest) {
  return qyPost<QyTransferPreview>('/transfer/preview', body)
}

export function qyCreateTransfer(body: QyTransferCreateRequest) {
  return qyPost<QyTransferCreated>('/transfer', body)
}
