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

import type { TFunction } from 'i18next'

import {
  formatSd,
  formatSdSigned,
  formatSdWithUnit,
  qyStardustName,
} from '../format-sd'

/**
 * 星屑格式化的契约：**整数、千分位、不换算**。
 *
 * 它与 `lib/format.ts` 那一套刻意分开：额度会按站点汇率换成 `$0.27`，星屑不会。
 * 这里的每一条都不依赖 `system-config-store` 的任何状态 —— 星屑的显示不该随
 * 站点把展示币种从 USD 切到 CNY 而变，那正是本轮改造要消灭的错。
 */
describe('formatSd', () => {
  const cases: {
    name: string
    input: number | null | undefined
    want: string
  }[] = [
    { name: '零', input: 0, want: '0' },
    { name: '不到一千不加分隔', input: 999, want: '999' },
    { name: '千分位', input: 1234567, want: '1,234,567' },
    { name: '负数', input: -1234, want: '-1,234' },
    // 星屑没有小数：后端只会下整数，前端算出来的中间值截断而不是四舍五入，
    // 否则 0.5 会被印成 1，与账上那个 0 对不上。
    { name: '小数截断', input: 1999.9, want: '1,999' },
    { name: '负小数截断', input: -0.5, want: '0' },
    { name: 'null 是短横', input: null, want: '-' },
    { name: 'undefined 是短横', input: undefined, want: '-' },
    { name: 'NaN 是短横', input: Number.NaN, want: '-' },
    { name: 'Infinity 是短横', input: Number.POSITIVE_INFINITY, want: '-' },
  ]
  for (const c of cases) {
    test(c.name, () => {
      assert.equal(formatSd(c.input), c.want)
    })
  }
})

describe('formatSdSigned', () => {
  test('正数带加号、负数带减号、零不带符号', () => {
    assert.equal(formatSdSigned(1500), '+1,500')
    assert.equal(formatSdSigned(-1500), '-1,500')
    assert.equal(formatSdSigned(0), '0')
  })

  test('取不到的值仍然是短横，不是 +-', () => {
    assert.equal(formatSdSigned(null), '-')
    assert.equal(formatSdSigned(Number.NaN), '-')
  })
})

describe('formatSdWithUnit', () => {
  test('数字后面跟单位名，中间一个空格', () => {
    assert.equal(formatSdWithUnit(1234, '星屑'), '1,234 星屑')
    assert.equal(formatSdWithUnit(0, 'Stardust'), '0 Stardust')
  })

  test('取不到的值不带单位', () => {
    assert.equal(formatSdWithUnit(null, '星屑'), '-')
  })
})

describe('qyStardustName', () => {
  const t = ((key: string) => `<${key}>`) as unknown as TFunction

  test('运营配了名字就用那个名字（去掉首尾空白）', () => {
    assert.equal(qyStardustName('  星尘  ', t), '星尘')
  })

  test('没配（空串 / 纯空白）回落到 i18n 默认词', () => {
    assert.equal(qyStardustName('', t), '<qy_sd_unit_default>')
    assert.equal(qyStardustName('   ', t), '<qy_sd_unit_default>')
  })
})
