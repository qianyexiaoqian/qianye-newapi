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
import { Users } from 'lucide-react'
import { useState } from 'react'
import { useTranslation } from 'react-i18next'

import {
  StaticDataTable,
  staticDataTableClassNames,
  type StaticDataTableColumn,
} from '@/components/data-table'
import { Badge } from '@/components/ui/badge'
import { Tabs, TabsContent, TabsList, TabsTrigger } from '@/components/ui/tabs'
import { formatTimestampToDate } from '@/lib/format'

import { QyAmountText } from '../../components/qy-amount-text'
import { QyPageBoundary } from '../../components/qy-page-boundary'
import { qyCommissionRecordsQuery, qyInviteesQuery } from '../affiliate/api'
import type { QyCommissionRecord, QyInvitee } from '../affiliate/types'
import { QyPager } from '../components/qy-pager'
import { QY_PAGE_SIZE } from '../lib/constants'

/**
 * 已邀请用户 + 我的佣金流水（「推广佣金」选择夹的第二张标签，需求 3）。
 *
 * 两张表合成一张标签下的两个次级标签：用户问"这个人给我带来了多少佣金"时看
 * 第一张，问"这笔 3.27 是怎么来的"时看第二张。拆成两张同级标签会让这个对照
 * 过程和"提现"挤在同一排，四张变六张。
 *
 * 次级标签**刻意不进 hash**：hash 只表达"选择夹选了哪一格"这一层。两层都写
 * 进同一个 hash 需要一套编码规则，而它换来的只是一个更长的分享链接。
 *
 * **列表里的用户名已由后端脱敏**（`commission/mask.go`），前端不得再处理，
 * 也不要试图去补一个真实用户名 —— 后端刻意连 user_id 都没下发。
 */
export function QyInviteesBody() {
  const { t } = useTranslation()

  return (
    <Tabs defaultValue='invitees' className='gap-3'>
      <TabsList>
        <TabsTrigger value='invitees'>{t('qy_aff_tab_people')}</TabsTrigger>
        <TabsTrigger value='records'>{t('qy_aff_tab_records')}</TabsTrigger>
      </TabsList>
      <TabsContent value='invitees'>
        <InviteesTable />
      </TabsContent>
      <TabsContent value='records'>
        <CommissionRecordsTable />
      </TabsContent>
    </Tabs>
  )
}

function InviteesTable() {
  const { t } = useTranslation()
  const [page, setPage] = useState(1)
  const query = useQuery(qyInviteesQuery({ p: page, page_size: QY_PAGE_SIZE }))
  const items = query.data?.items ?? []

  const columns: StaticDataTableColumn<QyInvitee>[] = [
    {
      id: 'name',
      header: t('qy_aff_invitee'),
      className: staticDataTableClassNames.compactHeaderCell,
      cellClassName: staticDataTableClassNames.compactCell,
      cell: (row) => (
        <span className='inline-flex min-w-0 items-center gap-2'>
          <span className='truncate'>{row.masked_name || '-'}</span>
          <span className='text-muted-foreground shrink-0 font-mono text-xs'>
            {row.ref}
          </span>
        </span>
      ),
    },
    {
      id: 'bound_at',
      header: t('qy_aff_bound_at'),
      className: staticDataTableClassNames.compactHeaderCell,
      cellClassName: staticDataTableClassNames.compactMutedCell,
      cell: (row) => formatTimestampToDate(row.bound_at),
    },
    {
      id: 'base',
      header: t('qy_aff_base_quota'),
      className: staticDataTableClassNames.compactHeaderCellRight,
      cellClassName: staticDataTableClassNames.compactNumericCell,
      cell: (row) => <QyAmountText quota={row.total_base_quota} />,
    },
    {
      id: 'commission',
      header: t('qy_aff_total_commission'),
      className: staticDataTableClassNames.compactHeaderCellRight,
      cellClassName: staticDataTableClassNames.compactNumericCell,
      // 佣金是 decimal(30,10) 字符串，原样展示：转成 number 会在长尾小数上丢位。
      cell: (row) => row.total_commission,
    },
    {
      id: 'status',
      header: t('qy_common_status'),
      className: staticDataTableClassNames.compactHeaderCell,
      cellClassName: staticDataTableClassNames.compactCell,
      cell: (row) =>
        row.blocked ? (
          <Badge variant='destructive'>{t('qy_aff_blocked')}</Badge>
        ) : (
          <Badge variant='secondary'>{t('qy_aff_active')}</Badge>
        ),
    },
  ]

  return (
    <QyPageBoundary
      query={query}
      isEmpty={items.length === 0}
      emptyIcon={Users}
      emptyTitle={t('qy_aff_empty_people_title')}
      emptyDescription={t('qy_aff_empty_people_desc')}
    >
      <div className='w-full overflow-x-auto'>
        <StaticDataTable
          columns={columns}
          data={items}
          getRowKey={(row) => row.ref}
          tableClassName='min-w-[720px]'
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
  )
}

function CommissionRecordsTable() {
  const { t } = useTranslation()
  const [page, setPage] = useState(1)
  const query = useQuery(
    qyCommissionRecordsQuery({ p: page, page_size: QY_PAGE_SIZE })
  )
  const items = query.data?.items ?? []

  const columns: StaticDataTableColumn<QyCommissionRecord>[] = [
    {
      id: 'created_at',
      header: t('qy_common_time'),
      className: staticDataTableClassNames.compactHeaderCell,
      cellClassName: staticDataTableClassNames.compactMutedCell,
      cell: (row) => formatTimestampToDate(row.created_at),
    },
    {
      id: 'source',
      header: t('qy_aff_source'),
      className: staticDataTableClassNames.compactHeaderCell,
      cellClassName: staticDataTableClassNames.compactCell,
      cell: (row) => t(`qy_aff_src_${row.source_type}`, row.source_type),
    },
    {
      id: 'invitee',
      header: t('qy_aff_invitee'),
      className: staticDataTableClassNames.compactHeaderCell,
      cellClassName: staticDataTableClassNames.compactCell,
      cell: (row) => row.invitee_masked_name || row.invitee_ref || '-',
    },
    {
      id: 'base',
      header: t('qy_aff_base_quota'),
      className: staticDataTableClassNames.compactHeaderCellRight,
      cellClassName: staticDataTableClassNames.compactNumericCell,
      cell: (row) => <QyAmountText quota={row.base_quota} />,
    },
    {
      id: 'rate',
      header: t('qy_aff_rate'),
      className: staticDataTableClassNames.compactHeaderCellRight,
      cellClassName: staticDataTableClassNames.compactMutedNumericCell,
      cell: (row) => t('qy_aff_rate_value', { percent: row.rate_bps / 100 }),
    },
    {
      id: 'gross',
      header: t('qy_aff_gross'),
      className: staticDataTableClassNames.compactHeaderCellRight,
      cellClassName: staticDataTableClassNames.compactNumericCell,
      cell: (row) => row.gross_amount,
    },
    {
      id: 'mature',
      header: t('qy_aff_mature_at'),
      className: staticDataTableClassNames.compactHeaderCell,
      cellClassName: staticDataTableClassNames.compactMutedCell,
      cell: (row) => formatTimestampToDate(row.mature_at),
    },
    {
      id: 'status',
      header: t('qy_common_status'),
      className: staticDataTableClassNames.compactHeaderCell,
      cellClassName: staticDataTableClassNames.compactCell,
      cell: (row) => t(`qy_aff_st_${row.status}`, row.status),
    },
  ]

  return (
    <QyPageBoundary
      query={query}
      isEmpty={items.length === 0}
      emptyTitle={t('qy_aff_empty_records_title')}
      emptyDescription={t('qy_aff_empty_records_desc')}
    >
      <div className='w-full overflow-x-auto'>
        <StaticDataTable
          columns={columns}
          data={items}
          getRowKey={(row) => row.accrual_no}
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
  )
}
