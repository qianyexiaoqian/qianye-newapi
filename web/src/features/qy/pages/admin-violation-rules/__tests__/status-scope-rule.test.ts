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
  qyEmptyViolationRule,
  qyViolationRuleSchema,
  qyViolationRuleToForm,
  qyViolationRuleToPayload,
  type QyViolationRuleFormValues,
} from '../lib/rule-form'
import type { QyViolationRule } from '../types'

/**
 * status-scope-rule.test.ts —— 「状态码 + 错误正文」写成一条规则的前端回归。
 *
 * 项目方给的那条上游拒绝是 `status_code=400` **且**正文含
 * 「This content was flagged for possible cybersecurity risk」。在状态码作用域
 * 出现之前，匹配方式是单选：status_code 规则看不到正文、upstream_text 规则看不到
 * 状态码，只能拆成两条 —— 而两条规则会各自命中、各自计数、各自扣费。
 *
 * 前端这一侧要守住三件事：
 *
 *  1. 字段真的会被提交（漏在 payload 里 = 界面填了、后端收不到，静默失效）；
 *  2. prompt 阶段填了它必须当场报错（那一档状态码恒为 0，填了就永远不命中）；
 *  3. 历史规则没有这一列时读成空串，而不是 `undefined`（后者会让输入框从受控
 *     变成非受控，React 在下一次输入时把已填内容整段丢掉）。
 */

const SRC = new URL('..', import.meta.url)

function read(relative: string): string {
  return readFileSync(new URL(relative, SRC), 'utf-8')
}

function rule(patch: Partial<QyViolationRuleFormValues>) {
  return { ...qyEmptyViolationRule(), name: 'r', ...patch }
}

function issuePaths(values: QyViolationRuleFormValues): string[] {
  const parsed = qyViolationRuleSchema.safeParse(values)
  if (parsed.success) return []
  return parsed.error.issues.map((issue) => issue.path.join('.'))
}

describe('状态码作用域', () => {
  test('上游阶段可以同时配状态码与正文，这正是一条规则的目标形状', () => {
    const upstream = rule({
      phase: 'upstream_err',
      match_type: 'upstream_text',
      pattern: 'flagged for possible cybersecurity risk',
      status_scope: '400',
    })
    assert.deepEqual(issuePaths(upstream), [])
  })

  test('列表与区间都能通过校验', () => {
    for (const scope of ['400', '400,403', '400-499', '429,500-599', '']) {
      const values = rule({
        phase: 'upstream_err',
        match_type: 'upstream_text',
        pattern: 'denied',
        status_scope: scope,
      })
      assert.deepEqual(issuePaths(values), [], `作用域 ${scope} 不该被拒`)
    }
  })

  test('prompt 阶段填状态码作用域必须当场报错', () => {
    // 转发前还没有上游响应，状态码恒为 0。放行的话，管理员会得到一条保存成功、
    // 界面正常、线上一次都不会命中的规则 —— 而这类失效没有任何报错。
    const values = rule({
      phase: 'prompt',
      match_type: 'keyword',
      pattern: 'foo',
      status_scope: '400',
    })
    assert.ok(
      issuePaths(values).includes('status_scope'),
      'prompt 阶段配状态码作用域必须报在 status_scope 这一格上'
    )
  })

  test('prompt 阶段留空照常通过', () => {
    const values = rule({
      phase: 'prompt',
      match_type: 'keyword',
      pattern: 'foo',
      status_scope: '',
    })
    assert.deepEqual(issuePaths(values), [])
  })

  test('新建规则默认不限状态码', () => {
    assert.equal(qyEmptyViolationRule().status_scope, '')
  })

  test('字段真的会进请求体', () => {
    // 漏在 payload 里的表现是最坏的一种：界面上填了、保存成功、后端收到的是空串，
    // 于是这条规则在任何状态码上都命中 —— 与配置的意图正好相反。
    const payload = qyViolationRuleToPayload(
      rule({
        phase: 'upstream_err',
        match_type: 'upstream_text',
        pattern: 'denied',
        status_scope: '400',
      })
    )
    assert.equal(payload.status_scope, '400')
  })

  test('历史规则缺这一列时读成空串而不是 undefined', () => {
    // 后端 AutoMigrate 会给已有行回填空串，但一条从旧接口/旧缓存读来的规则可能
    // 整个字段都不存在。读成 undefined 会让输入框从受控变成非受控，
    // React 在下一次输入时把已填内容整段丢掉。
    const legacy = {
      ...qyViolationRuleToForm({} as QyViolationRule),
    }
    assert.equal(legacy.status_scope, '')
  })
})

describe('状态码作用域的界面与文案', () => {
  test('表单只在上游阶段渲染这一格', () => {
    const sheet = read('components/rule-form-sheet.tsx')
    assert.ok(
      sheet.includes("name='status_scope'"),
      '表单里没有状态码作用域字段 —— 后端能力存在但没人配得上'
    )
    assert.ok(
      sheet.includes("{phase !== 'prompt' && ("),
      'prompt 阶段必须隐藏这一格：摆一个填了也永不生效的格子只会让人以为自己配对了'
    )
  })

  test('中英文案齐备', () => {
    const keys = [
      'qy_vio_field_status_scope',
      'qy_vio_field_status_scope_desc',
      'qy_vio_err_status_scope_phase',
    ]
    for (const key of keys) {
      assert.ok(key in zh, `zh.json 缺少 ${key}`)
      assert.ok(key in en, `en.json 缺少 ${key}`)
    }
  })
})
