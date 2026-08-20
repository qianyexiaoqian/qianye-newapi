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

/**
 * 标记已发放。**系统不动钱** —— 这一步只登记"管理员已经把钱发出去了",
 * 并把佣金从冻结转成已提现。
 *
 * 两个必填项都是资金安全边界,不是表单装饰:
 *   - `payout_ref` 发放凭证。`paid` 是不可逆终态,没有凭证的发放记录在争议时
 *     等于没发;
 *   - `confirm_quota`(quota 单)/ `confirm_amount`(fiat 单)是管理员对
 *     "我实际发了多少"的复述,后端要求与单据金额**逐值相等**。它让"发多少"
 *     成为这个终态动作的显式入参,而不是点一下按钮的隐式后果 ——
 *     一个对着待发放队列无差别 POST 的脚本过不了这一关。
 *
 * 两个确认字段按 `method` 二选一填,填错那个等于没填(后端返回
 * `qy_wd_payout_amount_required`)。
 */
export function qyMarkWithdrawalPaid(input: {
  id: number
  payout_ref: string
  confirm_quota?: number
  confirm_amount?: string
  paid_at: number
  payout_note: string
}) {
  return qyPost<QyAdminWithdrawal>(`/admin/withdraw/${input.id}/mark-paid`, {
    payout_ref: input.payout_ref,
    confirm_quota: input.confirm_quota ?? 0,
    confirm_amount: input.confirm_amount ?? '',
    paid_at: input.paid_at,
    payout_note: input.payout_note,
  })
}

export function qyFailWithdrawal(input: { id: number; reason: string }) {
  return qyPost<QyAdminWithdrawal>(`/admin/withdraw/${input.id}/fail`, {
    reason: input.reason,
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
 *
 * `proofToken` 是 `withdraw.payee.read` 范围的安全证明，由 2FA / Passkey 现场
 * 签发。后端 `handleAdminRevealPayee` 把它作为第一道闸门：**没有它一律 403**，
 * 所以这个参数不是可选的增强，缺了就拿不到任何东西。之所以不做成拦截器里的
 * 全局注入，是因为证明按 scope 签发、只对这一个接口有效。
 */
export function qyRevealPayee(input: {
  id: number
  reason: string
  proofToken: string
}) {
  return qyGet<QyPayeePlain>(
    `/admin/withdraw/${input.id}/payee`,
    { reason: input.reason },
    { headers: { 'X-Security-Proof': input.proofToken } }
  )
}
