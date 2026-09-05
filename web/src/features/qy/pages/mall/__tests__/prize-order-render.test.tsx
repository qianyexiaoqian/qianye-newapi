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
 * 「我的订单」上的抽奖所得的单（`source='lottery'`）：徽标、「奖品」而不是
 * `0 星屑`、「待填地址」；详情里的地址表单发到这张单上、带 address / contact。
 *
 * 奖品单与兑换的单共用一张表、一套状态字面量，类型层分不出它们。渲染成
 * "0 星屑 · 待发货"在类型上完全合法，而用户读到的是"免费商品、等着就行"——
 * 其实卡住这张单的是他自己还没填地址。
 */
import assert from 'node:assert/strict'
import { after, describe, test } from 'node:test'

import type { QyMallOrder, QyMallOrderDetail } from '../types'
import { cleanupQyMallScreens, mountQyMallScreen, zhKeys } from './mall-harness'

const { QyMallOrdersBody } = await import('../orders')
const { QyMallOrderDetailDialog } =
  await import('../components/mall-order-detail-dialog')

after(async () => {
  await cleanupQyMallScreens()
})

const PRIZE_PHYSICAL: QyMallOrder = {
  order_no: 'ML-PRIZE-1',
  kind: 'physical',
  title: '限量马克杯',
  price: 0,
  status: 'paid',
  tracking_no: '',
  ship_note: '',
  fail_reason: '',
  user_subscription_id: 0,
  sub_renewed: false,
  source: 'lottery',
  ref_no: 'PO-9',
  address_missing: true,
  created_at: 1_800_000_000,
  fulfilled_at: 0,
  updated_at: 1_800_000_000,
}

const BOUGHT_PHYSICAL: QyMallOrder = {
  ...PRIZE_PHYSICAL,
  order_no: 'ML-BUY-1',
  title: '一件周边实物',
  price: 300,
  source: 'mall',
  ref_no: '',
  address_missing: false,
}

describe('抽奖所得的订单', () => {
  test('列表：奖品单挂「抽奖所得」、价格写「奖品」、状态写「待填地址」；兑换的单照旧', async () => {
    const screen = await mountQyMallScreen({
      element: <QyMallOrdersBody />,
      respond: (req) => {
        if (req.url.endsWith('/mall/orders')) {
          return {
            items: [PRIZE_PHYSICAL, BOUGHT_PHYSICAL],
            total: 2,
            page: 1,
            page_size: 20,
          }
        }
        return undefined
      },
    })
    const text = screen.text()
    assert.equal(
      text.split(zhKeys.qy_ml_source_lottery).length - 1,
      // 筛选下拉里也有一次「抽奖所得」，所以是 1（筛选）+ 1（奖品单那一行）。
      2,
      `「抽奖所得」应恰好挂在奖品单那一行：${text}`
    )
    assert.ok(
      text.includes(zhKeys.qy_ml_price_prize),
      '奖品单的价格没写成「奖品」'
    )
    // 「300 星屑」（兑换的那一单）里也含 "0 星屑" 这个子串，按"前面不是数字的 0"判。
    assert.doesNotMatch(
      text,
      new RegExp(`(?<!\\d)0 ${zhKeys.qy_sd_unit_default}`),
      '奖品单渲染成了 0 星屑：读起来像免费商品'
    )
    assert.ok(
      text.includes(`300 ${zhKeys.qy_sd_unit_default}`),
      '兑换的单仍要显示它的兑换价'
    )
    assert.ok(
      text.includes(zhKeys.qy_ml_st_address_missing),
      '没填地址的实物奖品单要写「待填地址」，不是「待发货」'
    )
    assert.ok(
      text.includes(zhKeys.qy_ml_st_paid_physical),
      '兑换的实物单照旧是「待发货」'
    )
    assert.ok(
      screen.buttons().includes(zhKeys.qy_ml_prize_address_btn),
      '待填地址的单要在行上直接给「填写地址」'
    )
  })

  test('详情：地址表单发到这张单的 /address，请求体只有 address 与 contact', async () => {
    const detail: QyMallOrderDetail = { ...PRIZE_PHYSICAL, events: [] }
    const screen = await mountQyMallScreen({
      element: (
        <QyMallOrderDetailDialog
          orderNo='ML-PRIZE-1'
          onClose={() => {}}
          onRevealCode={() => {}}
        />
      ),
      respond: (req) => {
        if (
          req.method === 'GET' &&
          req.url.endsWith('/mall/orders/ML-PRIZE-1')
        ) {
          return detail
        }
        if (req.method === 'POST' && req.url.endsWith('/address')) {
          return { ...PRIZE_PHYSICAL, address_missing: false }
        }
        return undefined
      },
    })
    const text = screen.text()
    assert.ok(text.includes('PO-9'), '奖品单详情要印出出款号')
    assert.ok(
      !screen.buttons().includes(zhKeys.qy_ml_cancel_btn),
      '奖品单一分钱没扣，不该有「取消并退回」'
    )
    assert.ok(
      text.includes(zhKeys.qy_ml_tl_next_address),
      '时间线的下一步要说"等你填地址"'
    )

    const address = screen.field(zhKeys.qy_ml_address)
    const contact = screen.field(zhKeys.qy_ml_contact)
    assert.ok(address != null && contact != null, '详情里没有地址表单')
    await screen.type(address, '某省某市某路 1 号')
    await screen.type(contact, '13800000000')
    assert.ok(await screen.click(zhKeys.qy_ml_prize_address_submit))

    const posts = screen.sent.filter((row) => row.method === 'POST')
    assert.equal(posts.length, 1, `应当恰好提交一次：${posts.length}`)
    assert.ok(posts[0].url.endsWith('/mall/orders/ML-PRIZE-1/address'))
    assert.deepEqual(posts[0].body, {
      address: '某省某市某路 1 号',
      contact: '13800000000',
    })
  })
})
