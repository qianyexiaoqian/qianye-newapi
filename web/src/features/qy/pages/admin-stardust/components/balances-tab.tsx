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
import { Wallet } from 'lucide-react'
import { useState } from 'react'
import { useTranslation } from 'react-i18next'

import {
  StaticDataTable,
  staticDataTableClassNames,
  type StaticDataTableColumn,
} from '@/components/data-table'
import { Badge } from '@/components/ui/badge'
import { Button } from '@/components/ui/button'
import { Input } from '@/components/ui/input'

import { QyPageBoundary } from '../../../components/qy-page-boundary'
import { QySdAmount } from '../../../components/qy-sd-amount'
import { useStardustName } from '../../../hooks/use-stardust-name'
import { qyArray } from '../../../lib/array'
import { QyPager } from '../../components/qy-pager'
import { QY_PAGE_SIZE } from '../../lib/constants'
import { formatQyTs } from '../../ops/format'
import { qySdHoldReasonKey } from '../../stardust/lib/display'
import { qyAdminStardustBalancesQuery } from '../api'
import type { QyStardustBalanceRow } from '../types'
import type { QySdAdjustTarget } from './adjust-stardust-dialog'

type BalancesTabProps = {
  /** 超级管理员才有：一行上的「手调」。role<100 时不传，行上就没有那颗按钮。 */
  onAdjust?: (target: QySdAdjustTarget) => void
  /** 跳到「流水」标签并按这个人筛。 */
  onShowLedger: (userId: number) => void
}

/**
 * 「余额」—— 一行一个用户。
 *
 * 五个数字受同一条恒等式约束（可用 = 累计获得 − 累计支出 + 累计退回 + 累计调整），
 * 是否真的相等由「结算」标签里的体检回答，这里一个数都不重算。`carry` 是不足
 * 1 星屑的余数（decimal 字符串，只展示）。关键词检索走主库（id / 用户名 /
 * 邮箱前缀），所以余额行上一个已删账号仍然显示它当初叫什么。
 */
export function QySdAdminBalancesTab(props: BalancesTabProps) {
  const { t } = useTranslation()
  const unit = useStardustName()
  const [page, setPage] = useState(1)
  const [keyword, setKeyword] = useState('')
  const [userId, setUserId] = useState('')

  const query = useQuery(
    qyAdminStardustBalancesQuery({
      page,
      page_size: QY_PAGE_SIZE,
      ...(keyword.trim() === '' ? {} : { keyword: keyword.trim() }),
      ...(/^\d+$/.test(userId) ? { user_id: Number(userId) } : {}),
    })
  )
  const items = qyArray(query.data?.items)

  const columns: StaticDataTableColumn<QyStardustBalanceRow>[] = [
    {
      id: 'user',
      header: t('qy_common_user'),
      className: staticDataTableClassNames.compactHeaderCell,
      cellClassName: staticDataTableClassNames.compactCell,
      cell: (row) => (
        <span className='flex flex-col'>
          <span>
            {row.username === '' ? t('qy_sdadm_user_gone') : row.username}
          </span>
          <span className='text-muted-foreground text-xs'>#{row.user_id}</span>
        </span>
      ),
    },
    {
      id: 'available',
      header: t('qy_sdadm_col_available'),
      className: staticDataTableClassNames.compactHeaderCellRight,
      cellClassName: staticDataTableClassNames.compactNumericCell,
      cell: (row) => <QySdAmount amount={row.available} />,
    },
    {
      id: 'earned',
      header: t('qy_sd_total_earned'),
      className: staticDataTableClassNames.compactHeaderCellRight,
      cellClassName: staticDataTableClassNames.compactMutedNumericCell,
      cell: (row) => <QySdAmount amount={row.total_earned} />,
    },
    {
      id: 'spent',
      header: t('qy_sd_total_spent'),
      className: staticDataTableClassNames.compactHeaderCellRight,
      cellClassName: staticDataTableClassNames.compactMutedNumericCell,
      cell: (row) => <QySdAmount amount={row.total_spent} />,
    },
    {
      id: 'refunded',
      header: t('qy_sd_total_refunded'),
      className: staticDataTableClassNames.compactHeaderCellRight,
      cellClassName: staticDataTableClassNames.compactMutedNumericCell,
      cell: (row) => <QySdAmount amount={row.total_refunded} />,
    },
    {
      id: 'adjusted',
      header: t('qy_sd_total_adjusted'),
      className: staticDataTableClassNames.compactHeaderCellRight,
      cellClassName: staticDataTableClassNames.compactMutedNumericCell,
      cell: (row) => <QySdAmount amount={row.total_adjusted} signed />,
    },
    {
      id: 'carry',
      header: t('qy_sd_carry_label'),
      className: staticDataTableClassNames.compactHeaderCellRight,
      cellClassName: staticDataTableClassNames.compactMutedNumericCell,
      cell: (row) => row.carry,
    },
    {
      id: 'hold',
      header: t('qy_sdadm_col_hold'),
      className: staticDataTableClassNames.compactHeaderCell,
      cellClassName: staticDataTableClassNames.compactCell,
      cell: (row) => <HoldCell reason={row.hold_reason} />,
    },
    {
      id: 'updated_at',
      header: t('qy_common_updated_at'),
      className: staticDataTableClassNames.compactHeaderCell,
      cellClassName: staticDataTableClassNames.compactMutedCell,
      cell: (row) => formatQyTs(row.updated_at),
    },
    {
      id: 'actions',
      header: t('qy_common_actions'),
      className: staticDataTableClassNames.actionHeaderCell,
      cellClassName: staticDataTableClassNames.actionCell,
      cell: (row) => (
        <div className='flex justify-end gap-1'>
          <Button
            variant='ghost'
            size='sm'
            onClick={() => props.onShowLedger(row.user_id)}
          >
            {t('qy_nav_stardust_ledger')}
          </Button>
          {props.onAdjust != null && (
            <Button
              variant='ghost'
              size='sm'
              onClick={() =>
                props.onAdjust?.({
                  user_id: row.user_id,
                  username: row.username,
                  available: row.available,
                })
              }
            >
              {t('qy_sdadm_adjust_action')}
            </Button>
          )}
        </div>
      ),
    },
  ]

  return (
    <div className='space-y-3'>
      <div className='flex flex-wrap items-center gap-2'>
        <Input
          className='h-8 w-56'
          value={keyword}
          placeholder={t('qy_sdadm_keyword_ph')}
          onChange={(event) => {
            setPage(1)
            setKeyword(event.target.value)
          }}
        />
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
      </div>
      <p className='text-muted-foreground text-xs'>
        {t('qy_sdadm_balances_formula', { unit })}
      </p>

      <QyPageBoundary
        query={query}
        isEmpty={query.data != null && items.length === 0}
        emptyIcon={Wallet}
        emptyTitle={t('qy_sdadm_balances_empty_title')}
        emptyDescription={t('qy_sdadm_balances_empty_desc')}
      >
        <div className='w-full overflow-x-auto'>
          <StaticDataTable
            columns={columns}
            data={items}
            getRowKey={(row) => row.user_id}
            tableClassName='min-w-[1200px]'
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

/** 暂缓标记。正常的人这一格留空，被扣住的人一枚红徽章 + 原因。 */
function HoldCell(props: { reason: QyStardustBalanceRow['hold_reason'] }) {
  const { t } = useTranslation()
  const key = qySdHoldReasonKey(props.reason)
  if (key == null) return <span className='text-muted-foreground'>-</span>
  return <Badge variant='destructive'>{t(key, { reason: props.reason })}</Badge>
}
