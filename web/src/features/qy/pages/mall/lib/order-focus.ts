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
import { qyTabTarget } from '../../../lib/pages'

/**
 * 从中奖结果屏 / 我的参与跳到「我的订单」并**定位到那一单**。
 *
 * ## 为什么走 history state 而不是 query 或 hash
 *
 * 「我的订单」是 `/qy/mall` 选择夹里的一张标签，标签由 hash 选中
 * （`pages/lib/tabs.ts`），hash 已经被占用；query 会碰上上游路由的
 * `validateSearch` 剥离问题（同一份说明见那个文件）。history state 两者都不碰，
 * 而且天然是**一次性的**：定位只在"刚中奖、点过来"这一次有意义，刷新或
 * 从侧栏再进来时不该还高亮着上次那一单。
 *
 * `@tanstack/history` 的 `HistoryState` 是空接口，专门留给应用这样合并声明。
 */
declare module '@tanstack/history' {
  interface HistoryState {
    /** 「我的订单」要定位并直接打开的那一单。 */
    qyMallFocusOrderNo?: string
  }
}

/** 交给 `<Link>` 的三件套：宿主页 + 标签 hash + 要定位的单号。 */
export function qyMallOrderLink(orderNo: string): {
  to: string
  hash?: string
  state: { qyMallFocusOrderNo: string }
} {
  return {
    ...qyTabTarget('/qy/mall-orders'),
    state: { qyMallFocusOrderNo: orderNo },
  }
}
