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
import type { CSSProperties } from 'react'

import { cn } from '@/lib/utils'

/** 粒子数。12 颗均匀分布一圈,再多就成了烟花,与「信号灯而不是主题色」相悖。 */
const PARTICLES = 12

/**
 * 中奖那一刻的一次性粒子:一圈小点向外散开并淡出,外加一道扩张的细环。
 *
 * ## 刻意不做辉光
 *
 * design-14 把品牌色定义成跑道灯:辉光全站 ≤ 2 处,而那两处已经分给了后台
 * 页头与落地页首屏。这里用**实心小点 + 描边环**表达"有事发生",不用径向渐变
 * —— 它不占辉光配额,也不违反零投影。
 *
 * ## 一次性
 *
 * 它没有状态:挂载即播放(CSS animation forwards),播完停在透明。想再放一次
 * 由调用方换 `key` 重新挂载。缩减动效下 CSS 直接把它置为透明,不显示。
 *
 * 纯装饰,`aria-hidden`。
 */
export function QyBurst(props: { className?: string }) {
  return (
    <span
      aria-hidden='true'
      data-testid='qy-burst'
      className={cn(
        'pointer-events-none absolute inset-0 overflow-visible',
        props.className
      )}
    >
      <span
        className='qy-fx-burst-ring absolute top-1/2 left-1/2 size-3/4 rounded-full border-2'
        style={{ borderColor: 'var(--primary)' }}
      />
      {Array.from({ length: PARTICLES }, (_, index) => {
        const angle = (index / PARTICLES) * Math.PI * 2
        const distance = index % 2 === 0 ? 46 : 34
        const style = {
          '--qy-fx-dx': `${Math.round(Math.cos(angle) * distance)}%`,
          '--qy-fx-dy': `${Math.round(Math.sin(angle) * distance)}%`,
          background: index % 3 === 0 ? 'var(--foreground)' : 'var(--primary)',
        } as CSSProperties
        return (
          <span
            key={index}
            className='qy-fx-burst-particle absolute top-1/2 left-1/2 size-2 rounded-full'
            style={style}
          />
        )
      })}
    </span>
  )
}
