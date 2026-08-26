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
import { useState } from 'react'

import type { QyLotHallPhase } from '../api'
import type { QyLotHallState } from '../components/lottery-hall-list'

/**
 * 一张大厅标签的「翻到哪儿了」——分段（进行中 / 已结束）+ 页码。
 *
 * ── 为什么状态住在宿主而不是列表组件自己身上 ──
 * `QyPageTabs` 刻意不加 `keepMounted`（不可见的标签一个请求都不发），Base UI
 * 的面板隐藏即卸载，列表组件自己的 `useState` 会在切走标签时归零。表现是：
 * 用户翻到「已结束」第 3 页做历史公正查询，切去「我的参与」核对一条再切回来，
 * 那一屏没了，要重新翻三次。宿主不随标签卸载，状态放在那里才留得住。
 *
 * ── 为什么是一个 hook 而不是宿主里六个 useState ──
 * 三张大厅标签各要一份，而且**必须各要一份**：抽奖、竞猜、双色球是三批不同的
 * 活动，共用一个页码只会互相把对方翻走。三份两两成对的 useState 写在宿主里，
 * 迟早出现"把竞猜的 setScope 接到双色球那一档上"这种接错线 —— 那种错在界面上
 * 的表现是"点了标签栏，另一张标签跳页了"，而类型系统看不出来（两者同型）。
 *
 * 返回的形状**就是** `QyLotHallState`，所以调用点是 `{...draw}` 一次展开，
 * 没有第二处可以接错。
 */
export function useQyLotHallCursor(): QyLotHallState {
  const [page, setPage] = useState(1)
  const [scope, setScope] = useState<QyLotHallPhase>('live')
  return {
    onPageChange: setPage,
    onScopeChange: setScope,
    page,
    scope,
  }
}
