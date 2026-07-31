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
import { formatTimestampToDate } from '@/lib/format'
import { cn } from '@/lib/utils'

import type { QyTimelineItem } from '../lib/types'

type QyTimelineProps = {
  items: QyTimelineItem[]
  className?: string
}

const DOT_CLASS: Record<NonNullable<QyTimelineItem['state']>, string> = {
  done: 'bg-success border-success',
  current: 'bg-warning border-warning animate-pulse',
  failed: 'bg-destructive border-destructive',
  pending: 'bg-muted border-border',
}

const TITLE_CLASS: Record<NonNullable<QyTimelineItem['state']>, string> = {
  done: 'text-foreground',
  current: 'text-foreground font-medium',
  failed: 'text-destructive font-medium',
  pending: 'text-muted-foreground',
}

/**
 * 单据状态时间线：提交 → 审核 → 打款 → 到账。
 *
 * 未到达的节点保留灰色占位而不是隐藏 —— 提现流程有 4~5 步，只显示已发生的
 * 部分会让用户以为"卡住了"，实际上只是还没轮到。
 */
export function QyTimeline(props: QyTimelineProps) {
  if (props.items.length === 0) return null

  return (
    <ol className={cn('relative space-y-4 ps-5', props.className)}>
      {props.items.map((item, index) => {
        const state = item.state ?? 'pending'
        const isLast = index === props.items.length - 1
        return (
          <li key={item.key} className='relative'>
            {/* 连接线：最后一个节点不画，否则会拖出一截悬空的尾巴 */}
            {!isLast && (
              <span
                className='bg-border absolute -start-[0.8125rem] top-4 h-full w-px'
                aria-hidden='true'
              />
            )}
            <span
              className={cn(
                'absolute top-1 -start-5 size-2.5 rounded-full border',
                DOT_CLASS[state]
              )}
              aria-hidden='true'
            />
            <div className='space-y-0.5'>
              <div className='flex flex-wrap items-baseline gap-x-2 gap-y-0.5'>
                <span className={cn('text-sm', TITLE_CLASS[state])}>
                  {item.title}
                </span>
                <span className='text-muted-foreground font-mono text-xs tabular-nums'>
                  {formatTimestampToDate(item.timestamp)}
                </span>
              </div>
              {item.description != null && (
                <div className='text-muted-foreground text-xs'>
                  {item.description}
                </div>
              )}
            </div>
          </li>
        )
      })}
    </ol>
  )
}
