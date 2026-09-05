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
import { describe, test } from 'node:test'

import {
  qyLotDraftForPlay,
  qyLotDraftToInput,
  qyLotEmptyDraft,
  qyLotProductChoiceOf,
  qyLotTierPrizeForm,
  qyLotValidateDraft,
  type QyLotDraft,
  type QyLotProductChoice,
} from '../draft'

/**
 * 商品奖（`prize_type='product'`，design-15 §7 的扩展）在向导里的三条契约：
 *
 *   1. 三种形态互斥地归一化进提交体 —— 后端对「星屑奖却带 product_no」、
 *      「商品奖带金额 / 领取说明」都是 400，而这些字段在表单上按形态隐藏，
 *      运营改不了它们；
 *   2. 份数不超过商品余量在**本地**就拦下（与后端发布期的
 *      `qy_lot_prize_product_short` 同一条判据），别让人走完四步才吃 400；
 *   3. 双色球不许商品奖。
 */

const YAML = {
  max_total_entries_hard: 50_000,
  reveal_delay_seconds: 0,
  max_prize_tiers: 8,
} as never

function wheelDraft(tiers: QyLotDraft['tiers']): QyLotDraft {
  return {
    ...qyLotDraftForPlay(qyLotEmptyDraft(500), 'wheel'),
    title: '转盘',
    stake_quota: 100,
    max_total_entries: 1_000,
    tiers,
  }
}

const products = new Map<string, QyLotProductChoice>([
  [
    'PD-CODE',
    { product_no: 'PD-CODE', kind: 'code', title: '码', remaining: 2 },
  ],
  [
    'PD-PLAN',
    { product_no: 'PD-PLAN', kind: 'plan', title: '月卡', remaining: -1 },
  ],
])

describe('商品奖草稿', () => {
  test('缺省形态是星屑奖；text / product 按 prize_type 判', () => {
    assert.equal(qyLotTierPrizeForm({}), 'quota')
    assert.equal(qyLotTierPrizeForm({ prize_type: '' }), 'quota')
    assert.equal(qyLotTierPrizeForm({ prize_type: 'text' }), 'text')
    assert.equal(qyLotTierPrizeForm({ prize_type: 'product' }), 'product')
  })

  test('管理端商品行 → 余量：兑换码看未用码数，实物看 stock − sold，-1 不限', () => {
    assert.equal(
      qyLotProductChoiceOf({
        product_no: 'a',
        kind: 'code',
        title: 't',
        stock: -1,
        sold: 0,
        code_stock: { unused: 7 },
      }).remaining,
      7
    )
    assert.equal(
      qyLotProductChoiceOf({
        product_no: 'b',
        kind: 'physical',
        title: 't',
        stock: 5,
        sold: 3,
      }).remaining,
      2
    )
    assert.equal(
      qyLotProductChoiceOf({
        product_no: 'c',
        kind: 'plan',
        title: 't',
        stock: -1,
        sold: 9,
      }).remaining,
      -1
    )
  })

  test('提交体按形态归一化：商品奖金额 0、无领取说明；非商品奖不带商品号', () => {
    const input = qyLotDraftToInput(
      wheelDraft([
        {
          tier: 1,
          name: '码',
          amount_quota: 999,
          count: 1,
          win_ppm: 100_000,
          prize_type: 'product',
          product_no: 'PD-CODE',
          text_desc: '残留的说明',
        },
        {
          tier: 2,
          name: '星屑',
          amount_quota: 500,
          count: 1,
          win_ppm: 100_000,
          product_no: 'PD-CODE',
        },
        {
          tier: 3,
          name: '文本',
          amount_quota: 3,
          count: 1,
          win_ppm: 100_000,
          prize_type: 'text',
          text_desc: '联系客服',
          product_no: 'PD-CODE',
        },
      ]),
      0
    )
    assert.deepEqual(
      input.prizes.map((p) => [
        p.prize_type,
        p.amount_quota,
        p.product_no,
        p.text_desc,
      ]),
      [
        ['product', 0, 'PD-CODE', ''],
        ['quota', 500, '', ''],
        ['text', 0, '', '联系客服'],
      ]
    )
  })

  test('商品奖必须选商品；份数不超过余量（同一商品多档按 Σ 核）', () => {
    const missing = wheelDraft([
      {
        tier: 1,
        name: '码',
        amount_quota: 0,
        count: 1,
        win_ppm: 100_000,
        prize_type: 'product',
      },
    ])
    assert.ok(
      qyLotValidateDraft(missing, YAML, 0, 0, undefined, products).includes(
        'qy_lot_v_product_required'
      )
    )
    const over = wheelDraft([
      {
        tier: 1,
        name: '码 A',
        amount_quota: 0,
        count: 1,
        win_ppm: 100_000,
        prize_type: 'product',
        product_no: 'PD-CODE',
      },
      {
        tier: 2,
        name: '码 B',
        amount_quota: 0,
        count: 2,
        win_ppm: 100_000,
        prize_type: 'product',
        product_no: 'PD-CODE',
      },
    ])
    const errors = qyLotValidateDraft(over, YAML, 0, 0, undefined, products)
    assert.ok(errors.includes('qy_lot_v_product_stock_short'), String(errors))
    assert.ok(
      !errors.includes('qy_lot_v_tier_amount'),
      '商品奖金额为 0 不是错误'
    )
    // 不限库存的套餐、或商品表还没拉到：不报余量。
    const fine = wheelDraft([
      {
        tier: 1,
        name: '月卡',
        amount_quota: 0,
        count: 100,
        win_ppm: 100_000,
        prize_type: 'product',
        product_no: 'PD-PLAN',
      },
    ])
    assert.deepEqual(
      qyLotValidateDraft(fine, YAML, 0, 0, undefined, products).filter((k) =>
        k.startsWith('qy_lot_v_product')
      ),
      []
    )
    assert.deepEqual(
      qyLotValidateDraft(over, YAML, 0, 0, undefined, undefined).filter((k) =>
        k.startsWith('qy_lot_v_product')
      ),
      []
    )
  })

  test('文本奖要领取说明；星屑奖照旧要金额', () => {
    const draft = wheelDraft([
      {
        tier: 1,
        name: '文本',
        amount_quota: 0,
        count: 1,
        win_ppm: 100_000,
        prize_type: 'text',
      },
      { tier: 2, name: '星屑', amount_quota: 0, count: 1, win_ppm: 100_000 },
    ])
    const errors = qyLotValidateDraft(draft, YAML, 0, 0)
    assert.ok(errors.includes('qy_lot_v_text_desc_required'))
    assert.ok(errors.includes('qy_lot_v_tier_amount'))
  })

  test('双色球不许商品奖', () => {
    const draft: QyLotDraft = {
      ...qyLotDraftForPlay(qyLotEmptyDraft(500), 'ball'),
      title: '双色球',
      stake_quota: 100,
      series_no: 'S1',
      tiers: [
        {
          tier: 1,
          name: '实物',
          amount_quota: 0,
          count: 1,
          win_ppm: 0,
          red_match: 6,
          blue_match: 1,
          pool_share_bps: 0,
          prize_type: 'product',
          product_no: 'PD-CODE',
        },
      ],
    }
    assert.ok(
      qyLotValidateDraft(draft, YAML, 0, 0, undefined, products).includes(
        'qy_lot_v_ball_no_product'
      )
    )
  })
})
