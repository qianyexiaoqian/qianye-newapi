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
import type { LucideIcon } from 'lucide-react'
import type { ReactNode } from 'react'

import { cn } from '@/lib/utils'

export type QyPhaseStepState = 'current' | 'done' | 'pending'

export type QyPhaseStep = {
  key: string
  label: ReactNode
  icon: LucideIcon
  state: QyPhaseStepState
}

const DOT_CLASS: Record<QyPhaseStepState, string> = {
  done: 'border-primary bg-primary text-primary-foreground',
  current: 'border-primary text-primary qy-fx-pulse',
  pending: 'border-border text-muted-foreground',
}

const LABEL_CLASS: Record<QyPhaseStepState, string> = {
  done: 'text-foreground',
  current: 'text-foreground font-medium',
  pending: 'text-muted-foreground',
}

/**
 * 横向的阶段轨:图标节点 + 连线 + 一个词的标签。
 *
 * 与 `QyTimeline`(纵向、带时刻、给单据用)不是一回事:这一条回答的是
 * "现在到哪一步了",放在页头一行就讲完,不带说明文字。已走过的节点填实、
 * 当前节点描边呼吸(`.qy-fx-pulse`,缩减动效下静止)、未到的灰色占位 ——
 * 未到的节点保留,用户要知道后面还有几步。
 *
 * 颜色只用 `--primary` 与中性灰;连线是一根 2px 的发丝线,走过的那段换成
 * 品牌色。零投影、零辉光。
 */
export function QyPhaseTrack(props: {
  steps: QyPhaseStep[]
  /** 整条轨的可访问名称(「活动进度」)。 */
  label: string
  className?: string
}) {
  if (props.steps.length === 0) return null

  return (
    <ol
      aria-label={props.label}
      className={cn('flex w-full items-start', props.className)}
    >
      {props.steps.map((step, index) => {
        const Icon = step.icon
        const isLast = index === props.steps.length - 1
        // 连线属于"从这一步到下一步":这一步走完了线才亮。
        const lineDone = step.state === 'done'
        return (
          <li
            key={step.key}
            aria-current={step.state === 'current' ? 'step' : undefined}
            data-state={step.state}
            className={cn(
              'flex min-w-0 items-start',
              isLast ? 'flex-none' : 'flex-1'
            )}
          >
            <span className='flex min-w-0 flex-col items-center gap-1'>
              <span
                className={cn(
                  'inline-flex size-7 shrink-0 items-center justify-center rounded-full border-2',
                  DOT_CLASS[step.state]
                )}
              >
                <Icon aria-hidden='true' className='size-3.5' />
              </span>
              <span
                className={cn(
                  'max-w-16 text-center text-[11px] leading-tight break-words',
                  LABEL_CLASS[step.state]
                )}
              >
                {step.label}
              </span>
            </span>
            {!isLast && (
              <span
                aria-hidden='true'
                className={cn(
                  'mt-3 h-0.5 min-w-3 flex-1 rounded-full',
                  lineDone ? 'bg-primary' : 'bg-border'
                )}
              />
            )}
          </li>
        )
      })}
    </ol>
  )
}
