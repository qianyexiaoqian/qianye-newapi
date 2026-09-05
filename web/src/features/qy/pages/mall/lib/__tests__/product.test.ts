/*
 * 商品卡片与详情共用的"现在能不能买、不能买是为什么"。
 *
 * 三处（卡片 / 详情 / 下单）都要说这句话；说错的代价是用户按下去吃一个后端
 * 错误。这里钉住的是判定顺序与 code 类库存的特殊口径。
 */
import assert from 'node:assert/strict'
import { describe, test } from 'node:test'

import type { QyMallProduct } from '../../types'
import { qyMallBuyBlock, qyMallRemaining, qyMallSaleState } from '../product'

const NOW = 1_800_000_000

function product(over: Partial<QyMallProduct> = {}): QyMallProduct {
  return {
    product_no: 'P-1',
    kind: 'physical',
    title: '一件实物',
    description: '',
    cover_url: '',
    price: 100,
    stock: -1,
    sold: 0,
    per_user_limit: 0,
    sale_start_at: 0,
    sale_end_at: 0,
    plan_id: 0,
    plan: null,
    available: true,
    my_count: 0,
    ...over,
  }
}

describe('发售窗', () => {
  test('0 = 不限；左闭右开', () => {
    assert.equal(qyMallSaleState(product(), NOW), 'on_sale')
    assert.equal(
      qyMallSaleState(product({ sale_start_at: NOW + 1 }), NOW),
      'upcoming'
    )
    assert.equal(
      qyMallSaleState(product({ sale_start_at: NOW }), NOW),
      'on_sale',
      '开售时刻本身在窗内'
    )
    assert.equal(
      qyMallSaleState(product({ sale_end_at: NOW }), NOW),
      'ended',
      '截止时刻本身已在窗外'
    )
  })
})

describe('剩余件数', () => {
  test('code 类的 stock 就是 unused 数，不再减 sold', () => {
    assert.equal(
      qyMallRemaining(product({ kind: 'code', stock: 3, sold: 10 })),
      3
    )
  })

  test('实物 / 套餐按 stock - sold；-1 = 不限', () => {
    assert.equal(qyMallRemaining(product({ stock: 5, sold: 2 })), 3)
    assert.equal(qyMallRemaining(product({ stock: 5, sold: 9 })), 0)
    assert.equal(qyMallRemaining(product({ stock: -1, sold: 9 })), null)
  })
})

describe('不能买的原因', () => {
  test('能买时为 null', () => {
    assert.equal(qyMallBuyBlock(product(), NOW), null)
  })

  test('售期先于库存先于限购先于后端 available', () => {
    assert.equal(
      qyMallBuyBlock(
        product({ sale_end_at: NOW - 1, stock: 0, available: false }),
        NOW
      ),
      'ended'
    )
    assert.equal(
      qyMallBuyBlock(product({ stock: 2, sold: 2, available: false }), NOW),
      'sold_out'
    )
    assert.equal(
      qyMallBuyBlock(
        product({ per_user_limit: 1, my_count: 1, available: true }),
        NOW
      ),
      'limit',
      '限购不在后端的 available 里，前端必须自己判'
    )
    assert.equal(
      qyMallBuyBlock(product({ available: false }), NOW),
      'unavailable',
      '字段上算不出原因时才回落到后端那句笼统的"不可兑换"'
    )
  })

  test('限购只在真的到顶时拦：买过但没到上限仍可买', () => {
    assert.equal(
      qyMallBuyBlock(product({ per_user_limit: 2, my_count: 1 }), NOW),
      null
    )
  })
})
