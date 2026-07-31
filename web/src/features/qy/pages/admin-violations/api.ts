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
import { qyGet, qyPost } from '../../lib/api'
import type { QyPage } from '../../lib/types'
import type {
  QyViolationAppeal,
  QyViolationBan,
  QyViolationEvidence,
  QyViolationRecord,
} from './types'

export type QyViolationRecordParams = {
  p: number
  page_size: number
  user_id?: number
  model?: string
  status?: string
  phase?: string
  request_id?: string
  rule_id?: number
  start_ts?: number
}

export function listQyViolationRecords(
  params: QyViolationRecordParams
): Promise<QyPage<QyViolationRecord>> {
  return qyGet<QyPage<QyViolationRecord>>('/admin/violation/records', params)
}

/**
 * 读取归档的违规上下文。
 *
 * **这是「查看他人输入原文」的操作，后端会写审计**（`records.view_evidence`，
 * 带 actor / target / rec_no）。前端必须在打开之前明确告知管理员这一点，
 * 否则「我只是随手点开看看」会在事后变成一条无法解释的访问记录。
 */
export function getQyViolationEvidence(
  id: number
): Promise<QyViolationEvidence> {
  return qyGet<QyViolationEvidence>(`/admin/violation/records/${id}/evidence`)
}

/** 撤销记录，可选退还扣费。后端幂等（状态条件 UPDATE + twophase 退款）。 */
export function revokeQyViolationRecord(
  id: number,
  body: { reason: string; refund: boolean }
): Promise<{ refunded_quota: number }> {
  return qyPost<{ refunded_quota: number }>(
    `/admin/violation/records/${id}/revoke`,
    body
  )
}

export function listQyViolationBans(params: {
  p: number
  page_size: number
  status?: string
  user_id?: number
}): Promise<QyPage<QyViolationBan>> {
  return qyGet<QyPage<QyViolationBan>>('/admin/violation/bans', params)
}

/** 解封。`reset_counter` 会同时把滚动窗口计数清零。 */
export function unbanQyViolationUser(
  userId: number,
  body: { note: string; reset_counter: boolean }
): Promise<unknown> {
  return qyPost<unknown>(`/admin/violation/bans/${userId}/unban`, body)
}

export function listQyViolationAppeals(params: {
  p: number
  page_size: number
  status?: string
  user_id?: number
}): Promise<QyPage<QyViolationAppeal>> {
  return qyGet<QyPage<QyViolationAppeal>>('/admin/violation/appeals', params)
}

export function reviewQyViolationAppeal(
  id: number,
  body: {
    decision: 'approved' | 'rejected'
    note: string
    refund: boolean
    unban: boolean
    reset_counter: boolean
  }
): Promise<{ refunded_quota: number; unbanned: boolean }> {
  return qyPost<{ refunded_quota: number; unbanned: boolean }>(
    `/admin/violation/appeals/${id}/review`,
    body
  )
}
