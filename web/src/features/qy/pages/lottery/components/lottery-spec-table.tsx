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

import { QyAmountText } from '../../../components/qy-amount-text'
import {
  qyLotOptions,
  qyLotTiers,
  type QyLotOption,
  type QyLotSpecItem,
  type QyLotTier,
} from '../types'

/**
 * 奖档（抽奖）或选项盘口（竞猜）。
 *
 * 这份集合在 publish 时进了 `spec_hash`，此后只读 —— 所以它同时是"能赢多少"
 * 与"事后有没有被改过"的展示位。事后加一个选项、改一档奖金，都会让验证脚本
 * 立刻算出不一样的 `spec_hash`。
 */
export function QyLotSpecTable(props: {
  kind: 'draw' | 'guess'
  /** 线上的扁平 spec 数组；分组由 `qyLotTiers` / `qyLotOptions` 完成。 */
  spec: QyLotSpecItem[]
  /** 已公布的获胜选项号；0 = 还没录。仅竞猜有意义。 */
  winOptNo?: number
}) {
  const { t } = useTranslation()

  if (props.kind === 'draw') {
    const tiers = qyLotTiers(props.spec)
    return (
      <StaticDataTable
        data={tiers}
        getRowKey={(row: QyLotTier) => row.tier}
        columns={[
          {
            id: 'tier',
            header: t('qy_lot_tier'),
            cellClassName: 'tabular-nums',
            cell: (row: QyLotTier) => t('qy_lot_tier_no', { no: row.tier }),
          },
          {
            id: 'name',
            header: t('qy_lot_prize_name'),
            cell: (row: QyLotTier) => row.name,
          },
          {
            id: 'amount',
            header: t('qy_lot_prize_amount'),
            cell: (row: QyLotTier) => <QyAmountText quota={row.amount_quota} />,
          },
          {
            id: 'count',
            header: t('qy_lot_prize_count'),
            cellClassName: 'tabular-nums',
            cell: (row: QyLotTier) => row.count,
          },
        ]}
      />
    )
  }

  const options = qyLotOptions(props.spec)
  // 实时盘口是活动详情才有的字段，证据链里没有（它不进承诺）。拿不到时整列
  // 不渲染，而不是显示 0 —— 那是一个错的数，比没有数更糟。
  const hasPool = options.some((option) => option.bet_quota != null)

  return (
    <StaticDataTable
      data={options}
      getRowKey={(row: QyLotOption) => row.opt_no}
      columns={[
        {
          id: 'label',
          header: t('qy_lot_option'),
          cell: (row: QyLotOption) => (
            <span className='inline-flex flex-wrap items-center gap-1.5'>
              <span className='break-words'>{row.label}</span>
              {row.is_catch_all && (
                <Badge variant='outline'>{t('qy_lot_option_catch_all')}</Badge>
              )}
              {props.winOptNo != null && props.winOptNo === row.opt_no && (
                <Badge>{t('qy_lot_option_winner')}</Badge>
              )}
            </span>
          ),
        },
        ...(hasPool
          ? [
              {
                id: 'bet_quota',
                header: t('qy_lot_option_bet_quota'),
                cell: (row: QyLotOption) => (
                  <QyAmountText quota={row.bet_quota ?? 0} />
                ),
              },
              {
                id: 'bet_count',
                header: t('qy_lot_option_bet_count'),
                cellClassName: 'tabular-nums',
                cell: (row: QyLotOption) => row.bet_count ?? 0,
              },
            ]
          : []),
      ]}
    />
  )
}
