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

import { QY_RING_CIRCUMFERENCE, QY_RING_RADIUS, qyRingDashOffset } from './ring'

/**
 * 环形进度(倒计时环)。
 *
 * 一圈发丝线做轨道,一段品牌色的弧表示**剩余**:弧越短越接近截止。中间留给
 * 调用方放数字。颜色只用 `--border` 与 `--primary`(一支色相),无投影、无辉光;
 * 弧长变化走 CSS 过渡(`.qy-fx-ring`),缩减动效下直接跳到终值。
 *
 * `role='img' + aria-label` 由调用方给一句完整的话(「距封盘 1 小时 2 分」):
 * 环本身不携带读屏能懂的量,中间那个数字才是。
 */
export function QyRing(props: {
  /** 剩余比例,1 = 满环。`null` 表示"没有可比的窗口",画满环。 */
  fraction: number | null
  label: string
  /** 像素直径,默认 40。 */
  size?: number
  children?: ReactNode
  className?: string
}) {
  const size = props.size ?? 40
  const offset = qyRingDashOffset(props.fraction ?? 1)

  return (
    <span
      className={cn(
        'relative inline-flex shrink-0 items-center justify-center',
        props.className
      )}
      style={{ width: size, height: size }}
    >
      <svg
        role='img'
        aria-label={props.label}
        viewBox='0 0 36 36'
        className='absolute inset-0 size-full -rotate-90'
      >
        <circle
          cx='18'
          cy='18'
          r={QY_RING_RADIUS}
          fill='none'
          stroke='var(--border)'
          strokeWidth='2.5'
        />
        <circle
          cx='18'
          cy='18'
          r={QY_RING_RADIUS}
          fill='none'
          stroke='var(--primary)'
          strokeWidth='2.5'
          strokeLinecap='round'
          strokeDasharray={QY_RING_CIRCUMFERENCE}
          strokeDashoffset={offset}
          className='qy-fx-ring'
          data-testid='qy-ring-arc'
        />
      </svg>
      {props.children != null && (
        <span className='relative text-center leading-none'>
          {props.children}
        </span>
      )}
    </span>
  )
}
