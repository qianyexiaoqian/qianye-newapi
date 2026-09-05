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
/*
 * 「我的参与」里中了商品奖的那一行：结果列写「商城商品」，按钮直接落到那张
 * 商城订单上，而不是渲染成 `+0 星屑`。
 *
 * `won.kind='product'` 的 amount 恒为 0，走额度那条分支在类型上完全合法，
 * 用户读到的是"中了个空气"。
 */
import assert from 'node:assert/strict'
import { after, describe, test } from 'node:test'

import type { QyLotMyEntry } from '../../lottery/types'
import {
  cleanupQyWheelScreens,
  mountQyWheelScreen,
  zhKeys,
} from '../../wheel/__tests__/wheel-harness'

const { QyLotteryRecordsBody } = await import('../index')

after(async () => {
  await cleanupQyWheelScreens()
})

const PRODUCT_ROW: QyLotMyEntry = {
  entry_no: 'LE-product-1',
  act_no: 'LW-P',
  title: '星屑转盘·商品奖',
  kind: 'draw',
  seq: 5,
  chain_hash: 'd'.repeat(64),
  user_ref: 'u-1',
  amount: 100,
  status: 'success',
  opt_no: 0,
  draw_mode: 'wheel',
  won: {
    kind: 'product',
    tier: 2,
    amount: 0,
    status: 'granted',
    payout_no: 'PO-9',
    prize_type: 'product',
    product_no: 'P-mug',
    mall_order_no: 'ML-PRIZE-1',
  },
  created_at: 1_800_000_000,
}

describe('我的参与里的商品奖', () => {
  test('结果列写「商城商品」+「去查看订单」指向商城，不渲染 +0 星屑', async () => {
    const screen = await mountQyWheelScreen({
      element: <QyLotteryRecordsBody />,
      respond: (req) =>
        req.url.includes('/lottery/my-entries')
          ? { items: [PRODUCT_ROW], total: 1, p: 1, page_size: 20 }
          : undefined,
      path: '/qy/lottery/LW-P/',
    })
    const text = screen.text()
    assert.ok(
      text.includes(zhKeys.qy_lot_payout_kind_product),
      `结果列没写「商城商品」：${text}`
    )
    assert.ok(
      !text.includes(`+0 ${zhKeys.qy_sd_unit_default}`),
      '商品奖渲染成了 +0 星屑'
    )
    const view = screen.button(zhKeys.qy_lot_product_view_order_btn)
    assert.ok(view != null, '没有「去查看订单」')
    assert.ok(
      (view.getAttribute('href') ?? '').includes('/qy/mall'),
      `「去查看订单」应指向商城：${view.getAttribute('href')}`
    )
  })
})
