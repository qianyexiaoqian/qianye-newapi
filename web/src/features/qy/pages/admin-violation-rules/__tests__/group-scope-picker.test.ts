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
import { describe, test } from 'node:test'

import {
  qyNormalizeGroupName,
  qyUnknownGroupNames,
  type QyGroupOption,
} from '../../../lib/group-options'
import { qyNormalizeGroupName as qyTransferNormalizeGroupName } from '../../admin-transfer-group-rules/lib/rule-form'
import {
  qyAppendViolationGroupScope,
  qyEmptyViolationRule,
  qySplitViolationGroupScope,
  qyViolationRuleSchema,
} from '../lib/rule-form'

/**
 * group-scope-picker.test.ts —— 分组作用域快速选择的前端回归。
 *
 * 这一项要挡住的缺陷与本轮反复出现的那个同形：**配错了却没有任何信号**。
 * 分组作用域原来是一个裸文本框，打错一个字母，规则就静默挂在一个不存在的分组
 * 上 —— 保存成功、界面正常、线上永不命中。
 *
 * 因此下面三组断言分别钉住三件事：
 *   1. 拆分口径与后端 `splitList` 一致（跟错了会产生一片假警报）；
 *   2. 未定义分组是**软告警**，绝不能变成一道校验闸门；
 *   3. 清单拉不到时不许把人卡死，也不许假警报。
 */

const OPTIONS: QyGroupOption[] = [
  { name: 'default', ratio: 1, has_channels: true, public_usable: true },
  { name: 'vip', ratio: 0.8, has_channels: true, public_usable: false },
]

const SRC = new URL('..', import.meta.url)

function read(relative: string): string {
  return readFileSync(new URL(relative, SRC), 'utf-8')
}

describe('分组作用域的拆分口径', () => {
  // 后端 `qianye/modules/violation/rules.go` 的 splitList 只认逗号与换行。
  // 划转那边的 parseGroupList 额外认分号，两边确实不同 —— 跟错一边，一个名字
  // 里带分号的分组会在界面上被拆成两个（两个都标黄的假警报），后端存的却仍是
  // 原来那一个。
  test('只认逗号与换行，分号是分组名的一部分', () => {
    assert.deepEqual(qySplitViolationGroupScope('default, vip\nbatch'), [
      'default',
      'vip',
      'batch',
    ])
    assert.deepEqual(qySplitViolationGroupScope('a;b, c'), ['a;b', 'c'])
    assert.deepEqual(qySplitViolationGroupScope('  ,, '), [])
  })

  // 从下拉选 vip、名单里已有 VIP：后端按 groupname.Effective 折叠后是同一个
  // 分组，再追加一次只会让运营看到一份自己删不干净的重复名单。
  test('追加时按归一后的名字去重', () => {
    assert.equal(qyAppendViolationGroupScope('VIP', 'vip'), 'VIP')
    assert.equal(qyAppendViolationGroupScope('vip', 'batch'), 'vip,batch')
    assert.equal(qyAppendViolationGroupScope('', 'vip'), 'vip')
    assert.equal(qyAppendViolationGroupScope('vip', '   '), 'vip')
  })

  // 两页共用同一份归一实现。各写一份的表现是：同一个分组名在一页被标黄、
  // 在另一页不被标黄，而两页说的都是同一个后端口径。
  test('归一口径与划转分组规则页是同一份实现', () => {
    assert.equal(qyNormalizeGroupName, qyTransferNormalizeGroupName)
    assert.equal(qyNormalizeGroupName('  VIP '), 'vip')
    assert.equal(qyNormalizeGroupName(''), '')
  })
})

describe('未定义分组是软告警，不是闸门', () => {
  test('打错字的分组名会被算出来', () => {
    const names = qySplitViolationGroupScope('vip, vlp').map((entry) =>
      qyNormalizeGroupName(entry)
    )
    assert.deepEqual(qyUnknownGroupNames(names, OPTIONS), ['vlp'])
  })

  // 历史分组（倍率表里已删、users 里还有人挂着）恰恰是最需要被违规规则覆盖的
  // 那批账号。把未定义分组做成校验，会让运营在最需要配置的时刻配不进去，
  // 而后端 ValidateRule 同样只看长度、不看分组存不存在。
  test('未定义分组照样能通过表单校验', () => {
    const parsed = qyViolationRuleSchema.safeParse({
      ...qyEmptyViolationRule(),
      name: '历史分组限流',
      pattern: 'x',
      group_scope: 'ghost-group',
    })
    assert.equal(parsed.success, true)
  })

  // 清单为空有两种来源：拉取失败，以及站点真的一个分组都没定义。两种情况下
  // 逐个比对都会把每一个名字标成黄的 —— 一片假警报，比没有警报更糟。
  test('清单为空时不产生任何告警', () => {
    assert.deepEqual(qyUnknownGroupNames(['vip', 'vlp'], []), ['vip', 'vlp'])
    const sheet = read('components/rule-form-sheet.tsx')
    assert.ok(
      sheet.includes('groupOptions.length === 0'),
      '抽屉必须在清单为空时收起未定义分组告警，否则拉取失败会糊一屏假警报'
    )
  })
})

describe('下拉必须真的接上，且不能把人卡死', () => {
  /**
   * 本扩展反复出现的失败模式是「实现齐全、消费层没接上」。这几条直接读源码，
   * 比 TypeScript 强一层：下拉整块被删掉，类型检查一个错都不会报。
   */
  test('用的是可搜索的下拉，不是裸 datalist', () => {
    const sheet = read('components/rule-form-sheet.tsx')
    assert.ok(
      !sheet.includes('<datalist'),
      '裸 datalist 只提示、不校验、不告警，正是这一轮要换掉的东西'
    )
    assert.ok(sheet.includes('ComboboxInput'), '分组作用域必须有可搜索的下拉')
    assert.ok(
      sheet.includes('qyGroupOptionsQuery'),
      '清单必须来自共享的分组候选查询，而不是页面自己拼一份'
    )
    assert.ok(
      sheet.includes('qyGroupOptionLabel'),
      '下拉必须吃到倍率 / 渠道元数据，否则元数据下发了也没人看'
    )
  })

  test('清单的三种非正常状态各有各的提示', () => {
    const sheet = read('components/rule-form-sheet.tsx')
    for (const key of [
      'qy_vio_group_scope_loading',
      'qy_vio_group_scope_failed',
      'qy_vio_group_scope_empty',
    ]) {
      assert.ok(sheet.includes(key), `缺少「${key}」这一档的提示`)
    }
  })

  // 拉取失败时把输入禁掉，等于让运营配不了规则。文本框必须始终是可编辑的
  // 自由输入 —— 它同时也是历史分组唯一的录入方式。
  test('自由输入的文本框不随清单状态禁用', () => {
    const sheet = read('components/rule-form-sheet.tsx')
    assert.ok(
      sheet.includes("<Input placeholder='default,vip' {...field} />"),
      '分组作用域必须保留一个始终可编辑的文本框'
    )
    assert.ok(
      !/disabled=\{groupQuery/.test(sheet),
      '任何控件都不许因为清单拉不到而被禁用'
    )
  })
})
