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
import { useTranslation } from 'react-i18next'

import { QySectionPageLayout } from '../../components/qy-section-page-layout'
import { useQyConfig } from '../../hooks/use-qy-config'
import { qyAnyLotPlayShown, qyLotDrawShown } from '../../lib/pages'
import { QyPageTabs } from '../components/qy-page-tabs'
import { QyLotteryBallBody } from '../lottery-ball'
import { QyLotteryGuessBody } from '../lottery-guess'
import { QyLotteryRecordsBody } from '../lottery-records'
import { QyLotteryDrawBody } from './index'
import { useQyLotHallCursor } from './lib/use-hall-cursor'

/**
 * 「抽奖竞猜」选择夹。
 *
 * 项目方原话（本轮）：「把双色球和竞猜分开选择夹，抽奖-竞猜-双色球。
 * （每个入口都可以单独被隐藏或显示）」上一轮那句是：「抽奖竞猜和我的参与放到
 * 一个页面，不要单独写一个菜单。」四张标签逐字对应这两句话，顺序与可见性来自
 * `lib/pages.ts` 的 `QY_TAB_GROUPS` —— 本文件只提供正文。顺序若在两处各写一份，
 * 迟早出现"侧栏说有四张、页面上只有三张"，而 `__tests__/qy-page-tabs.test.ts`
 * 正是按源码扫这一条覆盖度。
 *
 * ## 分区是两级：外层分玩法，内层分进行中 / 已结束
 *
 * 外层标签分的是"我今天想玩哪一种"，每张标签内部的分段栏分的是"还能不能参加"。
 * 两者不是同一维度，拍平成并列的六张标签会让标签栏随玩法数量翻倍。分段栏由
 * `QyLotHallList` 渲染，位置与状态见 `lib/use-hall-cursor.ts`。
 *
 * ## 三个入口各自的显隐，与既有的四个玩法开关怎么合并
 *
 * **不新增第五个开关。** 开关仍然是运营在「抽奖/竞猜配置」里的那四个玩法
 * （按名次 / 按公示概率 / 双色球 / 竞猜），标签的可见性**由它底下至少一个玩法
 * 可见决定**：
 *
 *   · 「双色球」「竞猜」各自只压着一种玩法 → 标签可见性就是那一个开关；
 *   · 「抽奖」底下压着按名次与按公示概率两种 → 两种都关掉时这张标签才消失
 *     （`qyLotDrawShown`）。
 *
 * 反过来（给标签自己再加一个开关）会造出"标签开着、底下两种玩法都关"这种自相
 * 矛盾的状态：用户点进去是一张永远空的列表，而运营在配置页上看到的是"抽奖已
 * 开启"。两套开关互相打架时，没有任何一处会报错。
 *
 * ## 「我的参与」永远在
 *
 * 玩法隐藏的效果**只到大厅可见性与新报名这一层**。已参与的人必须还能查到
 * 自己那一票、还能领奖 —— 这是玩法隐藏与"功能下线"的分界线，也是这一整条
 * 改动里唯一不能让步的一条。所以那一行 `bodies` 是无条件的，`lottery-play-
 * visibility.test.ts` 按源码盯着它不许长出 `?` 或 `&&`。
 *
 * 四种玩法全关时侧栏那一行也没了（`nav.ts` 的 `qyEntrySwitches`），能到这里的
 * 只有直达链接，顶部给一句中性说明 —— 而「我的参与」照旧在。
 *
 * 少给一张标签是 `QyPageTabs` 支持的做法（`bodies` 里缺 url = 那张不渲染），
 * 而不是在这里再写一份标签清单 —— 顺序的唯一真源仍然是 `QY_TAB_GROUPS`。
 */
export function QyLotteryHub() {
  const { t } = useTranslation()
  const config = useQyConfig()
  const plays = config.lottery.plays
  const drawShown = qyLotDrawShown(plays)
  const anyPlayShown = qyAnyLotPlayShown(plays)
  /*
    三张大厅标签各持一份「翻到哪儿了」，由宿主持有（宿主不随标签卸载）。
    必须各持一份：三批活动共用一个页码只会互相把对方翻走。
  */
  const draw = useQyLotHallCursor()
  const guess = useQyLotHallCursor()
  const ball = useQyLotHallCursor()

  return (
    <QySectionPageLayout>
      <QySectionPageLayout.Title>
        {t('qy_nav_lottery_hub')}
      </QySectionPageLayout.Title>
      <QySectionPageLayout.Content>
        <div className='space-y-3'>
          {/* 站点把展示开关关掉时，导航里已经没有这一行了；能到这里的只有
              直达链接。给一句中性说明而不是红色报错 —— 功能没坏，只是这一期
              不对外开放。放在宿主上而不是每张标签里：它讲的是整组的状态。 */}
          {config.status === 'enabled' && !config.lottery.show_entry && (
            <p className='text-muted-foreground rounded-lg border p-3 text-sm'>
              {t('qy_lot_entry_hidden_note')}
            </p>
          )}
          {/* 一种玩法都不开时，三张大厅标签都不渲染，只剩「我的参与」。
              这句话必须说出来 —— 否则用户看到的是一个只有一张标签的页面，
              分不清"这一期没开"与"页面坏了"。 */}
          {config.status === 'enabled' &&
            config.lottery.show_entry &&
            !anyPlayShown && (
              <p className='text-muted-foreground rounded-lg border p-3 text-sm'>
                {t('qy_lot_all_plays_hidden_note')}
              </p>
            )}
          <QyPageTabs
            host='/qy/lottery'
            bodies={{
              // 缺 url = 那张标签不渲染（见 QyPageTabs）。这里刻意用
              // `undefined` 而不是渲染一个空壳：空壳会把「这一期不开抽奖」
              // 显示成「抽奖一场都没有」，两者对用户是两回事。
              '/qy/lottery': drawShown ? (
                <QyLotteryDrawBody {...draw} />
              ) : undefined,
              '/qy/lottery-guess': plays.guess ? (
                <QyLotteryGuessBody {...guess} />
              ) : undefined,
              // 双色球只压着一个玩法开关，所以这里直接读它，不派生。
              '/qy/lottery-ball': plays.draw_ball ? (
                <QyLotteryBallBody {...ball} />
              ) : undefined,
              // 「我的参与」不看任何玩法开关。已参与的人查票与领奖是这一整条
              // 改动的硬约束，隐藏入口绝不能连带藏掉已经发生的事。
              '/qy/lottery-records': <QyLotteryRecordsBody />,
            }}
          />
        </div>
      </QySectionPageLayout.Content>
    </QySectionPageLayout>
  )
}
