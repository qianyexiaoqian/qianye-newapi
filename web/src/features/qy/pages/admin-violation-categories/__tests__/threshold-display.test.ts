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

import {
  qyEnforcementActionKey,
  qyThresholdStateKey,
} from '@/features/qy/lib/violation-thresholds'
import en from '@/i18n/qy/en.json'
import zh from '@/i18n/qy/zh.json'

/**
 * 阈值在界面上怎么说。
 *
 * 这一批断言守的是四件"不会让任何渲染测试变红、但会让用户读到假话"的事：
 *
 *  1. **三态塌成两态**。「还没配」与「配了但关着」在封号判定上等价，正因为等价
 *     才容易被写成同一个分支 —— 而那一塌就是项目方看到的现象：六个类型全显示
 *     一个 0，看起来像"0 次就封"，于是"到多少次封号"在界面上等于不存在。
 *  2. **未配阈值被说成 0 次**。用户端把 threshold=0 渲染成"到 0 次封号"是最坏的
 *     一种错法：它让一个根本没有门槛的类型看起来是最严的那一类。
 *  3. **处置动作被写死**。类型阈值只决定"几次"，越线之后是记录/限制/封号由分组
 *     策略档决定。写死"封号"在「仅记录」档下是一句吓人的假话。
 *  4. **未知动作吐出键名**。`policy_action` 是服务端下发的自由字符串，取到没登记
 *     的值时 i18next 会把键名原样吐到界面上。
 */

const repoRoot = join(
  dirname(fileURLToPath(import.meta.url)),
  '..',
  '..',
  '..',
  '..',
  '..',
  '..',
  '..'
)
const webSrc = join(repoRoot, 'web', 'src')
const zhKeys = zh as Record<string, string>
const enKeys = en as Record<string, string>

describe('阈值三态', () => {
  test('三态各自一个键，互不相同', () => {
    const cases: [string | undefined, string][] = [
      ['active', 'qy_vcat_threshold_value'],
      ['disabled', 'qy_vcat_threshold_disabled'],
      ['unset', 'qy_vcat_threshold_unset'],
      // 后端加了新态、前端还没跟上时，落到"还没配"这一档：它是三态里唯一
      // 不会让管理员以为"线正在生效"的那一个。
      [undefined, 'qy_vcat_threshold_unset'],
      ['something-new', 'qy_vcat_threshold_unset'],
    ]
    for (const [state, key] of cases) {
      assert.equal(qyThresholdStateKey(state), key, `state=${String(state)}`)
    }
    assert.notEqual(
      zhKeys.qy_vcat_threshold_unset,
      zhKeys.qy_vcat_threshold_disabled,
      '「还没配」与「配了但关着」显示成同一句话，管理员就分不出这一类要不要去配'
    )
  })

  test('"未配阈值"那句话里不能出现一个孤零零的 0', () => {
    // 这一条是项目方那句话的直接落点：显示 0 会被读成"0 次就封"。
    for (const [lang, text] of [
      ['zh', zhKeys.qy_vcat_threshold_unset],
      ['en', enKeys.qy_vcat_threshold_unset],
    ]) {
      assert.ok(
        !/\b0\b/.test(text),
        `${lang} 的"未配阈值"文案里出现了 0：它会被读成"0 次就封"，而实际语义是"这一类不计门槛"`
      )
    }
    assert.ok(zhKeys.qy_vcat_threshold_unset.includes('未配'))
    assert.ok(
      enKeys.qy_vcat_threshold_unset.toLowerCase().includes('not configured')
    )
  })

  test('列表列渲染走 threshold_state，不再自己从 enabled/threshold 推', () => {
    const page = readFileSync(
      join(
        webSrc,
        'features',
        'qy',
        'pages',
        'admin-violation-categories',
        'index.tsx'
      ),
      'utf8'
    )
    // 去掉全部空白再比对：这一段被 prettier 折成了四行，按原文匹配等于把
    // 断言绑在格式化结果上，下一次改缩进就红。
    const dense = page.replace(/\s+/g, '')
    assert.ok(
      dense.includes('qyThresholdStateKey(row.threshold_state,'),
      '阈值列必须用后端下发的三态。前端自己推的话，判定侧改了口径这里不会跟着改'
    )
    // 窗口必须一起传进去：不传的话「不限期限」的类型会渲染成
    // 「-1 小时内 3 次」——一个看起来像 bug、实际是口径丢失的数字。
    assert.ok(
      dense.includes(
        'qyThresholdStateKey(row.threshold_state,row.category.window_hours)'
      ),
      '阈值列没有把窗口传给文案选择器，「不限期限」会被渲染成 -1 小时'
    )
    assert.ok(
      !page.includes('row.category.enabled && row.category.threshold > 0'),
      '旧的两态推导还在：它把"还没配"与"配了但关着"塌成同一句话'
    )
  })
})

describe('处置动作的文案键', () => {
  test('三个已知取值各自成键；空串与未知值一律按最重的那一档读', () => {
    const cases: [string | undefined, string][] = [
      ['record', 'qy_vio_policy_action_record'],
      ['restrict', 'qy_vio_policy_action_restrict'],
      ['ban', 'qy_vio_policy_action_ban'],
      // 保守方向：把"会被封"说成"只记录"会让用户以为没事，
      // 那是这三个词里唯一不可接受的错法。
      ['', 'qy_vio_policy_action_ban'],
      [undefined, 'qy_vio_policy_action_ban'],
      ['qy_sentinel_unknown_action', 'qy_vio_policy_action_ban'],
    ]
    for (const [action, key] of cases) {
      assert.equal(qyEnforcementActionKey(action), key, `action=${String(action)}`)
    }
  })

  test('返回的键在 zh / en 里都存在 —— 否则界面上会出现键名本身', () => {
    for (const action of ['record', 'restrict', 'ban', 'nonsense', undefined]) {
      const key = qyEnforcementActionKey(action)
      assert.ok(zhKeys[key] != null && zhKeys[key] !== '', `zh 缺少 ${key}`)
      assert.ok(enKeys[key] != null && enKeys[key] !== '', `en 缺少 ${key}`)
    }
  })
})

describe('用户端公示的那一句话', () => {
  const card = readFileSync(
    join(
      webSrc,
      'features',
      'qy',
      'pages',
      'violations',
      'components',
      'categories-card.tsx'
    ),
    'utf8'
  )

  test('项目方要的四件事都在同一句里：类型名、我的次数、门槛、到了会怎样', () => {
    for (const lang of ['zh', 'en'] as const) {
      const text = (lang === 'zh' ? zhKeys : enKeys).qy_vio_cat_sentence
      for (const slot of ['{{title}}', '{{hit}}', '{{threshold}}', '{{action}}']) {
        assert.ok(
          text.includes(slot),
          `${lang} 的公示句缺少 ${slot}：项目方原话是「你违规了什么 XX 类型多少次，到多少次封号」，四件事缺一件这句话就答不完整`
        )
      }
    }
  })

  test('未配阈值走另一句，绝不套进"到 0 次封号"', () => {
    assert.ok(
      !zhKeys.qy_vio_cat_sentence_off.includes('{{threshold}}'),
      '"仅记录"那句里插了门槛占位符：threshold 恒为 0，渲染出来就是"到 0 次封号"'
    )
    assert.ok(zhKeys.qy_vio_cat_sentence_off.includes('仅记录'))
    assert.ok(
      enKeys.qy_vio_cat_sentence_off.toLowerCase().includes('recorded only')
    )
    // 组件必须真的分流，而不是把 0 也塞进主句。
    assert.ok(
      card.includes("item.threshold > 0") &&
        card.includes('qy_vio_cat_sentence_off'),
      '公示卡片没有为"未配阈值"分流'
    )
  })

  test('"会怎样"跟着 policy_action 走，不写死封号', () => {
    assert.ok(
      card.includes('qyEnforcementActionKey(data.policy_action)'),
      '处置动作必须来自后端下发的 policy_action：写死"封号"在「仅记录」档下是一句假话'
    )
  })

  test('公示卡片仍然只碰白名单字段 —— 一个内部字段都不能渗进来', () => {
    // 与 wiring.test.ts 的那条重复是刻意的：这次改动重写了整块渲染，
    // 而重写正是"顺手把 name 拿来当标题"最容易发生的时刻。
    for (const forbidden of [
      '.remark',
      '.key',
      'matched_terms',
      'match_snippet',
      'rule_name',
    ]) {
      assert.ok(
        !card.includes(forbidden),
        `公示卡片里出现了 ${forbidden}：内部匹配细节一旦渲染出去，等于把规则库送给刷子`
      )
    }
  })
})

describe('"到底几次封号"的最终判定规则写在管理端界面上', () => {
  // 判定侧的唯一出口是后端 anyReached：两条线各自独立，任一越过即触发。
  // 这条规则必须出现在**两页**：配单类型线的那一页，和配账号总量线的那一页。
  // 只写在其中一页，另一页的管理员就会把自己面前那个数字当成唯一那条线 ——
  // 「我明明设了 10 次，怎么第 3 次就封了」正是这么来的。
  // 两页的**口径**共享（OR），**措辞**不共享。
  //
  // 这两页一度用同一个 i18n 键，而那句话里有两个「本页」在处置策略档那一页上
  // 都是假的：「②「单类型线」就是本页每一类自己的阈值」（那一页没有任何按类型的
  // 阈值）、「本页只决定「几次」」（那一页恰恰就是选 record/restrict/ban 的地方）。
  // 照着它找的管理员会找不到按类型的阈值，同时会以为处置动作不在那里配。
  // 所以下面既钉住"两页都说了这件事"，也钉住"每一页说的是这一页的实情"。
  const pages: {
    label: string
    segs: string[]
    key: string
    /**
     * 另一条线在哪一页配。这一句是"照着能点"的部分。
     *
     * 期望值必须带上引号/「页」这样的**指路形状**，不能只写类型名：
     * 「②「单类型线」是每一个违规类型自己的阈值」里也有"违规类型"四个字，
     * 光搜词的话，把指路那一句整个删掉测试照样绿（这条实验做过，第一版就这么漏了）。
     */
    pointsTo: { zh: string; en: string }
  }[] = [
    {
      label: '单类型线',
      segs: ['features', 'qy', 'pages', 'admin-violation-categories', 'index.tsx'],
      key: 'qy_vcat_two_lines_note',
      pointsTo: { zh: '「违规处置策略」', en: 'violation enforcement policy' },
    },
    {
      label: '账号总量线',
      segs: [
        'features',
        'qy',
        'pages',
        'admin-violations',
        'components',
        'violation-ban-policies-tab.tsx',
      ],
      key: 'qy_vio_policy_two_lines_note',
      pointsTo: { zh: '「违规类型」页', en: 'violation categories page' },
    },
  ]

  for (const page of pages) {
    test(`${page.label}那一页把两条线的关系摆出来了`, () => {
      const source = readFileSync(join(webSrc, ...page.segs), 'utf8')
      assert.ok(
        source.includes(`t('${page.key}')`),
        `${page.label}页没有说明两条线的关系：管理员会把这一页的阈值当成唯一那条线`
      )
    })
  }

  test('两页各说各的，不许共用同一句', () => {
    const keys = pages.map((page) => page.key)
    assert.equal(
      new Set(keys).size,
      keys.length,
      '两页共用了同一句两条线说明 —— 那句话里的「本页」必然在其中一页上是假的'
    )
    for (const page of pages) {
      const source = readFileSync(join(webSrc, ...page.segs), 'utf8')
      for (const other of pages) {
        if (other.key === page.key) continue
        assert.ok(
          !source.includes(`t('${other.key}')`),
          `${page.label}页引用了另一页的那句说明（${other.key}）`
        )
      }
    }
  })

  test('每一页那句话都说清 OR，并指向另一条线真正配在哪一页', () => {
    for (const page of pages) {
      for (const lang of ['zh', 'en'] as const) {
        const dict = (lang === 'zh' ? zhKeys : enKeys) as Record<string, string>
        const text = dict[page.key]
        assert.ok(
          typeof text === 'string' && text !== '',
          `${lang}: ${page.key} 缺失`
        )
        // OR 语义。退化成"两条都要越过"就是一句谎话（后端 anyReached 是 OR）。
        assert.ok(
          lang === 'zh'
            ? text.includes('任一越过即触发')
            : text.toLowerCase().includes('either'),
          `${lang}/${page.label}: 两条线的关系不再写成"任一越过即触发"，而后端判定就是 OR`
        )
        // 另一条线配在哪一页必须点名 —— 这两页各只配得到其中一条，
        // 不说另一条在哪里，管理员就只能挨个页面翻。
        const want = page.pointsTo[lang]
        assert.ok(
          text.toLowerCase().includes(want.toLowerCase()),
          `${lang}/${page.label}: 没有指向另一条线真正配置的位置「${want}」`
        )
      }
    }
  })

  test('账号总量线那一页不许把按类型的阈值说成"在本页"', () => {
    // 这条单独立着，因为它是那次复用留下的具体伤口：
    // 「单类型线就是本页每一类自己的阈值」出现在一个根本没有类型列表的页面上。
    const text = (zhKeys as Record<string, string>)[
      'qy_vio_policy_two_lines_note'
    ]
    assert.ok(
      !text.includes('本页每一类'),
      '处置策略档那一页仍然写着"本页每一类自己的阈值"，而那一页没有任何按类型的阈值'
    )
    assert.ok(
      !text.includes('本页只决定'),
      '处置策略档那一页仍然写着"本页只决定几次"，而处置动作恰恰就在那一页配'
    )
  })
})
