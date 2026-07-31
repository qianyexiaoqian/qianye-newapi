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
import { useMemo } from 'react'
import { useTranslation } from 'react-i18next'

import { useChartTheme } from '@/lib/use-chart-theme'
import { cn } from '@/lib/utils'
import { VCHART_OPTION } from '@/lib/vchart'

import { formatQyQuotaHero } from '../../../lib/format'
import { formatQyCount } from '../../ops/format'
import type { QyGpShadowBucket } from '../lib/shadow-aggregate'

/** 图上最多画多少根条。再多就读不出来了，剩下的看下面的明细表。 */
const MAX_BARS = 12

/**
 * 涨跌配色。
 *
 * 画布里拿不到 CSS 变量，所以写死两个在明暗主题下都能读的十六进制值；
 * 与页面其他地方的语义一致：多收（用户被多扣）用红，少收用绿。
 */
const UP_COLOR = '#ef4444'
const DOWN_COLOR = '#10b981'

type QyGpShadowDeltaChartProps = {
  buckets: QyGpShadowBucket[]
  className?: string
}

/**
 * 影子差额条形图。
 *
 * 只画**差额**而不是旧价 / 新价两根并排的柱子：运营要回答的问题是
 * 「切换后这个月会多收还是少收、差多少」，两根柱子的高度差需要人眼去减，
 * 而差额本身就是答案。
 *
 * 差额为 0 的桶直接不画：一根零长度的条只会占位置，还会让人以为图没加载出来。
 */
export function QyGpShadowDeltaChart(props: QyGpShadowDeltaChartProps) {
  const { t } = useTranslation()
  const { resolvedTheme, themeReady } = useChartTheme()

  const points = useMemo(
    () =>
      props.buckets
        .filter((bucket) => bucket.delta_quota !== 0)
        .slice(0, MAX_BARS)
        .map((bucket) => ({
          label: bucket.label,
          delta: bucket.delta_quota,
          requests: bucket.requests,
        })),
    [props.buckets]
  )

  // 图上画的是「可折算」那部分的差额。有不可折算的请求时，柱子的总和小于
  // 这段时间真实发生的全部变化，必须在图下说一句，而不是让人以为图是全貌。
  const inexactRequests = useMemo(
    () =>
      props.buckets.reduce((sum, bucket) => sum + bucket.inexact_requests, 0),
    [props.buckets]
  )

  const spec = useMemo(() => {
    if (points.length === 0) return null
    return {
      type: 'bar' as const,
      direction: 'horizontal' as const,
      data: [{ id: 'delta', values: points }],
      xField: 'delta',
      yField: 'label',
      bar: {
        style: {
          fill: (datum: { delta: number }) =>
            datum.delta >= 0 ? UP_COLOR : DOWN_COLOR,
        },
      },
      axes: [
        {
          orient: 'bottom' as const,
          label: {
            formatMethod: (value: number | string) =>
              formatQyQuotaHero(Number(value)),
            style: { fontSize: 10 },
          },
          grid: { visible: true, style: { lineDash: [3, 3] } },
        },
        {
          orient: 'left' as const,
          label: { autoLimit: true, style: { fontSize: 10 } },
          tick: { visible: false },
        },
      ],
      tooltip: {
        mark: {
          title: { value: (datum: { label: string }) => datum.label },
          content: [
            {
              key: t('qy_gp_sh_stat_delta'),
              value: (datum: { delta: number }) =>
                formatQyQuotaHero(datum.delta),
            },
            {
              key: t('qy_gp_sh_stat_requests'),
              value: (datum: { requests: number }) =>
                formatQyCount(datum.requests),
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
        {t('qy_gp_sh_no_delta')}
      </div>
    )
  }

  return (
    <div className={props.className}>
      <div className='h-60 sm:h-72'>
        {themeReady && (
          <VChart
            key={`qy-gp-shadow-${resolvedTheme}`}
            spec={{
              ...spec,
              theme: resolvedTheme === 'dark' ? 'dark' : 'light',
              background: 'transparent',
            }}
            option={VCHART_OPTION}
          />
        )}
      </div>
      {inexactRequests > 0 && (
        <p className='text-muted-foreground mt-1 text-xs'>
          {t('qy_gp_sh_chart_inexact', {
            requests: formatQyCount(inexactRequests),
          })}
        </p>
      )}
    </div>
  )
}
