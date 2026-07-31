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
import type { QyGpShadowDimension, QyGpShadowSegment } from '../types'

/**
 * 影子差额的前端聚合。
 *
 * 后端给到「(分组, 模型, 规则区间)」的段粒度，「按模型看」「按分组看」在前端
 * 合并 —— 这两个视图必须来自同一批数字，否则切一下维度总额就对不上，运营会
 * 立刻不敢相信任何一个视图。合并的是整数 quota，累加不引入误差。
 *
 * **不精确的段不并入金额，只累加它的请求数。** 后端已经把这类段的
 * `delta_quota` 置 0 并单独计数（旧值为 0、或计价口径发生切换时按比例折算不
 * 成立），前端必须原样保留这个区分：把它们默默算成 0 差额，会让「合计」看起来
 * 完整而实际上漏了一块。
 */

export type QyGpShadowBucket = {
  key: string
  label: string
  requests: number
  /** 可折算部分分摊到本桶的真实扣费。 */
  attributed_quota: number
  /** 正 = 切换后多收，负 = 少收。只累加可折算的段。 */
  delta_quota: number
  /** 无法折算的请求数。大于 0 时本桶的金额是不完整的。 */
  inexact_requests: number
}

/** 时间区间预设。后端单次对账上限 31 天，所以最长给到 30 天。 */
export type QyGpRangePreset = '24h' | '7d' | '30d'

export const QY_GP_RANGE_PRESETS: QyGpRangePreset[] = ['24h', '7d', '30d']

const RANGE_SECONDS: Record<QyGpRangePreset, number> = {
  '24h': 24 * 3600,
  '7d': 7 * 24 * 3600,
  '30d': 30 * 24 * 3600,
}

/**
 * 预设 → unix 秒区间（后端参数名是 `start` / `end`）。
 *
 * 末端**对齐到整分钟**：直接用当前秒会让 queryKey 每次渲染都变，react-query
 * 会当成一个新查询无限重取。对账数字按分钟对齐完全够用。
 */
export function qyShadowRange(preset: QyGpRangePreset): {
  start: number
  end: number
} {
  const end = Math.floor(Date.now() / 60_000) * 60
  return { start: end - RANGE_SECONDS[preset], end }
}

/**
 * 按维度聚合并按差额绝对值从大到小排序。
 *
 * 排序用**绝对值**而不是差额本身：少收和多收对运营同样重要，按有符号值排会
 * 把「一个模型每天少收一大笔」压到列表最底下。
 */
export function qyAggregateShadow(
  segments: QyGpShadowSegment[] | undefined,
  dimension: QyGpShadowDimension
): QyGpShadowBucket[] {
  const buckets = new Map<string, QyGpShadowBucket>()

  for (const segment of segments ?? []) {
    const key = dimension === 'group' ? segment.group_name : segment.model_name
    let bucket = buckets.get(key)
    if (bucket == null) {
      bucket = {
        key,
        label: key,
        requests: 0,
        attributed_quota: 0,
        delta_quota: 0,
        inexact_requests: 0,
      }
      buckets.set(key, bucket)
    }
    bucket.requests += segment.requests
    if (segment.exact) {
      bucket.attributed_quota += segment.attributed_quota
      bucket.delta_quota += segment.delta_quota
    } else {
      bucket.inexact_requests += segment.requests
    }
  }

  return [...buckets.values()].sort(
    (left, right) => Math.abs(right.delta_quota) - Math.abs(left.delta_quota)
  )
}
