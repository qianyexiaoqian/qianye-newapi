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
import { useMemo } from 'react'
import { useTranslation } from 'react-i18next'

import { DataTablePage, useDataTable } from '@/components/data-table'
import { isQyError } from '@/features/qy/lib/api'

import { getAdminPlans, getPlansUsage } from '../api'
import {
  useSubscriptionsColumns,
  type SeatUsageState,
} from './subscriptions-columns'
import { useSubscriptions } from './subscriptions-provider'

export function SubscriptionsTable() {
  const { t } = useTranslation()
  const { refreshTrigger } = useSubscriptions()

  const { data, isLoading } = useQuery({
    queryKey: ['admin-subscription-plans', refreshTrigger],
    queryFn: async () => {
      const result = await getAdminPlans()
      return result.data || []
    },
    placeholderData: (prev) => prev,
  })

  // 占用人数是扩展库那一侧的数据，与套餐本体不是同一次请求，所以单独一个 query：
  // 合进上面那个会让"扩展未启用"顺带把整张套餐表也变成空态。
  //
  // queryKey 以 'qy' 开头是 qy 的硬性约定（见 features/qy/lib/query-keys.ts）：
  // 一次资金/配置操作之后 `invalidateQueries({ queryKey: ['qy'] })` 是唯一安全的
  // 收尾方式，而它成立的前提就是这个统一前缀。这里不往那份公共清单里加行，
  // 是因为这个 key 只有本表用得到，跨模块没有第二个消费者。
  const usageQuery = useQuery({
    queryKey: ['qy', 'admin', 'subscription', 'plans-usage', refreshTrigger],
    queryFn: getPlansUsage,
    placeholderData: (prev) => prev,
    // 扩展未启用时后端回 404，重试三次只是白等三次。
    retry: (count, error) => !(isQyError(error) && error.isHidden) && count < 1,
  })

  const seatUsage = useMemo<SeatUsageState>(() => {
    if (isQyError(usageQuery.error) && usageQuery.error.isHidden) {
      // 扩展整体未启用：静默隐藏这一列，而不是给管理员糊一列错误。
      return { status: 'hidden', byPlanId: new Map() }
    }
    if (usageQuery.error) return { status: 'error', byPlanId: new Map() }
    if (!usageQuery.data) return { status: 'loading', byPlanId: new Map() }
    return {
      status: 'ready',
      byPlanId: new Map(
        (usageQuery.data.plans ?? []).map((p) => [p.plan_id, p])
      ),
    }
  }, [usageQuery.data, usageQuery.error])

  const columns = useSubscriptionsColumns(seatUsage)

  const plans = useMemo(() => data || [], [data])

  const { table } = useDataTable({
    data: plans,
    columns,
    withFilteredRowModel: false,
    withFacetedRowModel: false,
  })

  return (
    <DataTablePage
      table={table}
      columns={columns}
      isLoading={isLoading}
      emptyTitle={t('No subscription plans yet')}
      emptyDescription={t(
        'Click "Create Plan" to create your first subscription plan'
      )}
      skeletonKeyPrefix='subscriptions-skeleton'
      applyHeaderSize
    />
  )
}
