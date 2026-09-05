/*
 * 下单弹窗的验密触发：先不带密码提交，后端说要才显示密码格，重试沿用同一个幂等键。
 *
 * 这条链路上没有一处能靠类型守住：
 *   · 第一次提交**不带** `pay_password`（阈值只有后端知道）；
 *   · 吃到 `qy_pay_pwd_required` 之后密码格才出现，而且首次不弹 toast；
 *   · 第二次提交带上密码，`client_request_id` 与第一次**逐字相同** ——
 *     换了键就是一次全新的兑换，后端的幂等索引认不出原单。
 *   · 成功之后回执上有单号与"去我的订单验密查看"那句话。
 */
import assert from 'node:assert/strict'
import { after, describe, test } from 'node:test'

import {
  cleanupQyMallScreens,
  mountQyMallScreen,
  qyMallHttpError,
  zhKeys,
} from './mall-harness'

const { QyMallOrderDialog } = await import('../components/mall-order-dialog')

after(async () => {
  await cleanupQyMallScreens()
})

const PRODUCT = {
  product_no: 'P-code',
  kind: 'code',
  title: '一张第三方卡密',
  description: '',
  cover_url: '',
  price: 1200,
  stock: 5,
  sold: 2,
  per_user_limit: 0,
  sale_start_at: 0,
  sale_end_at: 0,
  plan_id: 0,
  plan: null,
  available: true,
  my_count: 0,
  preview: null,
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

const STARDUST_ME = {
  name: '',
  quota_per_unit: 500000,
  balance: {
    available: 5000,
    total_earned: 0,
    total_spent: 0,
    total_refunded: 0,
    total_adjusted: 0,
    carry: '0',
    hold_reason: '',
  },
  next_settle_at: 0,
  yesterday: null,
  pending_held_count: 0,
}

describe('下单弹窗的支付密码', () => {
  test('后端回 qy_pay_pwd_required 之后才显示密码格；重试沿用同一个幂等键', async () => {
    let attempts = 0
    const screen = await mountQyMallScreen({
      element: (
        <QyMallOrderDialog target={{ product: PRODUCT }} onClose={() => {}} />
      ),
      respond: (req) => {
        if (req.url.includes('/pay-password')) return PAY_PASSWORD_STATUS
        if (req.url.includes('/stardust/me')) return STARDUST_ME
        if (req.method === 'POST' && req.url.endsWith('/mall/orders')) {
          attempts += 1
          if (attempts === 1) {
            return qyMallHttpError(400, 'qy_pay_pwd_required', '请输入支付密码')
          }
          return {
            order_no: 'ML-PROBE-1',
            kind: 'code',
            status: 'done',
            price: 1200,
            replayed: false,
            code_available: true,
          }
        }
        return undefined
      },
    })

    // 一开始没有密码格：阈值与"要不要验"只有后端知道，前端不猜。
    assert.ok(
      screen.field(zhKeys.qy_pp_field_label) == null,
      '还没提交就显示了密码格 —— 前端在猜后端的验密规则'
    )
    assert.ok(
      screen.text().includes(`1,200 ${zhKeys.qy_sd_unit_default}`),
      '弹窗上必须复述这一单要花多少'
    )

    assert.ok(await screen.click(zhKeys.qy_ml_order_submit), '没有「确认兑换」')
    const password = screen.field(zhKeys.qy_pp_field_label)
    assert.ok(password != null, '吃到 qy_pay_pwd_required 之后密码格没有出现')
    assert.ok(
      screen.button(zhKeys.qy_ml_order_submit)?.disabled === true,
      '密码还没填时提交键应当是禁用的'
    )

    await screen.type(password, 'secret-1')
    assert.ok(await screen.click(zhKeys.qy_ml_order_submit))

    const posts = screen.sent.filter(
      (row) => row.method === 'POST' && row.url.endsWith('/mall/orders')
    )
    assert.equal(posts.length, 2, `应当恰好提交两次：${posts.length}`)
    const first = posts[0].body as Record<string, unknown>
    const second = posts[1].body as Record<string, unknown>
    assert.equal(first.product_no, 'P-code')
    assert.ok(
      !('pay_password' in first),
      '第一次提交不该带 pay_password（前端不猜验密规则）'
    )
    assert.equal(second.pay_password, 'secret-1')
    assert.ok(
      typeof first.client_request_id === 'string' &&
        first.client_request_id !== '',
      '幂等键必须在打开弹窗时就生成'
    )
    assert.equal(
      second.client_request_id,
      first.client_request_id,
      '重试换了幂等键：后端会把它当成第二次兑换'
    )

    const text = screen.text()
    assert.ok(text.includes('ML-PROBE-1'), `回执上没有单号：${text}`)
    // 图形回执(task-C-visual):成功屏先用戳记说"成功",单号与下一步仍是文字。
    assert.ok(
      document.querySelector('[data-testid="qy-stamp"]') != null,
      '回执屏上没有戳记'
    )
    assert.ok(
      text.includes(zhKeys.qy_ml_receipt_code_next),
      '兑换码类的回执必须把"去我的订单验密查看"说出来'
    )
  })
})
