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
  QyViolationBreaker,
  QyViolationRule,
  QyViolationRuleInput,
  QyViolationRuleTestResult,
  QyViolationStats,
} from './types'

export type QyViolationRuleListParams = {
  p: number
  page_size: number
  phase?: string
  keyword?: string
}

export function listQyViolationRules(
  params: QyViolationRuleListParams
): Promise<QyPage<QyViolationRule>> {
  return qyGet<QyPage<QyViolationRule>>('/admin/violation/rules', params)
}

export function createQyViolationRule(
  body: QyViolationRuleInput
): Promise<{ id: number }> {
  return qyPost<{ id: number }>('/admin/violation/rules', body)
}

export function updateQyViolationRule(
  id: number,
  body: QyViolationRuleInput
): Promise<unknown> {
  return qyPut<unknown>(`/admin/violation/rules/${id}`, body)
}

/** 软删：历史记录的 `rule_id` 指向规则，硬删会让申诉复核失去上下文。 */
export function deleteQyViolationRule(id: number): Promise<unknown> {
  return qyDelete<unknown>(`/admin/violation/rules/${id}`)
}

/**
 * 规则试跑。
 *
 * 这是本模块最重要的一个接口：没有它，管理员只能「改完上线看线上炸不炸」，
 * 而线上一炸就是全站用户被误扣误封。
 */
export function testQyViolationRule(body: {
  rule: QyViolationRuleInput
  sample_text: string
  model: string
  group: string
}): Promise<QyViolationRuleTestResult> {
  return qyPost<QyViolationRuleTestResult>('/admin/violation/rules/test', body)
}

export function getQyViolationStats(params: {
  hours: number
}): Promise<QyViolationStats> {
  return qyGet<QyViolationStats>('/admin/violation/stats', params)
}

/** 手动解除「自动回落到影子模式」。规则确认修正后才该点。 */
export function resetQyViolationBreaker(): Promise<QyViolationBreaker> {
  return qyPost<QyViolationBreaker>('/admin/violation/breaker/reset')
}
