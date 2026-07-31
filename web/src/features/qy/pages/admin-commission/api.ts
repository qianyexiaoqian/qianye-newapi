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

import { qyGet, qyPost, qyPut } from '../../lib/api'
import { qyKeys } from '../../lib/query-keys'
import type { QyPage } from '../../lib/types'
import type { QyAdminAccrual, QyCommissionAdminConfig } from './types'

export function qyAdminCommissionConfigQuery() {
  return queryOptions({
    queryKey: qyKeys.adminCommissionConfig(),
    queryFn: () => qyGet<QyCommissionAdminConfig>('/admin/commission/config'),
  })
}

/**
 * 修改运营参数。
 *
 * 请求体是 `{key: int64}` 的稀疏 map，只传改动过的键 —— 后端逐键写
 * `qy_settings` 并各写一条审计，把没改的键一起发过去会污染"谁在什么时候
 * 把 3% 改成 8%"的追溯轨迹。
 *
 * 刻意忽略响应体：后端返回的 `effective` 用的是 Go 结构体字段名
 * （`TopupRateBps` 而非 `topup_rate_bps`），与 GET 的形状不一致。
 * 调用方成功后重新 GET 一次即可，别去适配那份大小写。
 */
export function qyUpdateCommissionConfig(patch: Record<string, number>) {
  return qyPut<unknown>('/admin/commission/config', patch)
}

export type QyAdminAccrualFilters = {
  p: number
  page_size: number
  inviter_id?: string
  invitee_id?: string
  source_type?: string
  status?: string
  accrual_no?: string
}

export function qyAdminAccrualsQuery(filters: QyAdminAccrualFilters) {
  const query: Record<string, unknown> = {
    p: filters.p,
    page_size: filters.page_size,
  }
  for (const key of [
    'inviter_id',
    'invitee_id',
    'source_type',
    'status',
    'accrual_no',
  ] as const) {
    const value = filters[key]
    if (value != null && value !== '') query[key] = value
  }

  return queryOptions({
    queryKey: qyKeys.adminCommissionRecords(query),
    queryFn: () =>
      qyGet<QyPage<QyAdminAccrual>>('/admin/commission/records', query),
  })
}

/**
 * 人工冲正。
 *
 * `reason` 与 `client_request_id` 都是后端必填：前者是事后复盘的唯一依据，
 * 后者防止一次网络重试把佣金扣两遍。
 */
export function qyClawbackAccrual(input: {
  accrual_id: number
  quota: number
  reason: string
  client_request_id: string
}) {
  return qyPost<{ accrual_no: string; gross_amount: string }>(
    '/admin/commission/clawback',
    input
  )
}

/** 立即结算指定用户，不必等下一个周期。 */
export function qySettleCommission(userId: number) {
  return qyPost<{ settled: boolean; user_id: number }>(
    `/admin/commission/settle?user_id=${userId}`
  )
}

/** 拉黑/解封一条邀请关系。只停止未来计佣，已发放的佣金要另走冲正。 */
export function qyBlockInviteRelation(input: {
  invitee_id: number
  blocked: boolean
  reason: string
}) {
  return qyPost<{ invitee_id: number; blocked: boolean }>(
    '/admin/commission/relations/block',
    input
  )
}
