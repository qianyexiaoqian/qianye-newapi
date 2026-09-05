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
import { Landmark } from 'lucide-react'
import { useState } from 'react'
import { useTranslation } from 'react-i18next'

import {
  StaticDataTable,
  staticDataTableClassNames,
  type StaticDataTableColumn,
} from '@/components/data-table'
import { Badge } from '@/components/ui/badge'
import { Input } from '@/components/ui/input'
import { NativeSelect, NativeSelectOption } from '@/components/ui/native-select'

import { QyPageBoundary } from '../../../components/qy-page-boundary'
import { QySdAmount } from '../../../components/qy-sd-amount'
import { qyArray } from '../../../lib/array'
import { QY_COMMISSION_CREDIT_STATUSES } from '../../affiliate/types'
import { qyCommissionCreditBadge } from '../../commission-records/lib/credit-status'
import { QyPager } from '../../components/qy-pager'
import { QY_PAGE_SIZE } from '../../lib/constants'
import { formatQyTs } from '../../ops/format'
import { qyAdminCommissionCreditsQuery } from '../api'
import type { QyAdminCommissionCredit } from '../types'

/**
 * 全站自动入账记录（「佣金用户」标签下的次级标签）。
 *
 * 每一行是一次「佣金余额 → 星屑余额」的入账。D-16 之后它是扩展库里的一个本地
 * 结局不明、等人裁决的单子 —— 单号那一格直接链到「资金对账」页，裁决在那边做，
 * 这里只看不动。默认筛选**不**落在 held 上：这张表首先是"钱去哪了"的台账，
 * 其次才是告警队列；告警的入口在资金对账页的默认视图里。
 */
export function QyAdminCommissionCreditsTable() {
  const { t } = useTranslation()
  const [page, setPage] = useState(1)
  const [userId, setUserId] = useState('')
  const [status, setStatus] = useState('')

  const query = useQuery(
    qyAdminCommissionCreditsQuery({
      p: page,
      page_size: QY_PAGE_SIZE,
      user_id: userId.trim(),
      status,
    })
  )
  const items = qyArray(query.data?.items)

  const columns: StaticDataTableColumn<QyAdminCommissionCredit>[] = [
    {
      id: 'created_at',
      header: t('qy_common_time'),
      className: staticDataTableClassNames.compactHeaderCell,
      cellClassName: staticDataTableClassNames.compactMutedCell,
      cell: (row) => formatQyTs(row.created_at),
    },
    {
      id: 'user',
      header: t('qy_common_user'),
      className: staticDataTableClassNames.compactHeaderCell,
      cellClassName: staticDataTableClassNames.compactCell,
      cell: (row) => (
        <span className='flex flex-col'>
          <span>
            {row.username === '' ? t('qy_cb_user_gone') : row.username}
          </span>
          <span className='text-muted-foreground text-xs'>#{row.user_id}</span>
        </span>
      ),
    },
    {
      id: 'credit_no',
      header: t('qy_aff_credit_no'),
      className: staticDataTableClassNames.compactHeaderCell,
      cellClassName: staticDataTableClassNames.compactMutedCodeCell,
      cell: (row) => row.credit_no,
    },
    {
      id: 'amount',
      header: t('qy_common_amount'),
      className: staticDataTableClassNames.compactHeaderCellRight,
      cellClassName: staticDataTableClassNames.compactNumericCell,
      cell: (row) => <QySdAmount amount={row.amount} />,
    },
    {
      id: 'status',
      header: t('qy_common_status'),
      className: staticDataTableClassNames.compactHeaderCell,
      cellClassName: staticDataTableClassNames.compactCell,
      cell: (row) => {
        const badge = qyCommissionCreditBadge(row.status)
        return (
          <Badge variant={badge.variant}>
            {t(badge.labelKey, { defaultValue: row.status })}
          </Badge>
        )
      },
    },
    {
      id: 'ledger_no',
      header: t('qy_sd_col_ledger_no'),
      className: staticDataTableClassNames.compactHeaderCell,
      cellClassName: staticDataTableClassNames.compactMutedCodeCell,
      // 流水号链到星屑账本：这一笔在那边按号一查就是同一行。D-15 时这一格是
      // 资金单号、链到对账台，因为那时入账跨库、有 held 的单子要人裁决；
      // 现在入账是本地事务，对账台上不会再出现佣金入账单。
      cell: (row) =>
        row.ledger_no === '' ? (
          '-'
        ) : (
          <Link
            to='/qy/admin/stardust'
            className='underline underline-offset-2'
          >
            {row.ledger_no}
          </Link>
        ),
    },
    {
      id: 'finished_at',
      header: t('qy_aff_credit_finished'),
      className: staticDataTableClassNames.compactHeaderCell,
      cellClassName: staticDataTableClassNames.compactMutedCell,
      cell: (row) => (row.finished_at > 0 ? formatQyTs(row.finished_at) : '-'),
    },
    {
      id: 'remark',
      header: t('qy_common_remark'),
      className: staticDataTableClassNames.compactHeaderCell,
      cellClassName: staticDataTableClassNames.compactMutedCell,
      cell: (row) => (row.remark === '' ? '-' : row.remark),
    },
  ]

  return (
    <div className='space-y-3'>
      <div className='flex flex-wrap items-center gap-2'>
        <Input
          className='h-8 w-40'
          inputMode='numeric'
          value={userId}
          placeholder={t('qy_cb_user_id_ph')}
          onChange={(event) => {
            setPage(1)
            setUserId(event.target.value)
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
          {QY_COMMISSION_CREDIT_STATUSES.map((value) => (
            <NativeSelectOption key={value} value={value}>
              {t(qyCommissionCreditBadge(value).labelKey)}
            </NativeSelectOption>
          ))}
        </NativeSelect>
      </div>

      <QyPageBoundary
        query={query}
        isEmpty={items.length === 0}
        emptyIcon={Landmark}
        emptyTitle={t('qy_aff_empty_credits_title')}
        emptyDescription={t('qy_cu_credits_empty_desc')}
      >
        <div className='w-full overflow-x-auto'>
          <StaticDataTable
            columns={columns}
            data={items}
            getRowKey={(row) => row.credit_no}
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
  )
}
