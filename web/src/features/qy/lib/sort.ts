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

/** `desc` = 从多到少（时间列是从新到旧）；`asc` = 从少到多。与后端 `?order=` 同值。 */
export type QySortOrder = 'asc' | 'desc'

export type QySortState<K extends string> = { key: K; order: QySortOrder }

/**
 * 点一次表头之后的排序状态：点同一列在两个方向之间切换，点另一列从「从多到少」开始
 * （运营点开一列，最想先看的是最大的那几个）。
 */
export function qyNextSort<K extends string>(
  current: QySortState<K>,
  key: K
): QySortState<K> {
  if (current.key !== key) return { key, order: 'desc' }
  return { key, order: current.order === 'desc' ? 'asc' : 'desc' }
}
