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
import { queryOptions } from '@tanstack/react-query'

import { isQyError, qyGet } from './api'
import { qyKeys } from './query-keys'
import type { QyConfig, QyConfigPayload } from './types'

/**
 * 引导端点 `GET /api/qy/config` 的取数定义与本地快照。
 *
 * 这是整个扩展在前端的开关总闸：菜单、路由守卫、钱包入口卡、日志扩展列全部
 * 依赖它。因此它必须满足三条：
 *   1. **绝不因为扩展未启用而报错** —— 404 被翻译成"全关"的配置，不是异常；
 *   2. **冷启动零闪烁** —— 用 localStorage 快照做 placeholderData，
 *      刷新页面时菜单不会先消失再出现；
 *   3. **非 hook 环境可读** —— 嵌套侧边栏视图的 `getNavGroups(t)` 不是 hook，
 *      只能通过 {@link getQyConfigSnapshot} 同步拿值。
 */

const SNAPSHOT_STORAGE_KEY = 'qy_config'

/** 全关配置。扩展未启用、或本地无快照时的安全默认值。 */
export const QY_DISABLED_CONFIG: QyConfig = {
  enabled: false,
  available: false,
  features: {
    transfer: false,
    commission: false,
    withdraw: false,
    availability: false,
    violation: false,
    lottery: false,
    ticket: false,
    group_matrix: false,
    pay_password: false,
  },
  wallet: {
    show_transfer_entry: false,
    show_commission_entry: false,
    show_withdraw_entry: false,
  },
  log_metrics: {
    show_reasoning_effort: false,
    show_cache_ratio: false,
    enable_filter: false,
  },
  withdraw_options: {
    methods: [],
    fiat_currency: '',
    remark_max_runes: 0,
  },
  transfer_options: {
    min_quota: 0,
    max_per_tx_quota: 0,
    recipient_lookup: 'id',
  },
  lottery: {
    show_entry: false,
    proof_public: false,
    // 扩展整体关掉时不该有任何娱乐入口。这里的四个 false 与
    // normalizeQyConfig 里"缺键按显示"并不矛盾：那一条讲的是后端在场却没下发
    // 这一段，这一条讲的是后端明确说了 enabled:false。
    plays: {
      draw_rank: false,
      draw_prob: false,
      draw_ball: false,
      guess: false,
    },
  },
}

function bool(value: unknown, fallback = false): boolean {
  return typeof value === 'boolean' ? value : fallback
}

function num(value: unknown, fallback = 0): number {
  return typeof value === 'number' && Number.isFinite(value) ? value : fallback
}

function str(value: unknown, fallback = ''): string {
  return typeof value === 'string' ? value : fallback
}

/**
 * 把可能残缺的响应补齐成完整配置。
 *
 * 后端在 `enabled=false` 时只返回两个字段，如果不补齐，下游每处都得写
 * `config.features?.transfer ?? false`，漏一处就是一个运行时崩溃。
 */
export function normalizeQyConfig(
  raw: QyConfigPayload | null | undefined
): QyConfig {
  if (raw == null || typeof raw !== 'object') return QY_DISABLED_CONFIG
  const features = raw.features ?? {}
  const wallet = raw.wallet ?? {}
  const logMetrics = raw.log_metrics ?? {}
  const withdraw = raw.withdraw_options ?? {}
  const transfer = raw.transfer_options ?? {}
  const lottery = raw.lottery ?? {}
  const plays = lottery.plays ?? {}
  const enabled = bool(raw.enabled)

  return {
    enabled,
    // available 只有在 enabled 时才有意义；扩展关掉时不应该让页面显示"稍后重试"。
    available: enabled && bool(raw.available),
    features: {
      transfer: bool(features.transfer),
      commission: bool(features.commission),
      withdraw: bool(features.withdraw),
      availability: bool(features.availability),
      violation: bool(features.violation),
      lottery: bool(features.lottery),
      ticket: bool(features.ticket),
      group_matrix: bool(features.group_matrix),
      pay_password: bool(features.pay_password),
    },
    wallet: {
      show_transfer_entry: bool(wallet.show_transfer_entry),
      show_commission_entry: bool(wallet.show_commission_entry),
      show_withdraw_entry: bool(wallet.show_withdraw_entry),
    },
    log_metrics: {
      show_reasoning_effort: bool(logMetrics.show_reasoning_effort),
      show_cache_ratio: bool(logMetrics.show_cache_ratio),
      enable_filter: bool(logMetrics.enable_filter),
    },
    withdraw_options: {
      methods: Array.isArray(withdraw.methods)
        ? withdraw.methods.filter(
            (m): m is 'fiat' | 'quota' => m === 'quota' || m === 'fiat'
          )
        : [],
      fiat_currency: str(withdraw.fiat_currency),
      remark_max_runes: num(withdraw.remark_max_runes),
    },
    transfer_options: {
      min_quota: num(transfer.min_quota),
      max_per_tx_quota: num(transfer.max_per_tx_quota),
      recipient_lookup: str(transfer.recipient_lookup, 'id'),
    },
    lottery: {
      // 后端不下发时按"不显示"处理：多显示一个点进去就 404 的入口，
      // 比少显示一个入口更糟 —— 前者是断链，后者只是这一期没开。
      show_entry: bool(lottery.show_entry),
      proof_public: bool(lottery.proof_public),
      // 玩法开关的缺省方向与 show_entry **相反**，理由见 types.ts 的
      // QyLotPlays：缺省隐藏会让一个从没动过配置的站点在升级后整块娱乐功能
      // 静默消失，而缺省显示最多是多一格入口（列表由后端过滤，不会变成断链）。
      //
      // 但"缺省显示"只在扩展开着时成立：后端回 `enabled:false` 时整个响应就只有
      // 两个字段，此时把四种玩法标成"显示"会得到一份自相矛盾的快照
      // （`features.lottery` 是 false，玩法却全开）。所以缺省值跟着 `enabled` 走。
      plays: {
        draw_rank: bool(plays.draw_rank, enabled),
        draw_prob: bool(plays.draw_prob, enabled),
        draw_ball: bool(plays.draw_ball, enabled),
        guess: bool(plays.guess, enabled),
      },
    },
  }
}

// ───────────────────────── 本地快照 ─────────────────────────

function readSnapshotFromStorage(): QyConfig | null {
  try {
    if (typeof window === 'undefined') return null
    const saved = window.localStorage.getItem(SNAPSHOT_STORAGE_KEY)
    if (saved == null) return null
    return normalizeQyConfig(JSON.parse(saved) as QyConfigPayload)
  } catch {
    return null
  }
}

// 模块级快照：非 hook 环境（嵌套侧边栏视图的 getNavGroups）唯一的读取通道。
let snapshot: QyConfig = readSnapshotFromStorage() ?? QY_DISABLED_CONFIG

function writeSnapshot(config: QyConfig): void {
  snapshot = config
  try {
    if (typeof window === 'undefined') return
    window.localStorage.setItem(SNAPSHOT_STORAGE_KEY, JSON.stringify(config))
  } catch {
    /* 隐私模式下 localStorage 会抛，退化成"每次冷启动闪一下"，不影响功能 */
  }
}

/**
 * 同步读取最近一次已知的扩展配置。
 *
 * **只允许在非 hook 环境使用**（如 `SidebarView.getNavGroups`）。React 组件
 * 一律用 `useQyConfig()`，否则拿不到响应式更新。
 */
export function getQyConfigSnapshot(): QyConfig {
  return snapshot
}

// ───────────────────────── 取数定义 ─────────────────────────

export function qyConfigQueryOptions() {
  return queryOptions({
    queryKey: qyKeys.config(),
    queryFn: async (): Promise<QyConfig> => {
      try {
        const config = normalizeQyConfig(
          await qyGet<QyConfigPayload>('/config')
        )
        writeSnapshot(config)
        return config
      } catch (error) {
        // 扩展未启用不是错误，是一种正常状态：返回"全关"配置，让上层静默隐藏。
        // 抛出去会让 react-query 进 error 态，页面就会显示红色报错。
        if (isQyError(error) && error.isHidden) {
          writeSnapshot(QY_DISABLED_CONFIG)
          return QY_DISABLED_CONFIG
        }
        // 503 / 网络错误保持 error 态：此时"扩展到底开没开"是未知的，
        // 上层会沿用本地快照，而不是错误地把菜单抹掉。
        throw error
      }
    },
    placeholderData: readSnapshotFromStorage() ?? undefined,
    // 未启用时后端返回 404，重试 3 次纯粹是浪费请求。
    retry: false,
    staleTime: 5 * 60 * 1000,
    gcTime: 30 * 60 * 1000,
    refetchOnWindowFocus: false,
  })
}
