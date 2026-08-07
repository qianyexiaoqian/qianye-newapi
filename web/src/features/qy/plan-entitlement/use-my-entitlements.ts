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
import { useQuery, useQueryClient } from '@tanstack/react-query'
import { useCallback, useMemo } from 'react'

import type { PlanEntitlementDisclosure } from '@/features/subscriptions/lib'

import { isQyError } from '../lib/api'
import { qyKeys } from '../lib/query-keys'
import { qyGetMyEntitlements } from './api'
import type {
  QyMyEntitlements,
  QyPlanBalanceScope,
  QyUserSubscriptionEntitlement,
} from './types'

/**
 * 买家侧的套餐权益披露。
 *
 * ── 这个 hook 存在的理由 ──
 *
 * 管理端撤掉「升级分组 / 降级分组」之后，一个套餐的卖点变成了「买了能多用哪几个
 * 模型分组」，而它在**买家能看到的每一屏上都不存在**：钱包页套餐卡、购买确认
 * 弹窗、以及买完之后的已购列表，全都只有价格 / 时长 / 额度 / 限购。
 * 「这笔余额只能花在 X 分组」更糟 —— 那是付款前必须知道、付款后才发现等于误导
 * 的一条。
 *
 * ── 它能覆盖到哪里，覆盖不到哪里 ──
 *
 * 用户端只有 `GET /api/qy/subscription/entitlements` 这一条接口，它下发两段：
 *
 *   · `subscriptions` —— 当前用户**已持有**的活跃订阅，喂已购视图（我的订阅）；
 *   · `plans` —— 全站**配置过权益**的套餐各一条，与是否持有无关，喂钱包页
 *     套餐卡与购买确认弹窗。没有这一段的话，「这笔余额只能花在 X」就只有
 *     付款之后才说得出口，而那正是它最没有价值的时刻。
 *
 * 没配过权益的套餐不在 `plans` 里，这里返回 `undefined`，调用方一行都不渲染。
 * 这个"不知道"必须与"读到了、是空的"分得清清楚楚：把前者渲染成「不解锁任何
 * 分组」等于替后端编造事实，而那正是这次要修的缺陷本身。
 */
export type QyEntitlementState = 'error' | 'hidden' | 'loading' | 'ready'

/** 一个套餐在买家侧的两条事实。 */
export type QyPlanGrantFacts = {
  unlockGroups: string[]
  balanceScope: QyPlanBalanceScope
}

/**
 * 响应的索引形式。
 *
 * 后端按扣费顺序下发一个数组，而界面要按 `plan_id`（套餐卡）和
 * `user_subscription_id`（已购列表的每一行）两种方式寻址。索引在这里建一次，
 * 不在渲染里做 `find` —— 那会让每张卡片对全量订阅做一次线性扫描，也让"同一个
 * 套餐的两条订阅"这种情况在不同调用点得到不同答案。
 */
export type QyEntitlementIndex = {
  /** 全部活跃订阅解锁的模型分组并集。 */
  unlockedGroups: string[]
  anyRestricted: boolean
  balanceScopeEnforced: boolean
  walletFallbackBlocked: boolean
  /** 已按扣费顺序排好，原样保留后端的顺序。 */
  subscriptions: QyUserSubscriptionEntitlement[]
  byPlan: ReadonlyMap<number, QyPlanGrantFacts>
  bySubscription: ReadonlyMap<number, QyUserSubscriptionEntitlement>
}

export type QyEntitlementResult = QyEntitlementIndex & {
  state: QyEntitlementState
  reload: () => void
}

const EMPTY_INDEX: QyEntitlementIndex = {
  unlockedGroups: [],
  anyRestricted: false,
  balanceScopeEnforced: false,
  walletFallbackBlocked: false,
  subscriptions: [],
  byPlan: new Map<number, QyPlanGrantFacts>(),
  bySubscription: new Map<number, QyUserSubscriptionEntitlement>(),
}

/**
 * 把响应折成索引。
 *
 * 每一处 `?? []` 都不是防御性洁癖：`qyGet` 是纯类型断言，字段缺失时拿到的是
 * `undefined`，而 `undefined.length` 会在渲染期抛异常，把整张钱包页白屏掉 ——
 * 一条纯展示的披露信息不该有能力做到这件事。
 *
 * 同一个套餐可能有多条活跃订阅（续费）。`bound_groups` 与 `balance_scope` 都是
 * **套餐级**快照（后端 `snap.BoundGroups(sub.PlanId)`），所以后写覆盖先写是
 * 幂等的，不需要合并。
 */
export function buildQyEntitlementIndex(
  data: QyMyEntitlements | null | undefined
): QyEntitlementIndex {
  if (data == null) return EMPTY_INDEX
  const subscriptions = data.subscriptions ?? []
  const byPlan = new Map<number, QyPlanGrantFacts>()
  const bySubscription = new Map<number, QyUserSubscriptionEntitlement>()
  // 先铺**买前**披露（全站配置过权益的每个套餐），再让已持有的订阅覆盖上去。
  // 两者读的是后端同一份快照，值必然相同；顺序只决定"没持有时也有话可说"。
  for (const plan of data.plans ?? []) {
    byPlan.set(plan.plan_id, {
      unlockGroups: plan.bound_groups ?? [],
      balanceScope: plan.balance_scope,
    })
  }
  for (const sub of subscriptions) {
    byPlan.set(sub.plan_id, {
      unlockGroups: sub.bound_groups ?? [],
      balanceScope: sub.balance_scope,
    })
    bySubscription.set(sub.user_subscription_id, sub)
  }
  return {
    unlockedGroups: data.unlocked_groups ?? [],
    anyRestricted: !!data.any_restricted,
    balanceScopeEnforced: !!data.balance_scope_enforced,
    walletFallbackBlocked: !!data.wallet_fallback_blocked,
    subscriptions,
    byPlan,
    bySubscription,
  }
}

/**
 * 某个套餐要在事实清单里披露什么。
 *
 * 返回 `undefined` 表示**不知道**（扩展未启用，或这个套餐根本没配过权益）——
 * 调用方据此整两行都不渲染。`error` 会照常返回，因为"知道有这回事但没读到"
 * 与"不知道有这回事"对买家是两句不同的话。
 *
 * `planId` 的判定排在 `error` **之前**：没有套餐就没有可披露的对象，此时报
 * "没读到"是在为一个不存在的问题制造红字。反过来，一旦有 planId，`error` 就
 * 必须照常返回 —— 后端的 `plans` 覆盖全站配置过权益的套餐，所以任何一张卡都
 * 可能本该有话说，而这次没读到。
 */
export function qyPlanDisclosure(
  result: QyEntitlementResult,
  planId: number | undefined
): PlanEntitlementDisclosure | undefined {
  if (result.state === 'hidden') return undefined
  if (planId == null || planId <= 0) return undefined
  if (result.state === 'error') return { state: 'error' }
  const grant = result.byPlan.get(planId)
  if (grant == null) {
    // 已读到但这个套餐不在其中 = 后台没给它配过权益，它的行为与上游逐位一致，
    // 这条接口对它无话可说。
    // loading 期间同样只能沉默：此刻渲染"正在读取"，等 ready 之后又要整行
    // 消失（因为没配过），列表会先长后缩。
    return undefined
  }
  return {
    state: 'ready',
    unlockGroups: grant.unlockGroups,
    balanceScope: grant.balanceScope,
    scopeEnforced: result.balanceScopeEnforced,
  }
}

/**
 * 拉一次「我的套餐权益」。
 *
 * `retry: false`：404（扩展未启用 / 功能开关关闭）是这条接口的**正常返回**，
 * 重试三次只是让入口晚三次才隐藏掉。
 */
export function useQyMyEntitlements(): QyEntitlementResult {
  const queryClient = useQueryClient()
  const query = useQuery({
    queryKey: qyKeys.myEntitlements(),
    queryFn: () => qyGetMyEntitlements(),
    retry: false,
    staleTime: 30_000,
  })

  const index = useMemo(() => buildQyEntitlementIndex(query.data), [query.data])

  const reload = useCallback(() => {
    void queryClient.invalidateQueries({ queryKey: qyKeys.myEntitlements() })
  }, [queryClient])

  let state: QyEntitlementState = 'loading'
  if (query.error != null) {
    state = isQyError(query.error) && query.error.isHidden ? 'hidden' : 'error'
  } else if (query.data != null) {
    state = 'ready'
  }

  return { ...index, state, reload }
}
