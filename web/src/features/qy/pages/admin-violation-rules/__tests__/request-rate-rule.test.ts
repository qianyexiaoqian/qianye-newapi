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

import en from '@/i18n/qy/en.json'
import zh from '@/i18n/qy/zh.json'

import {
  QY_VIOLATION_GROUP_SCOPE_MODES,
  QY_VIOLATION_MATCH_TYPES,
  qyEmptyViolationRule,
  qyViolationRuleSchema,
  type QyViolationRuleFormValues,
} from '../lib/rule-form'

/**
 * 前端校验重写一遍后端的约束，不是为了替代后端（后端仍是唯一权威），而是因为
 * 这几条一旦漏掉，表现全都是**静默失效**：保存成功、界面正常、线上永不命中。
 * 这里锁的三条都属于那一类。
 */

function rule(patch: Partial<QyViolationRuleFormValues>) {
  // 名称是独立的必填项，补上它才能让断言只反映被测的那一条约束。
  return { ...qyEmptyViolationRule(), name: 'r', ...patch }
}

function issuePaths(values: QyViolationRuleFormValues): string[] {
  const parsed = qyViolationRuleSchema.safeParse(values)
  if (parsed.success) return []
  return parsed.error.issues.map((issue) => issue.path.join('.'))
}

describe('request_rate 规则的表单校验', () => {
  test('阈值必须是 1..1000000 的整数', () => {
    const base = {
      match_type: 'request_rate' as const,
      phase: 'prompt' as const,
    }
    assert.deepEqual(issuePaths(rule({ ...base, pattern: '60' })), [])
    assert.deepEqual(issuePaths(rule({ ...base, pattern: '1' })), [])

    // 阈值 0 会让每一个非流式请求都命中，包括计数设施故障时 fail-open 的那些。
    assert.ok(issuePaths(rule({ ...base, pattern: '0' })).includes('pattern'))
    assert.ok(issuePaths(rule({ ...base, pattern: '-1' })).includes('pattern'))
    assert.ok(
      issuePaths(rule({ ...base, pattern: '60/min' })).includes('pattern')
    )
    assert.ok(issuePaths(rule({ ...base, pattern: '' })).includes('pattern'))
    assert.ok(issuePaths(rule({ ...base, pattern: '6.5' })).includes('pattern'))
    assert.ok(
      issuePaths(rule({ ...base, pattern: '1000001' })).includes('pattern')
    )
  })

  test('只能挂在转发前阶段', () => {
    // 挂在上游阶段的频率规则照样会执行，却只数得到失败的请求 ——
    // 而采集方的请求绝大多数是成功的。
    const paths = issuePaths(
      rule({
        match_type: 'request_rate',
        phase: 'upstream_err',
        pattern: '60',
        action: 'record',
      })
    )
    assert.ok(paths.includes('phase'))
  })

  test('阈值校验不会误伤其它匹配方式', () => {
    // 反向断言：少了它，一个「对所有规则都要求 pattern 是数字」的实现
    // 同样能让上面的用例全绿。
    assert.deepEqual(
      issuePaths(
        rule({ match_type: 'keyword', phase: 'prompt', pattern: '违禁词' })
      ),
      []
    )
  })
})

describe('作用域方向', () => {
  test('新规则默认是 include，与后端的空值语义一致', () => {
    assert.equal(qyEmptyViolationRule().group_scope_mode, 'include')
  })

  test('只有 include / exclude 两个方向', () => {
    // 抄 transfer 那套四值策略枚举的话，allow_all 与 deny_all 会各自变成
    // 「空 include」和「把规则停用」的第二种写法。
    assert.deepEqual(QY_VIOLATION_GROUP_SCOPE_MODES, ['include', 'exclude'])
    assert.ok(
      issuePaths(
        rule({ group_scope: 'vip', group_scope_mode: 'deny_all' as never })
      ).includes('group_scope_mode')
    )
  })
})

describe('i18n 文案', () => {
  /**
   * 每一种匹配方式、每一个作用域方向都必须有下拉框标签，否则界面上会出现一条
   * 原样显示 key 的选项 —— 而它恰好是新增取值时最容易漏的一步。
   */
  const required = [
    ...QY_VIOLATION_MATCH_TYPES.map((v) => `qy_vio_match_${v}`),
    ...QY_VIOLATION_GROUP_SCOPE_MODES.map(
      (v) => `qy_vio_group_scope_mode_${v}`
    ),
    'qy_vio_field_rate_threshold',
    'qy_vio_field_rate_threshold_desc',
    'qy_vio_field_group_scope_mode',
    'qy_vio_field_group_scope_mode_desc',
    'qy_vio_err_rate_pattern',
    'qy_vio_err_rate_phase',
    'qy_vio_test_rate_desc',
    'qy_vio_test_rate_count',
    'qy_vio_rate_degraded_title',
    'qy_vio_rate_degraded_desc',
  ]

  test('en / zh 都齐全', () => {
    for (const key of required) {
      assert.ok(key in en, `en.json 缺少 ${key}`)
      assert.ok(key in zh, `zh.json 缺少 ${key}`)
    }
  })

  /**
   * 判据的三条局限必须写在管理员配置它的那一刻。少了它们，运营会把这条规则
   * 当成一堵墙 —— 而它加一行 "stream": true 就能绕过。
   */
  test('三条局限的文案都在，且如实点名', () => {
    const caveats = [
      'qy_vio_rate_caveat_title',
      'qy_vio_rate_caveat_stream',
      'qy_vio_rate_caveat_false_positive',
      'qy_vio_rate_caveat_nodes',
      'qy_vio_rate_caveat_ladder',
    ]
    for (const key of caveats) {
      assert.ok(key in en, `en.json 缺少 ${key}`)
      assert.ok(key in zh, `zh.json 缺少 ${key}`)
    }
    const strings = en as Record<string, string>
    assert.match(strings.qy_vio_rate_caveat_stream, /stream/i)
    assert.match(strings.qy_vio_rate_caveat_false_positive, /embedding/i)
    assert.match(strings.qy_vio_rate_caveat_nodes, /Redis/i)

    // 断链回归：文案齐全但表单里那一块被删掉，上面的断言照样全绿，
    // 而管理员一个字都看不到。这是本项目反复出现的形状，必须直接钉住引用。
    const sheet = readFileSync(
      new URL('../components/rule-form-sheet.tsx', import.meta.url),
      'utf-8'
    )
    for (const key of caveats) {
      assert.ok(sheet.includes(key), `规则表单没有引用 ${key}`)
    }
  })
})
