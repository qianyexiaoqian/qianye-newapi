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
import { useMemo } from 'react'
import { useTranslation } from 'react-i18next'

import { StaticDataTable } from '@/components/data-table'
import { GroupBadge } from '@/components/group-badge'
import { StatusBadge } from '@/components/status-badge'
import { Button } from '@/components/ui/button'

import {
  formatQyAvailability,
  formatQyCount,
  formatQyMs,
  QY_EMPTY_TEXT,
} from '../../ops/format'
import {
  formatQyTps,
  getQyAvailStateStyle,
  qyAvailOutcomeKey,
} from '../constants'
import type { QyAvailCell } from '../types'

/**
 * 矩阵的表格视图。
 *
 * 与热力图共用同一份 `cells`，因此两个视图之间切换不会重新请求，
 * 也不可能出现「两个视图数字不一致」这种排查起来最费时的问题。
 *
 * 截断提示是必需的，而且不能只做在热力图上：表格里被截掉的模型是**整行消失**，
 * 页面上看不出与「这个模型本来就没有数据」有任何区别，用户会以为自己看到的是
 * 全部结果。
 */
export function QyAvailabilityTable(props: {
  cells: QyAvailCell[]
  /** 响应命中 `max_series_per_query`，表格里少了若干模型。 */
  truncated: boolean
  onSelectCell: (cell: QyAvailCell) => void
}) {
  const { t } = useTranslation()

  const columns = useMemo(
    () => [
      {
        id: 'model',
        header: t('qy_avl_model'),
        cell: (row: QyAvailCell) => (
          <Button
            type='button'
            variant='link'
            size='sm'
            className='h-auto p-0'
            onClick={() => props.onSelectCell(row)}
          >
            {row.model}
          </Button>
        ),
      },
      {
        id: 'group',
        header: t('qy_avl_group'),
        cell: (row: QyAvailCell) => <GroupBadge group={row.group} size='sm' />,
      },
      {
        id: 'availability',
        header: t('qy_avl_availability'),
        cellClassName: 'tabular-nums',
        cell: (row: QyAvailCell) => formatQyAvailability(row.availability),
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
        id: 'req_total',
        header: t('qy_avl_req_total'),
        cellClassName: 'tabular-nums',
        cell: (row: QyAvailCell) => formatQyCount(row.req_total),
      },
      {
        id: 'counted',
        header: t('qy_avl_counted'),
        cellClassName: 'tabular-nums',
        cell: (row: QyAvailCell) => formatQyCount(row.counted),
      },
      {
        id: 'top_reason',
        header: t('qy_avl_top_reason'),
        cell: (row: QyAvailCell) =>
          row.top_reason == null || row.top_reason === ''
            ? QY_EMPTY_TEXT
            : t(qyAvailOutcomeKey(row.top_reason)),
      },
      // 三个性能列一律走 formatQyMs / formatQyTps：样本不足时后端下发 null，
      // 这里必须渲染成占位符而不是 0 —— 「延迟 0ms」会被读成「快得离谱」。
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
    [props, t]
  )

  return (
    <div className='space-y-2'>
      <StaticDataTable
        columns={columns}
        data={props.cells}
        getRowKey={(row) => `${row.model} ${row.group}`}
        emptyContent={t('qy_avl_empty_no_data')}
      />
      {props.truncated && (
        <p className='text-warning px-1 text-xs'>{t('qy_avl_truncated')}</p>
      )}
    </div>
  )
}
