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
import type { QyRwTask } from '../types'

/**
 * 万分比 → 百分比字符串。
 *
 * 后端一律用 bps 整数(10000 = 100%),界面一律显示百分比。换算只在这里做一次:
 * 两处各写一遍的后果是其中一处忘了除,而 `0.01%` 与 `1%` 在这一页上相差一百倍
 * 的存储量。
 */
export function qyRwPercentText(bps: number): string {
  if (!Number.isFinite(bps) || bps <= 0) return '0%'
  const pct = bps / 100
  // 去掉无意义的尾零:1% 而不是 1.00%,0.01% 保留两位。
  return `${Number(pct.toFixed(2))}%`
}

/**
 * 百分比输入 → 万分比整数。
 *
 * 向下取整而不是四舍五入:这一格是成本闸门,把 0.004% 抬成 0.01% 是在管理员
 * 没同意的情况下把抽样量翻倍。低于 0.01% 的输入落到 0,由后端拒绝。
 */
export function qyRwPercentToBps(input: string): number {
  const pct = Number.parseFloat(input)
  if (!Number.isFinite(pct) || pct <= 0) return 0
  return Math.min(10000, Math.floor(pct * 100))
}

/** 倒计时的预设档,单位秒。上界 90 天由后端 `maxCountdownSeconds` 兜住。 */
export const QY_RW_COUNTDOWNS: readonly number[] = [
  3600,
  6 * 3600,
  24 * 3600,
  3 * 24 * 3600,
  7 * 24 * 3600,
  30 * 24 * 3600,
]

/**
 * 一个任务此刻还剩多久,单位秒。
 *
 * 返回 `null` 表示"没有终点"(永久监听,或者时间窗那一格是 0)。
 * 已经过了终点返回 0 —— 与 `null` 必须分开:前者是"跑完了",后者是"永远跑",
 * 而它们在界面上是两句完全不同的话。
 */
export function qyRwRemainingSeconds(
  task: Pick<QyRwTask, 'ends_at'>,
  nowSeconds: number
): number | null {
  if (!task.ends_at || task.ends_at <= 0) return null
  return Math.max(0, task.ends_at - nowSeconds)
}

/**
 * 任务的进度分母。0 表示"不限条数",此时没有进度可言。
 *
 * 单独一个函数是因为调用点有两处(进度条与列表文案),而"0 = 不限"这条口径
 * 抄第二遍就会有一处把它显示成 `37 / 0`。
 */
export function qyRwProgressText(task: QyRwTask): string {
  if (task.max_records <= 0) return String(task.captured)
  return `${task.captured} / ${task.max_records}`
}

/**
 * 这个任务的保留期该显示成什么。
 *
 * 三个语义值各有各的说法,而它们在 `retention_days` 这一格上分别是
 * `null` / `0` / 正整数 —— 折成两种的话,一份要跟着仲裁走完的取证材料
 * 会显示成"30 天后清理"。
 */
export function qyRwRetentionKind(
  task: Pick<QyRwTask, 'retention_days'>
): 'default' | 'forever' | 'custom' {
  if (task.retention_days == null) return 'default'
  if (task.retention_days <= 0) return 'forever'
  return 'custom'
}
