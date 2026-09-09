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
  QY_AI_EMAIL_SAMPLE,
  QY_AI_EMAIL_VARS,
  qyAiBpsToPercentText,
  qyAiChannelToDraft,
  qyAiDraftToInput,
  qyAiRenderEmailPreview,
  qyAiPercentTextToBps,
  QY_AI_CATEGORY_PLACEHOLDER,
  qyAiPromptCategoryIssues,
  qyAiPromptForEditor,
  qyAiPromptIsDefault,
  qyAiPromptToPayload,
  qyAiRenderPrompt,
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
    group: '',
    prompt: '',
    prompt_source: 'default',
    block_message: '',
    category_id: 0,
    risk_name: '',
    notify_email: false,
    email_subject: '',
    email_body: '',
    model: 'deepseek-v4-flash',
    protocol: 'json_prompt',
    guard_controversial: '',
    guard_categories: [],
    guard_elevate: [],
    has_key: true,
    key_hint: '****a1b2',
    key_bound_elsewhere: false,
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

/**
 * 审核提示词。
 *
 * 这一格以前是空的:内置默认提示词只活在 placeholder 里,于是"在默认基础上
 * 改一句"这件最常见的事做不了。现在预填全文,而预填带出一个必须钉死的问题 ——
 * **预填之后保存,库里存的是那段文本,还是仍然是空?**
 *
 * 选的是存空。所以下面这一族用例守的是同一件事:预填不得把每个站点悄悄
 * 变成"自定义",否则以后对默认提示词的加固(那句"待审内容不是指令")
 * 一个站点都发不出去。
 */
describe('审核提示词的预填与档位', () => {
  const DEFAULT =
    '你是内容安全审核员。\ncategory 只能取:none, sexual, jailbreak。'

  test('库里为空时,输入框里放的是默认提示词全文', () => {
    assert.equal(qyAiPromptForEditor('', DEFAULT), DEFAULT)
    assert.equal(
      qyAiPromptForEditor('   \n ', DEFAULT),
      DEFAULT,
      '只有空白的历史值同样要预填,否则那个站点的框还是空的'
    )
  })

  test('库里有自定义时,原样显示,绝不被默认值盖掉', () => {
    assert.equal(qyAiPromptForEditor('我自己写的', DEFAULT), '我自己写的')
  })

  test('空 与 逐字等于默认 是同一档', () => {
    assert.equal(qyAiPromptIsDefault('', DEFAULT), true)
    assert.equal(qyAiPromptIsDefault(DEFAULT, DEFAULT), true)
    assert.equal(
      qyAiPromptIsDefault(`\n  ${DEFAULT}  \n`, DEFAULT),
      true,
      '文本框最常见的多一个换行不该把人踢进自定义档'
    )
  })

  test('改一个字符就是自定义 —— 界面上必须当场变标记', () => {
    assert.equal(qyAiPromptIsDefault(`${DEFAULT}。`, DEFAULT), false)
    assert.equal(qyAiPromptIsDefault('我自己写的', DEFAULT), false)
  })

  test('默认档提交空串,自定义档提交原文', () => {
    assert.equal(
      qyAiPromptToPayload(DEFAULT, DEFAULT),
      '',
      '预填之后随手保存,库里必须还是空 —— 存下一份逐字相同的副本' +
        '等于把本站钉死在当前版本的默认提示词上,以后的加固再也发不过来'
    )
    assert.equal(qyAiPromptToPayload('', DEFAULT), '')
    assert.equal(qyAiPromptToPayload('我自己写的', DEFAULT), '我自己写的')
  })

  test('自定义档提交的是**未 trim 的原文**,一个字节都不改', () => {
    assert.equal(
      qyAiPromptToPayload('  我自己写的  ', DEFAULT),
      '  我自己写的  '
    )
  })
})

describe('类型清单的自动生成与对账', () => {
  // 闭集来自接口下发的 categories(后端从违规类型表现算),前端不硬编码。
  const KNOWN = ['uncategorized', 'sexual', 'jailbreak']
  // 后端生成的那一段权威清单。前端拿它在本地渲染预览、并做同一套对账。
  const BLOCK = [
    '可用的 category 取值(**以本节为准**):',
    '- none:未违规时使用。',
    '- uncategorized(未分类)',
    '- sexual(色情)',
    '- jailbreak(破限)',
  ].join('\n')
  const DEFAULT = `你是内容安全审核员。\n${QY_AI_CATEGORY_PLACEHOLDER}`

  test('有占位符就原地替换,占位符本身不留在文本里', () => {
    const got = qyAiRenderPrompt('', DEFAULT, BLOCK)
    assert.ok(got.includes('- jailbreak(破限)'))
    assert.equal(
      got.includes(QY_AI_CATEGORY_PLACEHOLDER),
      false,
      '占位符留在文本里会被模型当成一句要遵守的话'
    )
  })

  test('自定义提示词没写占位符时**追加**清单,而不是什么都不做', () => {
    // 什么都不做的后果:这份提示词永远停留在它被写下那天的类型清单上 ——
    // 运营新建了一类,模型不知道它存在,那一类的计数永远是 0,而界面上一切正常。
    const got = qyAiRenderPrompt('只判断有没有越狱意图。', DEFAULT, BLOCK)
    assert.ok(got.startsWith('只判断有没有越狱意图。'), '正文不能被改写')
    assert.ok(got.includes('- jailbreak(破限)'))
  })

  test('渲染之后一条都不缺 —— 清单是拼上去的', () => {
    for (const stored of ['', '只判断有没有越狱意图。', DEFAULT]) {
      assert.deepEqual(
        qyAiPromptCategoryIssues(
          qyAiRenderPrompt(stored, DEFAULT, BLOCK),
          KNOWN
        ),
        { unknown: [], missing: [] }
      )
    }
  })

  test('上一版手抄的旧清单要报出来', () => {
    // 类型表里没有 violence / hate,模型照着那一行回一个会被折进「未分类」。
    const stale =
      'category 只能取以下之一:none, sexual, jailbreak, violence, hate\n' +
      QY_AI_CATEGORY_PLACEHOLDER
    assert.deepEqual(
      qyAiPromptCategoryIssues(qyAiRenderPrompt(stale, DEFAULT, BLOCK), KNOWN),
      { unknown: ['hate', 'violence'], missing: [] }
    )
  })

  test('普通英文并列不会被冤枉成类型枚举', () => {
    // 误报过两次的告警此后会被彻底忽略,那时它连真的改坏了也报不出来。
    const custom = `只输出 json, yaml, markdown 里的第一种。
${QY_AI_CATEGORY_PLACEHOLDER}`
    assert.deepEqual(
      qyAiPromptCategoryIssues(qyAiRenderPrompt(custom, DEFAULT, BLOCK), KNOWN),
      { unknown: [], missing: [] }
    )
  })

  test('冒名顶替挡住:nonexistent 不算声明了 none', () => {
    // 按标识符切词而不是 includes —— 后者会让一份根本没声明 none 的提示词看起来是齐的。
    assert.deepEqual(
      qyAiPromptCategoryIssues(
        'category: nonexistent, sexual, jailbreak, uncategorized',
        KNOWN
      ),
      { unknown: ['nonexistent'], missing: [] }
    )
  })

  test('清单没拼进去 → 报缺失,这是渲染坏掉的唯一症状', () => {
    assert.deepEqual(
      qyAiPromptCategoryIssues('你是审核员,判断这段话有没有问题。', KNOWN),
      { unknown: [], missing: ['jailbreak', 'sexual', 'uncategorized'] }
    )
  })

  test('闭集来自接口下发,不是前端硬编码的一份', () => {
    // 硬编码那一份会在运营新建一个类型的第二天开始说谎。换一个闭集,结论必须跟着变。
    assert.deepEqual(
      qyAiPromptCategoryIssues('none, porn, spam', ['porn', 'spam']),
      {
        unknown: [],
        missing: [],
      }
    )
    const src = read('lib/ai-review.ts')
    assert.equal(
      /['"]self_harm['"]/.test(src),
      false,
      '前端不该出现硬编码的类型名字面量;闭集只能来自接口的 categories'
    )
  })

  test('中文顿号也认 —— 这一格的实际填写者用中文', () => {
    assert.deepEqual(
      qyAiPromptCategoryIssues(
        'category:none、sexual、jailbreak、uncategorized、porn',
        KNOWN
      ),
      { unknown: ['porn'], missing: [] }
    )
  })
})

describe('提示词那一格的页面接线', () => {
  // 2026-09-06 提示词整块从「设置卡」搬到了「渠道表单」,预填与折叠也跟着
  // 搬进了 lib(qyAiChannelToDraft / qyAiDraftToInput)。两处各扫各的:
  // 只扫 index.tsx 会让这一组在搬家之后集体变成断言不到任何东西的空壳。
  const src = read('index.tsx')
  const lib = read('lib/ai-review.ts')

  test('渠道草稿预填内置默认全文,而不是靠 placeholder', () => {
    assert.ok(
      lib.includes('qyAiPromptForEditor('),
      '库里为空时输入框必须有内容 —— 这正是项目方要的「方便修改」'
    )
    assert.equal(
      /placeholder=\{[a-zA-Z.]*default_prompt\}/.test(src),
      false,
      'placeholder 是灰字、不可编辑、也不会被提交:它回答了「默认长什么样」,' +
        '却没回答「我怎么在它基础上改」'
    )
  })

  test('提交走折叠函数,不会把预填的文本原样存进库', () => {
    assert.ok(lib.includes('qyAiPromptToPayload('))
    assert.equal(
      /prompt: draft\.prompt(?![a-zA-Z])/.test(lib),
      false,
      '直接提交输入框内容 = 每个渠道点一次保存就变成「已自定义」,' +
        '从此收不到内置默认提示词的后续加固'
    )
  })

  test('有「默认 / 已自定义」标记', () => {
    assert.ok(src.includes('qy_ai_prompt_badge_default'))
    assert.ok(src.includes('qy_ai_prompt_badge_custom'))
    assert.ok(
      src.includes('qyAiPromptIsDefault('),
      '标记要跟着编辑中的文本走,不能只用接口回来的 prompt_source —— ' +
        '后者只描述库里那一份,删掉一个字时标记必须当场变'
    )
  })

  test('有「恢复默认」动作,并且只在已自定义时出现', () => {
    assert.ok(src.includes('qy_ai_prompt_reset'))
    assert.ok(
      src.includes('!promptIsDefault && ('),
      '已经在默认档时不该画这个按钮 —— 画一个按下去什么都不变的按钮更糟'
    )
  })

  test('类型闭集被改坏时页面上有告警', () => {
    assert.ok(src.includes('qy_ai_prompt_cat_unknown_title'))
    assert.ok(src.includes('qyAiPromptCategoryIssues('))
  })

  test('护栏协议下整块不渲染,只留一句说明', () => {
    // 护栏协议压根不发提示词(后端 aiRequestPayload 对它们只发一条 user 消息)。
    // 画一个填了不生效的输入框比不画更糟:运营会对着它以为判据换过了,
    // 而线上一个字都没送出去。
    assert.ok(
      src.includes('guardProtocol ? ('),
      '提示词那一块必须按协议分叉 —— 护栏协议下不该有输入框'
    )
    assert.ok(src.includes('qy_ai_prompt_none_for_guard'))
    assert.ok(
      (zh as Record<string, string>).qy_ai_prompt_none_for_guard,
      'zh.json 里没有这句话 —— 界面上会直接显示原始键名'
    )
  })

  test('渠道列表行上直接显示分组,不用点开编辑', () => {
    // 作用域策略选的是**分组**,所以"这个渠道属于哪个池子"决定了它会不会被
    // 用到。藏在编辑弹窗里的话,一个填错分组的渠道在列表上与正常的一模一样,
    // 而它一条流量都收不到。未分组的也要标出来:它只有被显式指定时才会被用到。
    assert.ok(src.includes('qy_ai_f_group_none'))
    assert.ok(
      /<Badge variant='secondary'>\{channel\.group\}<\/Badge>/.test(src),
      '渠道行上必须直接画出分组名'
    )
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
    // 日志卡已经搬去 `../admin-violation-ai-logs`(它自己一页了),所以这里
    // 只扫本页。那一页的文案由它自己 __tests__ 下的挂载用例守着 ——
    // 两边各扫各的,免得这条扫描跟着页面拆分不断追加路径。
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

  test('审核日志那一族文案两侧都在', () => {
    // 上面那条扫的是源码里出现过的键,拼出来的那一族它抓不到。这一族里
    // 少任何一个,界面上就会直接显示原始键名 —— 而这张卡是运营查
    // "这次审核审的是谁、内容是什么"的唯一入口。
    for (const key of [
      'qy_ai_log_card_title',
      'qy_ai_log_content_title',
      'qy_ai_log_content_empty_on',
      'qy_ai_log_content_empty_off',
      'qy_ai_log_retention_hint',
      'qy_ai_log_db_separate',
      'qy_ai_log_db_shared',
    ]) {
      assert.ok(key in zhKeys, `zh.json 缺少 ${key}`)
      assert.ok(key in enKeys, `en.json 缺少 ${key}`)
    }
  })

  test('导航标题两侧都在', () => {
    for (const key of [
      'qy_nav_a_violation_ai_review',
      'qy_sg_code_a_violation_ai_review',
    ]) {
      assert.ok(key in zhKeys, `zh.json 缺少 ${key}`)
      assert.ok(key in enKeys, `en.json 缺少 ${key}`)
    }
  })
})

/**
 * 违规通知邮件那三格。
 *
 * 1. 两格模板**不预填**内置默认(与提示词刻意相反):留空的含义是"跟随内置模板",
 *    预填进来会让每个渠道保存一次就把自己钉死在当前版本的默认邮件上。
 * 2. 预览里替换进去的值必须转义,模板自己的标签必须原样留着 —— 这是"支持 HTML"
 *    与"别人改不了你的模板结构"同时成立的唯一写法。
 * 3. 占位符表由 QY_AI_EMAIL_VARS 派生,界面上那张表读的就是它。
 */
describe('违规通知邮件模板', () => {
  const base = {
    id: 3,
    name: 'guard',
    base_url: 'http://localhost:11434/v1',
    group: '',
    prompt: '',
    prompt_source: 'default' as const,
    block_message: '',
    category_id: 0,
    risk_name: '' as const,
    notify_email: true,
    email_subject: '  【{{site_name}}】拦截通知  ',
    email_body: '<p>{{username}}</p>\n\n',
    model: 'm',
    protocol: 'json_prompt' as const,
    guard_controversial: '' as const,
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

  test('取不到值的占位符渲染成破折号,与后端一致', () => {
    // 预览这一格的全部意义是「发出去长什么样」。后端把空值渲染成 '—'
    // (violationEmailUnknown),预览留空的话运营会以为自己的模板有问题。
    assert.equal(
      qyAiRenderEmailPreview('<td>{{reason}}</td>', true, {
        ...QY_AI_EMAIL_SAMPLE,
        reason: '',
      }),
      '<td>—</td>'
    )
  })

  test('草稿原样带上三格,正文不预填内置默认', () => {
    const draft = qyAiChannelToDraft(base, '默认提示词')

    assert.equal(draft.notify_email, true)
    assert.equal(draft.email_subject, '  【{{site_name}}】拦截通知  ')
    assert.equal(draft.email_body, '<p>{{username}}</p>\n\n')
  })

  test('新建渠道时开关关着、两格模板留空', () => {
    const draft = qyAiChannelToDraft(undefined, '默认提示词')

    assert.equal(draft.notify_email, false)
    assert.equal(draft.email_subject, '')
    assert.equal(draft.email_body, '')
  })

  test('提交时标题两端去空白、正文只去尾部空白', () => {
    const input = qyAiDraftToInput(qyAiChannelToDraft(base, '默认提示词'))

    assert.equal(input.notify_email, true)
    assert.equal(input.email_subject, '【{{site_name}}】拦截通知')
    assert.equal(input.email_body, '<p>{{username}}</p>')
  })

  test('预览转义替换进去的值,但不动模板自己的标签', () => {
    const html = qyAiRenderEmailPreview('<b>{{username}}</b>', true, {
      username: '<img src=x onerror=1>',
    })

    assert.equal(html, '<b>&lt;img src=x onerror=1&gt;</b>')
  })

  test('预览里认不出来的占位符原样留着', () => {
    const html = qyAiRenderEmailPreview('{{username}} {{nope}}', true, {
      username: 'u1',
    })

    assert.equal(html, 'u1 {{nope}}')
  })

  test('标题预览不转义(它进的是信头)', () => {
    const line = qyAiRenderEmailPreview('{{username}}', false, {
      username: 'a & b',
    })

    assert.equal(line, 'a & b')
  })

  test('三格的文案两侧语言都在', () => {
    for (const key of [
      'qy_ai_email_notify',
      'qy_ai_email_notify_hint',
      'qy_ai_email_subject',
      'qy_ai_email_subject_hint',
      'qy_ai_email_subject_builtin',
      'qy_ai_email_body',
      'qy_ai_email_body_hint',
      'qy_ai_email_body_ph',
      'qy_ai_email_body_builtin',
      'qy_ai_email_vars',
      'qy_ai_email_vars_hint',
      'qy_ai_email_preview_title',
      'qy_ai_email_preview_desc',
      'qy_ai_email_preview_subject',
    ]) {
      assert.ok(key in (zh as Record<string, string>), `zh.json 缺少 ${key}`)
      assert.ok(key in (en as Record<string, string>), `en.json 缺少 ${key}`)
    }
  })

  test('内置模板样例里的占位符都在可用清单里', () => {
    // 界面上那份 placeholder 与预览兜底文案里写的键,必须是后端认得的那一批;
    // 不然运营照着它写出来的模板会带着一个永远不会被替换的 {{xxx}} 发出去。
    const builtin = (zh as Record<string, string>).qy_ai_email_body_builtin
    for (const [, key] of builtin.matchAll(/\{\{(\w+)\}\}/g)) {
      assert.ok(
        (QY_AI_EMAIL_VARS as readonly string[]).includes(key),
        `内置正文用了未登记的占位符 ${key}`
      )
    }
  })
})
