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

import en from '@/i18n/qy/en.json'
import zh from '@/i18n/qy/zh.json'

import {
  qyAiApplyProtocol,
  qyAiChannelToDraft,
  qyAiDraftToInput,
  qyAiIsGuardProtocol,
  qyAiProtocolHasCategories,
} from '../lib/ai-review'
import {
  QY_AI_GRANITE_RISKS,
  type QyAiChannel,
  type QyAiProtocol,
} from '../types'

const PROTOCOLS: QyAiProtocol[] = [
  'json_prompt',
  'qwen3guard',
  'granite_guardian',
  'llama_guard',
]

function channel(over: Partial<QyAiChannel> = {}): QyAiChannel {
  return {
    id: 1,
    name: 'c',
    base_url: 'http://127.0.0.1:11434/v1',
    model: 'm',
    protocol: 'json_prompt',
    risk_name: '',
    guard_controversial: '',
    guard_categories: [],
    guard_elevate: [],
    group: '',
    prompt: '',
    prompt_source: 'default',
    block_message: '',
    has_key: false,
    key_hint: '',
    key_bound_elsewhere: false,
    timeout_ms: 3000,
    weight: 1,
    enabled: true,
    price_in_per_m: '0',
    price_out_per_m: '0',
    remark: '',
    updated_at: 0,
    ...over,
  } as QyAiChannel
}

describe('审核协议的前后端取值集', () => {
  // 少一档的代价不是"少一个选项":存着那一档的渠道在下拉里匹配不到任何
  // option,触发器显示空白、一动就被重置成第一项 —— 表现就是「已添加的渠道
  // 再也改不了协议」。granite_guardian 就是这么漏过一次的。
  test('每一档协议都有出厂默认地址与模型名', () => {
    for (const p of PROTOCOLS) {
      const draft = qyAiChannelToDraft(channel({ protocol: p }))
      assert.equal(draft.protocol, p)
      const next = qyAiApplyProtocol(qyAiChannelToDraft(), p)
      assert.ok(next.base_url.length > 0, `${p} 缺出厂地址`)
      assert.ok(next.model.length > 0, `${p} 缺出厂模型名`)
    }
  })

  test('每一档协议都有列表徽章与说明文案(中英各一份)', () => {
    const keys = [
      'qy_ai_proto_json',
      'qy_ai_proto_guard',
      'qy_ai_proto_granite',
      'qy_ai_proto_llama',
      'qy_ai_proto_json_short',
      'qy_ai_proto_guard_short',
      'qy_ai_proto_granite_short',
      'qy_ai_proto_llama_short',
      'qy_ai_proto_llama_desc',
    ]
    for (const k of keys) {
      assert.ok(k in zh, `zh 缺 ${k}`)
      assert.ok(k in en, `en 缺 ${k}`)
    }
  })

  // 护栏协议一律不发提示词 —— 界面据此整块不渲染提示词框。判错的表现是
  // 画出一个填了不生效的输入框,而运营会对着它以为判据换过了。
  test('三条护栏协议都算护栏,只有 json_prompt 不算', () => {
    assert.equal(qyAiIsGuardProtocol('json_prompt'), false)
    assert.equal(qyAiIsGuardProtocol('qwen3guard'), true)
    assert.equal(qyAiIsGuardProtocol('granite_guardian'), true)
    assert.equal(qyAiIsGuardProtocol('llama_guard'), true)
  })

  // 九类启用清单只对"一次给出多标签"的那两条生效。给 Granite 留一份
  // 等于给运营一个勾了不生效的开关。**与后端 guardProtocolHasCategories 一致。**
  test('只有一次多标签的协议画九类清单', () => {
    assert.equal(qyAiProtocolHasCategories('qwen3guard'), true)
    assert.equal(qyAiProtocolHasCategories('llama_guard'), true)
    assert.equal(qyAiProtocolHasCategories('granite_guardian'), false)
    assert.equal(qyAiProtocolHasCategories('json_prompt'), false)
  })

  test('Llama Guard 渠道的九类勾选会被提交,不会被当成 Granite 清掉', () => {
    const draft = qyAiChannelToDraft(
      channel({ protocol: 'llama_guard', guard_categories: ['violent'] })
    )
    assert.deepEqual(qyAiDraftToInput(draft).guard_categories, ['violent'])
  })
})

describe('Granite 的审核风险', () => {
  // 这一条是整个特性的地基:空串 = harm = 不发 system = 这一格出现之前的
  // 行为。任何把空串补成具体风险的写法都会让存量渠道在升级那一秒静默换掉
  // 判定口径,而界面上一切正常。
  test('未设置时是空串,提交出去仍是空串', () => {
    const draft = qyAiChannelToDraft(channel({ protocol: 'granite_guardian' }))
    assert.equal(draft.risk_name, '')
    assert.equal(qyAiDraftToInput(draft).risk_name, '')
  })

  test('选了风险就原样提交', () => {
    const draft = qyAiChannelToDraft(
      channel({ protocol: 'granite_guardian', risk_name: 'jailbreak' })
    )
    assert.equal(qyAiDraftToInput(draft).risk_name, 'jailbreak')
  })

  // 切走时必须提交空串,而不是留着一个被忽略的值 —— 留着的表现是
  // "表单里还写着 jailbreak,保存回来变空"的一帧。
  test('切到别的协议后不再提交风险名', () => {
    const base = qyAiChannelToDraft(
      channel({ protocol: 'granite_guardian', risk_name: 'jailbreak' })
    )
    for (const p of PROTOCOLS.filter((x) => x !== 'granite_guardian')) {
      assert.equal(qyAiDraftToInput({ ...base, protocol: p }).risk_name, '')
    }
  })

  // 选错风险的后果是**漏判**而不是报错,所以每一档都要有自己的说明 ——
  // 一个只有名字的下拉框在这里是不够的。
  test('每一档风险都有名字与逐档说明(中英各一份)', () => {
    for (const r of QY_AI_GRANITE_RISKS) {
      for (const k of [`qy_ai_risk_${r}`, `qy_ai_risk_${r}_hint`]) {
        assert.ok(k in zh, `zh 缺 ${k}`)
        assert.ok(k in en, `en 缺 ${k}`)
      }
    }
    assert.ok('qy_ai_f_risk' in zh)
    assert.ok('qy_ai_f_risk' in en)
  })

  // 拼写必须与后端 graniteRisks 的键一字不差:模板对 system 做的是精确
  // 匹配,拼错(比如 HuggingFace 那边的 jailbreaking)会静默落回 harm 档。
  test('风险名用 Ollama 那一套拼写', () => {
    assert.deepEqual(
      [...QY_AI_GRANITE_RISKS],
      [
        'harm',
        'jailbreak',
        'violence',
        'sexual_content',
        'social_bias',
        'profanity',
        'unethical_behavior',
      ]
    )
  })
})
