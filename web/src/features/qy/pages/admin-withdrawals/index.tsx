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
import { Banknote, TriangleAlert } from 'lucide-react'
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
import { formatTimestampToDate } from '@/lib/format'
import { cn } from '@/lib/utils'

import { QyAmountText } from '../../components/qy-amount-text'
import { QyPageBoundary } from '../../components/qy-page-boundary'
import { QySectionPageLayout } from '../../components/qy-section-page-layout'
import { QyStatusBadge } from '../../components/qy-status-badge'
import { QyFiatText } from '../components/qy-fiat-text'
import { QyPager } from '../components/qy-pager'
import { QyStatGrid, type QyStatItem } from '../components/qy-stat-grid'
import { QY_PAGE_SIZE } from '../lib/constants'
import type { QyAdminWithdrawal } from '../withdraw/types'
import { qyAdminWithdrawStatsQuery, qyAdminWithdrawalsQuery } from './api'
import { RevealPayeeDialog } from './components/reveal-payee-dialog'
import { ReviewDialog } from './components/review-dialog'

const STATUS_OPTIONS = [
  'pending',
  'approved',
  'paying',
  'paid',
  'rejected',
  'cancelled',
  'failed',
] as const

/**
 * 提现审核队列。
 *
 * 默认只看待审单并按申请时间正序（后端默认 `id asc`）：审核是先进先出的工作，
 * 倒序会让最老的单被新单一直往下压，而"最老"恰恰等于"最接近超时"。
 *
 * 超 SLA 的行整行标红，判定用后端下发的 `sla_breached` 而不是前端比时间 ——
 * 管理员机器的时钟偏差会让红色标记在两台电脑上不一致。
 */
export function QyAdminWithdrawals() {
  const { t } = useTranslation()
  const [page, setPage] = useState(1)
  const [status, setStatus] = useState('pending')
  const [method, setMethod] = useState('')
  const [keyword, setKeyword] = useState('')
  const [riskOnly, setRiskOnly] = useState(false)
  const [holdOnly, setHoldOnly] = useState(false)
  const [reviewId, setReviewId] = useState<number | null>(null)
  const [revealId, setRevealId] = useState<number | null>(null)

  const statsQuery = useQuery(qyAdminWithdrawStatsQuery())
  const query = useQuery(
    qyAdminWithdrawalsQuery({
      p: page,
      page_size: QY_PAGE_SIZE,
      status,
      method,
      // 单号是精确匹配，纯数字则当成 user_id —— 两个查询条件共用一个输入框，
      // 因为管理员手上拿到的线索要么是单号要么是用户 ID，不会是别的。
      withdraw_no: /^\d+$/.test(keyword.trim()) ? '' : keyword.trim(),
      user_id: /^\d+$/.test(keyword.trim()) ? keyword.trim() : '',
      risk_only: riskOnly ? 'true' : '',
      reconcile: holdOnly ? 'hold' : '',
    })
  )
  const items = query.data?.items ?? []
  const stats = statsQuery.data

  const statItems: QyStatItem[] =
    stats == null
      ? []
      : [
          {
            key: 'pending',
            label: t('qy_wd_a_stat_pending'),
            value: bucketCount(stats.buckets, 'pending'),
            emphasis: true,
          },
          {
            key: 'approved',
            label: t('qy_wd_a_stat_approved'),
            value: bucketCount(stats.buckets, 'approved'),
          },
          {
            key: 'sla',
            label: t('qy_wd_a_stat_sla'),
            value: stats.sla_breached,
            hint:
              stats.sla_breached > 0 ? t('qy_wd_a_stat_sla_hint') : undefined,
          },
          {
            key: 'hold',
            label: t('qy_wd_a_stat_hold'),
            value: stats.reconcile_hold,
            hint:
              stats.reconcile_hold > 0
                ? t('qy_wd_a_stat_hold_hint')
                : undefined,
          },
        ]

  const columns: StaticDataTableColumn<QyAdminWithdrawal>[] = [
    {
      id: 'created_at',
      header: t('qy_common_created_at'),
      className: staticDataTableClassNames.compactHeaderCell,
      cellClassName: staticDataTableClassNames.compactMutedCell,
      cell: (row) => (
        <span className='inline-flex items-center gap-1.5'>
          {formatTimestampToDate(row.created_at)}
          {row.sla_breached && (
            <TriangleAlert
              className='text-destructive size-3.5'
              aria-label={t('qy_wd_a_sla_title')}
            />
          )}
        </span>
      ),
    },
    {
      id: 'user',
      header: t('qy_common_user'),
      className: staticDataTableClassNames.compactHeaderCell,
      cellClassName: staticDataTableClassNames.compactCell,
      cell: (row) => `#${row.user_id} ${row.username}`,
    },
    {
      id: 'method',
      header: t('qy_wd_method'),
      className: staticDataTableClassNames.compactHeaderCell,
      cellClassName: staticDataTableClassNames.compactCell,
      cell: (row) => t(`qy_wd_m_${row.method}`),
    },
    {
      id: 'quota',
      header: t('qy_wd_amount'),
      className: staticDataTableClassNames.compactHeaderCellRight,
      cellClassName: staticDataTableClassNames.compactNumericCell,
      cell: (row) => <QyAmountText quota={row.quota} />,
    },
    {
      id: 'net',
      header: t('qy_wd_net'),
      className: staticDataTableClassNames.compactHeaderCellRight,
      cellClassName: staticDataTableClassNames.compactNumericCell,
      cell: (row) =>
        row.method === 'fiat' ? (
          <QyFiatText amount={row.net_amount} currency={row.currency} />
        ) : (
          '-'
        ),
    },
    {
      id: 'payee',
      header: t('qy_wd_payee'),
      className: staticDataTableClassNames.compactHeaderCell,
      cellClassName: staticDataTableClassNames.compactMutedCodeCell,
      // 队列里永远只显示脱敏值。明文要走带审计的专用弹窗。
      cell: (row) => (row.payee_masked === '' ? '-' : row.payee_masked),
    },
    {
      id: 'status',
      header: t('qy_common_status'),
      className: staticDataTableClassNames.compactHeaderCell,
      cellClassName: staticDataTableClassNames.compactCell,
      cell: (row) => (
        <span className='inline-flex items-center gap-1.5'>
          <QyStatusBadge status={row.status} />
          {row.reconcile_state === 'hold' && (
            <Badge variant='destructive'>{t('qy_wd_a_hold_badge')}</Badge>
          )}
          {row.risk_flags !== '' && (
            <Badge variant='outline'>{t('qy_wd_a_risk_badge')}</Badge>
          )}
        </span>
      ),
    },
    {
      id: 'actions',
      header: t('qy_common_actions'),
      className: staticDataTableClassNames.actionHeaderCell,
      cellClassName: staticDataTableClassNames.actionCell,
      cell: (row) => (
        <Button variant='ghost' size='sm' onClick={() => setReviewId(row.id)}>
          {t('qy_wd_a_open')}
        </Button>
      ),
    },
  ]

  const resetPage = () => setPage(1)

  return (
    <QySectionPageLayout>
      <QySectionPageLayout.Title>
        {t('qy_nav_a_withdrawals')}
      </QySectionPageLayout.Title>
      <QySectionPageLayout.Content>
        <div className='space-y-3'>
          <QyStatGrid items={statItems} />

          <div className='flex flex-wrap items-center gap-2'>
            <NativeSelect
              size='sm'
              aria-label={t('qy_common_status')}
              value={status}
              onChange={(event) => {
                resetPage()
                setStatus(event.target.value)
              }}
            >
              <NativeSelectOption value=''>
                {t('qy_common_all')}
              </NativeSelectOption>
              {STATUS_OPTIONS.map((value) => (
                <NativeSelectOption key={value} value={value}>
                  {t(`qy_common_st_${value}`)}
                </NativeSelectOption>
              ))}
            </NativeSelect>

            <NativeSelect
              size='sm'
              aria-label={t('qy_wd_method')}
              value={method}
              onChange={(event) => {
                resetPage()
                setMethod(event.target.value)
              }}
            >
              <NativeSelectOption value=''>
                {t('qy_wd_filter_all_methods')}
              </NativeSelectOption>
              <NativeSelectOption value='quota'>
                {t('qy_wd_m_quota')}
              </NativeSelectOption>
              <NativeSelectOption value='fiat'>
                {t('qy_wd_m_fiat')}
              </NativeSelectOption>
            </NativeSelect>

            <Input
              className='h-8 w-56'
              value={keyword}
              placeholder={t('qy_wd_a_search_ph')}
              onChange={(event) => {
                resetPage()
                setKeyword(event.target.value)
              }}
            />

            <Button
              variant={riskOnly ? 'default' : 'outline'}
              size='sm'
              onClick={() => {
                resetPage()
                setRiskOnly(!riskOnly)
              }}
            >
              {t('qy_wd_a_filter_risk')}
            </Button>
            <Button
              variant={holdOnly ? 'default' : 'outline'}
              size='sm'
              onClick={() => {
                resetPage()
                setHoldOnly(!holdOnly)
              }}
            >
              {t('qy_wd_a_filter_hold')}
            </Button>
          </div>

          <QyPageBoundary
            query={query}
            isEmpty={items.length === 0}
            emptyIcon={Banknote}
            emptyTitle={t('qy_wd_a_empty_title')}
            emptyDescription={t('qy_wd_a_empty_desc')}
          >
            <div className='w-full overflow-x-auto'>
              <StaticDataTable
                columns={columns}
                data={items}
                getRowKey={(row) => row.withdraw_no}
                getRowClassName={(row) =>
                  cn(row.sla_breached && 'bg-destructive/5')
                }
                tableClassName='min-w-[1000px]'
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
      </QySectionPageLayout.Content>

      <ReviewDialog
        withdrawalId={reviewId}
        onClose={() => setReviewId(null)}
        onReveal={setRevealId}
      />
      <RevealPayeeDialog
        withdrawalId={revealId}
        onClose={() => setRevealId(null)}
      />
    </QySectionPageLayout>
  )
}

function bucketCount(
  buckets: { status: string; count: number }[],
  status: string
): number {
  return buckets.find((bucket) => bucket.status === status)?.count ?? 0
}
