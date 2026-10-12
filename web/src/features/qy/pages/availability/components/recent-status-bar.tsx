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

import { getSuccessRateDotClass } from '@/features/performance-metrics/lib/format'
import { cn } from '@/lib/utils'

import { QY_AVAIL_RECENT_SLOTS, qyAvailRecentSlots } from '../constants'
import type { QyPlazaHour } from '../types'

// 格子的位置本身就是含义(距现在几小时),用位置号当键。
const SLOT_IDS = Array.from(
  { length: QY_AVAIL_RECENT_SLOTS },
  (_, slot) => slot
)

/**
 * 「最近请求」状态条:最近 24 小时,一小时一格,颜色与模型广场卡片的状态条同一套
 * (getSuccessRateDotClass),灰格 = 那一小时没有请求。最右一格是当前这一小时。
 *
 * 它回答的是可用率数字回答不了的问题:**现在**好不好。整段可用率被一次故障拉低之后
 * 要很久才回得去,而这条线上最近几格已经是绿的。
 */
export function QyAvailRecentBar(props: {
  hours: QyPlazaHour[] | undefined
  nowSec: number
  className?: string
}) {
  const { t } = useTranslation()
  const slots = qyAvailRecentSlots(props.hours, props.nowSec)
  return (
    <span
      role='img'
      aria-label={t('qy_avl_recent_bar')}
      title={t('qy_avl_recent_bar')}
      className={cn('flex h-2.5 items-center gap-px', props.className)}
    >
      {SLOT_IDS.map((slot) => {
        const rate = slots[slot]
        return (
          <span
            key={slot}
            aria-hidden
            className={cn(
              'h-full w-[2px] shrink-0 rounded-xs',
              rate == null
                ? 'bg-muted-foreground/15'
                : getSuccessRateDotClass(rate)
            )}
          />
        )
      })}
    </span>
  )
}
