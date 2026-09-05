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
import { Package, Plus } from 'lucide-react'
import { useState } from 'react'
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
import { qyArray } from '../../lib/array'
import { QyPager } from '../components/qy-pager'
import { QY_PAGE_SIZE } from '../lib/constants'
import { qyMallKindKey, qyMallRemaining } from '../mall/lib/product'
import type { QyMallProductKind } from '../mall/types'
import { formatQyTs } from '../ops/format'
import { qyAdminMallProductsQuery } from './api'
import { QyMallProductDeleteDialog } from './components/product-delete-dialog'
import { QyMallProductFormDialog } from './components/product-form-dialog'
import type { QyMallAdminProduct } from './types'

const KIND_OPTIONS: readonly QyMallProductKind[] = ['code', 'physical', 'plan']

/**
 * 商品管理（第一张标签）。
 *
 * 列表与新建 / 编辑 / 删除三个弹窗都在这里。删除有未完结订单时后端 409
 * `qy_ml_has_open_orders`，文案已登记，弹窗只负责把它 toast 出来。
 */
export function QyMallAdminProductsTab() {
  const { t } = useTranslation()
  const [page, setPage] = useState(1)
  const [kind, setKind] = useState('')
  const [enabled, setEnabled] = useState('')
  const [formOpen, setFormOpen] = useState(false)
  const [editing, setEditing] = useState<QyMallAdminProduct | null>(null)
  const [deleting, setDeleting] = useState<QyMallAdminProduct | null>(null)

  const query = useQuery(
    qyAdminMallProductsQuery({
      page,
      page_size: QY_PAGE_SIZE,
      kind: kind === '' ? undefined : kind,
      enabled: enabled === '' ? undefined : enabled === '1',
    })
  )
  const items = qyArray(query.data?.items)

  const columns: StaticDataTableColumn<QyMallAdminProduct>[] = [
    {
      id: 'title',
      header: t('qy_mladm_col_title'),
      className: staticDataTableClassNames.compactHeaderCell,
      cellClassName: staticDataTableClassNames.compactTopCell,
      cell: (row) => (
        <span className='flex flex-col gap-0.5'>
          <span className='inline-flex flex-wrap items-center gap-1.5'>
            <span className='break-words'>{row.title}</span>
            <Badge variant='outline'>
              {t(qyMallKindKey(row.kind), { defaultValue: row.kind })}
            </Badge>
            {!row.enabled && (
              <Badge variant='secondary'>{t('qy_common_off')}</Badge>
            )}
          </span>
          <span className='text-muted-foreground font-mono text-[11px]'>
            {row.product_no}
          </span>
        </span>
      ),
    },
    {
      id: 'price',
      header: t('qy_ml_price'),
      className: staticDataTableClassNames.compactHeaderCellRight,
      cellClassName: staticDataTableClassNames.compactTopNumericCell,
      cell: (row) => <QySdAmount amount={row.price} />,
    },
    {
      id: 'stock',
      header: t('qy_mladm_col_stock'),
      className: staticDataTableClassNames.compactHeaderCell,
      cellClassName: staticDataTableClassNames.compactTopCell,
      cell: (row) => {
        const remaining = qyMallRemaining(row)
        if (row.kind === 'code') {
          // 兑换码的库存是三态：没发的 / 发出去的 / 撤回的。只写"剩 N"
          // 看不出这件商品发过多少码。
          return (
            <span className='tabular-nums'>
              {t('qy_mladm_code_stock_line', {
                unused: row.code_stock.unused,
                issued: row.code_stock.issued,
                revoked: row.code_stock.revoked,
              })}
            </span>
          )
        }
        return (
          <span className='tabular-nums'>
            {remaining == null
              ? t('qy_common_unlimited')
              : t('qy_ml_remaining_n', { count: remaining })}
            {' · '}
            {t('qy_mladm_stock_sold', { count: row.sold })}
          </span>
        )
      },
    },
    {
      id: 'limit',
      header: t('qy_mladm_per_user_limit'),
      className: staticDataTableClassNames.compactHeaderCell,
      cellClassName: staticDataTableClassNames.compactTopCell,
      cell: (row) =>
        row.per_user_limit > 0
          ? t('qy_ml_remaining_n', { count: row.per_user_limit })
          : t('qy_common_unlimited'),
    },
    {
      id: 'window',
      header: t('qy_mladm_col_sale_window'),
      className: staticDataTableClassNames.compactHeaderCell,
      cellClassName: staticDataTableClassNames.topMutedCell,
      cell: (row) =>
        row.sale_start_at === 0 && row.sale_end_at === 0 ? (
          t('qy_common_unlimited')
        ) : (
          <span className='flex flex-col text-xs tabular-nums'>
            <span>{formatQyTs(row.sale_start_at)}</span>
            <span>{formatQyTs(row.sale_end_at)}</span>
          </span>
        ),
    },
    {
      id: 'sort',
      header: t('qy_mladm_sort_order'),
      className: staticDataTableClassNames.compactHeaderCellRight,
      cellClassName: staticDataTableClassNames.compactTopNumericCell,
      cell: (row) => row.sort_order,
    },
    {
      id: 'actions',
      header: t('qy_common_actions'),
      className: staticDataTableClassNames.actionHeaderCell,
      cellClassName: staticDataTableClassNames.actionCell,
      cell: (row) => (
        <span className='inline-flex items-center gap-1'>
          <Button
            type='button'
            variant='ghost'
            size='sm'
            onClick={() => {
              setEditing(row)
              setFormOpen(true)
            }}
          >
            {t('qy_common_edit')}
          </Button>
          <Button
            type='button'
            variant='ghost'
            size='sm'
            className='text-destructive'
            onClick={() => setDeleting(row)}
          >
            {t('qy_common_delete')}
          </Button>
        </span>
      ),
    },
  ]

  return (
    <>
      <div className='space-y-3'>
        <div className='flex flex-wrap items-center gap-2'>
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
            aria-label={t('qy_mladm_enabled')}
            value={enabled}
            onChange={(event) => {
              setPage(1)
              setEnabled(event.target.value)
            }}
          >
            <NativeSelectOption value=''>
              {t('qy_common_all')}
            </NativeSelectOption>
            <NativeSelectOption value='1'>
              {t('qy_common_on')}
            </NativeSelectOption>
            <NativeSelectOption value='0'>
              {t('qy_common_off')}
            </NativeSelectOption>
          </NativeSelect>
          <Button
            type='button'
            size='sm'
            className='ms-auto'
            onClick={() => {
              setEditing(null)
              setFormOpen(true)
            }}
          >
            <Plus aria-hidden='true' />
            {t('qy_mladm_create')}
          </Button>
        </div>

        <QyPageBoundary
          query={query}
          isEmpty={query.data != null && items.length === 0}
          emptyIcon={Package}
          emptyTitle={t('qy_mladm_products_empty_title')}
          emptyDescription={t('qy_mladm_products_empty_desc')}
        >
          <div className='w-full overflow-x-auto'>
            <StaticDataTable
              columns={columns}
              data={items}
              getRowKey={(row) => row.product_no}
              tableClassName='min-w-[960px]'
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

      <QyMallProductFormDialog
        open={formOpen}
        product={editing}
        onClose={() => setFormOpen(false)}
      />
      <QyMallProductDeleteDialog
        product={deleting}
        onClose={() => setDeleting(null)}
      />
    </>
  )
}
