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
import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query'
import { useEffect, useState } from 'react'
import { useTranslation } from 'react-i18next'
import { toast } from 'sonner'

import { Badge } from '@/components/ui/badge'
import { Button } from '@/components/ui/button'
import { Tabs, TabsContent, TabsList, TabsTrigger } from '@/components/ui/tabs'
import { formatTimestampToDate } from '@/lib/format'

import { QyAmountText } from '../../../components/qy-amount-text'
import { QyResponsiveDialog } from '../../../components/qy-responsive-dialog'
import { QyStatusBadge } from '../../../components/qy-status-badge'
import { qyKeys } from '../../../lib/query-keys'
import { qyAdminRelationsQuery } from '../../admin-commission-relations/api'
import { UnbindRelationDialog } from '../../admin-commission-relations/components/unbind-relation-dialog'
import type { QyAffRelation } from '../../admin-commission-relations/types'
import {
  qyAdminAccrualsQuery,
  qyBlockInviteRelation,
  qyBlockRelationErrorMessage,
} from '../../admin-commission/api'
import { qyAdminWithdrawalsQuery } from '../../admin-withdrawals/api'
import type { QyCommissionUser } from '../types'

/** 下钻里每张标签只拉一页：这是"看一眼这个人"的浮层，不是完整的流水页。 */
const DRILL_PAGE_SIZE = 10

type UserCommissionDrilldownProps = {
  user: QyCommissionUser | null
  onClose: () => void
}

/**
 * 一个用户的佣金全貌（下钻）。
 *
 * ── 四张标签，四个不同的问题 ──
 *   · 计佣：这些钱**是怎么来的**（逐笔，含手工调整那一类）；
 *   · 结算：其中哪些已经落进余额（`status = settled` 的那一批）；
 *   · 提现：他把钱**取走了多少**、有没有卡在审核里；
 *   · 下线：他**拉了谁**，以及在这里直接停掉/解除某一条关系。
 *
 * ── 全部复用既有接口 ──
 * 四张标签分别打 `/admin/commission/records`（两次，筛选不同）、
 * `/admin/withdraw`、`/admin/commission/relations`。写动作（停止计佣 /
 * 解绑）复用 `relations/block` 与 `relations/unbind` —— 本浮层里**没有一行
 * 资金逻辑**，上限校验、幂等、审计全部只有后端那一份实现。
 *
 * 「结算」这一档刻意用「已结算的计佣行」而不是结算单据：站内目前没有暴露
 * 结算单（`qy_commission_settlement`）的管理端接口，与其编一个不存在的端点，
 * 不如如实用已有数据回答同一个问题，并把口径写在标签的说明里。
 *
 * 标签**不预挂载**：四张一起挂等于一打开浮层就同时打四个接口，而运营多数
 * 时候只看其中一张。
 */
export function UserCommissionDrilldown(props: UserCommissionDrilldownProps) {
  const { t } = useTranslation()
  const queryClient = useQueryClient()
  const user = props.user

  const [tab, setTab] = useState('accruals')
  const [unbindTarget, setUnbindTarget] = useState<QyAffRelation | null>(null)

  // 换一个人必须回到第一张标签：停在「提现」上会让运营以为自己看的还是上一个
  // 人的单子 —— 两个人的提现列表长得一模一样，只有金额不同。
  useEffect(() => {
    setTab('accruals')
    setUnbindTarget(null)
  }, [user])

  const userId = user?.user_id ?? 0
  const enabled = user != null

  const accruals = useQuery({
    ...qyAdminAccrualsQuery({
      p: 1,
      page_size: DRILL_PAGE_SIZE,
      inviter_id: String(userId),
    }),
    enabled: enabled && tab === 'accruals',
  })
  const settled = useQuery({
    ...qyAdminAccrualsQuery({
      p: 1,
      page_size: DRILL_PAGE_SIZE,
      inviter_id: String(userId),
      status: 'settled',
    }),
    enabled: enabled && tab === 'settled',
  })
  const withdrawals = useQuery({
    ...qyAdminWithdrawalsQuery({
      p: 1,
      page_size: DRILL_PAGE_SIZE,
      user_id: String(userId),
    }),
    enabled: enabled && tab === 'withdrawals',
  })
  // 下线用 AFF 关系列表的 `scope=bound`：那一档由后端从**主库**
  // `users.inviter_id` 分页出（权威口径），与列表页上那个下线数是同一个来源，
  // 两者永远对得上。走扩展库快照的话会少绝大多数人 —— 快照是懒建的。
  const invitees = useQuery({
    ...qyAdminRelationsQuery({
      p: 1,
      page_size: DRILL_PAGE_SIZE,
      scope: 'bound',
      sort: 'newest',
      inviter_id: String(userId),
    }),
    enabled: enabled && tab === 'invitees',
  })

  const blockMutation = useMutation({
    mutationFn: qyBlockInviteRelation,
    onSuccess: async (result) => {
      toast.success(
        result.blocked ? t('qy_cm_block_ok') : t('qy_cm_unblock_ok')
      )
      await queryClient.invalidateQueries({ queryKey: qyKeys.all })
    },
    // 与佣金审核页共用同一档分级：后端按情形给出独立的 code，各出**一句**
    // 准确的话；只有 `network` 那一档才配说"请求可能已经生效"。两句同屏
    // 正是项目方看到的那个屏幕。
    onError: (error) => toast.error(qyBlockRelationErrorMessage(error, t)),
  })

  let subject: string | undefined
  if (user != null) {
    subject = user.user_resolved
      ? `${user.username} (#${user.user_id})`
      : `#${user.user_id}`
  }

  return (
    <>
      <QyResponsiveDialog
        open={user != null}
        onOpenChange={(open) => {
          if (!open) props.onClose()
        }}
        title={t('qy_cu_drill_title')}
        description={subject}
        contentClassName='sm:max-w-3xl'
      >
        {user != null && (
          <div className='space-y-4'>
            <dl className='grid grid-cols-2 gap-x-4 text-sm sm:grid-cols-4'>
              <div className='flex flex-col py-1'>
                <dt className='text-muted-foreground text-xs'>
                  {t('qy_cb_available')}
                </dt>
                <dd>
                  <QyAmountText quota={user.available_quota} />
                </dd>
              </div>
              <div className='flex flex-col py-1'>
                <dt className='text-muted-foreground text-xs'>
                  {t('qy_cb_frozen')}
                </dt>
                <dd>
                  <QyAmountText quota={user.frozen_quota} />
                </dd>
              </div>
              <div className='flex flex-col py-1'>
                <dt className='text-muted-foreground text-xs'>
                  {t('qy_cb_withdrawn')}
                </dt>
                <dd>
                  <QyAmountText quota={user.withdrawn_quota} />
                </dd>
              </div>
              <div className='flex flex-col py-1'>
                <dt className='text-muted-foreground text-xs'>
                  {t('qy_cu_col_invitees')}
                </dt>
                <dd className='tabular-nums'>{user.invitee_count}</dd>
              </div>
            </dl>

            <Tabs
              value={tab}
              onValueChange={(value) => {
                if (typeof value === 'string') setTab(value)
              }}
              className='gap-3'
            >
              <TabsList className='flex w-full flex-wrap'>
                <TabsTrigger value='accruals' className='px-3'>
                  {t('qy_cu_tab_accruals')}
                </TabsTrigger>
                <TabsTrigger value='settled' className='px-3'>
                  {t('qy_cu_tab_settled')}
                </TabsTrigger>
                <TabsTrigger value='withdrawals' className='px-3'>
                  {t('qy_cu_tab_withdrawals')}
                </TabsTrigger>
                <TabsTrigger value='invitees' className='px-3'>
                  {t('qy_cu_tab_invitees')}
                </TabsTrigger>
              </TabsList>

              <TabsContent value='accruals'>
                <ul className='divide-border divide-y text-sm'>
                  {(accruals.data?.items ?? []).map((row) => (
                    <li
                      key={row.accrual_no}
                      className='flex items-center justify-between gap-3 py-1.5'
                    >
                      <span className='flex flex-col'>
                        <span>
                          {t(`qy_aff_src_${row.source_type}`, row.source_type)}
                          {' · '}
                          {t(`qy_aff_st_${row.status}`, row.status)}
                        </span>
                        <span className='text-muted-foreground text-xs'>
                          {formatTimestampToDate(row.created_at)} · #
                          {row.invitee_id}
                        </span>
                      </span>
                      <span className='tabular-nums'>{row.gross_amount}</span>
                    </li>
                  ))}
                </ul>
                {(accruals.data?.items ?? []).length === 0 && (
                  <p className='text-muted-foreground text-sm'>
                    {t('qy_cu_drill_empty')}
                  </p>
                )}
              </TabsContent>

              <TabsContent value='settled'>
                <p className='text-muted-foreground mb-2 text-xs'>
                  {t('qy_cu_settled_hint')}
                </p>
                <ul className='divide-border divide-y text-sm'>
                  {(settled.data?.items ?? []).map((row) => (
                    <li
                      key={row.accrual_no}
                      className='flex items-center justify-between gap-3 py-1.5'
                    >
                      <span className='text-muted-foreground text-xs'>
                        {formatTimestampToDate(row.created_at)} · #
                        {row.invitee_id}
                      </span>
                      <span className='tabular-nums'>{row.settled_amount}</span>
                    </li>
                  ))}
                </ul>
                {(settled.data?.items ?? []).length === 0 && (
                  <p className='text-muted-foreground text-sm'>
                    {t('qy_cu_drill_empty')}
                  </p>
                )}
              </TabsContent>

              <TabsContent value='withdrawals'>
                <ul className='divide-border divide-y text-sm'>
                  {(withdrawals.data?.items ?? []).map((row) => (
                    <li
                      key={row.withdraw_no}
                      className='flex items-center justify-between gap-3 py-1.5'
                    >
                      <span className='flex flex-col gap-1'>
                        <QyStatusBadge status={row.status} />
                        <span className='text-muted-foreground text-xs'>
                          {formatTimestampToDate(row.created_at)} ·{' '}
                          {row.withdraw_no}
                        </span>
                      </span>
                      <QyAmountText quota={row.quota} />
                    </li>
                  ))}
                </ul>
                {(withdrawals.data?.items ?? []).length === 0 && (
                  <p className='text-muted-foreground text-sm'>
                    {t('qy_cu_drill_empty')}
                  </p>
                )}
              </TabsContent>

              <TabsContent value='invitees'>
                <ul className='divide-border divide-y text-sm'>
                  {(invitees.data?.items ?? []).map((row) => (
                    <li
                      key={`${row.inviter_id}-${row.invitee_id}`}
                      className='flex items-center justify-between gap-3 py-1.5'
                    >
                      <span className='flex flex-col'>
                        <span className='inline-flex items-center gap-1.5'>
                          {row.invitee_resolved
                            ? row.invitee_username
                            : t('qy_rel_user_gone')}
                          {row.blocked && (
                            <Badge variant='destructive'>
                              {t('qy_rel_state_blocked')}
                            </Badge>
                          )}
                        </span>
                        <span className='text-muted-foreground text-xs'>
                          #{row.invitee_id} ·{' '}
                          <QyAmountText quota={row.total_commission_quota} />
                        </span>
                      </span>
                      <span className='flex shrink-0 gap-1'>
                        <Button
                          variant='ghost'
                          size='sm'
                          disabled={blockMutation.isPending}
                          onClick={() =>
                            blockMutation.mutate({
                              invitee_id: row.invitee_id,
                              blocked: !row.blocked,
                              reason: t('qy_cu_block_default_reason'),
                            })
                          }
                        >
                          {row.blocked ? t('qy_cm_unblock') : t('qy_cm_block')}
                        </Button>
                        <Button
                          variant='ghost'
                          size='sm'
                          onClick={() => setUnbindTarget(row)}
                        >
                          {t('qy_rel_unbind')}
                        </Button>
                      </span>
                    </li>
                  ))}
                </ul>
                {(invitees.data?.items ?? []).length === 0 && (
                  <p className='text-muted-foreground text-sm'>
                    {t('qy_cu_drill_empty')}
                  </p>
                )}
              </TabsContent>
            </Tabs>
          </div>
        )}
      </QyResponsiveDialog>

      {/* 解绑复用 AFF 关系页那一个弹窗：它把"历史佣金全部保留、从此不再产生
          新的"这句话写在按钮上方，而那正是运营点解绑时唯一真正关心的事。
          在这里另写一份说明就是同一句话的第二份拷贝。 */}
      <UnbindRelationDialog
        relation={unbindTarget}
        onClose={() => setUnbindTarget(null)}
      />
    </>
  )
}
