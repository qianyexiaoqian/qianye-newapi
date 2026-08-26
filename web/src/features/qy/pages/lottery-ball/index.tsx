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
 * 双色球大厅（选择夹的第三张标签）。
 *
 * ## 为什么它是一张标签而不是抽奖列表里的一类活动
 *
 * 项目方原话：「把双色球和竞猜分开选择夹，抽奖-竞猜-双色球。」
 *
 * 拆的理由不是资金语义 —— 双色球与按名次/按公示概率一样是"参与费不退"，
 * 风险徽章也因此仍然是同一句。拆的理由是**用户在它上面要做的事完全不同**：
 * 这一页上的人是来选号的（挑红蓝球、机选、一次买多注），看的是开奖号和自己
 * 中了几个球；而抽奖那两种玩法上的人只是报名然后等结果。两类活动混在一张
 * 列表里时，想买一注双色球的人要先翻过若干场与他无关的抽奖，而卡面上那个
 * 「奖池」在两类活动上根本不是同一个数（双色球的是本期投注额）。
 *
 * ## 它没有第五个开关
 *
 * 可见性就是「双色球」那一个玩法开关（`plays.draw_ball`）本身 —— 标签与玩法
 * 在这里是一一对应的，不需要派生。关掉它只影响这张标签与新参与：已经买过的
 * 票在「我的参与」里照常查、照常领奖（见 `hub.tsx`）。
 */
export function QyLotteryBallBody(props: QyLotHallState) {
  return <QyLotHallList lane='ball' {...props} />
}
