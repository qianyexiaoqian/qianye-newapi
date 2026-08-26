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
import { QyLotHallList } from './components/lottery-hall-list'
import type { QyLotHallState } from './components/lottery-hall-list'

/**
 * 抽奖大厅（选择夹的第一张标签，按名次 / 按公示概率两种玩法）。
 *
 * 双色球本轮搬去了自己那张标签（`pages/lottery-ball`）：它的 `kind` 仍然是
 * `draw`，所以 `lane='draw'` 这个取值**刻意与 `kind` 不同义** —— 它排除
 * 双色球。这一条只在后端的 WHERE 里有一处实现，前端不再做二次过滤。
 *
 * 只是正文：区段头与标签栏由宿主 `hub.tsx` 提供。它曾经是一个独立页面
 * （`QyLottery`），需求 2 之后降级 —— 标签页里再套一层区段头会得到两级标题。
 *
 * 那句风险提示（「参与费不退」）没有消失，它挪进了 `QyLotHallList` 的分段栏，
 * 与「进行中 / 已结束」同一行：与竞猜合并进同一个菜单之后，两类活动的代价
 * 差一个量级，这句话必须在，但它此前独占一整块带边框的横幅 —— 五个字的内容
 * 占掉了首屏第一屏最值钱的那条位置，把奖池与倒计时挤到了折叠线以下。
 */
export function QyLotteryDrawBody(props: QyLotHallState) {
  return <QyLotHallList lane='draw' {...props} />
}
