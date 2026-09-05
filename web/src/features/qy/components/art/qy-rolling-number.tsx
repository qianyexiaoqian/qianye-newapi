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
import { useEffect, useRef, useState, type ReactNode } from 'react'

import { cn } from '@/lib/utils'

import { useQyReducedMotion } from '../../pages/wheel/lib/use-reduced-motion'

/**
 * 数字"到账"滚动:值变化时旧值向上滚出、新值从下滚入。
 *
 * ## 为什么不是逐帧数到终值
 *
 * 逐帧计数要么 setInterval(纪律禁止),要么 CSS `@property` + `counter()`
 * —— 后者把数字放进 `content`,读屏与页内搜索都拿不到,千分位也丢了。
 * 这里屏幕上**永远只有真实的新值**(由 `render` 渲染,含千分位与单位名),
 * 动效只是额外叠一层 `aria-hidden` 的旧值滚出去;`onAnimationEnd` 撤掉那一层,
 * 没有定时器。
 *
 * ## 只在数值变化时
 *
 * 首次挂载不动:进页面时大数字"从下面滚上来"是把每一次打开都演成一次到账。
 * 缩减动效下不叠旧值层,新值直接就位(CSS 也把 `.qy-fx-roll-in` 关了)。
 */
export function QyRollingNumber(props: {
  value: number
  render: (value: number) => ReactNode
  className?: string
}) {
  const reducedMotion = useQyReducedMotion()
  const lastValue = useRef(props.value)
  const [previous, setPrevious] = useState<number | null>(null)

  const { value } = props
  useEffect(() => {
    if (lastValue.current === value) return
    if (!reducedMotion) setPrevious(lastValue.current)
    lastValue.current = value
  }, [value, reducedMotion])

  const rolling = previous != null

  return (
    <span
      className={cn('relative inline-block', props.className)}
      data-rolling={rolling ? 'true' : undefined}
    >
      <span
        key={value}
        className={cn('inline-block', rolling && 'qy-fx-roll-in')}
        onAnimationEnd={() => setPrevious(null)}
      >
        {props.render(value)}
      </span>
      {rolling && (
        <span
          aria-hidden='true'
          className='qy-fx-roll-out pointer-events-none absolute inset-0 inline-block'
        >
          {props.render(previous)}
        </span>
      )}
    </span>
  )
}
