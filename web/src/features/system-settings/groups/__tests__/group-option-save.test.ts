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

import { nextRowId } from '../lib/group-options'
import { changedGroupOptionKeys } from '../lib/use-group-option-save'

/**
 * 三页共用的保存差分与行身份。
 *
 * 这两条都不是「覆盖率」，它们各自对应一次真实的数据丢失：
 *
 *  1. 差分不成立时，A 页保存一个开关会把它读到那一刻的 `GroupRatio` 旧快照一起
 *     PUT 回去；服务端 `LoadFromJsonString` 是整表替换，另一个管理员刚新增的分组
 *     倍率被静默抹掉，该分组随即按凭空的 1.0 计费。
 *  2. 行 id 从名字或数组长度派生时会撞车，撞车之后一次编辑同时改两行 ——
 *     改的是兜底倍率（钱）或用户能选到哪些模型分组。
 */
describe('changedGroupOptionKeys', () => {
  test('缩进差异不算改动：基线是服务端的紧凑 JSON，页面序列化带 2 空格缩进', () => {
    const server = '{"default":1,"vip":0.5}'
    const pretty = JSON.stringify({ default: 1, vip: 0.5 }, null, 2)
    assert.notEqual(server, pretty, '前提：两者的裸字符串确实不同')
    assert.deepEqual(
      changedGroupOptionKeys({ GroupRatio: pretty }, { GroupRatio: server }),
      [],
      '只有缩进不同的同一份 JSON 不得被当成改动 —— 否则每一次保存都在整表覆写'
    )
  })

  test('键顺序不同但内容相同，同样不算改动', () => {
    assert.deepEqual(
      changedGroupOptionKeys(
        { GroupRatio: '{"vip":0.5,"default":1}' },
        { GroupRatio: '{"default":1,"vip":0.5}' }
      ),
      []
    )
  })

  test('真的改了值必须被检出', () => {
    assert.deepEqual(
      changedGroupOptionKeys(
        { GroupRatio: JSON.stringify({ default: 1, vip: 0.4 }, null, 2) },
        { GroupRatio: '{"default":1,"vip":0.5}' }
      ),
      ['GroupRatio']
    )
  })

  test('显式 0 与缺键是两件事', () => {
    assert.deepEqual(
      changedGroupOptionKeys({ GroupRatio: '{"vip":0}' }, { GroupRatio: '{}' }),
      ['GroupRatio']
    )
  })

  test('非 JSON 的标量 option 逐位比较', () => {
    assert.deepEqual(
      changedGroupOptionKeys(
        { MaxTokenAutoGroups: 6, DefaultUseAutoGroup: true },
        { MaxTokenAutoGroups: 5, DefaultUseAutoGroup: true }
      ),
      ['MaxTokenAutoGroups']
    )
  })

  test('坏 JSON 退化成逐位比较，不会被当成「没变」', () => {
    assert.deepEqual(
      changedGroupOptionKeys(
        { GroupRatio: '{ 坏的' },
        { GroupRatio: '{"default":1}' }
      ),
      ['GroupRatio']
    )
  })
})

describe('nextRowId', () => {
  test('连续取用永不重复，且与内容无关', () => {
    const ids = new Set<string>()
    for (let i = 0; i < 200; i += 1) ids.add(nextRowId('mg'))
    for (let i = 0; i < 200; i += 1) ids.add(nextRowId('uu'))
    assert.equal(ids.size, 400, '行 id 撞车会让一次编辑同时改掉两行')
  })
})

describe('normalizeGroupOptionValue', () => {
  test('数组顺序是语义，绝不排序 —— auto 顺序调整必须被检出', () => {
    assert.deepEqual(
      changedGroupOptionKeys(
        { AutoGroups: JSON.stringify(['vip', 'default'], null, 2) },
        { AutoGroups: '["default","vip"]' }
      ),
      ['AutoGroups'],
      'auto 是从上往下试到第一个可用分组为止，顺序就是它的全部语义'
    )
  })

  test('嵌套对象（交叉倍率）同样按语义比较', () => {
    assert.deepEqual(
      changedGroupOptionKeys(
        { GroupGroupRatio: '{"vip":{"b":2,"a":1}}' },
        { GroupGroupRatio: '{"vip":{"a":1,"b":2}}' }
      ),
      []
    )
    assert.deepEqual(
      changedGroupOptionKeys(
        { GroupGroupRatio: '{"vip":{"a":1,"b":3}}' },
        { GroupGroupRatio: '{"vip":{"a":1,"b":2}}' }
      ),
      ['GroupGroupRatio']
    )
  })
})
