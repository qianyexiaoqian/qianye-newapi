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
import { useTranslation } from 'react-i18next'

import { QySectionPageLayout } from '../../components/qy-section-page-layout'
import { useQyConfig } from '../../hooks/use-qy-config'
import { QyPageTabs } from '../components/qy-page-tabs'
import { QyLotteryGuessBody } from '../lottery-guess'
import { QyLotteryRecordsBody } from '../lottery-records'
import type { QyLotHallPhase } from './api'
import { QyLotteryDrawBody } from './index'

/**
 * 「抽奖竞猜」选择夹（需求 2）。
 *
 * 项目方原话：「抽奖竞猜和我的参与放到一个页面，不要单独写一个菜单。」
 * 三张标签逐字对应那句话，顺序与可见性来自 `lib/pages.ts` 的 `QY_TAB_GROUPS` ——
 * 本文件只提供正文。顺序若在两处各写一份，迟早出现"侧栏说有三张、页面上只有
 * 两张"，而 `__tests__/qy-page-tabs.test.ts` 正是按源码扫这一条覆盖度。
 *
 * ## 双色球为什么不是第四张标签
 *
 * 它是活动行上的 `draw_mode`（rank / prob / ball）而不是新的 `kind`，因此天然
 * 属于「抽奖」那张标签里的一类活动。给它单开一张条件渲染的标签，等于让标签数
 * 随后台配置浮动 —— 用户每次进来看到的导航都不一样，而侧栏那一行的语义也就
 * 固定不下来。玩法再多，标签恒为三张。
 */
export function QyLotteryHub() {
  const { t } = useTranslation()
  const config = useQyConfig()
  /*
    两张大厅的「进行中 / 已结束」分段与页码由宿主持有。

    `QyPageTabs` 刻意不加 `keepMounted`（隐藏即卸载），标签正文自己的 useState
    会在切走时归零 —— 用户翻到「已结束」第 3 页做历史公正查询，切去「我的参与」
    核对一条再切回来，那一屏就没了。宿主不随标签卸载，状态放在这里才留得住。
    两张各一份：抽奖与竞猜是两批不同的活动，共用一个页码只会互相把对方翻走。
  */
  const [drawScope, setDrawScope] = useState<QyLotHallPhase>('live')
  const [drawPage, setDrawPage] = useState(1)
  const [guessScope, setGuessScope] = useState<QyLotHallPhase>('live')
  const [guessPage, setGuessPage] = useState(1)

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
          <QyPageTabs
            host='/qy/lottery'
            bodies={{
              '/qy/lottery': (
                <QyLotteryDrawBody
                  page={drawPage}
                  scope={drawScope}
                  onPageChange={setDrawPage}
                  onScopeChange={setDrawScope}
                />
              ),
              '/qy/lottery-guess': (
                <QyLotteryGuessBody
                  page={guessPage}
                  scope={guessScope}
                  onPageChange={setGuessPage}
                  onScopeChange={setGuessScope}
                />
              ),
              '/qy/lottery-records': <QyLotteryRecordsBody />,
            }}
          />
        </div>
      </QySectionPageLayout.Content>
    </QySectionPageLayout>
  )
}
