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
  QyAdminWithdrawal,
  QyPayeePlain,
  QyWithdrawStats,
} from '../withdraw/types'

export type QyAdminWithdrawFilters = {
  p: number
  page_size: number
  status?: string
  method?: string
  user_id?: string
  withdraw_no?: string
  /** `hold` 只看对账异常。 */
  reconcile?: string
  /** `true` 只看命中风控的单。 */
  risk_only?: string
}

/**
 * 审核队列。
 *
 * 后端默认按 `id asc`（先进先出），刻意不改成倒序：审核队列要让最老的单
 * 浮在最上面，否则超时的申请会被新单一直往下压。
 */
export function qyAdminWithdrawalsQuery(filters: QyAdminWithdrawFilters) {
  const query: Record<string, unknown> = {
    p: filters.p,
    page_size: filters.page_size,
  }
  for (const key of [
    'status',
    'method',
    'user_id',
    'withdraw_no',
    'reconcile',
    'risk_only',
  ] as const) {
    const value = filters[key]
    if (value != null && value !== '') query[key] = value
  }

  return queryOptions({
    queryKey: qyKeys.adminWithdrawals(query),
    queryFn: () => qyGet<QyPage<QyAdminWithdrawal>>('/admin/withdraw', query),
  })
}

export function qyAdminWithdrawStatsQuery() {
  return queryOptions({
    queryKey: qyKeys.adminWithdrawStats(),
    queryFn: () => qyGet<QyWithdrawStats>('/admin/withdraw/stats'),
    staleTime: 0,
  })
}

/** 单据详情。只有这个接口带 `events`（且含管理端专属的 `detail`）。 */
export function qyAdminWithdrawalQuery(id: number) {
  return queryOptions({
    queryKey: qyKeys.adminWithdrawal(id),
    queryFn: () => qyGet<QyAdminWithdrawal>(`/admin/withdraw/${id}`),
  })
}

export function qyApproveWithdrawal(id: number) {
  return qyPost<QyAdminWithdrawal>(`/admin/withdraw/${id}/approve`)
}

/** 拒绝。理由是后端必填项（`checkRunes` 后为空即 400）。 */
export function qyRejectWithdrawal(input: { id: number; reason: string }) {
  return qyPost<QyAdminWithdrawal>(`/admin/withdraw/${input.id}/reject`, {
    reason: input.reason,
  })
}

export function qyMarkWithdrawalPaid(input: {
  id: number
  payout_ref: string
  paid_at: number
  payout_note: string
}) {
  return qyPost<QyAdminWithdrawal>(`/admin/withdraw/${input.id}/mark-paid`, {
    payout_ref: input.payout_ref,
    paid_at: input.paid_at,
    payout_note: input.payout_note,
  })
}

export function qyFailWithdrawal(input: { id: number; reason: string }) {
  return qyPost<QyAdminWithdrawal>(`/admin/withdraw/${input.id}/fail`, {
    reason: input.reason,
  })
}

/** 对账异常单的人工裁决。`decision` 只接受 `paid` / `failed`。 */
export function qyResolveWithdrawal(input: {
  id: number
  decision: 'failed' | 'paid'
  evidence: string
}) {
  return qyPost<QyAdminWithdrawal>(`/admin/withdraw/${input.id}/resolve`, {
    decision: input.decision,
    evidence: input.evidence,
  })
}

/**
 * 查看收款信息明文。
 *
 * **这是全系统唯一能拿到明文的出口**，每次调用都会写一条 `qy_pii_audits`
 * 与一条全局审计。因此：
 *   - 必须是显式的一次性动作（mutation），绝不能做成 query 让 react-query
 *     在窗口聚焦/重连时自动重放 —— 那会凭空刷出一串"管理员查看了收款信息"；
 *   - `reason` 后端要求 ≥ 4 个字符，前端也要拦一次，别让人白点一次审计。
 */
export function qyRevealPayee(input: { id: number; reason: string }) {
  return qyGet<QyPayeePlain>(`/admin/withdraw/${input.id}/payee`, {
    reason: input.reason,
  })
}
