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

import { StaticDataTable } from '@/components/data-table'
import { Badge } from '@/components/ui/badge'

import { QySdAmount } from '../../../components/qy-sd-amount'
import {
  QY_LOT_PPM_DEN,
  isQyLotProductPrize,
  isQyLotTextPrize,
  qyLotTiers,
  type QyLotSpecItem,
  type QyLotTier,
} from '../../lottery/types'
import { QyMallKindBadge } from '../../mall/components/mall-kind-badge'
import { QY_EMPTY_TEXT } from '../../ops/format'
import { qyWheelSectorColor, qyWheelSectors } from '../lib/spin'

/**
 * 转盘奖档表：档位 / 奖品 / 概率 / 剩余 ÷ 初始。
 *
 * 与普通抽奖那张表（`QyLotSpecTable` 的 draw 分支）分开是刻意的：
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
    <StaticDataTable
      data={tiers}
      getRowKey={(row: QyLotTier) => row.tier}
      columns={[
        {
          id: 'tier',
          header: t('qy_lot_tier'),
          cellClassName: 'tabular-nums',
          cell: (row: QyLotTier) => (
            <span className='inline-flex items-center gap-1.5'>
              <span
                aria-hidden='true'
                className='inline-block size-2.5 shrink-0 rounded-full'
                style={{
                  background: qyWheelSectorColor(
                    colorByTier.get(row.tier) ?? -1
                  ),
                }}
              />
              {row.prize_type === 'none'
                ? t('qy_lot_wheel_tier_none')
                : t('qy_lot_tier_no', { no: row.tier })}
            </span>
          ),
        },
        {
          id: 'prize',
          header: t('qy_lot_wheel_prize'),
          cell: (row: QyLotTier) => {
            if (row.prize_type === 'none') {
              return (
                <span className='text-muted-foreground'>
                  {row.name || t('qy_lot_wheel_tier_none')}
                </span>
              )
            }
            return (
              <span className='inline-flex flex-col gap-0.5'>
                <span className='inline-flex flex-wrap items-center gap-1.5'>
                  <span className='break-words'>{row.name}</span>
                  {isQyLotTextPrize(row) && (
                    <Badge variant='outline'>
                      {t('qy_lot_prize_type_text')}
                    </Badge>
                  )}
                </span>
                {/* 文本奖的 `amount_quota` 恒为 0；它的价值全在公开说明里。 */}
                {isQyLotTextPrize(row) && (
                  <span className='text-muted-foreground text-xs break-words whitespace-pre-wrap'>
                    {row.text_desc}
                  </span>
                )}
                {/* 商品奖同样 0 星屑：这一格说的是"哪件商品、什么形态"。
                    商品名与形态是按商品号从商城现读的摘要，不进承诺；进承诺
                    的是 product_no，验证脚本比对的也是它。 */}
                {isQyLotProductPrize(row) && (
                  <span className='inline-flex flex-wrap items-center gap-1.5 text-xs'>
                    <QyMallKindBadge kind={row.product_kind ?? ''} />
                    <span className='break-words'>
                      {row.product_title || row.product_no}
                    </span>
                  </span>
                )}
                {!isQyLotTextPrize(row) && !isQyLotProductPrize(row) && (
                  <QySdAmount amount={row.amount_quota} />
                )}
              </span>
            )
          },
        },
        {
          id: 'win_ppm',
          header: t('qy_lot_win_ppm'),
          cellClassName: 'tabular-nums',
          cell: (row: QyLotTier) =>
            `${(((row.win_ppm ?? 0) / QY_LOT_PPM_DEN) * 100).toFixed(4)}%`,
        },
        ...(props.hideStock === true
          ? []
          : [
              {
                id: 'stock',
                header: t('qy_lot_wheel_stock'),
                cellClassName: 'tabular-nums',
                cell: (row: QyLotTier) => {
                  if (row.prize_type === 'none') return QY_EMPTY_TEXT
                  if (row.stock_left == null) return String(row.count)
                  const replayed = replay?.get(row.tier)
                  return (
                    <span className='inline-flex flex-col'>
                      <span
                        className={
                          row.stock_left <= 0 ? 'text-muted-foreground' : ''
                        }
                      >
                        {t('qy_lot_wheel_stock_value', {
                          left: row.stock_left,
                          total: row.count,
                        })}
                      </span>
                      {replayed != null && (
                        <span
                          className={
                            replayed === row.stock_left
                              ? 'text-muted-foreground text-xs'
                              : 'text-destructive text-xs'
                          }
                        >
                          {t('qy_lot_wheel_stock_replayed', { left: replayed })}
                        </span>
                      )}
                    </span>
                  )
                },
              },
            ]),
      ]}
    />
  )
}
