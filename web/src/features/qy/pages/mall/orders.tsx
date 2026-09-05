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
import { useLocation } from '@tanstack/react-router'
import { Gift, KeyRound, MapPin, ReceiptText } from 'lucide-react'
import { useEffect, useState } from 'react'
import { useTranslation } from 'react-i18next'

import {
  StaticDataTable,
  staticDataTableClassNames,
  type StaticDataTableColumn,
} from '@/components/data-table'
import { Badge } from '@/components/ui/badge'
import { Button } from '@/components/ui/button'
import { NativeSelect, NativeSelectOption } from '@/components/ui/native-select'

import { QyPageBoundary } from '../../components/qy-page-boundary'
import { QySdAmount } from '../../components/qy-sd-amount'
import { QyStatusBadge } from '../../components/qy-status-badge'
import { qyArray } from '../../lib/array'
import { QyPager } from '../components/qy-pager'
import { QY_PAGE_SIZE } from '../lib/constants'
import { formatQyTs } from '../ops/format'
import { qyMallOrdersQuery } from './api'
import { QyMallCodeRevealDialog } from './components/mall-code-reveal-dialog'
import { QyMallOrderDetailDialog } from './components/mall-order-detail-dialog'
import { isQyMallPrizeOrder, qyMallOrderStatusView } from './lib/order'
import { qyMallKindKey } from './lib/product'
import type { QyMallOrder, QyMallOrderSource } from './types'

/** 来源筛选：全部 / 抽奖所得 / 星屑兑换。 */
const SOURCE_OPTIONS: readonly QyMallOrderSource[] = ['lottery', 'mall']

/**
 * 我的订单（「星屑商城」选择夹的第二张标签）。
 *
 * 兑换码那一列的「查看兑换码」直接摆在行上而不是藏在详情里：这是用户来这一页
 * 的头号理由。它打开的是一个独立的验密弹窗，与详情弹窗并列渲染（详情里那颗
 * 同名按钮也走这里）—— 两层 `QyResponsiveDialog` 叠开不可靠。
 *
 * ## 抽奖所得的单
 *
 * 转盘 / 抽奖中的商品奖会落成一张 `source='lottery'`、价格 0 的单。它在这张表里
 * 与兑换的单并排，所以必须自己带一枚「抽奖所得」徽标，价格那一格写「奖品」而不是
 * `0 星屑` —— 后者读起来像"免费商品"，用户会去商城里再找一次。实物奖品单还没
 * 填地址时行上直接给「填写地址」，那是唯一卡住这张单的一步。
 *
 * 从中奖结果屏点「去查看」过来时（history state 带着单号，见 `lib/order-focus.ts`），
 * 那一单直接打开详情并在表里高亮；这是一次性的，刷新后不再高亮。
 */
export function QyMallOrdersBody() {
  const { t } = useTranslation()
  const [page, setPage] = useState(1)
  const [source, setSource] = useState('')
  const [detailNo, setDetailNo] = useState<string | null>(null)
  const [revealNo, setRevealNo] = useState<string | null>(null)
  const focusNo = useLocation({
    select: (location) => location.state.qyMallFocusOrderNo ?? '',
  })
  useEffect(() => {
    if (focusNo !== '') setDetailNo(focusNo)
  }, [focusNo])

  const query = useQuery(
    qyMallOrdersQuery({
      page,
      page_size: QY_PAGE_SIZE,
      source: source === '' ? undefined : source,
    })
  )
  const items = qyArray(query.data?.items)

  const columns: StaticDataTableColumn<QyMallOrder>[] = [
    {
      id: 'created_at',
      header: t('qy_common_created_at'),
      className: staticDataTableClassNames.compactHeaderCell,
      cellClassName: staticDataTableClassNames.compactMutedCell,
      cell: (row) => formatQyTs(row.created_at),
    },
    {
      id: 'title',
      header: t('qy_ml_product'),
      className: staticDataTableClassNames.compactHeaderCell,
      cellClassName: staticDataTableClassNames.compactCell,
      cell: (row) => (
        <span className='inline-flex flex-wrap items-center gap-1.5'>
          <span className='break-words'>{row.title}</span>
          <Badge variant='outline'>
            {t(qyMallKindKey(row.kind), { defaultValue: row.kind })}
          </Badge>
          {isQyMallPrizeOrder(row) && (
            <Badge variant='secondary' className='gap-1'>
              <Gift aria-hidden='true' className='size-3' />
              {t('qy_ml_source_lottery')}
            </Badge>
          )}
        </span>
      ),
    },
    {
      id: 'order_no',
      header: t('qy_common_order_no'),
      className: staticDataTableClassNames.compactHeaderCell,
      cellClassName: staticDataTableClassNames.compactMutedCodeCell,
      cell: (row) => <span className='text-xs'>{row.order_no}</span>,
    },
    {
      id: 'price',
      header: t('qy_ml_price'),
      className: staticDataTableClassNames.compactHeaderCellRight,
      cellClassName: staticDataTableClassNames.compactNumericCell,
      // 奖品单价格恒 0：写「奖品」而不是 `0 星屑`，后者读起来像"免费商品"。
      cell: (row) =>
        isQyMallPrizeOrder(row) ? (
          <span className='text-muted-foreground'>
            {t('qy_ml_price_prize')}
          </span>
        ) : (
          <QySdAmount amount={row.price} />
        ),
    },
    {
      id: 'status',
      header: t('qy_common_status'),
      className: staticDataTableClassNames.compactHeaderCell,
      cellClassName: staticDataTableClassNames.compactCell,
      cell: (row) => {
        const view = qyMallOrderStatusView(row.status, row.kind, row)
        return (
          <QyStatusBadge
            status={view.status}
            label={view.labelKey == null ? undefined : t(view.labelKey)}
          />
        )
      },
    },
    {
      id: 'actions',
      header: t('qy_common_actions'),
      className: staticDataTableClassNames.actionHeaderCell,
      cellClassName: staticDataTableClassNames.actionCell,
      cell: (row) => (
        <span className='inline-flex items-center gap-1'>
          {row.address_missing && (
            <Button
              type='button'
              variant='outline'
              size='sm'
              onClick={() => setDetailNo(row.order_no)}
            >
              <MapPin aria-hidden='true' />
              {t('qy_ml_prize_address_btn')}
            </Button>
          )}
          {row.kind === 'code' && row.status === 'done' && (
            <Button
              type='button'
              variant='outline'
              size='sm'
              onClick={() => setRevealNo(row.order_no)}
            >
              <KeyRound aria-hidden='true' />
              {t('qy_ml_code_reveal_btn')}
            </Button>
          )}
          <Button
            type='button'
            variant='ghost'
            size='sm'
            onClick={() => setDetailNo(row.order_no)}
          >
            {t('qy_common_detail')}
          </Button>
        </span>
      ),
    },
  ]

  return (
    <>
      <QyPageBoundary
        query={query}
        isEmpty={query.data != null && items.length === 0}
        emptyIcon={ReceiptText}
        emptyTitle={t('qy_ml_orders_empty_title')}
        emptyDescription={t('qy_ml_orders_empty_desc')}
      >
        <div className='space-y-3'>
          <div className='flex flex-wrap items-center gap-2'>
            <NativeSelect
              size='sm'
              aria-label={t('qy_ml_source')}
              value={source}
              onChange={(event) => {
                setPage(1)
                setSource(event.target.value)
              }}
            >
              <NativeSelectOption value=''>
                {t('qy_common_all')}
              </NativeSelectOption>
              {SOURCE_OPTIONS.map((value) => (
                <NativeSelectOption key={value} value={value}>
                  {t(`qy_ml_source_${value}`)}
                </NativeSelectOption>
              ))}
            </NativeSelect>
          </div>
          <div className='w-full overflow-x-auto'>
            <StaticDataTable
              columns={columns}
              data={items}
              getRowKey={(row) => row.order_no}
              // 从中奖结果屏跳过来的那一单：一圈主色描边，零投影（design-14 §4）。
              getRowClassName={(row) =>
                row.order_no === focusNo
                  ? 'bg-primary/5 ring-1 ring-primary/40 ring-inset'
                  : undefined
              }
              tableClassName='min-w-[760px]'
            />
          </div>
          <QyPager
            page={page}
            pageSize={QY_PAGE_SIZE}
            total={query.data?.total ?? 0}
            disabled={query.isFetching}
            onPageChange={setPage}
          />
        </div>
      </QyPageBoundary>

      <QyMallOrderDetailDialog
        orderNo={detailNo}
        onClose={() => setDetailNo(null)}
        onRevealCode={(orderNo) => {
          setDetailNo(null)
          setRevealNo(orderNo)
        }}
      />
      <QyMallCodeRevealDialog
        orderNo={revealNo}
        onClose={() => setRevealNo(null)}
      />
    </>
  )
}
