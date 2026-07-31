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

import type { QyAvailState } from './types'

/** 时间范围。上限 720 小时（30 天）与后端 `maxRangeHours` 对齐。 */
export const QY_AVAIL_RANGES = [
  { hours: 1, labelKey: 'qy_avl_range_1h' },
  { hours: 6, labelKey: 'qy_avl_range_6h' },
  { hours: 24, labelKey: 'qy_avl_range_24h' },
  { hours: 168, labelKey: 'qy_avl_range_7d' },
  { hours: 720, labelKey: 'qy_avl_range_30d' },
] as const

export const QY_AVAIL_SORTS = [
  { value: 'availability_asc', labelKey: 'qy_avl_sort_worst' },
  { value: 'requests_desc', labelKey: 'qy_avl_sort_requests' },
  { value: 'model_asc', labelKey: 'qy_avl_sort_model' },
] as const

type QyAvailStateStyle = {
  labelKey: string
  /** 热力图格子的底色 + 文字色。 */
  cellClass: string
  /**
   * 与颜色并行的第二编码。色盲用户只看颜色分不出 ok / degraded / down，
   * 而这个页面的全部信息量都在颜色上，因此符号是必需的而非装饰。
   */
  glyph: string
  badge: StatusVariant
}

const QY_AVAIL_STATE_STYLES: Record<string, QyAvailStateStyle> = {
  ok: {
    labelKey: 'qy_avl_state_ok',
    cellClass: 'bg-success/15 text-success',
    glyph: '✓',
    badge: 'success',
  },
  degraded: {
    labelKey: 'qy_avl_state_degraded',
    cellClass: 'bg-warning/15 text-warning',
    glyph: '!',
    badge: 'warning',
  },
  down: {
    labelKey: 'qy_avl_state_down',
    cellClass: 'bg-destructive/15 text-destructive',
    glyph: '✕',
    badge: 'danger',
  },
  low_sample: {
    labelKey: 'qy_avl_state_low_sample',
    cellClass: 'bg-muted text-muted-foreground',
    glyph: '?',
    badge: 'neutral',
  },
  // 无数据与「全挂了」必须一眼分得开：不给底色、不给百分比。
  no_data: {
    labelKey: 'qy_avl_state_no_data',
    cellClass: 'bg-transparent text-muted-foreground',
    glyph: '·',
    badge: 'neutral',
  },
  not_offered: {
    labelKey: 'qy_avl_state_not_offered',
    cellClass: 'bg-transparent text-muted-foreground/50',
    glyph: '/',
    badge: 'neutral',
  },
}

const QY_AVAIL_UNKNOWN_STYLE: QyAvailStateStyle = {
  labelKey: 'qy_avl_state_unknown',
  cellClass: 'bg-muted text-muted-foreground',
  glyph: '·',
  badge: 'neutral',
}

/** 取六态样式。未登记的取值回落成中性，后端加枚举时前端最多「不好看」。 */
export function getQyAvailStateStyle(state: QyAvailState): QyAvailStateStyle {
  return QY_AVAIL_STATE_STYLES[state] ?? QY_AVAIL_UNKNOWN_STYLE
}

/** 失败原因 / 口径条目的 i18n key。未登记的原样显示英文标识。 */
export function qyAvailOutcomeKey(outcome: string): string {
  return `qy_avl_outcome_${outcome}`
}
