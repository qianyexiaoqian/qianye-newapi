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
import { fileURLToPath } from 'node:url'

import en from '@/i18n/qy/en.json'
import zh from '@/i18n/qy/zh.json'

import {
  qyAppendGroup,
  qyGroupRuleSchema,
  qyNormalizeGroupName,
  qyRuleGroupNames,
  qyUnknownGroupNames,
} from '../lib/rule-form'
import type { QyTransferGroupOption } from '../types'

const OPTIONS: QyTransferGroupOption[] = [
  { name: 'default', ratio: 1, has_channels: true, public_usable: true },
  { name: 'vip', ratio: 0.8, has_channels: true, public_usable: false },
]

describe('未定义分组是软告警', () => {
  // 报得出来：打错一个字母会静默变成一条永不命中的规则，不告警运营只能靠肉眼
  // 对拼写。这一条钉住「告警真的算出来了」。
  test('规则引用的未定义分组会被算出来', () => {
    const names = qyRuleGroupNames('qingxin', 'agent,vip', 'allow_list')
    assert.deepEqual(names, ['qingxin', 'agent', 'vip'])
    assert.deepEqual(qyUnknownGroupNames(names, OPTIONS), ['qingxin', 'agent'])
  })

  // 通配符与 @self 不是分组名。把它们算进去，每条兜底规则都会挂两个假警报，
  // 而假警报比没有警报更糟：报错一次没人信，之后真的打错字也不会有人看。
  test('通配符与 @self 不算分组名', () => {
    const names = qyRuleGroupNames('*', '@self', 'allow_list')
    assert.deepEqual(names, [])
    assert.deepEqual(qyUnknownGroupNames(names, OPTIONS), [])
  })

  // 后端保存时会把名字折叠成小写。前端按原文比对的话，运营输入 VIP 就会被
  // 误标成未定义分组，而保存之后它显示的是 vip。
  test('大小写与空白按后端口径折叠后再比对', () => {
    const names = qyRuleGroupNames('  VIP  ', ' Default ', 'allow_list')
    assert.deepEqual(names, ['vip', 'default'])
    assert.deepEqual(qyUnknownGroupNames(names, OPTIONS), [])
  })

  // 名单类之外的策略不看 to_groups：后端 validateGroupRule 会把它清空，
  // 拿一份即将被丢掉的名单去报警等于凭空造警报。
  test('allow_all / deny_all 不看名单', () => {
    assert.deepEqual(qyRuleGroupNames('vip', 'agent', 'allow_all'), ['vip'])
    assert.deepEqual(qyRuleGroupNames('vip', 'agent', 'deny_all'), ['vip'])
  })

  // 闸门方向：未定义分组绝不能变成一道校验。历史分组（倍率表里已删、users 里
  // 还有人挂着）恰恰是最需要限制转出的一批账号，拦下来会让运营在最需要配置的
  // 时刻配不进去。后端同样只告警不拒绝。
  test('未定义分组照样能通过表单校验', () => {
    const parsed = qyGroupRuleSchema.safeParse({
      from_group: 'qingxin',
      policy: 'allow_list',
      to_groups: 'agent',
      enabled: true,
      remark: '',
    })
    assert.equal(parsed.success, true)
  })
})

describe('分组选择器', () => {
  // 从下拉选 vip、名单里已有 VIP：两者落库后是同一项（后端会折叠去重），
  // 再追加一次只会让运营看到一份自己删不干净的重复名单。
  test('追加时按归一后的名字去重', () => {
    assert.equal(qyAppendGroup('VIP', 'vip'), 'VIP')
    assert.equal(qyAppendGroup('vip', 'agent'), 'vip,agent')
    assert.equal(qyAppendGroup('', 'vip'), 'vip')
    assert.equal(qyAppendGroup('vip', '  '), 'vip')
  })

  test('归一口径与后端 groupname.Normalize 一致', () => {
    assert.equal(qyNormalizeGroupName('  VIP '), 'vip')
    assert.equal(qyNormalizeGroupName(''), '')
  })
})

describe('后端下发的字段必须真的有消费方', () => {
  /**
   * 本扩展反复出现的失败模式是「后端接口齐全、前端一行都没调」：字段下发了、
   * 类型也写好了，但页面根本没渲染它，于是接口看起来是对的、页面照旧是坏的。
   *
   * 这条断言直接读源码，比 TypeScript 强一层：`unknown_groups` 就算被删掉，
   * 只要没人读它，类型检查也不会报任何错。
   */
  const read = (relative: string) =>
    readFileSync(fileURLToPath(new URL(`../${relative}`, import.meta.url)), {
      encoding: 'utf8',
    })

  test('页面下拉取的是用户分组，不是模型分组', () => {
    const page = read('index.tsx')
    assert.ok(
      page.includes('user_group_options'),
      'from_group / to_groups 比的是 users.group —— 下拉必须取 user_group_options。' +
        '取 group_options(模型分组)会让运营配出一条永不命中的规则'
    )
    assert.ok(
      !page.includes('data?.group_options'),
      '模型分组清单不该再出现在这一页;它只留给违规规则的分组作用域'
    )
  })

  test('页面读了 unknown_groups 并把它交给矩阵与列表', () => {
    const page = read('index.tsx')
    assert.ok(
      page.includes('unknown_groups'),
      'index.tsx 没有读 unknown_groups —— 软告警成了一份零消费方的数据'
    )
    assert.ok(
      page.includes('unknownGroups={'),
      '矩阵没有拿到未定义分组，表头与行头就不会打黄标'
    )
    assert.ok(
      page.includes('qy_trg_group_source_note'),
      '页面必须说明分组在哪里定义，否则运营还是会在这一页找「新建分组」'
    )
  })

  test('表单用的是带元数据的下拉，不是裸 datalist', () => {
    const sheet = read('components/group-rule-form-sheet.tsx')
    assert.ok(
      !sheet.includes('<datalist'),
      '裸 datalist 只提示、不校验、不告警，正是这一轮要换掉的东西'
    )
    assert.ok(sheet.includes('ComboboxInput'), '分组输入必须是可搜索的下拉')
    assert.ok(
      sheet.includes('groupOptions') && sheet.includes('groupsProbeOk'),
      '下拉必须吃到后端下发的元数据，否则元数据下发了也没人看'
    )
    // 命名空间:这一页填的是**用户分组**(users.group)，与 from_group /
    // to_groups 的判定端同一套。填成模型分组的话，运营从下拉里挑出来的规则
    // 永不命中 —— 保存成功、界面正常、线上零命中，而且没有任何信号。
    assert.ok(
      sheet.includes('qyUserGroupOptionLabel'),
      '下拉必须按用户分组渲染;模型分组的倍率/渠道元数据不属于这一页'
    )
    assert.ok(
      sheet.includes('allowCustomValue'),
      '必须允许自由输入：历史分组不在候选清单里，但仍然要能配规则'
    )
  })

  test('矩阵表头与行头都会给未定义分组打标', () => {
    const card = read('components/group-matrix-card.tsx')
    const marks = card.match(/qy_trg_unknown_group_hint/g) ?? []
    assert.ok(
      marks.length >= 4,
      `表头与行头各需要「可见标记 + 读屏文字」两处，实际只有 ${marks.length} 处`
    )
  })
})

describe('i18n 键必须存在', () => {
  /**
   * zod 的 message 就是 i18n 键：`FormMessage` 对它调 `t()`，找不到就**原样
   * 返回裸 key**，管理员会在表单上看到 `qy_trg_err_from_required` 这种东西，
   * 足以让人认定整页坏了。
   *
   * 这四个键上一版全缺，而校验逻辑本身是对的 —— 正是「纯逻辑写对了、
   * 消费层没接上」那个形状。
   */
  test('schema 真正吐出来的报错键在 en/zh 两份里都有', () => {
    // 断言的是**校验真的失败时拿到的那个字符串**，而不是 schema 的内部结构：
    // 后者是实现细节，改一次 zod 版本就可能扫不到东西而静默变成永真断言。
    const emitted = new Set<string>()
    for (const invalid of [
      { from_group: '', to_groups: '', remark: '' },
      {
        from_group: 'x'.repeat(65),
        to_groups: 'g'.repeat(1025),
        remark: 'r'.repeat(256),
      },
    ]) {
      const parsed = qyGroupRuleSchema.safeParse({
        policy: 'allow_list',
        enabled: false,
        ...invalid,
      })
      assert.equal(parsed.success, false)
      for (const issue of parsed.error?.issues ?? []) emitted.add(issue.message)
    }

    assert.deepEqual(
      [...emitted].sort(),
      [
        'qy_trg_err_from_required',
        'qy_trg_err_group_too_long',
        'qy_trg_err_list_too_long',
        'qy_trg_err_remark_too_long',
      ],
      '表单的四条报错键必须原样吐出来，否则下面的存在性断言就是永真的'
    )
    for (const key of emitted) {
      assert.ok(key in en, `en.json 缺少 ${key} —— 管理员会看到裸 key`)
      assert.ok(key in zh, `zh.json 缺少 ${key} —— 管理员会看到裸 key`)
    }
  })

  // en 与 zh 键数必须相等：少一边就意味着某个语种下会漏出裸 key。
  test('en 与 zh 的键集合完全相同', () => {
    assert.deepEqual(Object.keys(en).sort(), Object.keys(zh).sort())
  })
})
