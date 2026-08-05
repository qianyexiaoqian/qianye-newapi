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
import { useEffect, useState } from 'react'
import { useTranslation } from 'react-i18next'

import { StaticDataTable } from '@/components/data-table'
import { StatusBadge } from '@/components/status-badge'
import { TableCell, TableRow } from '@/components/ui/table'
import { QyPageBoundary } from '@/features/qy/components/qy-page-boundary'
import { QyResponsiveDialog } from '@/features/qy/components/qy-responsive-dialog'
import { QyPager } from '@/features/qy/pages/components/qy-pager'

import { getPlanHolders } from '../../api'
import { formatTimestamp } from '../../lib'
import type { PlanHolder } from '../../types'
import { useSubscriptions } from '../subscriptions-provider'

/**
 * 每页条数。
 *
 * 后端的上限是 100（`qianye/httpq` 的 Spec 缺省），这里取 20：弹窗里一屏能看完
 * 的量，翻页比滚动更容易定位到"我刚才看到哪了"。
 */
const HOLDERS_PAGE_SIZE = 20

/**
 * 「当前人数」的下钻：具体是哪些用户持有这个套餐。
 *
 * # 为什么要有它
 *
 * 项目方原话：「订阅管理当前只能看见人数，无法查看具体是哪些用户还存在这些
 * 套餐。」一个只有人数的界面，在需要**做事**的时候等于没有：要下架一个套餐，
 * 得先知道会影响到谁；要核对"某人说他买了却没生效"，得能在这里找到他。
 *
 * # 与列表页那个数字的关系
 *
 * 后端 `activeHolderPage` 与那个数字用的 `activeHolders` 共用同一个 WHERE，
 * 并且在同一次请求里只取一次时钟，所以 `total` 与列表页那一列恒等，行数与
 * `total` 也恒等。前端因此**不自己算人数**：标题里的数字直接用后端回的
 * `total`，而不是 `items.length`（后者是当前页的行数，翻到第 2 页就变了，
 * 而用户点进来时看到的是一个全量数字，两者不一致会被读成"数字在跳"）。
 *
 * # 三态
 *
 * 加载 / 失败 / 空全部交给 `QyPageBoundary`：它同时处理扩展未启用（中性空态、
 * 不弹红）与扩展库降级（顶部横幅 + 照常渲染）。**失败绝不能渲染成空白** ——
 * "这个套餐没有人"与"没读到"会导出完全相反的运营动作，而两者长得一模一样。
 */
export function PlanHoldersDialog() {
  const { t } = useTranslation()
  const { open, setOpen, currentRow } = useSubscriptions()
  const [page, setPage] = useState(1)

  const isOpen = open === 'plan-holders'
  const planId = currentRow?.plan?.id ?? 0

  // 换一个套餐（或重新打开）必须回到第 1 页：留在上一次的第 3 页上，新套餐
  // 只有 2 页时会显示成空列表，看起来就像"这个套餐没人买"。
  useEffect(() => {
    if (isOpen) setPage(1)
  }, [isOpen, planId])

  const query = useQuery({
    // 前缀 'qy' 是 qy 的硬性约定（features/qy/lib/query-keys.ts）：一次配置/资金
    // 操作之后 invalidateQueries({ queryKey: ['qy'] }) 是唯一安全的收尾方式。
    queryKey: ['qy', 'admin', 'subscription', 'plan-holders', planId, page],
    queryFn: () => getPlanHolders(planId, page, HOLDERS_PAGE_SIZE),
    enabled: isOpen && planId > 0,
    // 翻页时保留上一页内容，避免整块内容闪成 loading 再闪回来。
    placeholderData: (prev) => prev,
  })

  const rows = query.data?.items ?? []
  const total = query.data?.total ?? 0
  const planLabel = currentRow?.plan?.title || `#${planId}`

  return (
    <QyResponsiveDialog
      open={isOpen}
      onOpenChange={(next) => !next && setOpen(null)}
      title={t('qy_plan_holders_title')}
      description={t('qy_plan_holders_desc', { plan: planLabel, total })}
      contentClassName='sm:max-w-3xl'
    >
      <QyPageBoundary
        query={query}
        isEmpty={query.data != null && rows.length === 0}
        emptyIcon={Users}
        emptyTitle={t('qy_plan_holders_empty')}
        emptyDescription={t('qy_plan_holders_empty_desc')}
      >
        <div className='space-y-3'>
          <StaticDataTable
            data={rows}
            getRowKey={(row) => row.user_id}
            columns={[
              { id: 'user', header: t('qy_plan_holders_col_user') },
              { id: 'status', header: t('qy_plan_holders_col_status') },
              { id: 'subscriptions', header: t('qy_plan_holders_col_subs') },
              { id: 'start', header: t('qy_plan_holders_col_start') },
              { id: 'end', header: t('qy_plan_holders_col_end') },
            ]}
            renderRow={(row: PlanHolder) => (
              <TableRow key={row.user_id}>
                <TableCell>
                  <div className='flex flex-wrap items-center gap-1.5'>
                    <span className='font-medium'>
                      {row.username === '' ? '-' : row.username}
                    </span>
                    <span className='text-muted-foreground font-mono text-xs'>
                      #{row.user_id}
                    </span>
                    {/* 已删除的用户仍占着名额，却在用户管理里查不到。不标出来的话
                        这一行看起来只是个普通用户，而它恰恰是需要处理的异常。 */}
                    {row.user_deleted ? (
                      <StatusBadge
                        label={t('qy_plan_holders_user_deleted')}
                        variant='warning'
                        copyable={false}
                        title={t('qy_plan_holders_user_deleted_hint')}
                      />
                    ) : null}
                  </div>
                </TableCell>
                <TableCell>
                  <StatusBadge
                    label={t('qy_plan_holders_status_active')}
                    variant='success'
                    copyable={false}
                  />
                </TableCell>
                <TableCell title={t('qy_plan_holders_col_subs_hint')}>
                  {row.subscriptions}
                </TableCell>
                <TableCell className='text-sm whitespace-nowrap'>
                  {formatTimestamp(row.start_time)}
                </TableCell>
                <TableCell className='text-sm whitespace-nowrap'>
                  {formatTimestamp(row.end_time)}
                </TableCell>
              </TableRow>
            )}
          />

          <QyPager
            page={page}
            pageSize={HOLDERS_PAGE_SIZE}
            total={total}
            onPageChange={setPage}
            disabled={query.isFetching}
          />
        </div>
      </QyPageBoundary>
    </QyResponsiveDialog>
  )
}
