/*
 * 订单在界面上怎么说：状态徽章按 kind 分文案，时间线保留"下一步"占位。
 */
import assert from 'node:assert/strict'
import { describe, test } from 'node:test'

import { buildQyMallTimeline, qyMallOrderStatusView } from '../order'

// buildQyMallTimeline 只用到 TFunction 的 (key, options?) => string 这一支。
const t = ((key: string) => key) as never

describe('状态徽章', () => {
  test('实物的 paid 是「待发货」，套餐的 paid 是「处理中」，颜色同一档', () => {
    const physical = qyMallOrderStatusView('paid', 'physical')
    const plan = qyMallOrderStatusView('paid', 'plan')
    assert.equal(physical.labelKey, 'qy_ml_st_paid_physical')
    assert.equal(plan.labelKey, 'qy_ml_st_paid')
    assert.equal(physical.status, plan.status)
  })

  test('held 走 uncertain 的告警色，绝不是失败色', () => {
    assert.equal(qyMallOrderStatusView('held', 'plan').status, 'uncertain')
    assert.equal(qyMallOrderStatusView('failed', 'plan').status, 'failed')
  })

  test('未登记的状态原样显示，不崩', () => {
    const view = qyMallOrderStatusView('something_new', 'code')
    assert.equal(view.status, 'something_new')
    assert.equal(view.labelKey, null)
  })
})

describe('时间线', () => {
  const base = { created_at: 1_800_000_000, kind: 'physical', status: 'paid' }

  test('未完结的单：已发生的事件之后追加灰色的"下一步"占位', () => {
    const items = buildQyMallTimeline(
      base,
      [{ at: 1_800_000_000, action: 'pay', note: '已支付 100 星屑' }],
      t
    )
    assert.deepEqual(
      items.map((item) => [item.title, item.state]),
      [
        ['qy_ml_ev_pay', 'current'],
        ['qy_ml_tl_next_ship', 'pending'],
      ]
    )
    assert.equal(items[0].description, '已支付 100 星屑')
  })

  test('终态不再有下一步；往回走的动作画成失败色', () => {
    const items = buildQyMallTimeline(
      { ...base, status: 'cancelled' },
      [
        { at: 1, action: 'pay', note: '' },
        { at: 2, action: 'cancel', note: '用户取消' },
      ],
      t
    )
    assert.deepEqual(
      items.map((item) => [item.title, item.state]),
      [
        ['qy_ml_ev_pay', 'done'],
        ['qy_ml_ev_cancel', 'failed'],
      ]
    )
  })

  test('held 的下一步是"等待人工核对"且高亮', () => {
    const items = buildQyMallTimeline(
      { ...base, kind: 'plan', status: 'held' },
      [{ at: 1, action: 'hold', note: '' }],
      t
    )
    const last = items.at(-1)
    assert.equal(last?.title, 'qy_ml_tl_next_held')
    assert.equal(last?.state, 'current')
  })

  test('一条事件都没有时至少画出下单那一刻', () => {
    const items = buildQyMallTimeline({ ...base, status: 'done' }, [], t)
    assert.equal(items.length, 1)
    assert.equal(items[0].timestamp, base.created_at)
  })
})
