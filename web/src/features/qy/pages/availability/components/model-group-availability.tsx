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
import { HeartPulse } from 'lucide-react'
import { useMemo } from 'react'
import { useTranslation } from 'react-i18next'

import { StaticDataTable } from '@/components/data-table'
import { GroupBadge } from '@/components/group-badge'
import { StatusBadge } from '@/components/status-badge'

import { qyKeys } from '../../../lib/query-keys'
import { formatQyAvailability, formatQyMs } from '../../ops/format'
import { getQyAvailabilityMatrix } from '../api'
import {
  formatQyTps,
  getQyAvailStateStyle,
  qyModelGroupRows,
} from '../constants'
import type { QyAvailCell, QyAvailMatrixParams } from '../types'

/**
 * 模型广场详情页「性能」标签的扩展口径版本:单个模型在各可见分组下的可用率。
 *
 * 数据与「模型可用率」页同源(同一个 /availability/matrix,同一套状态判定与
 * 性能指标),所以两处对同一个分组不会给出两个数字。时间窗固定 24 小时,
 * 与上游那一版性能面板的窗口一致。
 */
export function QyModelGroupAvailability(props: { modelName: string }) {
  const { t } = useTranslation()

  const params = useMemo<QyAvailMatrixParams>(
    () => ({ hours: 24, models: props.modelName, page: 1, page_size: 1 }),
    [props.modelName]
  )
  const query = useQuery({
    queryKey: qyKeys.availabilityMatrix(params),
    queryFn: () => getQyAvailabilityMatrix(params),
    staleTime: 60_000,
  })
  const rows = useMemo(
    () => qyModelGroupRows(query.data?.cells ?? [], props.modelName),
    [query.data, props.modelName]
  )

  const columns = useMemo(
    () => [
      {
        id: 'group',
        header: t('qy_avl_group'),
        cell: (row: QyAvailCell) => <GroupBadge group={row.group} size='sm' />,
      },
      {
        id: 'state',
        header: t('qy_common_status'),
        cell: (row: QyAvailCell) => {
          const style = getQyAvailStateStyle(row.state)
          return (
            <StatusBadge
              label={t(style.labelKey)}
              variant={style.badge}
              copyable={false}
            />
          )
        },
      },
      {
        id: 'availability',
        header: t('qy_avl_availability'),
        cellClassName: 'tabular-nums',
        cell: (row: QyAvailCell) => formatQyAvailability(row.availability),
      },
      {
        id: 'latency',
        header: t('qy_avl_latency'),
        cellClassName: 'tabular-nums',
        cell: (row: QyAvailCell) => formatQyMs(row.avg_latency_ms),
      },
      {
        id: 'ttft',
        header: t('qy_avl_ttft'),
        cellClassName: 'tabular-nums',
        cell: (row: QyAvailCell) => formatQyMs(row.avg_ttft_ms),
      },
      {
        id: 'tps',
        header: t('qy_avl_tps'),
        cellClassName: 'tabular-nums',
        cell: (row: QyAvailCell) => formatQyTps(row.avg_tps),
      },
    ],
    [t]
  )

  return (
    <section className='flex flex-col gap-2'>
      <div className='flex flex-wrap items-start justify-between gap-2'>
        <div className='flex min-w-0 items-start gap-2'>
          <HeartPulse className='text-muted-foreground/70 mt-0.5 size-3.5 shrink-0' />
          <div className='min-w-0'>
            <div className='text-foreground text-sm font-semibold'>
              {t('qy_avl_pricing_title')}
            </div>
            <p className='text-muted-foreground/80 text-xs'>
              {t('qy_avl_pricing_desc')}
            </p>
          </div>
        </div>
        <Link
          to='/qy/availability'
          className='text-primary shrink-0 text-xs font-medium hover:underline'
        >
          {t('qy_avl_pricing_open_full')}
        </Link>
      </div>
      {query.isLoading ? (
        <div className='text-muted-foreground rounded-lg border p-6 text-center text-sm'>
          {t('qy_avl_pricing_loading')}
        </div>
      ) : (
        <StaticDataTable
          className='rounded-lg'
          tableClassName='text-sm'
          columns={columns}
          data={rows}
          getRowKey={(row) => row.group}
          emptyContent={t('qy_avl_pricing_empty')}
        />
      )}
    </section>
  )
}
