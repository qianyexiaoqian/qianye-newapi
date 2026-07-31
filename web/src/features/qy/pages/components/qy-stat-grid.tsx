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
import type { CSSProperties, ReactNode } from 'react'

import { Card, CardContent } from '@/components/ui/card'
import { cn } from '@/lib/utils'

import { useQyIsSteinsGate } from '../../hooks/use-qy-theme-preset'

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
  const steinsGate = useQyIsSteinsGate()

  if (props.items.length === 0) return null

  // Steins Gate 的"纸面而非卡片"(规范 §1):同样四项数字，改用发丝线分栏 +
  // 等宽显示级数值 + 标注式标签，去掉四张卡片与它们的边框/圆角/内边距。
  // 这是本主题与默认主题在同一屏上差别最大的一处，卡片化会立刻把它打回原形。
  //
  // 第二版(design-11 §1.5)进一步做成【仪表读数条】:每栏左侧一条 3px 色条，
  // 标签移到数值上方并改成极小号大写宽字距。标签上移是真的换 DOM 顺序，
  // 不是用 flex order 做视觉换位 —— 读屏顺序也应该是"这是什么 → 是多少"，
  // order 只改绘制顺序、不改朗读顺序，那样两种用户看到的是两个不同的界面。
  // 样式挂在 .qy-sg-readout 上，见 styles/qy-sg-data.css 里关于特异度的说明。
  if (steinsGate) {
    return (
      <div
        className={cn('qy-sg-stats qy-sg-readout', props.className)}
        style={
          // 列数跟随实际项数（上限 4）——见 styles/qy-sg-pages.css 里的说明。
          {
            '--qy-sg-stat-cols': Math.min(props.items.length, 4),
          } as CSSProperties
        }
      >
        {props.items.map((item) => (
          <div
            key={item.key}
            className='qy-sg-stat'
            data-emphasis={item.emphasis === true ? 'true' : undefined}
          >
            <div className='qy-sg-stat-label'>{item.label}</div>
            <div className='qy-sg-stat-num'>{item.value}</div>
            {item.hint != null && (
              <div className='qy-sg-stat-sub'>{item.hint}</div>
            )}
          </div>
        ))}
      </div>
    )
  }

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
