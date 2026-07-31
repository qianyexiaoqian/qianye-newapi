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
import { formatCompactNumber, formatTimestampToDate } from '@/lib/format'

/**
 * 运维类页面（可用率 / 违规 / 对账 / 审计 / 健康）共用的格式化。
 *
 * 这几个页面只由本目录下的页面使用，因此刻意不放进 `features/qy/lib/` ——
 * 那里是 13 个页面共享的契约层，往里加只有 5 个页面用得到的东西会让契约变糊。
 */

/**
 * 「无数据」的统一占位符。
 *
 * 可用率看板最贵的一个坑就是把「没有样本」渲染成 `0%`：运营会据此判定服务
 * 全挂并做出错误决策。后端已经用 `availability: null` + `state: no_data`
 * 把两者彻底分开，前端必须原样保留这个区分。
 */
export const QY_EMPTY_TEXT = '—'

/** unix 秒 → `YYYY-MM-DD HH:mm:ss`，0 / 缺省显示 `-`（与全站一致）。 */
export function formatQyTs(ts: number | null | undefined): string {
  if (ts == null) return '-'
  return formatTimestampToDate(ts)
}

/**
 * 可用率百分比。
 *
 * `null` 一律返回 {@link QY_EMPTY_TEXT} 而不是 `0%` —— 见上面的注释，
 * 这是本模块唯一不能妥协的展示规则。
 */
export function formatQyAvailability(value: number | null | undefined): string {
  if (value == null || !Number.isFinite(value)) return QY_EMPTY_TEXT
  return `${value.toFixed(2)}%`
}

/** 大数量级用缩写（1.2K），列表与卡片都用它，避免同一个数字两种写法。 */
export function formatQyCount(value: number | null | undefined): string {
  if (value == null || !Number.isFinite(value)) return QY_EMPTY_TEXT
  return formatCompactNumber(value)
}

/** 毫秒。0 表示「没有样本」而不是「0 毫秒」，因此同样显示占位符。 */
export function formatQyMs(value: number | null | undefined): string {
  if (value == null || !Number.isFinite(value) || value <= 0) {
    return QY_EMPTY_TEXT
  }
  return `${Math.round(value)} ms`
}

/** 微秒 → 人类可读（规则试跑的耗时在几十到几千微秒之间）。 */
export function formatQyMicros(value: number | null | undefined): string {
  if (value == null || !Number.isFinite(value)) return QY_EMPTY_TEXT
  if (value < 1000) return `${value} µs`
  return `${(value / 1000).toFixed(2)} ms`
}

/** 秒数 → `1d 2h 3m`，用于「最老待定单已挂多久」这类运维读数。 */
export function formatQyDuration(seconds: number | null | undefined): string {
  if (seconds == null || !Number.isFinite(seconds) || seconds < 0) {
    return QY_EMPTY_TEXT
  }
  const days = Math.floor(seconds / 86400)
  const hours = Math.floor((seconds % 86400) / 3600)
  const minutes = Math.floor((seconds % 3600) / 60)
  if (days > 0) return `${days}d ${hours}h`
  if (hours > 0) return `${hours}h ${minutes}m`
  if (minutes > 0) return `${minutes}m ${seconds % 60}s`
  return `${seconds}s`
}

/** 当前时间往前 N 小时的 unix 秒，供筛选栏拼 `start_ts`。 */
export function qySinceHours(hours: number): number {
  return Math.floor(Date.now() / 1000) - hours * 3600
}

/**
 * 把后端下发的 JSON 快照串格式化成可读文本。
 *
 * 解析失败时原样返回：审计快照是被截断过的（`audit.snapshot_max_bytes`），
 * 截断后必然不是合法 JSON，但里面的内容对排障仍然有价值，不能吞掉。
 */
export function formatQySnapshot(raw: string | null | undefined): string {
  const text = (raw ?? '').trim()
  if (text === '') return ''
  try {
    return JSON.stringify(JSON.parse(text), null, 2)
  } catch {
    return text
  }
}
