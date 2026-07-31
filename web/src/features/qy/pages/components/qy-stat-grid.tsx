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
import type { ReactNode } from 'react'

import { Card, CardContent } from '@/components/ui/card'
import { cn } from '@/lib/utils'

export type QyStatItem = {
  key: string
  label: string
  value: ReactNode
  /** 一行补充说明，例如"含未成熟部分"。 */
  hint?: ReactNode
  /** 让关键数字（可提现余额、待处理笔数）在一屏里跳出来。 */
  emphasis?: boolean
}

type QyStatGridProps = {
  items: QyStatItem[]
  className?: string
}

/**
 * 概览数字网格。推广看板、提现申请、审核队列角标共用同一套排版。
 *
 * 统一成一个组件是为了让"可提现佣金"在推广页与提现页长得一模一样 ——
 * 同一个数字在两处用不同字号和颜色，用户会怀疑它们不是一回事。
 */
export function QyStatGrid(props: QyStatGridProps) {
  if (props.items.length === 0) return null

  return (
    <div
      className={cn(
        'grid gap-3 sm:grid-cols-2 lg:grid-cols-4',
        props.className
      )}
    >
      {props.items.map((item) => (
        <Card key={item.key} data-card-hover='false' className='py-0'>
          <CardContent className='space-y-1 p-3 sm:p-4'>
            <div className='text-muted-foreground truncate text-[11px] font-medium tracking-wider uppercase'>
              {item.label}
            </div>
            <div
              className={cn(
                'truncate font-semibold tabular-nums',
                item.emphasis === true ? 'text-xl' : 'text-base'
              )}
            >
              {item.value}
            </div>
            {item.hint != null && (
              <div className='text-muted-foreground line-clamp-2 text-xs'>
                {item.hint}
              </div>
            )}
          </CardContent>
        </Card>
      ))}
    </div>
  )
}
