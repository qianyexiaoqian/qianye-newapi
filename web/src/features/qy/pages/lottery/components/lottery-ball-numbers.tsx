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
 * `hits` 里的号加实心边框与对勾式的强调；其余号降到描边态。这条视觉差**不能
 * 只靠颜色**——红球本来就是红的，用颜色区分命中与否在色弱与深色模式下会整个
 * 塌掉。所以命中态同时改：边框加粗、字重变粗、并挂 `aria-label` 说明命中。
 *
 * ## 拿不到号时画什么
 *
 * 画 `emptyText`（默认一个占位破折号），**不画零颗球**：一排空白与"还在加载"
 * 长得一样，而这一格恰恰是用户最急着看的那一格。
 */
export function QyLotBallNumbers(props: {
  /** 规范化串 `08,10,11|03`。解析不了按"没有号"处理，不抛。 */
  pick: string
  /** 要高亮的号。缺省 = 一个都不高亮（还没开奖 / 这一格不是"我的号"）。 */
  hits?: { reds: number[]; blues: number[] }
  /** 紧凑态用在表格单元格里。 */
  size?: 'md' | 'sm'
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
  const small = props.size === 'sm'

  const ball = (value: number, tone: 'blue' | 'red', hit: boolean) => (
    <span
      key={`${tone}-${value}`}
      aria-label={
        hit
          ? t('qy_lot_ball_hit_aria', { no: String(value).padStart(2, '0') })
          : undefined
      }
      className={cn(
        'inline-flex shrink-0 items-center justify-center rounded-full tabular-nums',
        small ? 'size-6 text-[11px]' : 'size-8 text-sm',
        // 命中与未命中差的不只是颜色：边框粗细 + 字重同时变，
        // 这样在色弱、深色模式与灰度截图上仍然分得开。
        hit
          ? cn(
              'border-2 font-bold text-white',
              tone === 'red'
                ? 'border-red-900 bg-red-500 dark:border-red-200'
                : 'border-blue-900 bg-blue-500 dark:border-blue-200'
            )
          : cn(
              'border font-medium',
              tone === 'red'
                ? 'border-red-500/40 text-red-600 dark:text-red-400'
                : 'border-blue-500/40 text-blue-600 dark:text-blue-400'
            )
      )}
    >
      {String(value).padStart(2, '0')}
    </span>
  )

  return (
    <span
      className={cn(
        'inline-flex flex-wrap items-center gap-1',
        props.className
      )}
    >
      {parsed.reds.map((value) => ball(value, 'red', hitReds.includes(value)))}
      {parsed.blues.length > 0 && parsed.reds.length > 0 && (
        <span aria-hidden='true' className='text-muted-foreground px-0.5'>
          |
        </span>
      )}
      {parsed.blues.map((value) =>
        ball(value, 'blue', hitBlues.includes(value))
      )}
    </span>
  )
}
