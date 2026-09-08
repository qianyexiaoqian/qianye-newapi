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
 * 奖档的档位牌:一枚带号的圆牌。
 *
 * 三张奖档清单(抽奖 / 双色球 / 转盘)此前各自是一张多列表格,第一列印着
 * 「第 N 档」三个字。那一列在窄屏上占掉整行三分之一的宽度,却只承载一个数;
 * 而"第几档"恰恰是这份清单里**唯一有序**的量 —— 用户扫的是"从上往下奖越来越
 * 小",不是逐行读字。换成一枚圆牌之后,这一列缩到一个字符宽,顺序仍然一眼可读。
 *
 * ## 颜色
 *
 * 默认只有头奖挑出来(`--warning` 兑一层背景色),其余中性 —— design-14 是
 * "一支色相",给每一档各配一种颜色会把这份清单变成一张色卡。转盘那张清单例外:
 * 它传 `color`,取值是盘面上那一格的扇区色,让"表里的这一行"与"盘上的那一格"
 * 对得起来 —— 那是转盘唯一需要跨两个图形建立的对应关系。
 *
 * 纯装饰:档位号在同一行的文字里已经说过了(「第 2 档」),圆牌重复它只是噪音,
 * 所以 `aria-hidden`。
 */
export function QyTierMedal(props: {
  /** 档位号。`0` 画一个空心圆牌 —— 「谢谢参与」那种没有档位的行。 */
  tier: number
  /** 与盘面扇区对齐的颜色。只有转盘给。 */
  color?: string
  className?: string
}) {
  const isTop = props.tier === 1
  const isNone = props.tier <= 0

  return (
    <span
      aria-hidden='true'
      // 一档一枚牌是这份清单的形状契约（`lottery/__tests__/visual-shape.test.tsx`
      // 按它数行）。挂 testid 而不是让测试去认 Tailwind 类名：类名会随排版调整，
      // 而"一档一枚牌"不会。
      data-testid='qy-tier-medal'
      className={cn(
        'inline-flex size-7 shrink-0 items-center justify-center rounded-full text-xs font-bold tabular-nums',
        props.color == null && isTop && 'bg-warning/25 text-foreground',
        props.color == null && !isTop && 'bg-muted text-muted-foreground',
        isNone && 'border',
        props.className
      )}
      style={
        props.color == null
          ? undefined
          : // 扇区色是实心的,牌面文字要压白才读得出来 —— 与盘面上的处理一致。
            { background: props.color, color: 'var(--primary-foreground)' }
      }
    >
      {isNone ? '' : props.tier}
    </span>
  )
}
