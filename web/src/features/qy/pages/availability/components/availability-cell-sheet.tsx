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
import { useTranslation } from 'react-i18next'

import { GroupBadge } from '@/components/group-badge'
import { LoadingState } from '@/components/loading-state'
import { StatusBadge } from '@/components/status-badge'
import {
  Sheet,
  SheetContent,
  SheetDescription,
  SheetHeader,
  SheetTitle,
} from '@/components/ui/sheet'

import { qyKeys } from '../../../lib/query-keys'
import { formatQyCount, formatQyMs, QY_EMPTY_TEXT } from '../../ops/format'
import { QyKeyValue } from '../../ops/qy-ops-ui'
import { getQyAvailabilitySeries } from '../api'
import {
  formatQyTps,
  getQyAvailStateStyle,
  qyAvailOutcomeKey,
  type QyAvailMetricDef,
} from '../constants'
import type { QyAvailCell, QyAvailDefinition } from '../types'
import { QyAvailabilityTrendChart } from './availability-trend-chart'

type QyAvailabilityCellSheetProps = {
  cell: QyAvailCell | null
  hours: number
  /** 页面当前的主指标，趋势图跟着它走 —— 切到「速度」再点开格子，
   *  看到的仍是可用率曲线的话，切换就等于没生效。 */
  metric: QyAvailMetricDef
  definition: QyAvailDefinition | null
  onClose: () => void
}

/**
 * 单元格抽屉：某个（分组, 模型）的趋势与失败归因。
 *
 * 趋势只在抽屉打开时才请求（`enabled`）—— 矩阵一屏可能有几百个格子，
 * 预取所有时序会把这个只读页面变成一个廉价的 DoS 面。
 */
export function QyAvailabilityCellSheet(props: QyAvailabilityCellSheetProps) {
  const { t } = useTranslation()
  const cell = props.cell

  const seriesQuery = useQuery({
    queryKey: qyKeys.availabilitySeries({
      model: cell?.model ?? '',
      group: cell?.group ?? '',
      hours: props.hours,
    }),
    queryFn: () =>
      getQyAvailabilitySeries({
        model: cell?.model ?? '',
        groups: cell?.group,
        hours: props.hours,
      }),
    enabled: cell != null,
    staleTime: 30_000,
  })

  const style = getQyAvailStateStyle(cell?.state ?? 'no_data')

  return (
    <Sheet
      open={cell != null}
      onOpenChange={(open) => {
        if (!open) props.onClose()
      }}
    >
      <SheetContent side='right' className='sm:max-w-xl'>
        <SheetHeader>
          <SheetTitle className='flex flex-wrap items-center gap-2 pr-8'>
            <span className='min-w-0 break-all'>{cell?.model}</span>
            {cell != null && <GroupBadge group={cell.group} />}
          </SheetTitle>
          <SheetDescription>
            {t('qy_avl_cell_desc', { hours: props.hours })}
          </SheetDescription>
        </SheetHeader>

        <div className='min-h-0 flex-1 space-y-4 overflow-y-auto px-4 pb-4'>
          {/* 大字跟着主指标走，状态徽章始终是可用率六态：
              「延迟 1.2s」旁边配一个「不可用」，两条信息缺一不可。 */}
          <div className='flex items-baseline gap-3'>
            <span className='text-3xl font-semibold tabular-nums'>
              {props.metric.format(
                cell == null ? null : props.metric.valueOf(cell)
              )}
            </span>
            <span className='text-muted-foreground text-xs'>
              {t(props.metric.labelKey)}
            </span>
            <StatusBadge
              label={t(style.labelKey)}
              variant={style.badge}
              copyable={false}
              size='md'
            />
          </div>

          <div>
            <QyKeyValue label={t('qy_avl_req_total')}>
              {formatQyCount(cell?.req_total)}
            </QyKeyValue>
            <QyKeyValue label={t('qy_avl_counted')}>
              {formatQyCount(cell?.counted)}
            </QyKeyValue>
            <QyKeyValue label={t('qy_avl_success')}>
              {formatQyCount(cell?.success)}
            </QyKeyValue>
            {/* 被排除的样本必须单独展示：没有它就无法回答
                「我明明打了 1000 次，为什么只统计了 300 次」。 */}
            <QyKeyValue label={t('qy_avl_excluded')}>
              {formatQyCount(cell?.excluded_total)}
            </QyKeyValue>
            <QyKeyValue label={t('qy_avl_top_reason')}>
              {cell?.top_reason == null || cell.top_reason === ''
                ? QY_EMPTY_TEXT
                : t(qyAvailOutcomeKey(cell.top_reason))}
            </QyKeyValue>
            {/* 三个性能指标各带自己的样本数：可用率有 1000 条样本，不代表
                首字延迟也有 —— 非流式请求根本不产生首字样本。没有这一列，
                「为什么这里是横杠」就永远解释不清。 */}
            <QyKeyValue label={t('qy_avl_latency')}>
              {`${formatQyMs(cell?.avg_latency_ms)} · ${t('qy_avl_samples_n', {
                n: formatQyCount(cell?.latency_samples),
              })}`}
            </QyKeyValue>
            <QyKeyValue label={t('qy_avl_ttft')}>
              {`${formatQyMs(cell?.avg_ttft_ms)} · ${t('qy_avl_samples_n', {
                n: formatQyCount(cell?.ttft_samples),
              })}`}
            </QyKeyValue>
            <QyKeyValue label={t('qy_avl_tps')}>
              {`${formatQyTps(cell?.avg_tps)} · ${t('qy_avl_samples_n', {
                n: formatQyCount(cell?.speed_samples),
              })}`}
            </QyKeyValue>
          </div>

          <div>
            <h3 className='mb-2 text-sm font-medium'>
              {`${t('qy_avl_trend')} · ${t(props.metric.labelKey)}`}
            </h3>
            {seriesQuery.isLoading ? (
              <LoadingState />
            ) : (
              <QyAvailabilityTrendChart
                series={seriesQuery.data?.series ?? []}
                metric={props.metric}
                bucketSeconds={seriesQuery.data?.bucket_seconds ?? 300}
              />
            )}
          </div>

          {props.definition != null && (
            <p className='text-muted-foreground text-xs leading-relaxed'>
              {props.definition.note}
            </p>
          )}
        </div>
      </SheetContent>
    </Sheet>
  )
}
