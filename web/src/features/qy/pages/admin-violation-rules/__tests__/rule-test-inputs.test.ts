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
  qyRuleTestContentInputs,
  qyRuleTestInputs,
} from '../lib/rule-test'
import type {
  QyViolationMatchType,
  QyViolationPhase,
  QyViolationTestInput,
} from '../types'

/**
 * rule-test-inputs.test.ts —— 试跑面板「问什么」的前端回归。
 *
 * 项目方原话：「规则试跑，目的是测试这个规则的逻辑，当前还要填写：模型，分组，
 * 这样不对。应该：上下文，返回值，返回代码，上游返回文本等，根据当前规则匹配的。」
 *
 * 那三格对大多数规则都是错的问题：错误码规则要的是错误码，上游规则要的是上游
 * 正文与状态码，频率规则一段文本都不看；而模型和分组根本不参与内容匹配。
 *
 * 这里守住四件事：
 *
 *  1. 字段随规则走 —— 每种阶段 × 匹配方式渲染且只渲染它会读到的格子；
 *  2. 模型/分组降级成可选 —— 「能不能试跑」不看它们，界面上也不再与样本混排；
 *  3. 与后端逐字对齐 —— 后端 `TestInput*` / `TestOutcome*` / `TestScopeFail*`
 *     的每一个取值，前端都必须认识（新增一个而前端没跟上就变红）；
 *  4. 三态文案齐备 —— 少一条就会在界面上渲染成键名本身。
 */

const SRC = new URL('..', import.meta.url)
const REPO = new URL('../../../../../../../', import.meta.url)

function read(relative: string): string {
  return readFileSync(new URL(relative, SRC), 'utf-8')
}

/** 从后端源码里抠出一组常量的字面量取值。 */
function backendConsts(prefix: string): string[] {
  const go = readFileSync(
    new URL('qianye/modules/violation/api_admin.go', REPO),
    'utf-8'
  )
  const found = go.matchAll(
    new RegExp(`\\b${prefix}\\w+\\s*=\\s*"([^"]+)"`, 'g')
  )
  return [...found].map((m) => m[1]).sort()
}

describe('试跑输入跟着规则走', () => {
  const cases: {
    name: string
    phase: 'prompt' | 'reject_reason' | 'upstream_err'
    matchType:
      | 'error_code'
      | 'keyword'
      | 'regex'
      | 'request_rate'
      | 'status_code'
      | 'upstream_text'
    want: QyViolationTestInput[]
  }[] = [
    {
      name: 'prompt 关键词只问请求上下文',
      phase: 'prompt',
      matchType: 'keyword',
      want: ['request_text', 'model', 'group'],
    },
    {
      name: 'prompt 正则同样只问请求上下文',
      phase: 'prompt',
      matchType: 'regex',
      want: ['request_text', 'model', 'group'],
    },
    {
      name: '频率规则只问计数，一个文本框都不该出现',
      phase: 'prompt',
      matchType: 'request_rate',
      want: ['rate_count', 'model', 'group'],
    },
    {
      name: '上游正文规则问上游正文 + 拒绝原因 + 状态码',
      phase: 'upstream_err',
      matchType: 'upstream_text',
      want: ['upstream_text', 'reject_reason', 'status_code', 'model', 'group'],
    },
    {
      name: '上游阶段的关键词规则扫的是上游文本，不是请求上下文',
      phase: 'upstream_err',
      matchType: 'keyword',
      want: ['upstream_text', 'reject_reason', 'status_code', 'model', 'group'],
    },
    {
      name: '软违规阶段同样两段文本都要问',
      phase: 'reject_reason',
      matchType: 'regex',
      want: ['upstream_text', 'reject_reason', 'status_code', 'model', 'group'],
    },
    {
      name: '错误码规则问错误码，外加状态码作用域那一格',
      phase: 'upstream_err',
      matchType: 'error_code',
      want: ['error_code', 'status_code', 'model', 'group'],
    },
    {
      name: '状态码规则只问一次状态码，不重复',
      phase: 'upstream_err',
      matchType: 'status_code',
      want: ['status_code', 'model', 'group'],
    },
  ]

  for (const tc of cases) {
    test(tc.name, () => {
      assert.deepEqual(qyRuleTestInputs(tc.phase, tc.matchType), tc.want)
    })
  }

  test('AI 审核规则问结论/类型/置信度，不问任何文本', () => {
    // ai_review 读的是外部模型的结论，不是上下文本身。问一段文本等于把面板变成
    // 「我猜模型会怎么判」—— 而那正是这条规则唯一无法在本地复现的一步。
    assert.deepEqual(
      qyRuleTestInputs('prompt', 'ai_review' as QyViolationMatchType),
      ['ai_verdict', 'ai_category', 'ai_confidence', 'model', 'group']
    )
  })

  test('转发后异步阶段不问状态码', () => {
    // post_async 跑在异步 worker 上，手上没有上游响应。多摆一格填了也不生效的
    // 状态码输入，与这次改动要修的毛病同形。
    assert.deepEqual(
      qyRuleTestInputs(
        'post_async' as QyViolationPhase,
        'ai_review' as QyViolationMatchType
      ),
      ['ai_verdict', 'ai_category', 'ai_confidence', 'model', 'group']
    )
  })

  test('模型与分组永远不算内容输入', () => {
    // 「能不能跑」只看内容输入。把作用域算进去，一次本来跑得动的试跑就被锁死了 ——
    // 而它们留空的语义恰恰是「不限」。
    for (const tc of cases) {
      const content = qyRuleTestContentInputs(qyRuleTestInputs(tc.phase, tc.matchType))
      assert.ok(!content.includes('model'), `${tc.name}：模型不该算内容输入`)
      assert.ok(!content.includes('group'), `${tc.name}：分组不该算内容输入`)
      assert.ok(content.length > 0, `${tc.name}：这条规则一个内容输入都没有`)
    }
  })
})

describe('试跑面板与后端逐字对齐', () => {
  test('后端每一个试跑输入标识，前端都认识', () => {
    // 漏掉一个的表现是最坏的一种：后端会读它、界面从不问它，于是那一维度的规则
    // 在面板里永远显示「未命中」—— 一个看起来权威、实则只是没有输入的结论。
    const backend = backendConsts('TestInput')
    assert.ok(backend.length > 0, '没能从后端源码里读到 TestInput* 常量')
    const source = readFileSync(new URL('types.ts', SRC), 'utf-8')
    for (const id of backend) {
      assert.ok(
        source.includes(`'${id}'`),
        `types.ts 的 QyViolationTestInput 少了 ${id}`
      )
    }
    // 反向也钉住：前端多认一个后端根本不发的标识，同样是漂移。
    const all = new Set<string>()
    for (const phase of [
      'prompt',
      'upstream_err',
      'reject_reason',
      // AI 审核那一摊新加的阶段。这个字面量刻意不走 QY_VIOLATION_PHASES ——
      // 那个常量属于规则表单，而这里要断言的是「后端有的维度，试跑都问得到」。
      'post_async',
    ] as QyViolationPhase[]) {
      for (const matchType of [
        'keyword',
        'regex',
        'upstream_text',
        'error_code',
        'status_code',
        'request_rate',
        'ai_review',
      ] as QyViolationMatchType[]) {
        for (const id of qyRuleTestInputs(phase, matchType)) all.add(id)
      }
    }
    assert.deepEqual([...all].sort(), backend)
  })

  test('后端每一种结论与作用域闸都有文案', () => {
    const keys = [
      ...backendConsts('TestOutcome').map((v) => `qy_vio_test_outcome_${v}`),
      ...backendConsts('TestScopeFail').map((v) => `qy_vio_test_scope_fail_${v}`),
      ...backendConsts('TestInput').map((v) => `qy_vio_test_input_${v}`),
      'qy_vio_test_blank_inputs',
      'qy_vio_test_scope_desc',
      'qy_vio_test_model_optional',
      'qy_vio_test_group_optional',
    ]
    for (const key of keys) {
      assert.ok(key in zh, `zh.json 缺少 ${key}`)
      assert.ok(key in en, `en.json 缺少 ${key}`)
    }
  })
})

describe('试跑面板的渲染形状', () => {
  // 试跑面板已经从规则表单抽屉里搬进自己的文件（表单那份 1100 行里，试跑只是
  // 末尾一小段，而它现在要自带判据条与缺席维度说明）。断言跟着搬。
  const sheet = read('components/rule-tester.tsx')

  test('每一个内容格子各自条件渲染，而不是一个万能样本框', () => {
    for (const id of [
      'request_text',
      'upstream_text',
      'reject_reason',
      'status_code',
      'error_code',
      'rate_count',
      'ai_verdict',
      'ai_category',
      'ai_confidence',
    ]) {
      assert.ok(
        sheet.includes(`asks('${id}')`),
        `试跑面板没有按规则条件渲染 ${id} 这一格`
      )
    }
  })

  test('结论按三态渲染，不是一个 matched 布尔', () => {
    assert.ok(
      sheet.includes('qy_vio_test_outcome_${result.outcome}'),
      '结论仍在读 matched 布尔：「不在作用域」与「没命中」会长得一模一样'
    )
    assert.ok(
      sheet.includes("result.outcome === 'out_of_scope'"),
      '不在作用域时必须点名是哪一道闸'
    )
    assert.ok(
      sheet.includes('result.blank_inputs.length > 0'),
      '未命中时必须点名哪几格没填，否则管理员会去改一个本来就对的模式串'
    )
  })

  test('不在作用域时不发的字段一律发空，绝不用一段样本灌满两个通道', () => {
    // 一个上游样本被同时灌进请求上下文，会让 prompt 规则凭空「命中」——
    // 一个捏造出来的绿灯，比不给试跑更危险。
    assert.ok(
      sheet.includes("request_text: asks('request_text') ? requestText : ''"),
      '请求上下文没有按「这条规则读不读它」发送'
    )
    assert.ok(
      !sheet.includes('sample_text'),
      '面板还在发旧的 sample_text 万能样本框'
    )
  })
})
