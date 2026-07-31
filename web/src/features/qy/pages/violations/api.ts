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
import type { QyMyViolationRecord, QyMyViolationSummary } from './types'

/** 只返回真实命中（后端过滤掉 shadow 记录），影子模式下用户并未被扣钱。 */
export function listQyMyViolations(params: {
  p: number
  page_size: number
}): Promise<QyPage<QyMyViolationRecord>> {
  return qyGet<QyPage<QyMyViolationRecord>>('/violation/my-records', params)
}

export function getQyMyViolationSummary(): Promise<QyMyViolationSummary> {
  return qyGet<QyMyViolationSummary>('/violation/my-summary')
}

/** 提交申诉。后端要求理由至少 20 个字，且一条记录只允许申诉一次。 */
export function createQyViolationAppeal(body: {
  record_id: number
  reason: string
}): Promise<{ id: number }> {
  return qyPost<{ id: number }>('/violation/appeals', body)
}

/** 与后端 `minAppealReasonRunes` 保持一致。 */
export const QY_APPEAL_MIN_RUNES = 20
