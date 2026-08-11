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
  qyAiBpsToPercentText,
  qyAiChannelToDraft,
  qyAiDraftToInput,
  qyAiPercentTextToBps,
} from '../lib/ai-review'
import type { QyAiChannel } from '../types'

/**
 * ai-review.test.ts —— AI 审核页的前端回归。
 *
 * 三件事必须钉死,每一件漏掉都对应一个真实事故:
 *
 *  1. **抽样率的换算方向**。填错一个数量级 = 花错一个数量级的钱,
 *     而界面上 30% 与 0.3% 长得几乎一样。
 *  2. **密钥的三态**。"不动 / 清除 / 换新"压成两态时,每次编辑渠道
 *     都会静默清掉密钥,而清掉之后不可恢复。
 *  3. **默认值里绝不能出现任何密钥**,而且模型名不能是已停用的旧名。
 */

const SRC = new URL('..', import.meta.url)

function read(relative: string): string {
  return readFileSync(new URL(relative, SRC), 'utf-8')
}

describe('抽样率换算', () => {
  test('万分比 → 百分比文本', () => {
    assert.equal(qyAiBpsToPercentText(0), '0')
    assert.equal(qyAiBpsToPercentText(3000), '30')
    assert.equal(qyAiBpsToPercentText(10000), '100')
    assert.equal(qyAiBpsToPercentText(3050), '30.5')
    assert.equal(qyAiBpsToPercentText(1), '0.01')
  })

  test('百分比文本 → 万分比,并夹进 0..10000', () => {
    assert.equal(qyAiPercentTextToBps('30'), 3000)
    assert.equal(qyAiPercentTextToBps('30.5'), 3050)
    assert.equal(qyAiPercentTextToBps('100'), 10000)
    assert.equal(qyAiPercentTextToBps('200'), 10000, '超过 100% 夹到满值')
    assert.equal(qyAiPercentTextToBps('-5'), 0)
  })

  test('解析失败一律回 0 —— 那是不花钱的那一侧', () => {
    for (const bad of ['', '  ', 'abc', 'NaN', 'Infinity']) {
      assert.equal(
        qyAiPercentTextToBps(bad),
        0,
        `${JSON.stringify(bad)} 必须解析成 0(不抽样),绝不能落到全量送审`
      )
    }
  })

  test('两个方向往返自洽', () => {
    for (const bps of [0, 1, 500, 3000, 10000]) {
      assert.equal(qyAiPercentTextToBps(qyAiBpsToPercentText(bps)), bps)
    }
  })
})

describe('渠道密钥的三态', () => {
  const existing: QyAiChannel = {
    id: 1,
    name: 'deepseek',
    base_url: 'https://api.deepseek.com/v1',
    model: 'deepseek-v4-flash',
    has_key: true,
    key_hint: '****a1b2',
    timeout_ms: 0,
    weight: 1,
    enabled: true,
    price_in_per_m: '0.28',
    price_out_per_m: '0.42',
    remark: '',
    updated_at: 0,
  }

  test('没碰密钥格子时,请求体里根本不带 api_key 这个键', () => {
    const body = qyAiDraftToInput(qyAiChannelToDraft(existing))
    assert.equal(
      'api_key' in body,
      false,
      '带一个 undefined 也不行:后端按「字段缺失」判定「保持原密钥」,' +
        '而 JSON 序列化会把 undefined 整键丢掉、把 null 变成显式清除'
    )
  })

  test('显式填空串 = 清除密钥', () => {
    const draft = { ...qyAiChannelToDraft(existing), apiKey: '' }
    const body = qyAiDraftToInput(draft)
    assert.equal(body.api_key, '')
  })

  test('填了新密钥 = 换新', () => {
    const draft = { ...qyAiChannelToDraft(existing), apiKey: 'sk-new' }
    assert.equal(qyAiDraftToInput(draft).api_key, 'sk-new')
  })

  test('草稿绝不回填任何密钥', () => {
    assert.equal(
      qyAiChannelToDraft(existing).apiKey,
      null,
      '接口本来就不下发明文密钥;草稿里出现任何非 null 值都意味着有人在别处造了一份'
    )
  })
})

describe('新建渠道的默认值', () => {
  const draft = qyAiChannelToDraft()

  test('密钥留空,由项目方自己填', () => {
    assert.equal(draft.apiKey, null)
  })

  test('默认模型名是 2026-08 仍在服务的那个', () => {
    // deepseek-chat / deepseek-reasoner 已于 2026-07-24 停用,
    // 拿它们做默认值 = 新建的渠道开箱即不可用,而失败方向是静默放行。
    assert.equal(draft.model, 'deepseek-v4-flash')
    assert.equal(draft.base_url, 'https://api.deepseek.com/v1')
  })

  test('出厂不启用 —— 地址与密钥没填全之前不该有流量打出去', () => {
    assert.equal(draft.enabled, false)
  })
})

describe('源码里不许出现任何密钥', () => {
  test('本页与它的默认值都不含 sk- 开头的字面量', () => {
    for (const file of [
      'lib/ai-review.ts',
      'api.ts',
      'types.ts',
      'index.tsx',
    ]) {
      const src = read(file)
      assert.equal(
        /['"`]sk-[A-Za-z0-9]{8,}/.test(src),
        false,
        `${file} 里出现了疑似 API Key 的字面量 —— 密钥一律由项目方在管理端填写`
      )
    }
  })
})

describe('i18n 键齐全', () => {
  const enKeys = en as Record<string, string>
  const zhKeys = zh as Record<string, string>

  test('页面里用到的 qy_ai_ 键在中英两侧都存在', () => {
    const src = read('index.tsx')
    const used = new Set(src.match(/qy_ai_[a-z0-9_]+/g) ?? [])
    assert.ok(used.size > 0, '页面应当走 i18n 而不是写死中文')
    for (const key of used) {
      // 以 `_` 结尾的是模板字面量的前缀(`qy_ai_outcome_${...}`),
      // 它本身不是一个键。下一条用测试自己的清单把那一族补齐。
      if (key.endsWith('_')) continue
      assert.ok(key in zhKeys, `zh.json 缺少 ${key}`)
      assert.ok(key in enKeys, `en.json 缺少 ${key}`)
    }
  })

  test('六种结局的文案两侧都在', () => {
    // 结局键是拼出来的,上一条抓不到。逐个列出来不是重复劳动:漏掉任何一个,
    // 成本页那一列就会显示原始的 `qy_ai_outcome_timeout`,而 timeout 恰恰是
    // 运营最需要看懂的一行(它说明"审核挂了、请求都放行了")。
    for (const outcome of [
      'clean',
      'violation',
      'timeout',
      'bad_json',
      'upstream_error',
      'no_channel',
    ]) {
      const key = `qy_ai_outcome_${outcome}`
      assert.ok(key in zhKeys, `zh.json 缺少 ${key}`)
      assert.ok(key in enKeys, `en.json 缺少 ${key}`)
    }
  })

  test('导航标题两侧都在', () => {
    for (const key of [
      'qy_nav_a_violation_ai_review',
      'qy_sg_jp_a_violation_ai_review',
    ]) {
      assert.ok(key in zhKeys, `zh.json 缺少 ${key}`)
      assert.ok(key in enKeys, `en.json 缺少 ${key}`)
    }
  })
})
