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
import { VChart } from '@visactor/react-vchart'
import dayjs from 'dayjs'
import { useMemo } from 'react'
import { useTranslation } from 'react-i18next'

import { useChartTheme } from '@/lib/use-chart-theme'
import { cn } from '@/lib/utils'
import { VCHART_OPTION } from '@/lib/vchart'

import type { QyAvailSeriesLine } from '../types'

/** 全部点都 ≥ 该值时把 y 轴下限抬到 90，让 99.x 的波动看得见。 */
const FOCUSED_AXIS_MIN = 90

type QyAvailabilityTrendChartProps = {
  series: QyAvailSeriesLine[]
  /** 桶粒度（秒）。决定 x 轴标签是「HH:mm」还是「MM-DD HH:mm」。 */
  bucketSeconds: number
  className?: string
}

/**
 * 可用率趋势折线图（每个分组一条线）。
 *
 * 用项目已在用的 `@visactor/react-vchart`（`model-details-charts.tsx`、
 * `dashboard/**` 全部是它），不引入 recharts —— 两套图表库共存会带来
 * 两套主题切换时序与两份体积。
 *
 * **`availability` 为 null 的点直接丢弃而不是补 0**：补 0 会在图上画出
 * 一条掉到底的假故障线，正是这个页面最需要避免的误导。
 */
export function QyAvailabilityTrendChart(props: QyAvailabilityTrendChartProps) {
  const { t } = useTranslation()
  const { resolvedTheme, themeReady } = useChartTheme()

  const points = useMemo(() => {
    const pattern = props.bucketSeconds >= 3600 ? 'MM-DD HH:mm' : 'HH:mm'
    return props.series.flatMap((line) =>
      line.points
        .filter((point) => point.availability != null)
        .map((point) => ({
          time: dayjs(point.ts * 1000).format(pattern),
          group: line.group,
          availability: point.availability as number,
          counted: point.counted,
        }))
    )
  }, [props.bucketSeconds, props.series])

  const spec = useMemo(() => {
    if (points.length === 0) return null
    const min = Math.min(...points.map((point) => point.availability))
    return {
      type: 'line' as const,
      data: [{ id: 'availability', values: points }],
      xField: 'time',
      yField: 'availability',
      seriesField: 'group',
      smooth: false,
      point: { visible: points.length <= 120, style: { size: 4 } },
      line: { style: { lineWidth: 2 } },
      legends: { visible: true, position: 'middle' as const },
      crosshair: { xField: { visible: true } },
      axes: [
        {
          orient: 'bottom' as const,
          label: { autoLimit: true, style: { fontSize: 10 } },
          tick: { visible: false },
        },
        {
          orient: 'left' as const,
          min: min >= FOCUSED_AXIS_MIN ? FOCUSED_AXIS_MIN : 0,
          max: 100,
          label: {
            formatMethod: (value: number | string) => `${value}%`,
            style: { fontSize: 10 },
          },
          grid: { visible: true, style: { lineDash: [3, 3] } },
        },
      ],
      tooltip: {
        mark: {
          title: { value: (datum: { time: string }) => datum.time },
          content: [
            {
              key: (datum: { group: string }) => datum.group,
              value: (datum: { availability: number }) =>
                `${datum.availability.toFixed(2)}%`,
            },
            {
              key: t('qy_avl_counted'),
              value: (datum: { counted: number }) => String(datum.counted),
            },
          ],
        },
      },
    }
  }, [points, t])

  if (spec == null) {
    return (
      <div
        className={cn(
          'text-muted-foreground flex h-52 items-center justify-center rounded-lg border text-xs',
          props.className
        )}
      >
        {t('qy_avl_no_series')}
      </div>
    )
  }

  return (
    <div className={cn('h-52 sm:h-64', props.className)}>
      {themeReady && (
        <VChart
          key={`qy-avail-${resolvedTheme}`}
          spec={{
            ...spec,
            theme: resolvedTheme === 'dark' ? 'dark' : 'light',
            background: 'transparent',
          }}
          option={VCHART_OPTION}
        />
      )}
    </div>
  )
}
