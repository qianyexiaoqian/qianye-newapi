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

import { QyPageBoundary } from '../../components/qy-page-boundary'
import { QySdAmount } from '../../components/qy-sd-amount'
import { QyPager } from '../components/qy-pager'
import { QY_PAGE_SIZE } from '../lib/constants'
import { qyAdminRelationsQuery } from './api'
import { BindRelationDialog } from './components/bind-relation-dialog'
import {
  BlockRelationDialog,
  type QyBlockRelationTarget,
} from './components/block-relation-dialog'
import { RebindRelationDialog } from './components/rebind-relation-dialog'
import { UnbindRelationDialog } from './components/unbind-relation-dialog'
import {
  QY_RELATION_SCOPES,
  QY_RELATION_SORTS,
  type QyInviteRelation,
  type QyRelationScope,
  type QyRelationSort,
} from './types'

/**
 * 「邀请关系」—— 邀请管理选择夹的第一张标签（宿主）。
 *
 * ── 数据源为什么是主库而不是扩展库 ──
 * 邀请返链路上"谁是这个人的邀请人"只认主库的 `users.inviter_id`；扩展库的
 * `qy_invite_relation` 是**懒建**的展示快照，某个下线第一次触发邀请返时才写那一行。
 * 所以本页（绑定中）由后端从主库分页出。
 *
 * ── 两个 scope ──
 * 「绑定中」来自主库；「已解绑」只能来自快照——解绑之后主库的 `inviter_id` 已经
 * 清零，"他曾经是谁的下线"在主库里一个字都不剩。
 *
 * ── 四个动作在同一行上 ──
 * 停止 / 恢复计返（可逆）、换绑、解绑（不可逆）。此前换绑住在已删除的「用户佣金」
 * 页里，其余三个在这里；四个动作都不动已经返出去的星屑，改的只是"从此以后还
 * 产不产生新的、给谁"。「该关系累计返」一列是这一对累计返给邀请人的星屑
 * （各 kind 合计），逐日的明细去「日结明细」按邀请人筛。
 */
export function QyAdminInviteRelationsBody() {
  const { t } = useTranslation()

  const [page, setPage] = useState(1)
  const [scope, setScope] = useState<QyRelationScope>('bound')
  const [sort, setSort] = useState<QyRelationSort>('newest')
  const [username, setUsername] = useState('')
  const [inviterId, setInviterId] = useState('')
  const [bindOpen, setBindOpen] = useState(false)
  const [unbindTarget, setUnbindTarget] = useState<QyInviteRelation | null>(
    null
  )
  const [rebindTarget, setRebindTarget] = useState<QyInviteRelation | null>(
    null
  )
  // 停止/恢复计返的目标。与解绑并排放在同一行按钮里 —— 关系还在、只是不计返
  // 这条可逆的路要看得见，否则运营的结论只能是要停就只能解绑。
  const [blockTarget, setBlockTarget] = useState<QyBlockRelationTarget | null>(
    null
  )

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

  const columns: StaticDataTableColumn<QyInviteRelation>[] = [
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
      //   两个都是 0  → 直接说"不详"。老账号 users.created_at 是 NULL，
      //                  照着 0 渲染会得到"1970-01-01"。
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
      id: 'stardust',
      header: t('qy_inv_a_col_total_stardust'),
      className: staticDataTableClassNames.compactHeaderCellRight,
      cellClassName: staticDataTableClassNames.compactNumericCell,
      cell: (row) => <QySdAmount amount={row.total_stardust} />,
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
            <Badge variant='destructive'>{t('qy_inv_blocked')}</Badge>
          )}
          {/* 自动风控标记与人工事由分成两格：前者是系统判定（reciprocal_invite），
              后者是运营写的话。混在一格里两个都会说错。 */}
          {row.risk_flags !== '' && (
            <Badge variant='outline'>{row.risk_flags}</Badge>
          )}
          {row.block_reason !== '' && (
            <Badge variant='outline' title={row.block_reason}>
              {t('qy_rel_block_reason_badge', { reason: row.block_reason })}
            </Badge>
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
            <>
              {/* 方向由这一行当前的 blocked 决定，不写死 —— 写死一个方向正是
                  「停了就恢复不了」这个结论的来源。 */}
              <Button
                variant='ghost'
                size='sm'
                onClick={() =>
                  setBlockTarget({
                    inviteeId: row.invitee_id,
                    blocked: row.blocked,
                  })
                }
              >
                {row.blocked ? t('qy_inv_a_unblock') : t('qy_inv_a_block')}
              </Button>
              <Button
                variant='ghost'
                size='sm'
                onClick={() => setRebindTarget(row)}
              >
                {t('qy_inv_a_rebind')}
              </Button>
              <Button
                variant='ghost'
                size='sm'
                onClick={() => setUnbindTarget(row)}
              >
                {t('qy_rel_unbind')}
              </Button>
            </>
          )}
        </div>
      ),
    },
  ]

  const resetPage = () => setPage(1)

  return (
    <div className='space-y-3'>
      <div className='bg-muted/40 text-muted-foreground rounded-md border p-3 text-xs'>
        <p className='text-foreground font-medium'>{t('qy_rel_authority')}</p>
        <p className='mt-1'>{t('qy_inv_a_authority_hint')}</p>
      </div>

      <div className='flex flex-wrap items-center gap-2'>
        {/* 「新增绑定」放在筛选行：本页没有自己的页头（区段头由宿主出），
            而这个按钮是手工建立一条邀请关系唯一的入口。 */}
        <Button variant='outline' size='sm' onClick={() => setBindOpen(true)}>
          <Plus aria-hidden='true' />
          {t('qy_rel_bind')}
        </Button>
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
            tableClassName='min-w-[900px]'
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

      <BindRelationDialog open={bindOpen} onClose={() => setBindOpen(false)} />
      <RebindRelationDialog
        relation={rebindTarget}
        onClose={() => setRebindTarget(null)}
      />
      <UnbindRelationDialog
        relation={unbindTarget}
        onClose={() => setUnbindTarget(null)}
      />
      <BlockRelationDialog
        target={blockTarget}
        onClose={() => setBlockTarget(null)}
      />
    </div>
  )
}
