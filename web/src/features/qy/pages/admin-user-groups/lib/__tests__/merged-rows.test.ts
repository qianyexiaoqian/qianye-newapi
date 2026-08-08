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
  qyUgParseTopupInput,
  qyUgSplitUsable,
  qyUgTopupInputOf,
} from '../merged-rows'

/**
 * 合并成一张表之后仍然留在前端的两件事。
 *
 * 合并本身在后端（`userGroupRow` 直接下发观测 ∪ 登记 ∪ options 死配置，
 * 连「可用模型分组」的名字清单都算好了）。这里只剩纯显示的折叠，和充值倍率
 * 输入框那三态 —— 后者每一条错了的方向都是收款金额。
 */
describe('可用模型分组那一列的折叠', () => {
  test('不超过上限时全列出来，不折', () => {
    assert.deepEqual(qyUgSplitUsable(['a', 'b'], 3), {
      shown: ['a', 'b'],
      overflow: 0,
    })
  })

  test('超出的折成 +N，且折的是**显示**，总数仍然对得上', () => {
    const usable = ['免费の渠道', '浅夜の梦专属号池', 'c', 'd', 'e']
    const { shown, overflow } = qyUgSplitUsable(usable, 3)
    assert.deepEqual(shown, ['免费の渠道', '浅夜の梦专属号池', 'c'])
    assert.equal(overflow, 2)
    assert.equal(shown.length + overflow, usable.length)
  })

  test('一个都没有时是空的展示 + 0 折叠，不是负数', () => {
    assert.deepEqual(qyUgSplitUsable([], 3), { shown: [], overflow: 0 })
  })
})

describe('充值倍率输入框的三态', () => {
  test('空输入是「删掉这个键」，不是 0', () => {
    // 删键 = 回落上游兜底（1 + 一条 SysError）；写 0 = 这一档充值恒为 0 元。
    assert.deepEqual(qyUgParseTopupInput(''), { kind: 'clear' })
    assert.deepEqual(qyUgParseTopupInput('   '), { kind: 'clear' })
  })

  test('显式 0 是一个合法配置，不能被当成空值吞掉', () => {
    assert.deepEqual(qyUgParseTopupInput('0'), { kind: 'set', value: 0 })
  })

  test('小数原样解析，不漂移', () => {
    assert.deepEqual(qyUgParseTopupInput('0.9'), { kind: 'set', value: 0.9 })
  })

  test('非十进制字面量一律拒绝，绝不提交一个荒谬的乘数', () => {
    // `1e3` / `0x10` / `Infinity` 都是合法的 JS 数字字面量，静默接受会把它们
    // 写进收款倍率；`abc` 会变成 NaN，序列化后整份 TopupGroupRatio 变 null。
    for (const raw of ['abc', '1e3', '0x10', 'Infinity', '-1', '1.2.3']) {
      assert.deepEqual(
        qyUgParseTopupInput(raw),
        { kind: 'invalid' },
        `「${raw}」必须被拒绝`
      )
    }
  })
})

describe('服务端值 → 输入框原文', () => {
  test('没配过是空输入框，**不预填 1**', () => {
    // 预填之后运营随手保存一遍，一档"没配过"就固化成一条显式记录，
    // 此后上游兜底再改也影响不到它，而没有任何人做过这个决定。
    assert.equal(qyUgTopupInputOf(null), '')
  })

  test('显式 0 回显 "0"，而不是退化成"没配过"', () => {
    assert.equal(qyUgTopupInputOf(0), '0')
  })

  test('往返稳定：回显之后再解析回同一个值', () => {
    assert.deepEqual(qyUgParseTopupInput(qyUgTopupInputOf(0.85)), {
      kind: 'set',
      value: 0.85,
    })
    assert.deepEqual(qyUgParseTopupInput(qyUgTopupInputOf(null)), {
      kind: 'clear',
    })
  })
})
