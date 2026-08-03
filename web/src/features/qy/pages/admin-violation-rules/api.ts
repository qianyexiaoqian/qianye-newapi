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
import { api } from '@/lib/api'

import {
  qyDelete,
  qyErrorFromBlobFailure,
  qyGet,
  qyPost,
  qyPut,
  QY_API_PREFIX,
} from '../../lib/api'
import type { QyPage } from '../../lib/types'
import type {
  QyViolationBreaker,
  QyViolationBuiltinCatalog,
  QyViolationCounterPage,
  QyViolationImportResult,
  QyViolationRecord,
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
 * 手动解除熔断。规则确认修正后才该点。
 *
 * 它**一个字节都不动规则的 mode** —— 解除熔断的语义是「刹车松开，各条规则回到
 * 它们自己声明的模式」。顺手把规则转正就是替运营做了一个他没做过的决定。
 *
 * 全局影子开关（`PUT /violation/mode`）已随全局模式层一起删除：模式绑在规则上，
 * 改模式就是去改那条规则，不再有第二个入口。
 */
export function resetQyViolationBreaker(): Promise<QyViolationBreaker> {
  return qyPost<QyViolationBreaker>('/admin/violation/breaker/reset')
}

/**
 * 内置防护规则包的目录 + 每条在本站点的导入状态。
 *
 * 返回的是**代码里的模板 + 库里的状态**，与规则列表不是同一种东西 ——
 * 所以它是独立的一条路由，而不是 `/rules?source=builtin`。
 */
export function getQyViolationBuiltinCatalog(): Promise<QyViolationBuiltinCatalog> {
  return qyGet<QyViolationBuiltinCatalog>('/admin/violation/rules/builtin')
}

/**
 * 一键导入内置防护规则包。
 *
 * `keys` 为空 = 全部。导入出来一律是**影子模式**的普通规则行，可编辑、可删除、
 * 可单独开关。`upgrade: true` 时额外把「未被运营改过」的旧版规则替换成新版
 * 模式串 —— 改过的一律跳过并在 `results` 里如实说明，任何情况下都不覆盖。
 */
export function importQyViolationBuiltinRules(body: {
  keys?: string[]
  upgrade?: boolean
}): Promise<QyViolationImportResult> {
  return qyPost<QyViolationImportResult>(
    '/admin/violation/rules/import-builtin',
    body
  )
}

export type QyViolationRecordListParams = {
  p: number
  page_size: number
  rule_id?: number
  /** 三态：不传 = 全部；`1` = 只看影子；`0` = 只看真实。 */
  shadow?: '0' | '1'
  shadow_reason?: string
  user_id?: number
  start_ts?: number
  end_ts?: number
}

/** 命中记录列表。这一页只用它做「按规则看影子命中」的分析，不做处置。 */
export function listQyViolationRecords(
  params: QyViolationRecordListParams
): Promise<QyPage<QyViolationRecord>> {
  return qyGet<QyPage<QyViolationRecord>>('/admin/violation/records', params)
}

/**
 * 导出命中记录为 CSV。
 *
 * 走鉴权接口 + Blob，而不是拼一个 `<a href>` 直链：这条路由要管理员身份，
 * 直链会因为缺 Bearer 直接 401，而浏览器下载失败时不会有任何可见提示。
 *
 * 不能用 `qyGet`：那条路会把响应体当 `{success,data}` 信封解，而这里的成功
 * 响应就是 CSV 文本本身。失败时 axios 给回的 `response.data` 也是 Blob
 * （responseType 对错误响应一视同仁），所以错误还原走 `qyErrorFromBlobFailure`。
 */
export async function exportQyViolationRecords(
  params: Omit<QyViolationRecordListParams, 'p' | 'page_size'>
): Promise<Blob> {
  try {
    const res = await api.get(
      `${QY_API_PREFIX}/admin/violation/records/export`,
      {
        skipErrorHandler: true,
        skipBusinessError: true,
        responseType: 'blob',
        params,
        // 上游的在途 GET 去重只按 url + params 归并，认不出 responseType 的差异。
        disableDuplicate: true,
      }
    )
    return res.data as Blob
  } catch (error) {
    throw await qyErrorFromBlobFailure(error)
  }
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
