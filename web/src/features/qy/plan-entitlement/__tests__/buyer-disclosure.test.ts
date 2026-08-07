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
import assert from 'node:assert/strict'
import { describe, test } from 'node:test'

import type { QyMyEntitlements, QyUserSubscriptionEntitlement } from '../types'
import {
  buildQyEntitlementIndex,
  qyPlanDisclosure,
  type QyEntitlementResult,
  type QyEntitlementState,
} from '../use-my-entitlements'

/**
 * 买家侧披露的取数判据。
 *
 * 这里钉的不是映射代码，而是**「不知道」与「知道是空的」必须是两种东西**。
 * 一旦它们塌成同一种，钱包页会在接口挂掉时、以及在用户还没买这个套餐时，
 * 都言之凿凿地写上"不解锁额外的模型分组" —— 而那正好是套餐的卖点所在。
 */

function subscription(
  over: Partial<QyUserSubscriptionEntitlement> &
    Pick<QyUserSubscriptionEntitlement, 'plan_id' | 'user_subscription_id'>
): QyUserSubscriptionEntitlement {
  return {
    plan_title: '',
    amount_total: 0,
    amount_used: 0,
    unlimited: true,
    remaining: 0,
    pending_reset: false,
    next_reset_time: 0,
    start_time: 0,
    end_time: 0,
    allow_wallet_overflow: true,
    balance_scope: 'universal',
    bound_groups: [],
    usable_here: true,
    will_charge_first: false,
    ...over,
  }
}

const payload: QyMyEntitlements = {
  model_group: '',
  // `plans` 与是否持有无关，覆盖全站配置过权益的套餐 —— 这是「买之前」那一半的
  // 数据源。21 号套餐**没人买过**，它只出现在这里。
  plans: [
    {
      plan_id: 7,
      balance_scope: 'restricted',
      bound_groups: ['浅夜の梦专属号池'],
    },
    { plan_id: 9, balance_scope: 'universal', bound_groups: ['pro'] },
    { plan_id: 21, balance_scope: 'restricted', bound_groups: ['vip'] },
  ],
  subscriptions: [
    subscription({
      user_subscription_id: 11,
      plan_id: 7,
      bound_groups: ['浅夜の梦专属号池'],
      balance_scope: 'restricted',
      will_charge_first: true,
    }),
    subscription({
      user_subscription_id: 12,
      plan_id: 9,
      bound_groups: ['pro'],
    }),
  ],
  unlocked_groups: ['pro', '浅夜の梦专属号池'],
  any_restricted: true,
  balance_scope_enforced: true,
  wallet_fallback_blocked: false,
}

function resultOf(
  state: QyEntitlementState,
  data: QyMyEntitlements | null
): QyEntitlementResult {
  return {
    ...buildQyEntitlementIndex(data),
    state,
    reload: () => {},
  }
}

describe('买家侧套餐权益索引', () => {
  test('按 plan_id 与 user_subscription_id 双向寻址', () => {
    const index = buildQyEntitlementIndex(payload)
    assert.deepEqual(index.byPlan.get(7), {
      unlockGroups: ['浅夜の梦专属号池'],
      balanceScope: 'restricted',
    })
    assert.equal(index.bySubscription.get(11)?.will_charge_first, true)
    assert.deepEqual(index.unlockedGroups, ['pro', '浅夜の梦专属号池'])
  })

  /**
   * `qyGet` 是纯类型断言，字段缺失时拿到的是 `undefined`，而 `undefined.length`
   * 会在渲染期抛异常 —— 一条纯展示的披露信息不该有能力把整张钱包页白屏掉。
   */
  test('字段缺失不抛异常，退化成空集合', () => {
    const index = buildQyEntitlementIndex({
      model_group: '',
    } as unknown as QyMyEntitlements)
    assert.deepEqual(index.subscriptions, [])
    assert.deepEqual(index.unlockedGroups, [])
    assert.equal(index.byPlan.size, 0)
  })
})

describe('某个套餐该披露什么', () => {
  test('读到了、且用户持有该套餐 → 真值', () => {
    const disclosure = qyPlanDisclosure(resultOf('ready', payload), 7)
    assert.deepEqual(disclosure, {
      state: 'ready',
      unlockGroups: ['浅夜の梦专属号池'],
      balanceScope: 'restricted',
      scopeEnforced: true,
    })
  })

  /**
   * 接口挂了要说"没读到"，而不是让整两行消失 —— 消失与"这个套餐本来就没有
   * 这项权益"在界面上完全一样，买家没有任何办法分辨。
   */
  test('接口失败 → error，且不塌成"没有解锁"', () => {
    const disclosure = qyPlanDisclosure(resultOf('error', null), 7)
    assert.deepEqual(disclosure, { state: 'error' })
  })

  test('扩展未启用 → 一行都不渲染', () => {
    assert.equal(qyPlanDisclosure(resultOf('hidden', null), 7), undefined)
  })

  /**
   * 本轮修的核心缺陷：**买之前**必须说得出口。
   *
   * 「这笔余额只能花在 vip 上」是付款**前**必须知道、付款后才发现等于误导的一条。
   * 数据源只有已持有订阅的话，第一次买的人在钱包页套餐卡与购买确认弹窗里一个字
   * 都看不到，只有续费/复购才有真值 —— 那正好是这条信息最没有价值的时刻。
   */
  test('用户还没买过，但套餐配了权益 → 付款前照样说得出口', () => {
    const disclosure = qyPlanDisclosure(resultOf('ready', payload), 21)
    assert.deepEqual(disclosure, {
      state: 'ready',
      unlockGroups: ['vip'],
      balanceScope: 'restricted',
      scopeEnforced: true,
    })
  })

  /**
   * 没配过权益的套餐不在 `plans` 里。"查不到"绝不等于"它不解锁任何分组"：
   * 后者是在替后端编造事实。
   */
  test('套餐没配过权益 → 不知道，不渲染', () => {
    assert.equal(qyPlanDisclosure(resultOf('ready', payload), 404), undefined)
  })

  /**
   * `planId` 的判定必须排在 `error` 之前：没有套餐就没有可披露的对象，
   * 此时报"没读到"是在为一个不存在的问题制造红字。
   */
  test('没有 planId 时即使接口失败也不渲染', () => {
    assert.equal(
      qyPlanDisclosure(resultOf('error', null), undefined),
      undefined
    )
    assert.equal(qyPlanDisclosure(resultOf('error', null), 0), undefined)
  })

  /**
   * 已持有的订阅与 `plans` 读的是后端同一份快照，两边必然相同；这里钉住
   * "同一个套餐在买前买后是同一句话"，而不是两处实现各说各的。
   */
  test('已持有的套餐，买前买后同一句话', () => {
    const held = qyPlanDisclosure(resultOf('ready', payload), 7)
    const listedOnly = buildQyEntitlementIndex(payload).byPlan.get(7)
    assert.deepEqual(listedOnly, {
      unlockGroups: ['浅夜の梦专属号池'],
      balanceScope: 'restricted',
    })
    assert.equal(
      held?.state === 'ready' ? held.balanceScope : null,
      'restricted'
    )
  })

  test('加载中不先渲染再缩回去', () => {
    assert.equal(qyPlanDisclosure(resultOf('loading', null), 7), undefined)
  })

  /**
   * scopeEnforced 取自响应的全局字段而不是订阅行：「仅限」有没有接进扣费路径
   * 是**站点级**的事实。取错了会让所有 restricted 套餐在未接线时都被说成
   * "只能用于 X"，而钱照样花在别处。
   */
  test('「仅限」未生效时如实带出 scopeEnforced=false', () => {
    const result = resultOf('ready', {
      ...payload,
      balance_scope_enforced: false,
    })
    const disclosure = qyPlanDisclosure(result, 7)
    assert.equal(
      disclosure?.state === 'ready' ? disclosure.scopeEnforced : null,
      false
    )
  })
})
