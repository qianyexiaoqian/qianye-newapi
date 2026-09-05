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
import { Link2, Users } from 'lucide-react'
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
import { Tabs, TabsContent, TabsList, TabsTrigger } from '@/components/ui/tabs'

import { QyPageBoundary } from '../../components/qy-page-boundary'
import { QySdAmount } from '../../components/qy-sd-amount'
import { qyArray } from '../../lib/array'
import { formatSd } from '../../lib/format-sd'
import { qyTabTarget } from '../../lib/pages'
import { QyAdminCommissionBalancesBody } from '../admin-commission-balances'
import { AdjustCommissionDialog } from '../admin-commission-balances/components/adjust-commission-dialog'
import { QyPager } from '../components/qy-pager'
import { QY_PAGE_SIZE } from '../lib/constants'
import { qyAdminCommissionUsersQuery } from './api'
import { QyAdminCommissionCreditsTable } from './components/credits-table'
import { UserCommissionDrilldown } from './components/user-commission-drilldown'
import {
  QY_COMMISSION_USER_FILTERS,
  QY_COMMISSION_USER_SORTS,
  QY_COMMISSION_USER_SORT_LABEL_KEY,
  type QyCommissionUser,
  type QyCommissionUserFilter,
  type QyCommissionUserSort,
} from './types'

/**
 * 「佣金用户」——「结算台」的第三张标签（D-15 恢复）。
 *
 * 三个次级标签回答同一件事的三个切面：
 *   · 用户总览：**一行 = 一个用户**，上线是谁、拉了多少人、四列额度、行内改佣金；
 *   · 余额对账：恒等式与 `ledger_drift`，改钱之前先看这一张；
 *   · 入账记录：佣金余额 → 星屑余额的每一笔入账，带着星屑流水号。
 *
 * D-14 之前这一行是侧栏上独立的「用户佣金」宿主（用户总览 / AFF 关系 / 佣金余额）。
 * AFF 关系归了 invite 模块（「邀请管理」），「提现」那一档永久删除，剩下的两张
 * 加上新的入账记录并成这一张标签 —— 侧栏因此只多回「结算台」一行。
 *
 * ── 金额一律走 `QyAmountText` ──
 * 账本以额度整数记账，界面按站内展示单位印；运营要拿它和用户在钱包页看到的
 * 余额对话，两处口径不同就会得出"系统少算了他的钱"这种结论。
 *
 * 次级标签**刻意不进 hash**：hash 只表达"选择夹选了哪一格"这一层。
 */
export function QyAdminCommissionUsersBody() {
  const { t } = useTranslation()

  return (
    <Tabs defaultValue='users' className='gap-3'>
      <TabsList>
        <TabsTrigger value='users'>{t('qy_cu_title')}</TabsTrigger>
        <TabsTrigger value='balances'>{t('qy_cb_title')}</TabsTrigger>
        <TabsTrigger value='credits'>{t('qy_cu_tab_credits')}</TabsTrigger>
      </TabsList>
      <TabsContent value='users'>
        <UsersTable />
      </TabsContent>
      <TabsContent value='balances'>
        <QyAdminCommissionBalancesBody />
      </TabsContent>
      <TabsContent value='credits'>
        <QyAdminCommissionCreditsTable />
      </TabsContent>
    </Tabs>
  )
}

function UsersTable() {
  const { t } = useTranslation()

  const [page, setPage] = useState(1)
  const [sort, setSort] = useState<QyCommissionUserSort>('available')
  const [keyword, setKeyword] = useState('')
  const [flags, setFlags] = useState<QyCommissionUserFilter[]>([])

  const [drilldown, setDrilldown] = useState<QyCommissionUser | null>(null)
  const [adjustTarget, setAdjustTarget] = useState<QyCommissionUser | null>(
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
  const items = qyArray(query.data?.items)
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
            {/* `blocked` 说的是「**他作为下线**的这条关系被停了」——
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
      // 「拉了多少人，点开能看到都是谁」—— 数字本身就是下钻入口。
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
      header: t('qy_cb_available_xh'),
      className: staticDataTableClassNames.compactHeaderCellRight,
      cellClassName: staticDataTableClassNames.compactNumericCell,
      cell: (row) => <QySdAmount amount={row.available} />,
    },
    {
      id: 'credited',
      header: t('qy_cb_credited'),
      className: staticDataTableClassNames.compactHeaderCellRight,
      cellClassName: staticDataTableClassNames.compactMutedNumericCell,
      cell: (row) => <QySdAmount amount={row.credited} />,
    },
    {
      id: 'earned',
      header: t('qy_cb_earned'),
      className: staticDataTableClassNames.compactHeaderCellRight,
      cellClassName: staticDataTableClassNames.compactNumericCell,
      cell: (row) => <QySdAmount amount={row.total_earned} />,
    },
    {
      id: 'clawback',
      header: t('qy_cb_clawback'),
      className: staticDataTableClassNames.compactHeaderCellRight,
      cellClassName: staticDataTableClassNames.compactMutedNumericCell,
      cell: (row) => <QySdAmount amount={row.total_clawback} />,
    },
    {
      id: 'check',
      header: t('qy_cb_check'),
      className: staticDataTableClassNames.compactHeaderCell,
      cellClassName: staticDataTableClassNames.compactCell,
      // 三态，一个都不能省：没有余额行 / 漂移为 0 / 漂移非 0。"0"与"没有这一行"
      // 含义不同，混成一个 0 会让对账时找错方向。
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

      {/* 绑定 / 换绑 / 解绑 / 停止计返在「邀请管理」里做：那是 invite 模块的事，
          佣金与星屑两条线共用同一条关系。这里只指路，不再各写一份。 */}
      <div className='flex flex-wrap gap-2'>
        <Button
          variant='outline'
          size='sm'
          render={<Link {...qyTabTarget('/qy/admin/invite')} />}
        >
          <Link2 aria-hidden='true' />
          {t('qy_nav_a_invite')}
        </Button>
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
              {t(QY_COMMISSION_USER_SORT_LABEL_KEY[value])}
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
      {/* 手工增减佣金复用「余额对账」那一个弹窗：同一个动作在两张表上走两份
          实现，就是两套各自漂移的上限校验与幂等键。 */}
      <AdjustCommissionDialog
        balance={adjustTarget}
        onClose={() => setAdjustTarget(null)}
      />
    </div>
  )
}

/**
 * 对账列的三态徽章。
 *
 * 单独一个组件而不是在单元格里写嵌套三元：这三个分支表达的是**账本健康度**
 * 这个稳定的领域概念（没有账 / 对得上 / 已漂移）。它同时是这一页上唯一"改钱
 * 之前必须先看"的信号。
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
      {t('qy_cb_check_drift', {
        drift: formatSd(row.ledger_drift),
      })}
    </Badge>
  )
}
