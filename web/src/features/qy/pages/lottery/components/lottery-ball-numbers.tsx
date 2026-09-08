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
import { useTranslation } from 'react-i18next'

import { cn } from '@/lib/utils'

import { qyLotBallSafeParsePick } from '../lib/ball'

/**
 * 一组号码，按彩票的习惯画成红球 + 蓝球。
 *
 * ## 为什么不是一行 `08,10,11|03`
 *
 * 那串字节是**进哈希链的那一份**，它必须在「我的参与」与证据链里一字不差地
 * 留着（用户拿它去比对链）。但它不是给人用来"一眼看出中没中"的形状：一个
 * 竖线分隔的定长串，与旁边那串长得一模一样，肉眼逐位比对两组号是这一屏上
 * 最容易出错的动作。所以展示位画球、留存位留串，两者同时在。
 *
 * ## 高亮就是"中不中"本身
 *
 * `hits` 里的号画成一颗**实心球**；其余号降到描边态。这条视觉差**不能只靠
 * 颜色**——红球本来就是红的，用颜色区分命中与否在色弱与深色模式下会整个
 * 塌掉。所以命中态同时改三处：填充（球 / 空心）、字重、并挂 `aria-label`
 * 说明命中。三者里任何一个单独拿掉，灰度截图上仍然分得开。
 *
 * ## 为什么是球而不是一个圆
 *
 * 实心态走 `.qy-art-ball`：底色由 `currentColor` 给（红球红、蓝球蓝），CSS 再
 * 叠一层左上高光与一层右下暗面。彩票的号码本来就是一颗球，画成扁平的色块时
 * 这一屏读起来像一张状态表而不像开奖——项目方那句「文字画太多、观感很差」
 * 指的正是这个。光用白与黑的低透明度叠加而不是调色板里的颜色，所以换主题
 * 预设时不需要跟着改（口径写在 `styles/qy-visual.css`）。
 *
 * ## `reveal` 只给开出的那一行
 *
 * 逐颗落位（第 N 颗延迟 N × 90ms）是"开奖"这个动作本身，所以只有**本期开出
 * 的号**那一行给它；我的号、表格里的号一律静止——每一处都在弹跳的页面读不了。
 * 缩减动效下 CSS 直接把动画关掉、球停在终态。
 *
 * ## 拿不到号时画什么
 *
 * 画 `emptyText`（默认一个占位破折号），**不画零颗球**：一排空白与"还在加载"
 * 长得一样，而这一格恰恰是用户最急着看的那一格。
 */
/** 逐颗落位的间隔。6 红 + 1 蓝共 7 颗 ≈ 0.6 秒开完，再慢就等得住了。 */
const REVEAL_STEP_MS = 90

const BALL_SIZE = {
  lg: 'size-11 text-base',
  md: 'size-8 text-sm',
  sm: 'size-6 text-[11px]',
} as const

export function QyLotBallNumbers(props: {
  /** 规范化串 `08,10,11|03`。解析不了按"没有号"处理，不抛。 */
  pick: string
  /** 要高亮的号。缺省 = 一个都不高亮（还没开奖 / 这一格不是"我的号"）。 */
  hits?: { reds: number[]; blues: number[] }
  /** `lg` 给详情页开出的那一行，`sm` 给表格单元格。 */
  size?: 'lg' | 'md' | 'sm'
  /**
   * 这一组**就是本期开出的号**。每颗都画成实心球，且一个「命中」标签都不挂 ——
   * 开奖号没有可对照的对象，说它"命中"是一句没有主语的话。
   *
   * 不能靠 `hits` 顶替：那会让读屏把七颗开奖号逐个念成"命中 03、命中 09…"。
   * 也不能与 `reveal` 合并：`reveal` 是"要不要放落位动效"，缩减动效下它被 CSS
   * 关掉，而开奖号在那时**照样**得是实心球。
   */
  drawn?: boolean
  /** 逐颗落位。只给"本期开出的号"那一行——见上文。 */
  reveal?: boolean
  /** 号解析不出来时显示什么。 */
  emptyText?: string
  className?: string
}) {
  const { t } = useTranslation()
  const parsed = qyLotBallSafeParsePick(props.pick)
  if (parsed == null) {
    return (
      <span className='text-muted-foreground text-xs'>
        {props.emptyText ?? '—'}
      </span>
    )
  }

  const hitReds = props.hits?.reds ?? []
  const hitBlues = props.hits?.blues ?? []
  const drawn = props.drawn === true

  const ball = (
    value: number,
    tone: 'blue' | 'red',
    hit: boolean,
    order: number
  ) => (
    <span
      key={`${tone}-${value}`}
      aria-label={
        hit
          ? t('qy_lot_ball_hit_aria', { no: String(value).padStart(2, '0') })
          : undefined
      }
      data-solid={hit || drawn ? 'true' : 'false'}
      style={
        props.reveal === true
          ? { animationDelay: `${order * REVEAL_STEP_MS}ms` }
          : undefined
      }
      className={cn(
        'qy-art-ball inline-flex shrink-0 items-center justify-center rounded-full tabular-nums',
        BALL_SIZE[props.size ?? 'md'],
        props.reveal === true && 'qy-fx-ball-drop',
        // 球体的底色取自 `currentColor`，`.qy-art-ball` 只往上叠光；未命中态
        // 的字色就是这一支色，所以两态用同一个文字色轴表达。
        hit || drawn
          ? cn('font-bold', tone === 'red' ? 'text-red-500' : 'text-blue-500')
          : cn(
              'border font-medium',
              tone === 'red'
                ? 'border-red-500/40 text-red-600 dark:text-red-400'
                : 'border-blue-500/40 text-blue-600 dark:text-blue-400'
            )
      )}
    >
      {/* 数字自己一层：实心球的 `currentColor` 是球的颜色，字得反过来压白，
          否则红底红字。空心态不套色，跟着球的描边色走。 */}
      <span className={hit || drawn ? 'text-white' : undefined}>
        {String(value).padStart(2, '0')}
      </span>
    </span>
  )

  return (
    <span
      className={cn(
        'inline-flex flex-wrap items-center gap-1',
        props.className
      )}
    >
      {parsed.reds.map((value, index) =>
        ball(value, 'red', hitReds.includes(value), index)
      )}
      {parsed.blues.length > 0 && parsed.reds.length > 0 && (
        <span aria-hidden='true' className='text-muted-foreground px-0.5'>
          |
        </span>
      )}
      {parsed.blues.map((value, index) =>
        ball(
          value,
          'blue',
          hitBlues.includes(value),
          parsed.reds.length + index
        )
      )}
    </span>
  )
}
