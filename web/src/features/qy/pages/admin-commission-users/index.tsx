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
import { Button } from '@/components/ui/button'
import { Input } from '@/components/ui/input'
import { Label } from '@/components/ui/label'
import { NativeSelect, NativeSelectOption } from '@/components/ui/native-select'
import { Switch } from '@/components/ui/switch'

import { QyAmountText } from '../../components/qy-amount-text'
import { QyPageBoundary } from '../../components/qy-page-boundary'
import { AdjustCommissionDialog } from '../admin-commission-balances/components/adjust-commission-dialog'
import { QyPager } from '../components/qy-pager'
import { QY_PAGE_SIZE } from '../lib/constants'
import { qyAdminCommissionUsersQuery } from './api'
import { ManageRelationDialog } from './components/manage-relation-dialog'
import { UserCommissionDrilldown } from './components/user-commission-drilldown'
import {
  QY_COMMISSION_USER_FILTERS,
  QY_COMMISSION_USER_SORTS,
  type QyCommissionUser,
  type QyCommissionUserFilter,
  type QyCommissionUserSort,
} from './types'

/**
 * 「用户佣金」——**一行 = 一个用户**。
 *
 * ── 它与站内另外三张佣金表的分工 ──
 * 项目方原话：「我需要的是新增一个用户佣金列表，我可以查看/编辑用户的佣金，
 * 以及查看拉了多少用户，以及编辑/移除/添加这个用户的佣金绑定关系。」
 *
 * 已有的表一张都答不了这句话，因为它们的**主键不是人**：
 *   · 「计佣流水」一行 = 一笔计佣（`accrual_no`）；
 *   · 「AFF 关系」一行 = 一条邀请关系（邀请人 × 被邀请人）；
 *   · 「佣金余额」一行 = 一个用户的**余额**，但它既看不到上线、也不能改关系。
 *
 * 本页把"关于这个人的全部佣金事务"收在同一行上：上线是谁、拉了多少人、
 * 五列额度、以及三个行内动作（下钻 / 改佣金 / 改绑定）。
 *
 * ── 金额一律走 `QyAmountText` ──
 * 裸 quota 整数（`137200`）在这张表上尤其危险：运营要拿它和用户在钱包页看到
 * 的余额对话，而那一边显示的是站内余额口径。两处口径不同就会得出"系统少算了
 * 他的钱"这种结论。
 *
 * ── 本页不自己算任何一个数 ──
 * `derived_available_quota` / `ledger_drift` 与「佣金余额」标签同一实现
 * （后端 `newBalanceView`），前端因此不存在第二份恒等式算法。
 */
export function QyAdminCommissionUsersBody() {
  const { t } = useTranslation()

  const [page, setPage] = useState(1)
  const [sort, setSort] = useState<QyCommissionUserSort>('available')
  const [keyword, setKeyword] = useState('')
  const [flags, setFlags] = useState<QyCommissionUserFilter[]>([])

  const [drilldown, setDrilldown] = useState<QyCommissionUser | null>(null)
  const [adjustTarget, setAdjustTarget] = useState<QyCommissionUser | null>(
    null
  )
  const [relationTarget, setRelationTarget] = useState<QyCommissionUser | null>(
    null
  )

  const query = useQuery(
    qyAdminCommissionUsersQuery({
      p: page,
      page_size: QY_PAGE_SIZE,
      sort,
      keyword: keyword.trim(),
      flags,
    })
  )
  const items = query.data?.items ?? []
  const totals = query.data?.totals

  const resetPage = () => setPage(1)
  const toggleFlag = (flag: QyCommissionUserFilter, on: boolean) => {
    resetPage()
    setFlags((current) =>
      on ? [...current, flag] : current.filter((item) => item !== flag)
    )
  }

  const columns: StaticDataTableColumn<QyCommissionUser>[] = [
    {
      id: 'user',
      header: t('qy_common_user'),
      className: staticDataTableClassNames.compactHeaderCell,
      cellClassName: staticDataTableClassNames.compactCell,
      // 展示名优先、用户名兜底；两个都拿不到（`user_resolved` 为假）时**明说**
      // 账号已不存在，而不是渲染一个空格子 —— 对着一个空名字改钱是这一页上最
      // 不该发生的事。
      cell: (row) => (
        <span className='flex flex-col'>
          <span>
            {row.user_resolved
              ? row.display_name || row.username
              : t('qy_cb_user_gone')}
          </span>
          <span className='text-muted-foreground text-xs'>
            #{row.user_id}
            {row.email !== '' && ` · ${row.email}`}
          </span>
          {row.user_group !== '' && (
            <span className='mt-0.5'>
              <Badge variant='outline'>{row.user_group}</Badge>
            </span>
          )}
        </span>
      ),
    },
    {
      id: 'inviter',
      header: t('qy_cu_col_inviter'),
      className: staticDataTableClassNames.compactHeaderCell,
      cellClassName: staticDataTableClassNames.compactCell,
      // 「他自己的邀请人是谁」。0 不是一个用户 id 而是"没有上线"，
      // 照着 `#0` 渲染会让运营去查一个不存在的账号。
      cell: (row) =>
        row.inviter_id > 0 ? (
          <span className='flex flex-col'>
            <span>
              {row.inviter_resolved
                ? row.inviter_username
                : t('qy_rel_user_gone')}
            </span>
            <span className='text-muted-foreground text-xs'>
              #{row.inviter_id}
            </span>
            {/* `blocked` 说的是「**他作为下线**的这条关系被拉黑了」——
                他的消费不再给上线计佣。它不是"这个账号被封了"，所以徽章挂在
                上线这一列上，而不是挂在用户名旁边。 */}
            {row.inviter_blocked && (
              <span className='mt-0.5'>
                <Badge variant='destructive'>{t('qy_rel_state_blocked')}</Badge>
              </span>
            )}
          </span>
        ) : (
          <span className='text-muted-foreground'>{t('qy_cu_no_inviter')}</span>
        ),
    },
    {
      id: 'invitees',
      header: t('qy_cu_col_invitees'),
      className: staticDataTableClassNames.compactHeaderCellRight,
      cellClassName: staticDataTableClassNames.compactNumericCell,
      // 「拉了多少人，点开能看到都是谁」—— 数字本身就是下钻入口，
      // 而不是另外再放一个按钮：运营看到 12 的第一反应就是想点它。
      cell: (row) => (
        <span className='inline-flex items-center gap-1.5'>
          <Button
            variant='link'
            size='sm'
            className='h-auto p-0 tabular-nums'
            disabled={row.invitee_count === 0}
            onClick={() => setDrilldown(row)}
          >
            {row.invitee_count}
          </Button>
          {row.blocked_invitee_count > 0 && (
            <Badge variant='outline'>
              {t('qy_cu_blocked_invitees', {
                count: row.blocked_invitee_count,
              })}
            </Badge>
          )}
        </span>
      ),
    },
    {
      id: 'available',
      header: t('qy_cb_available'),
      className: staticDataTableClassNames.compactHeaderCellRight,
      cellClassName: staticDataTableClassNames.compactNumericCell,
      cell: (row) => <QyAmountText quota={row.available_quota} />,
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
      cellClassName: staticDataTableClassNames.compactMutedNumericCell,
      cell: (row) => <QyAmountText quota={row.withdrawn_quota} />,
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
      id: 'check',
      header: t('qy_cb_check'),
      className: staticDataTableClassNames.compactHeaderCell,
      cellClassName: staticDataTableClassNames.compactCell,
      // 三态，一个都不能省：
      //   没有余额行 → 这个人一分佣金都没产生过。与"余额是 0"（产生过又走完了）
      //                含义不同，混成一个 0 会让对账时找错方向；
      //   漂移为 0   → 账本与结算流水对得上；
      //   漂移非 0   → 这一行已经对不上了，**改钱之前**必须先查清楚。
      cell: (row) => (
        <span className='inline-flex flex-wrap items-center gap-1.5'>
          <LedgerCheckBadge row={row} />
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
          <Button variant='ghost' size='sm' onClick={() => setDrilldown(row)}>
            {t('qy_cu_view')}
          </Button>
          <Button
            variant='ghost'
            size='sm'
            onClick={() => setAdjustTarget(row)}
          >
            {t('qy_adj_action')}
          </Button>
          <Button
            variant='ghost'
            size='sm'
            onClick={() => setRelationTarget(row)}
          >
            {t('qy_cu_manage_relation')}
          </Button>
        </div>
      ),
    },
  ]

  return (
    <div className='space-y-3'>
      <div className='bg-muted/40 text-muted-foreground rounded-md border p-3 text-xs'>
        <p className='text-foreground font-medium'>{t('qy_cu_scope')}</p>
        <p className='mt-1'>{t('qy_cu_scope_hint')}</p>
      </div>

      <div className='flex flex-wrap items-center gap-3'>
        <Input
          className='h-8 w-64'
          value={keyword}
          placeholder={t('qy_cu_keyword_ph')}
          onChange={(event) => {
            resetPage()
            setKeyword(event.target.value)
          }}
        />
        <NativeSelect
          size='sm'
          aria-label={t('qy_cb_sort')}
          value={sort}
          onChange={(event) => {
            resetPage()
            setSort(event.target.value as QyCommissionUserSort)
          }}
        >
          {QY_COMMISSION_USER_SORTS.map((value) => (
            <NativeSelectOption key={value} value={value}>
              {t(`qy_cu_sort_${value}`)}
            </NativeSelectOption>
          ))}
        </NativeSelect>

        {QY_COMMISSION_USER_FILTERS.map((flag) => (
          <Label
            key={flag}
            className='text-muted-foreground flex items-center gap-1.5 text-xs'
          >
            <Switch
              checked={flags.includes(flag)}
              onCheckedChange={(on) => toggleFlag(flag, on)}
            />
            {t(`qy_cu_filter_${flag}`)}
          </Label>
        ))}
      </div>

      {totals != null && (
        <p className='text-muted-foreground text-xs'>
          {t('qy_cu_totals', {
            users: totals.user_count,
            invitees: totals.invitee_count,
          })}
        </p>
      )}

      <QyPageBoundary
        query={query}
        isEmpty={items.length === 0}
        emptyIcon={Users}
        emptyTitle={t('qy_cu_empty_title')}
        emptyDescription={t('qy_cu_empty_desc')}
      >
        <div className='w-full overflow-x-auto'>
          <StaticDataTable
            columns={columns}
            data={items}
            getRowKey={(row) => row.user_id}
            tableClassName='min-w-[1400px]'
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

      <UserCommissionDrilldown
        user={drilldown}
        onClose={() => setDrilldown(null)}
      />
      {/* 手工增减佣金复用「佣金余额」那一个弹窗：同一个动作在两张表上走两份
          实现，就是两套各自漂移的上限校验与幂等键。它只需要五个字段
          （见 `QyAdjustSubject`），本表的行结构上就满足。 */}
      <AdjustCommissionDialog
        balance={adjustTarget}
        onClose={() => setAdjustTarget(null)}
      />
      <ManageRelationDialog
        user={relationTarget}
        onClose={() => setRelationTarget(null)}
      />
    </div>
  )
}

/**
 * 对账列的三态徽章。
 *
 * 单独一个组件而不是在单元格里写嵌套三元：这三个分支表达的是**账本健康度**
 * 这个稳定的领域概念（没有账 / 对得上 / 已漂移），而不是为了让某一行短一点
 * 而机械抽出来的片段。它同时是这一页上唯一"改钱之前必须先看"的信号。
 */
function LedgerCheckBadge(props: { row: QyCommissionUser }) {
  const { t } = useTranslation()
  const { row } = props

  if (!row.has_balance_row) {
    return <Badge variant='outline'>{t('qy_cu_no_ledger')}</Badge>
  }
  if (row.ledger_drift === 0) {
    return <Badge variant='secondary'>{t('qy_cb_check_ok')}</Badge>
  }
  return (
    <Badge variant='destructive'>
      {t('qy_cb_check_drift', { drift: row.ledger_drift })}
    </Badge>
  )
}
