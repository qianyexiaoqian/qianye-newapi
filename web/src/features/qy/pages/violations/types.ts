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
/**
 * 用户端违规视图。
 *
 * 字段集由后端**白名单**构造（`userRecordView`），刻意不含命中词、命中片段、
 * 内部规则名、rule_id、IP、渠道等信息 —— 那些等于把规则库送给刷子。
 * 前端不要试图从别的接口把这些补回来。
 */

export type QyMyViolationRecord = {
  id: number
  created_at: number
  model_name: string
  /** 规则的**对外文案**，不是内部规则名。 */
  reason: string
  blocked: boolean
  fee_quota: number
  fee_status: string
  status: 'active' | 'appealed' | 'revoked' | (string & {})
  counter_after: number
}

/**
 * 「当前窗口违规几次、还差几次会被封号」。
 *
 * 威慑价值大于泄露价值：知道「再违规 2 次就封号」的用户会主动收敛，
 * 不知道的只会在被封之后来发工单。
 */
export type QyMyViolationSummary = {
  hit_count: number
  window_hours: number
  ban_threshold: number
  remaining: number
  total_fee_quota: number
}
