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
  duplicateRowNames,
  moveAutoGroup,
  parseAutoGroups,
  serializeAutoGroups,
  TOKEN_DEFAULT_PAGE_KEYS,
  type GroupOptionKey,
} from '../lib/group-options'

/**
 * 分组配置项的归属守卫。
 *
 * 「同一份数据只有一个编辑器」是拆页唯一的硬约束，而它在代码里没有任何天然的
 * 表现形式：谁都可以在第二个页面上再放一个输入框，typecheck 全绿、页面看起来
 * 更方便，直到两个页面各持一份基线、互相把对方的改动覆盖掉。
 *
 * 序列化那一批断言搬去了两张表各自的合并逻辑旁边（`features/qy/pages/
 * admin-user-groups/lib/__tests__/merged-rows.test.ts` 与
 * `features/qy/pages/admin-model-groups/lib/__tests__/merged-rows.test.ts`），
 * 因为写回 `options` 的代码已经搬去了那里。
 */
describe('分组配置项的归属（完备且互斥）', () => {
  const buckets: readonly (readonly GroupOptionKey[])[] = [
    USER_GROUP_PAGE_KEYS,
    GROUP_MATRIX_PAGE_KEYS,
    MODEL_GROUP_PAGE_KEYS,
    TOKEN_DEFAULT_PAGE_KEYS,
    READ_ONLY_GROUP_OPTION_KEYS,
  ]

  test('并集恰好是全部 8 项：没有配置项在合并中落单', () => {
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
          `${key} 同时归属两处（#${previous} 与 #${index}）：两处各持一份基线，后保存的会覆盖前一处`
        )
        owner.set(key, index)
      }
    }
  })

  test('交叉倍率绝不经 updateOption 写回', () => {
    // `GroupGroupRatio` 必须走矩阵自己的两阶段 PUT。它一旦漏进
    // `GROUP_MATRIX_PAGE_OPTION_KEYS`，倍率就会被一次普通的 updateOption
    // 写进上游 options —— 绕开预览闸门、绕开 base_ratio_hash 冲突检测、
    // 也绕开部分失败横幅，而那正是这套两库写入最坏的失败方式。
    for (const key of GROUP_MATRIX_PAGE_OPTION_KEYS) {
      assert.ok(
        (GROUP_MATRIX_PAGE_KEYS as readonly string[]).includes(key),
        `${key} 不在交叉倍率那一栏的归属清单里`
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

  test('充值折扣归用户分组页；兜底倍率与用户可选清单归模型分组页', () => {
    // 这三条是项目方肉眼可见的归位，写成断言而不是注释。
    // `UserUsableGroups` 本轮从「矩阵」那一栏搬到模型分组页：它的**键**就是
    // 那张表上的「用户可选」开关列，主语是一批渠道，不是一对分组。
    assert.ok(
      (USER_GROUP_PAGE_KEYS as readonly string[]).includes('TopupGroupRatio')
    )
    assert.ok(
      (MODEL_GROUP_PAGE_KEYS as readonly string[]).includes('GroupRatio')
    )
    assert.ok(
      (MODEL_GROUP_PAGE_KEYS as readonly string[]).includes('UserUsableGroups')
    )
    assert.ok(
      !(MODEL_GROUP_PAGE_KEYS as readonly string[]).includes('TopupGroupRatio')
    )
  })
})

describe('重名检测', () => {
  test('去空白后相同即算重名（写回时后一个键静默吃掉前一个）', () => {
    assert.deepEqual(
      duplicateRowNames([{ name: 'vip' }, { name: ' vip ' }, { name: 'free' }]),
      ['vip']
    )
  })

  test('空名不参与：它不会被写回，报出来只是噪音', () => {
    assert.deepEqual(duplicateRowNames([{ name: '  ' }, { name: '' }]), [])
  })
})

describe('auto 顺序', () => {
  test('顺序就是语义：上移下移逐项交换，不做任何排序', () => {
    assert.deepEqual(moveAutoGroup(['a', 'b', 'c'], 1, 'up'), ['b', 'a', 'c'])
    assert.deepEqual(moveAutoGroup(['a', 'b', 'c'], 1, 'down'), ['a', 'c', 'b'])
  })

  test('越界不动：首项上移 / 末项下移原样返回', () => {
    assert.deepEqual(moveAutoGroup(['a', 'b'], 0, 'up'), ['a', 'b'])
    assert.deepEqual(moveAutoGroup(['a', 'b'], 1, 'down'), ['a', 'b'])
  })

  test('往返保序：auto 从上往下试到第一个可用的分组为止', () => {
    const list = ['pool_b', 'pool_a']
    assert.deepEqual(parseAutoGroups(serializeAutoGroups(list)), list)
  })

  test('坏 JSON / 非数组回落成空清单，不抛异常', () => {
    assert.deepEqual(parseAutoGroups('{'), [])
    assert.deepEqual(parseAutoGroups('{"a":1}'), [])
    assert.deepEqual(parseAutoGroups('["a",1,"b"]'), ['a', 'b'])
  })
})
