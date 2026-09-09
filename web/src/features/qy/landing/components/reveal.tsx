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

interface QyLandingRevealProps {
  children: ReactNode
  className?: string
  /** 同一组元素错开入场的毫秒数。 */
  delay?: number
  as?: 'div' | 'section' | 'li'
}

/**
 * 进入视口时淡入。
 *
 * **刻意不复用上游的 `<AnimateInView>`**：它挂的类名是
 * `landing-animate-fade-up`，而 Midnight Signal 主题 §12 的作用域根正是
 * `.min-h-svh:has(> section .landing-animate-fade-up)` —— 那一整块是为**上游**
 * 首页写的补救式改写（去色、区段间距、标题字重）。本页自己就是照主题画的，
 * 让那块规则也压上来只会两套版式打架。换个类名就把两页彻底隔开。
 *
 * 终态写在 CSS 的 `[data-revealed='true']` 上而不是这里，`prefers-reduced-motion`
 * 才能在同一处一次性关掉（design-14 §4 第 6 条）。取不到
 * `IntersectionObserver` 时直接判定已入场：看不见内容比看不见动效严重得多。
 */
export function QyLandingReveal(props: QyLandingRevealProps) {
  const ref = useRef<HTMLDivElement>(null)
  const [revealed, setRevealed] = useState(false)

  useEffect(() => {
    const el = ref.current
    if (!el) return
    if (typeof IntersectionObserver === 'undefined') {
      setRevealed(true)
      return
    }

    const observer = new IntersectionObserver(
      ([entry]) => {
        if (!entry.isIntersecting) return
        setRevealed(true)
        observer.disconnect()
      },
      { threshold: 0.12, rootMargin: '0px 0px -40px 0px' }
    )
    observer.observe(el)
    return () => observer.disconnect()
  }, [])

  const Tag = props.as ?? 'div'

  return (
    <Tag
      ref={ref as never}
      className={cn('qy-lp-reveal', props.className)}
      data-revealed={revealed}
      style={
        props.delay
          ? ({ '--qy-lp-delay': `${props.delay}ms` } as React.CSSProperties)
          : undefined
      }
    >
      {props.children}
    </Tag>
  )
}
