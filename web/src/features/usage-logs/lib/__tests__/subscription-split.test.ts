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
import fs from 'node:fs'
import path from 'node:path'
import { describe, test } from 'node:test'
import { fileURLToPath } from 'node:url'

import { splitSubscriptionCharge } from '../format'

/**
 * 订阅账单的分摊。
 *
 * 套餐吃满 amount_total 之后差额由钱包补收。此前 `wallet_quota_deducted` 恒为 0
 * （差额被静默丢弃），账单页把整笔都写成「由订阅抵扣」——那句话现在是假的：
 * 线上实测的那条日志整笔 9693，套餐只吃了 3050，另外 6643 是从余额扣走的。
 */
describe('subscription charge split', () => {
  test('整笔走套餐时不拆，避免给绝大多数日志加噪声', () => {
    assert.deepEqual(splitSubscriptionCharge(9693, 0), {
      fromSubscription: 9693,
      fromWallet: 0,
    })
    assert.deepEqual(splitSubscriptionCharge(9693, undefined), {
      fromSubscription: 9693,
      fromWallet: 0,
    })
  })

  test('撞到套餐上限时按后端记的补收额拆开', () => {
    // 线上实测：套餐 3050 + 钱包 6643 == 账单 9693。
    assert.deepEqual(splitSubscriptionCharge(9693, 6643), {
      fromSubscription: 3050,
      fromWallet: 6643,
    })
    // 另一组：套餐 750 + 钱包 2540 == 3290。
    assert.deepEqual(splitSubscriptionCharge(3290, 2540), {
      fromSubscription: 750,
      fromWallet: 2540,
    })
  })

  test('两段之和恒等于整笔账单', () => {
    for (const [quota, wallet] of [
      [9693, 6643],
      [3290, 2540],
      [16523, 4523],
      [28, 0],
    ] as const) {
      const split = splitSubscriptionCharge(quota, wallet)
      assert.equal(split.fromSubscription + split.fromWallet, quota)
    }
  })

  test('脏数据不能算出一个负的订阅金额', () => {
    // 补收额 >= 整笔账单在正常链路上出不来；真出来了也只能退回不拆，
    // 而不是在账单页上显示「由订阅抵扣 -1」。
    assert.deepEqual(splitSubscriptionCharge(100, 100), {
      fromSubscription: 100,
      fromWallet: 0,
    })
    assert.deepEqual(splitSubscriptionCharge(100, 250), {
      fromSubscription: 100,
      fromWallet: 0,
    })
    assert.deepEqual(splitSubscriptionCharge(100, -5), {
      fromSubscription: 100,
      fromWallet: 0,
    })
    assert.deepEqual(splitSubscriptionCharge(100, Number.NaN), {
      fromSubscription: 100,
      fromWallet: 0,
    })
  })
})

/**
 * 源码级：两处渲染点都必须真的读到这个字段。
 *
 * 上面的行为断言全部作用在纯函数上——把组件里的 `props.other?.wallet_quota_deducted`
 * 换回常量 0，它们**照绿**。而这个缺陷的形状恰恰是「后端字段齐全、前端一个字都没读」
 * （改动前 `grep -rn wallet_quota_deducted web/src/` 命中 0）。
 */
describe('wallet shortfall is actually wired into the UI', () => {
  const root = path.resolve(
    path.dirname(fileURLToPath(import.meta.url)),
    '../..'
  )
  const read = (relative: string) =>
    fs.readFileSync(path.join(root, relative), 'utf8')

  test('金额角标把补收额喂给拆分函数', () => {
    const source = read('components/log-cost-display.tsx')
    assert.match(source, /splitSubscriptionCharge/)
    assert.match(
      source,
      /walletShortfall=\{props\.other\?\.wallet_quota_deducted\}/
    )
  })

  test('详情弹窗单独渲染补收额', () => {
    const source = read('components/dialogs/details-dialog.tsx')
    assert.match(source, /other\.wallet_quota_deducted/)
    assert.match(source, /Charged to balance/)
  })
})
