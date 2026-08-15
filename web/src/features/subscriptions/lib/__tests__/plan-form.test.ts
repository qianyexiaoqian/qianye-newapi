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

import {
  PLAN_FORM_DEFAULTS,
  formValuesToPlanPayload,
  planToFormValues,
} from '@/features/subscriptions/lib/plan-form'
import type { SubscriptionPlan } from '@/features/subscriptions/types'

/**
 * 一个「用户组商品」形态的套餐：买了把人改成 vip，到期改回 default。
 * 购买时由 `CreateUserSubscriptionFromPlanTx` 改 `users.group`，到期由
 * `ExpireDueSubscriptions` 改回去。
 */
const groupProductPlan: SubscriptionPlan = {
  id: 7,
  title: '会员套餐',
  price_amount: 10,
  currency: 'USD',
  duration_unit: 'month',
  duration_value: 1,
  quota_reset_period: 'never',
  enabled: true,
  sort_order: 0,
  allow_balance_pay: true,
  allow_wallet_overflow: true,
  max_purchase_per_user: 0,
  total_amount: 0,
  no_quota: false,
  upgrade_group: 'vip',
  downgrade_group: 'default',
}

describe('套餐表单里的用户分组改写', () => {
  /**
   * 上游 `AdminUpdateSubscriptionPlan` 是**显式 map 全量覆盖**，
   * `upgrade_group` / `downgrade_group` 恒在 map 里 —— 表单不传就等于清空。
   * 所以这条钉的是一句业务结论：**只是改个价格、保存一次，不该让这个套餐悄悄
   * 不再改用户分组**。曾经有一版恒提交空串，任何一次编辑都会把配好的分组抹掉，
   * 而界面上没有任何一处显示过它被抹掉了。
   */
  test('回填后原样保存不会动已配的升降级分组', () => {
    const payload = formValuesToPlanPayload(planToFormValues(groupProductPlan))
    assert.equal(payload.plan.upgrade_group, 'vip')
    assert.equal(payload.plan.downgrade_group, 'default')
  })

  /**
   * 空串是**取值**而不是"没填"：upgrade 空 = 购买不动用户分组，downgrade 空 =
   * 到期回退到购买前的原分组。所以它必须能被提交出去，运营才撤得掉一个已配的
   * 分组；把空串当成"缺省"跳过不传，界面上那次"改回不改"就永远保存不上。
   */
  test('清空后能把空串提交出去（撤掉已配的分组）', () => {
    const values = planToFormValues(groupProductPlan)
    const payload = formValuesToPlanPayload({
      ...values,
      upgrade_group: '',
      downgrade_group: '',
    })
    assert.equal(payload.plan.upgrade_group, '')
    assert.equal(payload.plan.downgrade_group, '')
  })
})

describe('纯商品与永久档的落库形态', () => {
  /**
   * 纯商品与"不限额度"必须是两个不同的东西：后端预扣那句
   * `if sub.AmountTotal > 0` 让 0 直接跳过余额检查，也就是说拿
   * `total_amount = 0` 表达"没有余额"，卖出去的是一份**无限订阅余额**。
   * 所以 payload 里 `no_quota` 必须真的带出去，而不是靠额度填 0 来暗示。
   */
  test('纯商品带出 no_quota，并把总额度归零', () => {
    const payload = formValuesToPlanPayload({
      ...PLAN_FORM_DEFAULTS,
      title: '会员身份',
      no_quota: true,
      total_amount: 25,
    })
    assert.equal(payload.plan.no_quota, true)
    assert.equal(
      payload.plan.total_amount,
      0,
      '界面上已经不显示这一格了，把上一次的旧额度继续提交只会留下一个看不见、' +
        '却会在关掉纯商品那一刻突然复活的数字'
    )
  })

  test('非纯商品照常按填写的额度换算', () => {
    const payload = formValuesToPlanPayload({
      ...PLAN_FORM_DEFAULTS,
      title: '普通套餐',
      no_quota: false,
      total_amount: 1,
    })
    assert.equal(payload.plan.no_quota, false)
    assert.ok(
      (payload.plan.total_amount ?? 0) > 0,
      '带余额的套餐不能被归零 —— 那会把它变成"不限额度"'
    )
  })

  /**
   * 永久档的 duration_value 必须落 0。后端的兜底
   * （`duration_value <= 0` 补 1）**特意跳过**永久档，所以 0 会原样留在库里；
   * 传 1 的话，列表页会把一个永远不到期的套餐显示成「1 个月」——
   * 一个看起来完全正常的假到期日。
   */
  test('永久档的时长数值落 0', () => {
    const payload = formValuesToPlanPayload({
      ...PLAN_FORM_DEFAULTS,
      title: '永久会员',
      duration_unit: 'permanent',
      duration_value: 1,
    })
    assert.equal(payload.plan.duration_unit, 'permanent')
    assert.equal(payload.plan.duration_value, 0)
  })

  test('常规档的时长数值原样带出', () => {
    const payload = formValuesToPlanPayload({
      ...PLAN_FORM_DEFAULTS,
      title: '季付',
      duration_unit: 'month',
      duration_value: 3,
    })
    assert.equal(payload.plan.duration_value, 3)
  })
})

describe('套餐表单的字段边界', () => {
  /**
   * 总名额落**扩展库**，上游 `AdminUpsertSubscriptionPlanRequest` 绑到
   * `model.SubscriptionPlan`，多出来的键会被 Go 静默丢弃 —— 表单显示"保存成功"
   * 而名额压根没落库。它必须在这里被摘掉，由 setPlanSeatLimit 单独写。
   */
  test('总名额不进套餐本体的 payload', () => {
    const payload = formValuesToPlanPayload({
      ...PLAN_FORM_DEFAULTS,
      title: '新套餐',
      max_total_users: 100,
    })
    assert.equal('max_total_users' in payload.plan, false)
  })
})
