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
import { Gift, ReceiptText } from 'lucide-react'
import { useState } from 'react'
import { useTranslation } from 'react-i18next'

import {
  StaticDataTable,
  staticDataTableClassNames,
  type StaticDataTableColumn,
} from '@/components/data-table'
import { Badge } from '@/components/ui/badge'
import { Button } from '@/components/ui/button'
import { Input } from '@/components/ui/input'
import { NativeSelect, NativeSelectOption } from '@/components/ui/native-select'

import { QyPageBoundary } from '../../components/qy-page-boundary'
import { QySdAmount } from '../../components/qy-sd-amount'
import { QyStatusBadge } from '../../components/qy-status-badge'
import { qyArray } from '../../lib/array'
import { QyPager } from '../components/qy-pager'
import { QY_PAGE_SIZE } from '../lib/constants'
import { isQyMallPrizeOrder, qyMallOrderStatusView } from '../mall/lib/order'
import { qyMallKindKey } from '../mall/lib/product'
import type {
  QyMallOrderSource,
  QyMallOrderStatus,
  QyMallProductKind,
} from '../mall/types'
import { formatQyTs } from '../ops/format'
import { qyAdminMallOrdersQuery } from './api'
import { QyMallOrderAddressDialog } from './components/order-address-dialog'
import { QyMallAdminOrderDialog } from './components/order-detail-dialog'
import type { QyMallAdminOrder } from './types'

/** 状态筛选的候选：三种商品的全部取值（契约 §4）。 */
const STATUS_OPTIONS: readonly QyMallOrderStatus[] = [
  'paid',
  'shipped',
  'done',
  'held',
  'failed',
  'cancelled',
  'revoked',
]
const KIND_OPTIONS: readonly QyMallProductKind[] = ['code', 'physical', 'plan']
const SOURCE_OPTIONS: readonly QyMallOrderSource[] = ['lottery', 'mall']

/**
 * 订单（第三张标签）。
 *
 * 默认不筛状态：与提现队列不同，这里三种商品各有各的"待办"（实物待发货 /
 * 套餐待核对 / 兑换码没有待办），用一个默认值盖不住。`held` 的行整行标出来 ——
 * 那是钱扣了、主库动没动不知道的单，越早处理越好。
 *
 * 处理弹窗与地址明文弹窗并列渲染（后者是独立的一层 `QyResponsiveDialog`）。
 */
export function QyMallAdminOrdersTab() {
  const { t } = useTranslation()
  const [page, setPage] = useState(1)
  const [status, setStatus] = useState('')
  const [kind, setKind] = useState('')
  const [source, setSource] = useState('')
  const [userId, setUserId] = useState('')
  const [current, setCurrent] = useState<QyMallAdminOrder | null>(null)
  const [addressNo, setAddressNo] = useState<string | null>(null)

  const userIdNumber = /^\d+$/.test(userId.trim())
    ? Number(userId.trim())
    : undefined
  const query = useQuery(
    qyAdminMallOrdersQuery({
      page,
      page_size: QY_PAGE_SIZE,
      status: status === '' ? undefined : status,
      kind: kind === '' ? undefined : kind,
      source: source === '' ? undefined : source,
      user_id: userIdNumber,
    })
  )
  const items = qyArray(query.data?.items)

  const columns: StaticDataTableColumn<QyMallAdminOrder>[] = [
    {
      id: 'created_at',
      header: t('qy_common_created_at'),
      className: staticDataTableClassNames.compactHeaderCell,
      cellClassName: staticDataTableClassNames.compactMutedCell,
      cell: (row) => formatQyTs(row.created_at),
    },
    {
      id: 'user',
      header: t('qy_common_user'),
      className: staticDataTableClassNames.compactHeaderCell,
      cellClassName: staticDataTableClassNames.compactCell,
      cell: (row) => `#${row.user_id} ${row.username}`,
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
          {/* 抽奖所得的单与兑换的单并排：它没有扣款流水、不能退款、
              实物的地址由中奖者事后补填 —— 处理方式不同，行上就要标出来。 */}
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
        <Button
          type='button'
          variant='ghost'
          size='sm'
          onClick={() => setCurrent(row)}
        >
          {t('qy_mladm_open')}
        </Button>
      ),
    },
  ]

  return (
    <>
      <div className='space-y-3'>
        <div className='flex flex-wrap items-center gap-2'>
          <NativeSelect
            size='sm'
            aria-label={t('qy_common_status')}
            value={status}
            onChange={(event) => {
              setPage(1)
              setStatus(event.target.value)
            }}
          >
            <NativeSelectOption value=''>
              {t('qy_common_all')}
            </NativeSelectOption>
            {STATUS_OPTIONS.map((value) => (
              <NativeSelectOption key={value} value={value}>
                {t(`qy_ml_st_${value}`)}
              </NativeSelectOption>
            ))}
          </NativeSelect>
          <NativeSelect
            size='sm'
            aria-label={t('qy_ml_kind')}
            value={kind}
            onChange={(event) => {
              setPage(1)
              setKind(event.target.value)
            }}
          >
            <NativeSelectOption value=''>
              {t('qy_common_all')}
            </NativeSelectOption>
            {KIND_OPTIONS.map((value) => (
              <NativeSelectOption key={value} value={value}>
                {t(qyMallKindKey(value))}
              </NativeSelectOption>
            ))}
          </NativeSelect>
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
          <Input
            className='h-8 w-40'
            inputMode='numeric'
            value={userId}
            placeholder={t('qy_mladm_user_id_ph')}
            aria-label={t('qy_common_user')}
            onChange={(event) => {
              setPage(1)
              setUserId(event.target.value)
            }}
          />
        </div>

        <QyPageBoundary
          query={query}
          isEmpty={query.data != null && items.length === 0}
          emptyIcon={ReceiptText}
          emptyTitle={t('qy_mladm_orders_empty_title')}
          emptyDescription={t('qy_mladm_orders_empty_desc')}
        >
          <div className='w-full overflow-x-auto'>
            <StaticDataTable
              columns={columns}
              data={items}
              getRowKey={(row) => row.order_no}
              getRowClassName={(row) =>
                row.status === 'held' ? 'bg-destructive/5' : undefined
              }
              tableClassName='min-w-[900px]'
            />
          </div>
          <QyPager
            page={page}
            pageSize={QY_PAGE_SIZE}
            total={query.data?.total ?? 0}
            disabled={query.isFetching}
            onPageChange={setPage}
          />
        </QyPageBoundary>
      </div>

      <QyMallAdminOrderDialog
        order={current}
        onClose={() => setCurrent(null)}
        onRevealAddress={(orderNo) => {
          setCurrent(null)
          setAddressNo(orderNo)
        }}
      />
      <QyMallOrderAddressDialog
        orderNo={addressNo}
        onClose={() => setAddressNo(null)}
      />
    </>
  )
}
