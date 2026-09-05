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
  QyCommissionCredit,
  QyCommissionRecord,
  QyCommissionSummary,
  QyInviteRecord,
  QyInviteSummary,
  QyInvitee,
  QyInviteeDailyPage,
} from './types'

/**
 * 邀请返星屑（用户端）取数。路径与契约「用户端接口」逐字一致；后端 Y1 落地之前
 * 它们在 `route-contract.test.ts` 的 `MISS_EXEMPT` 里，落地之后请把那一条豁免删掉。
 */

export function qyInviteSummaryQuery() {
  return queryOptions({
    queryKey: qyKeys.inviteSummary(),
    queryFn: () => qyGet<QyInviteSummary>('/invite/summary'),
    staleTime: 15_000,
  })
}

export function qyInviteesQuery(params: { p: number; page_size: number }) {
  return queryOptions({
    queryKey: qyKeys.inviteInvitees(params),
    queryFn: () => qyGet<QyPage<QyInvitee>>('/invite/invitees', params),
    staleTime: 15_000,
  })
}

/** 返星屑明细。`kind` 留空 = 五种邀请类 kind 全部。 */
export function qyInviteRecordsQuery(params: {
  p: number
  page_size: number
  kind?: string
}) {
  const query =
    params.kind == null || params.kind === ''
      ? { p: params.p, page_size: params.page_size }
      : params
  return queryOptions({
    queryKey: qyKeys.inviteRecords(query),
    queryFn: () => qyGet<QyPage<QyInviteRecord>>('/invite/records', query),
    staleTime: 15_000,
  })
}

/**
 * 我名下的下线在某一天贡献了多少消费基数、按我的分组档计提了多少星屑。
 *
 * 一天一份而不是区间：日结就是按天跑的，一行 = 一个下线在那一天的那一桶，
 * 与管理端「日结明细」同一口径，用户看到的数与运营看到的数才对得上。
 */
export function qyInviteeDailyQuery(day: string) {
  return queryOptions({
    queryKey: qyKeys.inviteInviteeDaily(day),
    queryFn: () => qyGet<QyInviteeDailyPage>('/invite/invitee-daily', { day }),
    staleTime: 15_000,
  })
}

/**
 * 邀请码。
 *
 * 走**上游**的 `/api/user/aff`，而不是 qy 自己的接口：邀请码是原项目
 * `users.aff_code`，注册绑定逻辑也在上游。qy 只在这条关系之上做返星屑，
 * 再造一个码只会让"用户拿到的链接"和"后端认的邀请人"分叉。
 *
 * 因此这个 query 的失败**不是** `QyError`，也不该走扩展的隐藏逻辑 ——
 * 拿不到邀请码时页面照常展示星屑数据，只是链接区显示占位。
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

// ───────────────────────── 推广佣金（D-16：记星屑）─────────────────────────
// 路径与契约「用户」三条逐字一致。后端 Z1 并行实现，落地之前它们在
// `route-contract.test.ts` 的 MISS_EXEMPT 里；清单重新生成后把那一条豁免删掉。

export function qyCommissionSummaryQuery() {
  return queryOptions({
    queryKey: qyKeys.commissionSummary(),
    queryFn: () => qyGet<QyCommissionSummary>('/commission/summary'),
    staleTime: 15_000,
  })
}

/** 佣金账本逐笔。`source_type` 留空 = 全部来源。 */
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
    staleTime: 15_000,
  })
}

/** 自动入账记录（佣金余额 → 星屑余额的每一笔）。 */
export function qyCommissionCreditsQuery(params: {
  p: number
  page_size: number
}) {
  return queryOptions({
    queryKey: qyKeys.commissionCredits(params),
    queryFn: () =>
      qyGet<QyPage<QyCommissionCredit>>('/commission/credits', params),
    staleTime: 15_000,
  })
}
