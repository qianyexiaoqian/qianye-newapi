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
import { formatTimestampToDate } from '@/lib/format'

import { QyAmountText } from '../../components/qy-amount-text'
import { QyMaskedUser } from '../../components/qy-masked-user'
import { QyPageBoundary } from '../../components/qy-page-boundary'
import { QySdAmount } from '../../components/qy-sd-amount'
import { QySdDecimal } from '../../components/qy-sd-decimal'
import { qyArray } from '../../lib/array'
import { qyInviteesQuery } from '../affiliate/api'
import type { QyInvitee } from '../affiliate/types'
import { QyPager } from '../components/qy-pager'
import { QY_PAGE_SIZE } from '../lib/constants'
import { qySdFormatDay } from '../stardust/lib/display'

/**
 * 「下线」—— 「我的推广」选择夹的第二张标签。
 *
 * 回答"这个人一共给我带来了多少"。下线充值 / 兑换码 / 消费带来的返还只走推广佣金，
 * 所以"带来的佣金"一列来自佣金账本（含尚未结算进余额的部分），逐笔明细在「佣金记录」
 * 那一张标签。星屑账本上因 TA 直接得到的（注册奖 / 套餐返 / 已下线的旧版下线返）单列
 * 一格，**不与佣金相加**：一个还在持有期里、一个已经到账，加成一个数就再也解释不了。
 *
 * **列表里的用户名已由后端脱敏**，前端不得再处理。消费基数是**额度**（`QyAmountText`），
 * 两笔收益是**星屑**（`QySdDecimal` / `QySdAmount`）。
 */
export function QyInviteesBody() {
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
      id: 'commission',
      header: t('qy_inv_col_commission'),
      className: staticDataTableClassNames.compactHeaderCellRight,
      cellClassName: staticDataTableClassNames.compactNumericCell,
      cell: (row) => <QySdDecimal value={row.commission_gross} />,
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
      <p className='text-muted-foreground mb-3 text-xs'>
        {t('qy_inv_people_hint')}
      </p>
      <div className='w-full overflow-x-auto'>
        <StaticDataTable
          columns={columns}
          data={items}
          getRowKey={(row) => row.user_id}
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
  )
}
