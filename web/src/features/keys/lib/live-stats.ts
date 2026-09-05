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
 * 「当前并发 / 近 1 分钟」那一列的取数。
 *
 * 项目方原话：「api密钥增加一个 sub2 一样显示一下 key 当前并发数（每）分钟。」
 *
 * ## 一次请求，不是每行一次
 *
 * 与「今日消耗」同一个形状：后端按当前用户**一次**返回名下全部活跃令牌
 * （`qianye/controller/token_live_stats.go`），整张表共用一个 query，
 * 单元格只做一次 map 查表。区别在于这一份不落库 —— 它读的是进程内存，
 * 所以才敢每 5 秒问一次。
 *
 * ## 零值口径
 *
 *   此刻没在跑、近 60 秒也没请求 → **不在** stats 里 → 单元格渲染 0
 *   取数失败                      → stats 为 undefined → 单元格渲染 "—"
 *
 * 与「今日消耗」逐字同一套：缺席与 0 在用户眼里是同一件事（"这把 key 现在
 * 没人用"），而它们与"没查到"必须长得**不一样**。
 *
 * ## 这个数只覆盖一个节点
 *
 * 计数是**进程内**的（理由见 `middleware/qy_token_live_export.go` 的文件头：
 * 放进 Redis 之后，节点崩在请求中途会让在途数永久虚高，而一个只用来显示的
 * 数字骗起人来比限流器更彻底）。多节点部署时每个节点只看得见打到自己身上的
 * 那一部分，两次刷新落在不同节点上数字就会跳。这句话必须出现在悬浮提示里 ——
 * 留在代码注释里等于让用户自己去猜。
 */
import { queryOptions } from '@tanstack/react-query'
import { useCallback, useEffect, useState } from 'react'

import { isQyError, qyGet } from '@/features/qy/lib/api'
import { qyKeys } from '@/features/qy/lib/query-keys'

/** 后端 `GET /api/qy/token-usage/live` 的 data 段。 */
type TokenLiveStatsPayload = {
  /** 「近 N 秒」的窗口长度。由后端下发，界面不写死。 */
  window_seconds: number
  /** 键是令牌 id 的十进制字符串 —— JSON 对象的键只能是字符串。 */
  stats: Record<string, { in_flight: number; requests: number }>
}

/** 一把令牌此刻的两个实时数。 */
export type TokenLive = {
  /** 此刻正在跑、还没返回的转发请求数。 */
  inFlight: number
  /** 窗口内（默认近 60 秒）用这把 key 打进来的转发请求数。 */
  requests: number
}

export type TokenLiveStats = {
  windowSeconds: number
  /** 令牌 id → 实时数。缺席即两个数都是 0。 */
  stats: Record<number, TokenLive>
}

/** 自动刷新的默认节奏。项目方口径：「默认五秒钟刷新一次」。 */
export const TOKEN_LIVE_REFRESH_MS = 5000

/**
 * 把字符串键的 map 转成数字键，并丢掉任何不是有限非负整数的值。
 *
 * 与 `today-usage.ts` 里那个转换同一个理由：`Number('abc')` 是 NaN，而 NaN
 * 作为对象键会变成字符串 `"NaN"` —— 那一行永远查不到，表现是"某把 key 的并发
 * 恒为 0"，没有任何报错。
 */
function toNumericStats(
  raw: TokenLiveStatsPayload['stats'] | undefined | null
): Record<number, TokenLive> {
  const out: Record<number, TokenLive> = {}
  for (const [key, value] of Object.entries(raw ?? {})) {
    const id = Number(key)
    if (!Number.isInteger(id) || id <= 0) continue
    const inFlight = Number(value?.in_flight)
    const requests = Number(value?.requests)
    if (!Number.isFinite(inFlight) || !Number.isFinite(requests)) continue
    out[id] = {
      inFlight: Math.max(0, Math.trunc(inFlight)),
      requests: Math.max(0, Math.trunc(requests)),
    }
  }
  return out
}

/**
 * 取数定义。
 *
 * ── 扩展未启用时返回 null，而不是抛错 ──
 * 与「今日消耗」同一档：消费方是**上游**的密钥列表，扩展关掉时那一页必须原样
 * 可用，所以 404 / feature_off 翻译成 `null`，由列定义据此**整列不渲染**。
 *
 * ── 为什么是可关的轮询 ──
 * 项目方口径：「默认五秒钟刷新一次，用户可以选择不刷新。」关掉时
 * `refetchInterval: false`，此时这一列显示的是**上一次取到的那一刻**的数，
 * 而不是"实时数停住了" —— 所以表头那颗开关关掉之后必须看得出来（见
 * `api-key-live-stats-cell.tsx`）。
 *
 * ── staleTime 为什么是 0 ──
 * 这一列的全部意义就是"现在"。任何大于 0 的 staleTime 都会让窗口重新聚焦时
 * 看到的是一个过去的数，而它长得和当前的数一模一样。
 */
export function tokenLiveStatsQuery(autoRefresh: boolean) {
  return queryOptions({
    queryKey: qyKeys.tokenLiveStats(),
    queryFn: async (): Promise<TokenLiveStats | null> => {
      try {
        const data = await qyGet<TokenLiveStatsPayload>('/token-usage/live')
        return {
          windowSeconds:
            Number.isFinite(data.window_seconds) && data.window_seconds > 0
              ? data.window_seconds
              : 60,
          stats: toNumericStats(data.stats),
        }
      } catch (error) {
        if (isQyError(error) && error.isHidden) return null
        throw error
      }
    },
    retry: false,
    staleTime: 0,
    refetchInterval: autoRefresh ? TOKEN_LIVE_REFRESH_MS : false,
  })
}

const AUTO_REFRESH_STORAGE_KEY = 'api_keys_live_stats_auto_refresh'

function readAutoRefresh(): boolean {
  try {
    // 缺省是开：项目方口径是"默认五秒钟刷新一次"，关掉是用户的显式选择。
    return localStorage.getItem(AUTO_REFRESH_STORAGE_KEY) !== 'off'
  } catch {
    return true
  }
}

/**
 * 「自动刷新」这颗开关的状态，持久化在 localStorage 里。
 *
 * 存起来而不是每次进页面都回到默认值：会去关它的人，理由通常是"这台机器上
 * 每 5 秒一次的请求很吵"或者"我要盯着某一行看"，而这两个理由都不会因为
 * 切了一次页面就消失。跨标签页同步走 storage 事件，与 `useTableCompactMode`
 * 同一套写法。
 */
export function useTokenLiveAutoRefresh(): [boolean, (value: boolean) => void] {
  const [autoRefresh, setState] = useState(readAutoRefresh)

  const setAutoRefresh = useCallback((value: boolean) => {
    setState(value)
    try {
      localStorage.setItem(AUTO_REFRESH_STORAGE_KEY, value ? 'on' : 'off')
    } catch {
      /* ignore */
    }
  }, [])

  useEffect(() => {
    const handleStorage = (e: StorageEvent) => {
      if (e.key !== AUTO_REFRESH_STORAGE_KEY) return
      setState(e.newValue !== 'off')
    }
    window.addEventListener('storage', handleStorage)
    return () => window.removeEventListener('storage', handleStorage)
  }, [])

  return [autoRefresh, setAutoRefresh]
}
