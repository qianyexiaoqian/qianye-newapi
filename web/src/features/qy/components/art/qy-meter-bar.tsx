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
 * 余量条(库存 / 剩余份数)。
 *
 * 「剩余 / 初始」两个数并排是**看不出比例**的:`1 / 2` 与 `50 / 100` 读起来
 * 一样,而用户要判断的正是"还剩多少比例"。条子把比例画出来,数字留给调用方
 * 印在旁边(它仍然是可复制、可读屏的那一份)。
 *
 * 归零时轨道换成斜纹(`.qy-art-hatch`):空白轨道与"还在加载"长得一样,
 * 而"已发完"是一个必须一眼认出的终态。
 *
 * `role='progressbar'` + `aria-valuenow/max` + `aria-label`:读屏拿到的是
 * "头奖 1 / 2",与旁边印的数字同一份事实。
 */
export function QyMeterBar(props: {
  value: number
  max: number
  label: string
  className?: string
}) {
  const max = Math.max(0, props.max)
  const value = Math.min(max, Math.max(0, props.value))
  const percent = max === 0 ? 0 : (value / max) * 100
  const empty = value <= 0

  return (
    <div
      role='progressbar'
      aria-label={props.label}
      aria-valuemin={0}
      aria-valuemax={max}
      aria-valuenow={value}
      data-empty={empty ? 'true' : undefined}
      className={cn(
        'h-1.5 w-full overflow-hidden rounded-full',
        empty ? 'qy-art-hatch' : 'bg-muted',
        props.className
      )}
    >
      <div
        className='qy-fx-bar bg-primary h-full rounded-full'
        style={{ width: `${percent}%` }}
      />
    </div>
  )
}
