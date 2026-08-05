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

import { QyLotHallList } from './components/lottery-hall-list'
import type { QyLotHallState } from './components/lottery-hall-list'

/**
 * 抽奖大厅（选择夹的第一张标签）。
 *
 * 只是正文：区段头与标签栏由宿主 `hub.tsx` 提供。它曾经是一个独立页面
 * （`QyLottery`），需求 2 之后降级 —— 标签页里再套一层区段头会得到两级标题。
 *
 * 顶部那句风险提示不是装饰：与竞猜合并进同一个菜单之后，两类活动在用户心里
 * 会糊成一件事，而「参与费不退」与「可能亏本金」是两种完全不同的代价。
 * 靠卡片配色暗示是不够的。
 */
export function QyLotteryDrawBody(props: QyLotHallState) {
  const { t } = useTranslation()
  return (
    <div className='space-y-3'>
      <p className='text-muted-foreground rounded-lg border p-3 text-sm'>
        {t('qy_lot_risk_badge_stake_lost')}
      </p>
      <QyLotHallList kind='draw' {...props} />
    </div>
  )
}
