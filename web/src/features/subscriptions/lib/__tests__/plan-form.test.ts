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
 * 一个仍然带着「升级分组 / 降级分组」的存量套餐 —— 站上真实存在的那一批：
 * 它们是在这两个字段还能编辑的年代建的，购买时会
 * `UPDATE users SET group = 'vip'`，到期再改回 'default'。
 */
const legacyPlan: SubscriptionPlan = {
  id: 7,
  title: '存量套餐',
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
  upgrade_group: 'vip',
  downgrade_group: 'default',
}

describe('套餐表单不再携带用户分组改写', () => {
  /**
   * 上游 `AdminUpdateSubscriptionPlan` 是**显式 map 全量覆盖**，
   * `upgrade_group` / `downgrade_group` 恒在 map 里。所以这个断言钉的不是一个
   * 字段名，而是一条业务结论：**在新表单里保存一次，这个套餐从此不再改写
   * users.group**。哪天有人为了"少传点东西"把这两行删掉，落库结果不变
   * （Go 零值同样是空串），但下一个读这段代码的人会以为清空是意外 ——
   * 而这条测试会告诉他那是有意的。
   */
  test('保存存量套餐会清掉 upgrade_group / downgrade_group', () => {
    const payload = formValuesToPlanPayload(planToFormValues(legacyPlan))
    assert.equal(payload.plan.upgrade_group, '')
    assert.equal(payload.plan.downgrade_group, '')
  })

  /**
   * 回填时同样不能把这两个值带进表单：带进来就意味着界面上有一个看不见的字段
   * 正按旧值提交，而管理员以为自己刚刚把它去掉了。
   */
  test('回填不再产生 upgrade_group / downgrade_group 字段', () => {
    const values = planToFormValues(legacyPlan) as Record<string, unknown>
    assert.equal('upgrade_group' in values, false)
    assert.equal('downgrade_group' in values, false)
  })

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
