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
          const total = Number(row.original.plan.total_amount || 0)
          return (
            <span className='text-muted-foreground'>
              {total > 0 ? formatQuota(total) : t('Unlimited')}
            </span>
          )
        },
        size: 150,
      },
      /*
        存量的「购买改写用户分组」。

        ── 为什么这一列还在，而且用的是告警色 ──

        表单里的「升级分组 / 降级分组」已经撤掉了（用户分组与模型分组分离之后，
        买套餐只该多解锁几个模型分组，不该把人搬到另一个用户分组）。但上游那两列
        与读它们的那段逻辑**一行没动**：`CreateUserSubscriptionFromPlanTx` 在
        `upgrade_group != ''` 时照样 `UPDATE users SET group = …`，到期由
        `ExpireDueSubscriptions` 再改回去。

        也就是说，**从未在新表单里保存过的存量套餐仍然会改写用户分组**。把这一列
        一并删掉，就是把一个还在跑的行为从界面上抹掉：运营会发现有人的用户分组
        自己变了，而站内没有任何一个页面显示过哪个套餐会干这件事。

        清除方式不需要迁移脚本：打开该套餐的编辑抽屉、保存一次即可 —— 新表单恒
        提交空的 upgrade_group / downgrade_group（见 lib/plan-form.ts）。这一列
        就是那批套餐的待办清单，清空之后它整列都是「—」。
      */
      {
        id: 'legacy_group_rewrite',
        header: t('Legacy user group rewrite'),
        meta: { mobileHidden: true },
        cell: ({ row }) => {
          const upgrade = row.original.plan.upgrade_group
          const downgrade = row.original.plan.downgrade_group
          if (!upgrade && !downgrade) {
            return <span className='text-muted-foreground'>—</span>
          }
          return (
            <BadgeCell>
              {upgrade && <GroupBadge group={upgrade} />}
              {downgrade && <GroupBadge group={downgrade} />}
            </BadgeCell>
          )
        },
        size: 160,
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
