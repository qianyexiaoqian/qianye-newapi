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
 * 「当前并发 / 近 1 分钟」列。
 *
 * 项目方原话：「api密钥增加一个 sub2 一样显示一下 key 当前并发数（每）分钟。」
 *
 * ── 一格里两个数，靠表头区分 ──
 *
 * 格子里是 `2 / 37`：左边是**此刻**在跑的转发请求数，右边是**近 60 秒**发起过
 * 的次数。两个数字挤在一格里唯一会出的错就是被读反，所以表头写的是
 * 「并发 / 分钟」而不是一个笼统的「实时」，悬浮里再各用一句整话说一遍。
 *
 * ── 三种状态必须长得不一样 ──
 *
 *   还在取   骨架条
 *   取到了   数字（此刻没人用的密钥是 0，不是 "-"）
 *   取不到   「—」+ 说明
 *
 * 与「今日消耗」逐字同一套。把"取不到"画成 0，是在告诉用户"你的 key 现在没人
 * 用"——而这恰好是他打开这一列最想确认的那件事。
 *
 * ── 关掉自动刷新之后 ──
 *
 * 项目方口径：「默认五秒钟刷新一次，用户可以选择不刷新。」关掉之后格子里显示的
 * 是**上一次取到的那一刻**的数，而它和实时的数长得一模一样。所以表头那颗按钮
 * 关掉后变成"继续"图标、旁边多出一颗手动刷新，悬浮里写明取数时刻 ——
 * 三处一起，才让"这个数停住了"这件事在界面上看得见。
 */
import { PauseIcon, PlayIcon, RefreshIcon } from '@hugeicons/core-free-icons'
import { HugeiconsIcon } from '@hugeicons/react'
import type { Row } from '@tanstack/react-table'
import { useTranslation } from 'react-i18next'

import { Button } from '@/components/ui/button'
import { Skeleton } from '@/components/ui/skeleton'
import {
  Tooltip,
  TooltipContent,
  TooltipTrigger,
} from '@/components/ui/tooltip'
import dayjs from '@/lib/dayjs'
import { cn } from '@/lib/utils'

import { TOKEN_LIVE_REFRESH_MS } from '../lib/live-stats'
import type { ApiKey } from '../types'
import { useApiKeys } from './api-keys-provider'

/**
 * 表头：列名 + 自动刷新开关（关掉后再多一颗手动刷新）。
 *
 * 开关放在表头而不是页面顶部的按钮区：它只管这一列，放在通用工具栏里会让人
 * 以为它管的是整张表的刷新。
 */
export function ApiKeyLiveStatsHeader() {
  const { t } = useTranslation()
  const {
    liveAutoRefresh,
    setLiveAutoRefresh,
    refetchLiveStats,
    liveStatsRefetching,
  } = useApiKeys()

  return (
    <div className='flex items-center gap-1'>
      <span>{t('Concurrency / min')}</span>
      <Tooltip>
        <TooltipTrigger
          render={
            <Button
              variant='ghost'
              size='icon'
              className='size-6'
              aria-label={
                liveAutoRefresh
                  ? t('Pause auto-refresh')
                  : t('Resume auto-refresh')
              }
              aria-pressed={!liveAutoRefresh}
              onClick={() => setLiveAutoRefresh(!liveAutoRefresh)}
            />
          }
        >
          <HugeiconsIcon
            icon={liveAutoRefresh ? PauseIcon : PlayIcon}
            strokeWidth={2}
            aria-hidden='true'
          />
        </TooltipTrigger>
        <TooltipContent>
          <span className='text-xs'>
            {liveAutoRefresh
              ? t('Auto-refreshing every {{seconds}}s — click to pause', {
                  seconds: TOKEN_LIVE_REFRESH_MS / 1000,
                })
              : t('Auto-refresh is off — click to resume')}
          </span>
        </TooltipContent>
      </Tooltip>
      {liveAutoRefresh ? null : (
        <Tooltip>
          <TooltipTrigger
            render={
              <Button
                variant='ghost'
                size='icon'
                className='size-6'
                aria-label={t('Refresh now')}
                disabled={liveStatsRefetching}
                onClick={refetchLiveStats}
              />
            }
          >
            <HugeiconsIcon
              icon={RefreshIcon}
              strokeWidth={2}
              aria-hidden='true'
            />
          </TooltipTrigger>
          <TooltipContent>
            <span className='text-xs'>{t('Refresh now')}</span>
          </TooltipContent>
        </Tooltip>
      )}
    </div>
  )
}

/**
 * 「当前并发 / 近 1 分钟」单元格。
 *
 * 与 `ApiKeyTodayUsageCell` 一样**必须是模块级的稳定组件引用**（理由见
 * api-keys-columns.tsx 里那段长注释）：它订阅的是 provider 里那个每 5 秒刷新
 * 一次的查询，写成内联箭头的话，表格每 30 秒推进一次 `now` 就会把每一行的
 * 订阅卸载重挂一次。
 */
export function ApiKeyLiveStatsCell({ row }: { row: Row<ApiKey> }) {
  const { t } = useTranslation()
  const {
    liveStats,
    liveStatsLoading,
    liveStatsFailed,
    liveStatsUpdatedAt,
    liveAutoRefresh,
  } = useApiKeys()

  if (liveStatsLoading) {
    return <Skeleton className='h-4 w-12' />
  }

  if (liveStatsFailed || !liveStats) {
    return (
      <Tooltip>
        <TooltipTrigger
          render={<span className='text-muted-foreground font-mono text-xs' />}
        >
          —
        </TooltipTrigger>
        <TooltipContent>
          <span className='text-xs'>
            {t('Live request stats are unavailable right now')}
          </span>
        </TooltipContent>
      </Tooltip>
    )
  }

  // 缺席 = 此刻没在跑、近 60 秒也没请求 = 0。与"恰好都是 0"在界面上是同一件事。
  const live = liveStats.stats[row.original.id] ?? { inFlight: 0, requests: 0 }

  return (
    <Tooltip>
      <TooltipTrigger
        render={<span className='inline-flex items-baseline gap-1' />}
      >
        <span
          className={cn(
            'text-sm font-medium tabular-nums',
            live.inFlight > 0
              ? 'text-emerald-600 dark:text-emerald-400'
              : 'text-muted-foreground'
          )}
        >
          {live.inFlight}
        </span>
        <span className='text-muted-foreground/60 text-xs'>/</span>
        <span className='text-muted-foreground text-xs tabular-nums'>
          {live.requests}
        </span>
      </TooltipTrigger>
      <TooltipContent>
        <div className='space-y-1 text-xs'>
          <div>{t('In flight now: {{count}}', { count: live.inFlight })}</div>
          <div>
            {t('Requests in the last {{seconds}}s: {{count}}', {
              seconds: liveStats.windowSeconds,
              count: live.requests,
            })}
          </div>
          <div className='text-muted-foreground'>
            {liveAutoRefresh
              ? t('Refreshes every {{seconds}}s', {
                  seconds: TOKEN_LIVE_REFRESH_MS / 1000,
                })
              : t('Auto-refresh off — read at {{time}}', {
                  time: dayjs(liveStatsUpdatedAt).format('HH:mm:ss'),
                })}
          </div>
          <div className='text-muted-foreground'>
            {t('Counted on the node that served the request')}
          </div>
        </div>
      </TooltipContent>
    </Tooltip>
  )
}
