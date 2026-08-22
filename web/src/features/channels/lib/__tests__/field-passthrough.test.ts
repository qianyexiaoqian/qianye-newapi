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
// 字段透传开关的渠道类型闸门（同步自上游 e90a7c48e，上游没带测试）。
//
// 这几个开关决定「客户端传来的 service_tier / store / safety_identifier /
// inference_geo / speed 要不要原样转给上游」。闸门算错的后果是**静默**的：
// 该写的没写进 settings，管理员在界面上打开了开关、保存、回来一看还是关的；
// 不该写的写进去了，一个不认识这些字段的上游会因为多出来的参数直接 400。
//
// 判据按渠道类型逐格钉死，而不是抽象成「支持的集合」—— 集合本身就是被测对象。
// 从公开入口 transformFormDataToCreatePayload 进，不去碰私有的 buildSettingsJSON。
import assert from 'node:assert/strict'
import { describe, test } from 'node:test'

import { CHANNEL_TYPE_NEW_API } from '../../constants'
import {
  CHANNEL_FORM_DEFAULT_VALUES,
  transformFormDataToCreatePayload,
} from '../channel-form'

const PASSTHROUGH_KEYS = [
  'allow_service_tier',
  'disable_store',
  'allow_safety_identifier',
  'allow_include_obfuscation',
  'allow_inference_geo',
  'allow_speed',
  'claude_beta_query',
] as const

type PassthroughKey = (typeof PASSTHROUGH_KEYS)[number]

function settingsFor(type: number): Record<string, unknown> {
  const { channel } = transformFormDataToCreatePayload({
    ...CHANNEL_FORM_DEFAULT_VALUES,
    name: 'qy-passthrough-probe',
    type,
    key: 'sk-probe',
    models: 'gpt-4o',
    // 七个开关全部打开：这样「没出现在 settings 里」只可能是闸门挡的，
    // 不可能是因为表单本来就是 false。
    allow_service_tier: true,
    disable_store: true,
    allow_safety_identifier: true,
    allow_include_obfuscation: true,
    allow_inference_geo: true,
    allow_speed: true,
    claude_beta_query: true,
  })
  return JSON.parse(channel.settings ?? '{}') as Record<string, unknown>
}

const OPENAI = 1
const ANTHROPIC = 14
const CODEX = 57
const GATEWAY_58 = 58
const GATEWAY_59 = 59
const UNRELATED = 3 // Azure：一格都不该有

const expected: Record<number, PassthroughKey[]> = {
  [OPENAI]: [
    'allow_service_tier',
    'disable_store',
    'allow_safety_identifier',
    'allow_include_obfuscation',
    'allow_inference_geo',
  ],
  [ANTHROPIC]: [
    'allow_service_tier',
    'allow_inference_geo',
    'allow_speed',
    'claude_beta_query',
  ],
  [CODEX]: [
    'allow_service_tier',
    'disable_store',
    'allow_safety_identifier',
    'allow_include_obfuscation',
    'allow_inference_geo',
  ],
  // 58 / 59 / new-api 三个网关型渠道同时在 OpenAI 组与 Claude 组里，
  // 所以两边的字段都写；claude_beta_query 是唯一的例外 ——
  // 只有原生 Anthropic 适配器（type 14）支持强制 beta query。
  [GATEWAY_58]: [
    'allow_service_tier',
    'disable_store',
    'allow_safety_identifier',
    'allow_include_obfuscation',
    'allow_inference_geo',
    'allow_speed',
  ],
  [GATEWAY_59]: [
    'allow_service_tier',
    'disable_store',
    'allow_safety_identifier',
    'allow_include_obfuscation',
    'allow_inference_geo',
    'allow_speed',
  ],
  [CHANNEL_TYPE_NEW_API]: [
    'allow_service_tier',
    'disable_store',
    'allow_safety_identifier',
    'allow_include_obfuscation',
    'allow_inference_geo',
    'allow_speed',
  ],
  [UNRELATED]: [],
}

describe('field passthrough gating by channel type', () => {
  for (const [type, keys] of Object.entries(expected)) {
    test(`channel type ${type} writes exactly ${keys.length} passthrough flag(s)`, () => {
      const settings = settingsFor(Number(type))
      const present = PASSTHROUGH_KEYS.filter((key) => key in settings)
      assert.deepEqual(present, keys)
      for (const key of keys) {
        assert.equal(settings[key], true, `${key} must round-trip as true`)
      }
    })
  }

  test('flags left off are written as false, not omitted', () => {
    // 关掉的开关必须落成 false 而不是消失：后端读不到键时会回落默认值，
    // 「管理员显式关掉」与「这个渠道类型不支持」就分不开了。
    const { channel } = transformFormDataToCreatePayload({
      ...CHANNEL_FORM_DEFAULT_VALUES,
      name: 'qy-passthrough-off',
      type: CHANNEL_TYPE_NEW_API,
      key: 'sk-probe',
      models: 'gpt-4o',
      allow_service_tier: false,
      allow_speed: false,
    })
    const settings = JSON.parse(channel.settings ?? '{}') as Record<
      string,
      unknown
    >
    assert.equal(settings.allow_service_tier, false)
    assert.equal(settings.allow_speed, false)
  })
})
