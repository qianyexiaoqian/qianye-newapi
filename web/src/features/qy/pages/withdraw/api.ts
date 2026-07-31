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

import { qyDelete, qyGet, qyPost } from '../../lib/api'
import { qyKeys } from '../../lib/query-keys'
import type { QyPage } from '../../lib/types'
import type {
  QyPayeeAccount,
  QyWithdrawConfig,
  QyWithdrawCreateRequest,
  QyWithdrawal,
} from './types'

/**
 * 提现门槛 + 当前用户可提额度。
 *
 * `staleTime: 0`：`withdrawable_quota` / `used_today` 会随用户自己的提交变化，
 * 缓存住会让"刚提完还显示能提"。
 */
export function qyWithdrawConfigQuery() {
  return queryOptions({
    queryKey: qyKeys.withdrawConfig(),
    queryFn: () => qyGet<QyWithdrawConfig>('/withdraw/config'),
    staleTime: 0,
  })
}

export function qyWithdrawPayeesQuery() {
  return queryOptions({
    queryKey: qyKeys.withdrawPayees(),
    queryFn: async () => {
      const page = await qyGet<{ items: QyPayeeAccount[] }>('/withdraw/payees')
      return page.items ?? []
    },
  })
}

export function qyWithdrawRecordsQuery(params: {
  p: number
  page_size: number
  status?: string
  method?: string
}) {
  const query: Record<string, unknown> = {
    p: params.p,
    page_size: params.page_size,
  }
  // 空串不进 query：axios 会把 `status=` 原样拼进 URL，而 queryKey 里多一个
  // 恒空的字段会让同一组筛选条件分裂成两个缓存条目。
  if (params.status != null && params.status !== '') {
    query.status = params.status
  }
  if (params.method != null && params.method !== '') {
    query.method = params.method
  }

  return queryOptions({
    queryKey: qyKeys.withdrawRecords(query),
    queryFn: () => qyGet<QyPage<QyWithdrawal>>('/withdraw/records', query),
  })
}

/** 单据详情。**只有这个接口会带 `events`**，时间线必须走它而不是列表行。 */
export function qyWithdrawRecordQuery(id: number) {
  return queryOptions({
    queryKey: qyKeys.withdrawRecord(id),
    queryFn: () => qyGet<QyWithdrawal>(`/withdraw/${id}`),
  })
}

export function qyCreateWithdrawal(body: QyWithdrawCreateRequest) {
  return qyPost<QyWithdrawal>('/withdraw', body)
}

export function qyCancelWithdrawal(id: number) {
  return qyPost<QyWithdrawal>(`/withdraw/${id}/cancel`)
}

export function qyCreatePayee(body: {
  channel: string
  label: string
  payee: Record<string, string>
}) {
  return qyPost<QyPayeeAccount>('/withdraw/payees', body)
}

export function qyDeletePayee(ref: string) {
  return qyDelete<{ ref: string }>(`/withdraw/payees/${ref}`)
}
