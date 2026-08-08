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
import { dirname, join } from 'node:path'
import { describe, test } from 'node:test'
import { fileURLToPath } from 'node:url'

import en from '@/i18n/qy/en.json'
import zh from '@/i18n/qy/zh.json'

/**
 * 「指路文案不能指向已经不存在的地名」的机器校验。
 *
 * 上一轮把分组矩阵搬进「系统设置 → 计费与支付 → 用户分组」，并把「分组定价」
 * 改名为「模型分组定价」。改名与搬家本身不会让任何东西变红：站内那些写死了
 * 旧地名的句子（「系统设置 → 分组倍率」「矩阵页」）照常渲染，只是照着走的人
 * 到不了目的地 —— 与「配置项从界面上消失、而它管的事情还在生效」是同一种失败：
 * 代码全都在，只有文案没跟上，而且没有任何东西会红。
 *
 * 这条测试守两件事：
 *   1. qy 语言包里不再出现那两个死地名；
 *   2. 换掉的那句话真的**指名道姓**说清被拆掉的两个编辑器去了哪里 ——
 *      少了名字，运营只能靠猜，而这正是复核判定「做了但看起来没做」的判据。
 */

const enKeys = en as Record<string, string>
const zhKeys = zh as Record<string, string>

/**
 * 死地名。判据是**导航指向**而不是名词本身：
 *
 *   · 「分组倍率表」指的是 `options.GroupRatio` 这份数据，它没有改名，留着是对的；
 *   · 「系统设置 → 分组倍率」「Group Pricing」是菜单上的一项，它已经不叫这个了。
 *
 * 所以下面只匹配带箭头/带「页」的导航形态，以及已被整体撤销的「矩阵页」。
 */
const RETIRED_PLACE_NAMES: { pattern: RegExp; why: string }[] = [
  {
    pattern: /矩阵页/,
    why: '分组矩阵已整体搬进「系统设置 → 计费与支付 → 用户分组」，站内没有一个页面叫「矩阵页」',
  },
  {
    pattern: /→\s*分组倍率/,
    why: '菜单上那一项现在叫「模型分组」',
  },
  {
    pattern: /「分组倍率」(页|里|设置)/,
    why: '菜单上那一项现在叫「模型分组」',
  },
  {
    pattern: /→\s*Group (Pricing|Ratio|ratios?)\b/,
    why: 'that drawer item is now called Model groups',
  },
  {
    pattern: /\bmatrix page\b/,
    why: 'the matrix moved into System Settings → Billing & Payment → User group',
  },
  /*
    本轮下线的第三个菜单项。它被留在文案里的形状与 3cbf00b84 修过的那一次
    逐字相同：标题链接改指了「用户分组」，而正文还在让运营去找一个不存在的
    菜单项。加进守卫是为了让下一次同类下线在测试里被拦住，而不是在复核里。
  */
  {
    pattern: /用户分组可用的模型分组配置/,
    why: '计费与支付下面只有「用户分组」「模型分组」两项，第三项本轮已下线',
  },
  {
    pattern: /\bModel groups per user group\b/,
    why: 'that third billing item was retired; the matrix lives inside the User groups edit dialog',
  },
  {
    pattern: /模型分组定价/,
    why: '菜单上那一项现在叫「模型分组」',
  },
  {
    pattern: /\bModel Group Pricing\b/,
    why: 'that drawer item is now called Model groups',
  },
]

describe('语言包里的指路文案', () => {
  test('不再指向已经改名或搬走的地名', () => {
    const offenders: string[] = []
    // qy 自有包 + 上游 7 份 locale 一起扫：死地名同样会被写进上游那些
    // 「英文原文即键名」的句子里（本轮就改了三句），只扫 qy 会漏掉那一半。
    const tables: (readonly [string, Record<string, string>])[] = [
      ['qy/en', enKeys],
      ['qy/zh', zhKeys],
      ...LOCALES.map((lang) => [`locales/${lang}`, loadLocale(lang)] as const),
    ]
    for (const [lang, table] of tables) {
      for (const [key, value] of Object.entries(table)) {
        for (const { pattern, why } of RETIRED_PLACE_NAMES) {
          if (pattern.test(value)) {
            offenders.push(`${lang}.json ${key}: /${pattern.source}/ —— ${why}`)
          }
        }
      }
    }
    assert.deepEqual(
      offenders,
      [],
      `以下文案把人支向了一个已经不存在的地名：\n${offenders.join('\n')}`
    )
  })

  test('「模型分组」页上那块指路牌点名说清两个被拆掉的编辑器去了哪里', () => {
    // 只说「去用户分组页」是不够的：运营是按控件的名字找东西的，而这一页上
    // 那两个名字一个都不剩。名字缺席时，唯一能做的就是猜。
    for (const name of [
      '分组间覆盖',
      '特殊可用分组规则',
      '用户分组',
      // 「登记表」是内部数据模型的词（`qy_model_groups` 那张表），本轮起不出现
      // 在任何一句运营可见的文案里。这一栏要点的名是**界面上真的有的那个**。
      '模型分组',
    ]) {
      assert.ok(
        zhKeys.qy_group_pricing_moved_desc.includes(name),
        `zh 的 qy_group_pricing_moved_desc 没提到「${name}」`
      )
    }
    for (const name of [
      'Inter-group overrides',
      'Special usable group rules',
      'User group',
    ]) {
      assert.ok(
        enKeys.qy_group_pricing_moved_desc.includes(name),
        `en 的 qy_group_pricing_moved_desc 没提到 "${name}"`
      )
    }
  })

  test('「特殊可用分组规则」必须说成已经下线，而不是被范围接管', () => {
    // 上一轮这句话说的是「未设定范围的用户分组，它原有的 +: / -: 规则仍在照常
    // 生效」——那在当时是对的。本轮 `GroupSpecialUsableGroup` 已整体下线（后端
    // 结构、读取逻辑一并删除），文案若还停在旧说法，运营会继续去找一批**已经
    // 不存在**的存量规则，并且以为自己站上还躺着一堆看不见的差分。
    //
    // 断言两件事：不再承诺"仍在生效"，且明说它是「下线」而不是「被接管」。
    assert.doesNotMatch(zhKeys.qy_group_pricing_moved_desc, /仍在照常生效/)
    assert.match(zhKeys.qy_group_pricing_moved_desc, /整套下线/)
    assert.doesNotMatch(
      enKeys.qy_group_pricing_moved_desc,
      /no scope keep their/
    )
    assert.match(enKeys.qy_group_pricing_moved_desc, /retired entirely/)
  })

  test('空态与指路牌对够不着系统设置的管理员另有一句', () => {
    // 这一页的后端是 `AdminAuth`（role>=10），而 `/system-settings` 的前端守卫
    // 要求 role=100。普通管理员在旧路由 `/qy/admin/group-matrix` 上原地渲染这一页，
    // 对他们来说指向系统设置的那条链接必然 403 —— 空态是这一页在"站里一个用户
    // 分组都没有"时**唯一**的指引，指错了他在前端就没有出路。
    for (const table of [enKeys, zhKeys]) {
      for (const key of [
        'qy_group_matrix_axis_create_empty_no_access',
        'qy_group_matrix_axis_create_need_super',
      ]) {
        assert.ok(table[key] != null && table[key].trim() !== '', `缺 ${key}`)
        assert.match(table[key], /超级管理员|super admin/)
      }
    }
  })

  test('空态文案不以冒号结尾，带链接的那句才以冒号结尾', () => {
    // `..._hint` 后面紧跟一个 <Link>（见 user-group-list / axis-legend），冒号是
    // 它的一部分；`..._empty` 用在 QyPageBoundary 上，那里收不下链接
    // （emptyDescription 只吃字符串），复用带冒号的句子会渲染出一个悬空的冒号。
    for (const table of [enKeys, zhKeys]) {
      assert.match(table.qy_group_matrix_axis_create_hint, /[:：]$/)
      // 空态是唯一的出路，它必须自带路径，而不是只说「去别处建」。
      for (const key of [
        'qy_group_matrix_axis_create_empty',
        'qy_group_matrix_axis_create_empty_no_access',
      ]) {
        assert.doesNotMatch(table[key], /[:：]$/)
        // 新建入口本轮搬到「用户分组」页表格上方那个按钮上。旧断言要求这两句里
        // 出现「模型分组定价」—— 那既是一个已经不存在的菜单项，也把人支到了一张
        // **模型分组**的表上（在那里加一行只会多一个模型分组，用户分组表照旧是空的）。
        // 判据跟着入口走。
        assert.ok(
          /用户分组|User groups/.test(table[key]),
          `${key} 没有说去哪里新建用户分组`
        )
      }
    }
  })
})

/**
 * 上游语言包（英文原文即键名）的 7 语种覆盖。
 *
 * 上一轮的实际失败就是这一条：新文案只落了英文，中文界面上原样显示英文，
 * 被判定成「做了但看起来没做」。typecheck 与其余单测对此全部沉默。
 */
const LOCALE_DIR = join(
  dirname(fileURLToPath(import.meta.url)),
  '..',
  '..',
  '..',
  '..',
  'i18n',
  'locales'
)

const LOCALES = ['en', 'zh', 'zh-TW', 'fr', 'ru', 'ja', 'vi'] as const
const OTHER_LOCALES = LOCALES.filter((lang) => lang !== 'en')

function loadLocale(lang: string): Record<string, string> {
  const file = JSON.parse(
    readFileSync(join(LOCALE_DIR, `${lang}.json`), 'utf8')
  ) as { translation: Record<string, string> }
  return file.translation
}

/**
 * 只挑**整句**做「真的翻译过」的判定。
 *
 * 语言包里有大量合法的同形键：专有名词（`Anthropic`、`Stripe`、`API`）、
 * 示例 JSON、端口号、路径片段 —— 它们在每一种语言里都长得一样，拿"与英文相同"
 * 去判它们全是误报。而"忘了翻"这件事只发生在句子上：句子够长（≥6 个词），
 * 同形就只可能是把英文原文直接抄了过去。
 */
const SENTENCE_WORD_COUNT = 6
const isSentence = (key: string) =>
  key.trim().split(/\s+/).length >= SENTENCE_WORD_COUNT

describe('上游语言包（英文原文即键名）的 7 语种覆盖', () => {
  /**
   * 通用扫描，**不是白名单**。
   *
   * 上一轮的原始失败是「新句子只落了英文，中文界面上原样显示英文」。第一版守卫
   * 把当轮那几句写死成常量，于是下一轮再加一句新英文键时它照样全绿 —— 守的是
   * 那几句话，不是那条规则。这里改成：`en.json` 的**每一个**键都必须在另外 6 份
   * 语言包里存在且非空。新键总是先落 en，所以任何"只落英文"都必然在这里变红，
   * 而与本轮改了哪几句话无关。
   */
  test('en.json 里的每一个键，另外 6 个 locale 都有且非空', () => {
    const en = loadLocale('en')
    const enKeys = Object.keys(en)
    const missing: string[] = []
    for (const lang of OTHER_LOCALES) {
      const table = loadLocale(lang)
      for (const key of enKeys) {
        const value = table[key]
        if (value == null || value.trim() === '') {
          missing.push(`${lang}: ${key}`)
        }
      }
    }
    assert.deepEqual(
      missing,
      [],
      `这些键只落了英文，对应语言的界面上会夹一句英文：\n${missing.join('\n')}`
    )
  })

  test('整句不得原样留着英文原文', () => {
    const en = loadLocale('en')
    const sentences = Object.keys(en).filter(isSentence)
    // 这一条在句子上是硬约束，所以先确认样本量没有塌掉（正则写错时最容易发生）。
    assert.ok(sentences.length > 500, `整句样本只有 ${sentences.length} 条`)
    const untranslated: string[] = []
    for (const lang of OTHER_LOCALES) {
      const table = loadLocale(lang)
      for (const key of sentences) {
        if (table[key] === en[key]) untranslated.push(`${lang}: ${key}`)
      }
    }
    assert.deepEqual(
      untranslated,
      [],
      `这些整句在对应语言里就是英文原文：\n${untranslated.join('\n')}`
    )
  })
})
