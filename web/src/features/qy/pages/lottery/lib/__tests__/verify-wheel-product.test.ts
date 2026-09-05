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
import { readFileSync } from 'node:fs'
import { describe, test } from 'node:test'

import { qyLotTiers } from '../../types'
import {
  verifyQyLotProof,
  type QyLotVerifyStep,
  type QyLotWheelProof,
} from '../verify'

/**
 * 带商品奖（`prize_type='product'`）的转盘证据链。
 *
 * `fixtures/wheel-product-proof.json` 由 Go 的端到端用例
 * （`prize_product_db_test.go`，`QY_WHEEL_PRODUCT_PROOF_OUT`）从真实端点导出：
 * 三档商品奖（兑换码 / 实物 / 套餐）各中一转。同一份文件 Python 脚本验过
 * 「全部通过」，这里要求浏览器端得出同一结论 —— 尤其是 spec 原像的第 11 位
 * （`product_no`）三份实现逐字节一致。
 *
 * 篡改：把一档商品奖换成另一件商品，`spec_hash` 必须红 —— 那正是把商品号
 * 放进承诺的全部理由（发布后换一件更便宜的商品与改一个金额是同一件事）。
 */

function loadFixture(): QyLotWheelProof {
  const raw = readFileSync(
    new URL('./fixtures/wheel-product-proof.json', import.meta.url),
    'utf8'
  )
  return JSON.parse(raw) as QyLotWheelProof
}

function statusOf(steps: QyLotVerifyStep[], key: string): string {
  const found = steps.find((item) => item.key === key)
  assert.ok(found, `missing step ${key}`)
  return found.status
}

describe('wheel 商品奖 fixture', () => {
  test('三档商品奖各中一转，六步全绿', async () => {
    const proof = loadFixture()
    const productTiers = qyLotTiers(proof.spec).filter(
      (tier) => tier.prize_type === 'product'
    )
    assert.equal(productTiers.length, 3)
    assert.ok(productTiers.every((tier) => (tier.product_no ?? '') !== ''))
    assert.deepEqual(
      (proof.tiers ?? []).map((tier) => tier.prize_type),
      ['product', 'product', 'product', 'quota', 'none']
    )
    assert.deepEqual(
      (proof.spins ?? []).map((spin) => spin.tier),
      [1, 2, 3]
    )
    assert.ok(
      proof.winners.every(
        (winner) => winner.prize_type === 'product' && winner.amount === 0
      )
    )
    const steps = await verifyQyLotProof(proof)
    assert.deepEqual(
      steps.map((item) => [item.key, item.status]),
      [
        ['rules', 'ok'],
        ['spec', 'ok'],
        ['commit', 'ok'],
        ['chain', 'ok'],
        ['roster', 'ok'],
        ['result', 'ok'],
      ]
    )
  })

  test('发布后把一档商品奖换成另一件商品 → spec_hash 变红', async () => {
    const proof = loadFixture()
    const tier = proof.spec.find((item) => item.prize_type === 'product')
    assert.ok(tier)
    tier.product_no = 'PD-CHEAPER'
    const steps = await verifyQyLotProof(proof)
    assert.equal(statusOf(steps, 'spec'), 'fail')
  })

  test('把商品奖改成 0 星屑的星屑奖（保留商品号）→ spec_hash 同样变红', async () => {
    const proof = loadFixture()
    const tier = proof.spec.find((item) => item.prize_type === 'product')
    assert.ok(tier)
    tier.prize_type = 'quota'
    const steps = await verifyQyLotProof(proof)
    assert.equal(statusOf(steps, 'spec'), 'fail')
  })
})
