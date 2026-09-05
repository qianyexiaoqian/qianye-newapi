/*
 * 管理端订单处理弹窗在**真实 DOM** 里的形状。
 *
 * 守两件事：
 *   1. 裁决是超级管理员专属 —— role=10 看到的是一句"该找谁"而不是一颗点了吃
 *      403 的按钮；role=100 有按钮、没有那句话。判据反了或"只藏按钮不给出口"，
 *      在 typecheck 与源码 grep 上都看不见，只有挂起来才看得见
 *      （口径同 `__tests__/root-action-gates.test.tsx`）。
 *   2. 发货必填单号：没填时确认键禁用，填了之后发出去的请求体里就是那个单号。
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

const HELD_PLAN: QyMallAdminOrder = {
  order_no: 'ML-HELD-1',
  kind: 'plan',
  title: '一份月卡套餐',
  price: 500,
  status: 'held',
  tracking_no: '',
  ship_note: '',
  fail_reason: '',
  user_subscription_id: 0,
  sub_renewed: false,
  source: 'mall',
  ref_no: '',
  address_missing: false,
  created_at: 1_800_000_000,
  fulfilled_at: 0,
  updated_at: 1_800_000_000,
  user_id: 9,
  username: 'buyer',
  fund_order_no: 'FO-1',
}

const PAID_PHYSICAL: QyMallAdminOrder = {
  ...HELD_PLAN,
  order_no: 'ML-PHYS-1',
  kind: 'physical',
  title: '一件周边实物',
  price: 300,
  status: 'paid',
  fund_order_no: '',
}

function mount(order: QyMallAdminOrder, role: number) {
  return mountQyMallScreen({
    element: (
      <QyMallAdminOrderDialog
        order={order}
        onClose={() => {}}
        onRevealAddress={() => {}}
      />
    ),
    respond: () => ({}),
    role,
    path: '/qy/admin/mall',
  })
}

describe('套餐订单裁决（mall.adjudicate）', () => {
  test('role=100 有「裁决」按钮，没有那句解释', async () => {
    const screen = await mount(HELD_PLAN, ROLE.SUPER_ADMIN)
    assert.ok(
      screen.buttons().includes(zhKeys.qy_mladm_adjudicate),
      `超管应当看到「裁决」：${screen.buttons().join(' | ')}`
    )
    assert.ok(!screen.text().includes(zhKeys.qy_mladm_adjudicate_root_only))
  })

  test('role=10 没有「裁决」，但被告知该找谁', async () => {
    const screen = await mount(HELD_PLAN, ROLE.ADMIN)
    assert.ok(
      !screen.buttons().includes(zhKeys.qy_mladm_adjudicate),
      `普通管理员不该看到「裁决」—— 点了就是 403：${screen.buttons().join(' | ')}`
    )
    assert.ok(
      screen.text().includes(zhKeys.qy_mladm_adjudicate_root_only),
      '按钮消失了却没有任何解释：role=10 会看到一张没有任何出口的单'
    )
  })
})

describe('实物订单发货', () => {
  test('待发货的单有发货 / 标记失败 / 查看地址，没有撤回兑换码；单号必填', async () => {
    const screen = await mount(PAID_PHYSICAL, ROLE.ADMIN)
    const buttons = screen.buttons()
    for (const label of [
      zhKeys.qy_mladm_ship,
      zhKeys.qy_mladm_fail,
      zhKeys.qy_mladm_reveal_address,
    ]) {
      assert.ok(
        buttons.includes(label),
        `缺按钮「${label}」：${buttons.join(' | ')}`
      )
    }
    assert.ok(
      !buttons.includes(zhKeys.qy_mladm_revoke_code),
      '实物单上长出了「撤回兑换码」'
    )

    assert.ok(await screen.click(zhKeys.qy_mladm_ship))
    assert.equal(
      screen.button(zhKeys.qy_mladm_confirm_ship)?.disabled,
      true,
      '没填单号时「确认发货」应当禁用'
    )

    const tracking = screen.field(zhKeys.qy_mladm_tracking_no)
    assert.ok(tracking != null, '发货表单里没有单号输入框')
    await screen.type(tracking, 'SF123456')
    assert.ok(await screen.click(zhKeys.qy_mladm_confirm_ship))

    const ships = screen.sent.filter(
      (row) => row.method === 'POST' && row.url.endsWith('/ship')
    )
    assert.equal(ships.length, 1, `应当恰好发一次发货请求：${ships.length}`)
    assert.ok(
      ships[0].url.includes('/admin/mall/orders/ML-PHYS-1/ship'),
      `发货请求打到了别的单上：${ships[0].url}`
    )
    assert.deepEqual(ships[0].body, { tracking_no: 'SF123456' })
  })
})

describe('实物订单标记完成', () => {
  test('已发货的单有「标记完成」，确认后走同一发货端点并带 done:true；单号可不填', async () => {
    const screen = await mount(
      {
        ...PAID_PHYSICAL,
        order_no: 'ML-PHYS-2',
        status: 'shipped',
        tracking_no: 'SF1',
      },
      ROLE.ADMIN
    )
    const buttons = screen.buttons()
    assert.ok(
      buttons.includes(zhKeys.qy_mladm_complete),
      `已发货的单缺「标记完成」：${buttons.join(' | ')}`
    )
    assert.ok(
      !buttons.includes(zhKeys.qy_mladm_ship),
      '已发货的单不该再长出「发货」'
    )

    assert.ok(await screen.click(zhKeys.qy_mladm_complete))
    assert.equal(
      screen.button(zhKeys.qy_mladm_confirm_complete)?.disabled,
      false,
      '「确认完成」不依赖单号：发货那次已经填过'
    )
    assert.ok(await screen.click(zhKeys.qy_mladm_confirm_complete))

    const ships = screen.sent.filter(
      (row) => row.method === 'POST' && row.url.endsWith('/ship')
    )
    assert.equal(ships.length, 1, `应当恰好发一次请求：${ships.length}`)
    assert.ok(ships[0].url.includes('/admin/mall/orders/ML-PHYS-2/ship'))
    assert.deepEqual(ships[0].body, { tracking_no: '', done: true })
  })
})
