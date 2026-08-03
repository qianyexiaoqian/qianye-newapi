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
import { Scale, TriangleAlert } from 'lucide-react'
import { useId, useMemo, useState } from 'react'
import { useTranslation } from 'react-i18next'

import {
  Select,
  SelectContent,
  SelectItem,
  SelectTrigger,
  SelectValue,
} from '@/components/ui/select'

import { QyPageBoundary } from '../../../components/qy-page-boundary'
import { qyArray } from '../../../lib/array'
import { QyStatGrid } from '../../components/qy-stat-grid'
import { formatQyCount, formatQyTs } from '../../ops/format'
import { QyFilterBar, QyFilterField } from '../../ops/qy-ops-ui'
import { qyGpShadowQuery } from '../api'
import {
  QY_GP_RANGE_PRESETS,
  qyAggregateShadow,
  qyShadowRange,
  type QyGpRangePreset,
} from '../lib/shadow-aggregate'
import type { QyGpShadowDimension } from '../types'
import { QyGpDeltaAmount } from './shadow-amount'
import { QyGpShadowDeltaChart } from './shadow-delta-chart'
import { QyGpShadowSegmentsTable } from './shadow-segments-table'

const DIMENSIONS: QyGpShadowDimension[] = ['model', 'group']

/**
 * 影子模式差额对账。
 *
 * 存在的唯一理由是回答一句话：**「切换到真实计费后，这段时间会多收还是少收、
 * 差多少」**。因此合计里最醒目的是「差额」而不是新价或旧价，图上画的也是
 * 差额本身 —— 两根柱子的高度差需要人眼去减，而差额就是答案。
 *
 * 这一页有三处「不完整」必须当场说清楚，否则运营会拿一份看起来完整的报表去
 * 做上线决策：
 *   1. `quota_source_error` —— 主库日志聚合失败，金额列全是 0；
 *   2. `inexact_requests`   —— 有一批请求的差额按比例折算不成立，不在合计里；
 *   3. `truncated`          —— 维度组合超上限，明细只有 Top N。
 */
export function QyGpShadowPanel(props: { shadowMode: boolean }) {
  const { t } = useTranslation()
  const rangeId = useId()
  const dimensionId = useId()
  const [preset, setPreset] = useState<QyGpRangePreset>('30d')
  const [dimension, setDimension] = useState<QyGpShadowDimension>('model')

  // 区间只在预设变化时重算：每次渲染都取一次 Date.now() 会让 queryKey 一直变，
  // react-query 会把它当成新查询无限重取。
  const range = useMemo(() => qyShadowRange(preset), [preset])
  const query = useQuery(qyGpShadowQuery(range))
  const summary = query.data?.summary

  // segments 在这里一次性收敛成真数组：下面有三处消费它（空态判定、分桶聚合、
  // 明细表），每处各兜一次底就是同一个判断的第三份拷贝，而漏掉任何一处的后果
  // 都是整页白屏 —— 提现队列角标就是这么炸的。
  //
  // 必须过 useMemo：契约违约那一支每次渲染都会 new 一个 []，直接当依赖会让
  // 下面那个 useMemo 每帧重算，而它正是为了避开重算才存在的。
  const segments = useMemo(
    () => qyArray(summary?.segments),
    [summary?.segments]
  )

  const buckets = useMemo(
    () => qyAggregateShadow(segments, dimension),
    [dimension, segments]
  )

  return (
    <div className='space-y-4'>
      <QyFilterBar>
        <QyFilterField label={t('qy_gp_sh_range')} htmlFor={rangeId}>
          <Select
            value={preset}
            onValueChange={(value) =>
              setPreset((value ?? preset) as QyGpRangePreset)
            }
          >
            <SelectTrigger id={rangeId} className='w-32'>
              <SelectValue />
            </SelectTrigger>
            <SelectContent>
              {QY_GP_RANGE_PRESETS.map((item) => (
                <SelectItem key={item} value={item}>
                  {t(`qy_gp_sh_range_${item}`)}
                </SelectItem>
              ))}
            </SelectContent>
          </Select>
        </QyFilterField>

        <QyFilterField label={t('qy_gp_sh_dim')} htmlFor={dimensionId}>
          <Select
            value={dimension}
            onValueChange={(value) =>
              setDimension((value ?? dimension) as QyGpShadowDimension)
            }
          >
            <SelectTrigger id={dimensionId} className='w-36'>
              <SelectValue />
            </SelectTrigger>
            <SelectContent>
              {DIMENSIONS.map((item) => (
                <SelectItem key={item} value={item}>
                  {t(`qy_gp_sh_dim_${item}`)}
                </SelectItem>
              ))}
            </SelectContent>
          </Select>
        </QyFilterField>
      </QyFilterBar>

      <QyPageBoundary
        query={query}
        isEmpty={summary != null && segments.length === 0}
        emptyIcon={Scale}
        emptyTitle={t('qy_gp_sh_empty_title')}
        emptyDescription={t('qy_gp_sh_empty_desc')}
      >
        {summary != null && (
          <div className='space-y-4'>
            {/* 报表与页面的模式必须双向印证：真实计费下这张表是历史而不是
                「即将发生」，当成预测会让人把涨跌重复计算一次。 */}
            {!props.shadowMode && (
              <p className='text-warning text-xs'>
                {t('qy_gp_sh_live_notice')}
              </p>
            )}

            {summary.quota_source_error != null &&
              summary.quota_source_error !== '' && (
                <p className='text-destructive flex items-start gap-1.5 text-xs'>
                  <TriangleAlert
                    className='mt-0.5 size-3.5 shrink-0'
                    aria-hidden='true'
                  />
                  <span>
                    {t('qy_gp_sh_quota_error', {
                      reason: summary.quota_source_error,
                    })}
                  </span>
                </p>
              )}

            <QyStatGrid
              items={[
                {
                  key: 'requests',
                  label: t('qy_gp_sh_stat_requests'),
                  value: formatQyCount(summary.total_requests),
                  hint: t('qy_gp_sh_window', {
                    from: formatQyTs(summary.start),
                    to: formatQyTs(summary.end),
                  }),
                },
                {
                  key: 'actual',
                  label: t('qy_gp_sh_stat_actual'),
                  value: (
                    <QyGpDeltaAmount quota={summary.total_actual_quota} plain />
                  ),
                  hint: t('qy_gp_sh_stat_actual_hint'),
                },
                {
                  key: 'delta',
                  label: t('qy_gp_sh_stat_delta'),
                  value: <QyGpDeltaAmount quota={summary.total_delta_quota} />,
                  hint: t('qy_gp_sh_delta_hint'),
                  emphasis: true,
                },
                {
                  key: 'inexact',
                  label: t('qy_gp_sh_stat_inexact'),
                  value: formatQyCount(summary.inexact_requests),
                  hint: t('qy_gp_sh_stat_inexact_hint'),
                },
              ]}
            />

            <QyGpShadowDeltaChart buckets={buckets} />

            <QyGpShadowSegmentsTable segments={segments} />

            {summary.truncated && (
              <p className='text-muted-foreground text-xs'>
                {t('qy_gp_sh_truncated')}
              </p>
            )}
          </div>
        )}
      </QyPageBoundary>
    </div>
  )
}
