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
import { CalendarClock } from 'lucide-react'
import { useState } from 'react'
import { useTranslation } from 'react-i18next'

import {
  StaticDataTable,
  staticDataTableClassNames,
  type StaticDataTableColumn,
} from '@/components/data-table'
import { Input } from '@/components/ui/input'
import { NativeSelect, NativeSelectOption } from '@/components/ui/native-select'

import { QyAmountText } from '../../../components/qy-amount-text'
import { QyPageBoundary } from '../../../components/qy-page-boundary'
import { QyStatusBadge } from '../../../components/qy-status-badge'
import { useStardustName } from '../../../hooks/use-stardust-name'
import { qyArray } from '../../../lib/array'
import { QyPager } from '../../components/qy-pager'
import { QY_PAGE_SIZE } from '../../lib/constants'
import { formatQyTs } from '../../ops/format'
import {
  QY_SD_ACCRUAL_STATUSES,
  qySdAccrualBadge,
  qySdBpsPercent,
  qySdFormatDay,
  qySdHoldReasonKey,
} from '../../stardust/lib/display'
import { qyAdminStardustAccrualsQuery } from '../api'
import type { QyStardustAdminAccrualRow } from '../types'

/**
 * 「日桶」—— 消费返的逐日计提，一行一个 (用户, 桶日)。
 *
 * 运营在这张表上要回答的是「某一天为什么没发 / 发了多少 / 谁被扣住了」，
 * 所以三个筛选正好是那三个问题：用户、桶日、状态。`held` 的行是要人处理的那批
 * —— 账户余额补正、账号恢复之后，下一次结算（或「结算」标签里的重跑）会重评它们。
 */
export function QySdAdminAccrualsTab() {
  const { t } = useTranslation()
  const unit = useStardustName()
  const [page, setPage] = useState(1)
  const [userId, setUserId] = useState('')
  const [day, setDay] = useState('')
  const [status, setStatus] = useState('')

  // 桶日只在凑齐八位数字之后才发出去：半截日期后端会 400，而运营正在敲。
  const dayFilter = /^\d{8}$/.test(day) ? day : ''
  const query = useQuery(
    qyAdminStardustAccrualsQuery({
      page,
      page_size: QY_PAGE_SIZE,
      ...(/^\d+$/.test(userId) ? { user_id: Number(userId) } : {}),
      ...(dayFilter === '' ? {} : { bucket_date: dayFilter }),
      ...(status === '' ? {} : { status }),
    })
  )
  const items = qyArray(query.data?.items)

  const columns: StaticDataTableColumn<QyStardustAdminAccrualRow>[] = [
    {
      id: 'bucket_date',
      header: t('qy_sd_col_day'),
      className: staticDataTableClassNames.compactHeaderCell,
      cellClassName: staticDataTableClassNames.compactCell,
      cell: (row) => qySdFormatDay(row.bucket_date),
    },
    {
      id: 'user',
      header: t('qy_common_user'),
      className: staticDataTableClassNames.compactHeaderCell,
      cellClassName: staticDataTableClassNames.compactCell,
      cell: (row) => `#${row.user_id}`,
    },
    {
      id: 'user_group',
      header: t('qy_sd_col_group'),
      className: staticDataTableClassNames.compactHeaderCell,
      cellClassName: staticDataTableClassNames.compactMutedCell,
      cell: (row) => (row.user_group === '' ? '-' : row.user_group),
    },
    {
      id: 'base_quota',
      header: t('qy_sd_yesterday_base'),
      className: staticDataTableClassNames.compactHeaderCellRight,
      cellClassName: staticDataTableClassNames.compactNumericCell,
      cell: (row) => <QyAmountText quota={row.base_quota} />,
    },
    {
      id: 'rate',
      header: t('qy_sd_yesterday_rate'),
      className: staticDataTableClassNames.compactHeaderCellRight,
      cellClassName: staticDataTableClassNames.compactMutedNumericCell,
      cell: (row) =>
        t('qy_sd_percent', { percent: qySdBpsPercent(row.rate_bps) }),
    },
    {
      id: 'gross',
      header: t('qy_sd_yesterday_gross'),
      className: staticDataTableClassNames.compactHeaderCellRight,
      cellClassName: staticDataTableClassNames.compactNumericCell,
      cell: (row) => t('qy_sd_amount_with_unit', { amount: row.gross, unit }),
    },
    {
      id: 'status',
      header: t('qy_common_status'),
      className: staticDataTableClassNames.compactHeaderCell,
      cellClassName: staticDataTableClassNames.compactCell,
      cell: (row) => <AccrualStatus row={row} />,
    },
    {
      id: 'ledger_id',
      header: t('qy_sdadm_col_ledger_id'),
      className: staticDataTableClassNames.compactHeaderCellRight,
      cellClassName: staticDataTableClassNames.compactMutedNumericCell,
      cell: (row) => (row.ledger_id > 0 ? String(row.ledger_id) : '-'),
    },
    {
      id: 'computed_at',
      header: t('qy_sdadm_col_computed_at'),
      className: staticDataTableClassNames.compactHeaderCell,
      cellClassName: staticDataTableClassNames.compactMutedCell,
      cell: (row) => formatQyTs(row.computed_at),
    },
    {
      id: 'settled_at',
      header: t('qy_sd_col_settled_at'),
      className: staticDataTableClassNames.compactHeaderCell,
      cellClassName: staticDataTableClassNames.compactMutedCell,
      cell: (row) => formatQyTs(row.settled_at),
    },
  ]

  return (
    <div className='space-y-3'>
      <div className='flex flex-wrap items-center gap-2'>
        <Input
          className='h-8 w-40'
          inputMode='numeric'
          value={userId}
          placeholder={t('qy_sdadm_user_id_ph')}
          onChange={(event) => {
            setPage(1)
            setUserId(event.target.value.replaceAll(/\D/g, ''))
          }}
        />
        <Input
          className='h-8 w-40'
          inputMode='numeric'
          value={day}
          maxLength={8}
          aria-invalid={day !== '' && dayFilter === ''}
          placeholder={t('qy_sdadm_day_ph')}
          onChange={(event) => {
            setPage(1)
            setDay(event.target.value.replaceAll(/\D/g, ''))
          }}
        />
        <NativeSelect
          size='sm'
          aria-label={t('qy_common_status')}
          value={status}
          onChange={(event) => {
            setPage(1)
            setStatus(event.target.value)
          }}
        >
          <NativeSelectOption value=''>{t('qy_common_all')}</NativeSelectOption>
          {QY_SD_ACCRUAL_STATUSES.map((value) => (
            <NativeSelectOption key={value} value={value}>
              {t(qySdAccrualBadge(value).labelKey)}
            </NativeSelectOption>
          ))}
        </NativeSelect>
      </div>

      <QyPageBoundary
        query={query}
        isEmpty={query.data != null && items.length === 0}
        emptyIcon={CalendarClock}
        emptyTitle={t('qy_sd_accruals_empty_title')}
        emptyDescription={t('qy_sdadm_accruals_empty_desc')}
      >
        <div className='w-full overflow-x-auto'>
          <StaticDataTable
            columns={columns}
            data={items}
            getRowKey={(row) => `${row.user_id}-${row.bucket_date}`}
            tableClassName='min-w-[1100px]'
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

function AccrualStatus(props: { row: QyStardustAdminAccrualRow }) {
  const { t } = useTranslation()
  const badge = qySdAccrualBadge(props.row.status)
  const holdKey =
    props.row.status === 'held'
      ? qySdHoldReasonKey(props.row.hold_reason)
      : null
  return (
    <span className='inline-flex flex-wrap items-center gap-1.5'>
      <QyStatusBadge
        status={badge.status}
        label={badge.labelKey === '' ? undefined : t(badge.labelKey)}
      />
      {holdKey != null && (
        <span className='text-muted-foreground text-xs'>
          {t(holdKey, { reason: props.row.hold_reason })}
        </span>
      )}
    </span>
  )
}
