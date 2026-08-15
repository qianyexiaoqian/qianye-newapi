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
import { type TFunction } from 'i18next'

// ============================================================================
// Duration Unit Options
// ============================================================================

export const DURATION_UNITS = [
  { value: 'year', labelKey: 'years' },
  { value: 'month', labelKey: 'months' },
  { value: 'day', labelKey: 'days' },
  { value: 'hour', labelKey: 'hours' },
  { value: 'custom', labelKey: 'Custom (seconds)' },
  // 永久档不是"很长的时长"，而是压根没有到期时间（后端存 end_time = 0）。
  // 它排在最后：上面五档都要配一个数字，选到这一档时那个数字整个消失。
  { value: 'permanent', labelKey: 'qy_plan_duration_permanent' },
] as const

export const RESET_PERIODS = [
  { value: 'never', labelKey: 'No Reset' },
  { value: 'daily', labelKey: 'Daily' },
  { value: 'weekly', labelKey: 'Weekly' },
  { value: 'monthly', labelKey: 'Monthly' },
  { value: 'custom', labelKey: 'Custom (seconds)' },
] as const

export function getDurationUnitOptions(t: TFunction) {
  return DURATION_UNITS.map((u) => ({ value: u.value, label: t(u.labelKey) }))
}

export function getResetPeriodOptions(t: TFunction) {
  return RESET_PERIODS.map((p) => ({ value: p.value, label: t(p.labelKey) }))
}

// ============================================================================
// User Group Product Options
// ============================================================================

/**
 * 「购买后改用户分组 / 到期回退分组」下拉里的「留空」哨兵值。
 *
 * 这两个字段的**空串**是有语义的（不改 / 回退原组），而 Base UI 的 Select 把
 * 空串当成"未选中"并回落到 placeholder —— 运营选了「不改」之后看到的会是一个
 * 空下拉，与"还没选"长得一模一样。所以走一个哨兵，在字段边界上换回空串。
 */
export const PLAN_GROUP_KEEP = '__keep__'
