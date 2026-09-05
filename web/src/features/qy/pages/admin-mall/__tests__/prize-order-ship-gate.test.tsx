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
 * 管理端处理弹窗上的实物奖品单：中奖者还没填地址时「发货」「查看地址」禁用，
 * 并且旁边写着为什么。
 *
 * 后端对它是 409 `qy_ml_address_missing`；这里守的是"别让运营白点一次、也别让
 * 他以为这张单坏了"。按钮消失与按钮禁用+一句解释是两回事：前者让 role=10 看到
 * 一张没有任何出口的单。
 */
import assert from 'node:assert/strict'
import { after, describe, test } from 'node:test'

import { ROLE } from '@/lib/roles'

import {
  cleanupQyMallScreens,
  mountQyMallScreen,
  zhKeys,
} from '../../mall/__tests__/mall-harness'
import type { QyMallAdminOrder } from '../types'

const { QyMallAdminOrderDialog } =
  await import('../components/order-detail-dialog')

after(async () => {
  await cleanupQyMallScreens()
})

const PRIZE_PHYSICAL: QyMallAdminOrder = {
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
  user_id: 9,
  username: 'winner',
  fund_order_no: '',
}

describe('实物奖品单的发货闸门', () => {
  test('没填地址：发货与查看地址禁用、有解释；标记失败仍可用', async () => {
    const screen = await mountQyMallScreen({
      element: (
        <QyMallAdminOrderDialog
          order={PRIZE_PHYSICAL}
          onClose={() => {}}
          onRevealAddress={() => {}}
        />
      ),
      respond: () => ({}),
      role: ROLE.ADMIN,
      path: '/qy/admin/mall',
    })
    const text = screen.text()
    assert.ok(
      text.includes(zhKeys.qy_ml_source_lottery),
      '弹窗上没标「抽奖所得」'
    )
    assert.ok(text.includes('PO-9'), '弹窗上没有出款号')
    assert.ok(text.includes(zhKeys.qy_ml_price_prize), '价格没写成「奖品」')
    assert.equal(
      screen.button(zhKeys.qy_mladm_ship)?.disabled,
      true,
      '没填地址时「发货」应当禁用'
    )
    assert.equal(
      screen.button(zhKeys.qy_mladm_reveal_address)?.disabled,
      true,
      '没填地址时「查看地址」应当禁用（没有地址可看）'
    )
    assert.equal(
      screen.button(zhKeys.qy_mladm_fail)?.disabled,
      false,
      '「标记失败」不受地址约束'
    )
    assert.ok(
      text.includes(zhKeys.qy_mladm_ship_needs_address),
      '禁用的按钮旁边必须说明为什么'
    )
  })

  test('填过地址：发货照常可用，解释消失', async () => {
    const screen = await mountQyMallScreen({
      element: (
        <QyMallAdminOrderDialog
          order={{ ...PRIZE_PHYSICAL, address_missing: false }}
          onClose={() => {}}
          onRevealAddress={() => {}}
        />
      ),
      respond: () => ({}),
      role: ROLE.ADMIN,
      path: '/qy/admin/mall',
    })
    assert.equal(screen.button(zhKeys.qy_mladm_ship)?.disabled, false)
    assert.ok(!screen.text().includes(zhKeys.qy_mladm_ship_needs_address))
  })
})
