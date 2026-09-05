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

import { QyAmountText } from '../../../components/qy-amount-text'
import { QyPageBoundary } from '../../../components/qy-page-boundary'
import { QyStatusBadge } from '../../../components/qy-status-badge'
import { useStardustName } from '../../../hooks/use-stardust-name'
import { qyArray } from '../../../lib/array'
import { QyPager } from '../../components/qy-pager'
import { QY_PAGE_SIZE } from '../../lib/constants'
import {
  qySdAccrualBadge,
  qySdBpsPercent,
  qySdFormatDay,
  qySdHoldReasonKey,
} from '../../stardust/lib/display'
import { qyAdminInviteAccrualsQuery } from '../api'
import type { QyInviteAccrualRow } from '../types'

/** 昨天的 `yyyy-mm-dd`，用作日期框的初值（与后端的缺省口径一致）。 */
function yesterdayInputValue(): string {
  const d = new Date()
  d.setDate(d.getDate() - 1)
  return d.toISOString().slice(0, 10)
}

/**
 * 「日结明细」—— 邀请管理选择夹的第三张标签（D-14）。
 *
 * 一行一个（邀请人 × 下线 × 日）的桶（`qy_sd_invite_accrual`）：日结那一次 run
 * 对当日有消费且 `InviteeEligible` 的下线，按**邀请人**分组档取 bps 算出来的
 * 那一份。运营对账时问的是"这个人昨天为什么返了 / 没返这么多"，答案全在这一行上：
 * 基数（额度，走 `QyAmountText`）、分组档、比例、计提（星屑 decimal 字符串，
 * 只展示）、状态与暂缓原因、以及真正发出去那一笔的流水号。
 *
 * 它与「下线日消费」是同一件事的两半：那张表说下线花了多少（主库 logs），这张表
 * 说按邀请人分组档算下来该返多少 —— 两者的差里装着没有上线 / 被停止计返 /
 * 0% 分组的人，只看这一张会以为日结就是全部的消费。
 */
export function QyAdminInviteAccrualsBody() {
  const { t } = useTranslation()
  const unit = useStardustName()
  const [page, setPage] = useState(1)
  const [date, setDate] = useState(yesterdayInputValue)
  const [inviterId, setInviterId] = useState('')

  const query = useQuery(
    qyAdminInviteAccrualsQuery({
      p: page,
      page_size: QY_PAGE_SIZE,
      day: date.replaceAll('-', ''),
      inviter_id: inviterId.trim(),
    })
  )
  const items = qyArray(query.data?.items)

  const columns: StaticDataTableColumn<QyInviteAccrualRow>[] = [
    {
      id: 'day',
      header: t('qy_sd_col_day'),
      className: staticDataTableClassNames.compactHeaderCell,
      cellClassName: staticDataTableClassNames.compactMutedCell,
      cell: (row) => qySdFormatDay(row.bucket_date),
    },
    {
      id: 'inviter',
      header: t('qy_rel_col_inviter'),
      className: staticDataTableClassNames.compactHeaderCell,
      cellClassName: staticDataTableClassNames.compactCell,
      cell: (row) => (
        <span className='flex flex-col'>
          <span>{row.inviter_username || '-'}</span>
          <span className='text-muted-foreground text-xs'>
            #{row.inviter_id}
          </span>
        </span>
      ),
    },
    {
      id: 'invitee',
      header: t('qy_rel_col_invitee'),
      className: staticDataTableClassNames.compactHeaderCell,
      cellClassName: staticDataTableClassNames.compactCell,
      cell: (row) => (
        <span className='flex flex-col'>
          <span>{row.invitee_username || '-'}</span>
          <span className='text-muted-foreground text-xs'>
            #{row.invitee_id}
          </span>
        </span>
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
      id: 'rate',
      header: t('qy_inv_a_col_rate'),
      className: staticDataTableClassNames.compactHeaderCellRight,
      cellClassName: staticDataTableClassNames.compactMutedNumericCell,
      // 「按哪一档、多少」并排：比例本身不说明它从哪来。
      cell: (row) =>
        `${row.rate_group || '-'} · ${t('qy_sd_percent', {
          percent: qySdBpsPercent(row.bps),
        })}`,
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
        const holdKey = qySdHoldReasonKey(row.hold_reason)
        return (
          <span className='inline-flex flex-wrap items-center gap-1.5'>
            <QyStatusBadge
              status={badge.status}
              label={badge.labelKey === '' ? undefined : t(badge.labelKey)}
            />
            {row.status === 'held' && holdKey != null && (
              <span className='text-muted-foreground text-xs'>
                {t(holdKey, { reason: row.hold_reason })}
              </span>
            )}
          </span>
        )
      },
    },
    {
      id: 'ledger_no',
      header: t('qy_sd_col_ledger_no'),
      className: staticDataTableClassNames.compactHeaderCell,
      cellClassName: staticDataTableClassNames.compactMutedCodeCell,
      cell: (row) => (row.ledger_no === '' ? '-' : row.ledger_no),
    },
  ]

  return (
    <div className='space-y-3'>
      <p className='text-muted-foreground text-sm'>
        {t('qy_inv_a_accruals_hint', { unit })}
      </p>
      <div className='flex flex-wrap items-end gap-2'>
        <label className='flex flex-col gap-1 text-xs'>
          {t('qy_inv_daily_date')}
          <Input
            type='date'
            value={date}
            onChange={(event) => {
              setDate(event.target.value)
              setPage(1)
            }}
          />
        </label>
        <label className='flex flex-col gap-1 text-xs'>
          {t('qy_rel_col_inviter')}
          <Input
            className='w-40'
            inputMode='numeric'
            value={inviterId}
            placeholder={t('qy_rel_inviter_id_ph')}
            onChange={(event) => {
              setInviterId(event.target.value)
              setPage(1)
            }}
          />
        </label>
      </div>
      {query.data !== undefined && (
        <p className='text-muted-foreground text-sm'>
          {t('qy_inv_a_accruals_summary', { total: query.data.total })}
        </p>
      )}

      <QyPageBoundary
        query={query}
        isEmpty={query.data != null && items.length === 0}
        emptyIcon={CalendarClock}
        emptyTitle={t('qy_inv_a_accruals_empty_title')}
        emptyDescription={t('qy_inv_a_accruals_empty_desc')}
      >
        <div className='w-full overflow-x-auto'>
          <StaticDataTable
            columns={columns}
            data={items}
            getRowKey={(row) =>
              `${row.inviter_id}-${row.invitee_id}-${row.bucket_date}`
            }
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
