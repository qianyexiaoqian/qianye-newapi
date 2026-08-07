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
  GROUP_MATRIX_PAGE_KEYS,
  GROUP_MATRIX_PAGE_OPTION_KEYS,
  GROUP_OPTION_KEYS,
  MODEL_GROUP_PAGE_KEYS,
  READ_ONLY_GROUP_OPTION_KEYS,
  USER_GROUP_PAGE_KEYS,
  buildModelGroupRows,
  buildUserGroupRows,
  duplicateRowNames,
  freeModelGroups,
  invalidRatioRowNames,
  modelGroupsMissingRatio,
  moveAutoGroup,
  parseAutoGroups,
  serializeModelGroupRows,
  serializeTopupRatios,
  serializeUsableGroupRows,
  buildUsableGroupRows,
  type GroupOptionKey,
} from '../lib/group-options'

/**
 * 拆页之后的两条硬约束的机器校验。
 *
 *  1. **完备且互斥**：8 个上游 option 每一个恰好归属一页，没有落单、没有两页
 *     都能改的。落单的表现是一个还在参与计费的配置项从界面上彻底消失；重复的
 *     表现是两页各持一份基线、后保存的那一页把前一页的改动整段覆盖回旧值。
 *  2. **序列化不吃掉显式零**：倍率与充值折扣里的 `0` 与「未设置」是两件事，
 *     混同的方向恰好是资金方向（本该免费的按原价收、或反过来）。
 */
describe('分组配置项的归属（完备且互斥）', () => {
  const buckets: readonly (readonly GroupOptionKey[])[] = [
    USER_GROUP_PAGE_KEYS,
    GROUP_MATRIX_PAGE_KEYS,
    MODEL_GROUP_PAGE_KEYS,
    READ_ONLY_GROUP_OPTION_KEYS,
  ]

  test('并集恰好是全部 7 项：没有配置项在拆页中落单', () => {
    const seen = new Set<string>()
    for (const bucket of buckets) for (const key of bucket) seen.add(key)
    assert.deepEqual(
      [...seen].sort(),
      [...GROUP_OPTION_KEYS].sort(),
      '有配置项没有归属页面：它会从界面上消失，但仍在参与计费'
    )
  })

  test('两两不交：同一份数据只有一个编辑器', () => {
    const owner = new Map<string, number>()
    for (const [index, bucket] of buckets.entries()) {
      for (const key of bucket) {
        const previous = owner.get(key)
        assert.equal(
          previous,
          undefined,
          `${key} 同时归属两页（#${previous} 与 #${index}）：两页各持一份基线，后保存的会覆盖前一页`
        )
        owner.set(key, index)
      }
    }
  })

  test('矩阵页经 updateOption 写回的键是它归属键的真子集', () => {
    // `GroupGroupRatio` 必须走矩阵自己的两阶段 PUT。它一旦漏进
    // `GROUP_MATRIX_PAGE_OPTION_KEYS`，倍率就会被一次普通的 updateOption
    // 写进上游 options —— 绕开预览闸门、绕开 base_ratio_hash 冲突检测、
    // 也绕开部分失败横幅，而那正是这套两库写入最坏的失败方式。
    for (const key of GROUP_MATRIX_PAGE_OPTION_KEYS) {
      assert.ok(
        (GROUP_MATRIX_PAGE_KEYS as readonly string[]).includes(key),
        `${key} 不在矩阵页的归属清单里`
      )
    }
    assert.equal(
      (GROUP_MATRIX_PAGE_OPTION_KEYS as readonly string[]).includes(
        'GroupGroupRatio'
      ),
      false,
      '交叉倍率不得经 updateOption 写回：那会绕开矩阵的预览闸门'
    )
  })

  test('充值折扣归用户分组页，兜底倍率归模型分组页', () => {
    // 这两条是项目方肉眼可见的那处错位，写成断言而不是注释。
    assert.ok(
      (USER_GROUP_PAGE_KEYS as readonly string[]).includes('TopupGroupRatio')
    )
    assert.ok(
      (MODEL_GROUP_PAGE_KEYS as readonly string[]).includes('GroupRatio')
    )
    assert.ok(
      !(MODEL_GROUP_PAGE_KEYS as readonly string[]).includes('TopupGroupRatio')
    )
  })
})

describe('模型分组：GroupRatio 的读写', () => {
  test('显式 0 倍率原样写回，不被当成未设置丢掉', () => {
    const rows = buildModelGroupRows('{"free":0,"paid":1.5}', '{}')
    const out = JSON.parse(serializeModelGroupRows(rows)) as Record<
      string,
      number
    >
    assert.equal(out.free, 0)
    assert.equal(out.paid, 1.5)
  })

  test('只在可选清单里、不在倍率表里的名字：建行但不写回倍率', () => {
    const rows = buildModelGroupRows('{"paid":1.5}', '{"paid":"","ghost":""}')
    const ghost = rows.find((row) => row.name === 'ghost')
    assert.ok(ghost != null, '缺兜底倍率的分组必须建行，否则界面上看不见它')
    assert.equal(ghost.ratio, null)

    const out = JSON.parse(serializeModelGroupRows(rows)) as Record<
      string,
      number
    >
    assert.equal(
      Object.hasOwn(out, 'ghost'),
      false,
      '替缺失的分组补一个 1 等于替运营做了一次定价决定'
    )
    assert.deepEqual(modelGroupsMissingRatio(rows), ['ghost'])
  })

  test('清空输入框不会静默变成 0 倍率（免费），而是落回 1 并禁用保存', () => {
    // 上游 normalizeRatio 是 `Number.isFinite(Number(v)) ? Number(v) : 1`，
    // 而 Number('') === 0 —— 清空一个倍率格子会静默把那个模型分组改成免费。
    // 这条断言就是那个方向的守卫：写回值必须是 1，且这一行必须被标成非法。
    const rows = [
      { id: 'a', name: 'x', ratio: 'abc' },
      { id: 'b', name: 'y', ratio: '' },
      { id: 'c', name: 'z', ratio: '0' },
    ]
    const out = JSON.parse(serializeModelGroupRows(rows)) as Record<
      string,
      number
    >
    assert.equal(out.x, 1)
    assert.equal(out.y, 1, '空输入框绝不能变成 0（白送整个模型分组）')
    assert.equal(out.z, 0, '显式敲进去的 0 仍然是显式免费，逐位保持上游语义')
    assert.deepEqual(invalidRatioRowNames(rows), ['x', 'y'])
  })

  test('小数往返不漂移；空名与首尾空白被规范掉', () => {
    const rows = buildModelGroupRows('{"a":0.1,"b":3}', '{}')
    rows.push({ id: 'blank', name: '   ', ratio: '2' })
    rows.push({ id: 'pad', name: '  c  ', ratio: '0.30' })
    const out = JSON.parse(serializeModelGroupRows(rows)) as Record<
      string,
      number
    >
    assert.deepEqual(out, { a: 0.1, b: 3, c: 0.3 })
  })

  test('重名会被点名（写回时后一个键静默吃掉前一个）', () => {
    assert.deepEqual(
      duplicateRowNames([{ name: 'vip' }, { name: ' vip ' }, { name: 'free' }]),
      ['vip']
    )
  })

  test('0 倍率的分组单独列出（白送必须显式看见）', () => {
    const rows = buildModelGroupRows('{"free":0,"paid":1}', '{}')
    assert.deepEqual(freeModelGroups(rows), ['free'])
  })
})

describe('用户分组：TopupGroupRatio 的读写', () => {
  test('取值域不含 GroupRatio 的键：模型分组不会漏进用户分组页', () => {
    const rows = buildUserGroupRows(
      ['default', 'vip'],
      '{"vip":0.8}',
      '{"vip":{"pool":0.3}}'
    )
    const names = rows.map((row) => row.name).sort()
    assert.deepEqual(names, ['default', 'vip'])
    assert.equal(
      names.includes('pool'),
      false,
      'GroupGroupRatio 的内层键是模型分组，绝不能当成用户分组'
    )
  })

  test('未设置（空串）不写键；显式 0 写回 0', () => {
    const rows = buildUserGroupRows(['a', 'b'], '{"b":0}', '{}')
    const a = rows.find((row) => row.name === 'a')
    const b = rows.find((row) => row.name === 'b')
    assert.equal(a?.topupRatio, '')
    assert.equal(b?.topupRatio, '0')

    const out = JSON.parse(serializeTopupRatios(rows)) as Record<string, number>
    assert.equal(Object.hasOwn(out, 'a'), false, '未设置不能变成 0')
    assert.equal(out.b, 0, '显式免费充值不能被 omitempty 吃掉')
  })

  test('非法数值整条跳过，绝不写出 NaN（会让整份 JSON 变 null）', () => {
    const out = serializeTopupRatios([
      { id: '1', name: 'a', topupRatio: 'abc' },
      { id: '2', name: 'b', topupRatio: '1.2' },
    ])
    assert.equal(out.includes('NaN'), false)
    assert.deepEqual(JSON.parse(out), { b: 1.2 })
  })

  test('扩展未启用（权威清单为空）时仍能从两份 option 建出行', () => {
    const rows = buildUserGroupRows([], '{"vip":0.8}', '{"svip":{"pool":0.3}}')
    assert.deepEqual(rows.map((row) => row.name).sort(), ['svip', 'vip'])
  })
})

describe('可选清单', () => {
  test('UserUsableGroups 往返保持描述（含空描述）', () => {
    const rows = buildUsableGroupRows('{"a":"标准","b":""}')
    assert.deepEqual(JSON.parse(serializeUsableGroupRows(rows)), {
      a: '标准',
      b: '',
    })
  })

  test('坏 JSON 不抛异常，退化成空清单', () => {
    assert.deepEqual(buildUsableGroupRows('nope'), [])
  })
})

describe('auto 顺序', () => {
  test('只认字符串数组，混入非字符串项被剔掉而不是整份丢弃', () => {
    assert.deepEqual(parseAutoGroups('["a",1,"b",null]'), ['a', 'b'])
    assert.deepEqual(parseAutoGroups('{"a":1}'), [])
  })

  test('上下移动保持其余项顺序；越界时原样返回', () => {
    assert.deepEqual(moveAutoGroup(['a', 'b', 'c'], 1, 'up'), ['b', 'a', 'c'])
    assert.deepEqual(moveAutoGroup(['a', 'b', 'c'], 2, 'down'), ['a', 'b', 'c'])
    assert.deepEqual(moveAutoGroup(['a', 'b', 'c'], 0, 'up'), ['a', 'b', 'c'])
  })
})

describe('freeModelGroups 与空输入框', () => {
  test('清空的输入框不算「白送」——否则同一行会同时出红条和黄条，结论互相矛盾', () => {
    const rows = [
      { id: 'mg_1', name: '正在重填的分组', ratio: '' },
      { id: 'mg_2', name: '空白也算空', ratio: '   ' },
      { id: 'mg_3', name: '显式免费', ratio: '0' },
      { id: 'mg_4', name: '正常', ratio: '0.5' },
      { id: 'mg_5', name: '只在可选清单里', ratio: null },
    ]
    assert.deepEqual(freeModelGroups(rows), ['显式免费'])
    // 同一行仍然必须被「填不出数值」拦下：serializeModelGroupRows 对空串写回的是 1，
    // 而黄条会说它是 0 —— 三处说法不一致时运营会按最乐观的那条理解。
    assert.deepEqual(invalidRatioRowNames(rows), [
      '正在重填的分组',
      '空白也算空',
    ])
    assert.equal(
      JSON.parse(serializeModelGroupRows(rows))['正在重填的分组'],
      1,
      '空输入框写回 1（= GetGroupRatio 找不到键时的值），不是 0'
    )
    assert.equal(JSON.parse(serializeModelGroupRows(rows))['显式免费'], 0)
  })
})
