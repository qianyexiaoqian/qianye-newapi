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
import { NativeSelect, NativeSelectOption } from '@/components/ui/native-select'

import { QyAmountText } from '../../components/qy-amount-text'
import { QyPageBoundary } from '../../components/qy-page-boundary'
import { qyArray } from '../../lib/array'
import { formatQyQuotaLedger } from '../../lib/format'
import { qyTabTarget } from '../../lib/pages'
import { QyPager } from '../components/qy-pager'
import { QY_PAGE_SIZE } from '../lib/constants'
import { qyAdminBalancesQuery } from './api'
import { AdjustCommissionDialog } from './components/adjust-commission-dialog'
import {
  QY_BALANCE_SORTS,
  QY_BALANCE_SORT_LABEL_KEY,
  type QyBalanceSort,
  type QyCommissionBalance,
} from './types'

/**
 * 佣金余额对账（D-15 恢复；「登记已提现」不回来）。
 *
 * ── 为什么第一屏就是一条公式 ──
 * 这张表的四个额度列受同一条恒等式约束：
 *
 *   可用 + 入账中 + 已入账 = 累计已结算 − 累计冲正
 *
 * 运营在这个页面上最常问的两个问题——"他明明有佣金为什么还没进星辉""这个人的
 * 可用为什么少了"——答案全在这条式子里：钱要么还在入账中（资金单未落定），
 * 要么已经计入已入账。只给一个"可用"数字，这两个问题永远要靠翻代码回答。
 *
 * `derived_available_quota` / `ledger_drift` 由后端算好下发，本页一个字都不重算。
 *
 * ── 为什么是 Body 而不是整页 ──
 * 本页是「佣金用户」标签下的一个次级标签（余额对账），区段头由宿主页出。
 */
export function QyAdminCommissionBalancesBody() {
  const { t } = useTranslation()

  const [page, setPage] = useState(1)
  const [sort, setSort] = useState<QyBalanceSort>('available')
  const [userId, setUserId] = useState('')
  const [username, setUsername] = useState('')
  const [adjustTarget, setAdjustTarget] = useState<QyCommissionBalance | null>(
    null
  )

  const query = useQuery(
    qyAdminBalancesQuery({
      p: page,
      page_size: QY_PAGE_SIZE,
      sort,
      user_id: userId.trim(),
      username: username.trim(),
    })
  )
  const items = qyArray(query.data?.items)
  const totals = query.data?.totals

  const columns: StaticDataTableColumn<QyCommissionBalance>[] = [
    {
      id: 'user',
      header: t('qy_common_user'),
      className: staticDataTableClassNames.compactHeaderCell,
      cellClassName: staticDataTableClassNames.compactCell,
      cell: (row) => (
        <span className='flex flex-col'>
          <span>{row.user_resolved ? row.username : t('qy_cb_user_gone')}</span>
          <span className='text-muted-foreground text-xs'>#{row.user_id}</span>
        </span>
      ),
    },
    {
      id: 'earned',
      header: t('qy_cb_earned'),
      className: staticDataTableClassNames.compactHeaderCellRight,
      cellClassName: staticDataTableClassNames.compactNumericCell,
      cell: (row) => <QyAmountText quota={row.total_earned_quota} />,
    },
    {
      id: 'clawback',
      header: t('qy_cb_clawback'),
      className: staticDataTableClassNames.compactHeaderCellRight,
      cellClassName: staticDataTableClassNames.compactMutedNumericCell,
      cell: (row) => <QyAmountText quota={row.total_clawback_quota} />,
    },
    {
      id: 'frozen',
      header: t('qy_cb_frozen_xh'),
      className: staticDataTableClassNames.compactHeaderCellRight,
      cellClassName: staticDataTableClassNames.compactMutedNumericCell,
      cell: (row) => <QyAmountText quota={row.frozen_quota} />,
    },
    {
      id: 'credited',
      header: t('qy_cb_credited'),
      className: staticDataTableClassNames.compactHeaderCellRight,
      cellClassName: staticDataTableClassNames.compactNumericCell,
      cell: (row) => <QyAmountText quota={row.credited_quota} />,
    },
    {
      id: 'available',
      header: t('qy_cb_available_xh'),
      className: staticDataTableClassNames.compactHeaderCellRight,
      cellClassName: staticDataTableClassNames.compactNumericCell,
      cell: (row) => <QyAmountText quota={row.available_quota} />,
    },
    {
      id: 'check',
      header: t('qy_cb_check'),
      className: staticDataTableClassNames.compactHeaderCell,
      cellClassName: staticDataTableClassNames.compactCell,
      cell: (row) => (
        <span className='inline-flex flex-wrap items-center gap-1.5'>
          {row.ledger_drift === 0 ? (
            <Badge variant='secondary'>{t('qy_cb_check_ok')}</Badge>
          ) : (
            // 漂移必须在改钱**之前**被看见：这一行的账本与结算流水已经对不上了。
            <Badge variant='destructive'>
              {t('qy_cb_check_drift', {
                drift: formatQyQuotaLedger(row.ledger_drift),
              })}
            </Badge>
          )}
          {row.debt_blocked && (
            <Badge variant='outline'>{t('qy_cb_debt_blocked')}</Badge>
          )}
        </span>
      ),
    },
    {
      id: 'actions',
      header: t('qy_common_actions'),
      className: staticDataTableClassNames.actionHeaderCell,
      cellClassName: staticDataTableClassNames.actionCell,
      cell: (row) => (
        <div className='flex justify-end gap-1'>
          {/* 下钻:这一行的四个额度是聚合值,"他这 137 额度是怎么来的"只有逐笔
              计佣答得了。带着 inviter_id 跳过去,佣金审核页会用它做筛选初值。
              目标走 `qyTabTarget`:佣金审核是「结算台」的第二张标签。 */}
          <Button
            variant='ghost'
            size='sm'
            render={
              <Link
                {...qyTabTarget('/qy/admin/commission-records')}
                search={{ inviter_id: String(row.user_id) }}
              />
            }
          >
            {t('qy_cb_accruals')}
          </Button>
          <Button
            variant='ghost'
            size='sm'
            onClick={() => setAdjustTarget(row)}
          >
            {t('qy_adj_action')}
          </Button>
        </div>
      ),
    },
  ]

  const resetPage = () => setPage(1)

  return (
    <div className='space-y-3'>
      <div className='bg-muted/40 text-muted-foreground rounded-md border p-3 text-xs'>
        <p className='text-foreground font-medium'>{t('qy_cb_formula_xh')}</p>
        <p className='mt-1'>{t('qy_cb_formula_hint_xh')}</p>
      </div>

      <div className='flex flex-wrap items-center gap-2'>
        <Input
          className='h-8 w-40'
          inputMode='numeric'
          value={userId}
          placeholder={t('qy_cb_user_id_ph')}
          onChange={(event) => {
            resetPage()
            setUserId(event.target.value)
          }}
        />
        <Input
          className='h-8 w-48'
          value={username}
          placeholder={t('qy_cb_username_ph')}
          onChange={(event) => {
            resetPage()
            setUsername(event.target.value)
          }}
        />
        <NativeSelect
          size='sm'
          aria-label={t('qy_cb_sort')}
          value={sort}
          onChange={(event) => {
            resetPage()
            setSort(event.target.value as QyBalanceSort)
          }}
        >
          {QY_BALANCE_SORTS.map((value) => (
            <NativeSelectOption key={value} value={value}>
              {t(QY_BALANCE_SORT_LABEL_KEY[value])}
            </NativeSelectOption>
          ))}
        </NativeSelect>
      </div>

      {totals != null && (
        <p className='text-muted-foreground text-xs'>
          {t('qy_cb_totals_xh', {
            available: formatQyQuotaLedger(totals.available_quota),
            credited: formatQyQuotaLedger(totals.credited_quota),
          })}
        </p>
      )}

      <QyPageBoundary
        query={query}
        isEmpty={items.length === 0}
        emptyIcon={Wallet}
        emptyTitle={t('qy_cb_empty_title')}
        emptyDescription={t('qy_cb_empty_desc')}
      >
        <div className='w-full overflow-x-auto'>
          <StaticDataTable
            columns={columns}
            data={items}
            getRowKey={(row) => row.user_id}
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

      <AdjustCommissionDialog
        balance={adjustTarget}
        onClose={() => setAdjustTarget(null)}
      />
    </div>
  )
}
