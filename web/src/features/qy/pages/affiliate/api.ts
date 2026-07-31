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

import { getAffiliateCode } from '@/features/wallet/api'

import { qyGet } from '../../lib/api'
import { qyKeys } from '../../lib/query-keys'
import type { QyPage } from '../../lib/types'
import type {
  QyCommissionRecord,
  QyCommissionSummary,
  QyInvitee,
} from './types'

export function qyCommissionSummaryQuery() {
  return queryOptions({
    queryKey: qyKeys.commissionSummary(),
    queryFn: () => qyGet<QyCommissionSummary>('/commission/summary'),
  })
}

export function qyInviteesQuery(params: { p: number; page_size: number }) {
  return queryOptions({
    queryKey: qyKeys.commissionInvitees(params),
    queryFn: () => qyGet<QyPage<QyInvitee>>('/commission/invitees', params),
  })
}

export function qyCommissionRecordsQuery(params: {
  p: number
  page_size: number
  source_type?: string
}) {
  const query =
    params.source_type == null || params.source_type === ''
      ? { p: params.p, page_size: params.page_size }
      : params
  return queryOptions({
    queryKey: qyKeys.commissionRecords(query),
    queryFn: () =>
      qyGet<QyPage<QyCommissionRecord>>('/commission/records', query),
  })
}

/**
 * 邀请码。
 *
 * 走**上游**的 `/api/user/aff`，而不是 qy 自己的接口：邀请码是原项目
 * `users.aff_code`，注册绑定逻辑也在上游。qy 只在这条关系之上做返佣账本，
 * 再造一个码只会让"用户拿到的链接"和"后端认的邀请人"分叉。
 *
 * 因此这个 query 的失败**不是** `QyError`，也不该走扩展的隐藏逻辑 ——
 * 拿不到邀请码时页面照常展示佣金数据，只是链接区显示占位。
 */
export function qyAffiliateCodeQuery() {
  return queryOptions({
    queryKey: [...qyKeys.all, 'affiliate-code'] as const,
    queryFn: async () => {
      const res = await getAffiliateCode()
      return res.success === true && typeof res.data === 'string'
        ? res.data
        : ''
    },
    staleTime: 5 * 60 * 1000,
  })
}
