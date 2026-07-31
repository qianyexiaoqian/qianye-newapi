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
import { ArrowLeftRight } from 'lucide-react'
import { useState } from 'react'
import { useTranslation } from 'react-i18next'

import {
  StaticDataTable,
  staticDataTableClassNames,
  type StaticDataTableColumn,
} from '@/components/data-table'
import { Input } from '@/components/ui/input'
import { NativeSelect, NativeSelectOption } from '@/components/ui/native-select'
import { formatTimestampToDate } from '@/lib/format'

import { QyAmountText } from '../../components/qy-amount-text'
import { QyPageBoundary } from '../../components/qy-page-boundary'
import { QySectionPageLayout } from '../../components/qy-section-page-layout'
import { QyStatusBadge } from '../../components/qy-status-badge'
import { QyPager } from '../components/qy-pager'
import { QY_PAGE_SIZE } from '../lib/constants'
import { qyAdminTransferRecordsQuery } from '../transfer-logs/api'
import type { QyAdminTransferRecord } from '../transfer-logs/types'

const STATUS_OPTIONS = ['pending', 'success', 'failed', 'uncertain'] as const

/**
 * 划转流水（管理端）。
 *
 * 与用户端的关键差异：这里是**真实用户名与余额快照**，不脱敏 —— 争议仲裁时
 * "扣款那一刻双方余额各是多少"只有这张表答得上来，主库 `users` 没有历史版本。
 *
 * 本页只读。划转没有人工审核环节，出问题走资金对账台（`/qy/admin/fund-orders`）
 * 的人工裁决，那里才是能改状态的地方。
 */
export function QyAdminTransferRecords() {
  const { t } = useTranslation()
  const [page, setPage] = useState(1)
  const [status, setStatus] = useState('')
  const [keyword, setKeyword] = useState('')

  const trimmed = keyword.trim()
  const numeric = /^\d+$/.test(trimmed)
  const query = useQuery(
    qyAdminTransferRecordsQuery({
      p: page,
      page_size: QY_PAGE_SIZE,
      status,
      // 纯数字当 user_id，其余当单号：管理员手上的线索只会是这两种之一。
      user_id: numeric ? Number(trimmed) : undefined,
      order_no: numeric ? undefined : trimmed,
    })
  )
  const items = query.data?.items ?? []

  const columns: StaticDataTableColumn<QyAdminTransferRecord>[] = [
    {
      id: 'created_at',
      header: t('qy_common_time'),
      className: staticDataTableClassNames.compactHeaderCell,
      cellClassName: staticDataTableClassNames.compactMutedCell,
      cell: (row) => formatTimestampToDate(row.created_at),
    },
    {
      id: 'from',
      header: t('qy_tr_a_from'),
      className: staticDataTableClassNames.compactHeaderCell,
      cellClassName: staticDataTableClassNames.compactCell,
      cell: (row) => `#${row.from_user_id} ${row.from_username}`,
    },
    {
      id: 'to',
      header: t('qy_tr_a_to'),
      className: staticDataTableClassNames.compactHeaderCell,
      cellClassName: staticDataTableClassNames.compactCell,
      cell: (row) => `#${row.to_user_id} ${row.to_username}`,
    },
    {
      id: 'amount',
      header: t('qy_common_amount'),
      className: staticDataTableClassNames.compactHeaderCellRight,
      cellClassName: staticDataTableClassNames.compactNumericCell,
      cell: (row) => <QyAmountText quota={row.amount} />,
    },
    {
      id: 'fee',
      header: t('qy_common_fee'),
      className: staticDataTableClassNames.compactHeaderCellRight,
      cellClassName: staticDataTableClassNames.compactMutedNumericCell,
      cell: (row) => <QyAmountText quota={row.fee_quota} />,
    },
    {
      id: 'snapshot',
      header: t('qy_tr_a_snapshot'),
      className: staticDataTableClassNames.compactHeaderCell,
      cellClassName: staticDataTableClassNames.compactMutedCodeCell,
      // 余额快照是争议仲裁的唯一凭据，必须能一眼看到"扣之前/扣之后"。
      cell: (row) =>
        `${row.from_quota_before} → ${row.from_quota_after} / ${row.to_quota_before} → ${row.to_quota_after}`,
    },
    {
      id: 'status',
      header: t('qy_common_status'),
      className: staticDataTableClassNames.compactHeaderCell,
      cellClassName: staticDataTableClassNames.compactCell,
      cell: (row) => <QyStatusBadge status={row.status} />,
    },
    {
      id: 'order_no',
      header: t('qy_common_order_no'),
      className: staticDataTableClassNames.compactHeaderCell,
      cellClassName: staticDataTableClassNames.compactMutedCodeCell,
      cell: (row) => row.order_no,
    },
    {
      id: 'client_ip',
      header: t('qy_tr_a_client_ip'),
      className: staticDataTableClassNames.compactHeaderCell,
      cellClassName: staticDataTableClassNames.compactMutedCodeCell,
      cell: (row) => (row.client_ip === '' ? '-' : row.client_ip),
    },
  ]

  return (
    <QySectionPageLayout>
      <QySectionPageLayout.Title>
        {t('qy_nav_a_transfer_records')}
      </QySectionPageLayout.Title>
      <QySectionPageLayout.Content>
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
                  {t(`qy_common_st_${value}`)}
                </NativeSelectOption>
              ))}
            </NativeSelect>
            <Input
              className='h-8 w-56'
              value={keyword}
              placeholder={t('qy_tr_a_search_ph')}
              onChange={(event) => {
                setPage(1)
                setKeyword(event.target.value)
              }}
            />
          </div>

          <QyPageBoundary
            query={query}
            isEmpty={items.length === 0}
            emptyIcon={ArrowLeftRight}
            emptyTitle={t('qy_tr_a_empty_title')}
            emptyDescription={t('qy_tr_a_empty_desc')}
          >
            <div className='w-full overflow-x-auto'>
              <StaticDataTable
                columns={columns}
                data={items}
                getRowKey={(row) => row.order_no}
                tableClassName='min-w-[1200px]'
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
    </QySectionPageLayout>
  )
}
