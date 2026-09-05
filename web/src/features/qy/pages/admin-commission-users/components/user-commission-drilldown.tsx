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
import { useEffect, useState } from 'react'
import { useTranslation } from 'react-i18next'

import { Badge } from '@/components/ui/badge'
import { Button } from '@/components/ui/button'
import { Tabs, TabsContent, TabsList, TabsTrigger } from '@/components/ui/tabs'
import { formatTimestampToDate } from '@/lib/format'

import { QyAmountText } from '../../../components/qy-amount-text'
import { QyResponsiveDialog } from '../../../components/qy-responsive-dialog'
import { QySdAmount } from '../../../components/qy-sd-amount'
import { qyArray } from '../../../lib/array'
import { qyAdminAccrualsQuery } from '../../admin-commission/api'
import { qyAdminRelationsQuery } from '../../admin-invite/api'
import {
  BlockRelationDialog,
  type QyBlockRelationTarget,
} from '../../admin-invite/components/block-relation-dialog'
import { UnbindRelationDialog } from '../../admin-invite/components/unbind-relation-dialog'
import type { QyInviteRelation } from '../../admin-invite/types'
import { qyCommissionCreditBadge } from '../../commission-records/lib/credit-status'
import { qyAdminCommissionCreditsQuery } from '../api'
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
 *   · 入账：余额里哪些已经**记进星辉**了、有没有卡在 held；
 *   · 下线：他**拉了谁**，以及在这里直接停掉 / 解除某一条关系。
 *
 * ── 全部复用既有接口 ──
 * 前三张打 `/admin/commission/records`（两次，筛选不同）与 `/admin/commission/
 * credits`；下线走 invite 模块的 `/admin/invite/relations`（`scope=bound` 由
 * 后端从**主库** `users.inviter_id` 分页出，与列表页上那个下线数同源）。
 * 写动作（停止计返 / 解绑）复用「邀请管理」的两个弹窗 —— 本浮层里**没有一行
 * 资金逻辑**，上限校验、幂等、审计全部只有后端那一份实现。
 *
 * D-14 之前的第三张是「提现」；提现模块永久删除，位置换成自动入账记录。
 *
 * 标签**不预挂载**：四张一起挂等于一打开浮层就同时打四个接口。
 */
export function UserCommissionDrilldown(props: UserCommissionDrilldownProps) {
  const { t } = useTranslation()
  const user = props.user

  const [tab, setTab] = useState('accruals')
  const [unbindTarget, setUnbindTarget] = useState<QyInviteRelation | null>(
    null
  )
  const [blockTarget, setBlockTarget] = useState<QyBlockRelationTarget | null>(
    null
  )

  // 换一个人必须回到第一张标签：停在「入账」上会让运营以为自己看的还是上一个
  // 人的单子 —— 两个人的入账列表长得一模一样，只有金额不同。
  useEffect(() => {
    setTab('accruals')
    setUnbindTarget(null)
    setBlockTarget(null)
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
  const credits = useQuery({
    ...qyAdminCommissionCreditsQuery({
      p: 1,
      page_size: DRILL_PAGE_SIZE,
      user_id: String(userId),
    }),
    enabled: enabled && tab === 'credits',
  })
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

  let subject: string | undefined
  if (user != null) {
    subject = user.user_resolved
      ? `${user.username} (#${user.user_id})`
      : `#${user.user_id}`
  }

  const accrualRows = qyArray(accruals.data?.items)
  const settledRows = qyArray(settled.data?.items)
  const creditRows = qyArray(credits.data?.items)
  const inviteeRows = qyArray(invitees.data?.items)

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
                  {t('qy_cb_available_xh')}
                </dt>
                <dd>
                  <QyAmountText quota={user.available_quota} />
                </dd>
              </div>
              <div className='flex flex-col py-1'>
                <dt className='text-muted-foreground text-xs'>
                  {t('qy_cb_frozen_xh')}
                </dt>
                <dd>
                  <QyAmountText quota={user.frozen_quota} />
                </dd>
              </div>
              <div className='flex flex-col py-1'>
                <dt className='text-muted-foreground text-xs'>
                  {t('qy_cb_credited')}
                </dt>
                <dd>
                  <QyAmountText quota={user.credited_quota} />
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
                <TabsTrigger value='credits' className='px-3'>
                  {t('qy_cu_tab_credits')}
                </TabsTrigger>
                <TabsTrigger value='invitees' className='px-3'>
                  {t('qy_cu_tab_invitees')}
                </TabsTrigger>
              </TabsList>

              <TabsContent value='accruals'>
                <ul className='divide-border divide-y text-sm'>
                  {accrualRows.map((row) => (
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
                      <QyAmountText quota={row.gross_amount} />
                    </li>
                  ))}
                </ul>
                {accrualRows.length === 0 && (
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
                  {settledRows.map((row) => (
                    <li
                      key={row.accrual_no}
                      className='flex items-center justify-between gap-3 py-1.5'
                    >
                      <span className='text-muted-foreground text-xs'>
                        {formatTimestampToDate(row.created_at)} · #
                        {row.invitee_id}
                      </span>
                      <QyAmountText quota={row.settled_amount} />
                    </li>
                  ))}
                </ul>
                {settledRows.length === 0 && (
                  <p className='text-muted-foreground text-sm'>
                    {t('qy_cu_drill_empty')}
                  </p>
                )}
              </TabsContent>

              <TabsContent value='credits'>
                <ul className='divide-border divide-y text-sm'>
                  {creditRows.map((row) => {
                    const badge = qyCommissionCreditBadge(row.status)
                    return (
                      <li
                        key={row.credit_no}
                        className='flex items-center justify-between gap-3 py-1.5'
                      >
                        <span className='flex flex-col gap-1'>
                          <span>
                            <Badge variant={badge.variant}>
                              {t(badge.labelKey, { defaultValue: row.status })}
                            </Badge>
                          </span>
                          <span className='text-muted-foreground text-xs'>
                            {formatTimestampToDate(row.created_at)} ·{' '}
                            {row.credit_no}
                          </span>
                        </span>
                        <QyAmountText quota={row.quota} />
                      </li>
                    )
                  })}
                </ul>
                {creditRows.length === 0 && (
                  <p className='text-muted-foreground text-sm'>
                    {t('qy_cu_drill_empty')}
                  </p>
                )}
              </TabsContent>

              <TabsContent value='invitees'>
                <ul className='divide-border divide-y text-sm'>
                  {inviteeRows.map((row) => (
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
                        {/* 关系行上的累计是**星屑**（invite 模块记的那本账），
                            与本浮层其余三张标签的星辉不是同一种单位，各印各的。 */}
                        <span className='text-muted-foreground text-xs'>
                          #{row.invitee_id} ·{' '}
                          <QySdAmount amount={row.total_stardust} />
                        </span>
                      </span>
                      <span className='flex shrink-0 gap-1'>
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
                {inviteeRows.length === 0 && (
                  <p className='text-muted-foreground text-sm'>
                    {t('qy_cu_drill_empty')}
                  </p>
                )}
              </TabsContent>
            </Tabs>
          </div>
        )}
      </QyResponsiveDialog>

      {/* 解绑与停止 / 恢复都复用「邀请管理」的弹窗：它们把"已发的星屑 / 佣金全部
          保留、从此不再产生新的"这句话写在按钮上方，在这里另写一份就是同一句话
          的第二份拷贝。 */}
      <UnbindRelationDialog
        relation={unbindTarget}
        onClose={() => setUnbindTarget(null)}
      />

      <BlockRelationDialog
        target={blockTarget}
        onClose={() => setBlockTarget(null)}
      />
    </>
  )
}
