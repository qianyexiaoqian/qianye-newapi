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
import { useQuery } from '@tanstack/react-query'
import { Link } from '@tanstack/react-router'
import { ExternalLink, RotateCw } from 'lucide-react'
import { useState, type ReactNode } from 'react'
import { useTranslation } from 'react-i18next'

import { StaticDataTable } from '@/components/data-table'
import { Badge } from '@/components/ui/badge'
import { Button } from '@/components/ui/button'
import {
  Card,
  CardContent,
  CardDescription,
  CardHeader,
  CardTitle,
} from '@/components/ui/card'

import { QyPageBoundary } from '../../../components/qy-page-boundary'
import { QySdAmount } from '../../../components/qy-sd-amount'
import { qyArray } from '../../../lib/array'
import { QyPager } from '../../components/qy-pager'
import { qyLotTiers, type QyLotSpecItem } from '../../lottery/types'
import { QyMallKindBadge } from '../../mall/components/mall-kind-badge'
import { qyMallOrderLink } from '../../mall/lib/order-focus'
import { QY_EMPTY_TEXT, formatQyTs } from '../../ops/format'
import { QY_WHEEL_SPINS_PAGE_SIZE, qyWheelMySpinsQuery } from '../api'
import { qyWheelOutcomeOf, qyWheelTierName } from '../lib/spin'
import type { QyWheelMySpin } from '../types'

/**
 * 「我的转动」：这一场里我的每一转，最近的在前。
 *
 * 回执弹窗关掉就没了，这一份才是留得住的那一份：序号、种子与链环是用户事后
 * 拿证据链复算自己那一转的全部输入。链环不打码 —— 它是凭据，不是身份。
 */
export function QyWheelMySpins(props: {
  actNo: string
  spec: QyLotSpecItem[]
}) {
  const { t } = useTranslation()
  const [page, setPage] = useState(1)
  const query = useQuery(
    qyWheelMySpinsQuery(props.actNo, {
      p: page,
      page_size: QY_WHEEL_SPINS_PAGE_SIZE,
    })
  )
  const items = qyArray(query.data?.items)

  return (
    <Card data-card-hover='false'>
      <CardHeader>
        <CardTitle className='flex items-center gap-2'>
          <RotateCw aria-hidden='true' className='size-4' />
          {t('qy_lot_wheel_my_spins_title')}
        </CardTitle>
        <CardDescription>{t('qy_lot_wheel_my_spins_desc')}</CardDescription>
      </CardHeader>
      <CardContent>
        <QyPageBoundary
          query={query}
          isEmpty={query.data != null && items.length === 0}
          emptyIcon={RotateCw}
          emptyTitle={t('qy_lot_wheel_my_spins_empty')}
        >
          <StaticDataTable
            data={items}
            getRowKey={(row: QyWheelMySpin) => row.entry_no}
            columns={[
              {
                id: 'seq',
                header: t('qy_lot_wheel_seq'),
                cellClassName: 'tabular-nums',
                cell: (row: QyWheelMySpin) => `#${row.seq}`,
              },
              {
                id: 'at',
                header: t('qy_lot_wheel_spun_at'),
                cellClassName: 'tabular-nums',
                cell: (row: QyWheelMySpin) => formatQyTs(row.created_at),
              },
              {
                id: 'outcome',
                header: t('qy_lot_wheel_outcome'),
                cell: (row: QyWheelMySpin) => (
                  <QyWheelSpinOutcomeCell row={row} spec={props.spec} />
                ),
              },
              {
                id: 'seed',
                header: t('qy_lot_wheel_seed_label'),
                cell: (row: QyWheelMySpin) => (
                  <span className='font-mono text-xs break-all'>
                    {row.client_seed === ''
                      ? t('qy_lot_wheel_seed_empty')
                      : row.client_seed}
                  </span>
                ),
              },
              {
                id: 'chain',
                header: t('qy_lot_wheel_chain_hash'),
                cell: (row: QyWheelMySpin) => (
                  <span className='font-mono text-xs break-all'>
                    {row.chain_hash}
                  </span>
                ),
              },
            ]}
          />
          <QyPager
            page={page}
            pageSize={QY_WHEEL_SPINS_PAGE_SIZE}
            total={query.data?.total ?? 0}
            onPageChange={setPage}
            disabled={query.isFetching}
          />
        </QyPageBoundary>
      </CardContent>
    </Card>
  )
}

/** 一转的结局那一格：中了哪档 + 拿到什么 / 摇中却已发完 / 谢谢参与。 */
function QyWheelSpinOutcomeCell(props: {
  row: QyWheelMySpin
  spec: QyLotSpecItem[]
}) {
  const { t } = useTranslation()
  const { row } = props
  const outcome = qyWheelOutcomeOf(row)
  if (outcome === 'none') {
    return (
      <span className='text-muted-foreground'>
        {t('qy_lot_wheel_tier_none')}
      </span>
    )
  }
  if (outcome === 'exhausted') {
    return (
      <span className='text-muted-foreground'>
        {t('qy_lot_wheel_row_exhausted', {
          name: qyWheelTierName(props.spec, row.exhausted_tier),
        })}
      </span>
    )
  }
  let prize: ReactNode = QY_EMPTY_TEXT
  if (row.prize_type === 'text') {
    prize = <Badge variant='outline'>{t('qy_lot_prize_type_text')}</Badge>
  } else if (row.prize_type === 'product') {
    // 商品奖：形态图标 + 商品名（按 tier 从公示奖档取摘要）+ 去看那张订单。
    // 回执弹窗关掉之后，这一格是用户找回"我中的那件东西在哪"的唯一入口。
    const tier = qyLotTiers(props.spec).find(
      (item) => item.tier === row.result_tier
    )
    prize = (
      <>
        <QyMallKindBadge kind={tier?.product_kind ?? ''} />
        <span className='break-words'>
          {tier?.product_title || row.product_no || ''}
        </span>
        {(row.mall_order_no ?? '') !== '' && (
          <Button
            type='button'
            variant='outline'
            size='sm'
            render={<Link {...qyMallOrderLink(row.mall_order_no ?? '')} />}
          >
            <ExternalLink aria-hidden='true' />
            {t('qy_lot_wheel_won_product_view')}
          </Button>
        )}
      </>
    )
  } else if (row.amount > 0) {
    prize = <QySdAmount amount={row.amount} />
  }
  return (
    <span className='inline-flex flex-wrap items-center gap-1.5'>
      <span className='break-words'>
        {qyWheelTierName(props.spec, row.result_tier) ||
          t('qy_lot_tier_no', { no: row.result_tier })}
      </span>
      {prize}
    </span>
  )
}
