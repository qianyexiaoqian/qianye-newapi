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
import { qyDelete, qyGet, qyPost, qyPut } from '../../lib/api'
import type { QyPage } from '../../lib/types'
import type {
  QyViolationAppeal,
  QyViolationBan,
  QyViolationBanPolicy,
  QyViolationBanPolicyImpact,
  QyViolationBanPolicyInput,
  QyViolationBanPolicyList,
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

/* ─────────────────────── 按用户分组的处置策略档 ─────────────────────── */

export function listQyViolationBanPolicies(): Promise<QyViolationBanPolicyList> {
  return qyGet<QyViolationBanPolicyList>('/admin/violation/ban-policies')
}

/**
 * 「改成这套阈值，立刻会处置几个人」。
 *
 * 独立成一个只读接口，是因为管理员需要**在填表的过程中**看到这个数。
 * 一个只在保存时才出现的数字，会让人先按一次保存去探路 —— 而那正是
 * 这个功能要防的动作。
 *
 * `user_group` 留空 = 兜底档，此时后端按**全部分组**算（响应里的
 * `scope_all_groups` 会标出来）：兜底档要匹配的是「没有专属档的全部分组」，
 * 那不是一个能写进 WHERE 的集合。
 */
export function previewQyViolationBanPolicyImpact(params: {
  user_group: string
  threshold: number
  window_hours: number
  action: string
}): Promise<{ impact: QyViolationBanPolicyImpact; scope_all_groups: boolean }> {
  return qyGet<{
    impact: QyViolationBanPolicyImpact
    scope_all_groups: boolean
  }>('/admin/violation/ban-policies/impact', params)
}

/**
 * 新建 / 编辑一档分组策略。
 *
 * 会扩大处置面的写入（阈值变小 / 窗口变长 / 动作变重 / 新建一档）必须带
 * `confirm: true`，否则后端回 409 `confirm_required`。前端的正常路径是
 * 先拉一次影响面预览、在确认弹窗里把数字摆给管理员看，再带着这一位提交；
 * 409 是给绕过界面直接调接口的那条路兜底的。
 */
export function upsertQyViolationBanPolicy(
  body: QyViolationBanPolicyInput
): Promise<{
  policy: QyViolationBanPolicy
  impact: QyViolationBanPolicyImpact
}> {
  return qyPut<{
    policy: QyViolationBanPolicy
    impact: QyViolationBanPolicyImpact
  }>('/admin/violation/ban-policies', body)
}

/**
 * 编辑兜底档。
 *
 * 与普通档是两条路由而不是一个带 `is_default` 参数的接口：**路径决定身份**。
 * 请求体里的 `user_group` 与 `enabled` 会被后端忽略 —— 兜底档恒为空分组、
 * 恒为启用，因为它没有可回落的下一级。
 */
export function upsertQyViolationDefaultBanPolicy(
  body: Omit<QyViolationBanPolicyInput, 'enabled' | 'user_group'>
): Promise<{
  policy: QyViolationBanPolicy
  impact: QyViolationBanPolicyImpact
}> {
  return qyPut<{
    policy: QyViolationBanPolicy
    impact: QyViolationBanPolicyImpact
  }>('/admin/violation/ban-policies/default', body)
}

/** 删除一档分组策略。兜底档后端一律拒绝（400）。 */
export function deleteQyViolationBanPolicy(
  id: number
): Promise<{ deleted: boolean }> {
  return qyDelete<{ deleted: boolean }>(`/admin/violation/ban-policies/${id}`)
}
