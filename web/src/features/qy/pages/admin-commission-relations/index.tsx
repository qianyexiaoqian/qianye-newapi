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
import { Link2, Plus } from 'lucide-react'
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
import { formatTimestampToDate } from '@/lib/format'

import { QyAmountText } from '../../components/qy-amount-text'
import { QyPageBoundary } from '../../components/qy-page-boundary'
import { QySectionPageLayout } from '../../components/qy-section-page-layout'
import { QyPager } from '../components/qy-pager'
import { QY_PAGE_SIZE } from '../lib/constants'
import { qyAdminRelationsQuery } from './api'
import { BindRelationDialog } from './components/bind-relation-dialog'
import { UnbindRelationDialog } from './components/unbind-relation-dialog'
import {
  QY_RELATION_SCOPES,
  QY_RELATION_SORTS,
  type QyAffRelation,
  type QyRelationScope,
  type QyRelationSort,
} from './types'

/**
 * AFF（邀请）关系列表。
 *
 * ── 数据源为什么是主库而不是扩展库 ──
 * 计佣链路上"谁是这个人的邀请人"只认主库的 `users.inviter_id`；扩展库的
 * `qy_invite_relation` 是**懒建**的展示快照，某个下线第一次产生佣金时才写那一行。
 * 备份库实测：`users` 里 375 条绑定，快照表 8 行。所以本页（绑定中）由后端从主库
 * 分页出，累计佣金再回扩展库按 (邀请人 × 被邀请人) 这一对聚合补上。
 *
 * ── 两个 scope ──
 * 「绑定中」来自主库；「已解绑」只能来自快照——解绑之后主库的 `inviter_id` 已经
 * 清零，"他曾经是谁的下线"在主库里一个字都不剩。已解绑那一页存在的意义就是
 * 回答"这条关系历史上挣了多少、那笔钱还在不在"。
 */
export function QyAdminCommissionRelations() {
  const { t } = useTranslation()

  const [page, setPage] = useState(1)
  const [scope, setScope] = useState<QyRelationScope>('bound')
  const [sort, setSort] = useState<QyRelationSort>('newest')
  const [username, setUsername] = useState('')
  const [inviterId, setInviterId] = useState('')
  const [bindOpen, setBindOpen] = useState(false)
  const [unbindTarget, setUnbindTarget] = useState<QyAffRelation | null>(null)

  const query = useQuery(
    qyAdminRelationsQuery({
      p: page,
      page_size: QY_PAGE_SIZE,
      scope,
      sort,
      username: username.trim(),
      inviter_id: inviterId.trim(),
    })
  )
  const items = query.data?.items ?? []

  const columns: StaticDataTableColumn<QyAffRelation>[] = [
    {
      id: 'inviter',
      header: t('qy_rel_col_inviter'),
      className: staticDataTableClassNames.compactHeaderCell,
      cellClassName: staticDataTableClassNames.compactCell,
      cell: (row) => (
        <span className='flex flex-col'>
          <span>
            {row.inviter_resolved
              ? row.inviter_username
              : t('qy_rel_user_gone')}
          </span>
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
          <span>
            {row.invitee_resolved
              ? row.invitee_username
              : t('qy_rel_user_gone')}
          </span>
          <span className='text-muted-foreground text-xs'>
            #{row.invitee_id}
          </span>
        </span>
      ),
    },
    {
      id: 'bound_at',
      header: t('qy_rel_col_bound_at'),
      className: staticDataTableClassNames.compactHeaderCell,
      cellClassName: staticDataTableClassNames.compactMutedCell,
      // 三态，一个都不能省：
      //   有快照      → 快照记下的绑定时刻，那是实测值；
      //   无快照有注册 → 回落到注册时间并**标注这是推定值**。自动绑定发生在注册
      //                  那一刻，两者对绝大多数关系是同一个时刻，但把推定值伪装成
      //                  实测值正是这个项目最忌讳的事；
      //   两个都是 0  → 直接说"不详"。备份库里的老账号 users.created_at 是 NULL，
      //                  照着 0 渲染会得到"1970-01-01"——一个看起来像数据、
      //                  实际上是缺失的日期。
      cell: (row) => {
        if (row.bound_at > 0) return formatTimestampToDate(row.bound_at)
        if (row.invitee_created_at > 0) {
          return (
            <span className='inline-flex items-center gap-1.5'>
              {formatTimestampToDate(row.invitee_created_at)}
              <Badge variant='outline'>{t('qy_rel_bound_at_inferred')}</Badge>
            </span>
          )
        }
        return <Badge variant='outline'>{t('qy_rel_bound_at_unknown')}</Badge>
      },
    },
    {
      id: 'commission',
      header: t('qy_rel_col_commission'),
      className: staticDataTableClassNames.compactHeaderCellRight,
      cellClassName: staticDataTableClassNames.compactNumericCell,
      cell: (row) => <QyAmountText quota={row.total_commission_quota} />,
    },
    {
      id: 'accruals',
      header: t('qy_rel_col_accruals'),
      className: staticDataTableClassNames.compactHeaderCellRight,
      cellClassName: staticDataTableClassNames.compactMutedNumericCell,
      cell: (row) => row.accrual_count,
    },
    {
      id: 'state',
      header: t('qy_common_status'),
      className: staticDataTableClassNames.compactHeaderCell,
      cellClassName: staticDataTableClassNames.compactCell,
      cell: (row) => (
        <span className='inline-flex flex-wrap items-center gap-1.5'>
          {row.unbound_at > 0 ? (
            <Badge variant='outline'>
              {t('qy_rel_state_unbound', {
                at: formatTimestampToDate(row.unbound_at),
              })}
            </Badge>
          ) : (
            <Badge variant='secondary'>{t('qy_rel_state_bound')}</Badge>
          )}
          {row.blocked && (
            <Badge variant='destructive'>{t('qy_rel_state_blocked')}</Badge>
          )}
          {row.risk_flags !== '' && (
            <Badge variant='outline'>{row.risk_flags}</Badge>
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
        <div className='flex justify-end'>
          {row.unbound_at > 0 ? (
            <span className='text-muted-foreground text-xs'>
              {t('qy_rel_history_only')}
            </span>
          ) : (
            <Button
              variant='ghost'
              size='sm'
              onClick={() => setUnbindTarget(row)}
            >
              {t('qy_rel_unbind')}
            </Button>
          )}
        </div>
      ),
    },
  ]

  const resetPage = () => setPage(1)

  return (
    <QySectionPageLayout>
      <QySectionPageLayout.Title>{t('qy_rel_title')}</QySectionPageLayout.Title>
      <QySectionPageLayout.Actions>
        <Button variant='outline' size='sm' onClick={() => setBindOpen(true)}>
          <Plus aria-hidden='true' />
          {t('qy_rel_bind')}
        </Button>
        <Button
          variant='outline'
          size='sm'
          render={<Link to='/qy/admin/commission-records' />}
        >
          {t('qy_nav_a_commission_records')}
        </Button>
      </QySectionPageLayout.Actions>
      <QySectionPageLayout.Content>
        <div className='space-y-3'>
          <div className='bg-muted/40 text-muted-foreground rounded-md border p-3 text-xs'>
            <p className='text-foreground font-medium'>
              {t('qy_rel_authority')}
            </p>
            <p className='mt-1'>{t('qy_rel_authority_hint')}</p>
          </div>

          <div className='flex flex-wrap items-center gap-2'>
            <NativeSelect
              size='sm'
              aria-label={t('qy_rel_scope')}
              value={scope}
              onChange={(event) => {
                resetPage()
                setScope(event.target.value as QyRelationScope)
              }}
            >
              {QY_RELATION_SCOPES.map((value) => (
                <NativeSelectOption key={value} value={value}>
                  {t(`qy_rel_scope_${value}`)}
                </NativeSelectOption>
              ))}
            </NativeSelect>

            <Input
              className='h-8 w-48'
              value={username}
              placeholder={t('qy_rel_username_ph')}
              onChange={(event) => {
                resetPage()
                setUsername(event.target.value)
              }}
            />
            <Input
              className='h-8 w-40'
              inputMode='numeric'
              value={inviterId}
              placeholder={t('qy_rel_inviter_id_ph')}
              onChange={(event) => {
                resetPage()
                setInviterId(event.target.value)
              }}
            />

            <NativeSelect
              size='sm'
              aria-label={t('qy_rel_sort')}
              value={sort}
              onChange={(event) => {
                resetPage()
                setSort(event.target.value as QyRelationSort)
              }}
            >
              {QY_RELATION_SORTS.map((value) => (
                <NativeSelectOption key={value} value={value}>
                  {t(`qy_rel_sort_${value}`)}
                </NativeSelectOption>
              ))}
            </NativeSelect>
          </div>

          <QyPageBoundary
            query={query}
            isEmpty={items.length === 0}
            emptyIcon={Link2}
            emptyTitle={t('qy_rel_empty_title')}
            emptyDescription={t('qy_rel_empty_desc')}
          >
            <div className='w-full overflow-x-auto'>
              <StaticDataTable
                columns={columns}
                data={items}
                getRowKey={(row) => `${row.inviter_id}-${row.invitee_id}`}
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
      </QySectionPageLayout.Content>

      <BindRelationDialog open={bindOpen} onClose={() => setBindOpen(false)} />
      <UnbindRelationDialog
        relation={unbindTarget}
        onClose={() => setUnbindTarget(null)}
      />
    </QySectionPageLayout>
  )
}
