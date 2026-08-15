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

import {
  REDEMPTION_PRODUCT_TYPE,
  getRedemptionProductType,
} from '@/features/redemption-codes/constants'
import {
  transformFormDataToPayload,
  transformRedemptionToFormDefaults,
} from '@/features/redemption-codes/lib/redemption-form'
import type { Redemption } from '@/features/redemption-codes/types'

// 跑在 bun 的 runner 上，类型仍然复用装在这里的 node:test 定义。
// 直接 `import { test } from 'node:test'` 在整包一起跑的时候会炸成
// "test() inside another test()"（bun 的 node:test 兼容层还没实现），
// 与仓库里既有的那批用例同一个坑 —— 见 components/__tests__ 里的同款写法。
const bunTestModule = 'bun:test'
const { test } = (await import(bunTestModule)) as {
  test: typeof import('node:test').test
}

function redemption(overrides: Partial<Redemption> = {}): Redemption {
  return {
    id: 1,
    user_id: 1,
    name: 'code',
    key: 'k',
    status: 1,
    quota: 500_000,
    created_time: 1,
    redeemed_time: 0,
    expired_time: 0,
    used_user_id: 0,
    ...overrides,
  }
}

/**
 * 存量兑换码没有 product_type（那一列是后加的）。归一化要是不把空值收到
 * 「余额」上，管理端会把库里所有还没兑换的老码显示成一个不认识的类型，
 * 编辑抽屉也会打开成套餐表单——而它们其实全是余额码。
 *
 * 这条规则与后端 `Redemption.ProductKind()` 是同一条，两边必须一起改。
 */
test('兑换码商品类型：缺失、空串、未知值都按余额码处理', () => {
  for (const input of [undefined, '', '   ', 'something-else']) {
    assert.equal(getRedemptionProductType(input), REDEMPTION_PRODUCT_TYPE.QUOTA)
  }
})

test('兑换码商品类型：已知类型原样返回', () => {
  assert.equal(getRedemptionProductType('plan'), REDEMPTION_PRODUCT_TYPE.PLAN)
  assert.equal(
    getRedemptionProductType('usergroup'),
    REDEMPTION_PRODUCT_TYPE.USER_GROUP
  )
})

test('存量码打开编辑抽屉时落在余额表单上', () => {
  const values = transformRedemptionToFormDefaults(
    redemption({ product_type: undefined, product_id: undefined })
  )
  assert.equal(values.product_type, REDEMPTION_PRODUCT_TYPE.QUOTA)
  assert.equal(values.product_id, 0)
})

test('余额码送额度，不送套餐 id', () => {
  const payload = transformFormDataToPayload({
    name: 'gift',
    product_type: REDEMPTION_PRODUCT_TYPE.QUOTA,
    // 切到余额类型之前选过的套餐必须被丢掉，否则后端会收到一张
    // 「类型是余额、却绑着套餐」的自相矛盾的码。
    product_id: 42,
    quota_dollars: 1,
    count: 3,
  })
  assert.equal(payload.product_type, REDEMPTION_PRODUCT_TYPE.QUOTA)
  assert.equal(payload.product_id, 0)
  assert.ok(payload.quota > 0)
  assert.equal(payload.count, 3)
})

/**
 * 套餐码的额度必须送 0。
 *
 * 后端建码时也会强制落 0，但前端跟着送 0 是有意义的：不然一张套餐码会带着
 * 用户在切类型之前随手填过的金额提交，任何一处漏掉那个强制归零，
 * 列表里就会出现一个永远不会发出去、却看着像会发出去的额度。
 */
test('套餐码送套餐 id，额度归零', () => {
  for (const productType of [
    REDEMPTION_PRODUCT_TYPE.PLAN,
    REDEMPTION_PRODUCT_TYPE.USER_GROUP,
  ]) {
    const payload = transformFormDataToPayload({
      name: 'vip',
      product_type: productType,
      product_id: 7,
      quota_dollars: 99,
      count: 1,
    })
    assert.equal(payload.product_type, productType)
    assert.equal(payload.product_id, 7)
    assert.equal(payload.quota, 0)
  }
})
