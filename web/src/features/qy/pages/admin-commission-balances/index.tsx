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
import { QyPager } from '../components/qy-pager'
import { QY_PAGE_SIZE } from '../lib/constants'
import { qyAdminBalancesQuery } from './api'
import { AdjustCommissionDialog } from './components/adjust-commission-dialog'
import { SetWithdrawnDialog } from './components/set-withdrawn-dialog'
import {
  QY_BALANCE_SORTS,
  type QyBalanceSort,
  type QyCommissionBalance,
} from './types'

/**
 * 佣金余额总览 + 已提现额度迁移编辑。
 *
 * ── 为什么第一屏就是一条公式 ──
 * 这张表的四个额度列受同一条恒等式约束：
 *
 *   可提现 + 冻结中 + 已提现 = 累计已结算 − 累计冲正
 *
 * 运营在这个页面上最常问的两个问题——"他明明有佣金为什么提不了""这个人的
 * 可提现为什么少了"——答案全在这条式子里。只给一个"可提现"数字，这两个问题
 * 永远要靠翻代码回答，所以公式与逐列拆解必须同屏。
 *
 * `derived_available_quota` / `ledger_drift` 由后端算好下发，本页一个字都不重算：
 * 这条恒等式在后端已经被结算 / 冲正 / 提现三条路径各实现了一遍，
 * 前端再实现第四遍就是在等它漂移。
 *
 * ── 为什么是 Body 而不是整页 ──
 * 本页已被收进「用户佣金」的选择夹（`QY_TAB_GROUPS`），侧栏上不再有独立的
 * 一行。区段头（`GATE NN` + 大标题）由宿主页 `admin-commission-users/
 * hub.tsx` 出，这里只提供正文 —— 标签里再套一层区段头会得到两级标题。
 * 旧地址 `/qy/admin/commission-records/balances` 保留成重定向。
 */
export function QyAdminCommissionBalancesBody() {
  const { t } = useTranslation()

  const [page, setPage] = useState(1)
  const [sort, setSort] = useState<QyBalanceSort>('available')
  const [userId, setUserId] = useState('')
  const [username, setUsername] = useState('')
  const [target, setTarget] = useState<QyCommissionBalance | null>(null)
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
  const items = query.data?.items ?? []
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
      header: t('qy_cb_frozen'),
      className: staticDataTableClassNames.compactHeaderCellRight,
      cellClassName: staticDataTableClassNames.compactMutedNumericCell,
      cell: (row) => <QyAmountText quota={row.frozen_quota} />,
    },
    {
      id: 'withdrawn',
      header: t('qy_cb_withdrawn'),
      className: staticDataTableClassNames.compactHeaderCellRight,
      cellClassName: staticDataTableClassNames.compactNumericCell,
      cell: (row) => <QyAmountText quota={row.withdrawn_quota} />,
    },
    {
      id: 'available',
      header: t('qy_cb_available'),
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
              {t('qy_cb_check_drift', { drift: row.ledger_drift })}
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
              计佣答得了。带着 inviter_id 跳过去,佣金审核页会用它做筛选初值 ——
              这一跳此前不存在,运营只能记住用户 ID、自己走去佣金审核再手打一遍。 */}
          <Button
            variant='ghost'
            size='sm'
            render={
              <Link
                to='/qy/admin/commission-records'
                search={{ inviter_id: String(row.user_id) }}
              />
            }
          >
            {t('qy_cb_accruals')}
          </Button>
          {/* 两个动作的语义完全不同,不能合成一个按钮:
              「登记已提现」只是把额度从可提现搬到已提现(恒等式两侧不变);
              「手工增减」是真的凭空加钱/扣钱,会落一条 manual 计佣行。 */}
          <Button
            variant='ghost'
            size='sm'
            onClick={() => setAdjustTarget(row)}
          >
            {t('qy_adj_action')}
          </Button>
          <Button variant='ghost' size='sm' onClick={() => setTarget(row)}>
            {t('qy_cb_set')}
          </Button>
        </div>
      ),
    },
  ]

  const resetPage = () => setPage(1)

  return (
    <div className='space-y-3'>
      <div className='bg-muted/40 text-muted-foreground rounded-md border p-3 text-xs'>
        <p className='text-foreground font-medium'>{t('qy_cb_formula')}</p>
        <p className='mt-1'>{t('qy_cb_formula_hint')}</p>
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
              {t(`qy_cb_sort_${value}`)}
            </NativeSelectOption>
          ))}
        </NativeSelect>
      </div>

      {totals != null && (
        <p className='text-muted-foreground text-xs'>
          {t('qy_cb_totals', {
            available: totals.available_quota,
            withdrawn: totals.withdrawn_quota,
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

      <SetWithdrawnDialog balance={target} onClose={() => setTarget(null)} />
      <AdjustCommissionDialog
        balance={adjustTarget}
        onClose={() => setAdjustTarget(null)}
      />
    </div>
  )
}
