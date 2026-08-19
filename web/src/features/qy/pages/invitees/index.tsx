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
import { Input } from '@/components/ui/input'
import { Tabs, TabsContent, TabsList, TabsTrigger } from '@/components/ui/tabs'
import { formatTimestampToDate } from '@/lib/format'

import { QyAmountText } from '../../components/qy-amount-text'
import { QyPageBoundary } from '../../components/qy-page-boundary'
import {
  qyCommissionRecordsQuery,
  qyInviteeDailyQuery,
  qyInviteesQuery,
} from '../affiliate/api'
import type {
  QyCommissionRecord,
  QyInvitee,
  QyInviteeDaily,
} from '../affiliate/types'
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
        <TabsTrigger value='daily'>{t('qy_id_title')}</TabsTrigger>
      </TabsList>
      <TabsContent value='invitees'>
        <InviteesTable />
      </TabsContent>
      <TabsContent value='records'>
        <CommissionRecordsTable />
      </TabsContent>
      <TabsContent value='daily'>
        <InviteeDailyTable />
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
      // 与左边的「计佣基数」同一个单位，所以走同一个展示件：原样印
      // decimal(30,10) 字符串换不来精度（展示只留 4 位小数），只换来
      // 一列 `0.4700000000` 挨着一列 `$2.74`。
      cell: (row) => <QyAmountText quota={row.total_commission} />,
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
      cell: (row) => <QyAmountText quota={row.gross_amount} />,
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

/** 昨天的 `yyyy-mm-dd`，用作两个日期框的初值（与后端的缺省口径一致）。 */
function yesterdayInputValue(): string {
  const d = new Date()
  d.setDate(d.getDate() - 1)
  return d.toISOString().slice(0, 10)
}

/**
 * 我的下线在一段时间里贡献了多少（「已邀请用户」旁边的第三张次级标签）。
 *
 * # 为什么它不给「消费额」
 *
 * 上面那张「已邀请用户」给的是**开天辟地以来**的累计，答不了「我上周推的那批
 * 人这周还在用吗」——这一张补的就是那个区间。补的只有区间，口径一个字没变：
 * 仍然是计佣基数（`base_quota`），不是下线账户的真实消费。
 *
 * 真实消费额比基数大，而大出来的那部分装着违规扣费（下线被罚了多少款）、
 * 渠道测试、0% 商务价分组、以及关系被停止计佣的那段时间。把它下发给上线，
 * 每一项都是**新增**的泄漏，其中一项直接暴露了下线的处罚记录。基数本身不是
 * 新增泄漏 —— 它早就在隔壁那张「佣金流水」里逐笔下发了，而且它正是"我凭什么
 * 拿到这笔钱"的凭据。
 *
 * 人名仍然只有后端算好的脱敏名与 `invitee_ref`，真实用户名/邮箱/user_id
 * 一个都不下发。
 */
function InviteeDailyTable() {
  const { t } = useTranslation()
  const [page, setPage] = useState(1)
  const [startDate, setStartDate] = useState(yesterdayInputValue)
  const [endDate, setEndDate] = useState(yesterdayInputValue)

  const query = useQuery(
    qyInviteeDailyQuery({
      p: page,
      page_size: QY_PAGE_SIZE,
      start_date: startDate.replaceAll('-', ''),
      end_date: endDate.replaceAll('-', ''),
    })
  )
  const items = query.data?.items ?? []

  const columns: StaticDataTableColumn<QyInviteeDaily>[] = [
    {
      id: 'name',
      header: t('qy_id_invitee'),
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
      id: 'base',
      header: t('qy_id_base'),
      className: staticDataTableClassNames.compactHeaderCellRight,
      cellClassName: staticDataTableClassNames.compactNumericCell,
      cell: (row) => <QyAmountText quota={row.base_quota} />,
    },
    {
      id: 'commission',
      header: t('qy_id_commission'),
      className: staticDataTableClassNames.compactHeaderCellRight,
      cellClassName: staticDataTableClassNames.compactNumericCell,
      cell: (row) => <QyAmountText quota={row.commission} />,
    },
    {
      id: 'status',
      header: t('qy_common_status'),
      className: staticDataTableClassNames.compactHeaderCell,
      cellClassName: staticDataTableClassNames.compactCell,
      cell: (row) =>
        row.blocked ? (
          <Badge variant='destructive'>{t('qy_id_blocked')}</Badge>
        ) : (
          <Badge variant='secondary'>{t('qy_aff_active')}</Badge>
        ),
    },
  ]

  return (
    <div className='flex flex-col gap-3'>
      <p className='text-muted-foreground text-sm'>{t('qy_id_range_hint')}</p>
      <div className='flex flex-wrap items-end gap-2'>
        <label className='flex flex-col gap-1 text-xs'>
          {t('qy_dc_start_date')}
          <Input
            type='date'
            value={startDate}
            onChange={(e) => {
              setStartDate(e.target.value)
              setPage(1)
            }}
          />
        </label>
        <label className='flex flex-col gap-1 text-xs'>
          {t('qy_dc_end_date')}
          <Input
            type='date'
            value={endDate}
            onChange={(e) => {
              setEndDate(e.target.value)
              setPage(1)
            }}
          />
        </label>
      </div>
      {query.data !== undefined && (
        <p className='text-muted-foreground text-sm'>
          {t('qy_id_summary', {
            days: query.data.range.days,
            invitees: query.data.summary.invitee_count,
          })}
        </p>
      )}
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
