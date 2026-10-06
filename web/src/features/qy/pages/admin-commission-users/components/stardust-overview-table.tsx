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
import { Sparkles } from 'lucide-react'
import { useState } from 'react'
import { useTranslation } from 'react-i18next'

import {
  StaticDataTable,
  staticDataTableClassNames,
  type StaticDataTableColumn,
} from '@/components/data-table'
import { Input } from '@/components/ui/input'
import { NativeSelect, NativeSelectOption } from '@/components/ui/native-select'

import { QyPageBoundary } from '../../../components/qy-page-boundary'
import { QySdAmount } from '../../../components/qy-sd-amount'
import { qyArray } from '../../../lib/array'
import { qyAdminStardustUserSummaryQuery } from '../../admin-stardust/api'
import {
  QY_STARDUST_USER_SUMMARY_SORTS,
  type QyStardustUserSummaryRow,
  type QyStardustUserSummarySort,
} from '../../admin-stardust/types'
import { QyPager } from '../../components/qy-pager'
import { QY_PAGE_SIZE } from '../../lib/constants'

const SORT_LABEL_KEY: Record<QyStardustUserSummarySort, string> = {
  available: 'qy_sdo_sort_available',
  self: 'qy_sdo_sort_self',
  downline: 'qy_sdo_sort_downline',
  other: 'qy_sdo_sort_other',
  spent: 'qy_sdo_sort_spent',
}

/**
 * 「结算台 → 佣金用户 → 星屑总览」:每个用户的星屑按来源拆开。
 *
 * 项目方要求:显示所有用户的星屑数量,区分「自身返还」与「下游返还」。
 * 归类口径见 `QyStardustUserSummaryRow`;可用 = 自身 + 下游 + 其他 − 已花费。
 */
export function QyAdminStardustOverviewTable() {
  const { t } = useTranslation()
  const [page, setPage] = useState(1)
  const [sort, setSort] = useState<QyStardustUserSummarySort>('available')
  const [keyword, setKeyword] = useState('')

  const query = useQuery(
    qyAdminStardustUserSummaryQuery({
      page,
      page_size: QY_PAGE_SIZE,
      sort,
      ...(keyword.trim() === '' ? {} : { keyword: keyword.trim() }),
    })
  )
  const items = qyArray(query.data?.items)

  const amountColumn = (
    id: string,
    headerKey: string,
    pick: (row: QyStardustUserSummaryRow) => number
  ): StaticDataTableColumn<QyStardustUserSummaryRow> => ({
    id,
    header: t(headerKey),
    className: staticDataTableClassNames.compactHeaderCellRight,
    cellClassName: staticDataTableClassNames.compactNumericCell,
    cell: (row) => <QySdAmount amount={pick(row)} />,
  })

  const columns: StaticDataTableColumn<QyStardustUserSummaryRow>[] = [
    {
      id: 'user',
      header: t('qy_common_user'),
      className: staticDataTableClassNames.compactHeaderCell,
      cellClassName: staticDataTableClassNames.compactCell,
      cell: (row) => (
        <span className='flex flex-col'>
          <span>{row.username || t('qy_cb_user_gone')}</span>
          <span className='text-muted-foreground text-xs'>#{row.user_id}</span>
        </span>
      ),
    },
    amountColumn('available', 'qy_sdo_col_available', (row) => row.available),
    amountColumn('self', 'qy_sdo_col_self', (row) => row.self_earned),
    amountColumn(
      'downline',
      'qy_sdo_col_downline',
      (row) => row.downline_earned
    ),
    amountColumn('other', 'qy_sdo_col_other', (row) => row.other_earned),
    amountColumn('spent', 'qy_sdo_col_spent', (row) => row.spent),
  ]

  return (
    <div className='space-y-3'>
      <div className='bg-muted/40 text-muted-foreground rounded-md border p-3 text-xs'>
        <p className='text-foreground font-medium'>{t('qy_sdo_scope')}</p>
        <p className='mt-1'>{t('qy_sdo_scope_hint')}</p>
      </div>

      <div className='flex flex-wrap items-center gap-3'>
        <Input
          className='h-8 w-64'
          value={keyword}
          placeholder={t('qy_cu_keyword_ph')}
          aria-label={t('qy_cu_keyword_ph')}
          onChange={(event) => {
            setPage(1)
            setKeyword(event.target.value)
          }}
        />
        <NativeSelect
          size='sm'
          aria-label={t('qy_cb_sort')}
          value={sort}
          onChange={(event) => {
            setPage(1)
            setSort(event.target.value as QyStardustUserSummarySort)
          }}
        >
          {QY_STARDUST_USER_SUMMARY_SORTS.map((value) => (
            <NativeSelectOption key={value} value={value}>
              {t(SORT_LABEL_KEY[value])}
            </NativeSelectOption>
          ))}
        </NativeSelect>
      </div>

      <QyPageBoundary
        query={query}
        isEmpty={items.length === 0}
        emptyIcon={Sparkles}
        emptyTitle={t('qy_sdo_empty_title')}
        emptyDescription={t('qy_sdo_empty_desc')}
      >
        <div className='w-full overflow-x-auto'>
          <StaticDataTable
            columns={columns}
            data={items}
            getRowKey={(row) => row.user_id}
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
      </QyPageBoundary>
    </div>
  )
}
