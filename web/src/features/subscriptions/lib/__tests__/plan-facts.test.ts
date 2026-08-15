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

import type { TFunction } from 'i18next'

import { buildPlanFacts } from '@/features/subscriptions/lib/plan-facts'
import type { SubscriptionPlan } from '@/features/subscriptions/types'

/**
 * 事实清单里的「解锁模型分组 / 余额使用范围」两行。
 *
 * 钉的是一条业务结论而不是渲染细节：**买家看到的「解锁」永远只有三种可能 ——
 * 真值、读取失败、以及不渲染**。第四种（把"没读到"渲染成"不解锁任何分组"）
 * 是这次要修的缺陷本身，也是唯一一种会让用户按一个错误的理由掏钱的写法。
 */

/** 只回显键名，断言才能盯住"用了哪个键"而不是某种语言的译文。 */
const t = ((key: string) => key) as unknown as TFunction

const plan: Partial<SubscriptionPlan> = {
  id: 7,
  title: '浅夜套餐',
  price_amount: 10,
  currency: 'USD',
  duration_unit: 'month',
  duration_value: 1,
  quota_reset_period: 'never',
  total_amount: 0,
  max_purchase_per_user: 0,
}

function factValue(
  facts: { id: string; value: string }[],
  id: string
): string | undefined {
  return facts.find((fact) => fact.id === id)?.value
}

describe('套餐事实清单里的解锁披露', () => {
  test('不传 entitlement 时两行都不渲染', () => {
    const facts = buildPlanFacts(plan, t)
    assert.equal(factValue(facts, 'unlock_groups'), undefined)
    assert.equal(factValue(facts, 'balance_scope'), undefined)
  })

  /**
   * 最要紧的一条：读取失败**不得**塌成"不解锁任何分组"。
   *
   * 两者在界面上都是一行灰字，差别只在文案；而它们对买家的含义正好相反 ——
   * 一个是"这个套餐没有这项权益"，一个是"这项权益此刻查不到"。塌掉的那一版
   * 会让人以为自己买的是一个不解锁任何分组的套餐，而它可能恰恰是唯一卖点。
   */
  test('读取失败渲染成"没读到"，绝不是"不解锁任何分组"', () => {
    const facts = buildPlanFacts(plan, t, { entitlement: { state: 'error' } })
    assert.equal(factValue(facts, 'unlock_groups'), 'qy_ent_failed')
    assert.equal(factValue(facts, 'balance_scope'), 'qy_ent_failed')
    assert.notEqual(factValue(facts, 'unlock_groups'), 'qy_ent_unlock_none')
  })

  test('加载中同样是独立的一档', () => {
    const facts = buildPlanFacts(plan, t, { entitlement: { state: 'loading' } })
    assert.equal(factValue(facts, 'unlock_groups'), 'qy_ent_loading')
    assert.equal(factValue(facts, 'balance_scope'), 'qy_ent_loading')
  })

  test('读到了才把空清单说成"不解锁额外分组"', () => {
    const facts = buildPlanFacts(plan, t, {
      entitlement: {
        state: 'ready',
        unlockGroups: [],
        balanceScope: 'universal',
        scopeEnforced: true,
      },
    })
    assert.equal(factValue(facts, 'unlock_groups'), 'qy_ent_unlock_none')
    assert.equal(factValue(facts, 'balance_scope'), 'qy_ent_scope_universal')
  })

  test('解锁清单原样列出，不做截断也不折成计数', () => {
    const facts = buildPlanFacts(plan, t, {
      entitlement: {
        state: 'ready',
        unlockGroups: ['浅夜の梦专属号池', 'pro'],
        balanceScope: 'restricted',
        scopeEnforced: true,
      },
    })
    assert.equal(
      factValue(facts, 'unlock_groups'),
      '浅夜の梦专属号池, pro',
      '买家要判断的是"我想用的那个分组在不在里面"，一个数字回答不了'
    )
    assert.equal(factValue(facts, 'balance_scope'), 'qy_ent_scope_restricted')
  })

  /**
   * 「仅限」还没接进扣费路径时说成"仅限"，就是让买家按一个不存在的限制去规划
   * 用量：他会以为这笔钱花不到别处，实际上会被任意分组花光。两句必须分开。
   */
  test('「仅限」未生效时不得说成"仅限"', () => {
    const facts = buildPlanFacts(plan, t, {
      entitlement: {
        state: 'ready',
        unlockGroups: ['pro'],
        balanceScope: 'restricted',
        scopeEnforced: false,
      },
    })
    assert.equal(
      factValue(facts, 'balance_scope'),
      'qy_ent_scope_restricted_off'
    )
  })
})

describe('纯商品与永久档的事实文案', () => {
  /**
   * 站上最贵的一句误导：纯商品落库的 `total_amount` 是 0，而 0 走的是
   * 「不限额度」那一支。不特判的话，一个一分余额都不带、只卖用户分组的套餐
   * 会在买家眼前显示成「无限额度」—— 与它真正的语义正好相反。
   */
  test('纯商品的额度绝不显示成「不限」', () => {
    const facts = buildPlanFacts({ ...plan, no_quota: true, total_amount: 0 }, t)
    assert.equal(factValue(facts, 'quota'), 'qy_plan_quota_pure_product')
    assert.notEqual(factValue(facts, 'quota'), 'Unlimited')
  })

  test('total_amount 为 0 而非纯商品时仍然是「不限」', () => {
    const facts = buildPlanFacts(
      { ...plan, no_quota: false, total_amount: 0 },
      t
    )
    assert.equal(
      factValue(facts, 'quota'),
      'Unlimited',
      '这是上游既有的"不限额度"口径，不能被纯商品那一支顺手改掉'
    )
  })

  /**
   * 永久档在后端 `end_time` 存 0，没有到期日可算。留着这一行只会显示一个
   * 破折号或一个由 dayjs 猜出来的假日期，而"有效期：永久有效"那一行已经把
   * 事实说全了。
   */
  test('永久档不渲染「预计到期」，有效期写成永久', () => {
    const facts = buildPlanFacts(
      { ...plan, duration_unit: 'permanent', duration_value: 0 },
      t
    )
    assert.equal(factValue(facts, 'expiry'), undefined)
    assert.equal(factValue(facts, 'duration'), 'qy_plan_duration_permanent')
  })

  test('常规档仍然渲染「预计到期」', () => {
    const facts = buildPlanFacts(plan, t)
    assert.notEqual(factValue(facts, 'expiry'), undefined)
  })
})
