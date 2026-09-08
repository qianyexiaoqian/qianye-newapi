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

import {
  QyLotPrizeTypeBadge,
  QyLotPrizeValue,
  QyLotTierRow,
} from '../../lottery/components/lottery-tier-row'
import {
  QY_LOT_PPM_DEN,
  qyLotTiers,
  type QyLotSpecItem,
  type QyLotTier,
} from '../../lottery/types'
import { qyWheelSectorColor, qyWheelSectors } from '../lib/spin'

/**
 * 转盘奖档清单：档位 / 奖品 / 概率 / 剩余 ÷ 初始。
 *
 * 与普通抽奖那份（`QyLotSpecTable` 的 draw 分支）分开是刻意的：
 *
 *   · 「谢谢参与」是一等公民的一行，概率与真实档并列印出来 —— 它进 spec 原像，
 *     用户在参与之前就该看见"落空的概率是多少"；
 *   · `count` 在这里是**初始库存**而不是"预算份数"，发完即止、永远不摊薄；
 *     `stock_left` 是实时剩余（不进承诺），两个数并排印才能看出"有效中奖率随
 *     库存下降"这条转盘固有的性质（design-15 §7.4）。
 *
 * `replayStock` 是证据链页复算出来的各档终态：揭示后它必须等于公布的
 * `stock_left`，并排印出来，对不上的那一格一眼可见。
 */
export function QyWheelTierTable(props: {
  spec: QyLotSpecItem[]
  /** 本地按 seq 重放库存递减得到的终态（tier → 剩余）。只有证据链页给。 */
  replayStock?: Map<number, number>
  /** 不显示"剩余 / 初始"那一列（老后端不下发 stock_left 时用）。 */
  hideStock?: boolean
}) {
  const { t } = useTranslation()
  const tiers = qyLotTiers(props.spec)
  const colorByTier = new Map(
    qyWheelSectors(props.spec).map((sector) => [sector.tier, sector.colorIndex])
  )
  const replay = props.replayStock

  return (
    <ul className='divide-border divide-y'>
      {tiers.map((row) => (
        <QyLotTierRow
          key={row.tier}
          // 「谢谢参与」没有档位号，画一枚空心牌；其余档的牌面直接取盘面上
          // 那一格的扇区色 —— 这份清单与盘面是同一份数据的两种画法，颜色是
          // 它们之间唯一的对应关系。
          tier={row.prize_type === 'none' ? 0 : row.tier}
          medalColor={
            row.prize_type === 'none'
              ? undefined
              : qyWheelSectorColor(colorByTier.get(row.tier) ?? -1)
          }
          name={
            row.prize_type === 'none' ? (
              <span className='text-muted-foreground'>
                {row.name || t('qy_lot_wheel_tier_none')}
              </span>
            ) : (
              row.name
            )
          }
          badge={<QyLotPrizeTypeBadge tier={row} />}
          value={
            row.prize_type === 'none' ? undefined : (
              <QyLotPrizeValue tier={row} />
            )
          }
          // 每一项**要不要出现**必须在这里判掉,不能交给子组件返回 null:
          // `QyLotTierRow` 只丢得掉空串与 null,一个渲染成 null 的元素在它眼里
          // 仍然是一项,于是行末多挂一个孤零零的分隔点(「50.0000% ·」)。
          meta={[
            `${(((row.win_ppm ?? 0) / QY_LOT_PPM_DEN) * 100).toFixed(4)}%`,
            props.hideStock === true || row.prize_type === 'none' ? (
              ''
            ) : (
              <QyWheelStock key='stock' tier={row} />
            ),
            replay?.get(row.tier) == null ? (
              ''
            ) : (
              <QyWheelStockReplay
                key='replay'
                tier={row}
                replayed={replay.get(row.tier) ?? 0}
              />
            ),
          ]}
        />
      ))}
    </ul>
  )
}

/**
 * 「剩余 / 初始」。
 *
 * `count` 在转盘里是**初始库存**而不是"预算份数"：发完即止、永远不摊薄，
 * 而 `stock_left` 是实时剩余（不进承诺）。两个数并排印才能看出"有效中奖率
 * 随库存下降"这条转盘固有的性质（design-15 §7.4）。
 *
 * 恒渲染出东西（不返回 null）：`prize_type='none'` 那一行由调用处判掉，
 * 理由见那里。
 */
function QyWheelStock(props: { tier: QyLotTier }) {
  const { t } = useTranslation()
  const { tier } = props

  // 老后端不下发 stock_left。那时只有初始库存这一个数，印它 —— 印一个
  // 「0 / N」会把"这个字段没有"说成"已经发完了"。
  if (tier.stock_left == null) return <span>{tier.count}</span>
  return (
    <span className={tier.stock_left <= 0 ? 'line-through' : undefined}>
      {t('qy_lot_wheel_stock_value', {
        left: tier.stock_left,
        total: tier.count,
      })}
    </span>
  )
}

/**
 * 证据链页按 seq 重放出来的各档终态。揭示之后它必须等于公布的 `stock_left`，
 * 并排印出来、**对不上的那一格自己变成警示色** —— 一个只是灰着的数字对不上，
 * 没有任何人会发现。
 */
function QyWheelStockReplay(props: {
  tier: QyLotTier
  replayed: number | undefined
}) {
  const { t } = useTranslation()
  if (props.replayed == null) return null

  return (
    <span
      className={
        props.replayed === props.tier.stock_left
          ? undefined
          : 'text-destructive'
      }
    >
      {t('qy_lot_wheel_stock_replayed', { left: props.replayed })}
    </span>
  )
}
