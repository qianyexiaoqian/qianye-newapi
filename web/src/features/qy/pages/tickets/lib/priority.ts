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
import type { StatusVariant } from '@/components/status-badge'

import type { QyTicketPriority } from '../types'

/**
 * 工单等级的色板与文案键。
 *
 * 不并进 `features/qy/lib/status.ts`：那张表回答的是"单据处于什么状态"，
 * 而等级是**另一个维度**。`low` / `high` 这种名字混进状态表之后，
 * 一个未来的 `high` 状态就再也没法登记了。
 *
 * 顺序即选择器顺序，与后端 `Priorities()` 一致。前端刻意**不**从后端下发的
 * `priorities` 数组推导颜色 —— 那样新增一档时前端会静默给它一个默认色，
 * 而这里少一行会在 TypeScript 上直接报错。
 */
const PRIORITY_STYLES: Record<
  QyTicketPriority,
  { variant: StatusVariant; labelKey: string }
> = {
  low: { variant: 'neutral', labelKey: 'qy_tk_prio_low' },
  normal: { variant: 'info', labelKey: 'qy_tk_prio_normal' },
  high: { variant: 'warning', labelKey: 'qy_tk_prio_high' },
  urgent: { variant: 'danger', labelKey: 'qy_tk_prio_urgent' },
}

/** 全部等级，从低到高。选择器与筛选器共用这一个数组。 */
export const QY_TICKET_PRIORITIES: readonly QyTicketPriority[] = [
  'low',
  'normal',
  'high',
  'urgent',
]

/** 全部状态，供管理端筛选器渲染。顺序 = 客服的处理优先级。 */
export const QY_TICKET_STATUSES = [
  'open',
  'user_replied',
  'replied',
  'closed',
] as const

/**
 * 取等级样式。未知取值回落中性徽章 —— 后端新增一档时前端最多"不好看"，不能崩。
 */
export function getQyTicketPriorityStyle(priority: string): {
  variant: StatusVariant
  labelKey: string
} {
  return (
    PRIORITY_STYLES[priority as QyTicketPriority] ?? {
      variant: 'neutral',
      labelKey: '',
    }
  )
}

/**
 * 字节数 → 人类可读的 MB 上限，用于"最大 2 MB"这类提示。
 *
 * 只保留一位小数并向下取整：显示"最大 2.1 MB"而实际上限是 2097152 字节的话，
 * 用户会拿一张 2.05 MB 的图反复失败。宁可说小一点。
 */
export function qyBytesToMbLabel(bytes: number): string {
  if (!Number.isFinite(bytes) || bytes <= 0) return '0'
  return (Math.floor((bytes / (1024 * 1024)) * 10) / 10).toString()
}
