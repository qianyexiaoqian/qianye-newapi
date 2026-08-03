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
import { NativeSelect, NativeSelectOption } from '@/components/ui/native-select'
import { formatTimestampToDate } from '@/lib/format'
import { cn } from '@/lib/utils'

import { QyAmountText } from '../../components/qy-amount-text'
import { QyMaskedUser } from '../../components/qy-masked-user'
import { QyPageBoundary } from '../../components/qy-page-boundary'
import { QyStatusBadge } from '../../components/qy-status-badge'
import { QyPager } from '../components/qy-pager'
import { QY_PAGE_SIZE } from '../lib/constants'
import { qyTransferRecordsQuery } from './api'
import type { QyTransferRecord } from './types'

/** 可筛选的状态。与 `qianye/modules/transfer/model.go` 的四个明细状态一致。 */
const STATUS_OPTIONS = ['pending', 'success', 'failed', 'uncertain'] as const

/**
 * 划转记录（钱包页「余额划转」选择夹的第二张标签，需求 2）。
 *
 * 转入与转出是**同一条记录的两个视角**（后端 `toRecordItem` 按当前用户算方向），
 * 所以这里不做两张表，只用一个方向筛选 + 一列带符号的金额 —— 拆成两张表会让
 * "我到底一共转出了多少"变得需要来回切标签。
 */
export function QyTransferLogsBody() {
  const { t } = useTranslation()
  const [page, setPage] = useState(1)
  const [direction, setDirection] = useState('')
  const [status, setStatus] = useState('')

  const query = useQuery(
    qyTransferRecordsQuery({
      p: page,
      page_size: QY_PAGE_SIZE,
      direction,
      status,
    })
  )
  const items = query.data?.items ?? []

  const columns: StaticDataTableColumn<QyTransferRecord>[] = [
    {
      id: 'created_at',
      header: t('qy_common_time'),
      className: staticDataTableClassNames.compactHeaderCell,
      cellClassName: staticDataTableClassNames.compactMutedCell,
      cell: (row) => formatTimestampToDate(row.created_at),
    },
    {
      id: 'direction',
      header: t('qy_common_direction'),
      className: staticDataTableClassNames.compactHeaderCell,
      cellClassName: staticDataTableClassNames.compactCell,
      cell: (row) => (
        <span
          className={cn(
            'text-xs font-medium',
            row.direction === 'in' ? 'text-success' : 'text-muted-foreground'
          )}
        >
          {row.direction === 'in' ? t('qy_common_in') : t('qy_common_out')}
        </span>
      ),
    },
    {
      id: 'counterparty',
      header: t('qy_tr_counterparty'),
      className: staticDataTableClassNames.compactHeaderCell,
      cellClassName: staticDataTableClassNames.compactCell,
      cell: (row) => (
        <QyMaskedUser
          userId={row.counterparty_id}
          maskedName={row.counterparty}
        />
      ),
    },
    {
      id: 'amount',
      header: t('qy_common_amount'),
      className: staticDataTableClassNames.compactHeaderCellRight,
      cellClassName: staticDataTableClassNames.compactNumericCell,
      cell: (row) => (
        <span className={row.direction === 'in' ? 'text-success' : undefined}>
          {row.direction === 'in' ? '+' : '-'}
          <QyAmountText quota={row.amount} />
        </span>
      ),
    },
    {
      id: 'fee',
      header: t('qy_common_fee'),
      className: staticDataTableClassNames.compactHeaderCellRight,
      cellClassName: staticDataTableClassNames.compactMutedNumericCell,
      // 转入方向没有手续费概念：它是从发起方额外扣的，显示 0 会让收款人
      // 以为自己也被收了钱。
      cell: (row) =>
        row.direction === 'in' ? '-' : <QyAmountText quota={row.fee_quota} />,
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
      id: 'remark',
      header: t('qy_common_remark'),
      className: staticDataTableClassNames.compactHeaderCell,
      cellClassName: staticDataTableClassNames.compactMutedCell,
      cell: (row) => (row.remark === '' ? '-' : row.remark),
    },
  ]

  const onFilterChange = (apply: () => void) => {
    // 换筛选条件必须回到第 1 页：留在第 3 页会得到一个空列表，
    // 而空列表看起来和"没有记录"一模一样。
    setPage(1)
    apply()
  }

  return (
    <div className='space-y-3'>
      <div className='flex flex-wrap items-center gap-2'>
        <NativeSelect
          size='sm'
          aria-label={t('qy_common_direction')}
          value={direction}
          onChange={(event) =>
            onFilterChange(() => setDirection(event.target.value))
          }
        >
          <NativeSelectOption value=''>
            {t('qy_tr_filter_all_directions')}
          </NativeSelectOption>
          <NativeSelectOption value='out'>
            {t('qy_common_out')}
          </NativeSelectOption>
          <NativeSelectOption value='in'>
            {t('qy_common_in')}
          </NativeSelectOption>
        </NativeSelect>

        <NativeSelect
          size='sm'
          aria-label={t('qy_common_status')}
          value={status}
          onChange={(event) =>
            onFilterChange(() => setStatus(event.target.value))
          }
        >
          <NativeSelectOption value=''>
            {t('qy_tr_filter_all_statuses')}
          </NativeSelectOption>
          {STATUS_OPTIONS.map((value) => (
            <NativeSelectOption key={value} value={value}>
              {t(`qy_common_st_${value}`)}
            </NativeSelectOption>
          ))}
        </NativeSelect>
      </div>

      <QyPageBoundary
        query={query}
        isEmpty={items.length === 0}
        emptyIcon={ArrowLeftRight}
        emptyTitle={t('qy_tr_empty_title')}
        emptyDescription={t('qy_tr_empty_desc')}
      >
        <div className='w-full overflow-x-auto'>
          <StaticDataTable
            columns={columns}
            data={items}
            getRowKey={(row) => row.order_no}
            tableClassName='min-w-[860px]'
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
  )
}
