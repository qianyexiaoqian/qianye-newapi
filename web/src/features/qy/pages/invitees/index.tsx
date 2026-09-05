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
import { QyMaskedUser } from '../../components/qy-masked-user'
import { QyPageBoundary } from '../../components/qy-page-boundary'
import { QySdAmount } from '../../components/qy-sd-amount'
import { QyStatusBadge } from '../../components/qy-status-badge'
import { useStardustName } from '../../hooks/use-stardust-name'
import { qyArray } from '../../lib/array'
import { qyInviteeDailyQuery, qyInviteesQuery } from '../affiliate/api'
import type { QyInvitee, QyInviteeDailyRow } from '../affiliate/types'
import { QyPager } from '../components/qy-pager'
import { QY_PAGE_SIZE } from '../lib/constants'
import {
  qySdAccrualBadge,
  qySdBpsPercent,
  qySdFormatDay,
} from '../stardust/lib/display'

/**
 * 「下线」—— 「我的推广」选择夹的第二张标签（D-14）。
 *
 * 两张表合成一张标签下的两个次级标签：用户问"这个人一共给我带来了多少"时看
 * 第一张（开天辟地以来的累计），问"我上周推的那批人昨天还在用吗"时看第二张
 * （按天）。次级标签**刻意不进 hash**：hash 只表达"选择夹选了哪一格"这一层。
 *
 * **列表里的用户名已由后端脱敏**，前端不得再处理，也不要试图去补一个真实
 * 用户名。累计消费是**额度**（走 `QyAmountText`），我因 TA 得到的是**星屑**
 * （走 `QySdAmount`）：两种单位并排各印各的，用户要看的正是"TA 花了多少、
 * 我换回来多少"。
 */
export function QyInviteesBody() {
  const { t } = useTranslation()

  return (
    <Tabs defaultValue='invitees' className='gap-3'>
      <TabsList>
        <TabsTrigger value='invitees'>{t('qy_inv_tab_people')}</TabsTrigger>
        <TabsTrigger value='daily'>{t('qy_inv_tab_daily')}</TabsTrigger>
      </TabsList>
      <TabsContent value='invitees'>
        <InviteesTable />
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
  const items = qyArray(query.data?.items)

  const columns: StaticDataTableColumn<QyInvitee>[] = [
    {
      id: 'name',
      header: t('qy_inv_col_invitee'),
      className: staticDataTableClassNames.compactHeaderCell,
      cellClassName: staticDataTableClassNames.compactCell,
      cell: (row) => (
        <QyMaskedUser userId={row.user_id} maskedName={row.username_masked} />
      ),
    },
    {
      id: 'bound_at',
      header: t('qy_inv_col_bound_at'),
      className: staticDataTableClassNames.compactHeaderCell,
      cellClassName: staticDataTableClassNames.compactMutedCell,
      cell: (row) => formatTimestampToDate(row.bound_at),
    },
    {
      id: 'last_active',
      header: t('qy_inv_col_last_active'),
      className: staticDataTableClassNames.compactHeaderCell,
      cellClassName: staticDataTableClassNames.compactMutedCell,
      cell: (row) =>
        row.last_active_day === '' ? '-' : qySdFormatDay(row.last_active_day),
    },
    {
      id: 'base',
      header: t('qy_inv_col_total_base'),
      className: staticDataTableClassNames.compactHeaderCellRight,
      cellClassName: staticDataTableClassNames.compactNumericCell,
      cell: (row) => <QyAmountText quota={row.total_base_quota} />,
    },
    {
      id: 'stardust',
      header: t('qy_inv_col_total_stardust'),
      className: staticDataTableClassNames.compactHeaderCellRight,
      cellClassName: staticDataTableClassNames.compactNumericCell,
      cell: (row) => <QySdAmount amount={row.total_stardust} />,
    },
    {
      id: 'status',
      header: t('qy_common_status'),
      className: staticDataTableClassNames.compactHeaderCell,
      cellClassName: staticDataTableClassNames.compactCell,
      cell: (row) =>
        row.blocked ? (
          <Badge variant='destructive'>{t('qy_inv_blocked')}</Badge>
        ) : (
          <Badge variant='secondary'>{t('qy_inv_active')}</Badge>
        ),
    },
  ]

  return (
    <QyPageBoundary
      query={query}
      isEmpty={query.data != null && items.length === 0}
      emptyIcon={Users}
      emptyTitle={t('qy_inv_empty_people_title')}
      emptyDescription={t('qy_inv_empty_people_desc')}
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
  )
}

/** 昨天的 `yyyy-mm-dd`，用作日期框的初值（与后端的缺省口径一致）。 */
function yesterdayInputValue(): string {
  const d = new Date()
  d.setDate(d.getDate() - 1)
  return d.toISOString().slice(0, 10)
}

/**
 * 我的下线在某一天贡献了多少（「下线」旁边的第二张次级标签）。
 *
 * 一行 = 一个下线在那一天的那一桶，与管理端「日结明细」同一口径：基数是
 * **额度**、按我的分组档计提出来的是 **星屑**（decimal 字符串，只展示），
 * 状态是日桶那三态（待发放 / 已发放 / 已暂缓）。两个数都要在 —— 只给基数
 * 用户算不出自己该拿多少，只给星屑他不知道那个数是按什么算的。
 *
 * 真实用户名 / 邮箱一个都不下发，人名只有后端算好的脱敏名。
 */
function InviteeDailyTable() {
  const { t } = useTranslation()
  const unit = useStardustName()
  const [date, setDate] = useState(yesterdayInputValue)

  const query = useQuery(qyInviteeDailyQuery(date.replaceAll('-', '')))
  const items = qyArray(query.data?.items)

  const columns: StaticDataTableColumn<QyInviteeDailyRow>[] = [
    {
      id: 'name',
      header: t('qy_inv_col_invitee'),
      className: staticDataTableClassNames.compactHeaderCell,
      cellClassName: staticDataTableClassNames.compactCell,
      cell: (row) => (
        <QyMaskedUser userId={row.invitee_id} maskedName={row.invitee_masked} />
      ),
    },
    {
      id: 'base',
      header: t('qy_inv_col_base'),
      className: staticDataTableClassNames.compactHeaderCellRight,
      cellClassName: staticDataTableClassNames.compactNumericCell,
      cell: (row) => <QyAmountText quota={row.base_quota} />,
    },
    {
      id: 'bps',
      header: t('qy_inv_col_bps'),
      className: staticDataTableClassNames.compactHeaderCellRight,
      cellClassName: staticDataTableClassNames.compactMutedNumericCell,
      cell: (row) => t('qy_sd_percent', { percent: qySdBpsPercent(row.bps) }),
    },
    {
      id: 'gross',
      header: t('qy_inv_col_gross'),
      className: staticDataTableClassNames.compactHeaderCellRight,
      cellClassName: staticDataTableClassNames.compactNumericCell,
      cell: (row) => t('qy_sd_amount_with_unit', { amount: row.gross, unit }),
    },
    {
      id: 'status',
      header: t('qy_common_status'),
      className: staticDataTableClassNames.compactHeaderCell,
      cellClassName: staticDataTableClassNames.compactCell,
      cell: (row) => {
        const badge = qySdAccrualBadge(row.status)
        return (
          <QyStatusBadge
            status={badge.status}
            label={badge.labelKey === '' ? undefined : t(badge.labelKey)}
          />
        )
      },
    },
  ]

  return (
    <div className='flex flex-col gap-3'>
      <p className='text-muted-foreground text-sm'>
        {t('qy_inv_daily_hint', { unit })}
      </p>
      <div className='flex flex-wrap items-end gap-2'>
        <label className='flex flex-col gap-1 text-xs'>
          {t('qy_inv_daily_date')}
          <Input
            type='date'
            value={date}
            onChange={(e) => setDate(e.target.value)}
          />
        </label>
      </div>
      {query.data !== undefined && (
        <p className='text-muted-foreground text-sm'>
          {t('qy_inv_daily_summary', {
            day: qySdFormatDay(query.data.day),
            invitees: query.data.items.length,
            gross: query.data.total_gross,
            unit,
          })}
        </p>
      )}
      <QyPageBoundary
        query={query}
        isEmpty={query.data != null && items.length === 0}
        emptyIcon={Users}
        emptyTitle={t('qy_inv_daily_empty_title')}
        emptyDescription={t('qy_inv_daily_empty_desc')}
      >
        <div className='w-full overflow-x-auto'>
          <StaticDataTable
            columns={columns}
            data={items}
            getRowKey={(row) => row.invitee_id}
            tableClassName='min-w-[640px]'
          />
        </div>
      </QyPageBoundary>
    </div>
  )
}
