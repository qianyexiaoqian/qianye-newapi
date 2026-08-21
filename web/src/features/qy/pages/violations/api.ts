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
  QyMyViolationCategories,
  QyMyViolationRecord,
  QyMyViolationSummary,
} from './types'

/** 只返回真实命中（后端过滤掉 shadow 记录），影子模式下用户并未被扣钱。 */
export function listQyMyViolations(params: {
  p: number
  page_size: number
}): Promise<QyPage<QyMyViolationRecord>> {
  return qyGet<QyPage<QyMyViolationRecord>>('/violation/my-records', params)
}

/**
 * 违规汇总。**当前没有任何页面调用它** —— 项目方要求用户端只留违规类型，
 * 三个统计块（窗口违规次数 / 距离封号还剩余 / 累计扣费）已经拿掉。
 *
 * 保留这个客户端而不是一起删：后端 `my-summary` 仍在下发，接口有人维护、
 * 前端却连一份类型化的读法都没有，等于把契约丢在半路。要恢复那三块统计，
 * 先读 `types.ts` 里 `QyMyViolationSummary` 顶部那段注释，别直接接回去。
 */
export function getQyMyViolationSummary(): Promise<QyMyViolationSummary> {
  return qyGet<QyMyViolationSummary>('/violation/my-summary')
}

/**
 * 违规类型公示：有哪些类型、各自多少次会被处置、以及自己当前每一类的计数。
 *
 * 只返回站点勾了「对用户公示」的类型，且只含对外文案 —— 内部说明与内部名一个
 * 字节都不会下发（后端白名单 `userCategoryView`）。未公示的类型照样计数、
 * 照样参与处置，只是不出现在这里。
 */
export function listQyMyViolationCategories(): Promise<QyMyViolationCategories> {
  return qyGet<QyMyViolationCategories>('/violation/my-categories')
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
