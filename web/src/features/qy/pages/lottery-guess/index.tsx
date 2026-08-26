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
import { QyLotHallList } from '../lottery/components/lottery-hall-list'
import type { QyLotHallState } from '../lottery/components/lottery-hall-list'

/**
 * 竞猜大厅（选择夹的第二张标签）。
 *
 * ## 为什么它是一张标签而不是抽奖列表里的一个筛选值
 *
 * 竞猜是**奖池再分配**：投注总额扣掉手续费后按比例分给猜中的人，猜错的那部分
 * 本金就是别人的赔付。抽奖是平台出奖品，用户最多损失参与费。两者的最坏结果
 * 差一个量级，而合并菜单之后它们在侧栏上已经是同一行 —— 如果在页面里还共用
 * 一张列表，用户就再也没有任何地方会被告知规则换了。
 *
 * 所以那句红字是**结构性**的，不是提示语：它挂在标签上，而不是挂在某一张卡片
 * 上，因此不可能被翻页翻掉。现在它由 `QyLotHallList` 渲染在分段栏那一行 ——
 * 位置没变（仍然随标签走），占的地方从一整块带边框的横幅缩成一枚徽章。
 * 「奖池再分配」那半句解释挪进了活动详情的手续费与退款说明，那里才是用户
 * 真正要下注的地方。
 */
export function QyLotteryGuessBody(props: QyLotHallState) {
  return <QyLotHallList lane='guess' {...props} />
}
