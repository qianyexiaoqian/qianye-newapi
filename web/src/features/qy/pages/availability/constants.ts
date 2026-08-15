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

import { formatQyAvailability, formatQyMs, QY_EMPTY_TEXT } from '../ops/format'
import type {
  QyAvailCell,
  QyAvailMetricKey,
  QyAvailPerf,
  QyAvailSortMode,
  QyAvailState,
} from './types'

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
  { value: 'latency_desc', labelKey: 'qy_avl_sort_latency' },
  { value: 'ttft_desc', labelKey: 'qy_avl_sort_ttft' },
  { value: 'tps_asc', labelKey: 'qy_avl_sort_tps' },
  { value: 'requests_desc', labelKey: 'qy_avl_sort_requests' },
  { value: 'model_asc', labelKey: 'qy_avl_sort_model' },
] as const

/** 生成速度。`null` 与 `<= 0` 一律占位符 —— 见 {@link QY_EMPTY_TEXT} 的理由。 */
export function formatQyTps(value: number | null | undefined): string {
  if (value == null || !Number.isFinite(value) || value <= 0) {
    return QY_EMPTY_TEXT
  }
  return `${value.toFixed(2)} t/s`
}

/**
 * 主指标取值所需的最小字段集。矩阵格子与趋势点都满足它，
 * 于是热力图、表格、趋势图三处共用同一份取值与格式化 ——
 * 不会出现「矩阵里 1.2s、点开抽屉变成 1200」这种自相矛盾。
 */
export type QyAvailMetricRow = QyAvailPerf & {
  availability: number | null
  counted: number
}

export type QyAvailMetricDef = {
  key: QyAvailMetricKey
  labelKey: string
  /** 该指标的「最差优先」排序。主指标一变就跟着变，理由见 QyAvailSortMode。 */
  sort: QyAvailSortMode
  /** 取值。`null` = 无数据 / 样本不足，调用方一律渲染成占位符，绝不补 0。 */
  valueOf: (row: QyAvailMetricRow) => number | null
  /** 该指标在这一格 / 这个时间点上的样本数，用来解释「为什么是横杠」。 */
  samplesOf: (row: QyAvailMetricRow) => number
  format: (value: number | null | undefined) => string
  /** 数值越大越好？可用率与速度是，两个延迟不是。 */
  higherIsBetter: boolean
  /** 图表 y 轴的单位后缀。空串表示不加后缀。 */
  axisSuffix: string
}

/**
 * 矩阵可切换的四个主指标。
 *
 * 用户的原话是「按照分组显示该分组模型可用率，延迟，速度等等」——
 * 这四项就是那句话的全部内容，一个不多一个不少。数据在一次矩阵请求里
 * 已经全部拿到，切换主指标只换展示与排序，不会多打一次请求。
 */
export const QY_AVAIL_METRICS: QyAvailMetricDef[] = [
  {
    key: 'availability',
    labelKey: 'qy_avl_availability',
    sort: 'availability_asc',
    valueOf: (row) => row.availability,
    samplesOf: (row) => row.counted,
    format: formatQyAvailability,
    higherIsBetter: true,
    axisSuffix: '%',
  },
  {
    key: 'latency',
    labelKey: 'qy_avl_latency',
    sort: 'latency_desc',
    valueOf: (row) => row.avg_latency_ms,
    samplesOf: (row) => row.latency_samples,
    format: formatQyMs,
    higherIsBetter: false,
    axisSuffix: ' ms',
  },
  {
    key: 'ttft',
    labelKey: 'qy_avl_ttft',
    sort: 'ttft_desc',
    valueOf: (row) => row.avg_ttft_ms,
    samplesOf: (row) => row.ttft_samples,
    format: formatQyMs,
    higherIsBetter: false,
    axisSuffix: ' ms',
  },
  {
    key: 'tps',
    labelKey: 'qy_avl_tps',
    sort: 'tps_asc',
    valueOf: (row) => row.avg_tps,
    samplesOf: (row) => row.speed_samples,
    format: formatQyTps,
    higherIsBetter: true,
    axisSuffix: ' t/s',
  },
]

export function getQyAvailMetric(key: QyAvailMetricKey): QyAvailMetricDef {
  return QY_AVAIL_METRICS.find((m) => m.key === key) ?? QY_AVAIL_METRICS[0]
}

/** 一行（同一个模型）中各分组的最好 / 最差取值。`null` 表示不值得比。 */
export type QyAvailRowScale = { best: number; worst: number } | null

/** 相对分档的显著性下限：差距不到 15% 一律不着色。 */
const QY_AVAIL_TONE_MIN_SPREAD = 0.15

/**
 * 性能指标的着色是「同一个模型在各可见分组之间的相对快慢」，**不是绝对阈值**。
 *
 * 绝对阈值在这里是错的：端到端延迟含完整生成时间，一个长回复 30 秒完全正常，
 * 而 gpt-5-nano 的 30 秒是灾难 —— 拿同一条线去卡所有模型，结果不是信息是噪声。
 * 而「同一个模型，哪个分组更快」恰好就是用户打开这一页要问的问题。
 *
 * 只有一个分组有数据时不着色：一个点没有「相对」可言。
 */
export function qyAvailRowScale(
  values: number[],
  higherIsBetter: boolean
): QyAvailRowScale {
  if (values.length < 2) return null
  const max = Math.max(...values)
  const min = Math.min(...values)
  if (max === min) return null
  // 分母取 max(|best|, 1)：best 接近 0 时（例如 TTFT 只有几毫秒）
  // 任何绝对差都会被算成巨大的相对差，凭空造出红绿。
  const best = higherIsBetter ? max : min
  const worst = higherIsBetter ? min : max
  if (
    Math.abs(max - min) / Math.max(Math.abs(best), 1) <
    QY_AVAIL_TONE_MIN_SPREAD
  ) {
    return null
  }
  return { best, worst }
}

export type QyAvailTone = 'bad' | 'good' | 'none'

export function qyAvailToneOf(
  value: number | null,
  scale: QyAvailRowScale
): QyAvailTone {
  if (value == null || scale == null) return 'none'
  if (value === scale.best) return 'good'
  if (value === scale.worst) return 'bad'
  return 'none'
}

/**
 * 相对分档的样式。沿用六态那套「颜色 + 符号」双编码：
 * 色盲用户只看颜色分不出好坏，而这一页的信息量全在颜色上。
 */
export const QY_AVAIL_TONE_STYLES: Record<
  QyAvailTone,
  { cellClass: string; glyph: string; labelKey: string }
> = {
  good: {
    cellClass: 'bg-success/15 text-success',
    glyph: '✓',
    labelKey: 'qy_avl_tone_best',
  },
  bad: {
    cellClass: 'bg-destructive/15 text-destructive',
    glyph: '!',
    labelKey: 'qy_avl_tone_worst',
  },
  none: {
    cellClass: 'bg-muted/40',
    glyph: '·',
    labelKey: 'qy_avl_tone_plain',
  },
}

/** 有格子但该指标没有取值时的样式。绝不复用六态的绿色 —— 那会把
 *  「可用率正常但延迟样本不足」画成一个绿色的空格。 */
export const QY_AVAIL_NO_VALUE_STYLE = {
  cellClass: 'bg-transparent text-muted-foreground',
  glyph: '·',
}

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

/**
 * 一个格子该按哪种状态渲染。
 *
 * ★ `measured` 为假（该模型不在 `covered_models` 里）时必须是 `unknown`，
 * **绝不能**是 `not_offered`。两者的区别是「我们没查」与「我们查了，该分组不提供
 * 这个模型」——后者是一个肯定断言。默认 `max_series_per_query=200`、一页 30 个
 * 模型，只要有 ≥7 个可见分组就每页必然截断，于是这条断言会被批量地印错。
 *
 * 缺失的格子（`cell == null`）同样按未知处理：被覆盖的模型后端保证整行都在，
 * 真的缺了就说明这一格没查到，而不是查到了「不提供」。
 */
export function qyAvailCellState(
  cell: QyAvailCell | undefined,
  measured: boolean
): QyAvailState {
  if (!measured || cell == null) return 'unknown'
  return cell.state
}

/** 失败原因 / 口径条目的 i18n key。未登记的原样显示英文标识。 */
export function qyAvailOutcomeKey(outcome: string): string {
  return `qy_avl_outcome_${outcome}`
}
