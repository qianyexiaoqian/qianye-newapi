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
import { type ColumnDef } from '@tanstack/react-table'
import { useMemo } from 'react'
import { useTranslation } from 'react-i18next'

import { BadgeCell } from '@/components/data-table'
import { GroupBadge } from '@/components/group-badge'
import { StatusBadge } from '@/components/status-badge'
import { TableId } from '@/components/table-id'
import { Skeleton } from '@/components/ui/skeleton'
import { formatQuota } from '@/lib/format'

import { formatDuration, formatResetPeriod } from '../lib'
import type { PlanRecord, PlanSeatUsage } from '../types'
import { DataTableRowActions } from './data-table-row-actions'
import { useSubscriptions } from './subscriptions-provider'

/**
 * 占用人数这一列的四种状态。
 *
 * 四种都必须有明确的显示：三种失败/未就绪状态如果都渲染成空白，管理员看到的
 * 是"这个套餐 0 个人"，而真相可能是"没读到"—— 这两件事会导出完全相反的运营动作。
 *
 * `hidden` 是 qy 扩展整体未启用（后端回 404，qy 客户端归类成 `disabled`）。
 * 这一档不显示错误而是**整列消失**：功能本来就不存在，摆一列错误只会让管理员
 * 去追一个不存在的问题。
 */
export type SeatUsageState = {
  status: 'loading' | 'ready' | 'error' | 'hidden'
  byPlanId: Map<number, PlanSeatUsage>
}

export function useSubscriptionsColumns(
  seatUsage: SeatUsageState
): ColumnDef<PlanRecord>[] {
  const { t } = useTranslation()
  // 「当前人数」那一列的数字是下钻入口，点开的弹窗与其余四个弹窗共用同一套
  // open/currentRow 状态，所以这里直接取 provider，而不是再往 props 里穿一层
  // 回调 —— 表格 → 列定义 → 单元格三层透传一个只有一个消费者的回调，
  // 是本仓反复出现的那种"为了不用 context 而多出来的管道"。
  const { setOpen, setCurrentRow } = useSubscriptions()

  return useMemo(
    (): ColumnDef<PlanRecord>[] => [
      {
        accessorFn: (row) => row.plan.id,
        id: 'id',
        header: t('ID'),
        meta: { mobileHidden: true },
        cell: ({ row }) => <TableId value={row.original.plan.id} />,
        size: 60,
      },
      {
        accessorFn: (row) => row.plan.title,
        id: 'title',
        header: t('Plan'),
        meta: { mobileTitle: true },
        cell: ({ row }) => {
          const plan = row.original.plan
          return (
            <div className='max-w-full min-w-0'>
              <div className='font-medium [overflow-wrap:anywhere]'>
                {plan.title}
              </div>
              {/* Two lines instead of one: the same cell backs the mobile card
                  list (meta.mobileTitle), where a one-line subtitle hid almost
                  the entire description. */}
              {plan.subtitle && (
                <div className='text-muted-foreground line-clamp-2 text-xs [overflow-wrap:anywhere] whitespace-pre-line'>
                  {plan.subtitle}
                </div>
              )}
            </div>
          )
        },
        size: 280,
      },
      {
        accessorFn: (row) => row.plan.price_amount,
        id: 'price',
        header: t('Price'),
        cell: ({ row }) => (
          <span className='font-semibold text-emerald-600'>
            ${Number(row.original.plan.price_amount || 0).toFixed(2)}
          </span>
        ),
        size: 100,
      },
      {
        id: 'duration',
        header: t('Validity'),
        cell: ({ row }) => (
          <span className='text-muted-foreground'>
            {formatDuration(row.original.plan, t)}
          </span>
        ),
        size: 100,
      },
      {
        id: 'reset',
        header: t('Quota Reset'),
        meta: { mobileHidden: true },
        cell: ({ row }) => (
          <span className='text-muted-foreground'>
            {formatResetPeriod(row.original.plan, t)}
          </span>
        ),
        size: 100,
      },
      {
        accessorFn: (row) => row.plan.sort_order,
        id: 'sort_order',
        header: t('Priority'),
        meta: { mobileHidden: true },
        cell: ({ row }) => (
          <span className='text-muted-foreground'>
            {row.original.plan.sort_order}
          </span>
        ),
        size: 100,
      },
      {
        accessorFn: (row) => row.plan.enabled,
        id: 'enabled',
        header: t('Status'),
        meta: { mobileBadge: true },
        cell: ({ row }) =>
          row.original.plan.enabled ? (
            <StatusBadge
              label={t('Enable')}
              variant='success'
              copyable={false}
              className='-ml-1.5'
            />
          ) : (
            <StatusBadge
              label={t('Disable')}
              variant='neutral'
              copyable={false}
              className='-ml-1.5'
            />
          ),
        size: 80,
      },
      // 占用人数：数据来自 qy 扩展（上游表上没有这个数）。扩展未启用时整列消失，
      // 而不是留一列错误 —— 功能本来就不存在。
      ...(seatUsage.status === 'hidden'
        ? []
        : [
            {
              id: 'active_users',
              header: t('qy_plan_active_users'),
              size: 110,
              cell: ({ row }) => {
                if (seatUsage.status === 'loading') {
                  return <Skeleton className='h-4 w-12' />
                }
                if (seatUsage.status === 'error') {
                  return (
                    <span className='text-destructive text-xs'>
                      {t('qy_plan_active_users_failed')}
                    </span>
                  )
                }
                const usage = seatUsage.byPlanId.get(row.original.plan.id)
                if (!usage) {
                  // 后端对每个套餐都回一行，所以缺行只可能是"套餐列表与占用是两次
                  // 请求，中间刚建了一个新套餐"。显示成 0 会是假数字，显示破折号
                  // 才是真话：这一行还没数过。
                  return <span className='text-muted-foreground'>—</span>
                }
                // 超卖是闸门的已知残余风险（并发购买，见后端 gate.go 的 R1），
                // 它的唯一可见处就是这里，所以必须一眼看得出来而不是静静显示成 4/3。
                //
                // 配色与提示都对齐编辑抽屉里的同一事实（subscriptions-mutate-drawer
                // 的 qy_plan_seat_usage 那一段）：同一个超卖套餐在列表页和抽屉里
                // 必须是同一种严重度，两种颜色会被读成两件不同的事。
                // qy_plan_seat_over_cap 是设计成追加在句子后面的括号补语，
                // 所以这里也追加而不是单独拿来当整条提示——单独用会弹出一个以
                // 全角括号开头、语法上悬空的片段。
                const over =
                  usage.capacity > 0 && usage.used_seats > usage.capacity
                // 数字本身就是下钻入口。项目方原话：「只能看见人数，无法查看
                // 具体是哪些用户」—— 一个只有人数的界面在需要做事时等于没有：
                // 下架套餐要先知道会影响到谁，核对"他说买了却没生效"要能找到他。
                //
                // 入口做成 <button> 而不是给 <span> 挂 onClick：键盘能 Tab 到、
                // 回车能触发、读屏会念出"按钮"。挂在 span 上的点击对不用鼠标的人
                // 等于这个功能不存在。
                //
                // 人数为 0 时禁用：点开必然是空列表，而一个点了没反应的数字会让人
                // 以为界面卡了。cursor-default 让它在视觉上也不像可点。
                return (
                  <button
                    type='button'
                    disabled={usage.used_seats <= 0}
                    onClick={() => {
                      setCurrentRow(row.original)
                      setOpen('plan-holders')
                    }}
                    className='hover:text-primary focus-visible:ring-ring inline-flex items-baseline gap-1 rounded-sm focus-visible:ring-2 focus-visible:outline-none disabled:cursor-default disabled:hover:text-inherit'
                    title={
                      over
                        ? `${t('qy_plan_active_users_hint')} ${t('qy_plan_seat_over_cap')}`
                        : t('qy_plan_active_users_hint')
                    }
                    // 插值变量刻意不叫 count：i18next 见到 count 会切到复数键
                    // 解析（qy_..._one / _other），而本仓的语言包是扁平单键，
                    // 结果是英文下解析不到键、直接把键名渲染到 aria-label 里。
                    aria-label={t('qy_plan_holders_open', {
                      n: usage.used_seats,
                    })}
                  >
                    <span
                      className={
                        over ? 'text-destructive font-semibold' : 'font-medium'
                      }
                    >
                      {usage.used_seats}
                    </span>
                    <span className='text-muted-foreground text-xs'>
                      /{' '}
                      {usage.capacity > 0
                        ? usage.capacity
                        : t('qy_plan_seat_unlimited')}
                    </span>
                  </button>
                )
              },
            } satisfies ColumnDef<PlanRecord>,
          ]),
      {
        id: 'payment',
        header: t('Payment Channel'),
        meta: { mobileHidden: true },
        cell: ({ row }) => {
          const plan = row.original.plan
          return (
            <BadgeCell>
              {plan.stripe_price_id && (
                <StatusBadge
                  label='Stripe'
                  variant='neutral'
                  copyable={false}
                />
              )}
              {plan.creem_product_id && (
                <StatusBadge label='Creem' variant='neutral' copyable={false} />
              )}
              {plan.waffo_pancake_product_id && (
                <StatusBadge
                  label='Waffo Pancake'
                  variant='neutral'
                  copyable={false}
                />
              )}
            </BadgeCell>
          )
        },
        size: 140,
      },
      {
        id: 'total_amount',
        header: t('Plan Quota'),
        meta: { mobileHidden: true },
        cell: ({ row }) => {
          const plan = row.original.plan
          // 纯商品要抢在 `total > 0` 之前判：它落库的 total_amount 是 0，
          // 而 0 走的是"不限额度"那一支。也就是说不特判的话，一个一分余额都
          // 不带、只卖用户分组的套餐，会在这一列显示成「无限」—— 与它真正的
          // 语义正好相反，而列表页是运营唯一一眼扫过全部套餐的地方。
          if (plan.no_quota) {
            return (
              <StatusBadge
                label={t('qy_plan_quota_pure_product')}
                variant='warning'
                copyable={false}
                className='-ml-1.5'
              />
            )
          }
          const total = Number(plan.total_amount || 0)
          return (
            <span className='text-muted-foreground'>
              {total > 0 ? formatQuota(total) : t('Unlimited')}
            </span>
          )
        },
        size: 150,
      },
      /*
        「购买后改用户分组」—— 把套餐当用户组商品卖的那一档。

        购买时由 `CreateUserSubscriptionFromPlanTx` 改 `users.group`、到期由
        `ExpireDueSubscriptions` 改回去。换用户分组会连带换掉买家的可用范围、
        倍率与自动分组，是比「解锁几个模型分组」重得多的一件事，所以它必须在
        列表页一眼看得到，而不是只在编辑抽屉里躺着。

        两块徽章必须各自带前缀标签：只并排摆两个分组名，运营无从判断哪个是买入
        哪个是到期 —— 而这两者填反的后果是"一买就掉到低级组、到期反而升级"。
        降级留空同理，它不是"没配"而是「回退到购买前的原分组」，写成空白会被
        读成"到期什么都不会发生"。
      */
      {
        id: 'user_group_change',
        header: t('qy_plan_group_product_title'),
        meta: { mobileHidden: true },
        cell: ({ row }) => {
          const upgrade = row.original.plan.upgrade_group
          const downgrade = row.original.plan.downgrade_group
          // 没配升级组时，到期那一步压根不会触发（`ExpireDueSubscriptions` 要求
          // downgrade_group 或 upgrade_group 之一非空，且回退原组还要求当前分组
          // 仍等于升级组）。两个都空就是"这个套餐不碰用户分组"。
          if (!upgrade && !downgrade) {
            return <span className='text-muted-foreground'>—</span>
          }
          return (
            <div className='flex flex-col gap-1'>
              {upgrade && (
                <BadgeCell>
                  <span className='text-muted-foreground text-xs'>
                    {t('qy_plan_group_col_upgrade')}
                  </span>
                  <GroupBadge group={upgrade} />
                </BadgeCell>
              )}
              <BadgeCell>
                <span className='text-muted-foreground text-xs'>
                  {t('qy_plan_group_col_downgrade')}
                </span>
                {downgrade ? (
                  <GroupBadge group={downgrade} />
                ) : (
                  <span className='text-muted-foreground text-xs'>
                    {t('qy_plan_group_col_revert_prev')}
                  </span>
                )}
              </BadgeCell>
            </div>
          )
        },
        size: 200,
      },
      {
        id: 'actions',
        header: () => t('Actions'),
        cell: ({ row }) => <DataTableRowActions row={row} />,
        meta: { pinned: 'right' as const },
      },
    ],
    [t, seatUsage, setOpen, setCurrentRow]
  )
}
