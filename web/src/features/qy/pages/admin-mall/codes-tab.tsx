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
import { Plus, Ticket } from 'lucide-react'
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
import { qyErrorMessage } from '../../lib/api'
import { qyArray } from '../../lib/array'
import { QyPager } from '../components/qy-pager'
import { QyStatGrid, type QyStatItem } from '../components/qy-stat-grid'
import { QY_PAGE_SIZE } from '../lib/constants'
import { formatQyTs } from '../ops/format'
import { qyAdminMallCodesQuery, qyAdminMallProductsQuery } from './api'
import { QyMallCodeDeleteDialog } from './components/code-delete-dialog'
import { QyMallCodeTakeDialog } from './components/code-take-dialog'
import { QyMallCodeUploadDialog } from './components/code-upload-dialog'
import { qyMallCodeStatusKey } from './lib/codes'
import type { QyMallAdminCode, QyMallCodeStatus } from './types'

const STATUS_OPTIONS: readonly QyMallCodeStatus[] = [
  'unused',
  'issued',
  'taken',
  'revoked',
]

/**
 * 码库存（第二张标签）：选一件 `code` 商品 → **逐枚列出**这件商品的码。
 *
 * ## 为什么是列表而不是一个粘贴框
 *
 * 上一版这一屏只有"选商品 + 粘贴上传"，运营看得见的只有三个计数。库里到底有
 * 哪几枚、哪一枚发给了谁、哪一枚是上周传错的，一概看不到，于是"删掉那一条传错
 * 的码"这件事在界面上根本不存在。列表是这些动作的落点。
 *
 * ## 这一屏永远不显示码本身
 *
 * 列表接口不回明文（后端 `codeStockView` 挑字段），列上也没有任何"点一下展开"
 * 的省略形态。明文只有「提卡」一条出口：逐枚、验密、写审计。一次越权 bug 在
 * 列表上就是全量泄漏，在提卡上只是一枚。
 *
 * ## 三个动作都在弹窗里
 *
 * 添加库存（粘贴上传）、提卡（验密后拿明文）、删码（不可逆确认）各自一个弹窗。
 * 它们要么持有明文、要么不可逆，都不适合摊在列表页上被误触。
 */
export function QyMallAdminCodesTab() {
  const { t } = useTranslation()
  const [productNo, setProductNo] = useState('')
  const [status, setStatus] = useState('')
  const [page, setPage] = useState(1)
  const [uploadOpen, setUploadOpen] = useState(false)
  const [taking, setTaking] = useState<QyMallAdminCode | null>(null)
  const [deleting, setDeleting] = useState<QyMallAdminCode | null>(null)

  // 只拉 code 类。上限 100 是后端分页硬顶；一个站点不会有一百件兑换码商品。
  const productsQuery = useQuery(
    qyAdminMallProductsQuery({ page: 1, page_size: 100, kind: 'code' })
  )
  const products = qyArray(productsQuery.data?.items)
  const selected = products.find((row) => row.product_no === productNo) ?? null

  const codesQuery = useQuery(
    qyAdminMallCodesQuery(productNo, {
      page,
      page_size: QY_PAGE_SIZE,
      status: status === '' ? undefined : (status as QyMallCodeStatus),
    })
  )
  const codes = qyArray(codesQuery.data?.items)

  const stats: QyStatItem[] =
    selected == null
      ? []
      : [
          {
            key: 'unused',
            label: t('qy_mladm_stock_unused'),
            value: selected.code_stock.unused,
            emphasis: true,
          },
          {
            key: 'issued',
            label: t('qy_mladm_stock_issued'),
            value: selected.code_stock.issued,
          },
          {
            key: 'taken',
            label: t('qy_mladm_stock_taken'),
            value: selected.code_stock.taken,
          },
          {
            key: 'revoked',
            label: t('qy_mladm_stock_revoked'),
            value: selected.code_stock.revoked,
          },
        ]

  const columns: StaticDataTableColumn<QyMallAdminCode>[] = [
    {
      id: 'id',
      header: t('qy_mladm_code_id'),
      className: staticDataTableClassNames.compactHeaderCell,
      cellClassName: staticDataTableClassNames.compactTopCell,
      cell: (row) => (
        <span className='font-mono text-xs tabular-nums'>#{row.id}</span>
      ),
    },
    {
      id: 'status',
      header: t('qy_common_status'),
      className: staticDataTableClassNames.compactHeaderCell,
      cellClassName: staticDataTableClassNames.compactTopCell,
      cell: (row) => (
        <Badge variant={row.status === 'unused' ? 'outline' : 'secondary'}>
          {t(qyMallCodeStatusKey(row.status), { defaultValue: row.status })}
        </Badge>
      ),
    },
    {
      id: 'created_at',
      header: t('qy_mladm_code_created_at'),
      className: staticDataTableClassNames.compactHeaderCell,
      cellClassName: staticDataTableClassNames.topMutedCell,
      cell: (row) => (
        <span className='text-xs tabular-nums'>
          {formatQyTs(row.created_at)}
        </span>
      ),
    },
    {
      id: 'whereabouts',
      header: t('qy_mladm_code_whereabouts'),
      className: staticDataTableClassNames.compactHeaderCell,
      cellClassName: staticDataTableClassNames.compactTopCell,
      // 「这枚码去哪了」：发出去的指向订单号，提走的指向那位管理员，
      // 没发出去的一律是一个破折号 —— 三种去向共用一列，看的人只扫一列。
      cell: (row) => {
        if (row.order_no !== '') {
          return (
            <span className='flex flex-col gap-0.5'>
              <span className='font-mono text-[11px] break-all'>
                {row.order_no}
              </span>
              <span className='text-muted-foreground text-[11px] tabular-nums'>
                {formatQyTs(row.issued_at)}
              </span>
            </span>
          )
        }
        if (row.status === 'taken') {
          return (
            <span className='flex flex-col gap-0.5'>
              <span className='break-words'>
                {row.taken_name === ''
                  ? t('qy_mladm_code_taken_by_unknown', { id: row.taken_by })
                  : row.taken_name}
              </span>
              <span className='text-muted-foreground text-[11px] tabular-nums'>
                {formatQyTs(row.taken_at)}
              </span>
            </span>
          )
        }
        return <span className='text-muted-foreground'>—</span>
      },
    },
    {
      id: 'actions',
      header: t('qy_common_actions'),
      className: staticDataTableClassNames.actionHeaderCell,
      cellClassName: staticDataTableClassNames.actionCell,
      // 两个动作都只对 unused 开放：提卡要的是还没发出去的码，删码删的是库存
      // 而不是证据。对其余状态整块留空，而不是画一个禁用的按钮 —— 后者会让人
      // 反复去点，然后来问"为什么点不动"。
      cell: (row) =>
        row.status === 'unused' ? (
          <span className='inline-flex items-center gap-1'>
            <Button
              type='button'
              variant='ghost'
              size='sm'
              onClick={() => setTaking(row)}
            >
              {t('qy_mladm_code_take')}
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
        ) : (
          <span className='text-muted-foreground'>—</span>
        ),
    },
  ]

  return (
    <>
      <QyPageBoundary
        query={productsQuery}
        isEmpty={productsQuery.data != null && products.length === 0}
        emptyIcon={Ticket}
        emptyTitle={t('qy_mladm_codes_no_product_title')}
        emptyDescription={t('qy_mladm_codes_no_product_desc')}
      >
        <div className='space-y-3'>
          <div className='flex flex-wrap items-center gap-2'>
            <NativeSelect
              size='sm'
              className='w-full sm:w-72'
              aria-label={t('qy_mladm_codes_product')}
              value={productNo}
              onChange={(event) => {
                setPage(1)
                setProductNo(event.target.value)
              }}
            >
              <NativeSelectOption value=''>
                {t('qy_mladm_codes_product_pick')}
              </NativeSelectOption>
              {products.map((row) => (
                <NativeSelectOption key={row.product_no} value={row.product_no}>
                  {row.title}
                  {row.enabled ? '' : ` (${t('qy_common_off')})`}
                </NativeSelectOption>
              ))}
            </NativeSelect>
            <NativeSelect
              size='sm'
              aria-label={t('qy_common_status')}
              disabled={selected == null}
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
                  {t(qyMallCodeStatusKey(value))}
                </NativeSelectOption>
              ))}
            </NativeSelect>
            <Button
              type='button'
              size='sm'
              className='ms-auto'
              disabled={selected == null}
              onClick={() => setUploadOpen(true)}
            >
              <Plus aria-hidden='true' />
              {t('qy_mladm_codes_add')}
            </Button>
          </div>

          {selected == null ? (
            <p className='text-muted-foreground rounded-lg border border-dashed p-6 text-center text-sm'>
              {t('qy_mladm_codes_pick_first')}
            </p>
          ) : (
            <>
              <QyStatGrid items={stats} />
              <QyPageBoundary
                query={codesQuery}
                isEmpty={codesQuery.data != null && codes.length === 0}
                emptyIcon={Ticket}
                emptyTitle={t('qy_mladm_codes_empty_title')}
                emptyDescription={t('qy_mladm_codes_empty_desc')}
              >
                <div className='w-full overflow-x-auto'>
                  <StaticDataTable
                    columns={columns}
                    data={codes}
                    getRowKey={(row) => row.id}
                    tableClassName='min-w-[720px]'
                  />
                </div>
                <QyPager
                  page={page}
                  pageSize={QY_PAGE_SIZE}
                  total={codesQuery.data?.total ?? 0}
                  disabled={codesQuery.isFetching}
                  onPageChange={setPage}
                />
              </QyPageBoundary>
            </>
          )}

          {codesQuery.isError && selected != null && (
            <p className='text-destructive text-xs'>
              {qyErrorMessage(codesQuery.error, t)}
            </p>
          )}
        </div>
      </QyPageBoundary>

      <QyMallCodeUploadDialog
        open={uploadOpen && selected != null}
        productNo={productNo}
        productTitle={selected?.title ?? ''}
        onClose={() => setUploadOpen(false)}
      />
      <QyMallCodeTakeDialog
        productNo={productNo}
        code={taking}
        onClose={() => setTaking(null)}
      />
      <QyMallCodeDeleteDialog
        productNo={productNo}
        code={deleting}
        onClose={() => setDeleting(null)}
      />
    </>
  )
}
