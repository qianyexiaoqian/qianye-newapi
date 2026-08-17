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
import type { TFunction } from 'i18next'

import dayjs from '@/lib/dayjs'

import type { SubscriptionPlan } from '../types'

/**
 * 套餐相对**发售时间窗**的三态。
 *
 * 与 `enabled`（手动上下架）是两个正交维度，不要合并成一个枚举：一个被手动
 * 下架、同时又已经过了停售时间的套餐，两种事实都真，而运营要做的事不同
 * （前者打开开关就能卖，后者还得把停售时间往后推）。合并成一个值必然要丢掉
 * 其中一半。合成展示由 {@link planSaleBadge} 负责，它显式规定了优先级。
 */
export type PlanSaleState = 'upcoming' | 'on_sale' | 'ended'

/** 发售/停售两列上「不限制」的取值。与后端 `PlanSaleWindowUnlimited` 同源。 */
export const PLAN_SALE_WINDOW_UNLIMITED = 0

/**
 * 算出套餐此刻处在时间窗的哪一档。
 *
 * ## 判据必须与后端逐字一致
 *
 * 后端 `model.PlanSaleWindowError` 是：
 *
 * ```
 * start != 0 && now <  start  →  未开售
 * end   != 0 && now >= end    →  已停售
 * ```
 *
 * 左闭右开 `[start, end)`：开售那一秒就能买，停售那一秒已经买不了。两边写不
 * 一致的后果是"界面上还写着在售、点下去被接口拒了"——用户会认定是系统坏了，
 * 而两侧各自看都言之凿凿。
 *
 * ## 0 不是 1970
 *
 * `end !== PLAN_SALE_WINDOW_UNLIMITED` 这个前置判断是必需的：`now >= 0` 恒真，
 * 少了它，全站每一个没配停售时间的套餐（也就是升级后的全部存量套餐）会在
 * 这一屏上集体显示成"已停售"，购买按钮全部变灰。
 *
 * @param nowMs 当前时刻（毫秒）。显式传入而不是内部取 `Date.now()`，是为了让
 *   倒计时组件在同一次渲染里用同一个时刻算状态和剩余秒数——分两次取会在
 *   开售那一瞬出现"状态说已开售、倒计时还剩 -1 秒"。
 */
export function planSaleState(
  plan: Pick<SubscriptionPlan, 'sale_start_at' | 'sale_end_at'> | null,
  nowMs: number = Date.now()
): PlanSaleState {
  const now = Math.floor(nowMs / 1000)
  const start = Number(plan?.sale_start_at || 0)
  const end = Number(plan?.sale_end_at || 0)
  if (start !== PLAN_SALE_WINDOW_UNLIMITED && now < start) return 'upcoming'
  if (end !== PLAN_SALE_WINDOW_UNLIMITED && now >= end) return 'ended'
  return 'on_sale'
}

/**
 * 「这个套餐现在能不能买」。
 *
 * 时间窗与 `enabled` 是**与**的关系：任何一个说"不"就是不。或的关系说不通——
 * 那意味着一个被手动下架的套餐会在开售时间到达时自己重新上架。
 *
 * 这条判据是购买按钮唯一的禁用理由来源（限购次数是另一条，两者独立）。
 */
export function isPlanPurchasable(
  plan: Pick<SubscriptionPlan, 'enabled' | 'sale_start_at' | 'sale_end_at'>,
  nowMs: number = Date.now()
): boolean {
  if (plan.enabled === false) return false
  return planSaleState(plan, nowMs) === 'on_sale'
}

/** 管理端列表那一格要显示的徽章。 */
export interface PlanSaleBadge {
  /** i18n 键，字面量传给 `t()`。 */
  labelKey: string
  /** 取值必须落在 `StatusBadge` 的 StatusVariant 里 —— `danger` 而不是 CSS 的 `destructive`。 */
  variant: 'success' | 'neutral' | 'warning' | 'danger'
}

/**
 * 管理端「状态」列的合成显示。
 *
 * 优先级：**手动下架 > 时间窗**。理由是这两者要做的下一步不同，而列表只有
 * 一格：一个 enabled=false 的套餐，无论时间窗说什么，运营都得先打开那个开关，
 * 所以先告诉他这一条。时间窗的细节（具体几点开售/停售）由同一格的 title
 * 提示补上，见 `subscriptions-columns.tsx`。
 *
 * 反过来（时间窗优先）会让一个已下架的套餐在列表上显示成"在售"，
 * 那是这一列能犯的最严重的错。
 */
export function planSaleBadge(
  plan: Pick<SubscriptionPlan, 'enabled' | 'sale_start_at' | 'sale_end_at'>,
  nowMs: number = Date.now()
): PlanSaleBadge {
  if (plan.enabled === false) {
    return { labelKey: 'qy_plan_sale_state_disabled', variant: 'neutral' }
  }
  const state = planSaleState(plan, nowMs)
  if (state === 'upcoming') {
    return { labelKey: 'qy_plan_sale_state_upcoming', variant: 'warning' }
  }
  if (state === 'ended') {
    return { labelKey: 'qy_plan_sale_state_ended', variant: 'danger' }
  }
  return { labelKey: 'qy_plan_sale_state_on_sale', variant: 'success' }
}

/**
 * 时间戳 → 可读时间。0 返回空串（调用方据此整行不渲染）。
 *
 * 不复用 `format.ts` 的 `formatTimestamp`：那个对 0 返回 `'-'`，而在这里
 * 「不限制」渲染成一个破折号会被读成"没读到"。两者要的是不同的空值表达。
 */
export function formatSaleTime(ts: number | undefined): string {
  const value = Number(ts || 0)
  if (value === PLAN_SALE_WINDOW_UNLIMITED) return ''
  const d = dayjs(value * 1000)
  // 越界时间戳（毫秒粘进秒字段）算出来是 Invalid Date。后端有上界校验，
  // 但存量脏数据绕得过去，而 `format()` 对无效日期会吐出 "Invalid Date"
  // 四个字直接糊在界面上。
  return d.isValid() ? d.format('YYYY-MM-DD HH:mm') : ''
}

/**
 * 距离开售还有多久，给未开售的套餐做倒计时用。
 *
 * 返回 `null` 表示"没有倒计时可言"（已经开售、或压根没配开售时间）——
 * 调用方据此退回到静态的"敬请期待"。返回 0 是有意义的一档：正好到点。
 */
export function secondsUntilSaleStart(
  plan: Pick<SubscriptionPlan, 'sale_start_at'> | null,
  nowMs: number = Date.now()
): number | null {
  const start = Number(plan?.sale_start_at || 0)
  if (start === PLAN_SALE_WINDOW_UNLIMITED) return null
  const remain = start - Math.floor(nowMs / 1000)
  return remain > 0 ? remain : null
}

/**
 * 倒计时文案。`天 时 分 秒` 逐级降,只显示前两个非零单位。
 *
 * 只显示两级是刻意的:`3 天 4 小时` 与 `3 天 4 小时 12 分 5 秒` 传达的决策
 * 信息完全一样,而后者每秒都在跳,反而看不出还剩多久。剩不到一小时时自然
 * 降到 `12 分 5 秒`,那时秒才真的有用。
 */
export function formatSaleCountdown(seconds: number, t: TFunction): string {
  const total = Math.max(0, Math.floor(seconds))
  const units: [number, string][] = [
    [Math.floor(total / 86400), t('days')],
    [Math.floor((total % 86400) / 3600), t('hours')],
    [Math.floor((total % 3600) / 60), t('minutes')],
    [total % 60, t('seconds')],
  ]
  const firstNonZero = units.findIndex(([value]) => value > 0)
  // 全零 = 不足一秒。显示 `0 秒` 而不是空串:空串会让整行消失,
  // 而这一刻恰恰是最该看到"马上就开了"的时候。
  if (firstNonZero === -1) return `0 ${t('seconds')}`
  return units
    .slice(firstNonZero, firstNonZero + 2)
    .filter(([value]) => value > 0)
    .map(([value, label]) => `${value} ${label}`)
    .join(' ')
}
