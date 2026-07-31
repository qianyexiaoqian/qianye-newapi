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
/** 与 `qianye/modules/violation/model.go` 的 Record / Ban / Appeal 对齐。 */

export type QyViolationRecordStatus = 'active' | 'appealed' | 'revoked'

/** 扣费结果。`truncated` / `insufficient` 是余额不足留下的可审计偏差。 */
export type QyViolationFeeStatus =
  | 'charged'
  | 'failed'
  | 'insufficient'
  | 'none'
  | 'refunded'
  | 'shadow'
  | 'skipped_dup_builtin'
  | 'truncated'
  | (string & {})

export type QyViolationRecord = {
  id: number
  rec_no: string
  user_id: number
  username: string
  token_id: number
  token_name: string
  rule_id: number
  rule_name: string
  /** 冻结命中当时的对外文案，规则改名后用户端仍应显示原文。 */
  public_reason: string
  phase: string
  action: string
  /** 影子模式命中：只记不罚，用户并未被扣钱。 */
  shadow: boolean
  blocked: boolean
  model_name: string
  using_group: string
  channel_id: number
  relay_format: string
  /** 与主库 `logs.request_id` 对齐，是与计费日志对账的唯一钥匙。 */
  request_id: string
  ip: string
  matched_terms: string
  match_snippet: string
  fee_mode: string
  fee_base_usd: string
  fee_multiple: string
  group_ratio: string
  fee_quota_want: number
  fee_quota: number
  fee_status: QyViolationFeeStatus
  fee_error: string
  /** 额度饱和的审计标记 JSON，非空几乎必然意味着规则配错。 */
  quota_clamp: string
  count_weight: number
  counted: boolean
  counter_after: number
  status: QyViolationRecordStatus
  revoked_by: number
  revoked_at: number
  revoke_reason: string
  refund_quota: number
  has_payload: boolean
  created_at: number
}

/** `GET /admin/violation/records/:id/evidence` 的响应。 */
export type QyViolationEvidence = {
  record: QyViolationRecord
  has_payload: boolean
  context?: string
  /** 多模态描述符 JSON 数组（哈希 / 大小 / MIME），绝不含二进制。 */
  files?: string
  truncated?: boolean
  redacted?: boolean
  redact_stats?: string
  origin_bytes?: number
  stored_bytes?: number
}

export type QyViolationBanStatus =
  | 'banned'
  | 'failed'
  | 'pending'
  | 'skipped'
  | 'unbanned'
  | (string & {})

export type QyViolationBan = {
  id: number
  user_id: number
  /** 每次解封 +1。不 +1 会让该用户的自动封号从此静默失效。 */
  ban_cycle: number
  trigger_record_id: number
  hit_count_at: number
  threshold: number
  status: QyViolationBanStatus
  attempts: number
  last_error: string
  banned_at: number
  unbanned_at: number
  unbanned_by: number
  unban_note: string
  created_at: number
}

export type QyViolationAppealStatus =
  | 'approved'
  | 'pending'
  | 'rejected'
  | 'withdrawn'
  | (string & {})

export type QyViolationAppeal = {
  id: number
  user_id: number
  record_id: number
  reason: string
  status: QyViolationAppealStatus
  reviewer_id: number
  review_note: string
  reviewed_at: number
  created_at: number
  updated_at: number
}
