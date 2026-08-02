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
  QyViolationCounterPage,
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
  /** `request_rate` 规则的试跑输入：假设这一分钟内已有多少条非流式请求。 */
  rate_count: number
}): Promise<QyViolationRuleTestResult> {
  return qyPost<QyViolationRuleTestResult>('/admin/violation/rules/test', body)
}

export function getQyViolationStats(params: {
  hours: number
}): Promise<QyViolationStats> {
  return qyGet<QyViolationStats>('/admin/violation/stats', params)
}

/**
 * 手动解除「熔断自动回落到影子模式」。规则确认修正后才该点。
 *
 * 它**管不到全局影子开关**（YAML 或 qy_settings 的覆盖）—— 那条路走
 * `setQyViolationShadowMode`。两者分开是刻意的：熔断是系统自己踩的刹车，
 * 全局开关是人定的发布口径，一个按钮同时松开两者会让一次熔断恢复顺手
 * 把还没准备好的规则全部放出去。
 */
export function resetQyViolationBreaker(): Promise<QyViolationBreaker> {
  return qyPost<QyViolationBreaker>('/admin/violation/breaker/reset')
}

/**
 * 设置**全局**影子开关。
 *
 * `shadow: null` = 清掉覆盖，重新跟随 YAML 的 `violation.shadow_mode`。
 * 后端不会因此改动任何一条规则的 `dry_run` —— 全局关掉时若顺手把规则转正，
 * 一次熔断自动恢复就会把全部灰度规则一起放出去。
 */
export function setQyViolationShadowMode(
  shadow: boolean | null
): Promise<QyViolationBreaker> {
  return qyPut<QyViolationBreaker>('/admin/violation/mode', { shadow })
}

export function listQyViolationCounters(params: {
  p: number
  page_size: number
  user_id?: number
}): Promise<QyViolationCounterPage> {
  return qyGet<QyViolationCounterPage>('/admin/violation/counters', params)
}

/**
 * 把某个用户当前窗口的违规计数清零。
 *
 * 存在理由是历史脏数据：本轮之前影子命中也会推进 `hit_count`，现网的计数器
 * 因此被污染，而历史行无法分辨哪几次来自影子。后端只清 `hit_count` 与窗口起点，
 * `total_count`（终身累计）与 `ban_cycle`（封禁认领互斥键）都不动。
 */
export function resetQyViolationCounter(
  userId: number,
  reason: string
): Promise<{ reset: boolean; hit_count_before: number }> {
  return qyPost<{ reset: boolean; hit_count_before: number }>(
    `/admin/violation/counters/${userId}/reset`,
    { reason }
  )
}
