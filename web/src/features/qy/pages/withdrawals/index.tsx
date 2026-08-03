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
import { Banknote } from 'lucide-react'
import { useState } from 'react'
import { useTranslation } from 'react-i18next'

import {
  StaticDataTable,
  staticDataTableClassNames,
  type StaticDataTableColumn,
} from '@/components/data-table'
import { Button } from '@/components/ui/button'
import { NativeSelect, NativeSelectOption } from '@/components/ui/native-select'
import { formatTimestampToDate } from '@/lib/format'

import { QyAmountText } from '../../components/qy-amount-text'
import { QyPageBoundary } from '../../components/qy-page-boundary'
import { QyStatusBadge } from '../../components/qy-status-badge'
import { QyFiatText } from '../components/qy-fiat-text'
import { QyPager } from '../components/qy-pager'
import { QY_PAGE_SIZE } from '../lib/constants'
import { qyWithdrawRecordsQuery } from '../withdraw/api'
import type { QyWithdrawal } from '../withdraw/types'
import { WithdrawalDetailDialog } from './components/withdrawal-detail-dialog'

/** 与后端 `knownStatuses`（`withdraw/api_user.go`）一致的七个状态。 */
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
 * 佣金提现记录（「推广佣金」选择夹的第四张标签，需求 3）。
 *
 * 列表只回答"这笔现在到哪一步了"，**"什么时候打的款 / 什么时候拒绝的 /
 * 为什么被拒"由详情弹窗里的时间线回答** —— 那三个字段（`paid_at`、
 * `reviewed_at`、`reject_reason`）在列表里塞不下，塞下了也没人看得懂顺序。
 * 所以每一行都必须能点开。
 */
export function QyWithdrawalsBody() {
  const { t } = useTranslation()
  const [page, setPage] = useState(1)
  const [status, setStatus] = useState('')
  const [detailId, setDetailId] = useState<number | null>(null)

  const query = useQuery(
    qyWithdrawRecordsQuery({ p: page, page_size: QY_PAGE_SIZE, status })
  )
  const items = query.data?.items ?? []

  const columns: StaticDataTableColumn<QyWithdrawal>[] = [
    {
      id: 'created_at',
      header: t('qy_common_created_at'),
      className: staticDataTableClassNames.compactHeaderCell,
      cellClassName: staticDataTableClassNames.compactMutedCell,
      cell: (row) => formatTimestampToDate(row.created_at),
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
      // quota 方式没有法币金额：显示 0.00 会让人以为一分钱没到账。
      cell: (row) =>
        row.method === 'fiat' ? (
          <QyFiatText amount={row.net_amount} currency={row.currency} />
        ) : (
          '-'
        ),
    },
    {
      id: 'status',
      header: t('qy_common_status'),
      className: staticDataTableClassNames.compactHeaderCell,
      cellClassName: staticDataTableClassNames.compactCell,
      cell: (row) => <QyStatusBadge status={row.status} />,
    },
    {
      id: 'progress',
      header: t('qy_wd_progress'),
      className: staticDataTableClassNames.compactHeaderCell,
      cellClassName: staticDataTableClassNames.compactMutedCell,
      // 一句话交代"最近发生了什么"，把三个时间字段收敛成一列。
      cell: (row) => summarize(row, t),
    },
    {
      id: 'actions',
      header: t('qy_common_actions'),
      className: staticDataTableClassNames.actionHeaderCell,
      cellClassName: staticDataTableClassNames.actionCell,
      cell: (row) => (
        <Button variant='ghost' size='sm' onClick={() => setDetailId(row.id)}>
          {t('qy_common_detail')}
        </Button>
      ),
    },
  ]

  return (
    <div className='space-y-3'>
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
          {t('qy_wd_filter_all_statuses')}
        </NativeSelectOption>
        {STATUS_OPTIONS.map((value) => (
          <NativeSelectOption key={value} value={value}>
            {t(`qy_common_st_${value}`)}
          </NativeSelectOption>
        ))}
      </NativeSelect>

      <QyPageBoundary
        query={query}
        isEmpty={items.length === 0}
        emptyIcon={Banknote}
        emptyTitle={t('qy_wd_empty_title')}
        emptyDescription={t('qy_wd_empty_desc')}
      >
        <div className='w-full overflow-x-auto'>
          <StaticDataTable
            columns={columns}
            data={items}
            getRowKey={(row) => row.withdraw_no}
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

      {/* 弹窗与列表并列写在同一个正文里，不再依赖 `QySectionPageLayout`
          把插槽外的 children 捞回来 —— 收进选择夹之后这里根本没有插槽。 */}
      <WithdrawalDetailDialog
        withdrawalId={detailId}
        onClose={() => setDetailId(null)}
      />
    </div>
  )
}

/**
 * 列表里的一句话进度。
 *
 * 优先级刻意是"打款 → 拒绝 → 审核 → 等待"：终态信息永远比中间态更值得占这一格，
 * 而用户最先要找的就是"钱到底打了没、为什么没打"。
 */
function summarize(
  row: QyWithdrawal,
  t: ReturnType<typeof useTranslation>['t']
): string {
  if (row.paid_at > 0) {
    return t('qy_wd_sum_paid', { time: formatTimestampToDate(row.paid_at) })
  }
  if (row.status === 'rejected') {
    return t('qy_wd_sum_rejected', {
      time: formatTimestampToDate(row.reviewed_at),
      reason:
        row.reject_reason === '' ? t('qy_common_none') : row.reject_reason,
    })
  }
  if (row.status === 'failed') {
    return t('qy_wd_sum_failed', {
      reason: row.fail_reason === '' ? t('qy_common_none') : row.fail_reason,
    })
  }
  if (row.reviewed_at > 0) {
    return t('qy_wd_sum_approved', {
      time: formatTimestampToDate(row.reviewed_at),
    })
  }
  return t('qy_wd_sum_pending')
}
