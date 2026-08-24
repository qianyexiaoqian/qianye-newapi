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
 * 项目方口径：「以服务器的时间为准，即 0 点到 23 点 59 分 59 秒是今日的消耗。」
 * 所以悬浮里必须写两件事，而且两件都不能省：
 *
 *   ① 实际区间 —— 按**服务器本地时区**渲染，不是浏览器时区。运营在上海打开
 *      一台跑在 PST 的机器，服务器的「今日 0 点」在他浏览器里是 15:00；
 *      照浏览器渲染的话，这行文案会把它自己要解释的那件事解释错。
 *   ② 当前实际在用的时区 —— 容器里 TZ 常常没设、tzdata 常常没装，进程认的
 *      「本地」就是 UTC，而运营以为是他自己所在地。不写出来就没人会发现。
 *
 * 本页的「今日」与**日消费明细**的「今日」不是同一段:后者走返佣的固定日界
 * (commission.day_offset_minutes),演示机上两者差 7 小时。所以这里刻意不再
 * 声称两处同源 —— 那句话在改成服务器本地自然日之后已经是假的。
 */
import type { Row } from '@tanstack/react-table'
import { useTranslation } from 'react-i18next'

import { Skeleton } from '@/components/ui/skeleton'
import {
  Tooltip,
  TooltipContent,
  TooltipTrigger,
} from '@/components/ui/tooltip'
import { formatQuota } from '@/lib/format'

import { formatInServerZone, formatServerZoneLabel } from '../lib/today-usage'
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
  const offset = todayUsage.utcOffsetMinutes
  const zone = formatServerZoneLabel(todayUsage.timezone, offset)

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
              start: formatInServerZone(todayUsage.dayStart, offset),
              // 右端点是半开区间的右端(次日 0 点),减一秒才是当天最后一秒。
              // 直接写死 23:59:59 会在夏令时跳变日说谎:本地 23:00 直接跳到
              // 次日 00:00 的那一天,最后一秒是 22:59:59。
              end: formatInServerZone(todayUsage.dayEnd - 1, offset),
            })}
          </div>
          <div className='text-muted-foreground'>
            {t(
              'Server local time {{zone}} — the daily consumption report may use a different day boundary',
              { zone }
            )}
          </div>
        </div>
      </TooltipContent>
    </Tooltip>
  )
}
