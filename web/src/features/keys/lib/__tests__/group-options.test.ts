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

import { buildApiKeyGroupOptions } from '../group-options'

/**
 * 用户建令牌时那个分组下拉的候选项。
 *
 * 这一层的契约只有两条，但两条都是"错了不会有任何信号"的形状：
 *
 *  1. **`desc` 原样透传。** 它是服务端按「按格备注 > 模型分组备注 > 历史白名单
 *     文案」解析之后的最终结果。在这里做任何回退推断，都会让运营在按格备注里
 *     写的那句话在用户界面上被另一句覆盖掉，而管理端显示得好好的。
 *  2. **顺序稳定。** 服务端给的是 Go map 序列化的顺序，两次请求可能不同 ——
 *     下拉里的选项在刷新之后换位置，用户会点错分组，而分组决定他按什么价扣钱。
 */
describe('建令牌时的分组候选', () => {
  test('备注原样透传 —— 服务端解析出来的那一句就是用户看到的那一句', () => {
    const options = buildApiKeyGroupOptions({
      pool: { desc: '浅夜の梦专属号池（本档专属价）', ratio: 0.3 },
    })
    assert.deepEqual(options, [
      {
        value: 'pool',
        label: 'pool',
        desc: '浅夜の梦专属号池（本档专属价）',
        ratio: 0.3,
      },
    ])
  })

  test('空备注回落成分组名 —— 排版兜底，不是语义兜底', () => {
    const options = buildApiKeyGroupOptions({ plain: { desc: '', ratio: 1 } })
    assert.equal(options[0].desc, 'plain')
  })

  test('顺序按名字升序，与服务端 map 的序列化顺序无关', () => {
    const forward = buildApiKeyGroupOptions({
      zeta: { desc: '', ratio: 1 },
      alpha: { desc: '', ratio: 1 },
      mid: { desc: '', ratio: 1 },
    })
    const shuffled = buildApiKeyGroupOptions({
      mid: { desc: '', ratio: 1 },
      zeta: { desc: '', ratio: 1 },
      alpha: { desc: '', ratio: 1 },
    })
    assert.deepEqual(
      forward.map((option) => option.value),
      ['alpha', 'mid', 'zeta']
    )
    assert.deepEqual(forward, shuffled)
  })

  test('auto 的倍率是字符串（服务端下发「自动」），不能被当成数字丢掉', () => {
    const options = buildApiKeyGroupOptions({
      auto: { desc: '自动挑选', ratio: '自动' },
    })
    assert.equal(options[0].ratio, '自动')
  })

  test('拿不到数据时是空数组，不是抛异常', () => {
    assert.deepEqual(buildApiKeyGroupOptions(undefined), [])
  })
})
