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
import { CalendarClock, Coins, Receipt, ShoppingCart } from 'lucide-react'
import { useState } from 'react'
import { useTranslation } from 'react-i18next'

import {
  StaticDataTable,
  staticDataTableClassNames,
  type StaticDataTableColumn,
} from '@/components/data-table'

import { QyPhaseTrack } from '../../../components/art/qy-phase-track'
import { QyAmountText } from '../../../components/qy-amount-text'
import { QyPageBoundary } from '../../../components/qy-page-boundary'
import { QyStatusBadge } from '../../../components/qy-status-badge'
import { useStardustName } from '../../../hooks/use-stardust-name'
import { qyArray } from '../../../lib/array'
import { QyPager } from '../../components/qy-pager'
import { QY_PAGE_SIZE } from '../../lib/constants'
import { QyLotFinePrint } from '../../lottery/components/lottery-fine-print'
import { formatQyTs } from '../../ops/format'
import { qyStardustAccrualsQuery } from '../api'
import {
  qySdAccrualBadge,
  qySdBpsPercent,
  qySdFormatDay,
  qySdHoldReasonKey,
} from '../lib/display'
import type { QyStardustAccrualRow } from '../types'

/**
 * 「待结算」—— 星屑选择夹的第三张标签：消费返的逐日计提（日桶）。
 *
 * 一行一天。它回答的是「明天会到多少、哪几天被扣住了」：`computed` 的桶等下一次
 * 结算，`held` 的桶要先把账户问题解决（余额为负 / 停用 / 注销）才会发。
 * 计提基数是**额度**（走 `QyAmountText`），计提出来的 gross 是星屑（decimal 字符串，
 * 只展示）—— 两种单位并排，各印各的。
 */
export function QyStardustAccrualsBody() {
  const { t } = useTranslation()
  const unit = useStardustName()
  const [page, setPage] = useState(1)

  const query = useQuery(
    qyStardustAccrualsQuery({ page, page_size: QY_PAGE_SIZE })
  )
  const items = qyArray(query.data?.items)

  const columns: StaticDataTableColumn<QyStardustAccrualRow>[] = [
    {
      id: 'bucket_date',
      header: t('qy_sd_col_day'),
      className: staticDataTableClassNames.compactHeaderCell,
      cellClassName: staticDataTableClassNames.compactCell,
      cell: (row) => qySdFormatDay(row.bucket_date),
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
      id: 'settled_at',
      header: t('qy_sd_col_settled_at'),
      className: staticDataTableClassNames.compactHeaderCell,
      cellClassName: staticDataTableClassNames.compactMutedCell,
      cell: (row) => formatQyTs(row.settled_at),
    },
  ]

  return (
    <QyPageBoundary
      query={query}
      isEmpty={query.data != null && items.length === 0}
      emptyIcon={CalendarClock}
      emptyTitle={t('qy_sd_accruals_empty_title')}
      emptyDescription={t('qy_sd_accruals_empty_desc')}
    >
      <div className='space-y-3'>
        {/* 一根「消费当天 → 次日计提 → 结算到账」的轴。表里每一行都在这条轴的
            某一点上：computed 的桶停在第二格等下一次结算，settled 的已经走到头。
            有桶还没发出去时第三格呼吸，全发完了三格都填实。那句关于零头结转的
            解释折起来 —— 它回答的是"为什么 7.4 只发了 7"，不是"明天到多少"。 */}
        <QyPhaseTrack
          className='max-w-sm'
          label={t('qy_sd_axis_aria')}
          steps={[
            {
              key: 'spend',
              label: t('qy_sd_axis_spend'),
              icon: ShoppingCart,
              state: 'done',
            },
            {
              key: 'compute',
              label: t('qy_sd_axis_compute'),
              icon: Receipt,
              state: 'done',
            },
            {
              key: 'settle',
              label: t('qy_sd_axis_settle'),
              icon: Coins,
              state: items.some((row) => row.status !== 'settled')
                ? 'current'
                : 'done',
            },
          ]}
        />
        <QyLotFinePrint>
          <p>{t('qy_sd_accruals_note', { unit })}</p>
        </QyLotFinePrint>
        <div className='w-full overflow-x-auto'>
          <StaticDataTable
            columns={columns}
            data={items}
            getRowKey={(row) => row.bucket_date}
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
      </div>
    </QyPageBoundary>
  )
}

/** 状态徽章 + 被扣住时那一句原因。原因跟在徽章旁边，不用点开才看得到。 */
function AccrualStatus(props: { row: QyStardustAccrualRow }) {
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
