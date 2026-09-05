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
 * 环形进度的几何。与 `qy-ring.tsx` 分开,是为了让"剩余比例 → 弧长"这一步
 * 能在 node:test 下不渲染就验:环画反了(剩 10% 却画成 90%)在浏览器里只是
 * "看起来怪",没有任何东西会红。
 */

/** viewBox 36×36 下的半径:留出 2.5 的线宽不被裁掉。 */
export const QY_RING_RADIUS = 15.5

/** 圆周长,`stroke-dasharray` 用它。 */
export const QY_RING_CIRCUMFERENCE = 2 * Math.PI * QY_RING_RADIUS

/** 把任意输入夹到 [0, 1];NaN 与非有限数按 0(空环)处理。 */
export function qyRingClamp(fraction: number): number {
  if (!Number.isFinite(fraction)) return 0
  return Math.min(1, Math.max(0, fraction))
}

/**
 * `fraction` 是**剩余**比例:1 = 满环,0 = 空环。
 * dashoffset 越大画出来的弧越短,所以剩得越少 offset 越大。
 */
export function qyRingDashOffset(fraction: number): number {
  return QY_RING_CIRCUMFERENCE * (1 - qyRingClamp(fraction))
}
