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
  qyCompareDecimalStrings,
  qyFormatDeltaPercent,
  qyQuotaDirection,
} from '../lib/pricing-math'

/**
 * 涨跌方向决定列表里那个箭头与颜色，而颜色是运营扫一眼改价列表时唯一会看的
 * 东西。这里保护的是两条会直接误导人的行为：
 *
 *   1. **没有可比基准时必须是 `null`**，不能退化成「不变」。后端在通配规则或
 *      模型未配全局价时不下发 `global_effective`，把它当成「持平」会让一次
 *      翻倍的调价显示成一个灰色的「不变」。
 *   2. **涨跌幅原样用后端算好的字符串**，前端不拿两个数重新相除 —— 那会变成
 *      第二个真相。
 */

describe('qyCompareDecimalStrings', () => {
  test('reports up when the new effective charge is higher', () => {
    assert.equal(qyCompareDecimalStrings('0.48', '0.6'), 'up')
  })

  test('reports down when the new effective charge is lower', () => {
    assert.equal(qyCompareDecimalStrings('0.6', '0.48'), 'down')
  })

  test('treats differently written but equal decimals as unchanged', () => {
    // 后端归一化后是 "0.5"，但列宽补零 / 手工输入都可能给出 "0.50"。
    assert.equal(qyCompareDecimalStrings('0.50', '0.5'), 'flat')
  })

  test('returns null when there is no baseline instead of claiming unchanged', () => {
    // 通配规则与未配全局价的模型都不带 global_effective。
    assert.equal(qyCompareDecimalStrings(undefined, '0.6'), null)
    assert.equal(qyCompareDecimalStrings('', '0.6'), null)
  })

  test('returns null when either side is not a decimal', () => {
    assert.equal(qyCompareDecimalStrings('abc', '0.6'), null)
    assert.equal(qyCompareDecimalStrings('0.48', '1.2.3'), null)
  })

  test('compares exponent notation the same as plain decimals', () => {
    assert.equal(qyCompareDecimalStrings('1e-7', '0.0000002'), 'up')
  })
})

describe('qyFormatDeltaPercent', () => {
  test('marks an increase with an explicit plus sign', () => {
    assert.equal(qyFormatDeltaPercent('25.00'), '+25.00%')
  })

  test('keeps the minus sign of a decrease', () => {
    assert.equal(qyFormatDeltaPercent('-25.00'), '-25.00%')
  })

  test('does not sign a zero change', () => {
    assert.equal(qyFormatDeltaPercent('0.00'), '0.00%')
  })

  test('returns an empty string when the backend omitted the percentage', () => {
    // 调用方据此退化成方向文案，而不是渲染出一个孤零零的 "%"。
    assert.equal(qyFormatDeltaPercent(undefined), '')
    assert.equal(qyFormatDeltaPercent(''), '')
  })
})

describe('qyQuotaDirection', () => {
  test('maps a positive difference to more charged', () => {
    assert.equal(qyQuotaDirection(1200), 'up')
  })

  test('maps a negative difference to less charged', () => {
    assert.equal(qyQuotaDirection(-1200), 'down')
  })

  test('maps zero and non-finite values to unchanged', () => {
    assert.equal(qyQuotaDirection(0), 'flat')
    assert.equal(qyQuotaDirection(Number.NaN), 'flat')
  })
})
