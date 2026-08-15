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

import { GroupBadge } from '@/components/group-badge'
import { cn } from '@/lib/utils'

import { formatQyCount } from '../../ops/format'
import {
  getQyAvailStateStyle,
  QY_AVAIL_NO_VALUE_STYLE,
  QY_AVAIL_TONE_STYLES,
  qyAvailCellState,
  qyAvailOutcomeKey,
  qyAvailRowScale,
  qyAvailToneOf,
  type QyAvailMetricDef,
  type QyAvailRowScale,
} from '../constants'
import type { QyAvailCell, QyAvailMatrix } from '../types'

function cellKey(model: string, group: string): string {
  return `${model} ${group}`
}

type QyAvailabilityMatrixProps = {
  matrix: QyAvailMatrix
  /** 主指标。切换它只换这张表的取值与配色，不会重新请求。 */
  metric: QyAvailMetricDef
  onSelectCell: (cell: QyAvailCell) => void
}

/**
 * 分组 × 模型热力图。
 *
 * 用 CSS Grid 而不是 vchart 的 heatmap：这里的诉求是「一个可点击、可横向
 * 滚动的二维彩色表」，图元注册 / tooltip 定制 / 主题异步加载的代价换不来
 * 任何收益，而 `div` + Tailwind 在移动端和键盘可达性上都更可控。
 *
 * 每格是 **颜色 + 符号双编码**：色盲用户只看颜色分不出好坏，
 * 而这一页的全部信息量都在颜色上。
 *
 * 两套配色逻辑，对应两类问题：
 *   - 可用率 → 后端下发的六态（绝对阈值，`ok / degraded / down` 有明确含义）；
 *   - 延迟 / 首字 / 速度 → 同一模型在各分组之间的**相对**快慢，理由见
 *     {@link qyAvailRowScale}。
 */
export function QyAvailabilityMatrix(props: QyAvailabilityMatrixProps) {
  const { t } = useTranslation()

  const byKey = useMemo(() => {
    const map = new Map<string, QyAvailCell>()
    for (const cell of props.matrix.cells) {
      map.set(cellKey(cell.model, cell.group), cell)
    }
    return map
  }, [props.matrix.cells])

  const metric = props.metric
  const groups = props.matrix.groups

  // 被截断掉的模型整行都没查过。用后端显式下发的 covered_models 判定，
  // 而不是「格子在不在 cells 里」——后者与「该分组不提供这个模型」不可分辨。
  const covered = useMemo(
    () => new Set(props.matrix.covered_models),
    [props.matrix.covered_models]
  )

  // 每个模型一行的相对刻度。可用率走六态，不需要相对刻度。
  const rowScales = useMemo(() => {
    const scales = new Map<string, QyAvailRowScale>()
    if (metric.key === 'availability') return scales
    for (const model of props.matrix.models) {
      const values: number[] = []
      for (const group of groups) {
        const cell = byKey.get(cellKey(model, group.group))
        if (cell == null) continue
        const value = metric.valueOf(cell)
        if (value != null) values.push(value)
      }
      scales.set(model, qyAvailRowScale(values, metric.higherIsBetter))
    }
    return scales
  }, [byKey, groups, metric, props.matrix.models])

  const columns = `minmax(11rem, 1.4fr) repeat(${groups.length}, minmax(5.5rem, 1fr))`

  return (
    <div className='overflow-x-auto rounded-lg border'>
      <div className='min-w-max'>
        <div
          className='bg-muted/50 sticky top-0 z-10 grid border-b'
          style={{ gridTemplateColumns: columns }}
        >
          <div className='bg-muted/50 sticky left-0 z-20 px-3 py-2 text-xs font-medium'>
            {t('qy_avl_model')}
          </div>
          {groups.map((group) => (
            <div
              key={group.group}
              className='flex items-center justify-center px-2 py-2'
              title={group.desc === '' ? group.group : group.desc}
            >
              <GroupBadge group={group.group} size='sm' />
            </div>
          ))}
        </div>

        {props.matrix.models.map((model) => (
          <div
            key={model}
            className='hover:bg-muted/30 grid border-b last:border-b-0'
            style={{ gridTemplateColumns: columns }}
          >
            <div
              className='bg-background sticky left-0 z-10 truncate px-3 py-1.5 text-xs font-medium'
              title={model}
            >
              {model}
            </div>
            {groups.map((group) => {
              const cell = byKey.get(cellKey(model, group.group))
              return (
                <QyAvailabilityMatrixCell
                  key={group.group}
                  model={model}
                  group={group.group}
                  cell={cell}
                  measured={covered.has(model)}
                  metric={metric}
                  scale={rowScales.get(model) ?? null}
                  onSelect={props.onSelectCell}
                />
              )
            })}
          </div>
        ))}
      </div>

      {props.matrix.truncated && (
        <p className='text-warning border-t px-3 py-2 text-xs'>
          {t('qy_avl_truncated')}
        </p>
      )}
    </div>
  )
}

/**
 * 单个格子。
 *
 * `not_offered`（该分组根本不提供这个模型）不可点击：点开一个空抽屉
 * 只会让人怀疑是页面坏了。其余状态哪怕没有数字也允许点开看时序。
 *
 * `measured` 为假表示这一格因为响应被截断而**根本没查过**，状态是「未知」
 * 而不是「未提供」，理由见 {@link qyAvailCellState}。
 */
function QyAvailabilityMatrixCell(props: {
  model: string
  group: string
  cell: QyAvailCell | undefined
  measured: boolean
  metric: QyAvailMetricDef
  scale: QyAvailRowScale
  onSelect: (cell: QyAvailCell) => void
}) {
  const { t } = useTranslation()
  const { cell, metric } = props
  const state = qyAvailCellState(cell, props.measured)
  const stateStyle = getQyAvailStateStyle(state)
  const disabled = cell == null || state === 'not_offered'

  const value = cell == null ? null : metric.valueOf(cell)
  const samples = cell == null ? 0 : metric.samplesOf(cell)
  const tone = qyAvailToneOf(value, props.scale)
  const toneStyle = QY_AVAIL_TONE_STYLES[tone]

  // 可用率用六态。性能指标没有取值时既不能用六态的绿色（那会把「可用率正常
  // 但延迟样本不足」画成一个绿色的空格），也没有相对色可着。
  //
  // 没查过的格子在任何主指标下都走「未知」那套灰底：性能指标的「无取值」样式是
  // 透明的，会被读成「查了，只是样本不足」——同样是一个我们给不出的结论。
  let style: { cellClass: string; glyph: string } = stateStyle
  if (
    props.measured &&
    metric.key !== 'availability' &&
    state !== 'not_offered'
  ) {
    style = value == null ? QY_AVAIL_NO_VALUE_STYLE : toneStyle
  }

  const lines = [`${props.model} · ${props.group}`]
  if (!props.measured) {
    lines.push(t('qy_avl_not_measured'))
  } else if (metric.key === 'availability') {
    lines.push(t(stateStyle.labelKey))
  } else if (value != null && tone !== 'none') {
    lines.push(t(toneStyle.labelKey))
  }
  lines.push(`${t(metric.labelKey)}: ${metric.format(value)}`)
  if (cell != null) {
    lines.push(`${t('qy_avl_samples')}: ${formatQyCount(samples)}`)
    lines.push(`${t('qy_avl_req_total')}: ${formatQyCount(cell.req_total)}`)
    if (cell.top_reason != null && cell.top_reason !== '') {
      lines.push(
        `${t('qy_avl_top_reason')}: ${t(qyAvailOutcomeKey(cell.top_reason))}`
      )
    }
  }
  const title = lines.join('\n')

  return (
    <button
      type='button'
      disabled={disabled}
      title={title}
      aria-label={title}
      onClick={() => {
        if (cell != null) props.onSelect(cell)
      }}
      className={cn(
        'm-0.5 flex flex-col items-center justify-center rounded-md py-1.5 text-xs font-medium tabular-nums transition-colors',
        style.cellClass,
        disabled
          ? 'cursor-default'
          : 'focus-visible:ring-ring cursor-pointer hover:brightness-110 focus-visible:ring-2 focus-visible:outline-none'
      )}
    >
      <span aria-hidden='true' className='text-[10px] leading-none opacity-70'>
        {style.glyph}
      </span>
      <span className='leading-tight'>{metric.format(value)}</span>
    </button>
  )
}
