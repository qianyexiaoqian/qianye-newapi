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
import { cn } from '@/lib/utils'

/**
 * 回执戳记:一圈虚线环 + 一个对勾,盖章式落下(`.qy-fx-stamp`)。
 *
 * 下单成功屏此前是标题一行 + 三行键值 + 一段"接下来"的话。戳记把"成功"这件事
 * 画出来,文字只留单号、状态与下一步。品牌色描边、无填充、无投影;纯装饰,
 * `aria-hidden`(旁边的标题「兑换成功」才是读屏念的那一句)。
 */
export function QyStamp(props: { className?: string }) {
  return (
    <svg
      aria-hidden='true'
      data-testid='qy-stamp'
      viewBox='0 0 64 64'
      className={cn('qy-fx-stamp size-16', props.className)}
    >
      <circle
        cx='32'
        cy='32'
        r='29'
        fill='none'
        stroke='var(--primary)'
        strokeWidth='2'
        strokeDasharray='4 3'
      />
      <circle
        cx='32'
        cy='32'
        r='23'
        fill='none'
        stroke='var(--primary)'
        strokeWidth='1.5'
      />
      <path
        d='M21 33 L29 41 L44 25'
        fill='none'
        stroke='var(--primary)'
        strokeWidth='3.5'
        strokeLinecap='round'
        strokeLinejoin='round'
      />
    </svg>
  )
}
