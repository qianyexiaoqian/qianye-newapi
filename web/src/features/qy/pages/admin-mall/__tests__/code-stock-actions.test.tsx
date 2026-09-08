/*
 * 码库存这一屏在**真实 DOM** 里的形状：列表 + 提卡 + 删码。
 *
 * 守三件事，每一件都只有挂起来才看得见：
 *
 *   1. **列表上没有码**。这一屏的接口不回明文，列上也不该有任何"点一下展开"的
 *      形态。断言的是渲染结果里找不到码 —— 将来谁把 `code` 字段加回视图并顺手
 *      渲染出来，这条会红。
 *   2. **提卡带着支付密码走请求头**。密码在 `X-Qy-Pay-Password` 里而不是 body，
 *      漏掉这个头就是一次 403，而 typecheck 与 grep 都看不见；明文在验密成功
 *      之前不能出现在屏幕上。
 *   3. **两个动作只对 unused 开放**。已发出 / 已提取的行是发放去向的证据，
 *      按钮出现在那些行上就等于把"抹掉发放记录"做成一次误触。
 */
import assert from 'node:assert/strict'
import { after, describe, test } from 'node:test'

import { ROLE } from '@/lib/roles'

import {
  act,
  cleanupQyMallScreens,
  mountQyMallScreen,
  zhKeys,
} from '../../mall/__tests__/mall-harness'
import type { QyMallAdminCode } from '../types'

const { QyMallAdminCodesTab } = await import('../codes-tab')

after(async () => {
  await cleanupQyMallScreens()
})

const PRODUCT = {
  product_no: 'P-CODE-1',
  kind: 'code',
  title: '一张第三方卡密',
  description: '',
  cover_url: '',
  price: 1200,
  stock: 0,
  sold: 3,
  per_user_limit: 0,
  sale_start_at: 0,
  sale_end_at: 0,
  plan_id: 0,
  plan: null,
  available: true,
  my_count: 0,
  preview: null,
  enabled: true,
  sort_order: 0,
  code_stock: { unused: 1, issued: 1, revoked: 0, taken: 1 },
  created_at: 1_800_000_000,
  updated_at: 1_800_000_000,
}

const UNUSED: QyMallAdminCode = {
  id: 11,
  status: 'unused',
  order_no: '',
  created_at: 1_800_000_000,
  issued_at: 0,
  taken_at: 0,
  taken_by: 0,
  taken_name: '',
}

const ISSUED: QyMallAdminCode = {
  id: 12,
  status: 'issued',
  order_no: 'ML-ISSUED-1',
  created_at: 1_800_000_000,
  issued_at: 1_800_000_100,
  taken_at: 0,
  taken_by: 0,
  taken_name: '',
}

const TAKEN: QyMallAdminCode = {
  id: 13,
  status: 'taken',
  order_no: '',
  created_at: 1_800_000_000,
  issued_at: 0,
  taken_at: 1_800_000_200,
  taken_by: 42,
  taken_name: 'root',
}

const PAY_PASSWORD_STATUS = {
  is_set: true,
  locked: false,
  locked_until: 0,
  fail_count: 0,
  remaining_attempts: 5,
  max_attempts: 5,
  lock_minutes: 30,
  set_at: 1,
  changed_at: 1,
}

/** 挂一屏并把商品选择器切到那件兑换码商品（列表在选中之前不请求）。 */
async function mountCodesTab(options: { onTake?: () => unknown } = {}) {
  const screen = await mountQyMallScreen({
    element: <QyMallAdminCodesTab />,
    role: ROLE.SUPER_ADMIN,
    path: '/qy/admin/mall',
    respond: (req) => {
      if (req.url.includes('/pay-password')) return PAY_PASSWORD_STATUS
      if (req.url.endsWith('/admin/mall/products')) {
        return { items: [PRODUCT], total: 1, page: 1, page_size: 100 }
      }
      if (req.method === 'GET' && req.url.endsWith('/codes')) {
        return {
          items: [TAKEN, ISSUED, UNUSED],
          total: 3,
          page: 1,
          page_size: 20,
        }
      }
      if (req.method === 'POST' && req.url.endsWith('/take')) {
        return options.onTake?.()
      }
      return undefined
    },
  })

  const select = document.body.querySelector<HTMLSelectElement>(
    `select[aria-label="${zhKeys.qy_mladm_codes_product}"]`
  )
  assert.ok(select != null, '找不到商品选择器')
  const setValue = Object.getOwnPropertyDescriptor(
    HTMLSelectElement.prototype,
    'value'
  )?.set
  await act(async () => {
    setValue?.call(select, PRODUCT.product_no)
    select.dispatchEvent(new Event('change', { bubbles: true }))
  })
  await screen.settle()
  await screen.settle()
  return screen
}

describe('码库存列表', () => {
  test('选中商品后逐枚列出，四态各有一行文案，且不请求任何明文', async () => {
    const screen = await mountCodesTab()

    const text = screen.text()
    assert.ok(text.includes('#11'), `列表里应有码编号：${text}`)
    assert.ok(
      text.includes(zhKeys.qy_mladm_code_status_unused),
      '未使用那一行的状态徽章缺失'
    )
    assert.ok(
      text.includes(zhKeys.qy_mladm_code_status_taken),
      '已提取那一行的状态徽章缺失'
    )
    // 已发出的那一行要指出去向（订单号），提走的那一行要指出提卡人。
    assert.ok(text.includes('ML-ISSUED-1'), '已发出的码要显示它去了哪张单')
    assert.ok(text.includes('root'), '已提取的码要显示是谁提走的')

    const listCalls = screen.sent.filter(
      (req) => req.method === 'GET' && req.url.endsWith('/codes')
    )
    assert.ok(listCalls.length > 0, '选中商品后应当拉一次码库存')
    assert.ok(
      listCalls.every((req) => req.url.includes(PRODUCT.product_no)),
      '码库存必须按商品号拉，不能拉全站的码'
    )
  })

  test('提卡 / 删除只出现在 unused 的行上', async () => {
    const screen = await mountCodesTab()

    // 三行里只有一行是 unused，所以两个动作各只有一颗。
    const buttons = screen.buttons()
    assert.equal(
      buttons.filter((label) => label === zhKeys.qy_mladm_code_take).length,
      1,
      `「提卡」只该出现在 unused 那一行：${buttons.join(' | ')}`
    )
    assert.equal(
      buttons.filter((label) => label === zhKeys.qy_common_delete).length,
      1,
      `「删除」只该出现在 unused 那一行：${buttons.join(' | ')}`
    )
  })
})

describe('提卡弹窗', () => {
  test('验密之前不显示明文；提交时密码走 X-Qy-Pay-Password 请求头', async () => {
    const screen = await mountCodesTab({
      onTake: () => ({
        id: UNUSED.id,
        code: 'PLAIN-SECRET-XYZ',
        status: 'taken',
        taken_at: 1_800_000_300,
      }),
    })

    assert.ok(await screen.click(zhKeys.qy_mladm_code_take), '点不开提卡弹窗')
    assert.ok(
      screen.text().includes(zhKeys.qy_mladm_code_take_notice_title),
      '提卡弹窗必须先说清「提走之后就不再出售」'
    )
    assert.ok(
      !screen.text().includes('PLAIN-SECRET-XYZ'),
      '还没验密就把明文显示出来了'
    )

    const field = screen.field(zhKeys.qy_pp_field_label)
    assert.ok(field != null, '提卡弹窗必须有支付密码输入框')
    await screen.type(field, 'pay-123456')
    assert.ok(
      await screen.click(zhKeys.qy_mladm_code_take_submit),
      '点不到提交键'
    )

    const take = screen.sent.find(
      (req) => req.method === 'POST' && req.url.endsWith('/take')
    )
    assert.ok(take != null, '没有发出提卡请求')
    assert.equal(
      take.headers['X-Qy-Pay-Password'],
      'pay-123456',
      '支付密码必须走请求头，不能落进 body'
    )
    assert.ok(
      take.body == null || JSON.stringify(take.body).includes('pay') === false,
      '请求体里不该带密码'
    )
    assert.ok(
      take.url.includes(`/codes/${UNUSED.id}/take`),
      `提卡请求打到了别的码上：${take.url}`
    )

    // 验密成功之后才出现明文，并附一句"关掉就再也看不到了"。
    const after = screen.text()
    assert.ok(after.includes('PLAIN-SECRET-XYZ'), '验密成功后应当显示明文')
    assert.ok(
      after.includes(zhKeys.qy_mladm_code_take_again),
      '必须提醒运营先复制'
    )
  })
})
