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

import { qyUsdScale } from '../../../lib/quota-usd'
import {
  qyTierDraftCoversAny,
  qyTierDraftFrom,
  qyTierDraftHasInvalid,
  qyTierDraftValue,
  qyTierOverrideOf,
} from '../lib/tier-draft'

/** 站点默认换算率：1 USD = 500000 额度，也就是 1 额度 = 0.000002 USD。 */
const scale = qyUsdScale(500000)
/** 换算率不可用的站点（1e6 不整除）：整页退回额度整数录入。 */
const rawScale = qyUsdScale(300000)

/** 主库额度列是 int32，门槛的上界就是它。 */
const MAX_QUOTA = 2147483647

describe('分档草稿的三态', () => {
  test('null 是「未覆盖」，绝不能变成 0', () => {
    const draft = qyTierDraftFrom(null, scale, true)
    assert.equal(draft.covered, false)
    const value = qyTierDraftValue(draft, scale, true)
    assert.deepEqual(value, { kind: 'unset' })
    assert.equal(qyTierOverrideOf(value), null)
  })

  test('0 是「显式关掉这道闸门」，绝不能变成未覆盖', () => {
    const draft = qyTierDraftFrom(0, scale, true)
    assert.equal(draft.covered, true, '0 必须是「已覆盖」')
    assert.equal(draft.text, '0')
    const value = qyTierDraftValue(draft, scale, true)
    assert.deepEqual(value, { kind: 'value', quota: 0 })
    assert.equal(qyTierOverrideOf(value), 0)
  })

  test('填错的框是 invalid，不是「未覆盖」', () => {
    // 换算率是 500000，1 额度 = 0.000002 USD，0.000001 USD 除不尽整数额度。
    const value = qyTierDraftValue(
      { covered: true, text: '0.000001' },
      scale,
      true
    )
    assert.deepEqual(value, { kind: 'invalid' })
    assert.equal(qyTierDraftHasInvalid([value]), true)
    // 塌缩成 unset 的话，这个填错的框会被静默存成「不覆盖」——
    // 那是一次没人察觉的门槛放宽。
    assert.notDeepEqual(value, { kind: 'unset' })
  })

  test('取消勾选保留输入文本：改主意勾回来时数字不该没了', () => {
    const draft = { covered: false, text: '12.5' }
    assert.deepEqual(qyTierDraftValue(draft, scale, true), { kind: 'unset' })
    assert.deepEqual(
      qyTierDraftValue({ ...draft, covered: true }, scale, true),
      {
        kind: 'value',
        quota: 6250000,
      }
    )
  })
})

describe('USD 往返一致性（分档侧）', () => {
  /**
   * 「界面显示 USD → 什么都不改直接保存 → 存回的额度逐位相同」。
   *
   * 差一个 quota 不是显示问题：运营什么都没改点一下保存，审计里就多出一条
   * 无中生有的门槛变更，而门槛变更正是「谁把某一档的日额度放大了十倍」
   * 要追的那条线索。
   */
  const roundTrip = (quota: number, s = scale, asUsd = true) => {
    const value = qyTierDraftValue(qyTierDraftFrom(quota, s, asUsd), s, asUsd)
    assert.deepEqual(
      value,
      { kind: 'value', quota },
      `额度 ${quota} 往返不一致`
    )
  }

  test('取值域两端与边界值逐位往返', () => {
    for (const quota of [
      0,
      1,
      2,
      499999,
      500000, // $1
      500001,
      12345678,
      100000000,
      MAX_QUOTA - 1,
      MAX_QUOTA,
    ]) {
      roundTrip(quota)
    }
  })

  test('换算率不可用时整项退回额度整数，往返同样无损', () => {
    assert.equal(rawScale.usable, false)
    for (const quota of [0, 1, 7, 12345678, MAX_QUOTA]) {
      roundTrip(quota, rawScale, false)
    }
  })

  test('非金额项（笔数/秒数/小时）走整数通道，0 与未覆盖仍然分得开', () => {
    roundTrip(0, scale, false)
    roundTrip(3, scale, false)
    assert.deepEqual(
      qyTierDraftValue(qyTierDraftFrom(null, scale, false), scale, false),
      {
        kind: 'unset',
      }
    )
  })

  test('一项都不覆盖会被识别出来 —— 后端对空档返回 400，不能等到那时才说', () => {
    const allUnset = [
      qyTierDraftValue({ covered: false, text: '' }, scale, true),
      qyTierDraftValue({ covered: false, text: '5' }, scale, false),
    ]
    assert.equal(qyTierDraftCoversAny(allUnset), false)
    assert.equal(
      qyTierDraftCoversAny([
        ...allUnset,
        qyTierDraftValue({ covered: true, text: '0' }, scale, false),
      ]),
      true,
      '覆盖成 0 也算覆盖了一项'
    )
  })
})
