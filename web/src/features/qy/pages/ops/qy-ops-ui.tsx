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

import { cn } from '@/lib/utils'

/**
 * 运维页面（可用率 / 违规 / 对账 / 审计 / 健康）共用的排版零件。
 *
 * 只放这几页都要用、而 `pages/components/` 里没有的三样东西：键值明细行、
 * 筛选栏容器、带标签的筛选控件。分页条与概览数字网格一律复用
 * `pages/components/qy-pager` 与 `pages/components/qy-stat-grid` ——
 * 同一个数字在两处用不同字号和颜色，用户会怀疑它们不是一回事。
 */

/** 键值明细行。健康面板、单据详情用它，避免每处各写一套 `dl`。 */
export function QyKeyValue(props: {
  label: ReactNode
  children: ReactNode
  className?: string
}) {
  return (
    <div
      // Midnight Signal 主题按「标签=标注、数值=数据」重排这一行（见
      // styles/qy-sg-apply.css §1 与 §3）。属性无条件输出：其他预设下没有任何
      // 规则消费它，只是 DOM 上多一个属性。
      data-qy-kv=''
      className={cn(
        'flex items-start justify-between gap-3 border-b py-1.5 text-sm last:border-b-0',
        props.className
      )}
    >
      <span className='text-muted-foreground shrink-0'>{props.label}</span>
      <span className='min-w-0 text-right break-all tabular-nums'>
        {props.children}
      </span>
    </div>
  )
}

/** 筛选栏容器。统一 gap 与换行行为，移动端自动堆叠。 */
export function QyFilterBar(props: {
  children: ReactNode
  className?: string
}) {
  return (
    <div className={cn('flex flex-wrap items-end gap-2', props.className)}>
      {props.children}
    </div>
  )
}

/** 带标签的筛选控件。`label` 用 `<label>` 关联，保证键盘与读屏可用。 */
export function QyFilterField(props: {
  label: string
  htmlFor?: string
  children: ReactNode
  className?: string
}) {
  return (
    <div className={cn('flex min-w-0 flex-col gap-1', props.className)}>
      <label
        htmlFor={props.htmlFor}
        className='text-muted-foreground text-xs font-medium'
      >
        {props.label}
      </label>
      {props.children}
    </div>
  )
}
