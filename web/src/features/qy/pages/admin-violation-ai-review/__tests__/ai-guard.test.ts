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
  qyAiApplyProtocol,
  qyAiChannelToDraft,
  qyAiDraftToInput,
  qyAiGuardShownIds,
  QY_AI_PROTOCOL_DEFAULTS,
} from '../lib/ai-review'
import type { QyAiChannel } from '../types'

/**
 * ai-guard.test.ts —— 护栏模型(qwen3guard)这条审核路线的前端回归。
 *
 * 后端已经钉住了协议本身(qianye/modules/violation/aireview_guard_test.go)。
 * 这里钉的是**只有前端能弄坏**的三件事:
 *
 *  1. 新建渠道的默认档必须还是通用模型。零值一旦偏到护栏模型那一侧,
 *     每一个照着表单点下去的站点都会得到一个它没有部署的端点。
 *  2. 切协议时**不许覆盖运营已经填过的地址**。被一次下拉框切换悄悄改掉是
 *     最难察觉的一种数据丢失 —— 保存之后才发现,而原值已经没了。
 *  3. 通用模型渠道**不许提交**「有争议」档与两张类别清单。它们在那条路上
 *     没有意义,提交一个被忽略的值会让人照着界面回显去查一个不存在的功能。
 *  4. 两张类别清单的**空数组语义**:空 = 全启用 / 空 = 参考实现的三类。
 *     把它折成"空 = 什么都不选"会让每一个存量渠道的判定静默降档。
 *  5. 本地护栏的默认模型名必须带 `sileader/` 命名空间 —— Qwen3Guard 不在
 *     Ollama 官方库里,裸写 `qwen3guard:0.6b` 拉不到镜像,而拉不到的表现是
 *     每次调用 404 → fail-open →「审核开着但一次都没生效」。
 */

const guardChannel: QyAiChannel = {
  id: 9,
  name: '本地护栏',
  base_url: 'http://localhost:11434/v1',
  model: 'sileader/qwen3guard:0.6b',
  protocol: 'qwen3guard',
  guard_controversial: 'unsafe',
  guard_categories: [],
  guard_elevate: [],
  has_key: false,
  key_hint: '',
  key_bound_elsewhere: false,
  timeout_ms: 0,
  weight: 1,
  enabled: true,
  price_in_per_m: '0',
  price_out_per_m: '0',
  remark: '',
  updated_at: 0,
}

describe('审核协议的默认档', () => {
  test('新建渠道起手是通用模型 —— 那是这一列出现之前的唯一行为', () => {
    const draft = qyAiChannelToDraft()
    assert.equal(
      draft.protocol,
      'json_prompt',
      '零值偏到护栏模型那一侧,每一个照着表单点下去的站点都会得到一个它没有部署的本地端点'
    )
    assert.equal(draft.guard_controversial, '', '「有争议」档的零值是宽松档')
    assert.equal(draft.base_url, QY_AI_PROTOCOL_DEFAULTS.json_prompt.base_url)
  })

  test('后端没下发两张清单时不许变成 undefined', () => {
    // 一次接口回滚 / 一个旧版本的后端。`undefined.map` 是白屏,而这一页
    // 的复选框正是拿它去 includes 的。
    const legacy = { ...guardChannel } as Record<string, unknown>
    delete legacy.guard_categories
    delete legacy.guard_elevate
    const draft = qyAiChannelToDraft(legacy as unknown as QyAiChannel)
    assert.deepEqual(draft.guard_categories, [])
    assert.deepEqual(draft.guard_elevate, [])
  })

  test('本地护栏的默认模型名带 sileader 命名空间', () => {
    assert.equal(
      QY_AI_PROTOCOL_DEFAULTS.qwen3guard.model,
      'sileader/qwen3guard:0.6b',
      'Qwen3Guard 不在 Ollama 官方库里,裸写 qwen3guard:0.6b 拉不到镜像 —— ' +
        '而拉不到的表现是每次调用 404 → fail-open →「审核开着但一次都没生效」'
    )
  })

  test('编辑护栏渠道时原样回显协议与「有争议」档', () => {
    const draft = qyAiChannelToDraft(guardChannel)
    assert.equal(draft.protocol, 'qwen3guard')
    assert.equal(draft.guard_controversial, 'unsafe')
    assert.equal(draft.base_url, 'http://localhost:11434/v1')
  })
})

describe('切协议时的地址与模型名', () => {
  test('还停在另一种协议的出厂默认值上时才换', () => {
    const from = qyAiChannelToDraft() // json_prompt 的出厂默认
    const to = qyAiApplyProtocol(from, 'qwen3guard')
    assert.equal(to.protocol, 'qwen3guard')
    assert.equal(to.base_url, QY_AI_PROTOCOL_DEFAULTS.qwen3guard.base_url)
    assert.equal(to.model, QY_AI_PROTOCOL_DEFAULTS.qwen3guard.model)
  })

  test('运营已经填过的地址与模型名一个字符都不动', () => {
    const typed = {
      ...qyAiChannelToDraft(),
      base_url: 'https://guard.internal.example/v1',
      model: 'my-own-guard',
    }
    const to = qyAiApplyProtocol(typed, 'qwen3guard')
    assert.equal(
      to.base_url,
      'https://guard.internal.example/v1',
      '被一次下拉框切换悄悄改掉,是保存之后才发现、而原值已经没了的那种丢失'
    )
    assert.equal(to.model, 'my-own-guard')
    assert.equal(to.protocol, 'qwen3guard', '协议本身还是要换')
  })

  test('空着的地址与模型名按新协议填上默认值', () => {
    const blank = { ...qyAiChannelToDraft(), base_url: '  ', model: '' }
    const to = qyAiApplyProtocol(blank, 'qwen3guard')
    assert.equal(to.base_url, QY_AI_PROTOCOL_DEFAULTS.qwen3guard.base_url)
    assert.equal(to.model, QY_AI_PROTOCOL_DEFAULTS.qwen3guard.model)
  })

  test('换回通用模型时同样只换出厂默认值', () => {
    const guard = qyAiChannelToDraft(guardChannel)
    const back = qyAiApplyProtocol(guard, 'json_prompt')
    assert.equal(back.base_url, QY_AI_PROTOCOL_DEFAULTS.json_prompt.base_url)
    assert.equal(back.model, QY_AI_PROTOCOL_DEFAULTS.json_prompt.model)
  })
})

describe('提交体', () => {
  test('通用模型渠道不提交「有争议」档', () => {
    const draft = {
      ...qyAiChannelToDraft(),
      // 先在护栏档下选过严格,再切回通用 —— 草稿上还留着那个值。
      guard_controversial: 'unsafe' as const,
    }
    const body = qyAiDraftToInput(draft)
    assert.equal(body.protocol, 'json_prompt')
    assert.equal(
      body.guard_controversial,
      '',
      '通用模型那条路根本没有 Controversial 这一档;提交一个被忽略的值会让人' +
        '照着界面回显去查「为什么设了 unsafe 却没生效」'
    )
  })

  test('护栏渠道原样提交「有争议」档与协议', () => {
    const body = qyAiDraftToInput(qyAiChannelToDraft(guardChannel))
    assert.equal(body.protocol, 'qwen3guard')
    assert.equal(body.guard_controversial, 'unsafe')
  })

  test('通用模型渠道不提交两张类别清单', () => {
    const draft = {
      ...qyAiChannelToDraft(),
      guard_categories: ['violent'],
      guard_elevate: ['pii'],
    }
    const body = qyAiDraftToInput(draft)
    assert.equal(body.protocol, 'json_prompt')
    assert.deepEqual(body.guard_categories, [])
    assert.deepEqual(body.guard_elevate, [])
  })

  test('护栏渠道原样提交勾选过的类别子集', () => {
    const draft = {
      ...qyAiChannelToDraft(guardChannel),
      guard_categories: ['violent', 'jailbreak'],
      guard_elevate: ['jailbreak'],
    }
    const body = qyAiDraftToInput(draft)
    assert.deepEqual(body.guard_categories, ['violent', 'jailbreak'])
    assert.deepEqual(body.guard_elevate, ['jailbreak'])
  })

  test('空的类别清单原样提交空数组 —— 它的含义是「全启用」,不是「没选」', () => {
    const body = qyAiDraftToInput(qyAiChannelToDraft(guardChannel))
    assert.deepEqual(
      body.guard_categories,
      [],
      '把空折成九项全列出来会让「运营从来没碰过这一格」与「运营手动勾了全部九项」' +
        '在库里长得一样,而前者要跟着后端的默认走(将来加第十类时自动包含)'
    )
    assert.deepEqual(body.guard_elevate, [])
  })

  test('本地护栏渠道不带 api_key 键 —— 免鉴权服务本来就不填', () => {
    const body = qyAiDraftToInput(qyAiChannelToDraft(guardChannel))
    assert.equal(
      'api_key' in body,
      false,
      '带一个空串会被后端当成「显式清除密钥」,而这里只是没碰过那一格'
    )
  })
})

describe('文案', () => {
  /**
   * 这条在什么情况下会红:有人给界面加了一个 `t('qy_ai_...')` 却忘了往
   * qy 的 zh/en 里补键。i18next 找不到键时**回显键名本身**,而
   * `qy_ai_proto_guard_desc` 这一串在界面上看起来像一个还没写完的占位符,
   * 却不会报任何错。
   */
  test('页面用到的 qy_ai_ 键在 zh 与 en 里都存在', () => {
    const src = readFileSync(new URL('../index.tsx', import.meta.url), 'utf-8')
    const used = new Set(
      [...src.matchAll(/t\('(qy_ai_[a-z0-9_]+)'/g)].map((m) => m[1])
    )
    assert.ok(used.size > 20, '正则没抓到键说明它已经与代码写法脱节了')
    const zhKeys = zh as Record<string, string>
    const enKeys = en as Record<string, string>
    for (const key of used) {
      assert.ok(zhKeys[key], `zh 缺少 ${key}`)
      assert.ok(enKeys[key], `en 缺少 ${key}`)
    }
  })

  test('两条路线的说明必须真的说出差别,不能只是换个名字', () => {
    const zhKeys = zh as Record<string, string>
    for (const key of ['qy_ai_proto_json_desc', 'qy_ai_proto_guard_desc']) {
      assert.ok(
        zhKeys[key].length > 60,
        `${key} 太短 —— 项目方要求界面上「要说清区别」,` +
          '而成本与类型体系的差别一句话说不完'
      )
    }
    // 护栏那一段必须点出它最要紧的限制:类别是固定的,提示词改不动。
    assert.match(zhKeys.qy_ai_proto_guard_desc, /固定/)
    // 通用那一段必须点出它最要紧的代价:提示词每次都要付钱。
    assert.match(zhKeys.qy_ai_proto_json_desc, /token/)
  })
})

describe('两张类别清单的空数组语义', () => {
  const NINE = [
    'violent',
    'non_violent_illegal_acts',
    'sexual_content_or_sexual_acts',
    'pii',
    'suicide_and_self_harm',
    'unethical_acts',
    'politically_sensitive_topics',
    'copyright_violation',
    'jailbreak',
  ]
  const DEFAULT_ELEVATE = ['jailbreak', 'pii', 'suicide_and_self_harm']

  test('空清单显示成默认全勾,而不是一个都不勾', () => {
    assert.deepEqual(
      qyAiGuardShownIds([], NINE),
      NINE,
      '显示成"全不勾"会让运营以为这个渠道什么都不审 —— 而后端的空清单含义恰恰相反'
    )
    assert.deepEqual(qyAiGuardShownIds([], DEFAULT_ELEVATE), DEFAULT_ELEVATE)
  })

  test('非空清单原样显示,不与默认合并', () => {
    assert.deepEqual(qyAiGuardShownIds(['violent'], NINE), ['violent'])
  })

  test('第一次取消勾选得到显式的八项,而不是"只剩这一项"', () => {
    // 组件把 shown 交给 toggle,而 shown 在空清单时已经是九项 —— 这一步
    // 正是"隐式全启用"展开成"显式八项"的地方。直接对 draft.guard_categories
    // 做 filter 的话,第一次取消会从空数组过滤出空数组,界面上看起来什么都
    // 没发生,而下一次点勾又会变成"只启用这一项"。
    const shown = qyAiGuardShownIds([], NINE)
    const next = shown.filter((x) => x !== 'pii')
    assert.equal(next.length, 8)
    assert.equal(next.includes('pii'), false)
  })
})

/**
 * 前端读的接口键与后端下发的接口键必须是同一个字符串。
 *
 * 这一条防的是一次**改名**:护栏九类的对照表在列表响应里原本叫
 * `guard_categories`,与渠道上那一格(启用子集)重名,于是改成了
 * `guard_catalog`。两边只改一边的表现是对照表变成空数组 —— 界面上那一段
 * 直接消失,没有报错、没有裸键、typecheck 也全绿(`?? []` 兜住了)。
 * 而那张表正是运营判断"哪几类会落进兜底"的唯一入口。
 */
describe('接口键与后端同源', () => {
  const goSrc = readFileSync(
    fileURLToPath(
      new URL(
        '../../../../../../../qianye/modules/violation/api_admin_aireview.go',
        import.meta.url
      )
    ),
    'utf-8'
  )

  for (const key of [
    'guard_catalog',
    'guard_elevate_default',
    'guard_categories',
    'guard_elevate',
  ]) {
    test(`后端确实下发 ${key}`, () => {
      assert.ok(
        goSrc.includes(`"${key}"`),
        `后端 api_admin_aireview.go 里找不到 ${key} —— 前端会拿到 undefined`
      )
    })
  }
})
