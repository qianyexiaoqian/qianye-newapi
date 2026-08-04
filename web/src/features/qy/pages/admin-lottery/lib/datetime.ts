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
/**
 * `<input type="datetime-local">` 的值 ↔ unix 秒。
 *
 * 浏览器把这个控件的值当作**本地时间**的无时区字符串，而后端四个时刻全是 unix
 * 秒。转换必须只有一处：两处各写一遍的典型后果是"填进去是 20:00、存下来是
 * 12:00"，而这四个时刻在 publish 之后就再也改不了了。
 */

/** unix 秒 → `YYYY-MM-DDTHH:mm`（本地时区）。0 返回空串。 */
export function qyLotToLocalInput(seconds: number): string {
  if (seconds <= 0) return ''
  const date = new Date(seconds * 1000)
  const pad = (value: number) => String(value).padStart(2, '0')
  return [
    date.getFullYear(),
    '-',
    pad(date.getMonth() + 1),
    '-',
    pad(date.getDate()),
    'T',
    pad(date.getHours()),
    ':',
    pad(date.getMinutes()),
  ].join('')
}

/** `YYYY-MM-DDTHH:mm`（本地时区）→ unix 秒。非法输入返回 0。 */
export function qyLotFromLocalInput(value: string): number {
  if (value.trim() === '') return 0
  const parsed = new Date(value)
  const time = parsed.getTime()
  if (!Number.isFinite(time)) return 0
  return Math.floor(time / 1000)
}
