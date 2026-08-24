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
 * 「今日消耗」那一列的取数。
 *
 * ## 一次请求,不是每行一次
 *
 * 后端按当前用户**一次**聚合出今天用过的全部令牌(见
 * `qianye/modules/commission/token_usage_api.go` 的文件头:主库 logs 447 万行,
 * 逐行去查就是一次页面加载往主库上打 20 条 GROUP BY)。所以这里也只有一个
 * query,整张表共用;单元格做的事只有一次 map 查表。
 *
 * ## 零值口径
 *
 *   今天没消费的令牌 → **不在** usage 里 → 单元格渲染 0
 *   合计恰好为 0     → 在 usage 里,值 0 → 同样渲染 0
 *   取数失败/未就绪  → usage 为 undefined → 单元格渲染骨架或 "—"
 *
 * 「不在表里」与「值是 0」在界面上必须长得一样(用户看到的都是"今天没花钱"),
 * 但它们与「没查到」必须长得**不一样** —— 把查询失败画成 0 是在编一个金额。
 */
import { queryOptions } from '@tanstack/react-query'

import { isQyError, qyGet } from '@/features/qy/lib/api'
import { qyKeys } from '@/features/qy/lib/query-keys'

/** 后端 `GET /api/qy/token-usage/today` 的 data 段。 */
type TokenTodayUsagePayload = {
  day_start: number
  day_end: number
  /** 服务器本地时区的缩写(`PDT` / `UTC` / `+08` …)。 */
  timezone: string
  /** 服务器本地时区在 day_start 那一刻相对 UTC 的偏移(分钟)。 */
  utc_offset_minutes: number
  index_ready: boolean
  /** 键是令牌 id 的十进制字符串 —— JSON 对象的键只能是字符串。 */
  usage: Record<string, number>
}

export type TokenTodayUsage = {
  /** 「今天」这一段的半开区间 [dayStart, dayEnd),unix 秒。 */
  dayStart: number
  dayEnd: number
  /** 服务器本地时区的缩写。与 utcOffsetMinutes 一起显示才没有歧义。 */
  timezone: string
  /**
   * 服务器本地时区相对 UTC 的偏移(分钟)。
   *
   * 界面**必须**用它来渲染区间,不能用浏览器本地时区:这一段是服务器那边的
   * 0 点到 23:59:59,浏览器在别的时区时直接 format 会显示成 15:00 之类的钟点,
   * 而那个数在服务器上没有任何意义。
   */
  utcOffsetMinutes: number
  /** 后端那条覆盖索引在不在。只是个诊断位,不参与任何显示判据。 */
  indexReady: boolean
  /** 令牌 id → 今日消费额(额度单位,与列表里的额度列同一单位)。 */
  usage: Record<number, number>
}

/**
 * 把字符串键的 map 转成数字键。
 *
 * 单独提出来是因为它有一条会静默出错的规则:`Number('')` 是 0、
 * `Number('abc')` 是 NaN,而 NaN 作为对象键会变成字符串 `"NaN"` —— 那把不存在的
 * 令牌永远不会被查到,表现是"某一行的今日消耗恒为 0",没有任何报错。
 */
function toNumericUsage(
  raw: Record<string, number> | undefined | null
): Record<number, number> {
  const out: Record<number, number> = {}
  for (const [key, value] of Object.entries(raw ?? {})) {
    const id = Number(key)
    if (!Number.isInteger(id) || id <= 0) continue
    if (typeof value !== 'number' || !Number.isFinite(value)) continue
    out[id] = value
  }
  return out
}

/**
 * 取数定义。
 *
 * ── 扩展未启用时返回 null,而不是抛错 ──
 * 消费方是**上游**的密钥列表。扩展关掉时那一页必须原样可用,所以
 * 404 / feature_off 在这里翻译成 `null`,由列定义据此**整列不渲染** ——
 * 让上游页面为一个它根本不知道存在的扩展弹红色报错是错的方向。
 * 503 / 网络错误保持抛出:那时"今天到底花了多少"是未知的,必须显示成未知。
 *
 * ── staleTime 为什么是 60 秒 ──
 * 这一列是概览,不是账单。后端那条聚合在最重的一个(用户, 日)组合上实测
 * 0.23~0.43 秒,而密钥页在改分组、启停、删除时都会 triggerRefresh ——
 * 把它接到 refreshTrigger 上等于每点一次开关就重跑一次聚合。
 * 60 秒 + 窗口重新聚焦时刷新,是"看得见变化"与"别拿它当心跳"之间的取舍。
 */
export function tokenTodayUsageQuery() {
  return queryOptions({
    queryKey: qyKeys.tokenTodayUsage(),
    queryFn: async (): Promise<TokenTodayUsage | null> => {
      try {
        const data = await qyGet<TokenTodayUsagePayload>('/token-usage/today')
        return {
          dayStart: data.day_start,
          dayEnd: data.day_end,
          timezone: typeof data.timezone === 'string' ? data.timezone : '',
          utcOffsetMinutes: Number.isFinite(data.utc_offset_minutes)
            ? data.utc_offset_minutes
            : 0,
          indexReady: data.index_ready === true,
          usage: toNumericUsage(data.usage),
        }
      } catch (error) {
        if (isQyError(error) && error.isHidden) return null
        throw error
      }
    },
    retry: false,
    staleTime: 60 * 1000,
  })
}

/**
 * 把偏移渲染成 `UTC+08:00` 这样的字符串。
 */
export function formatUtcOffsetLabel(minutes: number): string {
  const sign = minutes < 0 ? '-' : '+'
  const abs = Math.abs(Math.trunc(minutes))
  const hh = String(Math.floor(abs / 60)).padStart(2, '0')
  const mm = String(abs % 60).padStart(2, '0')
  return `UTC${sign}${hh}:${mm}`
}

/**
 * 把时区渲染成写给人看的一句:`PDT (UTC-07:00)`。
 *
 * 缩写单看是有歧义的(`CST` 既是中国标准时间也是美国中部标准时间),所以
 * 永远跟偏移一起显示。而 tzdata 里没有字母缩写的时区拿到的是 `-03`、`+0530`
 * 这样的数字形式 —— 那种情况下再拼一遍偏移就成了 `-03 (UTC-03:00)`,
 * 只留偏移。
 */
export function formatServerZoneLabel(
  timezone: string,
  offsetMinutes: number
): string {
  const offset = formatUtcOffsetLabel(offsetMinutes)
  if (!timezone || /^[+-]/.test(timezone)) return offset
  return `${timezone} (${offset})`
}

/**
 * 按**服务器本地时区**把 unix 秒渲染成 `YYYY-MM-DD HH:mm:ss`。
 *
 * 不走 dayjs 的本地格式化:那个用的是**浏览器**时区。运营在上海打开一台跑在
 * PST 的机器,服务器的「今日 0 点」在他浏览器里是 15:00 —— 显示成 15:00 的话,
 * 这一行文案就把它自己要解释的那件事解释错了。
 *
 * 做法是把偏移先加进时间戳再按 UTC 格式化:`toISOString()` 恒定输出 UTC,
 * 于是加完偏移之后读出来的就是服务器那边的墙上时间。
 */
export function formatInServerZone(
  unixSeconds: number,
  offsetMinutes: number
): string {
  const shifted = new Date((unixSeconds + offsetMinutes * 60) * 1000)
  if (Number.isNaN(shifted.getTime())) return ''
  return shifted.toISOString().slice(0, 19).replace('T', ' ')
}
