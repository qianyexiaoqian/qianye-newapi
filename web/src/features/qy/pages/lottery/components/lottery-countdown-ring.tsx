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
import { CalendarCheck } from 'lucide-react'
import { useTranslation } from 'react-i18next'

import { cn } from '@/lib/utils'

import { QyRing } from '../../../components/art/qy-ring'
import { formatQyDuration, formatQyTs } from '../../ops/format'
import type { qyLotCountdown } from '../lib/display'

/**
 * 倒计时那一格：环 + 标签 + 时长。
 *
 * 大厅卡片、转盘卡片、详情页头三处共用，因为它们必须显示同一个数字
 * （见 `qyLotCountdown`）—— 现在还必须画同一个环。没有倒计时（已封盘 /
 * 已结束）时退化成「开奖时间 + 时刻」，用一个日历图标占住环的位置，
 * 三张卡上这一格的形状才对得齐。
 *
 * 标签与时长仍是可见文字：环只表达"还剩几成"，读屏与页内搜索靠的是字。
 */
export function QyLotCountdownRing(props: {
  countdown: ReturnType<typeof qyLotCountdown>
  drawAt: number
  /** 环的像素直径，默认 32。 */
  size?: number
  /** 宿主已经写了标签（概览数字栏那一格）时不再重复印一遍。 */
  hideLabel?: boolean
  className?: string
}) {
  const { t } = useTranslation()
  const size = props.size ?? 32
  const { countdown } = props

  if (countdown == null) {
    return (
      <span className={cn('inline-flex items-center gap-2', props.className)}>
        <span
          aria-hidden='true'
          className='text-muted-foreground inline-flex shrink-0 items-center justify-center rounded-full border'
          style={{ width: size, height: size }}
        >
          <CalendarCheck className='size-3.5' />
        </span>
        <span className='flex min-w-0 flex-col leading-tight'>
          {props.hideLabel !== true && (
            <span className='text-muted-foreground text-[11px]'>
              {t('qy_lot_draw_at')}
            </span>
          )}
          <span className='text-xs tabular-nums'>
            {formatQyTs(props.drawAt)}
          </span>
        </span>
      </span>
    )
  }

  const label = t(countdown.labelKey)
  const duration = formatQyDuration(countdown.seconds)
  return (
    <span className={cn('inline-flex items-center gap-2', props.className)}>
      <QyRing
        fraction={countdown.fraction}
        label={`${label} ${duration}`}
        size={size}
      />
      <span className='flex min-w-0 flex-col leading-tight'>
        {props.hideLabel !== true && (
          <span className='text-muted-foreground text-[11px]'>{label}</span>
        )}
        <span className='text-xs tabular-nums'>{duration}</span>
      </span>
    </span>
  )
}
