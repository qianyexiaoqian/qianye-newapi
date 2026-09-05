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
import { readFileSync } from 'node:fs'
import { dirname, join } from 'node:path'
import { describe, test } from 'node:test'
import { fileURLToPath } from 'node:url'

/**
 * 「商品奖在管理端点得到」的守卫（项目方 2026-09-04：「转盘不要局限于星屑，增加
 * 一些套餐、兑换码、实物的东西进去」）。
 *
 * 与「找不到双色球」同一种缺陷形状：后端 `prize_type=product` 全部就位、typecheck
 * 全绿，而向导上没有那一格，运营就永远配不出一档商品奖。这里从源码上钉三件事：
 *
 *   1. 奖档编辑器有形态三选一，选商品时走商城商品列表（只拉上架的）；
 *   2. 复核屏与详情页把「第几档 → 哪件商品（形态）」印出来；
 *   3. 用到的每一个新文案键都在主包 `{en,zh}.json` 里(集成前曾在片段
 *      pending-prizes.*.json 里,合并后删除)。
 */

const dir = dirname(fileURLToPath(import.meta.url))
const read = (...p: string[]) => readFileSync(join(dir, '..', ...p), 'utf8')

const wizard = read('components', 'lottery-create-wizard.tsx')
const detail = read('detail.tsx')
const payouts = read('components', 'lottery-payouts-tab.tsx')

const fragment = (lang: 'en' | 'zh') =>
  JSON.parse(
    readFileSync(
      join(dir, '..', '..', '..', '..', '..', 'i18n', 'qy', `${lang}.json`),
      'utf8'
    )
  ) as Record<string, string>
const en = fragment('en')
const zh = fragment('zh')

describe('商品奖的管理端接线', () => {
  test('奖档编辑器：形态三选一 + 商城商品下拉（只拉上架的）', () => {
    for (const needle of [
      "t('qy_lot_prize_type_field')",
      "<SelectItem value='quota'>",
      "<SelectItem value='text'>",
      "<SelectItem value='product'>",
      'qyAdminMallProductsQuery({ page: 1, page_size: 100, enabled: true })',
      "t('qy_lot_product_placeholder')",
      "qyLotTierPrizeForm(tier) === 'product'",
      "qyLotTierPrizeForm(tier) === 'text'",
      "t('qy_lot_text_desc')",
    ]) {
      assert.ok(wizard.includes(needle), `向导缺 ${needle}`)
    }
  })

  test('复核屏与详情页印出商品名与形态', () => {
    assert.ok(wizard.includes("t('qy_lot_review_product_tiers')"))
    assert.ok(wizard.includes("t('qy_lot_review_product_row'"))
    assert.ok(detail.includes("t('qy_lot_a_product_tiers_title')"))
    assert.ok(detail.includes("t('qy_lot_a_product_row'"))
    assert.ok(detail.includes('view?.products?.['))
    assert.ok(payouts.includes('row.mall_order_no'))
  })

  test('新文案键在两份片段里都有，且一一对应', () => {
    assert.deepEqual(Object.keys(en).sort(), Object.keys(zh).sort())
    const sources = [wizard, detail, payouts]
    const used = new Set<string>()
    for (const source of sources) {
      for (const m of source.matchAll(
        /(?:^|[^\w.$])t\(\s*'(qy_lot_[a-z0-9_]+)'/g
      )) {
        used.add(m[1])
      }
    }
    // 本次新增的键必须全在片段里；老键在主包里，这里不重复核。
    for (const key of [
      'qy_lot_prize_type_field',
      'qy_lot_prize_type_quota',
      'qy_lot_prize_type_product',
      'qy_lot_product_field',
      'qy_lot_product_placeholder',
      'qy_lot_product_option',
      'qy_lot_product_stock_unlimited',
      'qy_lot_product_stock_left',
      'qy_lot_product_empty',
      'qy_lot_product_hint',
      'qy_lot_text_desc_public_hint',
      'qy_lot_review_product_tiers',
      'qy_lot_review_product_row',
      'qy_lot_a_product_tiers_title',
      'qy_lot_a_product_row',
      'qy_lot_a_product_unknown',
      'qy_lot_payout_mall_order',
    ]) {
      assert.ok(used.has(key), `${key} 没有被任何组件用到`)
      assert.ok(key in en && key in zh, `${key} 不在片段里`)
    }
    // 校验键走 `t(key)`（复核屏逐条印错误），字面量扫描不到，单独钉。
    for (const key of [
      'qy_lot_v_text_desc_required',
      'qy_lot_v_product_required',
      'qy_lot_v_product_stock_short',
      'qy_lot_v_ball_no_product',
      'qy_lot_payout_kind_product',
    ]) {
      assert.ok(key in en && key in zh, `${key} 不在片段里`)
    }
  })
})
