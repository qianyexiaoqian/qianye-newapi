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
/**
 * 「今日消耗」列。
 *
 * 项目方原话：「用户key密钥这里显示一个，今日消耗。」
 *
 * ── 显示口径 ──
 *
 * 走 `formatQuota`，与同一张表里的「额度」列**逐字同一个函数**。本仓有过
 * 「同一个弹窗里三行打裸额度、旁边是美元」的教训：同一屏上两种单位，用户会
 * 拿其中一个数去除另一个。
 *
 * ── 三种状态必须长得不一样 ──
 *
 *   还在取     骨架条
 *   取到了     金额（今天没花钱的密钥是 0，不是 "-"）
 *   取不到     「—」+ 说明
 *
 * 把"取不到"画成 0 是在编一个金额；把"今天是 0"画成 "-" 则会让用户以为这一列
 * 没做完。三者分开是这一列唯一会真正骗人的地方。
 *
 * ── 「今日」是哪一段 ──
 *
 * 悬浮里写死区间与日界时区。本站另有一个「今天」（划转/提现的日限额）走服务器
 * 本地时区的午夜，两者可以差好几个小时；不写清楚的话用户没有任何办法把两个数
 * 对上，而两个数都是对的。
 */
import type { Row } from '@tanstack/react-table'
import { useTranslation } from 'react-i18next'

import { Skeleton } from '@/components/ui/skeleton'
import {
  Tooltip,
  TooltipContent,
  TooltipTrigger,
} from '@/components/ui/tooltip'
import dayjs from '@/lib/dayjs'
import { formatQuota } from '@/lib/format'

import { formatDayOffsetLabel } from '../lib/today-usage'
import type { ApiKey } from '../types'
import { useApiKeys } from './api-keys-provider'

/**
 * 今日消耗单元格。
 *
 * 与 `ApiKeyRowActionsCell` / `ApiKeyGroupSwitchCell` 一样**必须是模块级的稳定
 * 组件引用**（理由见 api-keys-columns.tsx 里那段长注释）。这一格没有本地 state，
 * 但它订阅的是 provider 里那个 60 秒 staleTime 的查询：写成内联箭头的话，
 * 表格每 30 秒推进一次 `now` 就会把每一行的订阅卸载重挂一次。
 */
export function ApiKeyTodayUsageCell({ row }: { row: Row<ApiKey> }) {
  const { t } = useTranslation()
  const { todayUsage, todayUsageLoading, todayUsageFailed } = useApiKeys()

  if (todayUsageLoading) {
    return <Skeleton className='h-4 w-14' />
  }

  if (todayUsageFailed || !todayUsage) {
    return (
      <Tooltip>
        <TooltipTrigger
          render={<span className='text-muted-foreground font-mono text-xs' />}
        >
          —
        </TooltipTrigger>
        <TooltipContent>
          <span className='text-xs'>
            {t("Today's usage is unavailable right now")}
          </span>
        </TooltipContent>
      </Tooltip>
    )
  }

  // 缺席 = 今天一次都没用过 = 0。与"合计恰好为 0"在界面上是同一件事。
  const quota = todayUsage.usage[row.original.id] ?? 0
  const zone = formatDayOffsetLabel(todayUsage.dayOffsetMinutes)

  return (
    <Tooltip>
      <TooltipTrigger
        render={<span className='text-sm font-medium tabular-nums' />}
      >
        {formatQuota(quota)}
      </TooltipTrigger>
      <TooltipContent>
        <div className='space-y-1 text-xs'>
          <div>
            {t('Today: {{start}} → {{end}}', {
              start: dayjs.unix(todayUsage.dayStart).format('YYYY-MM-DD HH:mm'),
              end: dayjs.unix(todayUsage.dayEnd).format('YYYY-MM-DD HH:mm'),
            })}
          </div>
          <div className='text-muted-foreground'>
            {t(
              'Day boundary {{zone}} — the same one the daily consumption report uses',
              { zone }
            )}
          </div>
        </div>
      </TooltipContent>
    </Tooltip>
  )
}
